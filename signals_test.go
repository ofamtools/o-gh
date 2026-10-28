package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func kinds(sigs []Signal) []string {
	out := []string{}
	for _, s := range sigs {
		out = append(out, s.Kind+" "+s.URL)
	}
	return out
}

func TestDiffSignals(t *testing.T) {
	p := fixturePoll(t)
	st := newState()
	st.Watched[urlTeapot] = Mark{URL: urlTeapot}
	c := classify(p, st, Rules{})

	// First run: every review request, no CI change; CI recorded for mine + watched.
	sigs, snap := diffSignals(nil, c, fixtureNow)
	if got, want := kinds(sigs), []string{kindReview + " " + urlGrade, kindReview + " " + urlBump}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first run %v", got)
	}
	if sigs[0].Why != "review requested: tea-shop/kitchen#40 Bump leaf grade (by nori)" {
		t.Fatalf("why %q", sigs[0].Why)
	}
	if want := map[string]string{urlTimer: "fail", urlMenu: "pending", urlTeapot: "pass"}; !reflect.DeepEqual(snap.CI, want) {
		t.Fatalf("ci snapshot %v", snap.CI)
	}

	// Nothing changed: nothing to send.
	if again, _ := diffSignals(&snap, c, fixtureNow); len(again) != 0 {
		t.Fatalf("repeat %v", kinds(again))
	}

	// CI: fail -> pass and pending -> fail are signals; pass -> pending is not.
	prev := snap
	prev.CI = map[string]string{urlTimer: "pass", urlMenu: "pending", urlTeapot: "pending"}
	prev.Reviews = []string{urlGrade}
	sigs, _ = diffSignals(&prev, c, fixtureNow)
	got := kinds(sigs)
	want := []string{kindReview + " " + urlBump, kindCI + " " + urlTimer, kindCI + " " + urlTeapot}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("second run %v, want %v", got, want)
	}
	if s := sigs[1]; s.From != "pass" || s.To != "fail" || !strings.HasPrefix(s.Why, "CI failed: tea-shop/kitchen#12") {
		t.Fatalf("ci signal %+v", s)
	}
	if s := sigs[2]; s.To != "pass" || !strings.HasPrefix(s.Why, "CI passed: pottery/teapot#7") {
		t.Fatalf("ci signal %+v", s)
	}
	prev.CI[urlMenu] = "pass"
	if sigs, _ = diffSignals(&prev, c, fixtureNow); len(sigs) != 3 {
		t.Fatalf("pass -> pending signalled: %v", kinds(sigs))
	}
}

func TestDiffSignalsIgnore(t *testing.T) {
	p := fixturePoll(t)
	st := newState()
	st.Ignored[urlTimer] = Mark{URL: urlTimer}
	rules := parseRules([]string{"author:dependabot*"}, nil)
	c := classify(p, st, rules)
	prev := &Snapshot{Reviews: []string{}, CI: map[string]string{urlTimer: "pass"}}
	sigs, snap := diffSignals(prev, c, fixtureNow)
	// The bot's request and the ignored PR's CI failure send nothing.
	if got := kinds(sigs); !reflect.DeepEqual(got, []string{kindReview + " " + urlGrade}) {
		t.Fatalf("signals %v", got)
	}
	// The ignored PR's CI is still recorded, so unignoring it replays nothing;
	// the ignored review request is not, so unignoring it sends it.
	if snap.CI[urlTimer] != "fail" || strings.Contains(strings.Join(snap.Reviews, " "), urlBump) {
		t.Fatalf("snapshot %+v", snap)
	}
	delete(st.Ignored, urlTimer)
	sigs, _ = diffSignals(&snap, classify(p, st, Rules{}), fixtureNow)
	if got := kinds(sigs); !reflect.DeepEqual(got, []string{kindReview + " " + urlBump}) {
		t.Fatalf("after unignore %v", got)
	}
}

func TestUnsentStaysForTheNextRun(t *testing.T) {
	prev := &Snapshot{Reviews: []string{"a"}, CI: map[string]string{"x": "pass"}}
	next := Snapshot{Reviews: []string{"a", "b"}, CI: map[string]string{"x": "fail", "y": "fail"}}
	unsent(&next, prev, Signal{Kind: kindReview, URL: "b"})
	unsent(&next, prev, Signal{Kind: kindCI, URL: "x"})
	unsent(&next, prev, Signal{Kind: kindCI, URL: "y"})
	if !reflect.DeepEqual(next.Reviews, []string{"a"}) || !reflect.DeepEqual(next.CI, map[string]string{"x": "pass"}) {
		t.Fatalf("next %+v", next)
	}
}

func TestInboxCommand(t *testing.T) {
	s := Signal{URL: urlGrade, Why: "review requested: x"}
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	want := []string{"mtm", "action-item", "add", "--source", "gh", "--url", urlGrade, "--why", "review requested: x"}
	if got := inboxCommand(get, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("command %q", got)
	}
	env["O_GH_MTM"], env["O_GH_MTM_ARGS"] = "/opt/mtm", "--data /tmp/x.db"
	if got := inboxCommand(get, s); got[0] != "/opt/mtm" || got[1] != "--data" || got[2] != "/tmp/x.db" || got[3] != "action-item" {
		t.Fatalf("command %q", got)
	}
}

// TestMtmInboxRunsTheCommand runs the real seam against a fake mtm script.
func TestMtmInboxRunsTheCommand(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "mtm")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + log + "\n[ \"$FAIL\" = 1 ] && { echo 'no such verb' >&2; exit 3; }\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("O_GH_MTM", bin)
	t.Setenv("O_GH_MTM_ARGS", "")
	t.Setenv("FAIL", "")
	s := Signal{URL: urlGrade, Why: "review requested: tea-shop/kitchen#40 Bump leaf grade"}
	if err := mtmInbox(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "action-item\nadd\n--source\ngh\n--url\n" + urlGrade + "\n--why\nreview requested: tea-shop/kitchen#40 Bump leaf grade\n"
	if string(b) != want {
		t.Fatalf("argv:\n%s", b)
	}
	t.Setenv("FAIL", "1")
	if err := mtmInbox(context.Background(), s); err == nil || !strings.Contains(err.Error(), "no such verb") {
		t.Fatalf("failure: %v", err)
	}
}

func TestRunSignals(t *testing.T) {
	tool, gh, inbox, _ := testTool(t)
	ctx := context.Background()
	c := cfg(t, tool)

	// Dry run: the signals and the commands, nothing sent, no snapshot.
	run, err := tool.runSignals(ctx, c, true)
	if err != nil || !run.DryRun || !run.First || len(run.Signals) != 2 || len(run.Command) != 2 || len(inbox.got) != 0 {
		t.Fatalf("dry run %+v %v", run, err)
	}
	if run.Command[0][1] != "action-item" {
		t.Fatalf("dry-run command %q", run.Command[0])
	}
	if _, err := os.Stat(filepath.Join(tool.store.Dir, signalsFile)); !os.IsNotExist(err) {
		t.Fatal("a dry run wrote the snapshot")
	}

	// The real run: one delivery fails and is kept for the next run.
	inbox.fail[urlBump] = true
	run, err = tool.runSignals(ctx, c, false)
	if err == nil || run.Sent != 1 || len(run.Failed) != 1 {
		t.Fatalf("run %+v %v", run, err)
	}
	delete(inbox.fail, urlBump)
	run, err = tool.runSignals(ctx, c, false)
	if err != nil || run.Sent != 1 || run.Signals[0].URL != urlBump {
		t.Fatalf("retry %+v %v", run, err)
	}
	if run, _ = tool.runSignals(ctx, c, false); len(run.Signals) != 0 {
		t.Fatalf("nothing new: %v", kinds(run.Signals))
	}

	// CI on one of mine goes red, then green; a new review request arrives.
	gh.setCI(urlMenu, "fail")
	gh.reviews = append(gh.reviews, urlTeapot)
	run, err = tool.runSignals(ctx, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := kinds(run.Signals), []string{kindReview + " " + urlTeapot, kindCI + " " + urlMenu}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changes %v", got)
	}
	gh.setCI(urlMenu, "pass")
	run, _ = tool.runSignals(ctx, c, false)
	if len(run.Signals) != 1 || run.Signals[0].To != "pass" || run.Signals[0].From != "fail" {
		t.Fatalf("green %+v", run.Signals)
	}
	if got := len(inbox.got); got != 5 {
		t.Fatalf("inbox got %d", got)
	}
	// Every signals run polls: no cache.
	before := gh.calls
	tool.runSignals(ctx, c, true)
	if gh.calls != before+1 {
		t.Fatal("signals used the cache")
	}
}

func TestPollCache(t *testing.T) {
	tool, gh, _, _ := testTool(t)
	ctx := context.Background()
	c := cfg(t, tool)
	if _, _, err := tool.collect(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	tool.collect(ctx, c, false)
	if gh.calls != 1 {
		t.Fatalf("a fresh cache polled again: %d", gh.calls)
	}
	// A new watch the cached poll did not ask for: poll.
	tool.store.Change(func(st *State) error { st.Watched[urlTeapot] = Mark{URL: urlTeapot}; return nil })
	cs, _, _ := tool.collect(ctx, c, false)
	if gh.calls != 2 || len(cs[Watched]) != 1 || cs[Watched][0].Stale {
		t.Fatalf("watch did not poll: %d %+v", gh.calls, cs[Watched])
	}
	// Older than poll.max-age: poll.
	tool.now = func() time.Time { return fixtureNow.Add(2 * time.Minute) }
	tool.collect(ctx, c, false)
	if gh.calls != 3 {
		t.Fatalf("a stale cache answered: %d", gh.calls)
	}
}
