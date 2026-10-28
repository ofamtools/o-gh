package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fixtureQuery() Query {
	return Query{Mine: defaultMine, Reviews: defaultReviews, Limit: 50, Watched: []PRKey{
		{Owner: "pottery", Name: "teapot", Number: 7}, {Owner: "pottery", Name: "gone", Number: 1},
	}}
}

func TestParsePollFixture(t *testing.T) {
	p, err := parsePoll(readFixture(t), fixtureQuery(), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if p.Viewer != "miso" || p.MineTotal != 2 || p.ReviewsTotal != 2 || !p.At.Equal(fixtureNow) {
		t.Fatalf("header: %+v", p)
	}
	if len(p.Mine) != 2 || len(p.Reviews) != 2 || len(p.Watched) != 1 {
		t.Fatalf("counts: mine %d reviews %d (the {} node skipped) watched %d (w1 null skipped)", len(p.Mine), len(p.Reviews), len(p.Watched))
	}
	if want := []string{urlTeapot, urlGone}; !reflect.DeepEqual(p.Asked, want) {
		t.Fatalf("asked %v, want %v", p.Asked, want)
	}
	if len(p.Errors) != 1 || !strings.Contains(p.Errors[0], "pottery/gone") {
		t.Fatalf("errors %v", p.Errors)
	}
	timer := p.Mine[0]
	want := PR{
		NodeID: "PR_mine1", Key: "tea-shop/kitchen#12", URL: urlTimer, Repo: "tea-shop/kitchen", Number: 12,
		Title: "Add the rice cooker timer", Author: "miso", State: "OPEN", Mergeable: "MERGEABLE", ReviewDecision: "REVIEW_REQUIRED",
		Created: "2026-10-20T09:00:00Z", Updated: "2026-10-27T10:00:00Z", Head: "timer", Base: "main", HeadSHA: "aaa111",
		Labels: []string{"feature"}, Comments: 2, Requested: []string{"nori", "team:cooks"},
		Reviews: []Review{{Who: "nori", State: "COMMENTED"}}, Rollup: "FAILURE",
		Checks: []Check{
			{Name: "test", State: "pass", URL: "https://ci.example/test"},
			{Name: "lint (optional)", State: "fail", URL: "https://ci.example/lint"},
			{Name: "deploy/preview", State: "pass", URL: "https://ci.example/deploy"},
		},
	}
	if !reflect.DeepEqual(timer, want) {
		gotj, _ := json.MarshalIndent(timer, "", " ")
		t.Fatalf("timer PR:\n%s", gotj)
	}
	menu := p.Mine[1]
	if !menu.Draft || menu.Checks[0].State != "pending" || menu.ReviewDecision != "" {
		t.Fatalf("menu PR: %+v", menu)
	}
	if p.Reviews[0].Rollup != "" || p.Reviews[0].Checks != nil {
		t.Fatalf("a null rollup: %+v", p.Reviews[0])
	}
	teapot := p.Watched[0]
	if teapot.State != "MERGED" || teapot.Reviews[1].Who != "" || teapot.Checks[0].State != "skip" {
		t.Fatalf("teapot PR: %+v", teapot)
	}
}

func TestParsePollErrors(t *testing.T) {
	if _, err := parsePoll([]byte("not json"), Query{}, fixtureNow); err == nil {
		t.Fatal("not JSON parsed")
	}
	_, err := parsePoll([]byte(`{"errors":[{"message":"Bad credentials"}]}`), Query{}, fixtureNow)
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("no data: %v", err)
	}
}

func TestPollKeepsDataBesideAnError(t *testing.T) {
	// gh exits non-zero when the answer carries errors; the data still counts.
	fix := readFixture(t)
	run := func(context.Context, []byte, ...string) ([]byte, error) { return fix, errors.New("exit status 1") }
	p, err := poll(context.Background(), run, fixtureQuery(), fixtureNow)
	if err != nil || len(p.Mine) != 2 {
		t.Fatalf("poll: %v %+v", err, p)
	}
	fail := func(context.Context, []byte, ...string) ([]byte, error) { return nil, errors.New("gh: offline") }
	if _, err := poll(context.Background(), fail, fixtureQuery(), fixtureNow); err == nil {
		t.Fatal("a failed gh polled")
	}
}

func TestBuildQuery(t *testing.T) {
	doc, vars := buildQuery(fixtureQuery())
	for _, want := range []string{
		"query($first: Int!, $mine: String!, $reviews: String!) {",
		"mine: search(query: $mine, type: ISSUE, first: $first)",
		"reviews: search(query: $reviews, type: ISSUE, first: $first)",
		`w0: repository(owner: "pottery", name: "teapot") { pullRequest(number: 7) { ...pr } }`,
		`w1: repository(owner: "pottery", name: "gone") { pullRequest(number: 1) { ...pr } }`,
		"fragment pr on PullRequest",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("query lacks %q:\n%s", want, doc)
		}
	}
	if vars["mine"] != defaultMine || vars["reviews"] != defaultReviews || vars["first"] != 50 {
		t.Fatalf("vars %v", vars)
	}
	// One PR alone: no searches, no variables.
	doc, vars = buildQuery(Query{Watched: []PRKey{{Owner: "a", Name: "b", Number: 1}}})
	if strings.Contains(doc, "search(") || !strings.HasPrefix(doc, "query {") || len(vars) != 0 {
		t.Fatalf("watched only:\n%s %v", doc, vars)
	}
	if _, vars = buildQuery(Query{Mine: "x", Limit: 500}); vars["first"] != 50 {
		t.Fatalf("limit not clamped: %v", vars)
	}
}

func TestParseKey(t *testing.T) {
	for _, s := range []string{
		"https://github.com/tea-shop/kitchen/pull/12", "https://github.com/tea-shop/kitchen/pull/12/files",
		"tea-shop/kitchen#12", "pr/tea-shop/kitchen#12", " tea-shop/kitchen#12 ",
	} {
		k, err := parseKey(s)
		if err != nil || k.String() != "tea-shop/kitchen#12" || k.URL() != urlTimer {
			t.Errorf("%q: %v %v", s, k, err)
		}
	}
	for _, s := range []string{"", "tea-shop/kitchen", "https://github.com/a/b/issues/1", `a"b/c#1`, "a/b#0", "https://gitlab.com/a/b/pull/1"} {
		if _, err := parseKey(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestCheckStates(t *testing.T) {
	for _, c := range []struct{ status, conclusion, want string }{
		{"COMPLETED", "SUCCESS", "pass"}, {"COMPLETED", "FAILURE", "fail"}, {"COMPLETED", "TIMED_OUT", "fail"},
		{"COMPLETED", "CANCELLED", "fail"}, {"COMPLETED", "SKIPPED", "skip"}, {"COMPLETED", "NEUTRAL", "neutral"},
		{"IN_PROGRESS", "", "pending"}, {"QUEUED", "", "pending"}, {"COMPLETED", "", "pending"},
	} {
		if got := checkRunState(c.status, c.conclusion); got != c.want {
			t.Errorf("%s/%s: %s, want %s", c.status, c.conclusion, got, c.want)
		}
	}
	for in, want := range map[string]string{"SUCCESS": "pass", "ERROR": "fail", "FAILURE": "fail", "PENDING": "pending", "EXPECTED": "pending"} {
		if got := statusState(in); got != want {
			t.Errorf("status %s: %s", in, got)
		}
	}
}
