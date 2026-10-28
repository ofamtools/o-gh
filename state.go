package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// o-gh's own data, in its data directory ($O_GH_DATA, else
// $XDG_DATA_HOME/o-gh, else ~/.local/share/o-gh):
//
//	state.json    the PRs watched, ignored and unignored (Mark by URL)
//	poll.json     the last poll (the page's rows come from it while fresh)
//	signals.json  what the last o-gh signals run saw (Snapshot)
//	lock          flock'd around a poll and around each file's read-modify-write
//
// Every write is a temp file renamed into place.

const (
	stateFile   = "state.json"
	pollFile    = "poll.json"
	signalsFile = "signals.json"
	lockFile    = "lock"
)

// dataDir is the data directory.
func dataDir() string {
	if d := os.Getenv("O_GH_DATA"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "o-gh")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "o-gh")
	}
	return filepath.Join(home, ".local", "share", "o-gh")
}

// Mark is what o-gh recorded of a PR when it was marked: enough to show
// a row before (or without) a poll answering it.
type Mark struct {
	URL    string    `json:"url"`
	Key    string    `json:"key"`
	Title  string    `json:"title,omitempty"`
	Repo   string    `json:"repo,omitempty"`
	Number int       `json:"number,omitempty"`
	At     time.Time `json:"at"`
}

// stub is the PR row a mark stands for.
func (m Mark) stub() PR {
	pr := PR{URL: m.URL, Key: m.Key, Title: m.Title, Repo: m.Repo, Number: m.Number}
	if pr.Title == "" {
		pr.Title = "(not read yet)"
	}
	return pr
}

// State is o-gh's own PR state. A URL is in at most one of Watched and
// Ignored; Allowed holds the PRs unignored against a rule.
type State struct {
	Watched map[string]Mark `json:"watched"`
	Ignored map[string]Mark `json:"ignored"`
	Allowed map[string]Mark `json:"allowed"`
}

func (st *State) init() {
	if st.Watched == nil {
		st.Watched = map[string]Mark{}
	}
	if st.Ignored == nil {
		st.Ignored = map[string]Mark{}
	}
	if st.Allowed == nil {
		st.Allowed = map[string]Mark{}
	}
}

// marks are the watched and ignored marks, by URL.
func (st State) marks() []Mark {
	var out []Mark
	for _, m := range st.Watched {
		out = append(out, m)
	}
	for _, m := range st.Ignored {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b Mark) int {
		switch {
		case a.URL < b.URL:
			return -1
		case a.URL > b.URL:
			return 1
		}
		return 0
	})
	return out
}

// watchedKeys are the watched PRs, sorted by URL (the poll's aliases).
func (st State) watchedKeys() []PRKey {
	var urls []string
	for url := range st.Watched {
		urls = append(urls, url)
	}
	slices.Sort(urls)
	var out []PRKey
	for _, u := range urls {
		if k, err := parseKey(u); err == nil {
			out = append(out, k)
		}
	}
	return out
}

// Store is the data directory.
type Store struct{ Dir string }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

// locked runs fn holding the directory's lock (flock, exclusive).
func (s Store) locked(fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", s.path(lockFile), err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// read decodes name into v; a missing file is v unchanged and false.
func (s Store) read(name string, v any) (bool, error) {
	b, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", s.path(name), err)
	}
	return true, nil
}

// write encodes v into name atomically.
func (s Store) write(name string, v any) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path(name))
}

// State reads state.json.
func (s Store) State() (State, error) {
	var st State
	_, err := s.read(stateFile, &st)
	st.init()
	return st, err
}

// Change edits state.json under the lock.
func (s Store) Change(edit func(*State) error) (State, error) {
	var st State
	err := s.locked(func() error {
		var err error
		if st, err = s.State(); err != nil {
			return err
		}
		if err := edit(&st); err != nil {
			return err
		}
		return s.write(stateFile, st)
	})
	return st, err
}

// Snapshot is what a signals run saw: the review requests it signalled
// (or that were already there) and each tracked PR's CI state.
type Snapshot struct {
	At      time.Time         `json:"at"`
	Reviews []string          `json:"reviews"`
	CI      map[string]string `json:"ci"`
}
