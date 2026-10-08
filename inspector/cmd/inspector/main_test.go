package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestListActionsPrintsPostgresActionsWithoutDSN(t *testing.T) {
	t.Setenv("GLASSHOUSE_PG_DSN", "")

	var out bytes.Buffer
	if err := listActions("postgres", &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "insert_rows" {
		t.Errorf("got %q, want %q", got, "insert_rows")
	}
}

func TestListActionsRejectsAdapterWithoutActions(t *testing.T) {
	var out bytes.Buffer
	if err := listActions("nope", &out); err == nil {
		t.Error("unknown adapter: want error")
	}
}
