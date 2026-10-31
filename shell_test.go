package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	ocli "ofam.dev/ochazuke/cli"
)

// sh runs one o-gh command line on a fresh Tool sharing tool's fakes and
// data directory, and answers its stdout.
func sh(t *testing.T, tool *Tool, args ...string) (string, error) {
	t.Helper()
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	sub := &Tool{store: tool.store, gh: tool.gh, inbox: tool.inbox, now: tool.now, opener: tool.opener, clip: tool.clip,
		Stdin: strings.NewReader(""), Stdout: out, Stderr: errb}
	err := run(sub, args)
	if err != nil {
		t.Logf("%v: %v\n%s", args, err, errb)
	}
	return out.String(), err
}

func rowsOf(t *testing.T, out string) []ocli.Row {
	t.Helper()
	rows, err := ocli.DecodeRows(prNoun, []byte(out))
	if err != nil {
		t.Fatalf("rows: %v\n%s", err, out)
	}
	return rows
}

func ids(rows []ocli.Row) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestCatalog(t *testing.T) {
	tool, _, _, _ := testTool(t)
	out, err := sh(t, tool, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	var doc ocli.CatalogDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "o-gh" || len(doc.Pages) != 1 || len(doc.Pages[0].Lists) != 4 || doc.Pages[0].Lists[1].Source != "pr.reviews" {
		t.Fatalf("pages %+v", doc.Pages)
	}
	verbs := map[string]bool{}
	for _, n := range doc.AppNouns() {
		for _, v := range n.Verbs {
			verbs[n.Name+"."+v.Name] = true
		}
	}
	for _, want := range []string{"pr.mine", "pr.reviews", "pr.watched", "pr.ignored", "pr.open", "pr.show", "pr.copy-url", "pr.watch",
		"pr.unwatch", "pr.ignore", "pr.unignore", "pr.checkout", "pr.send-to-inbox", "collection.list", "signal.preview", "signal.push"} {
		if !verbs[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}
	if verb, rows := doc.RowSource(prNoun); verb == "" || rows != prNoun {
		t.Errorf("pr row source %q %q", verb, rows)
	}
}

func TestShellCollectionsAndMarks(t *testing.T) {
	tool, gh, inbox, _ := testTool(t)
	out, err := sh(t, tool, "pr", "mine")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rowsOf(t, out)); strings.Join(got, " ") != "pr/tea-shop/kitchen#12 pr/tea-shop/menu#3" {
		t.Fatalf("mine %v", got)
	}
	// Watch a PR by URL, ignore one by key, then the collections follow.
	if _, err := sh(t, tool, "pr", "watch", "--target", urlTeapot); err != nil {
		t.Fatal(err)
	}
	if _, err := sh(t, tool, "pr", "ignore", "--target", "tea-shop/kitchen#41"); err != nil {
		t.Fatal(err)
	}
	out, _ = sh(t, tool, "pr", "watched")
	if got := ids(rowsOf(t, out)); strings.Join(got, " ") != "pr/pottery/teapot#7" {
		t.Fatalf("watched %v", got)
	}
	out, _ = sh(t, tool, "pr", "reviews")
	if got := ids(rowsOf(t, out)); strings.Join(got, " ") != "pr/tea-shop/kitchen#40" {
		t.Fatalf("reviews %v", got)
	}
	out, _ = sh(t, tool, "pr", "ignored")
	rows := rowsOf(t, out)
	if strings.Join(ids(rows), " ") != "pr/tea-shop/kitchen#41" {
		t.Fatalf("ignored %v", ids(rows))
	}
	// Ignoring a watched PR stops the watch.
	sh(t, tool, "pr", "ignore", "--target", "pr/pottery/teapot#7")
	st, _ := tool.store.State()
	if _, w := st.Watched[urlTeapot]; w || len(st.Ignored) != 2 || st.Ignored[urlBump].Title != "Bump steeper from 1.2 to 1.3" {
		t.Fatalf("state %+v", st)
	}
	sh(t, tool, "pr", "unignore", "--target", urlBump)
	out, _ = sh(t, tool, "pr", "list", "--collection", "reviews", "--format", "table")
	if !strings.Contains(out, "tea-shop/kitchen#41") || !strings.Contains(out, "Bump steeper") {
		t.Fatalf("table:\n%s", out)
	}
	// Send to inbox: the seam gets the PR and a reason.
	if _, err := sh(t, tool, "pr", "send-to-inbox", "--target", urlGrade); err != nil {
		t.Fatal(err)
	}
	if len(inbox.got) != 1 || inbox.got[0].URL != urlGrade || !strings.HasPrefix(inbox.got[0].Why, "sent from o-gh: tea-shop/kitchen#40") {
		t.Fatalf("inbox %+v", inbox.got)
	}
	// Collections answer counts; a bad target is refused.
	out, _ = sh(t, tool, "collection", "list", "--format", "table")
	if !strings.Contains(out, "My Reviews") {
		t.Fatalf("collections:\n%s", out)
	}
	if _, err := sh(t, tool, "pr", "watch", "--target", "not-a-pr"); err == nil {
		t.Fatal("a bad target was watched")
	}
	// Every list above shared one poll per change of the watched set.
	if gh.calls > 3 {
		t.Fatalf("%d polls", gh.calls)
	}
}

func TestShellShowFetchesAnUnknownPR(t *testing.T) {
	tool, gh, _, _ := testTool(t)
	out, err := sh(t, tool, "pr", "show", "--target", urlTeapot)
	if err != nil {
		t.Fatal(err)
	}
	var it Item
	if err := json.Unmarshal([]byte(out), &it); err != nil || it.Title != "Teapot spout redesign" || it.CI != "pass" {
		t.Fatalf("show %v %+v\n%s", err, it, out)
	}
	if !strings.Contains(gh.bodies[len(gh.bodies)-1], `w0: repository(owner: \"pottery\", name: \"teapot\")`) {
		t.Fatalf("lookup query %s", gh.bodies[len(gh.bodies)-1])
	}
	if _, err := sh(t, tool, "pr", "show", "--target", urlGone); err == nil {
		t.Fatal("a missing PR showed")
	}
}

func TestSignalsCommand(t *testing.T) {
	tool, _, inbox, _ := testTool(t)
	out, err := sh(t, tool, "signals", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var run SignalRun
	if err := json.Unmarshal([]byte(out), &run); err != nil || !run.DryRun || len(run.Signals) != 2 || len(inbox.got) != 0 {
		t.Fatalf("dry run %v %+v", err, run)
	}
	if _, err := sh(t, tool, "signals"); err != nil {
		t.Fatal(err)
	}
	if len(inbox.got) != 2 {
		t.Fatalf("inbox %+v", inbox.got)
	}
}

func TestPageSmoke(t *testing.T) {
	tool, _, _, _ := testTool(t)
	tool.store.Change(func(st *State) error { st.Watched[urlTeapot] = Mark{URL: urlTeapot}; return nil })
	out, err := sh(t, tool, "view", "--smoke", "--width", "140", "--height", "34")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Mine (2)", "My Reviews (2)", "Watched (1)", "Ignored (0)", "tea-shop/kitchen#12", "[merged] Teapot", "✗ fail"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame lacks %q:\n%s", want, out)
		}
	}
}
