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
//
// usename, application_name, state, and query are coalesced to an empty
// string because background workers (autovacuum chief among them, which
// this lab triggers via delete_rows/update_rows) have no pg_authid row:
// st_userid is InvalidOid, so the LEFT JOIN to pg_authid leaves usename
// NULL, and some of these workers leave the other columns NULL too.
// Scanning a NULL into a plain Go string fails the whole row scan, which
// fails Snapshot() for as long as the worker runs — see the WAL-record NULL
// fix in 6767ef6 for the same class of bug.
const runningQueriesSQL = `
	SELECT pid, coalesce(usename, ''), coalesce(application_name, ''),
	       coalesce(state, ''),
	       coalesce(wait_event_type, ''), coalesce(wait_event, ''),
	       query_start, coalesce(query, '')
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
