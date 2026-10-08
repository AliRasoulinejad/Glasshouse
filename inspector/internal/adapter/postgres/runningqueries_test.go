package postgres

import (
	"database/sql"
	"testing"
	"time"
)

func TestQueryDurationValidStart(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 1, 0, time.UTC)
	start := sql.NullTime{Valid: true, Time: time.Date(2026, 10, 9, 12, 0, 0, 500_000_000, time.UTC)}
	got := queryDuration(start, now)
	if got != 500 {
		t.Errorf("want 500ms, got %d", got)
	}
}

func TestQueryDurationNullStart(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 1, 0, time.UTC)
	got := queryDuration(sql.NullTime{Valid: false}, now)
	if got != 0 {
		t.Errorf("want 0 for NULL query_start, got %d", got)
	}
}
