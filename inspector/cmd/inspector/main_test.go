package main

import (
	"bytes"
	"strings"
	"testing"

	"glasshouse/inspector/internal/adapter"
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

func TestListActionsRejectsAdapterWithoutActions(t *testing.T) {
	var out bytes.Buffer
	if err := listActions("nope", &out); err == nil {
		t.Error("unknown adapter: want error")
	}
}

func TestBuildAdapterPostgresRequiresDSN(t *testing.T) {
	t.Setenv("GLASSHOUSE_PG_DSN", "")

	if _, _, err := buildAdapter("postgres"); err == nil {
		t.Error("missing GLASSHOUSE_PG_DSN: want error")
	}
}

func TestBuildAdapterPostgresReturnsTarget(t *testing.T) {
	t.Setenv("GLASSHOUSE_PG_DSN", "postgres://example/dsn")

	a, target, err := buildAdapter("postgres")
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Error("want non-nil adapter")
	}
	want := adapter.Target{Name: "postgres", Endpoint: "postgres://example/dsn"}
	if target != want {
		t.Errorf("got target %+v, want %+v", target, want)
	}
}
