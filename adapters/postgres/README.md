# Postgres storage adapter

Shows one heap page of a real PostgreSQL 17 table, read through `pageinspect`.

## Run it

One command pulls the compose file and starts Postgres and the Inspector. The
Inspector image is built from source, so the source location must be given.
Pin both to a release tag so the lab doesn't shift under you as `main` moves:

```sh
curl -fsSL https://raw.githubusercontent.com/AliRasoulinejad/Glasshouse/v0.3.0/adapters/postgres/compose.yml | \
  GLASSHOUSE_INSPECTOR_CONTEXT=https://github.com/AliRasoulinejad/Glasshouse.git#v0.3.0:inspector \
  docker compose -f - up -d --build --wait
```

`GLASSHOUSE_ALLOWED_ORIGIN` sets which site may frame the viewer. It defaults
to the published article's origin, so you don't need to set it to follow the
article. Set it only to point the lab at a different copy of the article (a
local checkout, a fork's Pages site, ...) — and set it the same way for
`docker compose down` and `logs` too, since Compose re-reads the file's
variables for every command.

Note: `docker compose -f https://...` does not work, because Compose only reads
local paths. Piping through `-f -` is the form that works.

Then open http://127.0.0.1:8765/ in a browser. That is the viewer.

From a checkout: `make stack-up`, `make stack-down`, `sh adapters/postgres/demo.sh`.

Security boundary in the container: the Inspector binds 0.0.0.0 inside its own
container only (`-container`). Both published ports are bound to 127.0.0.1 on
the host, so nothing on the network can reach them. A host binary still refuses
any non-loopback bind.

## How the privilege boundary works

pageinspect's raw-page functions refuse non-superusers even when `EXECUTE` is
granted. The Inspector therefore never gets pageinspect directly. The init
script creates two `SECURITY DEFINER` wrappers owned by the admin role:

- `glasshouse_page_header(blk int)`
- `glasshouse_heap_page_items(blk int)`

Each is hard-wired to `glasshouse_demo` and takes only an integer block number.
The `glasshouse_inspector` role may execute those two wrappers and insert into
`glasshouse_demo`. Nothing else. Confirm with:

```sh
docker exec -e PGPASSWORD=glasshouse-local-inspector postgres-postgres-1 \
  psql -h 127.0.0.1 -U glasshouse_inspector -d glasshouse -c "SELECT get_raw_page('glasshouse_demo',0);"
# -> ERROR: must be superuser to use raw page functions
```

The same pattern covers the index: `glasshouse_btree_metap()` and
`glasshouse_btree_page_items(blk int)`, both hard-wired to
`glasshouse_demo_payload_idx`.

## One lab, two articles

The same compose file serves both the heap-page and index-page articles —
there is only one adapter mode, and it always reads both the heap pages and
the index pages in the same snapshot. Only one copy of the lab runs on
`:8765` at a time — stop it (`docker compose ... down -v`) before starting
another copy (e.g. to pick up a different `GLASSHOUSE_ALLOWED_ORIGIN`).

## Notes

- The port is published on `127.0.0.1` only.
- Dev passwords are defaults in `docker-compose.yml`. Override them with
  `GLASSHOUSE_PG_ADMIN_PASSWORD` and `GLASSHOUSE_PG_INSPECTOR_PASSWORD`. The
  init script runs only on a fresh volume, so use `docker compose down -v` after
  changing them.
- The Inspector reads the DSN from `GLASSHOUSE_PG_DSN`, never from a command-line
  argument, so it does not appear in process listings.
