package main

import (
	"context"
	"fmt"
	"strings"

	"ofam.dev/ochazuke"
)

// pr.show: the PR as Markdown to read, where pr.get answers the record.
// The verb declares MarkdownResult, so the palette, mtm's page and the
// viewer open it as a document; the CLI prints the text as it is.

func (t *Tool) viewPR(ctx context.Context, cfg Config, k PRKey, _ ochazuke.Arguments) (any, error) {
	it, err := t.item(ctx, cfg, k)
	if err != nil {
		return nil, err
	}
	return viewText(it), nil
}

// viewText is the readable view of one PR item.
func viewText(it Item) string {
	var b strings.Builder
	title := it.Title
	if title == "" {
		title = it.Key
	}
	fmt.Fprintf(&b, "# %s\n\n", oneLine(title))
	line := func(label, value string) {
		if value == "" {
			value = "-"
		}
		fmt.Fprintf(&b, "- **%s:** %s\n", label, value)
	}
	line("PR", fmt.Sprintf("#%d", it.Number))
	line("Repo", it.Repo)
	line("Author", it.Author)
	line("Status", strings.ToLower(it.Status))
	line("Draft", yesNo(it.Draft))
	line("Mergeable", strings.ToLower(it.Mergeable))
	line("CI", viewCI(it))
	line("Review", viewReview(it.PR))
	line("Reviewers", viewReviewers(it.PR))
	line("Comments", fmt.Sprint(it.Comments))
	line("Branch", viewBranch(it.PR))
	if len(it.Labels) > 0 {
		line("Labels", strings.Join(it.Labels, ", "))
	}
	line("URL", it.URL)
	b.WriteString("\n## Description\n\n")
	body := strings.TrimSpace(strings.ReplaceAll(it.Body, "\r\n", "\n"))
	if body == "" {
		body = "No description."
	}
	b.WriteString(body + "\n")
	b.WriteString("\n## Checks\n\n")
	if len(it.Checks) == 0 {
		b.WriteString("No checks.\n")
	}
	for _, c := range it.Checks {
		fmt.Fprintf(&b, "- %s %s\n", ciText(c.Status), oneLine(c.Name))
	}
	return b.String()
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// viewCI is the CI state with the count of checks behind it.
func viewCI(it Item) string {
	if len(it.Checks) == 0 {
		return ciText(it.CI) + " (no checks)"
	}
	counts := map[string]int{}
	for _, c := range it.Checks {
		counts[c.Status]++
	}
	var parts []string
	for _, s := range []string{"pass", "fail", "pending", "skip", "neutral"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	return ciText(it.CI) + " (" + strings.Join(parts, ", ") + ")"
}

func viewReview(pr PR) string {
	if pr.ReviewDecision == "" {
		return reviewText(pr)
	}
	return strings.ToLower(strings.ReplaceAll(pr.ReviewDecision, "_", " "))
}

// viewReviewers lists who reviewed (latest state each) and who is asked.
func viewReviewers(pr PR) string {
	var parts []string
	seen := map[string]int{}
	for _, r := range pr.Reviews {
		state := strings.ToLower(strings.ReplaceAll(r.Status, "_", " "))
		if i, ok := seen[r.Who]; ok {
			parts[i] = r.Who + " (" + state + ")"
			continue
		}
		seen[r.Who] = len(parts)
		parts = append(parts, r.Who+" ("+state+")")
	}
	for _, who := range pr.Requested {
		parts = append(parts, who+" (requested)")
	}
	return strings.Join(parts, ", ")
}

func viewBranch(pr PR) string {
	switch {
	case pr.Head != "" && pr.Base != "":
		return pr.Head + " -> " + pr.Base
	case pr.Head != "":
		return pr.Head
	}
	return ""
}
