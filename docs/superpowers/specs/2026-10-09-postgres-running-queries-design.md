# Postgres running queries section: design

Date: 2026-10-09
Status: draft for review

## Purpose

Glasshouse shows PostgreSQL's heap (and, in the index lab, index) page
layout from a real running instance. This adds a third kind of real state:
what the server is doing right now, pulled live from `pg_stat_activity`, so
a reader can see the demo actions (`insert_rows`, `update_rows`,
`delete_rows`, `vacuum_full`) as actual running backends while they execute,
not just their after-effects on the pages above.

Success: both existing labs (`postgres` and `postgres-index`) show a
running-queries panel listing every active, non-idle backend on the
database, with real PIDs, states, wait events, durations, and query text,
updating every poll.

## Scope

In scope:

- A new `readRunningQueries` helper reading `pg_stat_activity`, shared by
  both existing adapters.
- A `queries` field added to both existing snapshot data shapes
  (`postgres.heap_page` and `postgres.heap_and_index`). No new snapshot
  type.
- A `pg_read_all_stats` grant so query text is visible for every backend,
  not just `glasshouse_inspector`'s own.
- A shared render helper for the queries table, used by both existing view
  modules.
- `schema/README.md` and both static samples updated.

Out of scope (deferred):

- A standalone query-timeline or history view.
- Diffing/events for query start/finish — the panel is a point-in-time
  snapshot, redrawn each poll, like the heap/index pages.
- Any target other than the one fixed database the Inspector is connected
  to.
- The WAL overlay (`2026-10-09-postgres-wal-overlay-design.md`) — unrelated,
  unaffected.

## Decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Query scope | All active backends on the target database (`datname = current_database()`), not just ones touching `glasshouse_demo` | User chose a real "what's running" dashboard over the narrower fixed-target philosophy used elsewhere in this repo. |
| Delivery | Part of the polled `Snapshot`, not events | `pg_stat_activity` is inherently a live point-in-time list; there is no natural start/finish event shape for it, unlike heap tuple changes. |
| Permission | Grant `pg_read_all_stats` to `glasshouse_inspector` | Without it, other roles' `query` column reads `NULL` in `pg_stat_activity`, which would make the "all activity" scope misleading. `pg_read_all_stats` is the minimal predefined role for this — narrower than `pg_monitor`, which also grants settings/table-scan visibility this feature doesn't need. |
| Filtering | Exclude `pid = pg_backend_pid()` and `state = 'idle'` | Without excluding itself, the Inspector's own polling query would appear in its own output every tick, a self-referential artifact with no reader value. Idle connections are noise for a "what's running" panel. |
| Snapshot shape | Sibling `queries` field, not nested inside `HeapPages` | `WithIndexAdapter` reuses the plain `Adapter`'s `readPages` for its own `heap` field; adding `Queries` to `HeapPages` itself would either duplicate the read or leave it always empty there. A wrapper struct per adapter keeps `queries` at the top level of each snapshot's `data` without touching the shared `HeapPages`/`HeapAndIndex` types. |
| Row cap | `maxQueries = 20`, same capping pattern as `maxPages`/`maxIndexPages` | Keeps the read and the render cheap even under many concurrent connections; a `truncated` flag is unnecessary here since the panel is informational, not a structural tree the reader needs to know is cut off. |

## Components

### 1. `inspector/internal/adapter/postgres/runningqueries.go` (new)

```go
// RunningQuery is one active backend from pg_stat_activity.
type RunningQuery struct {
	PID             int    `json:"pid"`
	Usename         string `json:"usename"`
	ApplicationName string `json:"application_name"`
	State           string `json:"state"`
	WaitEventType   string `json:"wait_event_type"`
	WaitEvent       string `json:"wait_event"`
	QueryStart      string `json:"query_start"` // RFC3339, empty if NULL
	DurationMS      int64  `json:"duration_ms"`
	Query           string `json:"query"`
}

const maxQueries = 20
```

- `readRunningQueries(ctx, pool)`:
  ```sql
  SELECT pid, usename, application_name, state,
         coalesce(wait_event_type, ''), coalesce(wait_event, ''),
         query_start, query
  FROM pg_stat_activity
  WHERE datname = current_database()
    AND pid != pg_backend_pid()
    AND state != 'idle'
  ORDER BY query_start
  LIMIT $1
  ```
  Bound parameter is only the constant `maxQueries`, same "constant SQL,
  bound integers only" rule as the rest of the adapter. `DurationMS` is
  computed in Go as `time.Since(queryStart).Milliseconds()` after scanning
  `query_start` as `time.Time` (nullable via `sql.NullTime`; `0`/empty when
  NULL, which happens only for backends with no current query transition —
  rare given the `state != 'idle'` filter, but handled the same way the
  heap reader handles nullable columns).
  Returns `[]RunningQuery`, non-nil (empty slice, not nil, when no rows —
  same convention as `Page.Items`/`IndexPages.Pages`).

### 2. `inspector/internal/adapter/postgres/heappage.go` (modified)

- `Adapter.Snapshot` gains a call to `readRunningQueries` after
  `readPages`, and returns:
  ```go
  type heapSnapshot struct {
  	HeapPages
  	Queries []RunningQuery `json:"queries"`
  }
  ```
  so `data` for `postgres.heap_page` becomes
  `{relation, pages, queries}` — `HeapPages` itself is untouched, so
  `WithIndexAdapter`'s internal reuse of `a.readPages` is unaffected.

### 3. `inspector/internal/adapter/postgres/btreepage.go` (modified)

- `WithIndexAdapter.Snapshot` gains the same `readRunningQueries` call and
  returns:
  ```go
  type heapAndIndexSnapshot struct {
  	HeapAndIndex
  	Queries []RunningQuery `json:"queries"`
  }
  ```
  so `data` for `postgres.heap_and_index` becomes
  `{relation, heap, index, queries}`.

### 4. `adapters/postgres/compose.yml` (init script)

```sql
GRANT pg_read_all_stats TO glasshouse_inspector;
```

Added alongside the existing `CONNECT`/`SELECT`/pageinspect/btree grants.

### 5. `inspector/web/static/views/`

- New shared helper, exported from a small module (e.g.
  `running-queries.js`): `renderRunningQueries(container, queries)` —
  builds a table (PID, state, wait event, duration, query text truncated
  for display) using `textContent` only for every cell, including the
  query text (never `innerHTML`, consistent with the existing security
  rule).
- `postgres-heap-page.js` and `postgres-heap-and-index.js`: both import and
  call `renderRunningQueries` for a new section below their existing
  content, passing `snapshot.data.queries`.
- `registry.js`: unchanged — no new `snapshot.type`, so no new registration.

### 6. `schema/README.md`

Update both existing rows in the adapter-specific shapes table to include
`queries: [{pid, usename, application_name, state, wait_event_type,
wait_event, query_start, duration_ms, query}]`. `SchemaVersion` stays 1 —
additive field on existing types.

### 7. `samples/postgres.heap_page.json`, `samples/postgres.heap_and_index.json`

Add a representative `queries` array (one or two rows, e.g. one matching an
`insert_rows` action in flight) to both.

## Data flow

1. Reader runs either existing lab (`postgres` or `postgres-index`),
   unchanged start commands.
2. Each snapshot read, the adapter reads heap (and index, if applicable)
   pages as today, then reads `pg_stat_activity` the same poll.
3. The viewer's existing panel for either lab renders a new running-queries
   table below its current content, from `snapshot.data.queries`.
4. Triggering an action (`insert_rows` etc.) may show up as a transient row
   in the table on the next poll if the action's query is still running
   when the server reads it — otherwise the panel simply reflects whatever
   else is connected (e.g. a reader's own `psql` session).

## Error handling

A failure in `readRunningQueries` (e.g. the grant is missing) fails the
whole `Snapshot()` call, returned the same way a heap or index read failure
already is — no partial snapshot, no new error path.

## Testing

- No unit test for `readRunningQueries` itself — like the WAL and
  index-tree *reads* (not their parsing logic), there's no pure parsing
  step to isolate; the SQL has no branching logic worth a fixture test.
- `inspector/internal/server`: existing adapter-selection tests are
  unaffected (no new `-adapter` value, no new route).
- `make test` (gofmt check, vet, unit tests); `USE_DOCKER=1` on this host.
- Manual: `make stack-up` (and again with `GLASSHOUSE_ADAPTER=postgres-index`),
  run each demo action, confirm the running-queries panel appears in both
  labs and reflects real backends (including a manually opened `psql`
  session left idle-in-transaction, to confirm the `state != 'idle'` filter
  behaves as expected — idle-in-transaction is a distinct state from idle
  and should still show).

## Security

No new routes, no new browser input. `pg_read_all_stats` only grants
`glasshouse_inspector` read access to Postgres's own statistics views — it
does not grant table data access beyond what it already has, and the
adapter's SQL stays constant (only the fixed `maxQueries` bind parameter).
Query text from other sessions is inherently uncontrolled input from the
database's perspective, but it is rendered with `textContent` only, same as
every other adapter-sourced string already handled by this codebase.
