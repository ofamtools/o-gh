package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	ocli "ofam.dev/ochazuke/cli"
)

// send delivers msg to the page and, as Bubble Tea would, every message
// the commands it returns produce (a command that does not answer within
// 300ms, such as a tick, is dropped).
func send(p *page, msg tea.Msg) {
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if batch, ok := m.(tea.BatchMsg); ok {
			for _, c := range batch {
				queue = append(queue, run1(c)...)
			}
			continue
		}
		_, cmd := p.Update(m)
		queue = append(queue, run1(cmd)...)
	}
}

func run1(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case m := <-done:
		if m == nil {
			return nil
		}
		return []tea.Msg{m}
	case <-time.After(time.Second):
		return nil
	}
}

// press sends printable keys to the page and runs what they start.
func press(t *testing.T, p *page, keys ...rune) {
	t.Helper()
	// The keys go in first, their commands after: a chord's timeout must
	// not land between its keys.
	var cmds []tea.Cmd
	for _, k := range keys {
		_, cmd := p.Update(tea.KeyPressMsg{Code: k, Text: string(k)})
		cmds = append(cmds, cmd)
	}
	send(p, tea.BatchMsg(cmds))
	// The verb's Then marked the page dirty; the next message reloads it.
	send(p, tea.WindowSizeMsg{Width: 140, Height: 34})
}

func TestPageKeys(t *testing.T) {
	tool, _, inbox, _ := testTool(t)
	p, err := mountPage(tool)
	if err != nil {
		t.Fatal(err)
	}
	if err := ocli.Settle(p.t.r, nil); err != nil {
		t.Fatal(err)
	}
	send(p, tea.WindowSizeMsg{Width: 140, Height: 34})
	if n := p.v.List(listID(Mine)).Len(); n != 2 {
		t.Fatalf("mine rows %d", n)
	}
	// w on the first Mine row watches it; the Watched list reloads.
	press(t, p, 'w')
	st, _ := tool.store.State()
	if _, ok := st.Watched[urlTimer]; !ok {
		t.Fatalf("w did not watch: %+v", st)
	}
	if n := p.v.List(listID(Watched)).Len(); n != 1 {
		t.Fatalf("watched rows %d after w", n)
	}
	// d d ignores it: it leaves Mine and Watched for Ignored.
	press(t, p, 'd', 'd')
	st, _ = tool.store.State()
	if _, ok := st.Ignored[urlTimer]; !ok {
		t.Fatalf("d d did not ignore: %+v", st)
	}
	if p.v.List(listID(Ignored)).Len() != 1 || p.v.List(listID(Mine)).Len() != 1 || p.v.List(listID(Watched)).Len() != 0 {
		t.Fatalf("lists after d d: mine %d watched %d ignored %d", p.v.List(listID(Mine)).Len(), p.v.List(listID(Watched)).Len(), p.v.List(listID(Ignored)).Len())
	}
	// g i sends the focused row (now the menu PR) to the inbox.
	press(t, p, 'g', 'i')
	if len(inbox.got) != 1 || inbox.got[0].URL != urlMenu {
		t.Fatalf("g i: %+v", inbox.got)
	}
}
