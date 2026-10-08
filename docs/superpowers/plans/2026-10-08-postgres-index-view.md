# Postgres Index Page View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a real B-tree index page view, read live from PostgreSQL via `pageinspect`, shown stacked below the existing heap page view in a new article's lab panel.

**Architecture:** A new combined snapshot type (`postgres.heap_and_index`) carries both the existing heap pages and a breadth-first, capped read of the index's root/internal/leaf pages. A new `WithIndexAdapter` wraps the existing heap `Adapter` by embedding it, overriding only `Snapshot`; `StreamEvents`, `Connect`, and `Actions` are reused unchanged via Go's method promotion. One compose file serves both labs via a `GLASSHOUSE_ADAPTER` mode switch.

**Tech Stack:** Go 1.24 (`pgx/v5`), vanilla ES modules (no build step), PostgreSQL 17 `pageinspect`, Docker Compose.

**Spec:** `docs/superpowers/specs/2026-10-08-postgres-index-view-design.md`

## Global Constraints

- Host binary binds `127.0.0.1`/`localhost` only; no override flag (`server.ValidateAddr`, unchanged).
- Every SQL statement touching the database is a compile-time constant; the only bound parameter is an integer block number. No browser input ever reaches SQL.
- `SchemaVersion` stays `1`. This is an additive new snapshot type, not a change to `postgres.heap_page`, which stays untouched (adapter, view, and article).
- The viewer writes all API-derived text with `textContent`, never `innerHTML`.
- `WithIndexAdapter.Actions()` is the existing heap adapter's four actions, reused verbatim — no new browser-triggerable operation.
- Only one lab runs on `:8765` at a time; the index lab and the heap lab share one compose file via `GLASSHOUSE_ADAPTER`, never run together.

## Review Focus

- A single-level tree (root page is also the leaf): the root must be labeled `"root"`, not `"leaf"` or `"internal"`, and the walk must not try to parse its items as downlinks. (Task 1 test.)
- A page whose children would exceed the page cap: `Truncated` must become `true` and the walk must stop cleanly, not panic or silently drop pages without flagging it. (Task 1 test.)
- Exactly filling the cap (no pages left over): `Truncated` must stay `false` — the boundary must not be off by one in either direction. (Task 1 test.)
- A malformed or unexpected `ctid` on an internal page item: parsing must return a clear error, not panic or silently treat garbage as a block number. (Task 1 test.)
- `insert_rows` never sets `id` (stays NULL on every inserted row) — the index must be built on `payload`, which every row populates, or the tree would never fill. (Task 3: verified by inspecting the init script and by the end-to-end task's manual run.)

---

## Task 1: B-tree page walk — pure logic and types

**Files:**
- Create: `inspector/internal/adapter/postgres/btreepage.go`
- Test: `inspector/internal/adapter/postgres/btreepage_test.go`

**Interfaces:**
- Produces: `IndexItem{ItemOffset int, CTID string, DataHex string, Dead bool}`, `IndexPage{Block int, Level int, Type string, Items []IndexItem}`, `IndexPages{IndexName string, Pages []IndexPage, Truncated bool}`, `const maxIndexPages = 12`, `const IndexName = "glasshouse_demo_payload_idx"`, and the pure function `walkIndex(ctx context.Context, indexName string, readMeta func(context.Context) (root, level int, err error), readPage func(context.Context, int) ([]IndexItem, error)) (IndexPages, error)`. Task 2 calls `walkIndex` with real `pgxpool`-backed closures.

- [ ] **Step 1: Write the failing tests**

```go
package postgres

import (
	"context"
	"errors"
	"testing"
)

func metaFunc(root, level int) func(context.Context) (int, int, error) {
	return func(context.Context) (int, int, error) { return root, level, nil }
}

func pageFunc(pages map[int][]IndexItem) func(context.Context, int) ([]IndexItem, error) {
	return func(_ context.Context, blk int) ([]IndexItem, error) {
		items, ok := pages[blk]
		if !ok {
			return nil, errors.New("no such page")
		}
		return items, nil
	}
}

func TestWalkIndexSingleLevelTreeRootIsLeaf(t *testing.T) {
	leafItems := []IndexItem{
		{ItemOffset: 1, CTID: "(0,1)", DataHex: "aa", Dead: false},
		{ItemOffset: 2, CTID: "(0,2)", DataHex: "bb", Dead: true},
	}
	got, err := walkIndex(context.Background(), "idx",
		metaFunc(1, 0),
		pageFunc(map[int][]IndexItem{1: leafItems}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("want 1 page, got %d", len(got.Pages))
	}
	if got.Pages[0].Type != "root" {
		t.Errorf("want type %q, got %q", "root", got.Pages[0].Type)
	}
	if got.Pages[0].Level != 0 {
		t.Errorf("want level 0, got %d", got.Pages[0].Level)
	}
	if got.Truncated {
		t.Error("want not truncated")
	}
}

func TestWalkIndexTwoLevelTreeDescendsToLeaves(t *testing.T) {
	root := []IndexItem{
		{ItemOffset: 1, CTID: "(2,0)", DataHex: "aa"},
		{ItemOffset: 2, CTID: "(3,0)", DataHex: "bb"},
	}
	leaf2 := []IndexItem{{ItemOffset: 1, CTID: "(0,1)", DataHex: "a1"}}
	leaf3 := []IndexItem{{ItemOffset: 1, CTID: "(0,5)", DataHex: "b1"}}

	got, err := walkIndex(context.Background(), "idx",
		metaFunc(1, 1),
		pageFunc(map[int][]IndexItem{1: root, 2: leaf2, 3: leaf3}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != 3 {
		t.Fatalf("want 3 pages, got %d", len(got.Pages))
	}
	if got.Pages[0].Block != 1 || got.Pages[0].Type != "root" {
		t.Errorf("page 0: want root block 1, got %+v", got.Pages[0])
	}
	if got.Pages[1].Block != 2 || got.Pages[1].Type != "leaf" || got.Pages[1].Level != 0 {
		t.Errorf("page 1: want leaf block 2 level 0, got %+v", got.Pages[1])
	}
	if got.Pages[2].Block != 3 || got.Pages[2].Type != "leaf" {
		t.Errorf("page 2: want leaf block 3, got %+v", got.Pages[2])
	}
	if got.Truncated {
		t.Error("want not truncated")
	}
}

func TestWalkIndexTruncatesWhenChildrenExceedCap(t *testing.T) {
	// Root has 15 children (one more than fits once the root itself is
	// counted against the 12-page cap: 1 root + 11 leaves = 12).
	root := make([]IndexItem, 15)
	pages := map[int][]IndexItem{}
	for i := range root {
		child := i + 2
		root[i] = IndexItem{ItemOffset: i + 1, CTID: "(" + itoa(child) + ",0)"}
		pages[child] = []IndexItem{{ItemOffset: 1, CTID: "(0,1)"}}
	}
	pages[1] = root

	got, err := walkIndex(context.Background(), "idx", metaFunc(1, 1), pageFunc(pages))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != maxIndexPages {
		t.Fatalf("want %d pages, got %d", maxIndexPages, len(got.Pages))
	}
	if !got.Truncated {
		t.Error("want truncated")
	}
}

func TestWalkIndexExactlyFillingCapIsNotTruncated(t *testing.T) {
	// Root has 11 children: 1 root + 11 leaves = 12, exactly the cap.
	root := make([]IndexItem, 11)
	pages := map[int][]IndexItem{}
	for i := range root {
		child := i + 2
		root[i] = IndexItem{ItemOffset: i + 1, CTID: "(" + itoa(child) + ",0)"}
		pages[child] = []IndexItem{{ItemOffset: 1, CTID: "(0,1)"}}
	}
	pages[1] = root

	got, err := walkIndex(context.Background(), "idx", metaFunc(1, 1), pageFunc(pages))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != maxIndexPages {
		t.Fatalf("want %d pages, got %d", maxIndexPages, len(got.Pages))
	}
	if got.Truncated {
		t.Error("want not truncated: cap exactly filled")
	}
}

func TestWalkIndexRejectsMalformedCTID(t *testing.T) {
	root := []IndexItem{{ItemOffset: 1, CTID: "not-a-ctid"}}
	_, err := walkIndex(context.Background(), "idx",
		metaFunc(1, 1),
		pageFunc(map[int][]IndexItem{1: root}))
	if err == nil {
		t.Fatal("want error for malformed ctid")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestWalkIndex -v`
Expected: FAIL — `walkIndex`, `IndexItem`, `IndexPage`, `IndexPages`, `maxIndexPages` are undefined.

- [ ] **Step 3: Implement the walk**

```go
// btreepage.go additions, in package postgres (adapter.go's doc comment
// already covers the package; this file only needs its own).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// IndexItem is one entry on a B-tree index page. On an internal or root page,
// CTID encodes the downlink to a child block as "(block,0)"; on a leaf page
// it is the real heap tuple pointer the index entry points to.
type IndexItem struct {
	ItemOffset int    `json:"itemoffset"`
	CTID       string `json:"ctid"`
	DataHex    string `json:"data_hex"`
	Dead       bool   `json:"dead"`
}

// IndexPage is one block of the index, with its level (0 = leaf) and its
// role: "root", "internal", or "leaf".
type IndexPage struct {
	Block int         `json:"block"`
	Level int         `json:"level"`
	Type  string       `json:"type"`
	Items []IndexItem `json:"items"`
}

// IndexPages is the breadth-first, capped read of one index's tree shape,
// root first.
type IndexPages struct {
	IndexName string      `json:"index_name"`
	Pages     []IndexPage `json:"pages"`
	Truncated bool        `json:"truncated"`
}

// maxIndexPages caps how many index pages one snapshot reads, so a
// degenerate or large tree stays cheap to read and draw.
const maxIndexPages = 12

// IndexName is the one fixed index the adapter inspects. Indexing payload
// (not id) matters: insert_rows never sets id, so an id index would never
// fill or split.
const IndexName = "glasshouse_demo_payload_idx"

// pageType labels a page by its level relative to the root.
func pageType(level, rootLevel int) string {
	switch {
	case level == rootLevel:
		return "root"
	case level == 0:
		return "leaf"
	default:
		return "internal"
	}
}

// downlinkBlock parses the child block number out of an internal page
// item's ctid, formatted by pageinspect as "(block,offset)". The offset
// part is unused for a downlink.
func downlinkBlock(ctid string) (int, error) {
	s := strings.Trim(ctid, "()")
	block, _, ok := strings.Cut(s, ",")
	if !ok {
		return 0, fmt.Errorf("postgres: malformed index ctid %q", ctid)
	}
	n, err := strconv.Atoi(block)
	if err != nil {
		return 0, fmt.Errorf("postgres: malformed index ctid %q: %w", ctid, err)
	}
	return n, nil
}

type queuedIndexPage struct {
	block int
	level int
}

// walkIndex reads the index breadth-first from the root, following internal
// pages' downlinks to their children, capped at maxIndexPages total pages.
// readMeta and readPage are injected so the walk itself can be tested
// without a database.
func walkIndex(
	ctx context.Context,
	indexName string,
	readMeta func(context.Context) (root, level int, err error),
	readPage func(context.Context, int) ([]IndexItem, error),
) (IndexPages, error) {
	root, rootLevel, err := readMeta(ctx)
	if err != nil {
		return IndexPages{}, err
	}

	queue := []queuedIndexPage{{block: root, level: rootLevel}}
	pages := make([]IndexPage, 0, maxIndexPages)
	truncated := false

	for len(queue) > 0 {
		if len(pages) >= maxIndexPages {
			truncated = true
			break
		}
		cur := queue[0]
		queue = queue[1:]

		items, err := readPage(ctx, cur.block)
		if err != nil {
			return IndexPages{}, err
		}
		pages = append(pages, IndexPage{
			Block: cur.block,
			Level: cur.level,
			Type:  pageType(cur.level, rootLevel),
			Items: items,
		})

		if cur.level == 0 {
			continue
		}
		for _, it := range items {
			child, err := downlinkBlock(it.CTID)
			if err != nil {
				return IndexPages{}, err
			}
			if len(pages)+len(queue) >= maxIndexPages {
				truncated = true
				break
			}
			queue = append(queue, queuedIndexPage{block: child, level: cur.level - 1})
		}
	}
	if len(queue) > 0 {
		truncated = true
	}

	return IndexPages{IndexName: indexName, Pages: pages, Truncated: truncated}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd inspector && go test ./internal/adapter/postgres/... -run TestWalkIndex -v`
Expected: PASS for all five tests.

- [ ] **Step 5: Format and commit**

```bash
cd inspector && gofmt -w internal/adapter/postgres/btreepage.go internal/adapter/postgres/btreepage_test.go
cd .. && git add inspector/internal/adapter/postgres/btreepage.go inspector/internal/adapter/postgres/btreepage_test.go
git commit -m "$(cat <<'EOF'
Add the breadth-first B-tree page walk for the index view

Pure logic only, injected readers so the walk is testable without a
database. Wiring to real pageinspect queries comes next.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Wire the walk to Postgres, and the combined adapter

**Files:**
- Modify: `inspector/internal/adapter/postgres/btreepage.go` (add DB-backed reader, `WithIndexAdapter`, `NewWithIndex`, `HeapAndIndex`, `TypeHeapAndIndex`)
- Modify: `inspector/cmd/inspector/main.go` (`buildAdapter`, `listActions`, `-adapter` flag usage string)
- Modify: `inspector/cmd/inspector/main_test.go` (parity test)
- Modify: `schema/README.md` (document the new snapshot shape)

**Interfaces:**
- Consumes: `walkIndex`, `IndexItem`, `IndexPages`, `IndexName` from Task 1; `Adapter`, `HeapPages`, `Relation`, `New` from `heappage.go` (existing).
- Produces: `TypeHeapAndIndex = "postgres.heap_and_index"`, `HeapAndIndex{Relation string, Heap HeapPages, Index IndexPages}`, `NewWithIndex(interval time.Duration) *WithIndexAdapter`. `main.go`'s `-adapter postgres-index` case, consumed by Task 5's article and Task 6's manual run.

- [ ] **Step 1: Add the DB-backed reader and the combined adapter**

```go
// btreepage.go additions.

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"glasshouse/inspector/internal/adapter"
)

// TypeHeapAndIndex is the snapshot type emitted by WithIndexAdapter.
const TypeHeapAndIndex = "postgres.heap_and_index"

// HeapAndIndex is the snapshot payload: the demo table's heap pages and its
// payload index's pages, read in the same poll.
type HeapAndIndex struct {
	Relation string     `json:"relation"`
	Heap     HeapPages  `json:"heap"`
	Index    IndexPages `json:"index"`
}

// readIndexPages reads IndexName's current tree shape from pool, through
// the two SECURITY DEFINER wrappers the init script installs.
func readIndexPages(ctx context.Context, pool *pgxpool.Pool) (IndexPages, error) {
	readMeta := func(ctx context.Context) (int, int, error) {
		var root, level, fastroot, fastlevel int
		err := pool.QueryRow(ctx,
			`SELECT root, level, fastroot, fastlevel FROM glasshouse_btree_metap()`,
		).Scan(&root, &level, &fastroot, &fastlevel)
		if err != nil {
			return 0, 0, fmt.Errorf("postgres: btree_metap: %w", err)
		}
		return root, level, nil
	}

	readPage := func(ctx context.Context, blk int) ([]IndexItem, error) {
		rows, err := pool.Query(ctx,
			`SELECT itemoffset, ctid, dead, data_hex
			 FROM glasshouse_btree_page_items($1::int4)
			 ORDER BY itemoffset`,
			blk,
		)
		if err != nil {
			return nil, fmt.Errorf("postgres: btree_page_items: %w", err)
		}
		defer rows.Close()
		var items []IndexItem
		for rows.Next() {
			var it IndexItem
			if err := rows.Scan(&it.ItemOffset, &it.CTID, &it.Dead, &it.DataHex); err != nil {
				return nil, fmt.Errorf("postgres: scan index item: %w", err)
			}
			items = append(items, it)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("postgres: index items: %w", err)
		}
		if items == nil {
			items = []IndexItem{}
		}
		return items, nil
	}

	return walkIndex(ctx, IndexName, readMeta, readPage)
}

// WithIndexAdapter reads the demo table's heap pages and its payload
// index's pages in the same poll. It embeds the heap Adapter and reuses its
// Connect, Close, StreamEvents (heap-tuple diff events only — the index
// section updates with every snapshot, but has no event kind of its own
// yet), and Actions unchanged; only Snapshot is overridden.
type WithIndexAdapter struct {
	*Adapter
}

// NewWithIndex returns an adapter that polls both the heap and the index
// every interval.
func NewWithIndex(interval time.Duration) *WithIndexAdapter {
	return &WithIndexAdapter{Adapter: New(interval)}
}

// Snapshot returns the current heap pages and index pages together.
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

	a.mu.Lock()
	seq := a.seq
	source := a.source
	a.mu.Unlock()

	return adapter.Snapshot{
		Type:      TypeHeapAndIndex,
		Source:    source,
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Data: HeapAndIndex{
			Relation: Relation,
			Heap:     heap,
			Index:    index,
		},
	}, nil
}
```

- [ ] **Step 2: Wire `-adapter postgres-index` into `main.go`**

In `inspector/cmd/inspector/main.go`, update the flag usage string and both switches:

```go
	adapterName := flag.String("adapter", "mock", "adapter to run: mock | postgres | postgres-index")
```

```go
func buildAdapter(name string) (adapter.Adapter, adapter.Target, error) {
	switch name {
	case "mock":
		return mock.New(time.Second), adapter.Target{Name: "mock"}, nil
	case "postgres":
		dsn := os.Getenv("GLASSHOUSE_PG_DSN")
		if dsn == "" {
			return nil, adapter.Target{}, errors.New("GLASSHOUSE_PG_DSN is not set")
		}
		return postgres.New(500 * time.Millisecond), adapter.Target{Name: "postgres", Endpoint: dsn}, nil
	case "postgres-index":
		dsn := os.Getenv("GLASSHOUSE_PG_DSN")
		if dsn == "" {
			return nil, adapter.Target{}, errors.New("GLASSHOUSE_PG_DSN is not set")
		}
		return postgres.NewWithIndex(500 * time.Millisecond), adapter.Target{Name: "postgres-index", Endpoint: dsn}, nil
	default:
		return nil, adapter.Target{}, fmt.Errorf("unknown adapter %q", name)
	}
}
```

```go
func listActions(name string, w io.Writer) error {
	var a adapter.Actioner
	switch name {
	case "mock":
		a = mock.New(time.Second)
	case "postgres":
		a = postgres.New(500 * time.Millisecond)
	case "postgres-index":
		a = postgres.NewWithIndex(500 * time.Millisecond)
	default:
		return fmt.Errorf("unknown adapter %q", name)
	}
	names := make([]string, 0)
	for n := range a.Actions() {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintln(w, n)
	}
	return nil
}
```

- [ ] **Step 3: Write the failing parity test**

```go
// main_test.go addition.
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
```

- [ ] **Step 4: Run it to verify it fails, then passes**

Run: `cd inspector && go test ./cmd/inspector/... -run TestListActionsPrintsPostgresIndexActions -v`
Expected first: FAIL (`unknown adapter "postgres-index"`), since `main.go` isn't edited yet in a TDD run — if doing this in order (edit `main.go` in Step 2 before the test in Step 3), run once to confirm PASS instead:
Expected: PASS.

Also run the full package to confirm nothing else broke:
Run: `cd inspector && go build ./... && go vet ./... && go test ./... -count=1`
Expected: all PASS.

- [ ] **Step 5: Document the new snapshot shape**

Append to `schema/README.md`, after "## Static samples":

```markdown
## Adapter-specific shapes

`snapshot.data`'s shape is fixed per `snapshot.type`, documented as Go types
next to each adapter — this table is just a map to them:

| Type | Shape | Defined in |
| --- | --- | --- |
| `postgres.heap_page` | `{relation, pages: [{block, header, free_space, items}]}` | `inspector/internal/adapter/postgres/heappage.go` |
| `postgres.heap_and_index` | `{relation, heap: <postgres.heap_page's data>, index: {index_name, pages: [{block, level, type, items}], truncated}}` | `inspector/internal/adapter/postgres/btreepage.go` |
```

- [ ] **Step 6: Format and commit**

```bash
cd inspector && gofmt -w internal/adapter/postgres/btreepage.go cmd/inspector/main.go cmd/inspector/main_test.go
cd .. && git add inspector/internal/adapter/postgres/btreepage.go inspector/cmd/inspector/main.go inspector/cmd/inspector/main_test.go schema/README.md
git commit -m "$(cat <<'EOF'
Wire the index walk to Postgres and add the postgres-index adapter

WithIndexAdapter embeds the existing heap Adapter and overrides only
Snapshot; StreamEvents and Actions are reused unchanged.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Compose file — index, wrapper functions, mode switch

**Files:**
- Modify: `adapters/postgres/compose.yml`
- Modify: `adapters/postgres/README.md`

**Interfaces:**
- Consumes: `-adapter postgres-index` from Task 2.
- Produces: the SQL objects `glasshouse_demo_payload_idx`, `glasshouse_btree_metap()`, `glasshouse_btree_page_items(int)`, and the `GLASSHOUSE_ADAPTER` compose variable, consumed by Task 6's manual verification and Task 5's article.

- [ ] **Step 1: Add the index and wrapper functions to the init script**

In `adapters/postgres/compose.yml`, inside the `glasshouse_init` config's heredoc, right after the existing `CREATE FUNCTION glasshouse_heap_page_items(...)` block and before the two `REVOKE ALL` lines, insert:

```sql
      CREATE INDEX glasshouse_demo_payload_idx ON glasshouse_demo (payload);

      -- Same privilege boundary as the heap wrappers above: pageinspect's
      -- raw functions refuse non-superusers, so these are SECURITY DEFINER,
      -- hard-wired to the one fixed index, taking only a block number.
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
```

Then change the existing two `REVOKE ALL` / `GRANT EXECUTE` lines to also cover the two new functions:

```sql
      REVOKE ALL ON FUNCTION glasshouse_page_header(int) FROM PUBLIC;
      REVOKE ALL ON FUNCTION glasshouse_heap_page_items(int) FROM PUBLIC;
      REVOKE ALL ON FUNCTION glasshouse_btree_metap() FROM PUBLIC;
      REVOKE ALL ON FUNCTION glasshouse_btree_page_items(int) FROM PUBLIC;
      GRANT EXECUTE ON FUNCTION glasshouse_page_header(int) TO glasshouse_inspector;
      GRANT EXECUTE ON FUNCTION glasshouse_heap_page_items(int) TO glasshouse_inspector;
      GRANT EXECUTE ON FUNCTION glasshouse_btree_metap() TO glasshouse_inspector;
      GRANT EXECUTE ON FUNCTION glasshouse_btree_page_items(int) TO glasshouse_inspector;
      SQL
```

- [ ] **Step 2: Add the adapter mode switch**

In the `inspector` service's `command:` block, change:

```yaml
    command:
      - -addr
      - 0.0.0.0:8765
      - -adapter
      - postgres
      - -origin
      - ${GLASSHOUSE_ALLOWED_ORIGIN:-https://alirasoulinejad.github.io}
```

to:

```yaml
    command:
      - -addr
      - 0.0.0.0:8765
      - -adapter
      - ${GLASSHOUSE_ADAPTER:-postgres}
      - -origin
      - ${GLASSHOUSE_ALLOWED_ORIGIN:-https://alirasoulinejad.github.io}
```

Also update the file's top comment block to mention the switch, right after the existing `GLASSHOUSE_ALLOWED_ORIGIN` paragraph:

```
# GLASSHOUSE_ADAPTER selects which article's lab this is: "postgres" (the
# heap-page article, the default) or "postgres-index" (the index-page
# article). Both read the same database; the index and its two wrapper
# functions are created either way, so switching is just restarting the
# Inspector with a different flag.
```

- [ ] **Step 3: Verify the compose file is still well-formed**

Run: `docker compose -f adapters/postgres/compose.yml config --quiet`
Expected: no output, exit code 0. (If Docker is unavailable in this environment, skip this step — Task 6's manual stack-up is the real verification.)

- [ ] **Step 4: Update the adapter README**

In `adapters/postgres/README.md`, after the existing "## How the privilege boundary works" section's bullet list (the two `glasshouse_*` wrapper names), add:

```markdown
The same pattern covers the index: `glasshouse_btree_metap()` and
`glasshouse_btree_page_items(blk int)`, both hard-wired to
`glasshouse_demo_payload_idx`.

## Running the index-page lab instead

The same compose file serves both articles. Set `GLASSHOUSE_ADAPTER` before
starting it:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.1.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.1.0:inspector \
  GLASSHOUSE_ADAPTER=postgres-index \
  docker compose -f - up -d --build --wait
```

Only one of the two labs runs on `:8765` at a time — stop one
(`docker compose ... down -v`) before starting the other.
```

- [ ] **Step 5: Commit**

```bash
git add adapters/postgres/compose.yml adapters/postgres/README.md
git commit -m "$(cat <<'EOF'
Add the payload index and a mode switch to the Postgres lab compose file

One compose file now serves both the heap-page and index-page articles,
selected by GLASSHOUSE_ADAPTER.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: View — heap+index panel

**Files:**
- Modify: `inspector/web/static/views/postgres-heap-page.js` (export the heap-rendering function)
- Create: `inspector/web/static/views/postgres-heap-and-index.js`
- Modify: `inspector/web/static/views/registry.js` (no code change needed — registration happens via `registerView` calls already wired through `app.js`'s imports; see Step 3)
- Modify: `inspector/web/static/app.js` (import the new view module)
- Modify: `inspector/web/static/shell.css` (index tree styles)

**Interfaces:**
- Consumes: nothing new from Go — reads `snapshot.data` shaped as `HeapAndIndex` (JSON: `{relation, heap: {relation, pages}, index: {index_name, pages, truncated}}`) from Task 2.
- Produces: `export function renderHeapSection(container, data, focusLP)` in `postgres-heap-page.js`, used by both the existing `postgres.heap_page` view and the new `postgres.heap_and_index` view.

- [ ] **Step 1: Factor the heap renderer out and export it**

In `inspector/web/static/views/postgres-heap-page.js`, replace the tail (`registerView('postgres.heap_page', {...})`, currently lines 217–270) with:

```js
export function renderHeapSection(container, data, focusLP) {
  const pages = data.pages;

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
  container.replaceChildren(wrap);
}

registerView('postgres.heap_page', {
  title: 'Heap page (byte map)',
  render(container, { snapshot, focus }) {
    renderHeapSection(container, snapshot.data, focus?.detail?.lp ?? null);
  },
});
```

This only moves code and adds the `export` + extracted signature — `buildMap`, `legend`, and `table` stay as they are earlier in the file.

- [ ] **Step 2: Add the combined view**

Create `inspector/web/static/views/postgres-heap-and-index.js`:

```js
import { registerView } from './registry.js';
import { renderHeapSection } from './postgres-heap-page.js';

function keyChip(item) {
  const chip = document.createElement('span');
  chip.className = 'index-chip' + (item.dead ? ' dead' : '');
  chip.textContent = item.data_hex ? item.data_hex.slice(0, 8) : '(none)';
  chip.title = `itemoffset=${item.itemoffset} ctid=${item.ctid}` +
    (item.dead ? ' (dead)' : '');
  return chip;
}

function pageBox(page) {
  const box = document.createElement('div');
  box.className = 'index-page';

  const heading = document.createElement('p');
  heading.className = 'small muted';
  heading.style.margin = '0 0 4px';
  heading.textContent = `Block ${page.block} · ${page.type} · ` +
    `level ${page.level} · ${page.items.length} items`;
  box.append(heading);

  const list = document.createElement('div');
  list.className = 'index-items';
  for (const item of page.items) {
    list.append(keyChip(item));
  }
  box.append(list);
  return box;
}

function treeDiagram(indexPages) {
  const wrap = document.createElement('div');
  wrap.className = 'index-tree';

  const byLevel = new Map();
  for (const page of indexPages.pages) {
    if (!byLevel.has(page.level)) byLevel.set(page.level, []);
    byLevel.get(page.level).push(page);
  }
  const levels = [...byLevel.keys()].sort((a, b) => b - a);

  for (const level of levels) {
    const row = document.createElement('div');
    row.className = 'index-level';
    for (const page of byLevel.get(level)) {
      row.append(pageBox(page));
    }
    wrap.append(row);
  }

  if (indexPages.truncated) {
    const note = document.createElement('p');
    note.className = 'small muted';
    note.textContent = 'More index pages exist beyond what is shown here.';
    wrap.append(note);
  }
  return wrap;
}

registerView('postgres.heap_and_index', {
  title: 'Heap page + B-tree index',
  render(container, { snapshot, focus }) {
    const data = snapshot.data;

    const wrap = document.createElement('div');
    wrap.className = 'heap-and-index';

    const heapSection = document.createElement('div');
    renderHeapSection(heapSection, data.heap, focus?.detail?.lp ?? null);

    const indexHeading = document.createElement('h3');
    indexHeading.style.margin = '20px 0 8px';
    indexHeading.textContent = `Index: ${data.index.index_name}`;

    wrap.append(heapSection, indexHeading, treeDiagram(data.index));
    container.replaceChildren(wrap);
  },
});
```

- [ ] **Step 3: Register the new module**

Check how `postgres-heap-page.js` currently gets loaded — read `inspector/web/static/app.js` for its import list (it imports each view module so the `registerView` call at the bottom of each one runs). Add:

```js
import './views/postgres-heap-and-index.js';
```

next to the existing `import './views/postgres-heap-page.js';` line in that same import block.

- [ ] **Step 4: Add the index tree styles**

In `inspector/web/static/shell.css`, after the existing `.heap-detail h3 { font-size: 14px; }` rule, add:

```css
.heap-and-index { display: grid; gap: 4px; }
.index-tree { display: grid; gap: 14px; margin-top: 8px; }
.index-level { display: flex; gap: 10px; flex-wrap: wrap; }
.index-page {
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 8px 10px;
  background: var(--panel);
  min-width: 160px;
}
.index-items { display: flex; gap: 4px; flex-wrap: wrap; }
.index-chip {
  font: 11px var(--mono);
  padding: 2px 6px;
  border-radius: 4px;
  background: var(--cell);
  border: 1px solid var(--line);
}
.index-chip.dead { background: var(--cell); color: var(--muted); text-decoration: line-through; }
```

- [ ] **Step 5: Manual smoke test in the browser**

This is a view-layer change with no Go unit test. Verification happens in Task 6 once the lab is running end to end — note that dependency here and proceed.

- [ ] **Step 6: Commit**

```bash
git add inspector/web/static/views/postgres-heap-page.js inspector/web/static/views/postgres-heap-and-index.js inspector/web/static/app.js inspector/web/static/shell.css
git commit -m "$(cat <<'EOF'
Add the heap+index panel view

Factors the heap page renderer out of postgres-heap-page.js so the new
postgres.heap_and_index view can reuse it, and adds a B-tree level
diagram below it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: New article and docs

**Files:**
- Create: `site/articles/postgres-index-page.md`
- Modify: `/home/ali-rasouli/glasshouse/Glasshouse/CLAUDE.md`

**Interfaces:**
- Consumes: the four action names (unchanged, already in `site/actions.txt` since `postgres-index`'s `Actions()` delegates to the same ones), the `GLASSHOUSE_ADAPTER=postgres-index` switch from Task 3.

- [ ] **Step 1: Write the article**

Create `site/articles/postgres-index-page.md`:

```markdown
---
title: Reading a B-tree index
actions: [insert_rows, update_rows, delete_rows, vacuum_full]
---

## What you will see

The heap page article showed where PostgreSQL stores a table's rows. This
one shows the other half: the B-tree index that lets Postgres find a row by
value without scanning every page. The panel below shows both together —
the heap page on top, the index's real pages underneath — read live from
`pageinspect`, the same way as before. Nothing here is simulated.

The index is on `glasshouse_demo.payload`, not `id`. `insert_rows` never
sets `id` (it stays NULL on every new row), so an index on it would never
fill in or split; `payload` gets a random value on every row, so the tree
actually grows as you insert.

Each box below is one index page: its block number, whether it's the root,
an internal page, or a leaf, and its level (0 is the leaf level). The chips
inside are key prefixes — hover one for its exact position and the heap
tuple (or child page) it points to.

## Start the lab

This article shares its compose file with the heap-page article, switched
to the index adapter:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.1.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.1.0:inspector \
  GLASSHOUSE_ADAPTER=postgres-index \
  docker compose -f - up -d --build --wait
```

Once it reports ready, the panel at the end of this article connects to it
on `127.0.0.1:8765`, the same as the heap-page article. Only one of the two
labs can run on that port at a time — stop this one with `down -v` before
starting the other.

## Watch it change

Press **insert_rows** a few times. Each press adds 10 rows; watch the
index's leaf pages fill and, eventually, split into two as a page runs out
of room — a new leaf box appears below, and the tree grows a level once a
single root page can no longer hold all the leaf pointers.

**update_rows** and **delete_rows** mark heap tuples dead without touching
the index's keys directly — Postgres only cleans up the index entry once
the row is vacuumed. **vacuum_full** rewrites the table and the index
together, so dead entries disappear from both the heap page above and the
index pages below at the same time.

## When you're done

Stop the lab and remove its data with the same command, swapping `up` for
`down -v`:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.1.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.1.0:inspector \
  GLASSHOUSE_ADAPTER=postgres-index \
  docker compose -f - down -v
```

<!-- lab-panel -->
```

- [ ] **Step 2: Update CLAUDE.md**

In `/home/ali-rasouli/glasshouse/Glasshouse/CLAUDE.md`, change:

```
Not built yet (deferred): the Postgres index and replication adapters (3b/3c),
and Redis/Mongo/MinIO. The Website is in progress; see
`docs/superpowers/specs/2026-10-04-website-design.md`.
```

to:

```
Not built yet (deferred): the Postgres replication adapter (3c), and
Redis/Mongo/MinIO. The Postgres index adapter (3b) is built — see
`site/articles/postgres-index-page.md` and
`inspector/internal/adapter/postgres/btreepage.go`. The Website is in
progress; see `docs/superpowers/specs/2026-10-04-website-design.md`.
```

And add a new bullet to the "## Gotchas we already hit" list, after the `ctid::point` bullet:

```
- `bt_page_items`'s `ctid` column on an internal or root page encodes the
  downlink as `(block,0)` — the offset part is unused. `downlinkBlock` in
  `btreepage.go` parses just the block.
```

- [ ] **Step 3: Commit**

```bash
git add site/articles/postgres-index-page.md CLAUDE.md
git commit -m "$(cat <<'EOF'
Add the B-tree index article

Shares the heap-page article's compose file via GLASSHOUSE_ADAPTER, and
documents the index adapter's ctid downlink format as a gotcha.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: End-to-end verification and the static sample

**Files:**
- Create: `samples/postgres.heap_and_index.json`

**Interfaces:**
- Consumes: everything from Tasks 1–5, running together for the first time.

- [ ] **Step 1: Run the full automated test suite**

Run: `make test` (add `USE_DOCKER=1` if Go is not installed on this host)
Expected: gofmt check, vet, and all tests pass for both the `inspector` and `site` modules — including the five `TestWalkIndex*` tests, `TestListActionsPrintsPostgresIndexActionsWithoutDSN`, and the pre-existing suite (regression check: `postgres.heap_page` and its article are unaffected).

- [ ] **Step 2: Build the site and confirm the new article validates**

Run: `make site-build`
Expected: exits 0, prints "site built in site/dist"; `site/dist/postgres-index-page.html` (or whatever path the build script derives from the filename — check `site/dist` after the run) exists. This exercises `article.Build`'s check that every declared action (`insert_rows`, `update_rows`, `delete_rows`, `vacuum_full`) is in `site/actions.txt`.

- [ ] **Step 3: Start the index lab and exercise it**

```bash
cd adapters/postgres
GLASSHOUSE_INSPECTOR_CONTEXT=../../inspector GLASSHOUSE_ADAPTER=postgres-index \
  docker compose -f compose.yml up -d --build --wait
```

Then:

```bash
curl -fsS http://127.0.0.1:8765/health
curl -fsS http://127.0.0.1:8765/snapshot | head -c 2000
curl -fsS -X POST -H 'X-Glasshouse-Action: 1' http://127.0.0.1:8765/actions/insert_rows
curl -fsS -X POST -H 'X-Glasshouse-Action: 1' http://127.0.0.1:8765/actions/insert_rows
curl -fsS -X POST -H 'X-Glasshouse-Action: 1' http://127.0.0.1:8765/actions/insert_rows
curl -fsS http://127.0.0.1:8765/snapshot | python3 -c '
import json, sys
env = json.load(sys.stdin)
d = env["snapshot"]["data"]
print("heap blocks:", [p["block"] for p in d["heap"]["pages"]])
print("index pages:", [(p["block"], p["type"], p["level"]) for p in d["index"]["pages"]])
print("index truncated:", d["index"]["truncated"])
'
```

Expected: the index pages list shows at least a root/leaf, item counts grow
as `insert_rows` is pressed repeatedly, and (with enough presses) a second
leaf page appears once the first one splits.

- [ ] **Step 4: Confirm in the browser**

Open `http://127.0.0.1:8765/` and press the **insert_rows**, **update_rows**,
**delete_rows**, and **vacuum_full** buttons. Confirm:
- The heap byte map (top) and the index tree diagram (bottom) both appear
  and both change after each action.
- Hovering an index chip shows its `itemoffset`/`ctid` in a tooltip.
- No browser console errors.

- [ ] **Step 5: Capture the static sample**

```bash
curl -fsS http://127.0.0.1:8765/snapshot > /tmp/heap_and_index_raw.json
python3 -c '
import json
with open("/tmp/heap_and_index_raw.json") as f:
    env = json.load(f)
with open("samples/postgres.heap_and_index.json", "w") as f:
    json.dump(env, f, indent=2)
    f.write("\n")
'
```

Open `samples/postgres.heap_and_index.json` and confirm `schema_version`
is `1` and `snapshot.type` is `"postgres.heap_and_index"`.

- [ ] **Step 6: Stop the lab**

```bash
cd adapters/postgres
GLASSHOUSE_INSPECTOR_CONTEXT=../../inspector GLASSHOUSE_ADAPTER=postgres-index \
  docker compose -f compose.yml down -v
```

- [ ] **Step 7: Confirm the original heap-page lab still works unchanged**

```bash
make stack-up
curl -fsS http://127.0.0.1:8765/snapshot | python3 -c 'import json,sys; print(json.load(sys.stdin)["snapshot"]["type"])'
# expect: postgres.heap_page
make stack-down
```

- [ ] **Step 8: Commit the sample**

```bash
git add samples/postgres.heap_and_index.json
git commit -m "$(cat <<'EOF'
Add the static sample for postgres.heap_and_index

Captured from a real stack after a few insert_rows presses, so the
Website can read it with zero setup. Confirmed the original
postgres.heap_page lab is unaffected by the mode switch.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```
