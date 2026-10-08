# Postgres storage adapter

Shows one heap page of a real PostgreSQL 17 table, read through `pageinspect`.

## Run it

One command pulls the compose file and starts Postgres and the Inspector. The
Inspector image is built from source, so the source location must be given:

```sh
curl -fsSL <url>/compose.yml | GLASSHOUSE_INSPECTOR_CONTEXT=<git-url-or-path-to-inspector> GLASSHOUSE_ALLOWED_ORIGIN=<article-origin> docker compose -f - up -d --build --wait
```

Set `GLASSHOUSE_ALLOWED_ORIGIN` to the origin the article is served from
(for example `https://example.com`). It is required. It sets which sites may
frame the viewer. The same variable must be set for `docker compose down` and
`logs` too, since Compose re-reads the file's variables for every command.

Note: `docker compose -f https://...` does not work, because Compose only reads
local paths. Piping through `-f -` is the form that works.

Then open http://127.0.0.1:8765/ in a browser. That is the viewer.

From a checkout: `GLASSHOUSE_ALLOWED_ORIGIN=<article-origin> make stack-up`,
`GLASSHOUSE_ALLOWED_ORIGIN=<article-origin> make stack-down`,
`sh adapters/postgres/demo.sh`.

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

## Notes

- The port is published on `127.0.0.1` only.
- Dev passwords are defaults in `docker-compose.yml`. Override them with
  `GLASSHOUSE_PG_ADMIN_PASSWORD` and `GLASSHOUSE_PG_INSPECTOR_PASSWORD`. The
  init script runs only on a fresh volume, so use `docker compose down -v` after
  changing them.
- The Inspector reads the DSN from `GLASSHOUSE_PG_DSN`, never from a command-line
  argument, so it does not appear in process listings.
