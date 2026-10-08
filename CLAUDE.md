# Glasshouse — notes for Claude

Glasshouse shows the real internals of running systems (PostgreSQL first) from
real, unmodified services. Every visualization must trace back to data pulled
from a running instance. No simulations, no user code execution, no hosted
multi-tenant backend.

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
- `samples/postgres.heap_page.json` — static sample, same envelope as the API.
- `site/` — static articles, built by `make site-build`; plain files, no backend.
  Deployed to GitHub Pages by `.github/workflows/pages.yml` on every push to
  `main` that touches `site/` or the Inspector's action list.
- `Makefile` — the entry point. Run `make help`.

Not built yet (deferred): the Postgres index and replication adapters (3b/3c),
and Redis/Mongo/MinIO. The Website is in progress; see
`docs/superpowers/specs/2026-10-04-website-design.md`.

## Commands

- `make test` — gofmt check, vet, tests. Add `USE_DOCKER=1` when Go is not
  installed on the host (this machine has no local Go).
- `make run-mock` / `make run-postgres` — Inspector on the host. Both use :8765.
- `GLASSHOUSE_ALLOWED_ORIGIN=<origin> make stack-up` / `make stack-down` —
  Postgres 17 + Inspector in containers. The origin is required by `stack-up`
  (compose has no default). Stop one path before starting the other.
- `sh adapters/postgres/demo.sh` — before/after snapshot around an insert.
- `make site-build` — build the static articles into `site/dist`.

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
- `go fmt` rewrites files. Use `gofmt -l` for checks.
- `pkill -f <pattern>` can match its own shell and exit 144. Use `pkill -x`
  or a bracketed pattern such as `[h]ttp.server`.

## Conventions

- Commit as you go, with meaningful messages. End each commit with
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.
- Keep the event contract in `schema/README.md` in step with
  `inspector/internal/adapter/adapter.go`. Bump `SchemaVersion` on any
  breaking change.
- Adapter events are derived by diffing consecutive reads. They describe the
  state after the change, not the SQL that caused it.
- Leave the `.idea/` ignore line in `.gitignore` alone.
