# Postgres storage adapter

Shows one heap page of a real PostgreSQL 17 table, read through `pageinspect`.

## Run it

```sh
cd adapters/postgres
docker compose up -d                      # Postgres 17, pageinspect enabled, loopback only

cd ../../inspector
GLASSHOUSE_PG_DSN='postgres://glasshouse_inspector:glasshouse-local-inspector@127.0.0.1:55432/glasshouse?sslmode=disable' \
  go run ./cmd/inspector -adapter postgres -origin https://article.example

# in another terminal
sh adapters/postgres/demo.sh              # before / after snapshot around an insert
```

Then open http://127.0.0.1:8765/ for the byte-map view.

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
