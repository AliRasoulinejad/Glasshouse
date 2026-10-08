# Postgres index page view: design

Date: 2026-10-08
Status: draft for review

## Purpose

Glasshouse shows PostgreSQL's heap page layout from a real running instance.
This adds the matching view for a B-tree index: a second article and lab
panel that shows the index's real on-disk pages (via `pageinspect`) stacked
below the existing heap page view, so a reader can watch the same
`insert_rows`/`update_rows`/`delete_rows`/`vacuum_full` actions change both
at once.

Success: a reader of the new article sees real index pages — root, internal,
and leaf — read from their own running Postgres, updating in step with the
heap page above it, with the same "no simulations" guarantee as the existing
heap-page article.

## Scope

In scope:

- A new combined snapshot type, `postgres.heap_and_index`, carrying both the
  heap pages and the index pages for `glasshouse_demo` in one payload.
- A new adapter variant that reads both, reusing the existing heap-reading
  code.
- A breadth-first, capped read of the index's root/internal/leaf pages.
- A new view component rendering heap (top) and index tree (bottom) from one
  snapshot.
- A new article, `site/articles/postgres-index-page.md`, with its own lab
  start command, sharing `adapters/postgres/compose.yml` via a mode switch.
- A new static sample, `samples/postgres.heap_and_index.json`.

Out of scope (unchanged):

- The existing `postgres.heap_page` article, adapter, and view — untouched.
- Any index other than the one on `glasshouse_demo.payload`.
- Replication adapter, other targets (Redis, Mongo, MinIO) — still deferred.
- Custom reader-supplied actions, SQL, or table choice.

## Decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Indexed column | `payload`, not `id` | `insert_rows` never sets `id` (stays NULL), so an `id` index would never fill or split. `payload` is always populated. |
| Snapshot shape | One new combined type (`postgres.heap_and_index`), additive | Keeps the existing `postgres.heap_page` type and article untouched; `SchemaVersion` stays 1. |
| Adapter shape | `postgres.NewWithIndex` wraps the existing `Adapter`, overrides `Snapshot`/`StreamEvents`, reuses `Actions()` unchanged | Avoids duplicating the heap-reading and action code; the four actions already touch the demo table, and now visibly affect the index too. |
| Index page depth | Full tree shape (root + internal + leaf), breadth-first from the metapage, capped at `maxIndexPages` | Reader asked to see the whole tree shape, not just leaves. The cap keeps a degenerate case cheap to draw; a `truncated` flag tells the view when pages were cut off. |
| Lab layout | One `compose.yml`, mode switch via `GLASSHOUSE_ADAPTER` env var (default `postgres`) | The index and wrapper functions are harmless to create unconditionally; one file stays the single source of truth for both labs instead of two near-duplicates drifting apart. |

## Components

### 1. `inspector/internal/adapter/postgres/btreepage.go` (new)

```go
type IndexItem struct {
    ItemOffset int    `json:"itemoffset"`
    CTID       string `json:"ctid"`       // downlink block (internal) or heap tid (leaf)
    DataHex    string `json:"data_hex"`   // truncated key prefix
    Dead       bool   `json:"dead"`       // leaf items only
}

type IndexPage struct {
    Block int         `json:"block"`
    Level int         `json:"level"` // 0 = leaf
    Type  string      `json:"type"`  // "root" | "internal" | "leaf"
    Items []IndexItem `json:"items"`
}

type IndexPages struct {
    IndexName string      `json:"index_name"`
    Pages     []IndexPage `json:"pages"`     // breadth-first, root first
    Truncated bool        `json:"truncated"` // true if the cap cut off pages
}

const maxIndexPages = 12
```

- `readIndexPages(ctx, pool)`:
  1. Read the metapage (`glasshouse_btree_metap()`) for the root block and
     fast-root level.
  2. BFS from the root: read each page's items via
     `glasshouse_btree_page_items(blk)`; for internal pages, each item's
     `ctid` is the next level's block number to visit.
  3. Stop enqueuing new pages once the next page would exceed
     `maxIndexPages`; set `Truncated = true` if any were skipped.
- `HeapAndIndex` struct: `{Relation string; Heap HeapPages; Index IndexPages}`.
- `WithIndexAdapter` type: embeds `*Adapter` (the existing heap adapter),
  overrides:
  - `Snapshot(ctx)`: calls the embedded adapter's page-read helper for heap,
    calls `readIndexPages`, returns `adapter.Snapshot{Type: "postgres.heap_and_index", Data: HeapAndIndex{...}}`.
  - `StreamEvents(ctx)`: same polling loop and heap-tuple diff events as the
    existing adapter. No new index-specific event kind for v1 — the index
    section updates on every poll along with the heap, same as the snapshot
    path, but only heap changes are reported as discrete events. This can be
    revisited later if a reader-facing need for index-specific events shows up.
  - `Actions()`: delegates to the embedded adapter unchanged.
- `New WithIndex(interval time.Duration) *WithIndexAdapter` constructor,
  mirroring `postgres.New`.

### 2. SQL additions (`adapters/postgres/compose.yml` init script)

Added unconditionally, alongside the existing table and heap wrappers:

```sql
CREATE INDEX glasshouse_demo_payload_idx ON glasshouse_demo (payload);

CREATE FUNCTION glasshouse_btree_metap()
RETURNS TABLE (root int, level int, fastroot int, fastlevel int)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public
AS $$f$$
  SELECT m.root::int, m.level::int, m.fastroot::int, m.fastlevel::int
  FROM bt_metap('glasshouse_demo_payload_idx') AS m
$$f$$;

CREATE FUNCTION glasshouse_btree_page_items(blk int)
RETURNS TABLE (itemoffset int, ctid text, dead bool, data_hex text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public
AS $$f$$
  SELECT i.itemoffset::int, i.ctid::text, COALESCE(i.dead, false),
         COALESCE(encode(substring(i.data from 1 for 16), 'hex'), '')
  FROM bt_page_items('glasshouse_demo_payload_idx', blk) AS i
$$f$$;

REVOKE ALL ON FUNCTION glasshouse_btree_metap() FROM PUBLIC;
REVOKE ALL ON FUNCTION glasshouse_btree_page_items(int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION glasshouse_btree_metap() TO glasshouse_inspector;
GRANT EXECUTE ON FUNCTION glasshouse_btree_page_items(int) TO glasshouse_inspector;
```

Same pattern as the existing heap wrappers: `pageinspect`'s raw functions
refuse non-superusers, so these are `SECURITY DEFINER`, owned by the admin,
hard-wired to the one fixed index, taking only a block number.

The `inspector` service's command gains a mode switch:

```yaml
command:
  - -addr
  - 0.0.0.0:8765
  - -adapter
  - ${GLASSHOUSE_ADAPTER:-postgres}
  - -origin
  - ${GLASSHOUSE_ALLOWED_ORIGIN:-https://alirasoulinejad.github.io}
```

### 3. `inspector/cmd/inspector/main.go`

`buildAdapter` gains a `"postgres-index"` case constructing
`postgres.NewWithIndex(interval)` with the same DSN wiring as `"postgres"`.

### 4. `inspector/web/static/views/`

- `postgres-heap-page.js`: factor its page-rendering function out so it can
  be imported rather than duplicated (e.g. export `renderHeapPages(container, data)`).
- `postgres-heap-and-index.js` (new): renders two stacked sections from one
  `postgres.heap_and_index` snapshot — the heap grid (via the exported
  function above) on top, and a new tree diagram below: one row per level,
  each page drawn as a box of key-prefix chips (`DataHex`, truncated further
  for display), downlink arrows from internal items to their child block,
  sibling arrows between pages at the same level. All text via `textContent`.
  If `Truncated`, a trailing note says more pages exist.
- `registry.js`: map `"postgres.heap_and_index"` to the new view.

### 5. `site/articles/postgres-index-page.md` (new)

- Front matter: `actions: [insert_rows, update_rows, delete_rows, vacuum_full]`
  (same four, same meaning as the heap article).
- Explains why the index is on `payload`, not `id`.
- Start command sets `GLASSHOUSE_ADAPTER=postgres-index` in the same
  `curl | docker compose -f -` invocation used by the heap article, pointing
  at the same `compose.yml`.
- Panel embeds the viewer the same way the heap article does.
- Notes in the article (and in `CLAUDE.md`'s gotchas, once implemented) that
  only one lab runs on `:8765` at a time — stop the heap lab before starting
  this one, same as today's `make run-mock`/`make run-postgres` rule.

### 6. `schema/README.md`

New section documenting `postgres.heap_and_index`'s `data` shape
(`relation`, `heap`, `index`), alongside the existing `postgres.heap_page`
section. `SchemaVersion` stays 1 — this is an additive new type, not a
change to an existing one.

### 7. `samples/postgres.heap_and_index.json` (new)

Static sample in the same envelope shape as `samples/postgres.heap_page.json`,
for the Website to read with zero setup.

## Data flow

1. Reader opens the new article; runs its start command
   (`GLASSHOUSE_ADAPTER=postgres-index`) in their terminal.
2. Postgres starts with the demo table, its payload index, and both pairs of
   wrapper functions. The Inspector starts with `postgres.NewWithIndex`.
3. The panel's viewer calls `GET /snapshot`, gets `postgres.heap_and_index`,
   renders heap-on-top/index-below.
4. An action button (`insert_rows` etc.) posts to `/actions/<name>`; the next
   poll shows the change in both sections.

## Error handling

Unchanged from the heap page adapter: a failed read (bad connection, missing
index) returns an error from `Snapshot()`, which the server already turns
into a 502 with a generic message; the viewer already renders that with
`textContent`. No new error paths.

## Testing

- `inspector/internal/adapter/postgres`: table-driven tests for the BFS walk
  against synthetic page fixtures — leaf-only tree (root is a leaf), 2-level
  tree (root + leaves), and a tree wide enough to hit `maxIndexPages` 's
  truncation.
- `inspector/internal/server`: a test confirming `-adapter postgres-index`
  builds and serves `postgres.heap_and_index` (mirroring the existing
  adapter-selection tests).
- `make test` (gofmt check, vet, tests); `USE_DOCKER=1` on this host.
- Manual: `make stack-up` with `GLASSHOUSE_ADAPTER=postgres-index`, run each
  action, confirm both sections update; `site/articles/postgres-index-page.md`
  renders via `make site-build`.

## Security

No change to the existing rules: the new wrapper functions are
`SECURITY DEFINER`, hard-wired to one fixed index, take only a block number,
and are revoked from `PUBLIC`. No new routes, no new input from the browser.
