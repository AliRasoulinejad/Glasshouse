// Package postgres is the storage adapter for PostgreSQL. It reads one heap
// page of a demo table through pageinspect and emits it as a
// "postgres.heap_page" snapshot, plus events when tuples appear or get
// deleted.
//
// Every SQL statement in this package is a constant. The only values that
// vary are bound parameters, and the relation name is a constant too, so no
// browser input can reach a query.
package postgres

import (
	"context"
	"errors"
	"fmt"
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

// HeapPage is the snapshot payload.
type HeapPage struct {
	Relation string `json:"relation"`
	Block    int    `json:"block"`
	Header   Header `json:"header"`
	// FreeSpace is the gap between the item pointer array and the tuple area.
	FreeSpace int    `json:"free_space"`
	Items     []Item `json:"items"`
}

// Adapter inspects Relation's last block — the one most recently written —
// so the view keeps up as the demo table grows past its first page.
type Adapter struct {
	// Interval is how often the event stream re-reads the page.
	Interval time.Duration

	mu        sync.Mutex
	pool      *pgxpool.Pool
	source    string
	seq       uint64
	prev      map[int]Item
	prevBlock int
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

// readPage pulls the current header and item pointers of the relation's last
// block, so the view keeps up as the demo table grows past its first page.
func (a *Adapter) readPage(ctx context.Context) (HeapPage, error) {
	pool, err := a.getPool()
	if err != nil {
		return HeapPage{}, err
	}

	blocks, err := lastBlock(ctx, pool)
	if err != nil {
		return HeapPage{}, err
	}

	page := HeapPage{Relation: Relation, Block: blocks}

	err = pool.QueryRow(ctx,
		`SELECT lsn, checksum, flags, lower, upper, special, pagesize, version, prune_xid
		 FROM glasshouse_page_header($1::int4)`,
		blocks,
	).Scan(&page.Header.LSN, &page.Header.Checksum, &page.Header.Flags,
		&page.Header.Lower, &page.Header.Upper, &page.Header.Special,
		&page.Header.PageSize, &page.Header.Version, &page.Header.PruneXID)
	if err != nil {
		return HeapPage{}, fmt.Errorf("postgres: page_header: %w", err)
	}
	page.FreeSpace = page.Header.Upper - page.Header.Lower

	rows, err := pool.Query(ctx,
		`SELECT lp, lp_off, lp_flags, lp_len, t_xmin, t_xmax, t_ctid,
		        t_infomask2, t_infomask, t_hoff, t_bits, t_data_hex
		 FROM glasshouse_heap_page_items($1::int4)
		 ORDER BY lp`,
		blocks,
	)
	if err != nil {
		return HeapPage{}, fmt.Errorf("postgres: heap_page_items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.LP, &it.Offset, &it.Flags, &it.Length,
			&it.XMin, &it.XMax, &it.CTID,
			&it.Infomask2, &it.Infomask, &it.HoffSize,
			&it.NullBitmap, &it.DataHex); err != nil {
			return HeapPage{}, fmt.Errorf("postgres: scan item: %w", err)
		}
		page.Items = append(page.Items, it)
	}
	if err := rows.Err(); err != nil {
		return HeapPage{}, fmt.Errorf("postgres: items: %w", err)
	}
	if page.Items == nil {
		page.Items = []Item{}
	}
	return page, nil
}

// Snapshot returns the current heap page.
func (a *Adapter) Snapshot(ctx context.Context) (adapter.Snapshot, error) {
	page, err := a.readPage(ctx)
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
		Data:      page,
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
				page, err := a.readPage(ctx)
				if err != nil {
					if ctx.Err() == nil {
						errs <- err
					}
					return
				}
				for _, ev := range a.diff(page, now.UTC()) {
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

// diff compares the page with the previous read and returns events for new
// tuples, for tuples whose xmax was set (a delete or update), and for tuples
// that vanished from the same block (a vacuum reclaiming a dead tuple).
func (a *Adapter) diff(page HeapPage, now time.Time) []adapter.Event {
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
			ID:        fmt.Sprintf("%s:%d", a.source, a.seq),
			Seq:       a.seq,
			Timestamp: now,
			Source:    a.source,
			Kind:      kind,
			Detail:    detail,
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

// rowsOnLastBlock picks the first n tuples (by item pointer order) on the
// relation's last block, so update_rows and delete_rows act on rows the
// viewer is actually looking at.
const rowsOnLastBlock = `
	SELECT ctid FROM glasshouse_demo
	WHERE (ctid::text::point)[0]::int = $1
	ORDER BY ctid
	LIMIT $2`

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
			Description: "Overwrite payload on 3 rows of the visible page with a new random hex string (the old row version becomes a dead tuple)",
			Run: func(ctx context.Context) (any, error) {
				pool, err := a.getPool()
				if err != nil {
					return nil, err
				}
				blk, err := lastBlock(ctx, pool)
				if err != nil {
					return nil, err
				}
				tag, err := pool.Exec(ctx,
					`WITH target AS (`+rowsOnLastBlock+`)
					 UPDATE glasshouse_demo d SET payload = md5(random()::text)
					 FROM target t WHERE d.ctid = t.ctid`,
					blk, 3)
				if err != nil {
					return nil, err
				}
				return map[string]any{"rows_updated": tag.RowsAffected()}, nil
			},
		},
		"delete_rows": {
			Description: "Delete 3 rows from the visible page (they become dead tuples until vacuumed)",
			Run: func(ctx context.Context) (any, error) {
				pool, err := a.getPool()
				if err != nil {
					return nil, err
				}
				blk, err := lastBlock(ctx, pool)
				if err != nil {
					return nil, err
				}
				tag, err := pool.Exec(ctx,
					`WITH target AS (`+rowsOnLastBlock+`)
					 DELETE FROM glasshouse_demo d USING target t WHERE d.ctid = t.ctid`,
					blk, 3)
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
