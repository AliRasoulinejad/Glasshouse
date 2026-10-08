package article

import (
	"strings"
	"testing"
)

const good = `---
title: Reading a heap page
actions: [insert_rows]
---

Some prose.

<!-- lab-panel -->
`

func known() map[string]bool { return map[string]bool{"insert_rows": true} }

func TestBuildAcceptsKnownActions(t *testing.T) {
	a, err := Build([]byte(good), known())
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Reading a heap page" {
		t.Errorf("title = %q", a.Title)
	}
	if len(a.Actions) != 1 || a.Actions[0] != "insert_rows" {
		t.Errorf("actions = %v", a.Actions)
	}
	if !strings.Contains(string(a.Body), `<iframe`) {
		t.Error("panel marker was not replaced with an iframe")
	}
}

func TestBuildRejectsUnknownAction(t *testing.T) {
	src := strings.Replace(good, "insert_rows", "drop_table", 1)
	if _, err := Build([]byte(src), known()); err == nil {
		t.Error("unknown action: want error")
	}
}

func TestBuildDropsRawHTML(t *testing.T) {
	src := strings.Replace(good, "Some prose.", "<script>alert(1)</script>", 1)
	a, err := Build([]byte(src), known())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(a.Body), "alert(1)") {
		t.Error("raw HTML from the Markdown reached the page")
	}
}

func TestBuildRequiresExactlyOnePanel(t *testing.T) {
	src := strings.Replace(good, "<!-- lab-panel -->", "", 1)
	if _, err := Build([]byte(src), known()); err == nil {
		t.Error("no panel: want error")
	}
}

func TestBuildRejectsUnknownFrontMatterKey(t *testing.T) {
	src := strings.Replace(good, "title:", "owner:", 1)
	if _, err := Build([]byte(src), known()); err == nil {
		t.Error("unknown key: want error")
	}
}
