# Postgres WAL overlay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the real WAL record(s) that produced each heap page change in the existing Postgres heap page view, read live via `pg_walinspect` from the running instance.

**Architecture:** The existing `postgres.Adapter`'s poll loop (`StreamEvents` in `heappage.go`) gains a second read each tick — `pg_get_wal_records_info(prevLSN, currLSN)` — filtered to the demo relation's blocks, emitted as new `wal_record` events. Both the WAL events and the existing heap-diff events (`tuple_inserted` etc.) from the same tick share a `correlation_id`, since only one block (`last`) is ever diffed per tick already. The viewer's existing `postgres.heap_page` view looks up correlated `wal_record` events for the focused heap event and renders them inline.

**Tech Stack:** Go 1.24, `github.com/jackc/pgx/v5`, Postgres 17 (`pg_walinspect` extension, built in since PG15), plain ES modules (no build step) for the viewer.

**Spec:** `docs/superpowers/specs/2026-10-09-postgres-wal-overlay-design.md`

## Global Constraints

- Every SQL statement in `inspector/internal/adapter/postgres` stays a constant string; only bound parameters vary (`CLAUDE.md`, Security rules).
- The Inspector reads its DSN from `GLASSHOUSE_PG_DSN` only — unaffected by this change.
- The viewer writes API data with `textContent` only, never `innerHTML` (`CLAUDE.md`, Security rules).
- `schema/README.md` must stay in step with `inspector/internal/adapter/adapter.go` (`CLAUDE.md`, Conventions). `SchemaVersion` stays `1` — this is an additive new event kind, not a breaking change.
- Commit as you go, with meaningful messages, ending with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.
- This machine has no local Go — use `make test USE_DOCKER=1` for every Go test/vet/fmt run.

## Deviations from the spec (resolved during planning, not a design change)

1. **Relfilenode: resolved fresh every tick, not cached once in `Connect`.** The existing `vacuum_full` action runs `VACUUM (FULL)`, which rewrites the table and changes its relfilenode. A relfilenode cached once at `Connect` time would go stale the first time a reader clicks "vacuum full," silently breaking the WAL overlay for the rest of the session. `pg_relation_filenode()` is a cheap catalog lookup, so Task 2 resolves it on every `readWALRecords` call instead.
2. **Access mechanism: targeted `GRANT EXECUTE` on the one function used, not `pg_monitor` membership.** The spec left this open pending implementation. `pg_monitor` grants far more than WAL metadata (stats views, settings, etc.); a single `GRANT EXECUTE ON FUNCTION pg_get_wal_records_info(pg_lsn, pg_lsn) TO glasshouse_inspector` matches the least-privilege pattern the existing heap/index wrapper grants already follow.
3. **`CREATE EXTENSION pg_walinspect` was missing from the spec's compose.yml snippet** (it only showed the grant) — Task 3 adds it; the extension must exist before its functions can be granted or called.

## Review Focus

- A poll tick where no WAL advanced since the last tick (`prevLSN == currLSN`, e.g. the demo table is idle) must not call `pg_get_wal_records_info` with an empty range or error — Task 2's guard and its test cover this.
- A `block_ref` touching a fork other than `main` (`vm`, `fsm`, `init`) must not be treated as a heap block match — Task 1's parser tests cover this explicitly.
- A `block_ref` touching a different relation's relfilenode (any other table in the same database) must not leak into this relation's WAL events — Task 1's parser tests cover this.
- The very first tick after `Connect` (bootstrap, no `prevLSN` yet) must not attempt a WAL read, mirroring the existing heap-diff bootstrap rule — Task 2's test covers this.
- A `wal_record` event whose block has no matching heap-diff event in the viewer's currently visible batch (e.g. outside the `maxPages` window) must still render without throwing, via the existing generic/raw event path — Task 4's manual verification step covers this (no automated DOM test harness exists in this repo for the viewer).

---

## Task 1: WAL record type and block_ref parser (pure, unit-tested)

**Files:**
- Modify: `inspector/internal/adapter/postgres/heappage.go`
- Create: `inspector/internal/adapter/postgres/heappage_test.go`

**Interfaces:**
- Produces: `type WALRecord struct { LSN, Rmgr, RecordType string; Block, Length int; Description string }` (JSON tags: `lsn`, `rmgr`, `record_type`, `block`, `length`, `description`), and `func parseBlockRefBlocks(blockRef string, relfilenode uint32) []int`. Task 2 calls both.

- [ ] **Step 1: Write the failing tests for `parseBlockRefBlocks`**

Create `inspector/internal/adapter/postgres/heappage_test.go`:

```go
package postgres

import (
	"reflect"
	"testing"
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `make test USE_DOCKER=1`
Expected: FAIL — `undefined: parseBlockRefBlocks` (the function doesn't exist yet).

- [ ] **Step 3: Implement `WALRecord` and `parseBlockRefBlocks`**

In `inspector/internal/adapter/postgres/heappage.go`, add near the top (after the `Item`/`Page` types, before `maxPages`):

```go
// WALRecord is one WAL record, read live via pg_walinspect, that touched a
// block of Relation's main fork.
type WALRecord struct {
	LSN         string `json:"lsn"`
	Rmgr        string `json:"rmgr"`
	RecordType  string `json:"record_type"`
	Block       int    `json:"block"`
	Length      int    `json:"length"`
	Description string `json:"description"`
}
```

Add the import `"regexp"` and `"strconv"` to the existing import block, and add near `readPages`:

```go
// blockRefLine matches one "blkref" line from pg_walinspect's block_ref
// column, e.g. "blkref #0: rel 1663/16401/24595 fork main blk 3". A record
// can touch more than one block; block_ref lists one blkref line per block,
// newline-separated.
var blockRefLine = regexp.MustCompile(`rel \d+/\d+/(\d+) fork (\w+) blk (\d+)`)

// parseBlockRefBlocks returns the block numbers in blockRef that belong to
// relfilenode's main fork (the heap's own fork; vm/fsm/init blocks are not
// heap pages the viewer shows, so they are not heap-change causes here).
func parseBlockRefBlocks(blockRef string, relfilenode uint32) []int {
	var blocks []int
	for _, m := range blockRefLine.FindAllStringSubmatch(blockRef, -1) {
		if m[2] != "main" {
			continue
		}
		node, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil || uint32(node) != relfilenode {
			continue
		}
		blk, err := strconv.Atoi(m[3])
		if err != nil {
			continue
		}
		blocks = append(blocks, blk)
	}
	return blocks
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test USE_DOCKER=1`
Expected: PASS for `TestParseBlockRefBlocks`, and `make test`'s `fmt-check`/`vet` stay clean.

- [ ] **Step 5: Commit**

```bash
git add inspector/internal/adapter/postgres/heappage.go inspector/internal/adapter/postgres/heappage_test.go
git commit -m "$(cat <<'EOF'
Add WALRecord type and block_ref parser for the WAL overlay

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

## Task 2: Read WAL records live and wire them into the poll loop with correlation IDs

**Files:**
- Modify: `inspector/internal/adapter/postgres/heappage.go`
- Modify: `inspector/internal/adapter/postgres/heappage_test.go`

**Interfaces:**
- Consumes: `WALRecord`, `parseBlockRefBlocks` (Task 1).
- Produces: `Adapter.readWALRecords(ctx, prevLSN, currLSN string) ([]WALRecord, error)` method; modifies `Adapter.diff` to accept a `correlationID string` parameter; modifies `Adapter.StreamEvents`'s poll loop to call both and emit `wal_record` events. Task 4/5 (viewer, schema docs) consume the resulting event shape (`kind: "wal_record"`, `correlation_id` on both kinds).

- [ ] **Step 1: Write the failing tests for correlation-id assignment and the bootstrap/no-advance guards**

Append to `inspector/internal/adapter/postgres/heappage_test.go`:

```go
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
```

Add `"time"` to the test file's imports if not already present (it is, via the package's existing use — add it to the test file's own import block regardless, since test files need their own imports).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `make test USE_DOCKER=1`
Expected: FAIL to compile — `a.diff` takes 2 arguments in existing code, test calls it with 3; `a.emitWALEvents` undefined.

- [ ] **Step 3: Implement `readWALRecords`, `emitWALEvents`, and update `diff`'s signature**

In `inspector/internal/adapter/postgres/heappage.go`:

Add fields to `Adapter`:

```go
type Adapter struct {
	// Interval is how often the event stream re-reads the page.
	Interval time.Duration

	mu        sync.Mutex
	pool      *pgxpool.Pool
	source    string
	seq       uint64
	prev      map[int]Item
	prevBlock int
	prevLSN   string // "" until the first tick completes
	tick      uint64 // increments once per poll tick, for correlation_id
}
```

Add the WAL query and read method, near `readPages`:

```go
// walRecordsQuery lists every WAL record in the given LSN range that
// touched at least one block (checkpoints and similar records, which touch
// none, are excluded by the WHERE clause). relfilenode and fork filtering
// happens in Go, since block_ref is unstructured text.
const walRecordsQuery = `
	SELECT start_lsn::text, resource_manager, record_type, record_length, description, block_ref
	FROM pg_get_wal_records_info($1::pg_lsn, $2::pg_lsn)
	WHERE block_ref IS NOT NULL`

// readWALRecords returns the WAL records in (prevLSN, currLSN] that touched
// Relation's main fork. The relfilenode is resolved fresh on every call,
// not cached, because VACUUM FULL (one of the adapter's own actions)
// rewrites the relation onto a new relfilenode.
func (a *Adapter) readWALRecords(ctx context.Context, prevLSN, currLSN string) ([]WALRecord, error) {
	pool, err := a.getPool()
	if err != nil {
		return nil, err
	}

	var relfilenode uint32
	err = pool.QueryRow(ctx,
		`SELECT pg_relation_filenode($1::regclass)`, Relation,
	).Scan(&relfilenode)
	if err != nil {
		return nil, fmt.Errorf("postgres: relation filenode: %w", err)
	}

	rows, err := pool.Query(ctx, walRecordsQuery, prevLSN, currLSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: wal records: %w", err)
	}
	defer rows.Close()

	var out []WALRecord
	for rows.Next() {
		var lsn, rmgr, recordType, description, blockRef string
		var length int
		if err := rows.Scan(&lsn, &rmgr, &recordType, &length, &description, &blockRef); err != nil {
			return nil, fmt.Errorf("postgres: scan wal record: %w", err)
		}
		for _, blk := range parseBlockRefBlocks(blockRef, relfilenode) {
			out = append(out, WALRecord{
				LSN: lsn, Rmgr: rmgr, RecordType: recordType,
				Block: blk, Length: length, Description: description,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: wal records rows: %w", err)
	}
	return out, nil
}

// emitWALEvents turns records into adapter.Events of kind "wal_record".
// Records sharing a block get the same correlation_id, scoped to tick, so
// the viewer can group them with the heap-diff event from the same block
// and tick.
func (a *Adapter) emitWALEvents(records []WALRecord, tick uint64, now time.Time) []adapter.Event {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]adapter.Event, 0, len(records))
	for _, rec := range records {
		a.seq++
		out = append(out, adapter.Event{
			ID:            fmt.Sprintf("%s:%d", a.source, a.seq),
			Seq:           a.seq,
			Timestamp:     now,
			Source:        a.source,
			Kind:          "wal_record",
			CorrelationID: fmt.Sprintf("%s:%d:%d", a.source, rec.Block, tick),
			Detail:        rec,
		})
	}
	return out
}
```

Change `diff`'s signature and body to take and use `correlationID`:

```go
// diff compares the last block with the previous read and returns events for
// new tuples, for tuples whose xmax was set (a delete or update), and for
// tuples that vanished from the same block (a vacuum reclaiming a dead tuple).
// Every event gets correlationID, so the viewer can group them with any WAL
// records from the same tick and block.
func (a *Adapter) diff(page Page, now time.Time, correlationID string) []adapter.Event {
	a.mu.Lock()
	defer a.mu.Unlock()

	current := make(map[int]Item, len(page.Items))
	for _, it := range page.Items {
		current[it.LP] = it
	}

	var out []adapter.Event
	emit := func(kind string, detail any) {
		a.seq++
		out = append(out, adapter.Event{
			ID:            fmt.Sprintf("%s:%d", a.source, a.seq),
			Seq:           a.seq,
			Timestamp:     now,
			Source:        a.source,
			Kind:          kind,
			CorrelationID: correlationID,
			Detail:        detail,
		})
	}

	// (unchanged body below this point — same emit() calls as today)
	if a.prev != nil {
		for _, it := range page.Items {
			old, existed := a.prev[it.LP]
			if !existed {
				emit("tuple_inserted", map[string]any{
					"lp":         it.LP,
					"lp_off":     it.Offset,
					"lp_len":     it.Length,
					"t_xmin":     it.XMin,
					"free_space": page.FreeSpace,
				})
				continue
			}
			if old.XMax != it.XMax && it.XMax != "0" {
				emit("tuple_xmax_set", map[string]any{
					"lp":     it.LP,
					"t_xmax": it.XMax,
				})
			}
		}
		if page.Block == a.prevBlock {
			for lp := range a.prev {
				if _, stillThere := current[lp]; !stillThere {
					emit("tuple_removed", map[string]any{
						"lp":         lp,
						"free_space": page.FreeSpace,
					})
				}
			}
		}
	}
	a.prev = current
	a.prevBlock = page.Block
	return out
}
```

Update `StreamEvents`'s poll loop to read WAL records before diffing, and to pass a per-tick correlation id to both:

```go
func (a *Adapter) StreamEvents(ctx context.Context) (<-chan adapter.Event, <-chan error, error) {
	if _, err := a.getPool(); err != nil {
		return nil, nil, err
	}
	events := make(chan adapter.Event)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		ticker := time.NewTicker(a.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				pages, err := a.readPages(ctx)
				if err != nil {
					if ctx.Err() == nil {
						errs <- err
					}
					return
				}
				last := pages.Pages[len(pages.Pages)-1]

				a.mu.Lock()
				prevLSN := a.prevLSN
				tick := a.tick
				a.tick++
				a.prevLSN = last.Header.LSN
				a.mu.Unlock()

				var tickEvents []adapter.Event
				if prevLSN != "" && prevLSN != last.Header.LSN {
					records, err := a.readWALRecords(ctx, prevLSN, last.Header.LSN)
					if err != nil {
						if ctx.Err() == nil {
							errs <- err
						}
						return
					}
					tickEvents = append(tickEvents, a.emitWALEvents(records, tick, now.UTC())...)
				}
				corrID := fmt.Sprintf("%s:%d:%d", a.source, last.Block, tick)
				tickEvents = append(tickEvents, a.diff(last, now.UTC(), corrID)...)

				for _, ev := range tickEvents {
					select {
					case events <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return events, errs, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test USE_DOCKER=1`
Expected: PASS for `TestDiffSetsCorrelationID`, `TestEmitWALEventsCorrelationID`, and all pre-existing tests; `fmt-check`/`vet` clean.

- [ ] **Step 5: Commit**

```bash
git add inspector/internal/adapter/postgres/heappage.go inspector/internal/adapter/postgres/heappage_test.go
git commit -m "$(cat <<'EOF'
Read WAL records live and correlate them with heap-diff events

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

## Task 3: Grant WAL access and check the extension in Connect

**Files:**
- Modify: `adapters/postgres/compose.yml`
- Modify: `inspector/internal/adapter/postgres/heappage.go`

**Interfaces:**
- Consumes: none new.
- Produces: `glasshouse_inspector` can call `pg_get_wal_records_info`; `Connect` fails fast with a clear error if `pg_walinspect` isn't installed, mirroring the existing `pageinspect` check.

- [ ] **Step 1: Add the extension and grant to the init script**

In `adapters/postgres/compose.yml`, inside the `glasshouse_init` config's SQL heredoc, right after `CREATE EXTENSION IF NOT EXISTS pageinspect;`:

```sql
      CREATE EXTENSION IF NOT EXISTS pageinspect;
      CREATE EXTENSION IF NOT EXISTS pg_walinspect;
```

And after the existing `GRANT EXECUTE ON FUNCTION glasshouse_heap_page_items(int) TO glasshouse_inspector;` line, add:

```sql
      -- pg_walinspect's functions are superuser-only by default; this is the
      -- one function the adapter calls, granted directly (least privilege,
      -- narrower than membership in pg_monitor).
      GRANT EXECUTE ON FUNCTION pg_get_wal_records_info(pg_lsn, pg_lsn) TO glasshouse_inspector;
```

- [ ] **Step 2: Add the extension check to `Connect`**

In `inspector/internal/adapter/postgres/heappage.go`, in `Connect`, right after the existing `pageinspect` check (the `if !installed { ... }` block) and before `a.mu.Lock()`:

```go
	var walInspectInstalled bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_walinspect')`,
	).Scan(&walInspectInstalled)
	if err != nil {
		pool.Close()
		return fmt.Errorf("postgres: check pg_walinspect: %w", err)
	}
	if !walInspectInstalled {
		pool.Close()
		return errors.New("postgres: pg_walinspect extension is not installed")
	}
```

- [ ] **Step 3: Run the existing tests to confirm nothing broke**

Run: `make test USE_DOCKER=1`
Expected: PASS (this task adds no new unit-testable logic; `Connect`'s extra check needs a real Postgres instance, covered by Task 6's manual verification).

- [ ] **Step 4: Commit**

```bash
git add adapters/postgres/compose.yml inspector/internal/adapter/postgres/heappage.go
git commit -m "$(cat <<'EOF'
Grant pg_walinspect access and check it on connect

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

## Task 4: Document the new event kind and update the static sample

**Files:**
- Modify: `schema/README.md`
- Modify: `samples/postgres.heap_page.json`

**Interfaces:**
- Consumes: the `wal_record` event shape and `correlation_id` format from Task 2.
- Produces: documentation and sample data Task 5 (viewer) and future readers rely on as the contract reference.

- [ ] **Step 1: Document the event kind in `schema/README.md`**

Add a new subsection right after the existing `## Event` section (after the bullet list ending in "`detail` is adapter-specific."):

```markdown
### Postgres: `wal_record`

The Postgres heap-page adapter also emits `wal_record` events — WAL records,
read live via `pg_walinspect`, that touched one of the relation's blocks.
`detail` is:

```json
{
  "lsn": "0/1A2B3C0",
  "rmgr": "Heap",
  "record_type": "INSERT",
  "block": 5,
  "length": 64,
  "description": "off 12 flags 0x00"
}
```

`correlation_id` on Postgres heap-page events (both `wal_record` and the
existing `tuple_inserted`/`tuple_xmax_set`/`tuple_removed` kinds) has the
form `<source>:<block>:<tick>`. Events sharing one `correlation_id` happened
in the same poll tick and touched the same block — a WAL record and the
heap-diff event it produced always share one.
```

- [ ] **Step 2: Add a paired `wal_record` event to the sample**

In `samples/postgres.heap_page.json`, add `"correlation_id": "postgres:0:4"` to the `"id": "postgres:4"` event's object (the first `tuple_inserted` for `lp: 4`, since the sample's last page is block 0), and insert a new event object right after it in the `events` array:

```json
    {
      "id": "postgres:4b",
      "seq": 21,
      "timestamp": "2026-10-03T19:23:35.542283389Z",
      "source": "postgres",
      "kind": "wal_record",
      "correlation_id": "postgres:0:4",
      "detail": {
        "lsn": "0/196DA10",
        "rmgr": "Heap",
        "record_type": "INSERT",
        "block": 0,
        "length": 64,
        "description": "off 4 flags 0x00"
      }
    },
```

(Leave the rest of the sample's events untouched; this one addition is enough to make the sample demonstrate the pairing without renumbering every existing `seq`/`id`.)

- [ ] **Step 3: Validate the sample is still valid JSON**

Run: `python3 -c "import json; json.load(open('samples/postgres.heap_page.json'))" && echo OK`
Expected: `OK`

- [ ] **Step 4: Commit**

```bash
git add schema/README.md samples/postgres.heap_page.json
git commit -m "$(cat <<'EOF'
Document the wal_record event kind and update the heap page sample

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

## Task 5: Viewer overlay — show correlated WAL records under each heap event

**Files:**
- Modify: `inspector/web/static/views/postgres-heap-page.js`
- Modify: `inspector/web/static/shell.css`

**Interfaces:**
- Consumes: `events` and `focus` as already passed into `render(container, { snapshot, events, focus })` by `app.js` (unchanged shell contract, see `inspector/web/static/views/registry.js`); the `wal_record` event shape and `correlation_id` from Task 2/4.
- Produces: no new exports — this is the view's own internal rendering, self-contained.

- [ ] **Step 1: Add the correlated-WAL lookup and rendering helpers**

In `inspector/web/static/views/postgres-heap-page.js`, add this function above the `registerView(...)` call at the bottom of the file:

```js
// Returns the wal_record events in `events` that share focus's
// correlation_id, oldest first. Returns [] if focus has no correlation_id
// or no visible wal_record event matches it (e.g. its WAL record touched a
// block outside the window currently shown).
function correlatedWALRecords(events, focus) {
  if (!focus?.correlation_id) return [];
  return events.filter((ev) => ev.kind === 'wal_record' && ev.correlation_id === focus.correlation_id);
}

function walRecordsSection(records) {
  const section = document.createElement('div');
  section.className = 'wal-records';
  const heading = document.createElement('h4');
  heading.textContent = records.length === 1 ? 'Produced by this WAL record' : 'Produced by these WAL records';
  section.append(heading);
  for (const rec of records) {
    const d = rec.detail;
    section.append(table(fieldRows({
      lsn: d.lsn, rmgr: d.rmgr, record_type: d.record_type,
      block: d.block, length: d.length, description: d.description,
    })));
  }
  return section;
}
```

Replace the existing `registerView('postgres.heap_page', { ... })` call (the last statement in the file) with this version — the destructured render parameter gains `events`, and two new lines are added right before `container.replaceChildren(wrap)`; every other line of the existing body is unchanged:

```js
registerView('postgres.heap_page', {
  title: 'Heap page (byte map)',
  render(container, { snapshot, events, focus }) {
    const data = snapshot.data;
    const pages = data.pages;
    const focusLP = focus?.detail?.lp ?? null;

    const wrap = document.createElement('div');
    wrap.className = 'heap-page';

    const blockLabel = pages.length > 1
      ? `blocks ${pages[0].block}–${pages[pages.length - 1].block}`
      : `block ${pages[0].block}`;
    const totalItems = pages.reduce((n, p) => n + p.items.length, 0);
    const summary = document.createElement('p');
    summary.className = 'small';
    summary.textContent = `${data.relation} · ${blockLabel} · ` +
      `${totalItems} item pointers`;

    const grid = document.createElement('div');
    grid.className = 'heap-grid';

    const pagesBox = document.createElement('div');
    pagesBox.className = 'heap-pages';

    const detail = document.createElement('div');
    detail.className = 'heap-detail';
    const detailTitle = document.createElement('h3');
    detailTitle.textContent = 'Hover a region';
    detailTitle.style.margin = '0 0 8px';
    detail.append(detailTitle);

    const onEnter = (d) => {
      detailTitle.textContent = d.title;
      detail.replaceChildren(detailTitle, table(d.rows));
    };

    for (const page of pages) {
      const mapBox = document.createElement('div');
      mapBox.className = 'heap-map';
      const heading = document.createElement('p');
      heading.className = 'small muted';
      heading.style.margin = '0 0 4px';
      heading.textContent = `Block ${page.block} · ${page.items.length} rows · ` +
        `${page.free_space} bytes free`;
      mapBox.append(heading, buildMap(page, focusLP, onEnter));
      pagesBox.append(mapBox);
    }

    grid.append(pagesBox, detail);
    wrap.append(summary, grid, legend());

    const walRecords = correlatedWALRecords(events, focus);
    if (walRecords.length > 0) {
      wrap.append(walRecordsSection(walRecords));
    }

    container.replaceChildren(wrap);
  },
});
```

- [ ] **Step 2: Add minimal styling for the new section**

In `inspector/web/static/shell.css`, after the existing `.heap-detail h3 { font-size: 14px; }` line, add:

```css
.wal-records { margin-top: 14px; }
.wal-records h4 { font-size: 13px; margin: 0 0 6px; color: var(--muted); }
.wal-records table.kv { margin-bottom: 8px; }
```

- [ ] **Step 3: Verify with the mock adapter that nothing broke**

Run: `make run-mock` (or `make build && ./inspector/bin/inspector -adapter mock -origin http://localhost:8765` if `run-mock` needs a port already in use elsewhere), then open `http://localhost:8765` in a browser.
Expected: the mock counter view still renders (it's a different `snapshot.type`, unaffected by this change) — this step only confirms the build/serve path isn't broken by the edit. Full WAL-overlay verification needs the real Postgres lab and happens in Task 6.

- [ ] **Step 4: Commit**

```bash
git add inspector/web/static/views/postgres-heap-page.js inspector/web/static/shell.css
git commit -m "$(cat <<'EOF'
Show correlated WAL records inline under heap page events

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

## Task 6: CLAUDE.md gotcha, manual verification against the real stack

**Files:**
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing consumed by later tasks — this is the last task.

- [ ] **Step 1: Add the relfilenode gotcha to `CLAUDE.md`**

In `CLAUDE.md`'s "Gotchas we already hit" section, add a bullet (alphabetical/thematic placement doesn't matter — append near the other VACUUM-related bullet):

```markdown
- `VACUUM (FULL)` rewrites the table onto a new relfilenode, so the WAL
  overlay's relation-to-WAL-record matching (`readWALRecords` in
  `heappage.go`) re-resolves `pg_relation_filenode('glasshouse_demo')` on
  every poll tick instead of caching it — a cached value would silently stop
  matching any WAL record after the first `vacuum_full` action.
```

- [ ] **Step 2: Run the full test suite**

Run: `make test USE_DOCKER=1`
Expected: PASS, no `fmt-check`/`vet` issues.

- [ ] **Step 3: Manual verification against the real Postgres lab**

Run:
```bash
make stack-down   # in case the mock/host inspector is running on :8765
make stack-up
```
Open `http://localhost:8765` (or whatever the compose-published article panel points at), confirm the heap page view loads. Then, from a second terminal:
```bash
curl -s -X POST http://127.0.0.1:8765/actions/insert_rows -H 'X-Glasshouse-Action: 1'
```
Wait a couple of poll intervals (the adapter polls every 500ms), then in the browser: step the timeline back to one of the new `tuple_inserted` ticks and confirm a "Produced by this WAL record" (or "...these WAL records") section appears below the byte map legend, showing a `Heap`/`INSERT` record. Repeat with `update_rows` and `delete_rows` actions, confirming `Heap`/`UPDATE`-ish and the delete's WAL record type show up correspondingly. Then run `vacuum_full` and confirm a subsequent `insert_rows` still produces a correlated WAL record (proving the fresh-relfilenode-per-tick fix works, per the Task 2/6 deviation note).

Run `make stack-down` when done.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
Document the VACUUM FULL relfilenode gotcha for the WAL overlay

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```
