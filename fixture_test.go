package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests' GitHub: fakeGH answers gh api graphql from testdata/poll.json's
// PR nodes, building each answer from the query it is sent (the searches
// it names, one wN alias per watched PR), so a test can move PRs between
// searches and change their checks. No test runs the real gh.

var fixtureNow = time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/poll.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fakeGH struct {
	mu      sync.Mutex
	nodes   map[string]map[string]any // by URL
	mine    []string                  // URLs the mine search answers
	reviews []string
	calls   int
	bodies  []string
	fail    error
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	var doc struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(readFixture(t), &doc); err != nil {
		t.Fatal(err)
	}
	f := &fakeGH{nodes: map[string]map[string]any{}}
	for _, name := range []string{"mine", "reviews"} {
		var s struct {
			Nodes []map[string]any `json:"nodes"`
		}
		if err := json.Unmarshal(doc.Data[name], &s); err != nil {
			t.Fatal(err)
		}
		for _, n := range s.Nodes {
			u, _ := n["url"].(string)
			if u == "" {
				continue
			}
			f.nodes[u] = n
			if name == "mine" {
				f.mine = append(f.mine, u)
			} else {
				f.reviews = append(f.reviews, u)
			}
		}
	}
	var w0 struct {
		PullRequest map[string]any `json:"pullRequest"`
	}
	if err := json.Unmarshal(doc.Data["w0"], &w0); err != nil {
		t.Fatal(err)
	}
	f.nodes[w0.PullRequest["url"].(string)] = w0.PullRequest
	return f
}

var aliasRE = regexp.MustCompile(`w(\d+): repository\(owner: "([^"]+)", name: "([^"]+)"\) \{ pullRequest\(number: (\d+)\)`)

func (f *fakeGH) run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.bodies = append(f.bodies, string(stdin))
	if f.fail != nil {
		return nil, f.fail
	}
	if strings.Join(args, " ") != "api graphql --input -" {
		return nil, fmt.Errorf("fake gh: unexpected args %q", args)
	}
	var body struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(stdin, &body); err != nil {
		return nil, err
	}
	data := map[string]any{"viewer": map[string]any{"login": "miso"}}
	var errs []any
	search := func(urls []string) map[string]any {
		nodes := []any{}
		for _, u := range urls {
			nodes = append(nodes, f.nodes[u])
		}
		return map[string]any{"issueCount": len(nodes), "nodes": nodes}
	}
	if strings.Contains(body.Query, "mine: search") {
		data["mine"] = search(f.mine)
	}
	if strings.Contains(body.Query, "reviews: search") {
		data["reviews"] = search(f.reviews)
	}
	for _, m := range aliasRE.FindAllStringSubmatch(body.Query, -1) {
		n, _ := strconv.Atoi(m[4])
		u := PRKey{Owner: m[2], Name: m[3], Number: n}.URL()
		if node, ok := f.nodes[u]; ok {
			data["w"+m[1]] = map[string]any{"pullRequest": node}
		} else {
			data["w"+m[1]] = nil
			errs = append(errs, map[string]any{"message": "Could not resolve " + u})
		}
	}
	out := map[string]any{"data": data}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	return json.Marshal(out)
}

// setCI gives a PR one check in the given state (pass, fail, pending).
func (f *fakeGH) setCI(url, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status, conclusion := "COMPLETED", map[string]any{"pass": "SUCCESS", "fail": "FAILURE"}[state]
	if state == "pending" {
		status, conclusion = "IN_PROGRESS", nil
	}
	f.nodes[url]["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": "x",
		"statusCheckRollup": map[string]any{"state": "", "contexts": map[string]any{"nodes": []any{
			map[string]any{"__typename": "CheckRun", "name": "test", "status": status, "conclusion": conclusion}}}}}}}}
}

// inboxLog records the signals a fake inbox received.
type inboxLog struct {
	mu   sync.Mutex
	got  []Signal
	fail map[string]bool // URLs whose delivery fails
}

func (l *inboxLog) push(_ context.Context, s Signal) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail[s.URL] {
		return fmt.Errorf("inbox refused %s", s.URL)
	}
	l.got = append(l.got, s)
	return nil
}

// testTool is a Tool on a temp data and config directory with the fakes.
func testTool(t *testing.T) (*Tool, *fakeGH, *inboxLog, *bytes.Buffer) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("O_GH_DATA", "")
	gh, inbox := newFakeGH(t), &inboxLog{fail: map[string]bool{}}
	out := &bytes.Buffer{}
	tool := &Tool{store: Store{Dir: t.TempDir()}, gh: gh.run, inbox: inbox.push, now: func() time.Time { return fixtureNow },
		opener: func(context.Context, string) error { return nil }, clip: func(string) error { return nil },
		Stdin: strings.NewReader(""), Stdout: out, Stderr: &bytes.Buffer{}}
	if err := tool.newRuntime(true); err != nil {
		t.Fatal(err)
	}
	return tool, gh, inbox, out
}

// cfg is the default settings' Config.
func cfg(t *testing.T, tool *Tool) Config {
	t.Helper()
	return tool.config()
}

const (
	urlTimer  = "https://github.com/tea-shop/kitchen/pull/12"
	urlMenu   = "https://github.com/tea-shop/menu/pull/3"
	urlGrade  = "https://github.com/tea-shop/kitchen/pull/40"
	urlBump   = "https://github.com/tea-shop/kitchen/pull/41"
	urlTeapot = "https://github.com/pottery/teapot/pull/7"
	urlGone   = "https://github.com/pottery/gone/pull/1"
)
