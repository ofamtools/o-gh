package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unixsuperhero/ochazuke"
	ocli "github.com/unixsuperhero/ochazuke/cli"
	"github.com/unixsuperhero/oden"
)

// The PRs page on its own: o-gh mounts its declared data page over itself
// with ocli.MountPage, exactly as a host does, so the page o-gh shows is
// the page mtm shows. Each list loads from its source verb (pr.mine, ...)
// run in this process; the four loads share one poll (the lock and the
// cache). A verb that changes the lists (watch, ignore, ...) reloads them,
// and the page reloads itself every poll.max-age.

// viewCommand is o-gh view.
func viewCommand(t *Tool) oden.Command {
	return oden.Command{
		Name: "view", Summary: "open the PRs page: Mine, My Reviews, Watched and Ignored beside the selected PR",
		Usage: "[--smoke] [--width N] [--height N]", Source: "o-gh",
		Details: "o-gh with no command opens it too. Keys: enter opens the PR, y y copies its URL, w watches, W unwatches, " +
			"d d ignores, g i sends it to the inbox, g c checks it out; : runs any verb (pr unignore, pr show, ...); ? lists every key; q quits.",
		Run: func(_ context.Context, args []string) error { return runTUI(t, args) },
	}
}

// catalogDoc is t's own catalog, decoded as a host reads it.
func catalogDoc(t *Tool) (ocli.CatalogDoc, error) {
	b, err := ocli.Catalog(t.r, "o-gh", Version)
	if err != nil {
		return ocli.CatalogDoc{}, err
	}
	var doc ocli.CatalogDoc
	err = json.Unmarshal(b, &doc)
	return doc, err
}

// fetchInProcess runs NOUN VERB on a fresh Tool sharing t's fakes and
// answers its stdout, as a host's Fetch runs the bin.
func (t *Tool) fetchInProcess(ctx context.Context, noun, verb string) ([]byte, error) {
	var out, errb bytes.Buffer
	sub := &Tool{store: t.store, gh: t.gh, inbox: t.inbox, now: t.now, opener: t.opener, clip: t.clip,
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}
	if err := run(sub, []string{noun, verb}); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out.Bytes(), nil
}

// page is the mounted PRs page.
type page struct {
	t     *Tool
	m     *ochazuke.Model
	v     *ocli.PageView
	dirty bool
	every time.Duration
}

// mountPage builds the page on a page runtime of its own (t keeps the
// shell's runtime).
func mountPage(shell *Tool) (*page, error) {
	t := &Tool{store: shell.store, gh: shell.gh, inbox: shell.inbox, now: shell.now, opener: shell.opener, clip: shell.clip,
		Stdin: shell.Stdin, Stdout: shell.Stdout, Stderr: shell.Stderr}
	if err := t.newRuntime(false); err != nil {
		return nil, err
	}
	doc, err := catalogDoc(t)
	if err != nil {
		return nil, err
	}
	m := ochazuke.NewModel(t.r)
	m.Title = "o-gh"
	v, err := ocli.MountPage(m, doc, prsPage(), ocli.PageConfig{Fetch: t.fetchInProcess})
	if err != nil {
		return nil, err
	}
	quit := ochazuke.Do("app.quit")
	quit.Instance, quit.Mode, quit.Keys = pageID, "normal", "q"
	if err := t.r.Bind(quit); err != nil {
		return nil, err
	}
	if err := t.r.OpenPage(pageID); err != nil {
		return nil, err
	}
	// The page opens on Mine (a declared page's own focus stays the page).
	if err := v.List(listID(Mine)).Focus(); err != nil {
		return nil, err
	}
	p := &page{t: t, m: m, v: v, every: t.config().MaxAge}
	if p.every < 10*time.Second {
		p.every = 10 * time.Second
	}
	t.changed = func() { p.dirty = true }
	return p, v.Reload()
}

// runTUI parses the page's flags and runs it (or renders one frame).
func runTUI(shell *Tool, args []string) error {
	flags := flag.NewFlagSet("o-gh view", flag.ContinueOnError)
	flags.SetOutput(shell.Stderr)
	smoke := flags.Bool("smoke", false, "load the lists, render one frame to stdout and exit")
	width := flags.Int("width", 120, "--smoke frame width")
	height := flags.Int("height", 32, "--smoke frame height")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &oden.ExitError{Code: 2}
	}
	if flags.NArg() > 0 {
		return &oden.ExitError{Code: 2}
	}
	p, err := mountPage(shell)
	if err != nil {
		return err
	}
	if *smoke {
		if err := ocli.Settle(p.t.r, nil); err != nil {
			p.t.r.UI().Status(err.Error())
		}
		p.m.Width, p.m.Height = *width, *height
		_, err := fmt.Fprintln(shell.Stdout, strings.TrimRight(ansi.Strip(p.m.View().Content), "\n"))
		return err
	}
	_, err = tea.NewProgram(p).Run()
	return err
}

// reloadMsg is the page's own reload tick.
type reloadMsg struct{}

func (p *page) tick() tea.Cmd {
	return tea.Tick(p.every, func(time.Time) tea.Msg { return reloadMsg{} })
}

func (p *page) Init() tea.Cmd { return tea.Batch(p.m.Init(), p.t.r.PendingCommands(), p.tick()) }

func (p *page) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(reloadMsg); ok {
		if err := p.v.Reload(); err != nil {
			p.t.r.UI().Status(err.Error())
		}
		return p, tea.Batch(p.t.r.PendingCommands(), p.tick())
	}
	_, cmd := p.m.Update(msg)
	if p.t.r.Quit {
		return p, cmd
	}
	if p.dirty {
		p.dirty = false
		if err := p.v.Reload(); err != nil {
			p.t.r.UI().Status(err.Error())
		}
		cmd = tea.Batch(cmd, p.t.r.PendingCommands())
	}
	return p, cmd
}

func (p *page) View() tea.View { return p.m.View() }
