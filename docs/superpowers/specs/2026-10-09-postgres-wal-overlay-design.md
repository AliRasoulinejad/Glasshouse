# Postgres WAL overlay: design

Date: 2026-10-09
Status: draft for review

## Purpose

Glasshouse shows PostgreSQL's heap page layout from a real running instance.
This adds the WAL (write-ahead log) side of the same story: for each heap
page change the existing heap page view already shows (`tuple_inserted`,
`tuple_xmax_set`, `tuple_removed`), the reader also sees the real WAL
record(s) — read via `pg_walinspect` from the running instance's actual log —
that produced it. No simulated or reconstructed WAL; it is decoded from the
live WAL stream the same way the heap bytes are read live via `pageinspect`.

This is the first half of two: an inline overlay on the existing heap page
view now, with a standalone WAL timeline view deferred — the event shape
chosen here (sibling `wal_record` events, not nested inside heap events) is
deliberately the one a standalone timeline could reuse without rework.

Success: a reader watching `insert_rows`/`update_rows`/`delete_rows` on the
existing heap page article also sees, inline, which real WAL record(s) each
resulting page change came from — rmgr, record type, LSN — pulled from their
own running Postgres.

## Scope

In scope:

- Reading WAL records for the demo relation's visible blocks via
  `pg_walinspect.pg_get_wal_records_info`, scoped per poll tick.
- A new event kind, `wal_record`, emitted by the existing heap page adapter
  alongside its current `tuple_inserted`/`tuple_xmax_set`/`tuple_removed`
  kinds.
- `correlation_id` on both the new `wal_record` events and the existing heap
  diff events, scoped per block per tick, so a viewer can group them.
- Overlay rendering in the existing heap page view: each heap diff event
  shows its correlated WAL record(s) inline, always expanded.
- Permission grant for `pg_walinspect` functions to `glasshouse_inspector`.
- `schema/README.md` and sample updates.

Out of scope (deferred):

- A standalone WAL timeline view (separate from the heap page view).
- Any WAL record not touching `glasshouse_demo`'s visible blocks (e.g.
  checkpoint records, other relations' records).
- Transaction id (`xid`) in the event detail.
- The index page view (`2026-10-08-postgres-index-view-design.md`) — unrelated,
  unaffected.

## Decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Access mechanism | Grant `pg_monitor` (or targeted `EXECUTE`) on `pg_walinspect` functions to `glasshouse_inspector`, no wrapper functions | WAL reading isn't row-security sensitive like `pageinspect`'s raw page functions; a privilege grant is pg_walinspect's intended usage path, and avoids an unnecessary `SECURITY DEFINER` wrapper. |
| Event shape | New sibling event kind `wal_record`, linked to heap diff events via `correlation_id`, not nested in their `Detail` | Matches the event envelope's existing but unused `CorrelationID` field. Keeps the heap event schema untouched and lets a future standalone WAL timeline reuse these events unchanged. |
| Correlation grain | Per block, per tick: `correlation_id = "<source>:<block>:<tick-seq>"`, unique every tick | Per-tick-only grouping risks linking a WAL record for one block to a heap diff on a different block shown in the same poll window — misleading for a tool whose point is causality. Per-block avoids that; making the ID tick-unique avoids stale cross-tick grouping if old events linger in the viewer's log. |
| WAL range per tick | `pg_get_wal_records_info(prevLSN, currLSN)` where `currLSN` is the last visible block's current `page_header().lsn` | Reuses data already read this tick; no extra round trip to ask Postgres for "current WAL position" separately. |
| Block matching | Parse `block_ref` text (`"rel <ts>/<db>/<relfilenode> fork <name> blk <n>"`) for relfilenode + block number; relfilenode resolved once via `pg_relation_filenode('glasshouse_demo')` and cached | `pg_get_wal_records_info` returns block refs as text, not structured columns, in the available version; parsing once per tick is cheap and the format is documented/stable. |
| Event detail fields | `lsn`, `rmgr`, `record_type`, `block`, `length`, `description` | Enough to show what kind of WAL record it was and why, without `xid` or raw bytes, which add detail the overlay doesn't need for v1. |
| Overlay rendering | Always expanded (no collapse/expand interaction) | Simpler viewer code; the reader sees the causal link immediately without an extra click. |

## Components

### 1. `inspector/internal/adapter/postgres/heappage.go` (modified)

```go
// Adapter gains:
type Adapter struct {
    // ... existing fields ...
    relfilenode uint32 // resolved once in Connect
    prevLSN     string // "" until the first tick completes
}
```

- `Connect`: after the existing pageinspect check, resolve and cache
  `pg_relation_filenode('glasshouse_demo')` into `a.relfilenode`.
- `readWALRecords(ctx, pool, prevLSN, currLSN string) ([]WALRecord, error)`:
  calls `pg_get_wal_records_info($1, $2)` (bind params, constant SQL), for
  each row parses `block_ref` for relfilenode + block, keeps only rows whose
  relfilenode matches `a.relfilenode`, returns one `WALRecord` per match
  (a record touching multiple blocks of ours yields multiple `WALRecord`
  values, one per block, since the overlay groups by block).

```go
type WALRecord struct {
    LSN         string `json:"lsn"`
    Rmgr        string `json:"rmgr"`
    RecordType  string `json:"record_type"`
    Block       int    `json:"block"`
    Length      int    `json:"length"`
    Description string `json:"description"`
}
```

- `StreamEvents`'s poll loop, after `readPages` and before calling `a.diff`:
  1. `currLSN := last block's Header.LSN`.
  2. If `a.prevLSN != ""`, call `readWALRecords(ctx, pool, a.prevLSN, currLSN)`.
  3. For each `WALRecord`, emit a `wal_record` event (same `emit` helper
     pattern as `diff`, so it shares the adapter's `a.seq` counter) with
     `CorrelationID = fmt.Sprintf("%s:%d:%d", a.source, rec.Block, a.seq)`
     captured *before* incrementing seq for this tick's heap diff, so heap
     events from the same tick/block can reuse it (see below).
  4. Run the existing `a.diff(last, now)`, passing down the per-block
     correlation ids computed in step 3 so matching heap events get the same
     `CorrelationID` (events for a block with no WAL match get none — fine,
     a tick where the diff fires but no WAL record matched, e.g. a block
     outside the read range, simply shows the heap event alone).
  5. `a.prevLSN = currLSN` unconditionally at the end of the tick.
- First tick: `a.prevLSN == ""`, so no WAL read happens — same bootstrap rule
  the heap diff already follows for `a.prev == nil`.

### 2. `adapters/postgres/compose.yml` (init script)

```sql
GRANT pg_monitor TO glasshouse_inspector;
```

Added alongside the existing `pageinspect`/`MAINTAIN` grants. (If `pg_monitor`
proves broader than needed, fall back to targeted
`GRANT EXECUTE ON FUNCTION pg_get_wal_records_info(pg_lsn, pg_lsn) TO glasshouse_inspector;`
— decided during implementation by whichever grants cleanly in PG17.)

### 3. `schema/README.md`

New subsection under "Event" documenting the `wal_record` kind's `detail`
shape, and a note that Postgres heap-page events now carry `correlation_id`
in the format `<source>:<block>:<tick-seq>`.

### 4. `inspector/web/static/views/postgres-heap-page.js`

- When rendering the event timeline, group the current batch of events by
  `correlation_id` first. For each heap diff event with a non-empty
  `correlation_id`, render any `wal_record` events sharing it directly
  beneath it (e.g. "Produced by WAL: Heap/INSERT @ 0/1A2B3C0, 64 bytes"),
  always expanded, via `textContent` only.
- `wal_record` events with no co-occurring heap event in the same batch
  (block outside the `maxPages` window) fall through to the existing
  generic/unknown-kind rendering in the raw event log.
- No changes to `registry.js` — same `postgres.heap_page` snapshot type.

### 5. `samples/postgres.heap_page.json`

Add at least one `wal_record` event paired with an existing heap event via
`correlation_id`, so the static sample and the Website stay representative
without needing a live instance.

## Data flow

1. Reader runs the existing heap page lab (unchanged start command).
2. Each poll tick, the adapter reads the heap pages (unchanged), then reads
   WAL records in the LSN range since the previous tick, filtered to the
   relation's relfilenode and visible blocks.
3. Matching WAL records and the heap diff events from the same tick/block
   share a `correlation_id`.
4. The viewer's event timeline renders heap events with their correlated WAL
   record(s) inline, expanded.

## Error handling

A failure in `readWALRecords` (e.g. the grant is missing, or `pg_walinspect`
isn't installed) is treated like any other adapter read failure: returned up
through `StreamEvents`'s error channel, which the server already turns into
a terminal stream error. `Connect` additionally checks
`pg_extension` for `pg_walinspect` the same way it already checks for
`pageinspect`, failing fast with a clear message instead of failing later
mid-stream.

## Testing

- `inspector/internal/adapter/postgres`: table-driven unit tests for the
  `block_ref` parser (various relfilenode/fork/block combinations, including
  forks other than `main` which should be ignored) and for the correlation-id
  assignment logic in the poll loop (given a set of WAL records and heap diff
  events for one tick, confirm matching `correlation_id`s and confirm no
  WAL read happens on the bootstrap tick).
- The WAL-fetching call itself (`pg_get_wal_records_info` against real WAL)
  is not unit-tested — no realistic way to fake real WAL content — and is
  covered by manual/integration testing via `adapters/postgres/demo.sh`-style
  runs against the real container, matching how `pageinspect`-based reads
  are already tested in this repo.
- `make test` (gofmt check, vet, unit tests); `USE_DOCKER=1` on this host.
- Manual: `make stack-up`, run `insert_rows`/`update_rows`/`delete_rows`,
  confirm each resulting heap event shows its correlated WAL record(s) in
  the viewer.

## Security

No new routes, no new browser input. The `pg_monitor` (or targeted
`EXECUTE`) grant only lets `glasshouse_inspector` read WAL metadata through
`pg_walinspect`'s own functions — it does not grant filesystem access, and
the adapter's SQL stays constant (only LSN bind parameters vary, exactly
like the existing page-number bind parameters).
