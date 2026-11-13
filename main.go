// Command o-gh is the family's GitHub PR tool: the PRs page (Mine, My
// Reviews, Watched, Ignored) and the signals it sends into the mtm inbox,
// through the gh CLI (one gh api graphql poll; it never writes to GitHub).
//
//	o-gh [view] [--smoke]                  the PRs page
//	o-gh pr list|mine|reviews|watched|ignored|show|open|watch|ignore|... [--target PR]
//	o-gh collection list|show|open
//	o-gh signals [--dry-run]               signal push (the noun's default verb): new review requests and CI changes to the inbox
//	o-gh catalog                           the catalog a host (mtm) mounts
package main

import (
	"context"
	"io"
	"os"
	"strings"

	ocli "ofam.dev/ochazuke/cli"
	"ofam.dev/oden"
)

func main() {
	os.Exit(oden.Report(os.Stderr, progName(), Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)))
}

// progName is how messages name this command: the word under the parent
// oden app that ran it, else the bin name.
func progName() string {
	if parent := os.Getenv("ODEN_PARENT"); parent != "" {
		return parent + " gh"
	}
	return "o-gh"
}

// Main runs one invocation.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return run(newTool(stdin, stdout, stderr), args)
}

// run runs one invocation on t (tests pass a Tool with fakes).
func run(t *Tool, args []string) error {
	if err := t.newRuntime(true); err != nil {
		return err
	}
	app := oden.New("o-gh", args, oden.WithBuiltins(), oden.WithStdio(t.Stdin, t.Stdout, t.Stderr),
		oden.WithDefault(func(_ context.Context, args []string) error {
			if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				return oden.ErrNotFound
			}
			return runTUI(t, args)
		}))
	ocli.Mount(app, t.Shell())
	app.Add(viewCommand(t))
	app.HelpText += `
Page:
  o-gh (or o-gh view) opens the PRs page; --smoke renders one frame.
Signals:
  o-gh signals [--dry-run] sends new review requests (yours, and your on-call team's while on call),
  CI changes, reviews, comments, conflicts, merges and closes to the mtm inbox
  (mtm action-item add --source gh --url URL --why WHY --title T --kind K --key KEY --reopen;
  $O_GH_MTM names another mtm).`
	return app.Run(context.Background())
}

// Shell is the NOUN VERB command line over the runtime.
func (t *Tool) Shell() *ocli.Shell {
	sh := ocli.New(t.r, "o-gh", t.Stdin, t.Stdout, t.Stderr)
	sh.Usage = ocli.DefaultUsage("o-gh")
	sh.Version = Version
	sh.Reveal = func(target string) error {
		return t.reveal(target)
	}
	return sh
}
