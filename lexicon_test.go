package main

import (
	"testing"

	"ofam.dev/ochazuke/lexiconcheck"
)

// o-gh's registered operations are held to the lexicon's naming rules
// (lexicon plan): one word has one part of speech, nouns are singular, a
// namespace is not a noun or action word, a key has at most three parts.
// testdata/lexicon-words.tsv is a copy of ochazuke's word list.
func TestLexicon(t *testing.T) {
	tool, _, _, _ := testTool(t)
	lexiconcheck.Check(t, lexiconcheck.Config{
		Operations: &tool.r.Operations,
		MainNouns:  map[string]string{"gh": "pr"},
		Words:      "testdata/lexicon-words.tsv",
		Baseline:   "testdata/lexiconcheck.txt",
	})
}
