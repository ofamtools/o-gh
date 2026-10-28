package main

import (
	"reflect"
	"strings"
	"testing"
)

func fixturePoll(t *testing.T) Poll {
	t.Helper()
	p, err := parsePoll(readFixture(t), fixtureQuery(), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func urls(items []Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.URL)
	}
	return out
}

func newState() State {
	var st State
	st.init()
	return st
}

func TestRules(t *testing.T) {
	p := fixturePoll(t)
	timer, menu, bump := p.Mine[0], p.Mine[1], p.Reviews[1]
	for _, c := range []struct {
		rule string
		pr   PR
		want bool
	}{
		{"author:dependabot*", bump, true},
		{"author:dependabot*", timer, false},
		{"draft", menu, true},
		{"draft", timer, false},
		{"repo:tea-shop/*", timer, true},
		{"repo:TEA-SHOP/MENU", menu, true},
		{"label:depend*", bump, true},
		{"title:wip", menu, true},
		{"title:wip", timer, false},
		{"base:main head:dependabot/*", bump, true},
		{"repo:tea-shop/kitchen draft", timer, false}, // every term must match
		{"head:depend?bot/*", bump, true},
		{"repo:*", timer, true},
		{"repo:tea.shop/*", timer, false}, // . is itself
	} {
		r, err := parseRule(c.rule)
		if err != nil {
			t.Fatalf("%q: %v", c.rule, err)
		}
		if got := r.Match(c.pr); got != c.want {
			t.Errorf("%q on %s: %v, want %v", c.rule, c.pr.Key, got, c.want)
		}
	}
	for _, bad := range []string{"", "   ", "colour:red", "repo:", "wip"} {
		if _, err := parseRule(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	rs := parseRules([]string{"draft", "colour:red", ""}, []string{"lint*", ""})
	if len(rs.Ignore) != 1 || len(rs.IgnoreChecks) != 1 || len(rs.Problems) != 1 {
		t.Fatalf("parseRules: %+v", rs)
	}
}

func TestCIIgnoresChecks(t *testing.T) {
	p := fixturePoll(t)
	timer, menu, grade, teapot := p.Mine[0], p.Mine[1], p.Reviews[0], p.Watched[0]
	none := Rules{}
	if got := none.CI(timer); got != "fail" {
		t.Fatalf("timer CI %s, want fail (lint failed)", got)
	}
	optional := parseRules(nil, []string{"lint*"})
	if got := optional.CI(timer); got != "pass" {
		t.Fatalf("timer CI with lint ignored: %s, want pass", got)
	}
	if got := none.CI(menu); got != "pending" {
		t.Fatalf("menu CI %s", got)
	}
	if got := none.CI(grade); got != "none" {
		t.Fatalf("no rollup: %s", got)
	}
	if got := none.CI(teapot); got != "pass" {
		t.Fatalf("skipped + pass: %s", got)
	}
	if got := parseRules(nil, []string{"*"}).CI(timer); got != "none" {
		t.Fatalf("every check ignored: %s", got)
	}
}

func TestClassify(t *testing.T) {
	p := fixturePoll(t)
	st := newState()
	st.Watched[urlTeapot] = Mark{URL: urlTeapot, Key: "pottery/teapot#7"}
	st.Watched[urlGone] = Mark{URL: urlGone, Key: "pottery/gone#1"}
	st.Watched[urlTimer] = Mark{URL: urlTimer, Key: "tea-shop/kitchen#12"}
	c := classify(p, st, Rules{})
	if got, want := urls(c[Mine]), []string{urlTimer, urlMenu}; !reflect.DeepEqual(got, want) {
		t.Errorf("mine %v, want %v (newest first)", got, want)
	}
	if got, want := urls(c[Reviews]), []string{urlGrade, urlBump}; !reflect.DeepEqual(got, want) {
		t.Errorf("reviews %v", got)
	}
	if got, want := urls(c[Watched]), []string{urlTimer, urlTeapot, urlGone}; !reflect.DeepEqual(got, want) {
		t.Errorf("watched %v, want %v", got, want)
	}
	if len(c[Ignored]) != 0 {
		t.Errorf("ignored %v", urls(c[Ignored]))
	}
	timer, _ := c.find(urlTimer)
	if !timer.Watching || !reflect.DeepEqual(timer.In, []string{Mine, Watched}) || timer.CI != "fail" {
		t.Errorf("timer item %+v", timer)
	}
	gone, _ := c.find(urlGone)
	if !gone.Stale || gone.Title != "(not found on GitHub)" {
		t.Errorf("gone item %+v", gone)
	}
}

func TestClassifyIgnore(t *testing.T) {
	p := fixturePoll(t)
	rules := parseRules([]string{"author:dependabot*", "draft"}, nil)

	// Rules alone: the bot's bump and the draft are ignored, by rule.
	st := newState()
	c := classify(p, st, rules)
	if got := urls(c[Ignored]); !reflect.DeepEqual(got, []string{urlBump, urlMenu}) {
		t.Fatalf("ignored by rule %v", got)
	}
	bump, _ := c.find(urlBump)
	if bump.IgnoredBy != "author:dependabot*" || !reflect.DeepEqual(bump.In, []string{Ignored}) || !reflect.DeepEqual(bump.Found, []string{Reviews}) {
		t.Fatalf("bump %+v", bump)
	}
	if got := urls(c[Reviews]); !reflect.DeepEqual(got, []string{urlGrade}) {
		t.Fatalf("reviews keep only the unignored: %v", got)
	}

	// You ignore one PR; a watch and an unignore override the rules.
	st.Ignored[urlGrade] = Mark{URL: urlGrade}
	st.Watched[urlMenu] = Mark{URL: urlMenu}
	st.Allowed[urlBump] = Mark{URL: urlBump}
	c = classify(p, st, rules)
	if got := urls(c[Ignored]); !reflect.DeepEqual(got, []string{urlGrade}) {
		t.Fatalf("ignored %v", got)
	}
	grade, _ := c.find(urlGrade)
	if grade.IgnoredBy != "you" {
		t.Fatalf("grade %+v", grade)
	}
	if got := urls(c[Reviews]); !reflect.DeepEqual(got, []string{urlBump}) {
		t.Fatalf("reviews %v", got)
	}
	if got := urls(c[Watched]); !reflect.DeepEqual(got, []string{urlMenu}) {
		t.Fatalf("watched %v", got)
	}

	// An ignored PR the poll did not answer is listed from its mark.
	st.Ignored["https://github.com/a/b/pull/9"] = Mark{URL: "https://github.com/a/b/pull/9", Key: "a/b#9", Title: "old"}
	c = classify(p, st, rules)
	it, ok := c.find("https://github.com/a/b/pull/9")
	if !ok || !it.Stale || it.Title != "old" || !it.Ignored {
		t.Fatalf("stub %+v", it)
	}
}

func TestRowCells(t *testing.T) {
	p := fixturePoll(t)
	c := classify(p, newState(), Rules{})
	menu, _ := c.find(urlMenu)
	row := prRow(menu, fixtureNow)
	if row.ID != "pr/tea-shop/menu#3" || row.Noun != prNoun || !strings.HasPrefix(row.Label, "tea-shop/menu#3 ") {
		t.Fatalf("row %+v", row)
	}
	want := []string{"tea-shop/menu#3", "[draft] WIP: tea menu", "miso", "… run", "-", "2d"}
	if !reflect.DeepEqual(row.Cells, want) {
		t.Fatalf("cells %q, want %q", row.Cells, want)
	}
	if len(row.Cells) != len(prColumns) {
		t.Fatal("a cell per column")
	}
	timer, _ := c.find(urlTimer)
	if got := prCells(timer, fixtureNow)[4]; got != "waiting on 2" {
		t.Fatalf("review cell %q", got)
	}
}
