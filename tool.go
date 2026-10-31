package main

import (
	"context"
	"io"
	"os"
	"slices"
	"time"

	"ofam.dev/ochazuke"
)

// Tool is o-gh: its runtime (nouns, verbs, settings, the PRs page), the gh
// runner, the inbox seam and the data directory.
type Tool struct {
	r       *ochazuke.Runtime
	targets *ochazuke.List // the shell's --target rows (nil on the page)
	store   Store
	gh      Runner
	inbox   Inbox
	now     func() time.Time
	// opener and clip open a URL and copy text; tests replace them.
	opener func(ctx context.Context, url string) error
	clip   func(text string) error
	// changed runs (on the loop) after a verb changed what the lists show:
	// the page reloads its rows.
	changed func()

	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// Config is what a poll and the collections read from the settings, read
// on the loop so the work off it reads no runtime state.
type Config struct {
	Query  Query
	MaxAge time.Duration
	Rules  Rules
}

// config reads the settings.
func (t *Tool) config() Config {
	s := t.r.Settings()
	return Config{
		Query:  Query{Mine: s.String(setMine), Reviews: s.String(setReviews), Limit: s.Int(setLimit)},
		MaxAge: s.Duration(setMaxAge),
		Rules:  parseRules(s.Strings(setIgnore), s.Strings(setIgnoreChecks)),
	}
}

// cachedPoll is poll.json: the poll and the query that made it.
type cachedPoll struct {
	Query Query `json:"query"`
	Poll  Poll  `json:"poll"`
}

// current is the state and a poll that answers it: the cached poll when
// it is younger than MaxAge, made by the same searches and asked for
// every watched PR; else a new poll, saved. fresh always polls. The poll
// runs holding the lock, so pages loading their lists at once poll once.
func (t *Tool) current(ctx context.Context, cfg Config, fresh bool) (Poll, State, error) {
	var p Poll
	var st State
	err := t.store.locked(func() error {
		var err error
		if st, err = t.store.State(); err != nil {
			return err
		}
		q := cfg.Query
		q.Watched = st.watchedKeys()
		if !fresh {
			var c cachedPoll
			if ok, _ := t.store.read(pollFile, &c); ok && t.now().Sub(c.Poll.At) < cfg.MaxAge &&
				c.Query.Mine == q.Mine && c.Query.Reviews == q.Reviews && c.Query.Limit == q.Limit && covers(c.Poll.Asked, q.Watched) {
				p = c.Poll
				return nil
			}
		}
		if p, err = poll(ctx, t.gh, q, t.now()); err != nil {
			return err
		}
		return t.store.write(pollFile, cachedPoll{Query: q, Poll: p})
	})
	return p, st, err
}

// covers reports whether a poll asked for every watched PR.
func covers(asked []string, watched []PRKey) bool {
	for _, k := range watched {
		if !slices.Contains(asked, k.URL()) {
			return false
		}
	}
	return true
}

// collect is the collections from a current poll.
func (t *Tool) collect(ctx context.Context, cfg Config, fresh bool) (Collections, Poll, error) {
	p, st, err := t.current(ctx, cfg, fresh)
	if err != nil {
		return nil, Poll{}, err
	}
	return classify(p, st, cfg.Rules), p, nil
}

// newTool is a Tool with the real gh, inbox, opener and clipboard.
func newTool(stdin io.Reader, stdout, stderr io.Writer) *Tool {
	return &Tool{
		store: Store{Dir: dataDir()}, gh: ghRunner(os.Getenv("O_GH_GH")), inbox: mtmInbox, now: time.Now,
		opener: openURL, clip: copyToClipboard, Stdin: stdin, Stdout: stdout, Stderr: stderr,
	}
}
