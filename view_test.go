package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ofam.dev/ochazuke"
)

func viewOf(t *testing.T, url string) string {
	t.Helper()
	tool, _, _, _ := testTool(t)
	k, err := parseKey(url)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.viewPR(context.Background(), cfg(t, tool), k, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := out.(string)
	if !ok {
		t.Fatalf("pr.view answered %T, want text", out)
	}
	return s
}

func wantAll(t *testing.T, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(got, p) {
			t.Errorf("view lacks %q:\n%s", p, got)
		}
	}
}

func TestViewFull(t *testing.T) {
	got := viewOf(t, urlTimer)
	wantAll(t, got,
		"# Add the rice cooker timer\n",
		"- **PR:** #12\n", "- **Repo:** tea-shop/kitchen\n", "- **Author:** miso\n",
		"- **State:** open\n", "- **Draft:** no\n", "- **Mergeable:** mergeable\n",
		"- **Review:** review required\n",
		"nori (commented)", "nori (requested)", "team:cooks (requested)",
		"- **Comments:** 2\n", "- **Branch:** timer -> main\n", "- **Labels:** feature\n",
		"- **URL:** "+urlTimer+"\n",
		"## Checks\n", "test", "deploy/preview",
		"## Description\n\nAdds a timer to the cooker.\n\n- starts at lid close\n- beeps when done\n")
	if strings.Contains(got, "No checks") {
		t.Errorf("checks listed as none:\n%s", got)
	}
}

func TestViewEmptyBodyNoChecks(t *testing.T) {
	got := viewOf(t, urlMenu)
	wantAll(t, got, "# WIP: tea menu\n", "- **Draft:** yes\n", "## Description\n\nNo description.\n")
	tool, _, _, _ := testTool(t)
	k, _ := parseKey(urlMenu)
	it, err := tool.item(context.Background(), cfg(t, tool), k)
	if err != nil {
		t.Fatal(err)
	}
	it.Checks, it.CI = nil, "none"
	got = viewText(it)
	wantAll(t, got, "- **CI:** - (no checks)\n", "## Checks\n\nNo checks.\n", "No description.")
}

func TestViewUnknownFields(t *testing.T) {
	got := viewText(Item{PR: PR{Key: "a/b#1", Number: 1, Body: "line\r\nline2\r\n"}})
	wantAll(t, got, "# a/b#1\n", "- **Author:** -\n", "- **Mergeable:** -\n", "- **Comments:** 0\n", "line\nline2\n")
}

func TestViewDeclared(t *testing.T) {
	tool, _, _, _ := testTool(t)
	op, ok := tool.r.Operations.Find(ochazuke.Key(prNoun + ".view"))
	if !ok {
		t.Fatal("pr.view is not declared")
	}
	if op.Result != ochazuke.ResultMarkdown {
		t.Fatalf("pr.view result %q, want markdown", op.Result)
	}
	if _, ok := tool.r.Operations.Find(ochazuke.Key(prNoun + ".show")); !ok {
		t.Fatal("pr.show was removed")
	}
	doc, err := catalogDoc(tool)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ocliJSON(doc)
	wantAll(t, b, `"view"`, `"markdown"`)
}

func ocliJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
