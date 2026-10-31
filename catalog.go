package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"ofam.dev/ochazuke"
	ocli "ofam.dev/ochazuke/cli"
)

// The catalog: o-gh as a family extension. Nouns:
//
//	pr          one pull request (row id pr/OWNER/REPO#N; --target also takes
//	            its URL or OWNER/REPO#N): list, mine, reviews, watched,
//	            ignored, show, open, open-files, copy-url, copy-branch, watch,
//	            unwatch, ignore, unignore, checkout, send-to-inbox, refresh
//	collection  Mine, My Reviews, Watched, Ignored (collection/ID): list,
//	            show, open
//	signal      the inbox signals: preview (o-gh signals --dry-run), push
//	            (o-gh signals)
//
// The PRs page (prsPage) is a split of the four collections' lists beside
// the selected PR's record; a host (mtm) mounts it from the catalog and
// fills each list from its source verb (pr.mine, ...), which read one
// cached poll.

// Version is the catalog's version.
const Version = "0.1.0"

const (
	prNoun         = "pr"
	collectionNoun = "collection"
	signalNoun     = "signal"
	pageID         = "gh"
	targetsID      = "gh/targets"
)

// The settings (o-gh settings set NAME VALUE; ~/.config/o-gh/settings.json).
const (
	setMine         = "pr.mine-query"
	setReviews      = "pr.reviews-query"
	setLimit        = "pr.limit"
	setIgnore       = "pr.ignore"
	setIgnoreChecks = "pr.ignore-checks"
	setMaxAge       = "poll.max-age"
)

const (
	defaultMine    = "is:pr is:open author:@me archived:false sort:updated-desc"
	defaultReviews = "is:pr is:open review-requested:@me archived:false sort:updated-desc"
)

// prColumns are a PR row's table.
var prColumns = []ochazuke.Column{
	{Title: "PR", Min: 12, Max: 36},
	{Title: "Title", Min: 16},
	{Title: "Author", Max: 16, Priority: 3},
	{Title: "CI", Width: 7, Priority: 1},
	{Title: "Review", Max: 18, Priority: 2},
	{Title: "Updated", Width: 7, Align: ochazuke.AlignRight, Priority: 4},
}

var collectionColumns = []ochazuke.Column{
	{Title: "Collection", Min: 10},
	{Title: "PRs", Align: ochazuke.AlignRight, Width: 4},
	{Title: "About", Priority: 1},
}

// listID is a collection's list on the page.
func listID(c string) string { return pageID + "/" + c }

// prsPage is the PRs page as the catalog declares it.
func prsPage() ocli.CatalogPage {
	p := ocli.CatalogPage{ID: pageID, Title: "PRs", Layout: ocli.LayoutSplit,
		Detail: &ocli.CatalogDetail{Fields: []string{
			"key", "title", "url", "state", "draft", "author", "repo", "head", "base", "ci", "checks",
			"review_decision", "reviews", "requested", "comments", "mergeable", "labels",
			"in", "watching", "ignored", "ignored_by", "stale", "updated_at", "created_at",
		}}}
	for _, c := range collections {
		p.Lists = append(p.Lists, ocli.CatalogList{ID: listID(c.ID), Noun: prNoun, Source: prNoun + "." + c.ID, Title: c.Title})
	}
	return p
}

// newRuntime builds the runtime: settings, nouns, verbs, key rows and the
// declared page. targets adds the shell's --target list.
func (t *Tool) newRuntime(targets bool) error {
	r := ochazuke.NewRuntime()
	r.Config().App = "o-gh"
	t.r = r
	for _, s := range []ochazuke.Setting{
		{Name: setMine, Type: ochazuke.SettingString, Default: defaultMine, Description: "The GitHub search behind Mine"},
		{Name: setReviews, Type: ochazuke.SettingString, Default: defaultReviews, Description: "The GitHub search behind My Reviews (user-review-requested:@me leaves out team requests)"},
		{Name: setLimit, Type: ochazuke.SettingInteger, Default: 50, Description: "PRs per search (1-100)"},
		{Name: setIgnore, Type: ochazuke.SettingList, Default: []string{}, Description: "Ignore rules: each is terms that must all match, from draft, repo:GLOB, author:GLOB, label:GLOB, base:GLOB, head:GLOB, title:TEXT (for example \"author:dependabot*\"); pr watch or pr unignore overrides a rule for one PR"},
		{Name: setIgnoreChecks, Type: ochazuke.SettingList, Default: []string{}, Description: "Check names (globs) CI leaves out: a failing optional check does not make a PR's CI fail"},
		{Name: setMaxAge, Type: ochazuke.SettingDuration, Default: 60 * time.Second, Description: "How long a poll answers the lists before the next one (o-gh signals always polls)"},
	} {
		if err := r.DeclareSetting(s); err != nil {
			return err
		}
	}
	if targets {
		var err error
		if t.targets, err = r.AddList(targetsID, nil, ochazuke.Columns(prColumns...)); err != nil {
			return err
		}
	}
	t.define()
	return ocli.DeclarePage(r, prsPage())
}

// define registers the nouns' verbs, parameters and key rows.
func (t *Tool) define() {
	r := t.r
	pr := r.Define(prNoun).Columns(prColumns...).
		Verb("list", t.listPRs, "List the PRs of a collection (mine, reviews, watched, ignored), or of all").Rows(prNoun).Fetch().Quiet()
	for _, c := range collections {
		pr.Verb(c.ID, t.collectionRows(c.ID), c.Title+": "+c.Description).Rows(prNoun).Fetch().Quiet()
	}
	pr.Verb("show", t.on(t.showPR), "Show one PR: its record, CI checks and reviews (read from the last poll, else fetched)").Fetch().
		Verb("open", t.on(t.openPR), "Open the PR in the browser").
		Verb("open-files", t.on(t.openFiles), "Open the PR's changed files in the browser").
		Verb("copy-url", t.on(t.copyURL), "Copy the PR's URL to the clipboard and answer it").
		Verb("copy-branch", t.on(t.copyBranch), "Copy the PR's head branch to the clipboard and answer it").
		Verb("watch", t.on(t.watch), "Watch the PR: it joins Watched, its CI changes become signals, and no ignore rule hides it").
		Verb("unwatch", t.on(t.unwatch), "Stop watching the PR").
		Verb("ignore", t.on(t.ignore), "Ignore the PR: it moves to Ignored and sends no signals").
		Verb("unignore", t.on(t.unignore), "Stop ignoring the PR, overriding an ignore rule that matches it").
		Verb("checkout", t.on(t.checkout), "Check the PR's branch out in DIR (default the current directory) with gh pr checkout").Positional("dir").Confirm("Check out {{label}} with gh pr checkout?").
		Verb("send-to-inbox", t.on(t.sendToInbox), "Send the PR to the mtm inbox as an action item (mtm action-item add --source gh)").Positional("why").
		Verb("refresh", t.refresh, "Poll GitHub now and answer each collection's count")
	// The catalog's key rows: a host mounting the PRs page answers them on
	// the rows. The palette, the CLI and bindings.json reach every verb.
	pr.Mode("normal").
		Bind("enter", ochazuke.Do(prNoun+".open")).
		Bind("y y", ochazuke.Do(prNoun+".copy-url")).
		Bind("w", ochazuke.Do(prNoun+".watch")).
		Bind("W", ochazuke.Do(prNoun+".unwatch")).
		Bind("d d", ochazuke.Do(prNoun+".ignore")).
		Bind("g i", ochazuke.Do(prNoun+".send-to-inbox")).
		Bind("g c", ochazuke.Do(prNoun+".checkout"))
	r.Define(collectionNoun).Columns(collectionColumns...).
		Verb("list", t.listCollections, "List the collections with their counts").Rows(collectionNoun).Fetch().Quiet().
		Verb("show", t.showCollection, "List one collection's PRs").Rows(prNoun).Fetch().
		Verb("open", t.openCollection, "Open a collection's search on github.com (Mine, My Reviews)")
	r.Define(signalNoun).
		Verb("preview", t.signalVerb(true), "What o-gh signals would send now: new review requests and CI changes since the last run (o-gh signals --dry-run)").Fetch().
		Verb("push", t.signalVerb(false), "Poll, and send each new review request and CI change to the mtm inbox (o-gh signals)")
	params := map[string]map[string]ochazuke.Parameter{
		prNoun + ".list": {
			"collection": {Type: "string", Enum: []string{"all", Mine, Reviews, Watched, Ignored}, Description: "The collection (default all: every PR once)"},
			"fresh":      {Type: "boolean", Description: "Poll now, not the cached poll"},
		},
		prNoun + ".checkout":      {"dir": {Type: "string", Description: "The repository's directory (default the current one)"}},
		prNoun + ".send-to-inbox": {"why": {Type: "string", Description: "Why it needs you (default: sent from o-gh: the PR)"}},
		collectionNoun + ".show":  {"fresh": {Type: "boolean", Description: "Poll now, not the cached poll"}},
	}
	for _, c := range collections {
		params[prNoun+"."+c.ID] = map[string]ochazuke.Parameter{"fresh": {Type: "boolean", Description: "Poll now, not the cached poll"}}
	}
	for key, p := range params {
		op := r.Operations[key]
		op.Parameters = p
		r.Operations[key] = op
	}
}

// targetOf is the PR an instance names: its Data (a row's id) or its id.
func targetOf(inst *ochazuke.Instance) (PRKey, error) {
	if inst == nil {
		return PRKey{}, errors.New("name a PR: --target URL, OWNER/REPO#N or pr/OWNER/REPO#N")
	}
	id, _ := inst.Data.(string)
	if id == "" {
		id = inst.ID
	}
	return parseKey(id)
}

// on runs a PR verb off the loop on the PR its instance names; a verb that
// changed what the lists show (changes) reloads the page after.
func (t *Tool) on(run func(ctx context.Context, cfg Config, k PRKey, args ochazuke.Arguments) (any, error)) ochazuke.AppHandler {
	return func(ctx ochazuke.Ctx) ochazuke.Result {
		k, err := targetOf(ctx.Instance)
		if err != nil {
			return ochazuke.Failed(err)
		}
		cfg, args := t.config(), ctx.Args
		return ochazuke.Pending(func(c context.Context) (any, error) {
			return run(c, cfg, k, args)
		}).Then(func(ochazuke.Ctx, any, error) {
			if t.changed != nil {
				t.changed()
			}
		})
	}
}

// reveal makes a --target row exist.
func (t *Tool) reveal(target string) error {
	k, err := parseKey(target)
	if err != nil {
		return err
	}
	return t.targets.SetItems([]ochazuke.ListItem{{ID: target, Label: k.String(), Noun: prNoun, Data: target}})
}

func boolArg(a ochazuke.Arguments, name string) bool {
	v, _ := a[name].(bool)
	return v
}

// rows is items as PR rows.
func (t *Tool) rows(items []Item) []ocli.Row {
	out := make([]ocli.Row, 0, len(items))
	for _, it := range items {
		out = append(out, prRow(it, t.now()))
	}
	return out
}

func (t *Tool) listPRs(ctx ochazuke.Ctx) ochazuke.Result {
	which := ochazuke.StringArg(ctx.Args, "collection", "all")
	if _, ok := collectionByID(which); !ok && which != "all" {
		return ochazuke.Failed(fmt.Errorf("collection %q: one of all, mine, reviews, watched, ignored", which))
	}
	cfg, fresh := t.config(), boolArg(ctx.Args, "fresh")
	return ochazuke.Pending(func(c context.Context) (any, error) {
		cs, _, err := t.collect(c, cfg, fresh)
		if err != nil {
			return nil, err
		}
		if which != "all" {
			return t.rows(cs[which]), nil
		}
		var all []Item
		seen := map[string]bool{}
		for _, col := range collections {
			for _, it := range cs[col.ID] {
				if !seen[it.URL] {
					seen[it.URL] = true
					all = append(all, it)
				}
			}
		}
		sortItems(all)
		return t.rows(all), nil
	})
}

// collectionRows is a collection's row source (pr.mine, ...).
func (t *Tool) collectionRows(id string) ochazuke.AppHandler {
	return func(ctx ochazuke.Ctx) ochazuke.Result {
		cfg, fresh := t.config(), boolArg(ctx.Args, "fresh")
		return ochazuke.Pending(func(c context.Context) (any, error) {
			cs, _, err := t.collect(c, cfg, fresh)
			if err != nil {
				return nil, err
			}
			return t.rows(cs[id]), nil
		})
	}
}

// item is the PR's item: from the current poll, else fetched alone.
func (t *Tool) item(ctx context.Context, cfg Config, k PRKey) (Item, error) {
	cs, _, err := t.collect(ctx, cfg, false)
	if err != nil {
		return Item{}, err
	}
	if it, ok := cs.find(k.URL()); ok && !it.Stale {
		return it, nil
	}
	p, err := poll(ctx, t.gh, Query{Watched: []PRKey{k}}, t.now())
	if err != nil {
		return Item{}, err
	}
	if len(p.Watched) == 0 {
		msg := k.String() + ": no such PR"
		if len(p.Errors) > 0 {
			msg += " (" + strings.Join(p.Errors, "; ") + ")"
		}
		return Item{}, errors.New(msg)
	}
	st, err := t.store.State()
	if err != nil {
		return Item{}, err
	}
	one := classify(p, st, cfg.Rules)
	if it, ok := one.find(k.URL()); ok {
		return it, nil
	}
	// Neither in a search nor marked: still the PR asked for.
	pr := p.Watched[0]
	return Item{PR: pr, CI: cfg.Rules.CI(pr)}, nil
}

// known is what o-gh knows of a PR without polling: the cached poll's row,
// else its key alone.
func (t *Tool) known(k PRKey) PR {
	var c cachedPoll
	if ok, _ := t.store.read(pollFile, &c); ok {
		for _, list := range [][]PR{c.Poll.Mine, c.Poll.Reviews, c.Poll.Watched} {
			for _, pr := range list {
				if pr.URL == k.URL() {
					return pr
				}
			}
		}
	}
	return PR{URL: k.URL(), Key: k.String(), Repo: k.Owner + "/" + k.Name, Number: k.Number}
}

func (t *Tool) mark(k PRKey) Mark {
	pr := t.known(k)
	return Mark{URL: k.URL(), Key: k.String(), Title: pr.Title, Repo: pr.Repo, Number: k.Number, At: t.now().UTC()}
}

func (t *Tool) showPR(ctx context.Context, cfg Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	return t.item(ctx, cfg, k)
}

func (t *Tool) openPR(ctx context.Context, _ Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	return map[string]string{"opened": k.URL()}, t.opener(ctx, k.URL())
}

func (t *Tool) openFiles(ctx context.Context, _ Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	u := k.URL() + "/files"
	return map[string]string{"opened": u}, t.opener(ctx, u)
}

func (t *Tool) copyURL(_ context.Context, _ Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	return map[string]any{"url": k.URL(), "copied": t.clip(k.URL()) == nil}, nil
}

func (t *Tool) copyBranch(ctx context.Context, cfg Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	pr := t.known(k)
	if pr.Head == "" {
		it, err := t.item(ctx, cfg, k)
		if err != nil {
			return nil, err
		}
		pr = it.PR
	}
	if pr.Head == "" {
		return nil, fmt.Errorf("%s: no head branch", k)
	}
	return map[string]any{"branch": pr.Head, "copied": t.clip(pr.Head) == nil}, nil
}

func (t *Tool) watch(_ context.Context, _ Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	m := t.mark(k)
	_, err := t.store.Change(func(st *State) error {
		st.Watched[m.URL] = m
		delete(st.Ignored, m.URL)
		return nil
	})
	return map[string]any{"pr": m.Key, "url": m.URL, "watching": true}, err
}

func (t *Tool) unwatch(_ context.Context, _ Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	_, err := t.store.Change(func(st *State) error {
		delete(st.Watched, k.URL())
		return nil
	})
	return map[string]any{"pr": k.String(), "url": k.URL(), "watching": false}, err
}

func (t *Tool) ignore(_ context.Context, _ Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	m := t.mark(k)
	_, err := t.store.Change(func(st *State) error {
		st.Ignored[m.URL] = m
		delete(st.Watched, m.URL)
		delete(st.Allowed, m.URL)
		return nil
	})
	return map[string]any{"pr": m.Key, "url": m.URL, "ignored": true}, err
}

// unignore drops the PR's ignore and, when a rule would still hide it,
// records the exception.
func (t *Tool) unignore(_ context.Context, cfg Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	m := t.mark(k)
	pr := t.known(k)
	rule, ruled := cfg.Rules.rule(pr)
	_, err := t.store.Change(func(st *State) error {
		delete(st.Ignored, m.URL)
		if ruled {
			st.Allowed[m.URL] = m
		}
		return nil
	})
	out := map[string]any{"pr": m.Key, "url": m.URL, "ignored": false}
	if ruled {
		out["overrides"] = rule.Text
	}
	return out, err
}

func (t *Tool) checkout(ctx context.Context, _ Config, k PRKey, args ochazuke.Arguments) (any, error) {
	dir := ochazuke.StringArg(args, "dir", "")
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, envOr("O_GH_GH", "gh"), "pr", "checkout", k.URL())
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh pr checkout %s in %s: %w: %s", k, dir, err, strings.TrimSpace(string(out)))
	}
	return map[string]string{"pr": k.String(), "dir": dir, "output": strings.TrimSpace(string(out))}, nil
}

func (t *Tool) sendToInbox(ctx context.Context, _ Config, k PRKey, args ochazuke.Arguments) (any, error) {
	pr := t.known(k)
	why := strings.TrimSpace(ochazuke.StringArg(args, "why", ""))
	if why == "" {
		why = strings.TrimSpace("sent from o-gh: " + k.String() + " " + pr.Title)
	}
	s := Signal{Kind: "sent", URL: k.URL(), Key: k.String(), Title: pr.Title, Why: why}
	if err := t.inbox(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (t *Tool) refresh(ochazuke.Ctx) ochazuke.Result {
	cfg := t.config()
	return ochazuke.Pending(func(c context.Context) (any, error) {
		cs, p, err := t.collect(c, cfg, true)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"polled": p.At, "viewer": p.Viewer}
		for _, col := range collections {
			out[col.ID] = len(cs[col.ID])
		}
		if len(p.Errors) > 0 {
			out["errors"] = p.Errors
		}
		if len(cfg.Rules.Problems) > 0 {
			out["rule_problems"] = cfg.Rules.Problems
		}
		return out, nil
	}).Then(func(ochazuke.Ctx, any, error) {
		if t.changed != nil {
			t.changed()
		}
	})
}

func (t *Tool) listCollections(ochazuke.Ctx) ochazuke.Result {
	cfg := t.config()
	return ochazuke.Pending(func(c context.Context) (any, error) {
		cs, _, err := t.collect(c, cfg, false)
		if err != nil {
			return nil, err
		}
		out := make([]ocli.Row, 0, len(collections))
		for _, col := range collections {
			n := len(cs[col.ID])
			out = append(out, ocli.Row{Noun: collectionNoun, ID: collectionNoun + "/" + col.ID, Label: col.Title,
				Data:  map[string]any{"id": col.ID, "title": col.Title, "description": col.Description, "count": n},
				Cells: []string{col.Title, fmt.Sprint(n), col.Description}})
		}
		return out, nil
	})
}

// collectionOf is the collection an instance (or --target) names.
func collectionOf(inst *ochazuke.Instance) (collection, error) {
	if inst == nil {
		return collection{}, errors.New("name a collection: --target collection/ID (mine, reviews, watched, ignored)")
	}
	id, _ := inst.Data.(string)
	if id == "" {
		id = inst.ID
	}
	id = id[strings.LastIndex(id, "/")+1:]
	c, ok := collectionByID(id)
	if !ok {
		return collection{}, fmt.Errorf("%q: not a collection (mine, reviews, watched, ignored)", id)
	}
	return c, nil
}

func (t *Tool) showCollection(ctx ochazuke.Ctx) ochazuke.Result {
	c, err := collectionOf(ctx.Instance)
	if err != nil {
		return ochazuke.Failed(err)
	}
	return t.collectionRows(c.ID)(ctx)
}

func (t *Tool) openCollection(ctx ochazuke.Ctx) ochazuke.Result {
	c, err := collectionOf(ctx.Instance)
	if err != nil {
		return ochazuke.Failed(err)
	}
	cfg := t.config()
	q := map[string]string{Mine: cfg.Query.Mine, Reviews: cfg.Query.Reviews}[c.ID]
	if q == "" {
		return ochazuke.Failed(fmt.Errorf("%s is o-gh's own: GitHub has no search for it", c.Title))
	}
	u := "https://github.com/pulls?q=" + url.QueryEscape(q)
	return ochazuke.Pending(func(cx context.Context) (any, error) {
		return map[string]string{"opened": u}, t.opener(cx, u)
	})
}

func (t *Tool) signalVerb(dry bool) ochazuke.AppHandler {
	return func(ochazuke.Ctx) ochazuke.Result {
		cfg := t.config()
		return ochazuke.Pending(func(c context.Context) (any, error) {
			return t.runSignals(c, cfg, dry)
		})
	}
}
