package main

import (
	"encoding/json"
	"testing"

	"ofam.dev/ochazuke"
	ocli "ofam.dev/ochazuke/cli"
)

func TestCatalogDeclaresSingularNamespace(t *testing.T) {
	tool, _, _, _ := testTool(t)
	out, err := sh(t, tool, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	var doc ocli.CatalogDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Namespace != "gh" || ochazuke.DefaultNamespace != "gh" {
		t.Fatalf("catalog namespace %q, default %q; want gh", doc.Namespace, ochazuke.DefaultNamespace)
	}
	if got, e := ocli.CatalogNamespace(doc, "o-gh"); e != nil || got != "gh" {
		t.Fatal(got, e)
	}
}
