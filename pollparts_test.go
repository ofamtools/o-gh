package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// scripted runs the requests of a poll through a fake that answers by the
// document: answer(doc) returns the gh output or an error.
type scripted struct {
	docs   []string
	sleeps []time.Duration
	answer func(doc string, n int) ([]byte, error)
	n      atomic.Int32
}

func (s *scripted) run(_ context.Context, stdin []byte, _ ...string) ([]byte, error) {
	doc := string(stdin)
	s.docs = append(s.docs, doc)
	return s.answer(doc, int(s.n.Add(1)))
}

func (s *scripted) sleep(_ context.Context, d time.Duration) { s.sleeps = append(s.sleeps, d) }

func TestPollIsSeparateSlimRequests(t *testing.T) {
	gh := newFakeGH(t)
	q := Query{Mine: defaultMine, Reviews: defaultReviews, Oncall: "is:pr team-review-requested:x", Limit: 25}
	for i := 0; i < 45; i++ {
		q.Watched = append(q.Watched, PRKey{Owner: "o", Name: "r", Number: i + 1})
	}
	p, err := pollWith(context.Background(), gh.run, func(context.Context, time.Duration) {}, q, fixtureNow)
	if err != nil || len(p.Failed) != 0 {
		t.Fatalf("%v %+v", err, p.Failed)
	}
	if want := 3 + 3; gh.calls != want { // mine, reviews, oncall, tracked 20+20+5
		t.Fatalf("%d requests, want %d", gh.calls, want)
	}
	var mine, reviews, tracked int
	for _, doc := range gh.bodies {
		searches := strings.Count(doc, "search(query")
		aliases := strings.Count(doc, "repository(owner")
		if searches+boolInt(aliases > 0) != 1 {
			t.Fatalf("a request mixes parts:\n%s", doc)
		}
		switch {
		case strings.Contains(doc, "mine: search"):
			mine++
			if !strings.Contains(doc, "contexts(first: 50)") || !strings.Contains(doc, "mergeable") || strings.Contains(doc, "body") {
				t.Fatalf("mine fragment:\n%s", doc)
			}
		case strings.Contains(doc, "reviews: search"), strings.Contains(doc, "oncall: search"):
			reviews++
			for _, bad := range []string{"contexts(", "body", "comments(", "reviews(last", "mergeable", "labels("} {
				if strings.Contains(doc, bad) {
					t.Fatalf("a review-request request selects %q:\n%s", bad, doc)
				}
			}
			for _, want := range []string{"reviewRequests(first: 20)", "statusCheckRollup { state }", "isDraft", "updatedAt"} {
				if !strings.Contains(doc, want) {
					t.Fatalf("a review-request request lacks %q:\n%s", want, doc)
				}
			}
		default:
			tracked++
			if n := strings.Count(doc, "repository(owner"); n > 20 {
				t.Fatalf("%d aliases in a batch", n)
			}
			if !strings.Contains(doc, "contexts(first: 50)") || strings.Contains(doc, "body") {
				t.Fatalf("tracked fragment:\n%s", doc)
			}
		}
	}
	if mine != 1 || reviews != 2 || tracked != 3 {
		t.Fatalf("mine %d reviews %d tracked %d", mine, reviews, tracked)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestDetailFetchesContextsAndBody(t *testing.T) {
	tool, gh, _, _ := testTool(t)
	it, err := tool.item(context.Background(), cfg(t, tool), PRKey{Owner: "tea-shop", Name: "kitchen", Number: 12})
	if err != nil {
		t.Fatal(err)
	}
	last := gh.bodies[len(gh.bodies)-1]
	if !strings.Contains(last, "contexts(first: 100)") || !strings.Contains(last, "body") || strings.Count(last, "repository(owner") != 1 {
		t.Fatalf("detail request:\n%s", last)
	}
	if it.Body == "" || len(it.Checks) == 0 {
		t.Fatalf("detail lacks body or checks: %+v", it.PR)
	}
}

func okReply(q string) []byte {
	return []byte(`{"data":{"viewer":{"login":"miso"},"` + q + `":{"issueCount":0,"nodes":[]}}}`)
}

func TestRetryThenHalve(t *testing.T) {
	s := &scripted{}
	s.answer = func(doc string, n int) ([]byte, error) {
		if n < 3 {
			return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 504")
		}
		return okReply("reviews"), nil
	}
	p, err := pollWith(context.Background(), s.run, s.sleep, Query{Reviews: "r", Limit: 40}, fixtureNow)
	if err != nil || len(p.Failed) != 0 {
		t.Fatalf("%v %+v", err, p.Failed)
	}
	if len(s.docs) != 3 || !strings.Contains(s.docs[0], `"first":40`) || !strings.Contains(s.docs[1], `"first":40`) || !strings.Contains(s.docs[2], `"first":20`) {
		t.Fatalf("pages: %v", s.docs)
	}
	if len(s.sleeps) != 2 {
		t.Fatalf("sleeps %v", s.sleeps)
	}
}

func TestTimeoutIsReportedAndOtherPartsStay(t *testing.T) {
	s := &scripted{}
	s.answer = func(doc string, n int) ([]byte, error) {
		if strings.Contains(doc, "reviews: search") {
			return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 502")
		}
		return okReply("mine"), nil
	}
	p, err := pollWith(context.Background(), s.run, s.sleep, Query{Mine: "m", Reviews: "r"}, fixtureNow)
	if err != nil || len(p.Failed) != 1 || p.Failed[0].Part != "reviews" {
		t.Fatalf("%v %+v", err, p.Failed)
	}
	if got, want := p.Failed[0].Message, "GitHub timed out on reviews (HTTP 502) after 3 tries"; got != want {
		t.Fatalf("message %q", got)
	}
	// Every part failing is a failed poll.
	s2 := &scripted{answer: func(string, int) ([]byte, error) { return nil, errors.New("gh: HTTP 503") }}
	if _, err := pollWith(context.Background(), s2.run, s2.sleep, Query{Mine: "m"}, fixtureNow); err == nil || !strings.Contains(err.Error(), "GitHub timed out on mine (HTTP 503)") {
		t.Fatalf("err %v", err)
	}
}

func TestMineFallsBackToRollupOnly(t *testing.T) {
	s := &scripted{}
	s.answer = func(doc string, n int) ([]byte, error) {
		if strings.Contains(doc, "contexts(") {
			return nil, errors.New("gh: HTTP 504")
		}
		return okReply("mine"), nil
	}
	p, err := pollWith(context.Background(), s.run, s.sleep, Query{Mine: "m"}, fixtureNow)
	if err != nil || len(p.Failed) != 0 || len(p.Notes) != 1 || len(s.docs) != 4 {
		t.Fatalf("%v %+v %d", err, p, len(s.docs))
	}
}

func TestNoRetryOnAuth(t *testing.T) {
	for _, code := range []int{401, 403} {
		s := &scripted{answer: func(string, int) ([]byte, error) { return nil, fmt.Errorf("gh: HTTP %d: bad", code) }}
		_, err := pollWith(context.Background(), s.run, s.sleep, Query{Mine: "m"}, fixtureNow)
		if err == nil || len(s.docs) != 1 || len(s.sleeps) != 0 {
			t.Fatalf("HTTP %d: %v, %d requests, %d sleeps", code, err, len(s.docs), len(s.sleeps))
		}
	}
}

// A failed part keeps the last run's record and does not re-signal later.
func TestFailedPartKeepsSnapshotAndDoesNotResignal(t *testing.T) {
	tool, gh, inbox, _ := testTool(t)
	ctx := context.Background()
	c := cfg(t, tool)
	if _, err := tool.runSignals(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	sent := len(inbox.got)
	snap := tool.lastSnapshot()
	if len(snap.Reviews) == 0 || len(snap.PRs) == 0 {
		t.Fatalf("snapshot %+v", snap)
	}
	// Both searches time out.
	real := tool.gh
	tool.gh = func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
		if d := string(stdin); strings.Contains(d, "reviews: search") || strings.Contains(d, "mine: search") {
			return nil, errors.New("gh: HTTP 504")
		}
		return real(ctx, stdin, args...)
	}
	run, err := tool.runSignals(ctx, c, false)
	if err == nil || !strings.Contains(err.Error(), "GitHub timed out on reviews (HTTP 504) after 3 tries") || len(run.Signals) != 0 {
		t.Fatalf("run %+v err %v", run, err)
	}
	after := tool.lastSnapshot()
	if len(after.Reviews) != len(snap.Reviews) || len(after.PRs) != len(snap.PRs) {
		t.Fatalf("snapshot lost a part: before %+v after %+v", snap, after)
	}
	// GitHub answers again: nothing is new.
	tool.gh = real
	run, err = tool.runSignals(ctx, c, false)
	if err != nil || len(run.Signals) != 0 || len(inbox.got) != sent {
		t.Fatalf("re-signalled: %v %v", err, kinds(run.Signals))
	}
	_ = gh
}

func TestFirstRunWithAFailedPartRecordsNothing(t *testing.T) {
	tool, _, inbox, _ := testTool(t)
	real := tool.gh
	tool.gh = func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
		if strings.Contains(string(stdin), "reviews: search") {
			return nil, errors.New("gh: HTTP 504")
		}
		return real(ctx, stdin, args...)
	}
	_, err := tool.runSignals(context.Background(), cfg(t, tool), false)
	if err == nil || tool.lastSnapshot() != nil || len(inbox.got) != 0 {
		t.Fatalf("err %v snap %v sent %d", err, tool.lastSnapshot(), len(inbox.got))
	}
}

func TestMergedReviewRequestArchivesItsItem(t *testing.T) {
	tool, gh, inbox, _ := testTool(t)
	ctx := context.Background()
	c := cfg(t, tool)
	if _, err := tool.runSignals(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	if len(tool.lastSnapshot().Reviews) == 0 {
		t.Fatal("no review request recorded")
	}
	url := tool.lastSnapshot().Reviews[0]
	gh.setState(url, "MERGED")
	run, err := tool.runSignals(ctx, c, false)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := parseKey(url)
	want := "review-requested:" + key.String() + ":miso"
	found := false
	for _, k := range inbox.archived {
		found = found || k == want
	}
	if !found {
		t.Fatalf("archived %v, want %s (signals %v)", inbox.archived, want, kinds(run.Signals))
	}
	for _, u := range tool.lastSnapshot().Reviews {
		if u == url {
			t.Fatal("a merged request is still recorded")
		}
	}
}

func TestReviewWhyCarriesCI(t *testing.T) {
	if ciWhy("fail") != " (CI failing)" || ciWhy("none") != "" {
		t.Fatal("ciWhy")
	}
}
