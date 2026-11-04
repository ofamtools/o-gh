package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Signals: what needs you since the last o-gh signals run, pushed into the
// mtm inbox one action item each. The kinds (pr.signals picks which are
// sent; the others are still recorded):
//
//   - review-requested: a PR in My Reviews that the last run did not see
//     there. With no last run, every open review request is new (they all
//     still need you).
//   - oncall-review-requested: the same for On-call reviews, searched only
//     while on call. Leaving the window empties the record, so the next
//     window signals what is open then.
//   - ci: a tracked PR's CI state became fail or pass (from another known
//     state; pending is not a signal).
//   - review, comment, conflicts: on a tracked PR (yours, or watched), a
//     review by someone else (approved, changes requested; a review that
//     only comments is a comment), a comment by someone else (the count
//     rose), or mergeable turned to conflicting.
//   - merged, closed: a tracked PR left the open state. Tracked PRs are
//     fetched by number (the open ones the last run saw), because a merge
//     drops a PR out of the is:open searches.
//
// First run: ci, review, comment, conflicts, merged and closed only record
// what is there (a tracked PR first seen in a later run is recorded the
// same way: its old reviews and comments are not new). Review requests
// signal on a first run, as they all still need you.
//
// Each signal carries a key naming the event (KIND:OWNER/REPO#N:DETAIL), so
// one event is one inbox row and a new event is a new row; the inbox
// command also reopens, so a repeat of an archived item shows again.
//
// Ignored PRs (Ignored: by you or by a rule) send nothing; their state is
// still recorded, so unignoring one does not replay an old change, and
// their review request is not, so unignoring one sends it.

// Signal is one inbox item.
type Signal struct {
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Key   string `json:"key"` // the PR: owner/name#number
	Title string `json:"title"`
	Why   string `json:"why"`
	// Event names the change within the PR (a head SHA and state, a review
	// id, a comment count): with Kind and Key it is the inbox item's key.
	Event string `json:"event,omitempty"`
	From  string `json:"from,omitempty"` // ci: the state before
	To    string `json:"to,omitempty"`   // ci: the state now
}

// ItemKey is the inbox item's key: KIND:OWNER/REPO#N[:EVENT].
func (s Signal) ItemKey() string {
	k := s.Kind + ":" + s.Key
	if s.Event != "" {
		k += ":" + s.Event
	}
	return k
}

const (
	kindReview    = "review-requested"
	kindOncall    = "oncall-review-requested"
	kindCI        = "ci"
	kindPRReview  = "review"
	kindComment   = "comment"
	kindMerged    = "merged"
	kindClosed    = "closed"
	kindConflicts = "conflicts"
)

// allKinds are the signal kinds, in the order pr.signals lists them.
func allKinds() []string {
	return []string{kindReview, kindOncall, kindCI, kindPRReview, kindComment, kindConflicts, kindMerged, kindClosed}
}

// signalKindsProblem names the first pr.signals entry that is no kind.
func signalKindsProblem(kinds []string) string {
	for _, k := range kinds {
		if !slices.Contains(allKinds(), k) {
			return fmt.Sprintf("%s: %q is not a signal kind (%s)", setSignals, k, strings.Join(allKinds(), ", "))
		}
	}
	return ""
}

// diffOpts are what a diff needs besides the collections.
type diffOpts struct {
	Viewer string // your login: your own reviews and comments are no signal
	Kinds  []string
	Oncall bool // on call now: the on-call search ran
	Rules  Rules
}

func (o diffOpts) sends(kind string) bool { return len(o.Kinds) == 0 || slices.Contains(o.Kinds, kind) }

// reviewStates are the review states someone else's review signals;
// COMMENTED is the kind comment.
var reviewStates = map[string]string{"APPROVED": kindPRReview, "CHANGES_REQUESTED": kindPRReview, "COMMENTED": kindComment}

func shortSHA(s string) string { return s[:min(12, len(s))] }

// openOnly reports whether a review request is for a PR that is not open
// (a custom query without is:open): it makes no signal and is not kept.
func openOnly(pr PR) bool { return pr.State != "" && pr.State != "OPEN" }

// ciWhy is the CI state of a review request as a phrase for its why.
func ciWhy(ci string) string {
	switch ci {
	case "pass":
		return " (CI passing)"
	case "fail":
		return " (CI failing)"
	case "pending":
		return " (CI pending)"
	}
	return ""
}

// diffSignals compares the collections and the poll with the last run's
// snapshot (nil for none) and answers the signals and the snapshot to keep
// when every signal is delivered.
func diffSignals(prev *Snapshot, c Collections, p Poll, o diffOpts, at time.Time) ([]Signal, Snapshot) {
	next := Snapshot{At: at.UTC(), Reviews: []string{}, CI: map[string]string{}, Oncall: []string{}, PRs: map[string]PRState{}}
	var out []Signal
	emit := func(s Signal) {
		if o.sends(s.Kind) {
			out = append(out, s)
		}
	}
	if p.hasFailed("reviews") && prev != nil {
		next.Reviews = append(next.Reviews, prev.Reviews...) // not read: keep what the last run knew
	}
	for _, it := range c[Reviews] {
		if p.hasFailed("reviews") || openOnly(it.PR) {
			continue
		}
		next.Reviews = append(next.Reviews, it.URL)
		if prev != nil && slices.Contains(prev.Reviews, it.URL) {
			continue
		}
		why := "review requested" + ciWhy(it.CI) + ": " + it.Key + " " + it.Title
		if it.Author != "" {
			why += " (by " + it.Author + ")"
		}
		emit(Signal{Kind: kindReview, URL: it.URL, Key: it.Key, Title: it.Title, Why: why, Event: o.Viewer})
	}
	if o.Oncall && p.hasFailed("oncall") && prev != nil {
		next.Oncall = append(next.Oncall, prev.Oncall...)
	}
	if o.Oncall && !p.hasFailed("oncall") {
		for _, it := range c[OncallReviews] {
			if openOnly(it.PR) {
				continue
			}
			next.Oncall = append(next.Oncall, it.URL)
			if (prev != nil && slices.Contains(prev.Oncall, it.URL)) || slices.ContainsFunc(c[Reviews], func(r Item) bool { return r.URL == it.URL }) {
				continue // seen, or already a request to you
			}
			why := "on-call review requested" + ciWhy(it.CI) + ": " + it.Key + " " + it.Title
			if it.Author != "" {
				why += " (by " + it.Author + ")"
			}
			emit(Signal{Kind: kindOncall, URL: it.URL, Key: it.Key, Title: it.Title, Why: why, Event: "oncall"})
		}
	}

	// Tracked PRs: yours (found by the Mine search) or watched; then the
	// ones the last run saw open that no collection holds now.
	type tracked struct {
		pr      PR
		ci      string
		ignored bool
		stale   bool
	}
	byURL := map[string]tracked{}
	var order []string
	for _, id := range []string{Mine, Watched, Ignored} {
		for _, it := range c[id] {
			if !(it.Watching || slices.Contains(it.Found, Mine)) {
				continue // neither yours nor watched
			}
			if _, ok := byURL[it.URL]; !ok {
				byURL[it.URL] = tracked{pr: it.PR, ci: it.CI, ignored: it.Ignored, stale: it.Stale}
				order = append(order, it.URL)
			}
		}
	}
	read := map[string]PR{}
	for _, list := range [][]PR{p.Mine, p.Reviews, p.Oncall, p.Watched} {
		for _, pr := range list {
			if _, ok := read[pr.URL]; !ok {
				read[pr.URL] = pr
			}
		}
	}
	if prev != nil {
		var gone []string
		for url := range prev.PRs {
			if _, ok := byURL[url]; !ok {
				gone = append(gone, url)
			}
		}
		slices.Sort(gone)
		for _, url := range gone {
			ps := prev.PRs[url]
			pr, ok := read[url]
			switch {
			case ps.State != "OPEN":
				// closed and out of every list: no longer tracked
			case !ok:
				// not read this time (a failed alias): keep what was known
				byURL[url] = tracked{stale: true, pr: PR{URL: url}}
				order = append(order, url)
			default:
				byURL[url] = tracked{pr: pr, ci: o.Rules.CI(pr), ignored: ps.Ignored}
				order = append(order, url)
			}
		}
	}
	for _, url := range order {
		t := byURL[url]
		var ps PRState
		var had bool
		if prev != nil && prev.PRs != nil {
			ps, had = prev.PRs[url]
		}
		if t.stale {
			// Not read: keep what the last run knew.
			if prev != nil {
				if state, ok := prev.CI[url]; ok {
					next.CI[url] = state
				}
			}
			if had {
				next.PRs[url] = ps
			}
			continue
		}
		pr := t.pr
		vanished := !slices.ContainsFunc(c[Mine], func(i Item) bool { return i.URL == url }) &&
			!slices.ContainsFunc(c[Watched], func(i Item) bool { return i.URL == url }) &&
			!slices.ContainsFunc(c[Ignored], func(i Item) bool { return i.URL == url })
		cur := PRState{State: pr.State, Comments: pr.Comments, Mergeable: pr.Mergeable, Ignored: t.ignored}
		if cur.Mergeable == "UNKNOWN" || cur.Mergeable == "" {
			cur.Mergeable = ps.Mergeable
		}
		for _, r := range pr.Reviews {
			cur.Reviews = append(cur.Reviews, r.ID)
		}
		mk := func(kind, why, event string) Signal {
			return Signal{Kind: kind, URL: url, Key: pr.Key, Title: pr.Title, Why: why + ": " + pr.Key + " " + pr.Title, Event: event}
		}
		left := had && ps.State == "OPEN" && (pr.State == "MERGED" || pr.State == "CLOSED")
		if left && vanished {
			// It dropped out of the open searches: signal once and stop
			// tracking it.
			if !t.ignored && !ps.Ignored {
				if pr.State == "MERGED" {
					emit(mk(kindMerged, "merged", ""))
				} else {
					emit(mk(kindClosed, "closed without merging", ""))
				}
			}
			continue
		}
		// CI.
		next.CI[url] = t.ci
		if prev != nil && !t.ignored {
			if old, ok := prev.CI[url]; ok && old != t.ci && (t.ci == "fail" || t.ci == "pass") {
				why := "CI failed: " + pr.Key + " " + pr.Title
				if t.ci == "pass" {
					why = "CI passed: " + pr.Key + " " + pr.Title
				}
				emit(Signal{Kind: kindCI, URL: url, Key: pr.Key, Title: pr.Title, Why: why,
					Event: shortSHA(pr.HeadSHA) + "/" + t.ci, From: old, To: t.ci})
			}
		}
		next.PRs[url] = cur
		if !had || t.ignored {
			continue // a baseline: what is there now is not new
		}
		if pr.Mergeable == "CONFLICTING" && ps.Mergeable != "CONFLICTING" {
			emit(mk(kindConflicts, "merge conflicts", shortSHA(pr.HeadSHA)))
		}
		for _, r := range pr.Reviews {
			kind, ok := reviewStates[r.State]
			if !ok || r.Who == o.Viewer || slices.Contains(ps.Reviews, r.ID) {
				continue
			}
			what := map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "changes requested", "COMMENTED": "review comment"}[r.State]
			emit(mk(kind, what+" by "+r.Who, "review:"+r.ID))
		}
		if pr.Comments > ps.Comments && pr.LastCommentBy != o.Viewer {
			why := "new comment"
			if pr.LastCommentBy != "" {
				why += " by " + pr.LastCommentBy
			}
			emit(mk(kindComment, why, fmt.Sprintf("count:%d", pr.Comments)))
		}
		if left {
			// Merged or closed but still listed (watched): signal once.
			if pr.State == "MERGED" {
				emit(mk(kindMerged, "merged", ""))
			} else {
				emit(mk(kindClosed, "closed without merging", ""))
			}
		}
	}
	return out, next
}

// unsent takes a signal that was not delivered out of the snapshot, so
// the next run sends it again.
func unsent(next *Snapshot, prev *Snapshot, s Signal) {
	switch s.Kind {
	case kindReview:
		next.Reviews = slices.DeleteFunc(next.Reviews, func(u string) bool { return u == s.URL })
		return
	case kindOncall:
		next.Oncall = slices.DeleteFunc(next.Oncall, func(u string) bool { return u == s.URL })
		return
	case kindCI:
		if prev != nil {
			if old, ok := prev.CI[s.URL]; ok {
				next.CI[s.URL] = old
				return
			}
		}
		delete(next.CI, s.URL)
		return
	}
	if prev == nil {
		return
	}
	was, had := prev.PRs[s.URL]
	if !had {
		return
	}
	now, ok := next.PRs[s.URL]
	switch s.Kind {
	case kindMerged, kindClosed:
		next.PRs[s.URL] = was // tracked again, so the next run sees it leave
		if state, ok := prev.CI[s.URL]; ok {
			next.CI[s.URL] = state
		}
	case kindConflicts:
		if ok {
			now.Mergeable = was.Mergeable
			next.PRs[s.URL] = now
		}
	default: // review, comment
		if !ok {
			return
		}
		if id, isReview := strings.CutPrefix(s.Event, "review:"); isReview {
			now.Reviews = slices.DeleteFunc(now.Reviews, func(r string) bool { return r == id })
		} else {
			now.Comments = was.Comments
		}
		next.PRs[s.URL] = now
	}
}

// Inbox puts one signal in the mtm inbox.
type Inbox func(ctx context.Context, s Signal) error

// inboxCommand is the one place the inbox command is spelled:
//
//	mtm action-item add --source gh --url URL --why WHY --title TITLE
//	    --kind KIND --key KIND:OWNER/REPO#N[:EVENT] --reopen
//
// The key makes one event one row (a retry refreshes it); --reopen makes a
// repeat of an archived event show again. The bin
// is $O_GH_MTM (default mtm); $O_GH_MTM_ARGS adds words before the verb
// (for example --data /tmp/x.db).
func inboxCommand(getenv func(string) string, s Signal) []string {
	bin := getenv("O_GH_MTM")
	if bin == "" {
		bin = "mtm"
	}
	argv := append([]string{bin}, strings.Fields(getenv("O_GH_MTM_ARGS"))...)
	argv = append(argv, "action-item", "add", "--source", "gh", "--url", s.URL, "--why", s.Why)
	if s.Title != "" {
		argv = append(argv, "--title", s.Title)
	}
	if s.Kind != "" {
		argv = append(argv, "--kind", s.Kind)
	}
	if s.Key != "" {
		argv = append(argv, "--key", s.ItemKey(), "--reopen")
	}
	return argv
}

// inboxTimeout bounds one inbox command.
const inboxTimeout = 20 * time.Second

// mtmInbox runs inboxCommand.
func mtmInbox(ctx context.Context, s Signal) error {
	argv := inboxCommand(os.Getenv, s)
	ctx, cancel := context.WithTimeout(ctx, inboxTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s: %w: %s", strings.Join(argv[:min(3, len(argv))], " "), err, msg)
		}
		return fmt.Errorf("%s: %w", strings.Join(argv[:min(3, len(argv))], " "), err)
	}
	return nil
}

// archiveItem archives the open gh action items with key, through
//
//	mtm action-item list --source gh --format json
//	mtm action-item archive --target ID
//
// (O_GH_MTM and O_GH_MTM_ARGS as for inboxCommand). No item is no error.
func archiveItem(ctx context.Context, key string) error {
	bin := os.Getenv("O_GH_MTM")
	if bin == "" {
		bin = "mtm"
	}
	pre := strings.Fields(os.Getenv("O_GH_MTM_ARGS"))
	ctx, cancel := context.WithTimeout(ctx, inboxTimeout)
	defer cancel()
	list := append(append([]string{}, pre...), "action-item", "list", "--source", "gh", "--format", "json")
	out, err := exec.CommandContext(ctx, bin, list...).Output()
	if err != nil {
		return fmt.Errorf("mtm action-item list: %w", err)
	}
	var items []struct{ ID, Key string }
	if err := json.Unmarshal(out, &items); err != nil {
		return fmt.Errorf("mtm action-item list: %w", err)
	}
	for _, it := range items {
		if it.Key != key {
			continue
		}
		arch := append(append([]string{}, pre...), "action-item", "archive", "--target", it.ID)
		if msg, err := exec.CommandContext(ctx, bin, arch...).CombinedOutput(); err != nil {
			return fmt.Errorf("mtm action-item archive %s: %w: %s", it.ID, err, strings.TrimSpace(string(msg)))
		}
	}
	return nil
}

// SignalRun is what one signals run did.
type SignalRun struct {
	DryRun  bool       `json:"dry_run"`
	First   bool       `json:"first"` // no earlier run: PR states recorded only
	Signals []Signal   `json:"signals"`
	Sent    int        `json:"sent"`
	Failed  []string   `json:"failed,omitempty"`
	Command [][]string `json:"commands,omitempty"` // dry run: the commands it would run
	Polled  time.Time  `json:"polled"`
}

// runSignals polls, diffs against the snapshot and (unless dry) pushes
// each signal and keeps the snapshot of what was delivered.
func (t *Tool) runSignals(ctx context.Context, cfg Config, dry bool) (SignalRun, error) {
	if cfg.Problem != "" {
		return SignalRun{}, errors.New(cfg.Problem)
	}
	p, st, err := t.current(ctx, cfg, true)
	if err != nil {
		return SignalRun{}, err
	}
	c := classify(p, st, cfg.Rules)
	var prev *Snapshot
	var snap Snapshot
	if ok, err := t.store.read(signalsFile, &snap); err != nil {
		return SignalRun{}, err
	} else if ok {
		prev = &snap
	}
	sigs, next := diffSignals(prev, c, p, diffOpts{Viewer: p.Viewer, Kinds: cfg.Signals, Oncall: cfg.OncallActive, Rules: cfg.Rules}, t.now())
	run := SignalRun{DryRun: dry, First: prev == nil, Signals: sigs, Polled: p.At}
	if prev == nil && len(p.Failed) > 0 {
		// No baseline to keep a failed part from: send and record nothing,
		// so the next run is the first one.
		run.Signals, run.First = []Signal{}, true
		return run, errors.New(p.failures() + " (nothing sent or recorded: there is no earlier run to keep)")
	}
	if run.Signals == nil {
		run.Signals = []Signal{}
	}
	if dry {
		for _, s := range sigs {
			run.Command = append(run.Command, inboxCommand(os.Getenv, s))
		}
		return run, nil
	}
	var errs []error
	// A review request the last run saw that has since merged or closed
	// stops asking for attention: its action item is archived (by key).
	cands := slices.Clone(p.Closing)
	if prev != nil {
		for _, list := range [][]PR{p.Mine, p.Reviews, p.Oncall, p.Watched} {
			for _, pr := range list {
				if slices.Contains(prev.Reviews, pr.URL) || slices.Contains(prev.Oncall, pr.URL) {
					cands = append(cands, pr)
				}
			}
		}
	}
	archived := map[string]bool{}
	for _, pr := range cands {
		if !openOnly(pr) || archived[pr.URL] {
			continue
		}
		archived[pr.URL] = true
		failed := false
		for _, key := range []string{
			Signal{Kind: kindReview, Key: pr.Key, Event: p.Viewer}.ItemKey(),
			Signal{Kind: kindOncall, Key: pr.Key, Event: "oncall"}.ItemKey(),
		} {
			if err := t.archive(ctx, key); err != nil {
				failed = true
				run.Failed = append(run.Failed, pr.URL+": archive: "+err.Error())
			}
		}
		if failed { // try again next run
			if prev != nil && slices.Contains(prev.Reviews, pr.URL) {
				next.Reviews = append(next.Reviews, pr.URL)
			}
			if prev != nil && slices.Contains(prev.Oncall, pr.URL) {
				next.Oncall = append(next.Oncall, pr.URL)
			}
		}
	}
	for _, s := range sigs {
		if err := t.inbox(ctx, s); err != nil {
			run.Failed = append(run.Failed, s.URL+": "+err.Error())
			errs = append(errs, err)
			unsent(&next, prev, s)
			continue
		}
		run.Sent++
	}
	if err := t.store.locked(func() error { return t.store.write(signalsFile, next) }); err != nil {
		return run, err
	}
	if len(errs) > 0 {
		return run, fmt.Errorf("%d of %d signals not sent (kept for the next run): %w", len(errs), len(sigs), errors.Join(errs...))
	}
	if len(p.Failed) > 0 {
		return run, errors.New(p.failures() + " (the last run's record of it is kept)")
	}
	return run, nil
}
