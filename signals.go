package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Signals: what needs you since the last o-gh signals run, pushed into the
// mtm inbox one action item each.
//
//   - review-requested: a PR in My Reviews that the last run did not see
//     there. With no last run, every open review request is new (they all
//     still need you).
//   - ci: a PR in Mine or Watched whose CI state became fail or pass (from
//     another known state; pending is not a signal). The first run only
//     records the states.
//
// Ignored PRs (Ignored: by you or by a rule) send nothing; their CI state
// is still recorded, so unignoring one does not replay an old change, and
// their review request is not, so unignoring one sends it.

// Signal is one inbox item.
type Signal struct {
	Kind  string `json:"kind"` // review-requested, ci
	URL   string `json:"url"`
	Key   string `json:"key"`
	Title string `json:"title"`
	Why   string `json:"why"`
	From  string `json:"from,omitempty"` // ci: the state before
	To    string `json:"to,omitempty"`   // ci: the state now
}

const (
	kindReview = "review-requested"
	kindCI     = "ci"
)

// diffSignals compares the collections with the last run's snapshot (nil
// for none) and answers the signals and the snapshot to keep when every
// signal is delivered.
func diffSignals(prev *Snapshot, c Collections, at time.Time) ([]Signal, Snapshot) {
	next := Snapshot{At: at.UTC(), Reviews: []string{}, CI: map[string]string{}}
	var out []Signal
	for _, it := range c[Reviews] {
		next.Reviews = append(next.Reviews, it.URL)
		if prev != nil && slices.Contains(prev.Reviews, it.URL) {
			continue
		}
		why := "review requested: " + it.Key + " " + it.Title
		if it.Author != "" {
			why += " (by " + it.Author + ")"
		}
		out = append(out, Signal{Kind: kindReview, URL: it.URL, Key: it.Key, Title: it.Title, Why: why})
	}
	tracked := map[string]Item{}
	var order []string
	for _, id := range []string{Mine, Watched, Ignored} {
		for _, it := range c[id] {
			if it.Stale || !(it.Watching || slices.Contains(it.Found, Mine)) {
				continue // not read, or neither yours nor watched
			}
			if _, ok := tracked[it.URL]; !ok {
				tracked[it.URL] = it
				order = append(order, it.URL)
			}
		}
	}
	for _, url := range order {
		it := tracked[url]
		next.CI[url] = it.CI
		if prev == nil || it.Ignored {
			continue
		}
		old, ok := prev.CI[url]
		if !ok || old == it.CI || (it.CI != "fail" && it.CI != "pass") {
			continue
		}
		why := "CI failed: " + it.Key + " " + it.Title
		if it.CI == "pass" {
			why = "CI passed: " + it.Key + " " + it.Title
		}
		out = append(out, Signal{Kind: kindCI, URL: url, Key: it.Key, Title: it.Title, Why: why, From: old, To: it.CI})
	}
	// A stale tracked PR keeps its old state.
	if prev != nil {
		for url, state := range prev.CI {
			if _, ok := next.CI[url]; !ok && staleIn(c, url) {
				next.CI[url] = state
			}
		}
	}
	return out, next
}

func staleIn(c Collections, url string) bool {
	it, ok := c.find(url)
	return ok && it.Stale
}

// unsent takes a signal that was not delivered out of the snapshot, so
// the next run sends it again.
func unsent(next *Snapshot, prev *Snapshot, s Signal) {
	switch s.Kind {
	case kindReview:
		next.Reviews = slices.DeleteFunc(next.Reviews, func(u string) bool { return u == s.URL })
	case kindCI:
		if prev != nil {
			if old, ok := prev.CI[s.URL]; ok {
				next.CI[s.URL] = old
				return
			}
		}
		delete(next.CI, s.URL)
	}
}

// Inbox puts one signal in the mtm inbox.
type Inbox func(ctx context.Context, s Signal) error

// inboxCommand is the one place the inbox command is spelled (lane L1's
// contract): mtm action-item add --source gh --url URL --why WHY. The bin
// is $O_GH_MTM (default mtm); $O_GH_MTM_ARGS adds words before the verb
// (for example --data /tmp/x.db).
func inboxCommand(getenv func(string) string, s Signal) []string {
	bin := getenv("O_GH_MTM")
	if bin == "" {
		bin = "mtm"
	}
	argv := append([]string{bin}, strings.Fields(getenv("O_GH_MTM_ARGS"))...)
	return append(argv, "action-item", "add", "--source", "gh", "--url", s.URL, "--why", s.Why)
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

// SignalRun is what one signals run did.
type SignalRun struct {
	DryRun  bool       `json:"dry_run"`
	First   bool       `json:"first"` // no earlier run: CI states recorded only
	Signals []Signal   `json:"signals"`
	Sent    int        `json:"sent"`
	Failed  []string   `json:"failed,omitempty"`
	Command [][]string `json:"commands,omitempty"` // dry run: the commands it would run
	Polled  time.Time  `json:"polled"`
}

// runSignals polls, diffs against the snapshot and (unless dry) pushes
// each signal and keeps the snapshot of what was delivered.
func (t *Tool) runSignals(ctx context.Context, cfg Config, dry bool) (SignalRun, error) {
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
	sigs, next := diffSignals(prev, c, t.now())
	run := SignalRun{DryRun: dry, First: prev == nil, Signals: sigs, Polled: p.At}
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
	return run, nil
}
