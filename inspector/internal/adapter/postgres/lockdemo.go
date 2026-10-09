// Locking demo: three actions that give a reader something real to see in
// pg_stat_activity — a backend blocked on another session's row lock,
// released on command, distinct from a backend simply busy with its own
// long-running query.
package postgres

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// firstRowSQL names the row hold_lock and blocked_update both target, so
// the two actions always point at the same tuple.
const firstRowSQL = `SELECT ctid FROM glasshouse_demo ORDER BY ctid LIMIT 1`

// heldLock tracks the transaction hold_lock is currently holding open, so
// release_lock has something to commit and blocked_update has something to
// wait on. nil fields mean no lock is held.
type heldLock struct {
	mu   sync.Mutex
	conn *pgxpool.Conn
	tx   pgx.Tx
}

// holdLock opens its own connection and transaction and locks the first row
// of glasshouse_demo with SELECT ... FOR UPDATE. The lock stays open until
// releaseLock commits it — there is no timer here, only an explicit release.
func holdLock(lock *heldLock, pool *pgxpool.Pool) (string, error) {
	lock.mu.Lock()
	alreadyHeld := lock.tx != nil
	lock.mu.Unlock()
	if alreadyHeld {
		return "", errors.New("already holding a lock; press release_lock first")
	}

	conn, err := pool.Acquire(context.Background())
	if err != nil {
		return "", err
	}
	tx, err := conn.Begin(context.Background())
	if err != nil {
		conn.Release()
		return "", err
	}
	var ctid string
	if err := tx.QueryRow(context.Background(), firstRowSQL+" FOR UPDATE").Scan(&ctid); err != nil {
		tx.Rollback(context.Background())
		conn.Release()
		return "", err
	}

	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.tx != nil {
		// Another hold_lock press won the race while this one was acquiring
		// its own connection; give this one up rather than stomp on it.
		tx.Rollback(context.Background())
		conn.Release()
		return "", errors.New("already holding a lock; press release_lock first")
	}
	lock.conn, lock.tx = conn, tx
	return ctid, nil
}

// releaseLock commits the transaction holdLock opened, if any, and releases
// its connection back to the pool.
func releaseLock(lock *heldLock) error {
	lock.mu.Lock()
	conn, tx := lock.conn, lock.tx
	lock.conn, lock.tx = nil, nil
	lock.mu.Unlock()

	if tx == nil {
		return errors.New("no lock is currently held")
	}
	err := tx.Commit(context.Background())
	conn.Release()
	return err
}
