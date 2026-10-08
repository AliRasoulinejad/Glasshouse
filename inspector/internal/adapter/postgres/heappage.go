// Package postgres is the storage adapter for PostgreSQL. It reads the last
// few heap pages of a demo table through pageinspect and emits them as a
// "postgres.heap_page" snapshot, plus events when tuples appear or get
// deleted.
//
// Every SQL statement in this package is a constant. The only values that
// vary are bound parameters, and the relation name is a constant too, so no
// browser input can reach a query.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"glasshouse/inspector/internal/adapter"
)

// Type is the snapshot type this adapter emits.
const Type = "postgres.heap_page"

// Relation is the one table the adapter inspects. It is fixed in code on
// purpose; the viewer cannot choose a table.
const Relation = "glasshouse_demo"

// Header mirrors pageinspect's page_header() output.
type Header struct {
	LSN      string `json:"lsn"`
	Checksum int    `json:"checksum"`
	Flags    int    `json:"flags"`
	Lower    int    `json:"lower"`
	Upper    int    `json:"upper"`
	Special  int    `json:"special"`
	PageSize int    `json:"page_size"`
	Version  int    `json:"version"`
	PruneXID string `json:"prune_xid"`
}

// Item mirrors one row of pageinspect's heap_page_items() output.
type Item struct {
	LP         int    `json:"lp"`
	Offset     int    `json:"lp_off"`
	Flags      int    `json:"lp_flags"`
	Length     int    `json:"lp_len"`
	XMin       string `json:"t_xmin"`
	XMax       string `json:"t_xmax"`
	CTID       string `json:"t_ctid"`
	Infomask2  int    `json:"t_infomask2"`
	Infomask   int    `json:"t_infomask"`
	HoffSize   int    `json:"t_hoff"`
	NullBitmap string `json:"t_bits"`
	DataHex    string `json:"t_data_hex"`
}

// Page is one block of the relation.
type Page struct {
	Block  int    `json:"block"`
	Header Header `json:"header"`
	// FreeSpace is the gap between the item pointer array and the tuple area.
	FreeSpace int    `json:"free_space"`
	Items     []Item `json:"items"`
}

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

// maxPages caps how many blocks are read and shown side by side, so the
// panel stays cheap to draw even once the demo table has many pages.
const maxPages = 3

// HeapPages is the snapshot payload: the relation's last few blocks, oldest
// first, so the reader can see rows spill from one page into the next.
type HeapPages struct {
	Relation string `json:"relation"`
	Pages    []Page `json:"pages"`
}

// Adapter inspects Relation's last few blocks — up to maxPages, ending at the
// one most recently written — so the view keeps up as the demo table grows
// past its first page.
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

// New returns an adapter that polls every interval.
func New(interval time.Duration) *Adapter {
	return &Adapter{Interval: interval}
}

// Connect opens a pool and checks that pageinspect is installed. The endpoint
// is a libpq connection string. It is read from the environment by the
// caller, never from command-line arguments.
func (a *Adapter) Connect(ctx context.Context, target adapter.Target) error {
	pool, err := pgxpool.New(ctx, target.Endpoint)
	if err != nil {
		return fmt.Errorf("postgres: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("postgres: ping: %w", err)
	}
	var installed bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pageinspect')`,
	).Scan(&installed)
	if err != nil {
		pool.Close()
		return fmt.Errorf("postgres: check pageinspect: %w", err)
	}
	if !installed {
		pool.Close()
		return errors.New("postgres: pageinspect extension is not installed")
	}

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

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pool != nil {
		a.pool.Close()
	}
	a.pool = pool
	a.source = target.Name
	return nil
}

// Close releases the connection pool.
func (a *Adapter) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pool != nil {
		a.pool.Close()
		a.pool = nil
	}
}

func (a *Adapter) getPool() (*pgxpool.Pool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pool == nil {
		return nil, errors.New("postgres: not connected")
	}
	return a.pool, nil
}

// lastBlock returns the relation's highest block number, the one most
// recently written.
func lastBlock(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var blocks int
	err := pool.QueryRow(ctx,
		`SELECT greatest(pg_relation_size($1::regclass) / current_setting('block_size')::int - 1, 0)`,
		Relation,
	).Scan(&blocks)
	if err != nil {
		return 0, fmt.Errorf("postgres: relation size: %w", err)
	}
	return blocks, nil
}

// readBlock pulls the current header and item pointers of one block.
func readBlock(ctx context.Context, pool *pgxpool.Pool, block int) (Page, error) {
	page := Page{Block: block}

	err := pool.QueryRow(ctx,
		`SELECT lsn, checksum, flags, lower, upper, special, pagesize, version, prune_xid
		 FROM glasshouse_page_header($1::int4)`,
		block,
	).Scan(&page.Header.LSN, &page.Header.Checksum, &page.Header.Flags,
		&page.Header.Lower, &page.Header.Upper, &page.Header.Special,
		&page.Header.PageSize, &page.Header.Version, &page.Header.PruneXID)
	if err != nil {
		return Page{}, fmt.Errorf("postgres: page_header: %w", err)
	}
	page.FreeSpace = page.Header.Upper - page.Header.Lower

	rows, err := pool.Query(ctx,
		`SELECT lp, lp_off, lp_flags, lp_len, t_xmin, t_xmax, t_ctid,
		        t_infomask2, t_infomask, t_hoff, t_bits, t_data_hex
		 FROM glasshouse_heap_page_items($1::int4)
		 ORDER BY lp`,
		block,
	)
	if err != nil {
		return Page{}, fmt.Errorf("postgres: heap_page_items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		// heap_page_items() returns NULL for every tuple-header column once a
		// line pointer is LP_DEAD or LP_UNUSED (no tuple there to describe) —
		// which opportunistic pruning can produce at any time, not only after
		// an explicit VACUUM. Scan through nullable types and fall back to
		// zero values; the viewer already skips drawing zero-length items.
		var it Item
		var xmin, xmax, ctid, bits, dataHex sql.NullString
		var infomask2, infomask, hoff sql.NullInt32
		if err := rows.Scan(&it.LP, &it.Offset, &it.Flags, &it.Length,
			&xmin, &xmax, &ctid,
			&infomask2, &infomask, &hoff,
			&bits, &dataHex); err != nil {
			return Page{}, fmt.Errorf("postgres: scan item: %w", err)
		}
		it.XMin = orElse(xmin, "0")
		it.XMax = orElse(xmax, "0")
		it.CTID = ctid.String
		it.Infomask2 = int(infomask2.Int32)
		it.Infomask = int(infomask.Int32)
		it.HoffSize = int(hoff.Int32)
		it.NullBitmap = bits.String
		it.DataHex = dataHex.String
		page.Items = append(page.Items, it)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("postgres: items: %w", err)
	}
	if page.Items == nil {
		page.Items = []Item{}
	}
	return page, nil
}

// orElse returns s's value, or def if s is NULL.
func orElse(s sql.NullString, def string) string {
	if !s.Valid {
		return def
	}
	return s.String
}

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
		var lsn, rmgr, recordType, blockRef string
		var description sql.NullString
		var length int
		if err := rows.Scan(&lsn, &rmgr, &recordType, &length, &description, &blockRef); err != nil {
			return nil, fmt.Errorf("postgres: scan wal record: %w", err)
		}
		for _, blk := range parseBlockRefBlocks(blockRef, relfilenode) {
			out = append(out, WALRecord{
				LSN: lsn, Rmgr: rmgr, RecordType: recordType,
				Block: blk, Length: length, Description: orElse(description, ""),
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

// readPages pulls the relation's last few blocks, oldest first, so the view
// can show rows spilling from one page into the next.
func (a *Adapter) readPages(ctx context.Context) (HeapPages, error) {
	pool, err := a.getPool()
	if err != nil {
		return HeapPages{}, err
	}

	last, err := lastBlock(ctx, pool)
	if err != nil {
		return HeapPages{}, err
	}
	start := last - maxPages + 1
	if start < 0 {
		start = 0
	}

	pages := make([]Page, 0, last-start+1)
	for b := start; b <= last; b++ {
		page, err := readBlock(ctx, pool, b)
		if err != nil {
			return HeapPages{}, err
		}
		pages = append(pages, page)
	}
	return HeapPages{Relation: Relation, Pages: pages}, nil
}

// Snapshot returns the current heap pages.
func (a *Adapter) Snapshot(ctx context.Context) (adapter.Snapshot, error) {
	pages, err := a.readPages(ctx)
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
		Data:      pages,
	}, nil
}

// StreamEvents re-reads the page on an interval and emits what changed.
// Events are derived by diffing consecutive reads, so they describe what the
// page looks like afterwards, not the SQL that caused it.
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

	// First read only establishes the baseline.
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

// rowsOnVisiblePages picks n random tuples from the blocks the viewer is
// showing (the relation's last maxPages blocks), so update_rows and
// delete_rows spread their effect across the pages on screen instead of
// always landing on the last one.
const rowsOnVisiblePages = `
	SELECT ctid FROM glasshouse_demo
	WHERE (ctid::text::point)[0]::int >= $1
	ORDER BY random()
	LIMIT $2`

// firstVisibleBlock returns the lowest block number the viewer is currently
// showing, matching readPages' window.
func firstVisibleBlock(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	last, err := lastBlock(ctx, pool)
	if err != nil {
		return 0, err
	}
	start := last - maxPages + 1
	if start < 0 {
		start = 0
	}
	return start, nil
}

// Actions exposes the fixed operations the viewer may trigger. Each one runs
// a constant statement and takes no input.
func (a *Adapter) Actions() map[string]adapter.Action {
	return map[string]adapter.Action{
		"insert_rows": {
			Description: "Insert 10 rows into " + Relation + " (id: null, payload: a random 32-char hex string)",
			Run: func(ctx context.Context) (any, error) {
				pool, err := a.getPool()
				if err != nil {
					return nil, err
				}
				tag, err := pool.Exec(ctx,
					`INSERT INTO glasshouse_demo (payload)
					 SELECT md5(random()::text) FROM generate_series(1, 10)`)
				if err != nil {
					return nil, err
				}
				return map[string]any{"rows_inserted": tag.RowsAffected()}, nil
			},
		},
		"update_rows": {
			Description: "Overwrite payload on 3 random rows across the visible pages with a new random hex string (the old row version becomes a dead tuple)",
			Run: func(ctx context.Context) (any, error) {
				pool, err := a.getPool()
				if err != nil {
					return nil, err
				}
				start, err := firstVisibleBlock(ctx, pool)
				if err != nil {
					return nil, err
				}
				tag, err := pool.Exec(ctx,
					`WITH target AS (`+rowsOnVisiblePages+`)
					 UPDATE glasshouse_demo d SET payload = md5(random()::text)
					 FROM target t WHERE d.ctid = t.ctid`,
					start, 3)
				if err != nil {
					return nil, err
				}
				return map[string]any{"rows_updated": tag.RowsAffected()}, nil
			},
		},
		"delete_rows": {
			Description: "Delete 3 random rows across the visible pages (they become dead tuples until vacuumed)",
			Run: func(ctx context.Context) (any, error) {
				pool, err := a.getPool()
				if err != nil {
					return nil, err
				}
				start, err := firstVisibleBlock(ctx, pool)
				if err != nil {
					return nil, err
				}
				tag, err := pool.Exec(ctx,
					`WITH target AS (`+rowsOnVisiblePages+`)
					 DELETE FROM glasshouse_demo d USING target t WHERE d.ctid = t.ctid`,
					start, 3)
				if err != nil {
					return nil, err
				}
				return map[string]any{"rows_deleted": tag.RowsAffected()}, nil
			},
		},
		"vacuum_full": {
			Description: "VACUUM FULL " + Relation + " (reclaims space held by dead tuples)",
			Run: func(ctx context.Context) (any, error) {
				pool, err := a.getPool()
				if err != nil {
					return nil, err
				}
				if _, err := pool.Exec(ctx, `VACUUM (FULL) glasshouse_demo`); err != nil {
					return nil, err
				}
				return map[string]any{"vacuumed": true}, nil
			},
		},
	}
}
