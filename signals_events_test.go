package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Lane G1: on-call, review and comment signals, merges, event keys.

func kindsOf(sigs []Signal) []string {
	out := []string{}
	for _, s := range sigs {
		out = append(out, s.Kind)
	}
	return out
}

func setting(t *testing.T, tool *Tool, name string, v any) {
	t.Helper()
	if err := tool.r.Settings().Set(name, v); err != nil {
		t.Fatal(err)
	}
}

// primed is a tool that has made its first real signals run.
func primed(t *testing.T) (*Tool, *fakeGH, *inboxLog) {
	t.Helper()
	tool, gh, inbox, _ := testTool(t)
	if _, err := tool.runSignals(context.Background(), tool.config(), false); err != nil {
		t.Fatal(err)
	}
	inbox.got = nil
	return tool, gh, inbox
}

func TestOncallWindow(t *testing.T) {
	zone := time.FixedZone("local", -7*3600)
	at := func(day string, h, m, s int) time.Time {
		d, _ := time.ParseInLocation("2006-01-02", day, zone)
		return d.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
	}
	// A two-week window from Wed 2026-11-04, and the local-midnight edges.
	o := Oncall{Mode: OncallAuto, Start: "2026-11-04", Weeks: 2}
	for _, c := range []struct {
		now  time.Time
		want bool
	}{
		{at("2026-11-03", 23, 59, 59), false},
		{at("2026-11-04", 0, 0, 0), true},
		{at("2026-11-17", 23, 59, 59), true},
		{at("2026-11-18", 0, 0, 0), false},
	} {
		if got := o.Active(c.now); got != c.want {
			t.Errorf("Active(%s) = %v, want %v", c.now.Format(time.RFC3339), got, c.want)
		}
	}
	// Mode overrides the dates; no start is never on call.
	if !(Oncall{Mode: OncallOn}).Active(at("2020-01-01", 0, 0, 0)) || (Oncall{Mode: OncallOff, Start: "2026-11-04"}).Active(at("2026-11-05", 0, 0, 0)) {
		t.Error("mode on/off")
	}
	if (Oncall{Mode: OncallAuto}).Active(at("2026-11-05", 0, 0, 0)) {
		t.Error("no start is on call")
	}
	if (Oncall{Start: "soon"}).Problem() == "" || (Oncall{Weeks: 99}).Problem() == "" || (Oncall{Start: "2026-11-04", Weeks: 2}).Problem() != "" {
		t.Error("problems")
	}
}

func TestOncallSearchOnlyWhileOnCall(t *testing.T) {
	tool, gh, inbox, _ := testTool(t)
	ctx := context.Background()
	gh.oncall = []string{urlTeapot}
	setting(t, tool, setOncallQuery, "is:pr is:open team-review-requested:tea-shop/cooks")
	setting(t, tool, setOncallStart, "2026-11-04")
	setting(t, tool, setOncallWeeks, 1)

	// Outside the window: no third search, no item, an empty record.
	tool.now = func() time.Time { return time.Date(2026, 11, 3, 23, 59, 59, 0, time.Local) }
	run, err := tool.runSignals(ctx, tool.config(), false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gh.bodies[len(gh.bodies)-1], "oncall: search") || slices.Contains(kindsOf(run.Signals), kindOncall) {
		t.Fatalf("off call: %v", kindsOf(run.Signals))
	}
	cs, _, _ := tool.collect(ctx, tool.config(), false)
	if len(cs[OncallReviews]) != 0 {
		t.Fatalf("off-call list %v", cs[OncallReviews])
	}

	// At local midnight the window opens: the request is an item once.
	tool.now = func() time.Time { return time.Date(2026, 11, 4, 0, 0, 0, 0, time.Local) }
	run, err = tool.runSignals(ctx, tool.config(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gh.bodies[len(gh.bodies)-1], "oncall: search") {
		t.Fatal("on call: the on-call search did not run")
	}
	var got []Signal
	for _, s := range inbox.got {
		if s.Kind == kindOncall {
			got = append(got, s)
		}
	}
	if len(got) != 1 || got[0].URL != urlTeapot || got[0].ItemKey() != "oncall-review-requested:pottery/teapot#7:oncall" || !strings.HasPrefix(got[0].Why, "on-call review requested:") {
		t.Fatalf("on-call signal %+v", got)
	}
	cs, _, _ = tool.collect(ctx, tool.config(), true)
	if len(cs[OncallReviews]) != 1 {
		t.Fatalf("on-call list %v", cs[OncallReviews])
	}
	// Seen: not again. Window over: the record empties.
	if run, _ = tool.runSignals(ctx, tool.config(), false); slices.Contains(kindsOf(run.Signals), kindOncall) {
		t.Fatal("on-call request repeated")
	}
	tool.now = func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.Local) }
	tool.runSignals(ctx, tool.config(), false)
	if snap := tool.lastSnapshot(); len(snap.Oncall) != 0 {
		t.Fatalf("record after the window %v", snap.Oncall)
	}
	// A bad oncall.start refuses with a plain reason; nothing is sent.
	setting(t, tool, setOncallStart, "soon")
	n := len(inbox.got)
	if _, err := tool.runSignals(ctx, tool.config(), false); err == nil || !strings.Contains(err.Error(), "oncall.start") || len(inbox.got) != n {
		t.Fatalf("bad start: %v", err)
	}
}

func TestOncallSkipsWhatIsAlreadyAReviewRequestToYou(t *testing.T) {
	tool, gh, inbox, _ := testTool(t)
	gh.oncall = []string{urlGrade} // also in My Reviews
	setting(t, tool, setOncallQuery, "team")
	setting(t, tool, setOncallMode, OncallOn)
	if _, err := tool.runSignals(context.Background(), tool.config(), false); err != nil {
		t.Fatal(err)
	}
	for _, s := range inbox.got {
		if s.Kind == kindOncall {
			t.Fatalf("double signal %+v", s)
		}
	}
}

func TestReviewsAndCommentsSignalOnlyWhenNew(t *testing.T) {
	tool, gh, inbox := primed(t)
	ctx := context.Background()

	// Nothing changed: nothing sent (the first run recorded what was there,
	// including the fixture's existing review and 2 comments).
	if run, _ := tool.runSignals(ctx, tool.config(), false); len(run.Signals) != 0 {
		t.Fatalf("flood: %v", kindsOf(run.Signals))
	}

	gh.addReview(urlTimer, "R1", "nori", "APPROVED")
	gh.addReview(urlTimer, "R2", "miso", "APPROVED") // yours: no signal
	gh.addReview(urlTimer, "R3", "kombu", "CHANGES_REQUESTED")
	gh.addReview(urlTimer, "R4", "kombu", "COMMENTED")
	gh.addReview(urlTimer, "R5", "kombu", "DISMISSED")
	gh.setComments(urlMenu, 3, "nori")
	run, err := tool.runSignals(ctx, tool.config(), false)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, s := range run.Signals {
		keys = append(keys, s.ItemKey())
	}
	want := []string{
		"review:tea-shop/kitchen#12:review:R1",
		"review:tea-shop/kitchen#12:review:R3",
		"comment:tea-shop/kitchen#12:review:R4",
		"comment:tea-shop/menu#3:count:3",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %v", keys)
	}
	if !strings.HasPrefix(run.Signals[0].Why, "approved by nori: tea-shop/kitchen#12") {
		t.Fatalf("why %q", run.Signals[0].Why)
	}
	// Once only.
	if run, _ = tool.runSignals(ctx, tool.config(), false); len(run.Signals) != 0 {
		t.Fatalf("repeat %v", kindsOf(run.Signals))
	}
	// Your own comment is no signal, and a later one by someone else is.
	gh.setComments(urlMenu, 4, "miso")
	if run, _ = tool.runSignals(ctx, tool.config(), false); len(run.Signals) != 0 {
		t.Fatalf("own comment %v", kindsOf(run.Signals))
	}
	gh.setComments(urlMenu, 5, "nori")
	if run, _ = tool.runSignals(ctx, tool.config(), false); len(run.Signals) != 1 || run.Signals[0].Event != "count:5" {
		t.Fatalf("later comment %+v", run.Signals)
	}
	_ = inbox
}

func TestConflictsSignal(t *testing.T) {
	tool, gh, _ := primed(t)
	gh.nodes[urlTimer]["mergeable"] = "CONFLICTING"
	run, _ := tool.runSignals(context.Background(), tool.config(), false)
	if len(run.Signals) != 1 || run.Signals[0].Kind != kindConflicts {
		t.Fatalf("conflicts %+v", run.Signals)
	}
	if run, _ = tool.runSignals(context.Background(), tool.config(), false); len(run.Signals) != 0 {
		t.Fatal("conflicts repeated")
	}
}

func TestOldSnapshotIsABaselineForNewKinds(t *testing.T) {
	tool, gh, inbox, _ := testTool(t)
	ctx := context.Background()
	// A snapshot from before this change: reviews and ci only.
	old := Snapshot{At: fixtureNow, Reviews: []string{urlGrade, urlBump}, CI: map[string]string{urlTimer: "fail", urlMenu: "pending"}}
	if err := tool.store.write(signalsFile, old); err != nil {
		t.Fatal(err)
	}
	gh.addReview(urlTimer, "R9", "nori", "APPROVED")
	gh.setComments(urlMenu, 9, "nori")
	run, err := tool.runSignals(ctx, tool.config(), false)
	if err != nil || len(run.Signals) != 0 || len(inbox.got) != 0 {
		t.Fatalf("upgrade flood: %v %v", kindsOf(run.Signals), err)
	}
	snap := tool.lastSnapshot()
	if snap.PRs[urlMenu].Comments != 9 || !slices.Contains(snap.PRs[urlTimer].Reviews, "R9") {
		t.Fatalf("not recorded: %+v", snap.PRs)
	}
}

func TestFirstRunRecordsAndSignalsNoPRChange(t *testing.T) {
	tool, _, inbox, _ := testTool(t)
	gh := tool.gh
	_ = gh
	run, err := tool.runSignals(context.Background(), tool.config(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range inbox.got {
		if s.Kind != kindReview {
			t.Fatalf("first run sent %s", s.Kind)
		}
	}
	if !run.First {
		t.Fatal("not first")
	}
	snap := tool.lastSnapshot()
	if len(snap.PRs) == 0 || snap.PRs[urlTimer].Comments != 2 || len(snap.PRs[urlTimer].Reviews) != 1 {
		t.Fatalf("snapshot %+v", snap.PRs)
	}
}

func TestMergedAndClosedSignalOnce(t *testing.T) {
	tool, gh, inbox := primed(t)
	ctx := context.Background()
	gh.setState(urlTimer, "MERGED") // drops out of the is:open Mine search
	gh.setState(urlMenu, "CLOSED")
	run, err := tool.runSignals(ctx, tool.config(), false)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, s := range run.Signals {
		keys = append(keys, s.ItemKey())
	}
	if want := []string{"merged:tea-shop/kitchen#12", "closed:tea-shop/menu#3"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %v", keys)
	}
	if run, _ = tool.runSignals(ctx, tool.config(), false); len(run.Signals) != 0 {
		t.Fatalf("repeat %v", kindsOf(run.Signals))
	}
	if snap := tool.lastSnapshot(); len(snap.PRs) != 0 {
		t.Fatalf("still tracking %+v", snap.PRs)
	}
	_ = inbox
}

func TestMergedKeptWhenTheInboxFails(t *testing.T) {
	tool, gh, inbox := primed(t)
	gh.setState(urlTimer, "MERGED")
	inbox.fail[urlTimer] = true
	if _, err := tool.runSignals(context.Background(), tool.config(), false); err == nil {
		t.Fatal("no error")
	}
	delete(inbox.fail, urlTimer)
	run, err := tool.runSignals(context.Background(), tool.config(), false)
	if err != nil || len(run.Signals) != 1 || run.Signals[0].Kind != kindMerged {
		t.Fatalf("retry %+v %v", run.Signals, err)
	}
}

func TestSignalKindsSetting(t *testing.T) {
	tool, gh, _ := primed(t)
	setting(t, tool, setSignals, []string{kindMerged})
	gh.addReview(urlTimer, "R1", "nori", "APPROVED")
	gh.setState(urlMenu, "MERGED")
	run, _ := tool.runSignals(context.Background(), tool.config(), false)
	if got := kindsOf(run.Signals); !reflect.DeepEqual(got, []string{kindMerged}) {
		t.Fatalf("kinds %v", got)
	}
	// The review was recorded though not sent: enabling the kind replays nothing.
	setting(t, tool, setSignals, []string{kindPRReview})
	if run, _ = tool.runSignals(context.Background(), tool.config(), false); len(run.Signals) != 0 {
		t.Fatalf("replay %v", kindsOf(run.Signals))
	}
	setting(t, tool, setSignals, []string{"nope"})
	if _, err := tool.runSignals(context.Background(), tool.config(), false); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("unknown kind: %v", err)
	}
}

// A CI failure that repeats after the item was archived must show again:
// every event has its own key, and the command reopens.
func TestRepeatedCIFailureIsANewItem(t *testing.T) {
	tool, gh, inbox := primed(t)
	ctx := context.Background()
	gh.setCI(urlMenu, "fail")
	tool.runSignals(ctx, tool.config(), false)
	gh.setCI(urlMenu, "pass")
	tool.runSignals(ctx, tool.config(), false)
	gh.setCIOid(urlMenu, "fail", "second")
	tool.runSignals(ctx, tool.config(), false)
	var fails []Signal
	for _, s := range inbox.got {
		if s.Kind == kindCI && s.To == "fail" {
			fails = append(fails, s)
		}
	}
	if len(fails) != 2 || fails[0].ItemKey() == fails[1].ItemKey() {
		t.Fatalf("keys %+v", fails)
	}
	if got := inboxCommand(func(string) string { return "" }, fails[1]); !slices.Contains(got, "--reopen") || !slices.Contains(got, fails[1].ItemKey()) {
		t.Fatalf("command %q", got)
	}
}

func TestAFailedReadKeepsTheSnapshot(t *testing.T) {
	tool, gh, _ := primed(t)
	path := filepath.Join(tool.store.Dir, signalsFile)
	before, _ := os.ReadFile(path)
	gh.fail = os.ErrDeadlineExceeded
	if _, err := tool.runSignals(context.Background(), tool.config(), false); err == nil {
		t.Fatal("no error")
	}
	gh.fail = nil
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("snapshot changed")
	}
	// A search that errors beside other data is a failed read too, not an
	// empty list (which would signal everything again later).
	if _, err := parsePoll([]byte(`{"data":{"viewer":{"login":"miso"},"mine":null},"errors":[{"message":"rate limited"}]}`), Query{Mine: "q"}, fixtureNow); err == nil || !strings.Contains(err.Error(), "mine search did not answer") {
		t.Fatalf("null search: %v", err)
	}
}
