package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An older poll.json and signals.json hold `state` where the lexicon says
// `status`: they are read as status, and the next write saves status.
func TestOlderFilesStateKeyReadsAsStatus(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	poll := `{"query":{},"poll":{"mine":[{"key":"a/b#1","state":"OPEN","checks":[{"name":"t","state":"pass"}],"reviews":[{"who":"x","state":"APPROVED"}]}]}}`
	if err := os.WriteFile(filepath.Join(s.Dir, pollFile), []byte(poll), 0o644); err != nil {
		t.Fatal(err)
	}
	var c cachedPoll
	if ok, err := s.read(pollFile, &c); !ok || err != nil {
		t.Fatalf("read %v %v", ok, err)
	}
	pr := c.Poll.Mine[0]
	if pr.Status != "OPEN" || pr.Checks[0].Status != "pass" {
		t.Fatalf("older poll read as %+v", pr)
	}
	if err := s.write(pollFile, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, pollFile))
	if got := string(b); strings.Contains(got, `"state"`) || !strings.Contains(got, `"status": "OPEN"`) {
		t.Fatalf("written back with the old key:\n%s", got)
	}
	snap := `{"prs":{"https://github.com/a/b/pull/1":{"state":"OPEN","comments":2}}}`
	os.WriteFile(filepath.Join(s.Dir, signalsFile), []byte(snap), 0o644)
	var sn Snapshot
	if ok, err := s.read(signalsFile, &sn); !ok || err != nil || sn.PRs["https://github.com/a/b/pull/1"].Status != "OPEN" {
		t.Fatalf("older snapshot read as %+v (%v %v)", sn, ok, err)
	}
}
