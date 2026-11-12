# Glasshouse — notes for Claude

Glasshouse shows the real internals of running systems (PostgreSQL first) from
real, unmodified services. Every visualization must trace back to data pulled
from a running instance. No simulations, no user code execution. The one
hosted backend is the gateway (`gateway/`): one isolated, throwaway lab per
signed-in reader, buttons only.

## Layout (monorepo)

- `inspector/` — Go module `glasshouse/inspector`. Local daemon that serves a
  target's state to the browser.
  - `internal/adapter/` — the adapter contract (`adapter.go`), fixed actions
    (`actions.go`), `mock/` (canned counter), `postgres/` (pageinspect heap page).
  - `internal/hub/` — event history and SSE fan-out.
  - `internal/server/` — HTTP API and every security rule. Tests live here.
  - `web/static/` — viewer shell: plain ES modules, no build step.
    `views/registry.js` maps `snapshot.type` to a view.
- `adapters/postgres/` — `compose.yml` (self-contained, pullable from a URL),
  `demo.sh`, README.
- `schema/README.md` — snapshot/event contract, schema version 1.
- `samples/postgres.heap_page.json`, `samples/postgres.heap_and_index.json` —
  static samples, same envelope as the API.
- `site/` — static articles, built by `make site-build`; plain files, no backend.
  Deployed to GitHub Pages by `.github/workflows/pages.yml` on every push to
  `main` that touches `site/` or the Inspector's action list.
- `web/` — hosted site: Next.js 15 static export (Node 18 on this machine,
  so not Next 16). Articles in `content/articles/*.md` declare ordered
  `steps` in front matter; `public/snapshots/<slug>/<step>.json` are real
  captures (`make web-capture`), never hand-edited. `lib/article-core.mjs` is
  plain JS shared by the build, `node --test` and the capture script.
- `gateway/` — Go module `glasshouse/gateway`, stdlib only. Sign-in
  (GitHub/Google), one lab per reader via `docker compose` on dind, a narrow
  proxy to that lab's Inspector. `gateway/README.md` has the API and limits.
- `deploy/` — dind + gateway + Caddy in one network namespace.
- `Makefile` — the entry point. Run `make help`.

Not built yet (deferred): the Postgres replication adapter (3c), and
Redis/Mongo/MinIO. The Postgres index adapter (3b) is built — see
`site/articles/postgres-index-page.md` and
`inspector/internal/adapter/postgres/btreepage.go`. The Website is in
progress; see `docs/superpowers/specs/2026-10-04-website-design.md`.

## Commands

- `make test` — gofmt check, vet, tests. Add `USE_DOCKER=1` when Go is not
  installed on the host (this machine has no local Go).
- `make run-mock` / `make run-postgres` — Inspector on the host. Both use :8765.
- `make stack-up` / `make stack-down` — Postgres 17 + Inspector in containers.
  `GLASSHOUSE_ALLOWED_ORIGIN` defaults to the published article's origin
  (`compose.yml`); set it only to point the lab at a different copy of the
  article. Stop one path before starting the other. The same stack serves
  both the heap-page and index-page articles — the Inspector always emits
  both in one snapshot, no mode switch.
- `sh adapters/postgres/demo.sh` — before/after snapshot around an insert.
- `make site-build` — build the static articles into `site/dist`.
- `make web-capture` / `make web-build` / `make web-test` — hosted site.
  Capture restarts the local stack on a fresh volume once per article.
- `make gateway-integration` — one real lab through Docker.
- `make deploy-up` — hosted stack from `deploy/.env`.

## Security rules (non-negotiable)

- Host binary binds 127.0.0.1 or localhost only. `ValidateAddr` enforces it;
  there is no override flag.
- The only widening is `-container`, which allows `0.0.0.0` inside the
  container. Compose must publish ports on `127.0.0.1` on the host.
- Host-header allowlist on every request (blocks DNS rebinding).
- CORS only for configured `-origin` values. `/health` is the only open route.
- Origin check on every route. A request with an unknown `Origin` gets 403.
- `/actions/<name>` maps to a fixed, pre-scripted operation. The browser picks a
  name from the list. Actions never read the request body, and they need the
  `X-Glasshouse-Action: 1` header, which a cross-site form cannot send.
- Never pass browser input into SQL or a shell. Adapter SQL is constant and
  binds only integers.
- The Inspector reads its DSN from `GLASSHOUSE_PG_DSN`, never from argv.
- The viewer writes API data with `textContent` only, never `innerHTML`.
- Compose is never run from the browser. The Inspector has no Docker access;
  the reader starts the lab in their own terminal.

## Hosted-lab rules (gateway/, deploy/)

- The gateway binds loopback only (`gateway.ValidateAddr`). Caddy and the
  gateway run with `network_mode: service:dind`, because lab networks
  exist only in dind's namespace.
- Every non-GET request needs the exact public `Origin` and
  `X-Glasshouse-Action: 1`; Host must be the public host. No CORS headers.
- The proxy sends a lab nothing from the reader: no cookies, Origin or
  body. It sends `Host: localhost:8765`, which the Inspector already allows.
- Lab names are generated (`lab-<12 hex>`, checked by regex before any
  `docker` call), never taken from a request. Docker commands get a
  minimal env (`lab.DockerEnv`), never the gateway's secrets.
- Only actions in the lab's own `/health` list are forwarded.

## Gotchas we already hit

- pageinspect's raw-page functions refuse non-superusers even with EXECUTE.
  The Inspector role must not hold pageinspect directly. It uses two
  SECURITY DEFINER wrappers (`glasshouse_page_header`,
  `glasshouse_heap_page_items`) owned by the admin, hard-wired to
  `glasshouse_demo`, taking only a block number. Keep it that way.
- `docker compose -f https://…` does not work. Use `curl … | docker compose -f -`.
- Compose interpolates `$` in inline configs. Write `$$` for a literal `$` in
  `compose.yml`.
- `page_header()` returns `pagesize`, but the Go JSON tag is `page_size`.
- The init script runs only on a fresh volume. Use `down -v` after changes to it.
- The `docker:dind` image opens unauthenticated `tcp://0.0.0.0:2375` when
  `DOCKER_TLS_CERTDIR` is empty. Start it with `command: ["dockerd", ...]`
  so the entrypoint adds no default listeners.
- `docker compose` interpolates the base `compose.yml` before applying
  overrides, so `GLASSHOUSE_INSPECTOR_CONTEXT` must be set (to anything) even
  when `lab.override.yml` resets `build`.
- `go fmt` rewrites files. Use `gofmt -l` for checks.
- `pkill -f <pattern>` can match its own shell and exit 144. Use `pkill -x`
  or a bracketed pattern such as `[h]ttp.server`.
- Chrome's Local Network Access blocks the article's iframe from loading
  `127.0.0.1` outright ("The connection is blocked...") unless the iframe
  carries `allow="local-network-access"` (`panelHTML` in
  `site/internal/article/article.go`) and the Inspector answers with
  `Access-Control-Allow-Private-Network: true` on every response (set in
  `Server.ServeHTTP`). With both in place, Chrome shows a one-time permission
  prompt instead of blocking; the reader must click Allow.
- `ctid::point` fails ("cannot cast type tid to point"). Go through text:
  `ctid::text::point`. Used to find rows on the relation's current last block
  so `update_rows`/`delete_rows` act on what the viewer is showing.
- `bt_page_items`'s `ctid` column on an internal or root page encodes the
  downlink as `(block,offset)` — the offset is not meaningful (it holds the
  key's attribute count on most items, 0 only for the leftmost "minus
  infinity" item) and is ignored. `downlinkBlock` in `btreepage.go` parses
  just the block.
- `bt_page_items()`'s `data` column is already hex-encoded TEXT
  (space-separated byte pairs), unlike `heap_page_items()`'s `t_data`, which
  is `bytea`. Wrapping it in `encode(..., 'hex')` again (as the heap wrapper
  does for `t_data`) double-encodes it; `glasshouse_btree_page_items` just
  substrings `data` directly.
- `VACUUM (FULL)` needs the PG17 `MAINTAIN` privilege (granted to
  `glasshouse_inspector` in `compose.yml`), not superuser.
- `VACUUM (FULL)` rewrites the table onto a new relfilenode, so the WAL
  overlay's relation-to-WAL-record matching (`readWALRecords` in
  `heappage.go`) re-resolves `pg_relation_filenode('glasshouse_demo')` on
  every poll tick instead of caching it — a cached value would silently stop
  matching any WAL record after the first `vacuum_full` action.
- The adapter always reads the relation's *last* block (`lastBlock` in
  `heappage.go`), not a fixed one, so the view keeps following the demo table
  as `insert_rows` pushes it past its first page.
- The snapshot's `data.pages` is an array of up to `maxPages` blocks (oldest
  first), ending at the last block, so the viewer can show pages side by
  side. Only the last block is diffed for events (`diff` in `heappage.go`);
  older pages are read-only context.
- Heap items' `id`/`payload` and leaf-level index items' `value` are real,
  decoded column values, not derived from the hex by the adapter — they
  come from `heapValues` (`heappage.go`) joining the live table on `ctid`
  through the `glasshouse_heap_values` wrapper. `id` is legitimately `NULL`
  for every row `insert_rows` creates (it never sets `id`), so scan it as
  nullable; don't treat a `NULL` id as a lookup miss. Never join on an
  internal/root index page's `ctid` — there it is a downlink encoding a
  child block, not a heap pointer, so joining it could coincidentally match
  an unrelated live row at that block/offset.

## Conventions

- Commit as you go, with meaningful messages. End each commit with
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.
- Keep the event contract in `schema/README.md` in step with
  `inspector/internal/adapter/adapter.go`. Bump `SchemaVersion` on any
  breaking change.
- Adapter events are derived by diffing consecutive reads. They describe the
  state after the change, not the SQL that caused it.
- Leave the `.idea/` ignore line in `.gitignore` alone.
