package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	ocli "ofam.dev/ochazuke/cli"
)

// Page stories: cmd/o-story/stories/gh/*.json, in the shape ochazuke's
// o-story and o-questions use (name, skip, script, expect after N keys),
// run here against the PRs page mounted over the fake gh. A script entry
// is one printable key, or esc or enter; expect lists text the page shows
// (contains) or must not show (absent) after that many keys.
func TestPRStories(t *testing.T) {
	files, err := filepath.Glob("cmd/o-story/stories/gh/*.json")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			var story struct {
				Name   string
				Skip   string
				Script []string
				Expect []struct {
					After    int
					Contains []string
					Absent   []string
				}
			}
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &story); err != nil {
				t.Fatal(err)
			}
			if story.Skip != "" {
				t.Skip(story.Skip)
			}
			tool, _, _, _ := testTool(t)
			p, err := mountPage(tool)
			if err != nil {
				t.Fatal(err)
			}
			if err := ocli.Settle(p.t.r, nil); err != nil {
				t.Fatal(err)
			}
			send(p, tea.WindowSizeMsg{Width: 140, Height: 34})
			check := func(after int) {
				screen := ansi.Strip(p.m.View().Content)
				for _, e := range story.Expect {
					if e.After != after {
						continue
					}
					for _, s := range e.Contains {
						if !strings.Contains(screen, s) {
							t.Errorf("after %d keys the page lacks %q:\n%s", after, s, screen)
						}
					}
					for _, s := range e.Absent {
						if strings.Contains(screen, s) {
							t.Errorf("after %d keys the page shows %q:\n%s", after, s, screen)
						}
					}
				}
			}
			check(0)
			for i, k := range story.Script {
				storyKey(p, k)
				send(p, tea.WindowSizeMsg{Width: 140, Height: 34})
				check(i + 1)
			}
		})
	}
}

func storyKey(p *page, k string) {
	msg := tea.KeyPressMsg{}
	switch k {
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	default:
		r := []rune(k)[0]
		msg = tea.KeyPressMsg{Code: r, Text: k}
	}
	_, cmd := p.Update(msg)
	send(p, tea.BatchMsg{cmd})
}
