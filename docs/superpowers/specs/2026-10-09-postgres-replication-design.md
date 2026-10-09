# Postgres replication adapter: design

Date: 2026-10-09
Status: draft for review

## Purpose

Glasshouse's existing Postgres labs show one instance's storage internals
(heap/index pages) and activity (`pg_stat_activity`). This adds a second
node topology — one primary with two replicas, each replicating by a
different real mechanism — so a reader can see what "replication" actually
means underneath the word: bytes of WAL shipped to an exact physical copy,
versus decoded row changes applied to an independently writable database.

Success: a reader starts a four-container lab (primary + two replicas +
Inspector), presses `insert_rows` on the primary, and watches the same
write land on both replicas through visibly different mechanisms and
stats. Pausing the physical standby visibly opens and closes a lag gap.
Writing directly into the logical replica succeeds, proving it is a
separate writable database — something that has no equivalent action on
the physical standby.

## Scope

In scope:

- A new, self-contained compose stack, `adapters/postgres-replication/`:
  one primary, one physical (streaming) standby, one logical subscriber,
  one Inspector.
- A new adapter package, `inspector/internal/adapter/postgresreplication/`,
  selected via `-adapter postgres-replication`, reading three DSNs from the
  environment.
- A new snapshot type, `postgres.replication`.
- Three actions: `insert_rows`, `throttle_replica1`, `write_on_replica2`.
- A new view module registered for `postgres.replication`.
- A new article, `site/articles/postgres-replication.md`.
- Generalizing `Makefile`'s `stack-*`/`demo` targets to a `STACK` variable,
  and `site-actions` to append each known adapter's action list.
- `schema/README.md` and a new `samples/postgres.replication.json`.

Out of scope (deferred):

- More than two replicas, or chained/cascading replication.
- Switching a running replica's mode between logical and physical — the
  two are different topologies, not a runtime toggle (see Decisions).
- Synchronous replication (`synchronous_standby_names`) — a real variation,
  but a separate concern from logical-vs-physical; can be a follow-up.
- Failover/promotion (`pg_ctl promote` on the standby).
- Any adapter other than Postgres (Mongo/Redis/MinIO remain deferred
  elsewhere); this spec's Makefile change is written so a future adapter's
  own `adapters/<name>/` stack needs no further Makefile edits.

## Decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Comparison shape | Both replicas always running, side by side, against one primary | Logical and physical replication are different topologies (subscriber vs. standby), not modes of one connection — a live "switch" isn't realistic. Running both at once gives a direct, simultaneous contrast instead. |
| Adapter boundary | New sibling package (`postgresreplication`), new `-adapter` value | Keeps the existing `postgres` adapter (heap/btree/locks/queries) untouched; this is a different target topology, not a mode flag on it. |
| Config wiring | Three env vars: `GLASSHOUSE_PG_PRIMARY_DSN`, `GLASSHOUSE_PG_REPLICA1_DSN`, `GLASSHOUSE_PG_REPLICA2_DSN` | Matches the existing rule (DSNs from environment, never argv); a single adapter instance needs all three connections to compute cross-node lag. |
| Snapshot model | One adapter, one combined snapshot (`primary` + `replicas[]`), not three separate adapters/hubs | Matches the existing `postgres.heap_and_index` precedent (one adapter combining heap+index reads into one snapshot) rather than restructuring the server's single-adapter/single-hub contract to support multiple sources. |
| Streaming standby bootstrap | `pg_basebackup` from the primary at first start, then `standby.signal` + `primary_conninfo` (`application_name=replica1`) | Standard, minimal way to stand up a physical replica without a third-party tool; `application_name` makes the primary's `pg_stat_replication` row identifiable. |
| Logical subscriber bootstrap | Independent `initdb`, own copy of the `glasshouse_demo` schema created ahead of time, then `CREATE SUBSCRIPTION` against a `CREATE PUBLICATION` on the primary | Logical replication requires the subscriber to be a normal, independently writable database with a matching target table — it is not a block-level copy. |
| Primary's `wal_level` | `logical` | A superset of `replica`; required for the publication to exist at all, and still sufficient for the physical standby. |
| Replay-pause privilege | SECURITY DEFINER wrapper for `pg_wal_replay_pause()`/`pg_wal_replay_resume()`, owned by the superuser, hard-wired to no arguments | Same pattern as the existing `glasshouse_page_header` wrappers: these functions are superuser-only by default and `glasshouse_inspector` must not hold superuser. |
| Action surface | `insert_rows`, `throttle_replica1`, `write_on_replica2` — no sync/async toggle | The headline contrast is mechanism (byte-level WAL replay vs. decoded logical apply into a writable copy), not synchronous commit — `write_on_replica2` demonstrates the actual structural difference (logical replica is writable, physical standby is not) more directly than a stats-only comparison. |
| Makefile | Parameterize `stack-up`/`stack-down`/`stack-restart`/`stack-logs`/`demo` on a `STACK` variable (default `postgres`), replacing the hardcoded `PG_DIR` | Lets `adapters/postgres-replication/` (and any future `adapters/<name>/`) reuse the same targets via `make stack-up STACK=postgres-replication`, with zero behavior change to today's default invocation. |
| `site-actions` | Append each known adapter's `-list-actions` output into one `actions.txt`, rather than parameterizing `cmd/build` | `cmd/build`'s validation only checks "does this action name exist in some real adapter," so a union list is sufficient and keeps `cmd/build` unchanged. |
| Port/stack exclusivity | Only one stack runs at a time (same `:8765`/loopback ports) | Already true today for host-vs-container; extended to mean "stop one stack before starting another," documented in `CLAUDE.md`. |

## Components

### 1. `adapters/postgres-replication/compose.yml` (new)

Self-contained, same pulled-from-a-URL shape as `adapters/postgres/compose.yml`.
Four services:

- `postgres-primary` (`postgres:17-alpine`): inline init script (same
  `configs:` pattern) creates `glasshouse_demo`, seeds a few rows, creates
  `glasshouse_replicator` (`LOGIN REPLICATION`), creates
  `CREATE PUBLICATION glasshouse_pub FOR TABLE glasshouse_demo`, creates
  `glasshouse_inspector` with `pg_read_all_stats` (for
  `pg_stat_replication`) and the `glasshouse_replay_pause`/
  `glasshouse_replay_resume` SECURITY DEFINER wrappers (defined here but
  only ever called over replica connections — see below). Sets
  `wal_level = logical` via command-line `-c` flags.
- `postgres-replica1` (`postgres:17-alpine`): command overridden with a
  small shell entrypoint that, on an empty data directory, runs
  `pg_basebackup -h postgres-primary -U glasshouse_replicator -D $PGDATA
  -Fp -Xs -P`, writes `standby.signal`, and appends
  `primary_conninfo='... application_name=replica1'` to
  `postgresql.auto.conf`, then execs the normal entrypoint. Also creates
  `glasshouse_inspector` locally (read-only role; this is a standby, no
  writes are possible regardless of grants) with `pg_read_all_stats` and
  the two replay-pause/resume wrapper functions (owned by its own
  superuser, since pause/resume is called on the standby, not the
  primary). `depends_on` + `healthcheck` on the primary gate the basebackup.
- `postgres-replica2` (`postgres:17-alpine`): normal independent `initdb`.
  Init script creates the `glasshouse_demo` table (matching schema, no
  data), a `glasshouse_inspector` role with `SELECT, INSERT` on
  `glasshouse_demo` (this role performs `write_on_replica2` directly, no
  wrapper needed — it is a plain writable table here) and
  `pg_read_all_stats` (for `pg_stat_subscription`), then
  `CREATE SUBSCRIPTION glasshouse_sub CONNECTION 'host=postgres-primary
  ... dbname=glasshouse user=glasshouse_replicator' PUBLICATION
  glasshouse_pub`.
- `inspector`: same as today's compose, `-adapter postgres-replication`,
  three DSN env vars pointing at the three Postgres services over the
  compose network. Loopback-only published port, same as today.

### 2. `inspector/internal/adapter/postgresreplication/` (new package)

- `adapter.go`: `Adapter` struct holds three `*pgxpool.Pool` (primary,
  replica1, replica2) and a `seq uint64`. Implements `adapter.Adapter`.
  `Connect` opens all three pools. `source = "postgres-replication"`.
- Types:
  ```go
  type Primary struct {
  	LSN string `json:"lsn"`
  }

  type Replica struct {
  	Name  string `json:"name"`            // "replica1" | "replica2"
  	Mode  string `json:"mode"`            // "streaming" | "logical"

  	// streaming-only (replica1); omitted ("") / null when mode == "logical"
  	State       string  `json:"state,omitempty"`
  	SyncState   string  `json:"sync_state,omitempty"`
  	SentLSN     string  `json:"sent_lsn,omitempty"`
  	WriteLSN    string  `json:"write_lsn,omitempty"`
  	FlushLSN    string  `json:"flush_lsn,omitempty"`
  	ReplayLSN   string  `json:"replay_lsn,omitempty"`
  	ReplayLagMS *float64 `json:"replay_lag_ms,omitempty"`
  	Paused      *bool   `json:"paused,omitempty"`

  	// logical-only (replica2); omitted when mode == "streaming"
  	SubscriptionName string `json:"subscription_name,omitempty"`
  	ReceivedLSN      string `json:"received_lsn,omitempty"`
  	LatestEndLSN     string `json:"latest_end_lsn,omitempty"`
  	Writable         *bool  `json:"writable,omitempty"`

  	LagBytes *int64 `json:"lag_bytes,omitempty"`
  }

  type ReplicationSnapshot struct {
  	Primary  Primary   `json:"primary"`
  	Replicas []Replica `json:"replicas"`
  }
  ```
  A replica missing from `pg_stat_replication`/`pg_stat_subscription`
  entirely (not yet connected, or paused long enough to drop off) still
  gets an entry with just `name`/`mode` set and the rest zero-valued —
  the view must treat that as "disconnected," not an error, same rule as
  a `null` heap-item value elsewhere.
- `readPrimary(ctx, pool)`: `SELECT pg_current_wal_lsn()`.
- `readReplica1(ctx, primaryPool, replica1Pool)`:
  - From the primary: `SELECT state, sync_state, sent_lsn, write_lsn,
    flush_lsn, replay_lsn, extract(epoch from replay_lag) * 1000 FROM
    pg_stat_replication WHERE application_name = 'replica1'` (zero or one
    row).
  - From replica1 directly: `SELECT pg_is_wal_replay_paused()`.
  - `lag_bytes = pg_wal_lsn_diff(primary.lsn, replay_lsn)`, computed in Go
    via a `pg_wal_lsn_diff` call (bound params are the two LSN strings,
    both adapter-read values, never browser input).
- `readReplica2(ctx, primaryPool, replica2Pool)`:
  - From the primary: `SELECT state, sent_lsn FROM pg_stat_replication
    WHERE application_name = 'replica2'` — the logical walsender also
    appears here, distinguished by its slot's type.
  - From replica2 directly: `SELECT subname, received_lsn, latest_end_lsn
    FROM pg_stat_subscription WHERE subname = 'glasshouse_sub'`.
  - `lag_bytes` computed the same way against `received_lsn`.
- `Snapshot(ctx)`: runs the three reads, increments `seq`, returns
  `Snapshot{Type: "postgres.replication", Source: a.source, Seq: a.seq,
  Data: ReplicationSnapshot{...}}`.
- `events.go`: `StreamEvents` polls every tick (same `time.Ticker` pattern
  as `heappage.go`) and diffs consecutive `ReplicationSnapshot`s to emit:
  - `wal_shipped` — primary's `lsn` advanced. `detail: {from, to,
    bytes}`, `correlation_id: "<source>:<tick>"`.
  - `replay_paused` / `replay_resumed` — replica1's `paused` flipped.
    `detail: {replica: "replica1"}`.
  - `lag_opened` / `lag_closed` — either replica's `lag_bytes` crosses a
    fixed `lagThresholdBytes` (e.g. 1024) going up/down, so routine
    sub-threshold jitter each tick doesn't flood the timeline.
  - `replica2_write` — replica2's own row count advanced (detected the
    same diffing way, by reading `SELECT count(*) FROM glasshouse_demo`
    on replica2 each tick) — confirms `write_on_replica2` actually landed,
    independent of the primary's own `insert_rows`.
- `actions.go`:
  - `insert_rows`: `INSERT INTO glasshouse_demo (id, payload) VALUES
    ($1, md5(random()::text))` on the primary pool, same shape as the
    existing heap-page action.
  - `throttle_replica1`: calls `glasshouse_replay_pause()` on replica1's
    pool, sleeps in a background goroutine, then calls
    `glasshouse_replay_resume()` after a fixed duration (same
    timed-release pattern as `holdLock` in `lockdemo.go`).
  - `write_on_replica2`: `INSERT INTO glasshouse_demo (id, payload)
    VALUES ($1, md5(random()::text))` directly on replica2's pool —
    deliberately bypasses the primary/publication path entirely, to make
    the point that replica2 is not just receiving changes but is itself
    a normal writable database.

### 3. `inspector/cmd/inspector/main.go` (modified)

`buildAdapter` gains a `postgres-replication` case reading
`GLASSHOUSE_PG_PRIMARY_DSN`, `GLASSHOUSE_PG_REPLICA1_DSN`,
`GLASSHOUSE_PG_REPLICA2_DSN` (all required, same "missing env var is an
error" handling as the existing `postgres` case). `listActions` gains the
same case for `make site-actions`.

### 4. `inspector/web/static/views/postgres-replication.js` (new)

Registered for `postgres.replication` in `registry.js`. Renders:

- A primary header: current LSN (`textContent` only).
- Two labeled boxes, "Streaming (physical)" and "Logical", each rendering
  its replica's relevant fields (mode-specific, per the `Replica` shape
  above) plus a simple lag indicator (bytes and, for replica1, ms).
- The action buttons (`insert_rows`, `throttle_replica1`,
  `write_on_replica2`), same button-triggers-`/actions/<name>` pattern as
  every existing view.
- The existing shared event-timeline component below, rendering the four
  new event kinds; unrecognized kinds already fall back generically, so
  no new fallback logic is needed.

### 5. `Makefile` (modified)

```make
STACK     ?= postgres
STACK_DIR := adapters/$(STACK)

stack-up:
	cd $(STACK_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) docker compose -f compose.yml up -d --build --wait
	@echo "viewer: http://127.0.0.1:8765/"

stack-down:
	cd $(STACK_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) GLASSHOUSE_ALLOWED_ORIGIN=$${GLASSHOUSE_ALLOWED_ORIGIN:-unused} docker compose -f compose.yml down -v

stack-logs:
	cd $(STACK_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) GLASSHOUSE_ALLOWED_ORIGIN=$${GLASSHOUSE_ALLOWED_ORIGIN:-unused} docker compose -f compose.yml logs --tail=50

demo:
	sh $(STACK_DIR)/demo.sh

site-actions:
	cd $(INSPECTOR_DIR) && $(GO) run ./cmd/inspector -list-actions -adapter postgres > ../site/actions.txt
	cd $(INSPECTOR_DIR) && $(GO) run ./cmd/inspector -list-actions -adapter postgres-replication >> ../site/actions.txt
```

`PG_DIR` is removed (superseded by `STACK_DIR`). `make stack-up` with no
`STACK` set behaves exactly as today. The `help` target's doc comment
block gains a `STACK=<name>` line.

### 6. `adapters/postgres-replication/demo.sh` (new)

Mirrors `adapters/postgres/demo.sh`: before/after snapshot around
`insert_rows`, then `throttle_replica1` and a second snapshot showing
`lag_bytes` nonzero, then `write_on_replica2` and a final snapshot showing
replica2's row count changed independently.

### 7. `site/articles/postgres-replication.md` (new)

Same frontmatter/markdown/`<!-- lab-panel -->` shape as the three existing
articles. Frontmatter: `actions: [insert_rows, throttle_replica1,
write_on_replica2]`. Body walks: what streaming vs. logical replication
are, start command (`STACK=postgres-replication`), press `insert_rows` and
watch both replica boxes update, press `throttle_replica1` and watch
`lag_bytes` open then close on replica1 only, press `write_on_replica2`
and point out it has no equivalent button for replica1 — that asymmetry
*is* the lesson, not a side detail. Stop-the-lab section at the end, same
as existing articles.

### 8. `schema/README.md`, `samples/postgres.replication.json`

New row in the adapter-specific shapes table for `postgres.replication`,
pointing at `inspector/internal/adapter/postgresreplication/adapter.go`.
New sample with one representative tick: primary LSN, replica1 streaming
with small nonzero lag, replica2 logical with `writable: true`.

## Data flow

1. Reader runs `make stack-up STACK=postgres-replication` (or the
   pulled-compose form in the article).
2. Inspector connects all three pools, starts polling.
3. Each tick: read primary LSN, replica1's `pg_stat_replication` row +
   its own pause state, replica2's `pg_stat_subscription` row + its own
   row count; diff against the previous tick for events; publish one
   `postgres.replication` snapshot.
4. Viewer renders the primary header and the two replica boxes from the
   snapshot, and the event timeline from the hub's history + live stream.
5. Pressing an action calls `/actions/<name>`, which runs the fixed SQL
   against the fixed pool; the next poll tick reflects its effect.

## Error handling

A failure in any of the three per-tick reads fails the whole `Snapshot()`
call — no partial snapshot, same "no partial state" rule as the existing
adapters. If a replica's pool is simply not yet connected (first few
seconds of `pg_basebackup`/subscription catch-up), its `pg_stat_*` read
returns zero rows, not an error — surfaced as a replica entry with no
mode-specific fields set, per the `Replica` shape above, not a snapshot
failure.

## Testing

- Go unit tests for the four event kinds' diffing logic
  (`wal_shipped`, `replay_paused`/`resumed`, `lag_opened`/`closed`),
  against fixed before/after `ReplicationSnapshot` fixtures — no live DB,
  same pattern as `heappage_test.go`/`btreepage_test.go`. `replica2_write`
  diffed the same way against a fixed row-count fixture.
- No unit test for the raw reads themselves (no branching logic to
  isolate), same convention as the WAL/index-tree reads.
- `make test` (gofmt check, vet, unit tests); `USE_DOCKER=1` on this host.
- Manual: `make stack-up STACK=postgres-replication`, run
  `sh adapters/postgres-replication/demo.sh`, confirm both replica boxes
  render, `throttle_replica1` visibly opens and closes `lag_bytes`, and
  `write_on_replica2` lands only on replica2's row count.
- `site-actions`/`site-build` stay adapter-aware, not stack-aware (an
  adapter's action list doesn't depend on which stack happens to be
  running, so there's no `STACK=` variant of either); confirm
  `make site-actions` lists all of `postgres`'s and
  `postgres-replication`'s actions in one `actions.txt`, and
  `make site-build` succeeds with the new article's `actions:` list
  validated against it.

## Security

No new browser input: all three actions take no parameters, same
`X-Glasshouse-Action` header requirement as every existing action. The
two new SECURITY DEFINER wrappers (`glasshouse_replay_pause`/`_resume`)
take no arguments at all, narrower than the existing page-header wrappers
which take a block number. `glasshouse_inspector` on replica2 gets a
plain `INSERT` grant (not a wrapper) because unlike pageinspect's raw
functions, `INSERT` into an ordinary table is not a superuser-restricted
operation — no privilege boundary needs crossing there. All three
Postgres containers keep the same loopback-only port publishing as the
existing compose file; only the Inspector is reachable from the host
network, and only on `127.0.0.1`.
