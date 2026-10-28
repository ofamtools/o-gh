package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The poll: one `gh api graphql --input -` request answers the viewer,
// both searches (Mine and My Reviews) and every watched PR, each watched
// one an alias (w0, w1, ...) of repository(owner, name).pullRequest(number),
// so a PR is watched by its URL alone. Adapted from matomeru-old's
// internal/github (prs.go: pollQuery, pollNode, applyNode, ciState).

// Runner runs gh with args, stdin as its standard input, and returns its
// standard output. Tests replace it; ghRunner is the real one.
type Runner func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)

// ghTimeout bounds one gh call.
const ghTimeout = 60 * time.Second

// ghRunner runs the gh at bin ($O_GH_GH, else gh on PATH).
func ghRunner(bin string) Runner {
	if bin == "" {
		bin = "gh"
	}
	return func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, ghTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			head := strings.Join(args[:min(2, len(args))], " ")
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return out, fmt.Errorf("gh %s: %w: %s", head, err, msg)
			}
			return out, fmt.Errorf("gh %s: %w", head, err)
		}
		return out, nil
	}
}

// prFragment is every field a PR row, its detail and the signals read.
const prFragment = `fragment pr on PullRequest {
  id number title url isDraft state mergeable reviewDecision createdAt updatedAt
  author { login }
  repository { nameWithOwner }
  headRefName baseRefName
  labels(first: 10) { nodes { name } }
  comments { totalCount }
  reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } ... on Team { slug } } } }
  reviews(last: 30) { nodes { author { login } state } }
  commits(last: 1) { nodes { commit { oid statusCheckRollup { state contexts(first: 100) { nodes {
    __typename
    ... on CheckRun { name status conclusion detailsUrl }
    ... on StatusContext { context state targetUrl } } } } } } }
}`

// Query is what one poll asks for.
type Query struct {
	Mine, Reviews string  // the search queries; "" leaves that search out
	Limit         int     // rows per search
	Watched       []PRKey // fetched by number, whatever their state
}

// buildQuery is the GraphQL document and its variables for q.
func buildQuery(q Query) (string, map[string]any) {
	var params, body []string
	vars := map[string]any{}
	body = append(body, "  viewer { login }")
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if q.Mine != "" || q.Reviews != "" {
		params = append(params, "$first: Int!")
		vars["first"] = limit
	}
	if q.Mine != "" {
		params = append(params, "$mine: String!")
		vars["mine"] = q.Mine
		body = append(body, "  mine: search(query: $mine, type: ISSUE, first: $first) { issueCount nodes { ...pr } }")
	}
	if q.Reviews != "" {
		params = append(params, "$reviews: String!")
		vars["reviews"] = q.Reviews
		body = append(body, "  reviews: search(query: $reviews, type: ISSUE, first: $first) { issueCount nodes { ...pr } }")
	}
	for i, k := range q.Watched {
		// Owner and name are checked by parseKey; the literals are safe.
		body = append(body, fmt.Sprintf("  w%d: repository(owner: %q, name: %q) { pullRequest(number: %d) { ...pr } }", i, k.Owner, k.Name, k.Number))
	}
	head := "query"
	if len(params) > 0 {
		head += "(" + strings.Join(params, ", ") + ")"
	}
	return head + " {\n" + strings.Join(body, "\n") + "\n}\n" + prFragment + "\n", vars
}

// poll runs one query through gh.
func poll(ctx context.Context, run Runner, q Query, now time.Time) (Poll, error) {
	doc, vars := buildQuery(q)
	body, err := json.Marshal(map[string]any{"query": doc, "variables": vars})
	if err != nil {
		return Poll{}, err
	}
	out, err := run(ctx, body, "api", "graphql", "--input", "-")
	if err != nil {
		// gh exits non-zero on GraphQL errors but still prints the answer:
		// a watched PR that is gone is an error beside good data.
		if p, perr := parsePoll(out, q, now); perr == nil && p.Viewer != "" {
			return p, nil
		}
		return Poll{}, err
	}
	return parsePoll(out, q, now)
}

// Poll is one poll's answer.
type Poll struct {
	At           time.Time `json:"at"`
	Viewer       string    `json:"viewer"`
	Mine         []PR      `json:"mine"`
	Reviews      []PR      `json:"reviews"`
	Watched      []PR      `json:"watched"`
	MineTotal    int       `json:"mine_total"`
	ReviewsTotal int       `json:"reviews_total"`
	// Asked are the watched PRs the poll asked for (URLs), found or not.
	Asked []string `json:"asked,omitempty"`
	// Errors are GraphQL errors that came with data (a watched PR gone).
	Errors []string `json:"errors,omitempty"`
}

// PR is one pull request as the poll read it.
type PR struct {
	NodeID         string   `json:"node_id"`
	Key            string   `json:"key"` // owner/name#number
	URL            string   `json:"url"`
	Repo           string   `json:"repo"`
	Number         int      `json:"number"`
	Title          string   `json:"title"`
	Author         string   `json:"author"`
	Draft          bool     `json:"draft"`
	State          string   `json:"state"` // OPEN, CLOSED, MERGED
	Mergeable      string   `json:"mergeable,omitempty"`
	ReviewDecision string   `json:"review_decision,omitempty"`
	Created        string   `json:"created_at,omitempty"`
	Updated        string   `json:"updated_at,omitempty"`
	Head           string   `json:"head,omitempty"`
	Base           string   `json:"base,omitempty"`
	HeadSHA        string   `json:"head_sha,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	Comments       int      `json:"comments"`
	Requested      []string `json:"requested,omitempty"` // logins and team slugs
	Reviews        []Review `json:"reviews,omitempty"`
	Rollup         string   `json:"rollup,omitempty"` // statusCheckRollup.state
	Checks         []Check  `json:"checks,omitempty"`
}

// Check is one CI check or status context; State is pass, fail, pending,
// skip or neutral.
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// Review is one submitted review.
type Review struct {
	Who   string `json:"who"`
	State string `json:"state"`
}

// node is a PullRequest as the fragment answers it.
type node struct {
	ID             string `json:"id"`
	Number         int    `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	IsDraft        bool   `json:"isDraft"`
	State          string `json:"state"`
	Mergeable      string `json:"mergeable"`
	ReviewDecision string `json:"reviewDecision"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
	Author         *struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	Labels      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *struct {
				Login string `json:"login"`
				Slug  string `json:"slug"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Reviews struct {
		Nodes []struct {
			Author *struct {
				Login string `json:"login"`
			} `json:"author"`
			State string `json:"state"`
		} `json:"nodes"`
	} `json:"reviews"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID               string `json:"oid"`
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []struct {
							Typename   string `json:"__typename"`
							Name       string `json:"name"`
							Status     string `json:"status"`
							Conclusion string `json:"conclusion"`
							DetailsURL string `json:"detailsUrl"`
							Context    string `json:"context"`
							State      string `json:"state"`
							TargetURL  string `json:"targetUrl"`
						} `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type search struct {
	IssueCount int     `json:"issueCount"`
	Nodes      []*node `json:"nodes"`
}

// parsePoll reads a poll's answer. A search node that is not a PR (an
// empty object) is skipped; a watched alias that is null is left out of
// Watched (its error, if any, is in Errors).
func parsePoll(out []byte, q Query, now time.Time) (Poll, error) {
	var reply struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return Poll{}, fmt.Errorf("gh api graphql: not JSON: %w", err)
	}
	p := Poll{At: now.UTC(), Mine: []PR{}, Reviews: []PR{}, Watched: []PR{}}
	for _, e := range reply.Errors {
		p.Errors = append(p.Errors, e.Message)
	}
	if reply.Data == nil {
		if len(p.Errors) > 0 {
			return Poll{}, fmt.Errorf("gh api graphql: %s", strings.Join(p.Errors, "; "))
		}
		return Poll{}, errors.New("gh api graphql: no data")
	}
	var viewer struct {
		Login string `json:"login"`
	}
	if raw, ok := reply.Data["viewer"]; ok {
		_ = json.Unmarshal(raw, &viewer)
	}
	p.Viewer = viewer.Login
	readSearch := func(name string) ([]PR, int, error) {
		raw, ok := reply.Data[name]
		if !ok || string(raw) == "null" {
			return []PR{}, 0, nil
		}
		var s search
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, 0, fmt.Errorf("gh api graphql: %s: %w", name, err)
		}
		prs := []PR{}
		for _, n := range s.Nodes {
			if n != nil && n.URL != "" {
				prs = append(prs, n.pr())
			}
		}
		return prs, s.IssueCount, nil
	}
	var err error
	if p.Mine, p.MineTotal, err = readSearch("mine"); err != nil {
		return Poll{}, err
	}
	if p.Reviews, p.ReviewsTotal, err = readSearch("reviews"); err != nil {
		return Poll{}, err
	}
	for i, k := range q.Watched {
		p.Asked = append(p.Asked, k.URL())
		raw, ok := reply.Data["w"+strconv.Itoa(i)]
		if !ok || string(raw) == "null" {
			continue
		}
		var repo struct {
			PullRequest *node `json:"pullRequest"`
		}
		if err := json.Unmarshal(raw, &repo); err != nil {
			return Poll{}, fmt.Errorf("gh api graphql: w%d: %w", i, err)
		}
		if repo.PullRequest != nil && repo.PullRequest.URL != "" {
			p.Watched = append(p.Watched, repo.PullRequest.pr())
		}
	}
	return p, nil
}

// pr is the node as a PR row.
func (n *node) pr() PR {
	pr := PR{
		NodeID: n.ID, URL: n.URL, Repo: n.Repository.NameWithOwner, Number: n.Number, Title: n.Title,
		Draft: n.IsDraft, State: n.State, Mergeable: n.Mergeable, ReviewDecision: n.ReviewDecision,
		Created: n.CreatedAt, Updated: n.UpdatedAt, Head: n.HeadRefName, Base: n.BaseRefName,
		Comments: n.Comments.TotalCount,
	}
	if k, err := parseKey(n.URL); err == nil {
		pr.Key = k.String()
		if pr.Repo == "" {
			pr.Repo = k.Owner + "/" + k.Name
		}
	} else {
		pr.Key = fmt.Sprintf("%s#%d", pr.Repo, pr.Number)
	}
	if n.Author != nil {
		pr.Author = n.Author.Login
	}
	for _, l := range n.Labels.Nodes {
		pr.Labels = append(pr.Labels, l.Name)
	}
	for _, rr := range n.ReviewRequests.Nodes {
		if r := rr.RequestedReviewer; r != nil {
			who := r.Login
			if who == "" && r.Slug != "" {
				who = "team:" + r.Slug
			}
			if who != "" {
				pr.Requested = append(pr.Requested, who)
			}
		}
	}
	for _, r := range n.Reviews.Nodes {
		who := ""
		if r.Author != nil {
			who = r.Author.Login
		}
		pr.Reviews = append(pr.Reviews, Review{Who: who, State: r.State})
	}
	if len(n.Commits.Nodes) > 0 {
		c := n.Commits.Nodes[0].Commit
		pr.HeadSHA = c.OID
		if roll := c.StatusCheckRollup; roll != nil {
			pr.Rollup = roll.State
			for _, ctx := range roll.Contexts.Nodes {
				if ctx.Name != "" || ctx.Typename == "CheckRun" {
					pr.Checks = append(pr.Checks, Check{Name: ctx.Name, URL: ctx.DetailsURL, State: checkRunState(ctx.Status, ctx.Conclusion)})
				} else if ctx.Context != "" {
					pr.Checks = append(pr.Checks, Check{Name: ctx.Context, URL: ctx.TargetURL, State: statusState(ctx.State)})
				}
			}
		}
	}
	return pr
}

// checkRunState is a CheckRun's state: pending until it completes, then by
// its conclusion.
func checkRunState(status, conclusion string) string {
	if strings.ToUpper(status) != "COMPLETED" && status != "" {
		return "pending"
	}
	switch strings.ToUpper(conclusion) {
	case "SUCCESS":
		return "pass"
	case "FAILURE", "TIMED_OUT", "CANCELLED", "STARTUP_FAILURE", "ACTION_REQUIRED", "ERROR":
		return "fail"
	case "SKIPPED":
		return "skip"
	case "NEUTRAL", "STALE":
		return "neutral"
	case "":
		return "pending"
	}
	return strings.ToLower(conclusion)
}

// statusState is a StatusContext's state.
func statusState(state string) string {
	switch strings.ToUpper(state) {
	case "SUCCESS":
		return "pass"
	case "FAILURE", "ERROR":
		return "fail"
	case "PENDING", "EXPECTED":
		return "pending"
	}
	return strings.ToLower(state)
}

// rollupState is a statusCheckRollup state as a CI state (matomeru-old's
// ciState).
func rollupState(rollup string) string {
	switch strings.ToUpper(rollup) {
	case "SUCCESS":
		return "pass"
	case "FAILURE", "ERROR":
		return "fail"
	case "PENDING", "EXPECTED":
		return "pending"
	case "":
		return "none"
	}
	return strings.ToLower(rollup)
}

// PRKey names a PR: owner/name#number.
type PRKey struct {
	Owner, Name string
	Number      int
}

func (k PRKey) String() string { return fmt.Sprintf("%s/%s#%d", k.Owner, k.Name, k.Number) }

// URL is the PR's github.com URL.
func (k PRKey) URL() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", k.Owner, k.Name, k.Number)
}

var (
	ghName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	keyRE    = regexp.MustCompile(`^([^/\s#]+)/([^/\s#]+)#(\d+)$`)
	prURLRE  = regexp.MustCompile(`^https?://github\.com/([^/\s]+)/([^/\s]+)/pull/(\d+)(?:[/?#].*)?$`)
	rowIDPfx = "pr/"
)

// parseKey reads a PR's URL, its key (owner/name#N) or its row id
// (pr/owner/name#N).
func parseKey(s string) (PRKey, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), rowIDPfx))
	m := prURLRE.FindStringSubmatch(s)
	if m == nil {
		m = keyRE.FindStringSubmatch(s)
	}
	if m == nil {
		return PRKey{}, fmt.Errorf("%q names no PR: give its URL, OWNER/REPO#N or a row id pr/OWNER/REPO#N", s)
	}
	if !ghName.MatchString(m[1]) || !ghName.MatchString(m[2]) {
		return PRKey{}, fmt.Errorf("%q: not a GitHub owner/repository", m[1]+"/"+m[2])
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return PRKey{}, fmt.Errorf("%q: not a PR number", m[3])
	}
	return PRKey{Owner: m[1], Name: m[2], Number: n}, nil
}

// sortPRs orders PRs newest update first, then by key.
func sortPRs(prs []PR) {
	sort.SliceStable(prs, func(i, j int) bool {
		if prs[i].Updated != prs[j].Updated {
			return prs[i].Updated > prs[j].Updated
		}
		return prs[i].Key < prs[j].Key
	})
}

// envOr is $name, else fallback.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
