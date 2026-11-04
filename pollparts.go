package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The poll is several small `gh api graphql --input -` requests, not one
// big one: GitHub's GraphQL times out (HTTP 502/503/504) on one document
// that searches three times and selects checks for every PR. Each part
// (the Mine search, the My Reviews search, the On-call search, and the PRs
// fetched by number in batches) is its own request that retries and fails
// on its own; a part that fails keeps its last known state.

const (
	defaultLimit = 25 // PRs per search when pr.limit is unset or out of range
	batchSize    = 20 // tracked PRs per by-number request
	concurrency  = 2  // requests in flight at once
	mineChecks   = 50 // contexts per PR on the Mine fragment
	detailChecks = 100
)

// Query is what one poll asks for.
type Query struct {
	Mine, Reviews string  // the search queries; "" leaves that search out
	Oncall        string  // the on-call review search; "" off call or unset
	Limit         int     // rows per search
	Watched       []PRKey // fetched by number, whatever their state
	// Detail asks the Watched PRs for everything (body, all checks): the
	// one-PR query behind pr.show and pr.view. Polls leave it off.
	Detail bool `json:"detail,omitempty"`
	// Closing are PRs fetched by number with the request fields only, to
	// learn whether a review request the last run saw has merged or closed.
	Closing []PRKey `json:"closing,omitempty"`
}

// frag says which fields a part selects.
type frag int

const (
	// fragRequest is for PRs that ask for your review: who they are, the
	// request (is it you), and the overall CI state. Nothing else.
	fragRequest frag = iota
	// fragMine is for your PRs and the tracked ones: metadata, the signals'
	// fields, the rollup and bounded checks.
	fragMine
	// fragDetail is fragMine plus the body and every check.
	fragDetail
)

const fragHead = `id number title url isDraft state updatedAt
  author { login }
  repository { nameWithOwner }`

const requestedReviewer = `requestedReviewer { ... on User { login } ... on Team { slug } }`

// fragment is the "pr" fragment for k; contexts says whether to select the
// individual checks (the rollup state is always selected).
func fragment(k frag, contexts bool) string {
	var b strings.Builder
	b.WriteString("fragment pr on PullRequest {\n  " + fragHead + "\n")
	if k == fragRequest {
		b.WriteString("  reviewRequests(first: 20) { nodes { " + requestedReviewer + " } }\n")
		b.WriteString("  commits(last: 1) { nodes { commit { oid statusCheckRollup { state } } } }\n}")
		return b.String()
	}
	b.WriteString("  reviewDecision mergeable createdAt headRefName baseRefName\n")
	if k == fragDetail {
		b.WriteString("  body\n")
	}
	b.WriteString("  labels(first: 10) { nodes { name } }\n")
	b.WriteString("  comments(last: 1) { totalCount nodes { author { login } } }\n")
	if k == fragDetail {
		b.WriteString("  reviewRequests(first: 20) { nodes { " + requestedReviewer + " } }\n")
		b.WriteString("  reviews(last: 30) { nodes { id author { login } state } }\n")
	} else {
		b.WriteString("  reviewRequests(first: 10) { nodes { " + requestedReviewer + " } }\n")
		b.WriteString("  reviews(last: 10) { nodes { id author { login } state } }\n")
	}
	b.WriteString("  commits(last: 1) { nodes { commit { oid statusCheckRollup { state")
	if contexts {
		n := mineChecks
		if k == fragDetail {
			n = detailChecks
		}
		fmt.Fprintf(&b, " contexts(first: %d) { nodes {\n    __typename\n    ... on CheckRun { name status conclusion detailsUrl }\n    ... on StatusContext { context state targetUrl } } }", n)
	}
	b.WriteString(" } } } } \n}")
	return b.String()
}

// part is one request's worth of the poll.
type part struct {
	name   string // mine, reviews, oncall, tracked, tracked 2/3, viewer
	search string // the search query ("" for a by-number part)
	alias  string // the search's alias
	keys   []PRKey
	frag   frag
	into   string // "closing": the answer goes to Poll.Closing
}

// parts splits q into its requests.
func parts(q Query) []part {
	var ps []part
	if q.Mine != "" {
		ps = append(ps, part{name: "mine", alias: "mine", search: q.Mine, frag: fragMine})
	}
	if q.Reviews != "" {
		ps = append(ps, part{name: "reviews", alias: "reviews", search: q.Reviews, frag: fragRequest})
	}
	if q.Oncall != "" {
		ps = append(ps, part{name: "oncall", alias: "oncall", search: q.Oncall, frag: fragRequest})
	}
	kind := fragMine
	if q.Detail {
		kind = fragDetail
	}
	n := (len(q.Watched) + batchSize - 1) / batchSize
	for i := 0; i < n; i++ {
		name := "tracked"
		if n > 1 {
			name = fmt.Sprintf("tracked %d/%d", i+1, n)
		}
		ps = append(ps, part{name: name, keys: q.Watched[i*batchSize : min((i+1)*batchSize, len(q.Watched))], frag: kind})
	}
	nc := (len(q.Closing) + batchSize - 1) / batchSize
	for i := 0; i < nc; i++ {
		ps = append(ps, part{name: fmt.Sprintf("requests %d/%d", i+1, nc), into: "closing", frag: fragRequest,
			keys: q.Closing[i*batchSize : min((i+1)*batchSize, len(q.Closing))]})
	}
	if len(ps) == 0 {
		ps = append(ps, part{name: "viewer"})
	}
	return ps
}

func clampLimit(n int) int {
	if n <= 0 || n > 100 {
		return defaultLimit
	}
	return n
}

// buildPart is the GraphQL document and variables for keys (a by-number
// part) or the search, `size` PRs wide.
func buildPart(pt part, keys []PRKey, size int, contexts bool) (string, map[string]any) {
	vars := map[string]any{}
	body := []string{"  viewer { login }"}
	var params []string
	if pt.search != "" {
		params = []string{"$first: Int!", "$q: String!"}
		vars["first"], vars["q"] = size, pt.search
		body = append(body, "  "+pt.alias+": search(query: $q, type: ISSUE, first: $first) { issueCount nodes { ...pr } }")
	}
	for i, k := range keys {
		// Owner and name are checked by parseKey; the literals are safe.
		body = append(body, fmt.Sprintf("  w%d: repository(owner: %q, name: %q) { pullRequest(number: %d) { ...pr } }", i, k.Owner, k.Name, k.Number))
	}
	head := "query"
	if len(params) > 0 {
		head += "(" + strings.Join(params, ", ") + ")"
	}
	doc := head + " {\n" + strings.Join(body, "\n") + "\n}\n"
	if pt.search != "" || len(keys) > 0 {
		doc += fragment(pt.frag, contexts) + "\n"
	}
	return doc, vars
}

// PartFailure is a part that never answered.
type PartFailure struct {
	Part    string   `json:"part"`
	Message string   `json:"message"`
	Keys    []string `json:"keys,omitempty"` // the PR URLs a by-number part asked for
	Tries   int      `json:"tries,omitempty"`
}

func (p Poll) hasFailed(part string) bool {
	for _, f := range p.Failed {
		if f.Part == part {
			return true
		}
	}
	return false
}

// failures is the failed parts' messages, joined.
func (p Poll) failures() string {
	var m []string
	for _, f := range p.Failed {
		m = append(m, f.Message)
	}
	return strings.Join(m, "; ")
}

// Sleeper waits d (or until ctx is done); tests replace it so no test
// sleeps for real.
type Sleeper func(ctx context.Context, d time.Duration)

func realSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// retryWait is the pause before a retry; it doubles each time.
const retryWait = 2 * time.Second

var httpCodeRE = regexp.MustCompile(`HTTP (\d{3})`)

// httpCode is the HTTP status gh reported in err (0 for none).
func httpCode(err error) int {
	if m := httpCodeRE.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// transient reports whether a request is worth trying again: GitHub's
// 502, 503 and 504. 401, 403 and everything else are not.
func transient(err error) bool {
	switch httpCode(err) {
	case 502, 503, 504:
		return true
	}
	return false
}

// request runs one document and parses it. gh exits non-zero on GraphQL
// errors but still prints the answer: a watched PR that is gone is an
// error beside good data.
func request(ctx context.Context, run Runner, q Query, pt part, keys []PRKey, size int, contexts bool, now time.Time) (Poll, error) {
	doc, vars := buildPart(pt, keys, size, contexts)
	body, err := json.Marshal(map[string]any{"query": doc, "variables": vars})
	if err != nil {
		return Poll{}, err
	}
	out, err := run(ctx, body, "api", "graphql", "--input", "-")
	sub := Query{Watched: keys}
	switch pt.alias {
	case "mine":
		sub.Mine = pt.search
	case "reviews":
		sub.Reviews = pt.search
	case "oncall":
		sub.Oncall = pt.search
	}
	if err != nil {
		if p, perr := parsePoll(out, sub, now); perr == nil && p.Viewer != "" {
			return p, nil
		}
		return Poll{}, err
	}
	return parsePoll(out, sub, now)
}

// attempt runs the part once at `size` (PRs per search, or per by-number
// request: the keys go in chunks of size).
func attempt(ctx context.Context, run Runner, q Query, pt part, size int, contexts bool, now time.Time) (Poll, error) {
	if pt.search != "" || len(pt.keys) == 0 {
		return request(ctx, run, q, pt, nil, size, contexts, now)
	}
	var all Poll
	for i := 0; i < len(pt.keys); i += size {
		p, err := request(ctx, run, q, pt, pt.keys[i:min(i+size, len(pt.keys))], size, contexts, now)
		if err != nil {
			return Poll{}, err
		}
		all = mergePolls(all, p, now)
	}
	return all, nil
}

// runPart runs a part with retries: try, wait and try again, wait and try
// at half the page size; a part with checks then tries once more without
// them (rollup state only). It stops at once on an error that is not a
// 502, 503 or 504.
func runPart(ctx context.Context, run Runner, sleep Sleeper, q Query, pt part, now time.Time) (Poll, *PartFailure) {
	full := clampLimit(q.Limit)
	if pt.search == "" {
		full = max(len(pt.keys), 1)
	}
	half := max((full+1)/2, 1)
	contexts := pt.frag != fragRequest
	type try struct {
		size     int
		contexts bool
	}
	tries := []try{{full, contexts}, {full, contexts}, {half, contexts}}
	if contexts && pt.frag == fragMine {
		tries = append(tries, try{half, false})
	}
	wait := retryWait
	var last error
	for i, t := range tries {
		if i > 0 {
			sleep(ctx, wait)
			wait *= 2
			if ctx.Err() != nil {
				break
			}
		}
		p, err := attempt(ctx, run, q, pt, t.size, t.contexts, now)
		if err == nil {
			if contexts && !t.contexts {
				p.Notes = append(p.Notes, "GitHub timed out on "+pt.name+" with checks: it answered with the rollup state only")
			}
			return p, nil
		}
		last = err
		if !transient(err) {
			return Poll{}, &PartFailure{Part: pt.name, Tries: i + 1, Message: fmt.Sprintf("%s: %v", pt.name, last)}
		}
		if i == len(tries)-1 {
			return Poll{}, &PartFailure{Part: pt.name, Tries: i + 1,
				Message: fmt.Sprintf("GitHub timed out on %s (HTTP %d) after %d tries", pt.name, httpCode(last), i+1)}
		}
	}
	return Poll{}, &PartFailure{Part: pt.name, Message: fmt.Sprintf("%s: %v", pt.name, last)}
}

// mergePolls adds o's lists to p.
func mergePolls(p, o Poll, now time.Time) Poll {
	if p.At.IsZero() {
		p = Poll{At: now.UTC(), Mine: []PR{}, Reviews: []PR{}, Watched: []PR{}, Oncall: []PR{}}
	}
	if p.Viewer == "" {
		p.Viewer = o.Viewer
	}
	p.Mine = append(p.Mine, o.Mine...)
	p.Reviews = append(p.Reviews, o.Reviews...)
	p.Oncall = append(p.Oncall, o.Oncall...)
	p.Watched = append(p.Watched, o.Watched...)
	p.MineTotal += o.MineTotal
	p.ReviewsTotal += o.ReviewsTotal
	p.OncallTotal += o.OncallTotal
	p.Asked = append(p.Asked, o.Asked...)
	p.Errors = append(p.Errors, o.Errors...)
	p.Notes = append(p.Notes, o.Notes...)
	return p
}

// poll runs the requests of q through run, concurrency at a time. A part
// that fails is in Failed and its list is empty (a failure, not an empty
// search); the poll fails only when every part does.
func poll(ctx context.Context, run Runner, q Query, now time.Time) (Poll, error) {
	return pollWith(ctx, run, realSleep, q, now)
}

func pollWith(ctx context.Context, run Runner, sleep Sleeper, q Query, now time.Time) (Poll, error) {
	ps := parts(q)
	res := make([]Poll, len(ps))
	fails := make([]*PartFailure, len(ps))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range ps {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res[i], fails[i] = runPart(ctx, run, sleep, q, ps[i], now)
		}(i)
	}
	wg.Wait()
	out := Poll{At: now.UTC(), Mine: []PR{}, Reviews: []PR{}, Watched: []PR{}, Oncall: []PR{}}
	nfail, total := 0, 0
	for i, pt := range ps {
		if pt.into != "closing" {
			total++
		}
		if f := fails[i]; f != nil && pt.into == "closing" {
			out.Notes = append(out.Notes, f.Message)
			continue
		} else if f != nil {
			nfail++
			for _, k := range pt.keys {
				f.Keys = append(f.Keys, k.URL())
			}
			out.Failed = append(out.Failed, *f)
			continue
		}
		if pt.into == "closing" {
			out.Closing = append(out.Closing, res[i].Watched...)
			continue
		}
		out = mergePolls(out, res[i], now)
	}
	if nfail > 0 && nfail == total {
		return Poll{}, fmt.Errorf("%s", out.failures())
	}
	return out, nil
}

// fillFailed puts the failed parts back from old (the last good poll) and
// returns the failures it could not fill.
func fillFailed(p *Poll, old Poll) []PartFailure {
	var left []PartFailure
	for _, f := range p.Failed {
		switch {
		case f.Part == "mine":
			p.Mine, p.MineTotal = old.Mine, old.MineTotal
		case f.Part == "reviews":
			p.Reviews, p.ReviewsTotal = old.Reviews, old.ReviewsTotal
		case f.Part == "oncall":
			p.Oncall, p.OncallTotal = old.Oncall, old.OncallTotal
		case len(f.Keys) > 0:
			for _, u := range f.Keys {
				for _, pr := range old.Watched {
					if pr.URL == u {
						p.Watched = append(p.Watched, pr)
					}
				}
				for _, a := range old.Asked {
					if a == u {
						p.Asked = append(p.Asked, u)
					}
				}
			}
		default:
			left = append(left, f)
			continue
		}
		p.Notes = append(p.Notes, f.Message+": kept the last answer for "+f.Part)
	}
	return left
}
