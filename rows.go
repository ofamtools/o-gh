package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	ocli "github.com/unixsuperhero/ochazuke/cli"
)

// A PR row: id pr/OWNER/REPO#N (what --target takes), label "KEY title",
// the item as its record, and one cell per prColumns column.

func prRow(it Item, now time.Time) ocli.Row {
	return ocli.Row{Noun: prNoun, ID: rowIDPfx + it.Key, Label: it.Key + " " + it.Title, Data: it, Cells: prCells(it, now)}
}

func prCells(it Item, now time.Time) []string {
	title := it.Title
	if it.Draft {
		title = "[draft] " + title
	}
	if s := strings.ToUpper(it.State); s == "MERGED" || s == "CLOSED" {
		title = "[" + strings.ToLower(s) + "] " + title
	}
	return []string{it.Key, title, it.Author, ciText(it.CI), reviewText(it.PR), age(now, it.Updated)}
}

// ciText marks a CI state for the table.
func ciText(ci string) string {
	switch ci {
	case "pass":
		return "✓ pass"
	case "fail":
		return "✗ fail"
	case "pending":
		return "… run"
	case "", "none":
		return "-"
	}
	return ci
}

// reviewText is the review decision, else the approvals and reviews seen.
func reviewText(pr PR) string {
	switch strings.ToUpper(pr.ReviewDecision) {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	case "REVIEW_REQUIRED":
		if len(pr.Requested) > 0 {
			return fmt.Sprintf("waiting on %d", len(pr.Requested))
		}
		return "review required"
	}
	approved := 0
	for _, r := range pr.Reviews {
		if r.State == "APPROVED" {
			approved++
		}
	}
	switch {
	case approved > 0:
		return fmt.Sprintf("%d approved", approved)
	case len(pr.Reviews) > 0:
		return fmt.Sprintf("%d reviews", len(pr.Reviews))
	case len(pr.Requested) > 0:
		return fmt.Sprintf("waiting on %d", len(pr.Requested))
	}
	return "-"
}

// age is how long ago an RFC 3339 time was: 5m, 3h, 2d, 6w.
func age(now time.Time, at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dw", int(d.Hours()/24/7))
}

// openURL opens an http(s) URL with $O_GH_OPEN, else open or xdg-open
// (matomeru-old's openURL).
func openURL(ctx context.Context, url string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return fmt.Errorf("refusing to open a non-http URL: %q", url)
	}
	var argv []string
	switch {
	case os.Getenv("O_GH_OPEN") != "":
		argv = strings.Fields(os.Getenv("O_GH_OPEN"))
	case runtime.GOOS == "darwin":
		argv = []string{"open"}
	case runtime.GOOS == "linux":
		argv = []string{"xdg-open"}
	default:
		return fmt.Errorf("opening URLs is unsupported on %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, argv[0], append(argv[1:], url)...).CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %w: %s", url, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyToClipboard writes text to the clipboard ($O_GH_CLIPBOARD, else
// pbcopy, wl-copy, xclip or xsel), as o-md does.
func copyToClipboard(text string) error {
	var argv []string
	if cmd := os.Getenv("O_GH_CLIPBOARD"); cmd != "" {
		argv = strings.Fields(cmd)
	} else {
		candidates := [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
		if runtime.GOOS == "darwin" {
			candidates = append([][]string{{"pbcopy"}}, candidates...)
		}
		for _, cand := range candidates {
			if _, err := exec.LookPath(cand[0]); err == nil {
				argv = cand
				break
			}
		}
	}
	if len(argv) == 0 {
		return errors.New("no clipboard command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
