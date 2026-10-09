# Postgres running-queries section Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a running-queries panel (pulled live from `pg_stat_activity`) to both existing Postgres labs (`postgres` → `postgres.heap_page`, `postgres-index` → `postgres.heap_and_index`), showing every active, non-idle backend on the database while the demo actions run.

**Architecture:** A new `readRunningQueries(ctx, pool)` helper in a new file reads `pg_stat_activity`, filtered and capped. Both existing `Adapter.Snapshot` (heap-only) and `WithIndexAdapter.Snapshot` (heap+index) call it and add a sibling `queries` field to their own JSON payload via a local wrapper struct, without modifying the shared `HeapPages`/`HeapAndIndex` types. On the web side, a new shared render function is imported by both existing view modules, the same way `postgres-heap-and-index.js` already imports `renderHeapSection` from `postgres-heap-page.js`.

**Tech Stack:** Go (pgx/v5), plain ES modules (no build step), PostgreSQL 17 `pg_stat_activity` / predefined role `pg_read_all_stats`.

**Spec:** `docs/superpowers/specs/2026-10-09-postgres-running-queries-design.md`

## Global Constraints

- Every SQL statement stays constant text; the only bound parameter is the fixed `maxQueries` cap — no browser input ever reaches SQL.
- The viewer writes all adapter-sourced strings (including query text) with `textContent` only, never `innerHTML`.
- `SchemaVersion` (in `inspector/internal/adapter/adapter.go`) stays `1` — this is an additive field on existing snapshot types, not a breaking change.
- `gofmt -l` must report nothing changed; `go vet` must be clean (enforced by `make test`).
- Non-nil, possibly-empty slices for list fields (`RunningQuery` list), matching the existing convention in `Page.Items` / `IndexPages.Pages`.

## Review Focus

- **No active backends at all** (quiet database, e.g. right after `stack-up` before any action runs): `readRunningQueries` must return an empty, non-nil slice, and the viewer must render an empty table/placeholder, not throw on `queries.length === 0`. Covered in Task 1 (empty-rows case) and Task 5 (manual check with no action running).
- **NULL `wait_event_type`/`wait_event`** (a backend with CPU-bound work, not waiting on anything): the SQL coalesces these to `''`; a reader should see an empty cell, not the literal string `"null"` or a Go zero-value artifact. Covered by the `coalesce` in the SQL itself (Task 1) and confirmed by the sample data in Task 4 including at least one row with empty wait fields.
- **NULL `query_start`** (should not normally occur once `state != 'idle'` filters it, but a backend transitioning state between the filter and the scan is possible): `queryDuration` must return `0`, not panic on `.Time` of an invalid `sql.NullTime`. Covered by a dedicated unit test in Task 1.
- **Very long query text** (e.g. a reader's own exploratory `SELECT * FROM glasshouse_demo WHERE payload LIKE '%...%'` with a long literal): the view must truncate for display without breaking the table layout or including unescaped HTML, since it's rendered with `textContent` (which cannot inject HTML, but an untruncated row can still blow out table width). Covered in Task 5 by truncating query text in `renderRunningQueries`.
- **Both adapters emitting the same field shape**: `postgres.heap_page`'s and `postgres.heap_and_index`'s `queries` arrays must use the exact same per-row shape (same JSON field names), since the view's shared render function is used unmodified by both. Covered by Task 2 and Task 3 both consuming the same `RunningQuery` type from Task 1, and Task 5 using one render function for both.

---

### Task 1: `readRunningQueries` helper and duration logic

**Files:**
- Create: `inspector/internal/adapter/postgres/runningqueries.go`
- Test: `inspector/internal/adapter/postgres/runningqueries_test.go`

**Interfaces:**
- Produces: `type RunningQuery struct { PID int; Usename string; ApplicationName string; State string; WaitEventType string; WaitEvent string; QueryStart string; DurationMS int64; Query string }` (all fields JSON-tagged per the names below), `const maxQueries = 20`, `func readRunningQueries(ctx context.Context, pool *pgxpool.Pool) ([]RunningQuery, error)`. Tasks 2 and 3 call `readRunningQueries` and embed `[]RunningQuery` as a `Queries` field in their own response structs.

- [ ] **Step 1: Write the failing test for `queryDuration`**

Create `inspector/internal/adapter/postgres/runningqueries_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestQueryDuration -v`
Expected: FAIL — `queryDuration` and `RunningQuery`'s package are not yet defined (compile error).

- [ ] **Step 3: Write `runningqueries.go`**

Create `inspector/internal/adapter/postgres/runningqueries.go`:

```go
// Running-queries reader: a live, point-in-time read of pg_stat_activity,
// added as a sibling field on both existing snapshot types rather than a
// new snapshot type of its own.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunningQuery is one active backend from pg_stat_activity.
type RunningQuery struct {
	PID             int    `json:"pid"`
	Usename         string `json:"usename"`
	ApplicationName string `json:"application_name"`
	State           string `json:"state"`
	WaitEventType   string `json:"wait_event_type"`
	WaitEvent       string `json:"wait_event"`
	// QueryStart is RFC3339, or "" if NULL.
	QueryStart string `json:"query_start"`
	DurationMS int64  `json:"duration_ms"`
	Query      string `json:"query"`
}

// maxQueries caps how many backends one snapshot reads, so a busy database
// stays cheap to read and draw.
const maxQueries = 20

// runningQueriesSQL excludes the Inspector's own polling backend (it would
// otherwise show up in its own output every tick) and idle connections,
// keeping the panel to backends actually doing work. datname is scoped to
// the Inspector's own connected database, not a browser-chosen value.
const runningQueriesSQL = `
	SELECT pid, usename, application_name, state,
	       coalesce(wait_event_type, ''), coalesce(wait_event, ''),
	       query_start, query
	FROM pg_stat_activity
	WHERE datname = current_database()
	  AND pid != pg_backend_pid()
	  AND state != 'idle'
	ORDER BY query_start
	LIMIT $1`

// queryDuration returns how long a backend's current query has been
// running, or 0 if query_start is NULL (no query transition recorded yet).
func queryDuration(start sql.NullTime, now time.Time) int64 {
	if !start.Valid {
		return 0
	}
	return now.Sub(start.Time).Milliseconds()
}

// readRunningQueries reads the current active backends on pool's database,
// through the pg_read_all_stats grant the init script installs (needed to
// see other roles' query text in pg_stat_activity).
func readRunningQueries(ctx context.Context, pool *pgxpool.Pool) ([]RunningQuery, error) {
	rows, err := pool.Query(ctx, runningQueriesSQL, maxQueries)
	if err != nil {
		return nil, fmt.Errorf("postgres: pg_stat_activity: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	out := []RunningQuery{}
	for rows.Next() {
		var q RunningQuery
		var start sql.NullTime
		if err := rows.Scan(&q.PID, &q.Usename, &q.ApplicationName, &q.State,
			&q.WaitEventType, &q.WaitEvent, &start, &q.Query); err != nil {
			return nil, fmt.Errorf("postgres: scan pg_stat_activity row: %w", err)
		}
		if start.Valid {
			q.QueryStart = start.Time.UTC().Format(time.RFC3339)
		}
		q.DurationMS = queryDuration(start, now)
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: pg_stat_activity rows: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestQueryDuration -v`
Expected: PASS for both `TestQueryDurationValidStart` and `TestQueryDurationNullStart`.

- [ ] **Step 5: Run the full package test suite and gofmt/vet checks**

Run: `cd inspector && gofmt -l . && go vet ./... && go test ./... -count=1`
Expected: `gofmt -l .` prints nothing; `go vet` and `go test` both pass (existing tests unaffected).

- [ ] **Step 6: Commit**

```bash
git add inspector/internal/adapter/postgres/runningqueries.go inspector/internal/adapter/postgres/runningqueries_test.go
git commit -m "$(cat <<'EOF'
Add readRunningQueries, a pg_stat_activity reader for both Postgres adapters

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Wire running queries into the heap-only adapter (`postgres.heap_page`)

**Files:**
- Modify: `inspector/internal/adapter/postgres/heappage.go` (the `Snapshot` method, currently around line 266)

**Interfaces:**
- Consumes: `readRunningQueries(ctx, pool)` and `type RunningQuery` from Task 1; `a.getPool()` (existing method returning `(*pgxpool.Pool, error)`).
- Produces: `postgres.heap_page`'s snapshot `data` now has a `queries` field. No new exported type needed outside the package — `heapSnapshot` is local to `heappage.go`.

- [ ] **Step 1: Write the failing test**

There is no existing unit test file for `Adapter.Snapshot` (it requires a live pool), so this change is verified by the package build plus a compile-time shape check. Add to `inspector/internal/adapter/postgres/runningqueries_test.go` (same file, since it is the natural home for this package's JSON-shape assertions):

```go
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
```

Add `"encoding/json"` to that test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestHeapSnapshotJSON -v`
Expected: FAIL — `heapSnapshot` is not defined.

- [ ] **Step 3: Define `heapSnapshot` and update `Adapter.Snapshot`**

In `inspector/internal/adapter/postgres/heappage.go`, add above the `Snapshot` method:

```go
// heapSnapshot is the data payload for postgres.heap_page: HeapPages'
// fields (relation, pages) plus a sibling queries list. It is not a field
// on HeapPages itself because WithIndexAdapter.Snapshot (btreepage.go)
// reuses Adapter.readPages for its own "heap" field and must not carry a
// duplicate or stale queries list there.
type heapSnapshot struct {
	HeapPages
	Queries []RunningQuery `json:"queries"`
}
```

Replace the existing `Snapshot` method body:

```go
// Snapshot returns the current heap pages and the database's active queries.
func (a *Adapter) Snapshot(ctx context.Context) (adapter.Snapshot, error) {
	pages, err := a.readPages(ctx)
	if err != nil {
		return adapter.Snapshot{}, err
	}
	pool, err := a.getPool()
	if err != nil {
		return adapter.Snapshot{}, err
	}
	queries, err := readRunningQueries(ctx, pool)
	if err != nil {
		return adapter.Snapshot{}, err
	}
	a.mu.Lock()
	seq := a.seq
	source := a.source
	a.mu.Unlock()
	return adapter.Snapshot{
		Type:      Type,
		Source:    source,
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Data:      heapSnapshot{HeapPages: pages, Queries: queries},
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestHeapSnapshotJSON -v`
Expected: PASS.

- [ ] **Step 5: Run the full package test suite and gofmt/vet checks**

Run: `cd inspector && gofmt -l . && go vet ./... && go test ./... -count=1`
Expected: clean, all pass.

- [ ] **Step 6: Commit**

```bash
git add inspector/internal/adapter/postgres/heappage.go inspector/internal/adapter/postgres/runningqueries_test.go
git commit -m "$(cat <<'EOF'
Add running queries to the postgres.heap_page snapshot

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Wire running queries into the heap+index adapter (`postgres.heap_and_index`)

**Files:**
- Modify: `inspector/internal/adapter/postgres/btreepage.go` (the `WithIndexAdapter.Snapshot` method)

**Interfaces:**
- Consumes: `readRunningQueries(ctx, pool)` and `type RunningQuery` from Task 1 (same helper as Task 2 — not re-implemented).
- Produces: `postgres.heap_and_index`'s snapshot `data` now has a `queries` field, sibling to `relation`/`heap`/`index`.

- [ ] **Step 1: Write the failing test**

Add to `inspector/internal/adapter/postgres/runningqueries_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestHeapAndIndexSnapshotJSON -v`
Expected: FAIL — `heapAndIndexSnapshot` is not defined.

- [ ] **Step 3: Define `heapAndIndexSnapshot` and update `WithIndexAdapter.Snapshot`**

In `inspector/internal/adapter/postgres/btreepage.go`, add above the `Snapshot` method:

```go
// heapAndIndexSnapshot is the data payload for postgres.heap_and_index:
// HeapAndIndex's fields (relation, heap, index) plus a sibling queries
// list, read once per poll alongside the heap and index pages.
type heapAndIndexSnapshot struct {
	HeapAndIndex
	Queries []RunningQuery `json:"queries"`
}
```

Replace the existing `Snapshot` method body:

```go
// Snapshot returns the current heap pages, index pages, and active queries
// together.
func (a *WithIndexAdapter) Snapshot(ctx context.Context) (adapter.Snapshot, error) {
	heap, err := a.readPages(ctx)
	if err != nil {
		return adapter.Snapshot{}, err
	}
	pool, err := a.getPool()
	if err != nil {
		return adapter.Snapshot{}, err
	}
	index, err := readIndexPages(ctx, pool)
	if err != nil {
		return adapter.Snapshot{}, err
	}
	queries, err := readRunningQueries(ctx, pool)
	if err != nil {
		return adapter.Snapshot{}, err
	}

	a.mu.Lock()
	seq := a.seq
	source := a.source
	a.mu.Unlock()

	return adapter.Snapshot{
		Type:      TypeHeapAndIndex,
		Source:    source,
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Data: heapAndIndexSnapshot{
			HeapAndIndex: HeapAndIndex{
				Relation: Relation,
				Heap:     heap,
				Index:    index,
			},
			Queries: queries,
		},
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestHeapAndIndexSnapshotJSON -v`
Expected: PASS.

- [ ] **Step 5: Run the full package test suite and gofmt/vet checks**

Run: `cd inspector && gofmt -l . && go vet ./... && go test ./... -count=1`
Expected: clean, all pass.

- [ ] **Step 6: Commit**

```bash
git add inspector/internal/adapter/postgres/btreepage.go inspector/internal/adapter/postgres/runningqueries_test.go
git commit -m "$(cat <<'EOF'
Add running queries to the postgres.heap_and_index snapshot

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Grant, schema docs, and static samples

**Files:**
- Modify: `adapters/postgres/compose.yml`
- Modify: `schema/README.md`
- Modify: `samples/postgres.heap_page.json`
- Modify: `samples/postgres.heap_and_index.json`

**Interfaces:**
- Consumes: the `queries` field shape produced by Tasks 2 and 3 (`pid`, `usename`, `application_name`, `state`, `wait_event_type`, `wait_event`, `query_start`, `duration_ms`, `query`).
- Produces: nothing further code consumes; this task's deliverable is the grant the adapters' SQL depends on at runtime, plus docs/samples that describe the shape Task 5's view code will read.

- [ ] **Step 1: Add the grant to `compose.yml`**

Open `adapters/postgres/compose.yml` and find the existing grants block (near the `GRANT CONNECT ON DATABASE glasshouse TO glasshouse_inspector;` line). Add immediately after the existing grant lines for `glasshouse_inspector`:

```sql
GRANT pg_read_all_stats TO glasshouse_inspector;
```

- [ ] **Step 2: Update `schema/README.md`**

In the "Adapter-specific shapes" table, change the two existing rows to include `queries`:

```markdown
| `postgres.heap_page` | `{relation, pages: [{block, header, free_space, items}], queries: [{pid, usename, application_name, state, wait_event_type, wait_event, query_start, duration_ms, query}]}` | `inspector/internal/adapter/postgres/heappage.go` |
| `postgres.heap_and_index` | `{relation, heap: <postgres.heap_page's data, minus queries>, index: {index_name, pages: [{block, level, type, items}], truncated}, queries: [{pid, usename, application_name, state, wait_event_type, wait_event, query_start, duration_ms, query}]}` | `inspector/internal/adapter/postgres/btreepage.go` |
```

Below the table, add a short paragraph:

```markdown
`queries` is a live, point-in-time read of `pg_stat_activity` for the
target database, excluding the Inspector's own backend and idle
connections, capped at 20 rows. `query_start` is RFC3339, or `""` if NULL.
`wait_event_type`/`wait_event` are `""` when the backend is not waiting on
anything (e.g. actively using CPU).
```

- [ ] **Step 3: Update `samples/postgres.heap_page.json`**

Open the file and add a `"queries"` key as a sibling of `"relation"`/`"pages"` inside `snapshot.data` (after the closing `]` of `"pages"`):

```json
      "queries": [
        {
          "pid": 412,
          "usename": "glasshouse_inspector",
          "application_name": "glasshouse-demo-action",
          "state": "active",
          "wait_event_type": "",
          "wait_event": "",
          "query_start": "2026-10-03T19:23:41.000000000Z",
          "duration_ms": 12,
          "query": "INSERT INTO glasshouse_demo (payload) VALUES ($1)"
        }
      ]
```

- [ ] **Step 4: Update `samples/postgres.heap_and_index.json`**

Open the file and add the same `"queries"` key as a sibling of `"relation"`/`"heap"`/`"index"` inside `snapshot.data`, using a row that also exercises a non-empty wait event for variety:

```json
      "queries": [
        {
          "pid": 418,
          "usename": "glasshouse_inspector",
          "application_name": "glasshouse-demo-action",
          "state": "active",
          "wait_event_type": "Lock",
          "wait_event": "tuple",
          "query_start": "2026-10-08T22:14:30.500000000Z",
          "duration_ms": 438,
          "query": "UPDATE glasshouse_demo SET payload = $1 WHERE ctid = $2"
        }
      ]
```

- [ ] **Step 5: Validate both samples are well-formed JSON**

Run: `python3 -m json.tool samples/postgres.heap_page.json > /dev/null && python3 -m json.tool samples/postgres.heap_and_index.json > /dev/null && echo OK`
Expected: `OK` with no errors.

- [ ] **Step 6: Run `make test`**

Run: `make test` (add `USE_DOCKER=1` if Go is not installed on the host)
Expected: passes — this task touches no Go/JS source, only data/docs/compose, so this just confirms nothing else broke.

- [ ] **Step 7: Commit**

```bash
git add adapters/postgres/compose.yml schema/README.md samples/postgres.heap_page.json samples/postgres.heap_and_index.json
git commit -m "$(cat <<'EOF'
Grant pg_read_all_stats and document the queries field on both snapshot types

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Viewer: render running queries in both views

**Files:**
- Create: `inspector/web/static/views/running-queries.js`
- Modify: `inspector/web/static/views/postgres-heap-page.js`
- Modify: `inspector/web/static/views/postgres-heap-and-index.js`
- Modify: `inspector/web/static/shell.css`

**Interfaces:**
- Consumes: `snapshot.data.queries` (an array of the shape documented in Task 4) from both `postgres.heap_page` and `postgres.heap_and_index` views.
- Produces: `export function renderRunningQueries(container, queries)` — appends a table into `container` (does not clear `container`'s existing children, so callers control placement by passing a fresh child element, matching how `renderHeapSection` is used inside `postgres-heap-and-index.js`).

- [ ] **Step 1: Create `running-queries.js`**

```javascript
// Shared "running queries" section, rendered by both postgres.heap_page
// and postgres.heap_and_index views from their snapshot's queries array.
// Not registered with registry.js itself: it has no snapshot.type of its
// own, since it is a sibling section on two existing types, not a new one.

const MAX_QUERY_TEXT = 140;

function truncate(text) {
  if (!text) return '';
  return text.length > MAX_QUERY_TEXT ? `${text.slice(0, MAX_QUERY_TEXT)}…` : text;
}

function cell(tag, text) {
  const el = document.createElement(tag);
  el.textContent = text;
  return el;
}

// renderRunningQueries appends a running-queries table into container. It
// does not clear container's existing children; callers pass a dedicated
// child element so placement within a larger view stays under their
// control (see postgres-heap-page.js and postgres-heap-and-index.js).
export function renderRunningQueries(container, queries) {
  const heading = document.createElement('h3');
  heading.style.margin = '20px 0 8px';
  heading.textContent = `Running queries (${queries.length})`;
  container.append(heading);

  if (queries.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'small muted';
    empty.textContent = 'No active backends right now.';
    container.append(empty);
    return;
  }

  const table = document.createElement('table');
  table.className = 'queries';

  const head = document.createElement('tr');
  for (const label of ['PID', 'State', 'Wait event', 'Duration', 'Query']) {
    head.append(cell('th', label));
  }
  table.append(head);

  for (const q of queries) {
    const row = document.createElement('tr');
    const waitEvent = q.wait_event_type ? `${q.wait_event_type}: ${q.wait_event}` : '';
    row.append(
      cell('td', String(q.pid)),
      cell('td', q.state),
      cell('td', waitEvent),
      cell('td', `${q.duration_ms} ms`),
      cell('td', truncate(q.query)),
    );
    table.append(row);
  }

  container.append(table);
}
```

- [ ] **Step 2: Add the `table.queries` style to `shell.css`**

Open `inspector/web/static/shell.css` and add, after the existing `table.kv` rule block:

```css
table.queries { border-collapse: collapse; width: 100%; font: 12px/1.5 var(--mono); }
table.queries th, table.queries td {
  text-align: left;
  padding: 3px 8px 3px 0;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
  word-break: break-word;
}
table.queries th { color: var(--muted); font-weight: 500; }
```

- [ ] **Step 3: Wire into `postgres-heap-page.js`**

Add the import near the top of `inspector/web/static/views/postgres-heap-page.js`:

```javascript
import { renderRunningQueries } from './running-queries.js';
```

In `renderHeapSection`, find the final line `wrap.append(summary, grid, legend());` and change it to also append the queries section:

```javascript
  wrap.append(summary, grid, legend());
  renderRunningQueries(wrap, data.queries);
  container.replaceChildren(wrap);
}
```

(This replaces the two lines `wrap.append(summary, grid, legend());` and the following `container.replaceChildren(wrap);` with the three lines above — `renderHeapSection` already ends with exactly those two lines today.)

- [ ] **Step 4: Wire into `postgres-heap-and-index.js`**

Add the import near the top of `inspector/web/static/views/postgres-heap-and-index.js`:

```javascript
import { renderRunningQueries } from './running-queries.js';
```

In the `registerView('postgres.heap_and_index', ...)` call, `renderHeapSection(heapSection, data.heap, ...)` is called on a **separate** `heapSection` element — `data.heap` does not carry its own `queries` (Task 2/3 keep `Queries` off the embedded `HeapPages`), so `renderRunningQueries` must be called here with the top-level `data.queries`, not inside `heapSection`. Change the `render` function's body from:

```javascript
    wrap.append(heapSection, indexHeading, treeDiagram(data.index));
    container.replaceChildren(wrap);
```

to:

```javascript
    wrap.append(heapSection, indexHeading, treeDiagram(data.index));
    renderRunningQueries(wrap, data.queries);
    container.replaceChildren(wrap);
```

- [ ] **Step 5: Manual verification**

There is no JS test runner in this repo (plain ES modules, no build step) and `renderHeapSection`'s own existing tests are manual, so verify the same way:

Run: `make run-mock` (or, with Go unavailable, `USE_DOCKER=1 make build` then run the binary) is not sufficient here since the mock adapter doesn't emit `queries` — use the real Postgres lab:

```bash
make stack-up
```

Open `http://127.0.0.1:8765/` in a browser. Confirm:
1. A "Running queries" heading appears below the heap byte map, with a table of active backends (or "No active backends right now." if the database is quiet).
2. Trigger an action from the article/panel (e.g. `insert_rows`) and confirm a row appears showing that backend while it runs, including a non-empty `query` cell truncated with `…` if long.
3. Confirm no `wait_event_type`/`wait_event` values render as the literal text `null` or `undefined`.

Then switch labs:

```bash
make stack-down
GLASSHOUSE_ADAPTER=postgres-index make stack-up
```

Reload the browser and confirm the same running-queries table appears below the index tree diagram in the heap+index view, and the two sections' table layout/styling match.

Run `make stack-down` when done.

- [ ] **Step 6: Run `make test`**

Run: `make test` (add `USE_DOCKER=1` if Go is not installed on the host)
Expected: passes (this task is JS/CSS-only; confirms the Go build and site build are unaffected).

- [ ] **Step 7: Commit**

```bash
git add inspector/web/static/views/running-queries.js inspector/web/static/views/postgres-heap-page.js inspector/web/static/views/postgres-heap-and-index.js inspector/web/static/shell.css
git commit -m "$(cat <<'EOF'
Render the running-queries section in both Postgres views

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Final check

After Task 5, run `make test` once more at the repo root and confirm all Go, site, and fmt/vet checks pass, then review `git log --oneline -6` to confirm five focused commits (one per task) landed on top of the spec commit.
