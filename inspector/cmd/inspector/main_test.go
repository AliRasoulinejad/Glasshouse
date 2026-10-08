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
	want := "delete_rows\ninsert_rows\nupdate_rows\nvacuum_full"
	if got := strings.TrimSpace(out.String()); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestListActionsPrintsPostgresIndexActionsWithoutDSN(t *testing.T) {
	t.Setenv("GLASSHOUSE_PG_DSN", "")

	var out bytes.Buffer
	if err := listActions("postgres-index", &out); err != nil {
		t.Fatal(err)
	}
	want := "delete_rows\ninsert_rows\nupdate_rows\nvacuum_full"
	if got := strings.TrimSpace(out.String()); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestListActionsRejectsAdapterWithoutActions(t *testing.T) {
	var out bytes.Buffer
	if err := listActions("nope", &out); err == nil {
		t.Error("unknown adapter: want error")
	}
}
