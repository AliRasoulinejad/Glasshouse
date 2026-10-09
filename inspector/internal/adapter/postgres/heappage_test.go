package postgres

import (
	"reflect"
	"testing"
	"time"
)

func TestParseBlockRefBlocks(t *testing.T) {
	cases := []struct {
		name        string
		blockRef    string
		relfilenode uint32
		want        []int
	}{
		{
			name:        "single main-fork block, matching relfilenode",
			blockRef:    "blkref #0: rel 1663/16401/24595 fork main blk 3",
			relfilenode: 24595,
			want:        []int{3},
		},
		{
			name:        "non-main fork is ignored",
			blockRef:    "blkref #0: rel 1663/16401/24595 fork vm blk 0",
			relfilenode: 24595,
			want:        nil,
		},
		{
			name:        "different relfilenode is ignored",
			blockRef:    "blkref #0: rel 1663/16401/99999 fork main blk 3",
			relfilenode: 24595,
			want:        nil,
		},
		{
			name: "multiple blkref lines, mixed matches",
			blockRef: "blkref #0: rel 1663/16401/24595 fork main blk 1\n" +
				"blkref #1: rel 1663/16401/24595 fork main blk 2\n" +
				"blkref #2: rel 1663/16401/777 fork main blk 0",
			relfilenode: 24595,
			want:        []int{1, 2},
		},
		{
			name:        "full page write suffix still parses the block number",
			blockRef:    "blkref #0: rel 1663/16401/24595 fork main blk 5 FPW",
			relfilenode: 24595,
			want:        []int{5},
		},
		{
			name:        "empty string yields no blocks",
			blockRef:    "",
			relfilenode: 24595,
			want:        nil,
		},
		{
			name:        "malformed text yields no blocks",
			blockRef:    "not a blkref line at all",
			relfilenode: 24595,
			want:        nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBlockRefBlocks(tc.blockRef, tc.relfilenode)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseBlockRefBlocks(%q, %d) = %v, want %v",
					tc.blockRef, tc.relfilenode, got, tc.want)
			}
		})
	}
}

func TestDiffSetsCorrelationID(t *testing.T) {
	a := &Adapter{source: "postgres"}
	page := Page{
		Block:     0,
		FreeSpace: 100,
		Items: []Item{
			{LP: 1, Offset: 8000, Length: 50, XMin: "10", XMax: "0"},
		},
	}
	// First read only establishes the baseline; no events, no correlation to check.
	if events := a.diff(page, time.Now(), "postgres:0:0"); len(events) != 0 {
		t.Fatalf("baseline read: got %d events, want 0", len(events))
	}

	page2 := Page{
		Block:     0,
		FreeSpace: 50,
		Items: []Item{
			{LP: 1, Offset: 8000, Length: 50, XMin: "10", XMax: "0"},
			{LP: 2, Offset: 7950, Length: 50, XMin: "11", XMax: "0"},
		},
	}
	events := a.diff(page2, time.Now(), "postgres:0:1")
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].CorrelationID != "postgres:0:1" {
		t.Errorf("CorrelationID = %q, want %q", events[0].CorrelationID, "postgres:0:1")
	}
}

func TestEmitWALEventsCorrelationID(t *testing.T) {
	a := &Adapter{source: "postgres"}
	records := []WALRecord{
		{LSN: "0/1A2B3C0", Rmgr: "Heap", RecordType: "INSERT", Block: 5, Length: 64, Description: "off 1"},
		{LSN: "0/1A2B400", Rmgr: "Heap", RecordType: "INSERT", Block: 5, Length: 64, Description: "off 2"},
		{LSN: "0/1A2B440", Rmgr: "Heap", RecordType: "INSERT", Block: 7, Length: 64, Description: "off 1"},
	}
	events := a.emitWALEvents(records, 9, time.Now())
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i, ev := range events {
		if ev.Kind != "wal_record" {
			t.Errorf("event %d: Kind = %q, want %q", i, ev.Kind, "wal_record")
		}
	}
	if events[0].CorrelationID != "postgres:5:9" || events[1].CorrelationID != "postgres:5:9" {
		t.Errorf("block-5 events should share correlation_id postgres:5:9, got %q and %q",
			events[0].CorrelationID, events[1].CorrelationID)
	}
	if events[2].CorrelationID != "postgres:7:9" {
		t.Errorf("block-7 event CorrelationID = %q, want %q", events[2].CorrelationID, "postgres:7:9")
	}
	if events[0].Seq == events[1].Seq {
		t.Errorf("events must get distinct, increasing seq numbers; got %d twice", events[0].Seq)
	}
}
