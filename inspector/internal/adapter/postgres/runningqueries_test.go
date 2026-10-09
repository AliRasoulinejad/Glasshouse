package postgres

import (
	"database/sql"
	"encoding/json"
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

func TestHeapSnapshotJSONIncludesQueries(t *testing.T) {
	snap := heapSnapshot{
		HeapPages: HeapPages{Relation: "glasshouse_demo", Pages: []Page{}},
		Queries:   []RunningQuery{{PID: 123, State: "active"}},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["queries"]; !ok {
		t.Error("want top-level \"queries\" key in heapSnapshot JSON")
	}
	if _, ok := decoded["relation"]; !ok {
		t.Error("want top-level \"relation\" key (from embedded HeapPages) in heapSnapshot JSON")
	}
}

func TestHeapAndIndexSnapshotJSONIncludesQueries(t *testing.T) {
	snap := heapAndIndexSnapshot{
		HeapAndIndex: HeapAndIndex{
			Relation: "glasshouse_demo",
			Heap:     HeapPages{Relation: "glasshouse_demo", Pages: []Page{}},
			Index:    IndexPages{IndexName: "glasshouse_demo_payload_idx", Pages: []IndexPage{}},
		},
		Queries: []RunningQuery{{PID: 123, State: "active"}},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"queries", "relation", "heap", "index"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("want top-level %q key in heapAndIndexSnapshot JSON", key)
		}
	}
}
