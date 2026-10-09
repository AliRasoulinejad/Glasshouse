---
title: Watching a live query
actions: [insert_rows, hold_lock, blocked_update, try_update_nowait, release_lock, long_scan]
---

## What you will see

PostgreSQL keeps a live table of what every backend is doing right now:
`pg_stat_activity`. This article's panel shows a point-in-time read of it —
every row is a real backend on the database you start below, not a staged
example. For each one you get its state, what it's waiting on (if anything),
how long it's been running, and the query text itself.

Unlike the heap-page and index-page articles, there's no history to replay
here: the panel shows only what's active on the last poll. It's normal for
the list to go empty between actions — there's nothing wrong, the database
is just idle.

Every button below logs the exact SQL statement it ran underneath it, so you
can always check what a press actually did, not just its label.

## Start the lab

This article shares its lab with the heap-page article — the same compose
file, the same database, the same Inspector:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.5.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.5.0:inspector \
  docker compose -f - up -d --build --wait
```

Once it reports ready, the panel at the end of this article connects to it
on `127.0.0.1:8765`, the same as the other two articles. If one of those labs
is already running, this one is too — no separate start needed.

Press **insert_rows** once before you start below, so there's at least one
row in the demo table for the next two actions to target.

## A backend waiting on another backend

Press **hold_lock**. It opens its own connection, locks the first row of the
demo table with `SELECT ... FOR UPDATE`, and holds that lock open —
indefinitely, until you explicitly release it below. Nothing in the browser
controls when it ends; only pressing **release_lock** does.

Now, while the lock is still held, press **blocked_update**. It tries to
update that same row. Watch it show up in the panel stuck at
`state = active`, `wait_event_type = Lock`, `wait_event = transactionid` —
and duration climbing, for as long as you leave it there.

Press **release_lock**. It commits **hold_lock**'s transaction. The instant
it does, **blocked_update** completes on its own and drops out of the panel.
Nobody polls or retries it; it is Postgres's own lock queue releasing it the
moment the row is free.

If you press **hold_lock** again while one is already held, it refuses —
"already holding a lock; press release_lock first" — rather than silently
opening a second one.

## A backend that refuses to wait

**blocked_update** above waits however long it takes. Not every caller wants
that. Press **hold_lock** again (press **release_lock** first if one is
already held), then press **try_update_nowait**. It tries to update the same
row, but asks Postgres for the lock with `NOWAIT` instead of waiting in the
queue — it fails immediately, and the action log below the buttons shows the
real error Postgres returned: `could not obtain lock on row in relation
"glasshouse_demo"`. Nothing changed: Postgres rolls back that failed
statement's implicit transaction on its own, the same way it would roll back
any statement that errors.

The page diagram above the action log is reading this same row the whole
time. Watch it while you press **try_update_nowait**: the row's `t_xmax`
and payload stay exactly as **hold_lock** left them, because the failed
`UPDATE` never wrote anything. Now press **release_lock**, then
**try_update_nowait** again — this time nothing is holding the row, so it
succeeds, and you can watch the page's `t_xmax` get set on the old version
and a new tuple appear for the new one, the same shape `update_rows`
produces elsewhere in this panel.

## A backend waiting on nothing but itself

Press **long_scan**. It runs `SELECT pg_sleep(15)` on its own backend — nothing
else in the database is holding it up. In the panel it also shows up as
`state = active` for about 15 seconds, but with `wait_event_type = Timeout`
and `wait_event = PgSleep` instead of `Lock`. Same "busy" shape in the list,
different reason: one backend is waiting on another session to finish,
the other is just waiting on its own clock.

That distinction — `Lock` versus `Timeout`, or no wait event at all for a
backend doing real CPU work — is the first thing to check on a real database
when a query looks stuck: is it blocked by someone else, or just slow on its
own.

## When you're done

Stop the lab and remove its data with the same compose file:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.5.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.5.0:inspector \
  docker compose -f - down -v
```

<!-- lab-panel -->
