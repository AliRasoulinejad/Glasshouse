package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"glasshouse/inspector/internal/adapter"
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
	Type  string      `json:"type"`
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
