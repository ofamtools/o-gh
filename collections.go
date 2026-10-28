package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The collections: Mine (the mine search), My Reviews (the reviews
// search), Watched (the PRs watched in o-gh's state) and Ignored. A PR may
// be in Mine and Watched at once; an ignored PR is in Ignored only. A PR
// is ignored when o-gh's state ignores it, or when an ignore rule
// (pr.ignore) matches it and neither a watch nor an unignore overrides the
// rule: what a person said about one PR beats a rule.

// The collection ids, in page order.
const (
	Mine    = "mine"
	Reviews = "reviews"
	Watched = "watched"
	Ignored = "ignored"
)

// collection is one collection's names.
type collection struct {
	ID, Title, Description string
}

// collections are the four, in page order.
var collections = []collection{
	{Mine, "Mine", "Your open PRs and drafts (pr.mine-query)"},
	{Reviews, "My Reviews", "Open PRs that ask for your review (pr.reviews-query)"},
	{Watched, "Watched", "PRs you watch: pr watch; their CI changes are signals"},
	{Ignored, "Ignored", "PRs you ignored or an ignore rule (pr.ignore) matches: no signals"},
}

func collectionByID(id string) (collection, bool) {
	for _, c := range collections {
		if c.ID == id {
			return c, true
		}
	}
	return collection{}, false
}

// Rule is one ignore rule: terms that must all match. A term is
// repo:GLOB, author:GLOB, label:GLOB, base:GLOB, head:GLOB (globs: * is
// any text, / included, ? one character; case-insensitive), title:TEXT
// (a case-insensitive substring) or draft. A rule matches a PR when every
// term does.
type Rule struct {
	Text  string
	terms []term
}

type term struct{ field, value string }

// ruleFields are the fields a term may name.
var ruleFields = []string{"repo", "author", "label", "base", "head", "title"}

// parseRule reads one rule.
func parseRule(s string) (Rule, error) {
	r := Rule{Text: strings.TrimSpace(s)}
	for _, word := range strings.Fields(s) {
		if strings.EqualFold(word, "draft") {
			r.terms = append(r.terms, term{field: "draft"})
			continue
		}
		field, value, ok := strings.Cut(word, ":")
		field = strings.ToLower(field)
		if !ok || value == "" || !slices.Contains(ruleFields, field) {
			return Rule{}, fmt.Errorf("ignore rule %q: %q is not draft or FIELD:VALUE (%s)", r.Text, word, strings.Join(ruleFields, ", "))
		}
		r.terms = append(r.terms, term{field: field, value: strings.ToLower(value)})
	}
	if len(r.terms) == 0 {
		return Rule{}, fmt.Errorf("ignore rule %q has no terms", r.Text)
	}
	return r, nil
}

// Match reports whether every term matches pr.
func (r Rule) Match(pr PR) bool {
	for _, t := range r.terms {
		ok := false
		switch t.field {
		case "draft":
			ok = pr.Draft
		case "repo":
			ok = glob(t.value, pr.Repo)
		case "author":
			ok = glob(t.value, pr.Author)
		case "base":
			ok = glob(t.value, pr.Base)
		case "head":
			ok = glob(t.value, pr.Head)
		case "label":
			ok = slices.ContainsFunc(pr.Labels, func(l string) bool { return glob(t.value, l) })
		case "title":
			ok = strings.Contains(strings.ToLower(pr.Title), t.value)
		}
		if !ok {
			return false
		}
	}
	return true
}

// Rules are the ignore settings: PR rules (pr.ignore) and the check names
// whose state CI leaves out (pr.ignore-checks, globs).
type Rules struct {
	Ignore       []Rule
	IgnoreChecks []string
	// Problems are rules that did not parse (and are skipped).
	Problems []string
}

// parseRules reads the settings' rule lists; a bad rule is skipped and
// named in Problems.
func parseRules(ignore, ignoreChecks []string) Rules {
	var rs Rules
	for _, s := range ignore {
		if strings.TrimSpace(s) == "" {
			continue
		}
		r, err := parseRule(s)
		if err != nil {
			rs.Problems = append(rs.Problems, err.Error())
			continue
		}
		rs.Ignore = append(rs.Ignore, r)
	}
	for _, g := range ignoreChecks {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		rs.IgnoreChecks = append(rs.IgnoreChecks, strings.ToLower(g))
	}
	return rs
}

// rule is the first rule matching pr, if any.
func (rs Rules) rule(pr PR) (Rule, bool) {
	for _, r := range rs.Ignore {
		if r.Match(pr) {
			return r, true
		}
	}
	return Rule{}, false
}

// checkIgnored reports whether CI leaves a check out.
func (rs Rules) checkIgnored(name string) bool {
	for _, g := range rs.IgnoreChecks {
		if glob(g, name) {
			return true
		}
	}
	return false
}

// CI is a PR's CI state from its checks, the ignored checks left out:
// fail when any counted check failed, else pending when any is running,
// else pass when any passed, else none. With no checks at all it is the
// rollup's state (none without one).
func (rs Rules) CI(pr PR) string {
	if len(pr.Checks) == 0 {
		return rollupState(pr.Rollup)
	}
	var fail, pending, pass bool
	for _, c := range pr.Checks {
		if rs.checkIgnored(c.Name) {
			continue
		}
		switch c.State {
		case "fail":
			fail = true
		case "pending":
			pending = true
		case "pass":
			pass = true
		}
	}
	switch {
	case fail:
		return "fail"
	case pending:
		return "pending"
	case pass:
		return "pass"
	}
	return "none"
}

// glob reports whether s matches pattern (lower case): * is any text, ?
// one character, everything else itself; case-insensitive.
func glob(pattern, s string) bool {
	re := "^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(pattern)) + "$"
	ok, _ := regexp.MatchString(re, strings.ToLower(s))
	return ok
}

// Item is a PR in the collections: the PR, where it is and why.
type Item struct {
	PR
	CI string   `json:"ci"`
	In []string `json:"in"` // the collections (Ignored alone when ignored)
	// Found are the searches that answered the PR (mine, reviews), ignored or not.
	Found     []string `json:"found,omitempty"`
	Watching  bool     `json:"watching"`
	Ignored   bool     `json:"ignored"`
	IgnoredBy string   `json:"ignored_by,omitempty"` // "you" or the rule's text
	// Stale is set when the poll did not answer the PR (a watched or
	// ignored PR it did not fetch, or one that is gone): the row is what
	// o-gh's state recorded.
	Stale bool `json:"stale,omitempty"`
}

// Collections are the classified PRs, by collection id.
type Collections map[string][]Item

// classify sorts a poll into the collections.
func classify(p Poll, st State, rs Rules) Collections {
	out := Collections{Mine: {}, Reviews: {}, Watched: {}, Ignored: {}}
	seen := map[string]*Item{}
	var order []string
	add := func(pr PR, in string, stale bool) {
		it, ok := seen[pr.URL]
		if !ok {
			it = &Item{PR: pr, CI: rs.CI(pr), Stale: stale}
			seen[pr.URL] = it
			order = append(order, pr.URL)
		}
		if in != "" && !slices.Contains(it.In, in) {
			it.In = append(it.In, in)
			it.Found = append(it.Found, in)
		}
	}
	for _, pr := range p.Mine {
		add(pr, Mine, false)
	}
	for _, pr := range p.Reviews {
		add(pr, Reviews, false)
	}
	for _, pr := range p.Watched {
		add(pr, "", false)
	}
	// What the state names but the poll did not answer is shown as recorded.
	for _, m := range st.marks() {
		if _, ok := seen[m.URL]; !ok {
			pr := m.stub()
			if m.Title == "" && slices.Contains(p.Asked, m.URL) {
				pr.Title = "(not found on GitHub)"
			}
			add(pr, "", true)
		}
	}
	for _, url := range order {
		it := seen[url]
		_, it.Watching = st.Watched[url]
		_, explicit := st.Ignored[url]
		_, allowed := st.Allowed[url]
		switch {
		case explicit:
			it.Ignored, it.IgnoredBy = true, "you"
		case !it.Watching && !allowed:
			if r, ok := rs.rule(it.PR); ok {
				it.Ignored, it.IgnoredBy = true, r.Text
			}
		}
		if it.Watching && !slices.Contains(it.In, Watched) {
			it.In = append(it.In, Watched)
		}
		if it.Ignored {
			it.In = []string{Ignored}
		}
		if len(it.In) == 0 {
			continue // an allowed or unwatched PR no search answers
		}
		for _, c := range it.In {
			out[c] = append(out[c], *it)
		}
	}
	for _, items := range out {
		sortItems(items)
	}
	return out
}

func sortItems(items []Item) {
	slices.SortStableFunc(items, func(a, b Item) int {
		if a.Updated != b.Updated {
			if a.Updated > b.Updated {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
}

// find is the item for a PR URL in any collection.
func (c Collections) find(url string) (Item, bool) {
	for _, id := range []string{Mine, Reviews, Watched, Ignored} {
		for _, it := range c[id] {
			if it.URL == url {
				return it, true
			}
		}
	}
	return Item{}, false
}
