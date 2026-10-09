# Glasshouse

Glasshouse shows the real internals of running systems — PostgreSQL first —
from real, unmodified services. Every visualization traces back to data
pulled from a running instance: no simulations, no user code execution, no
hosted multi-tenant backend. A small local daemon (the **Inspector**) reads a
target's live state and streams it to a browser viewer; published **articles**
embed that viewer so a reader can watch real internals change as they poke at
a disposable demo database.

## What's in the box

- **Inspector** (`inspector/`) — a Go daemon that connects to one target
  (Postgres today, a canned mock for development) and serves its state over
  HTTP/SSE to a plain-JS viewer. It never executes arbitrary SQL or shell
  commands from the browser; the browser can only trigger a fixed, pre-scripted
  list of actions (insert rows, run a lock-contention demo, VACUUM FULL, ...).
- **Postgres adapter** (`adapters/postgres/`) — a self-contained Docker Compose
  lab: Postgres 17 with `pageinspect`, a demo table/index, and the Inspector
  built from source. This is what the published articles point readers at.
- **Site** (`site/`) — the static articles themselves (heap page, index page,
  running queries), built with no backend, deployed to GitHub Pages.
- **Samples** (`samples/`) — static JSON snapshots in the same envelope the
  API returns, for looking at the data shape without running anything.

## Quick start: see it work locally

You need Go and Docker. From the repo root:

```sh
make run-mock
```

This builds the Inspector and starts it against the mock adapter (a canned
counter, no external dependencies) on `http://127.0.0.1:8765/`. Open that URL
in a browser — that's the viewer. Good for a first look or for UI work on
`inspector/web/static/`.

To see the real thing — real PostgreSQL internals — run the full stack:

```sh
make stack-up        # Postgres 17 + Inspector in Docker, both on :8765
```

Open `http://127.0.0.1:8765/` once it reports ready. Then, in another
terminal:

```sh
sh adapters/postgres/demo.sh   # snapshot, insert 10 rows, snapshot again
```

Watch the viewer while the demo script runs — you'll see the heap page fill
up with real rows read straight out of the live table via `pageinspect`.

When you're done:

```sh
make stack-down       # stops the stack and deletes its data volume
```

Only one copy of the stack can run on `:8765` at a time. Use `make
stack-restart` after changing the Inspector's code or the database init
script (it does `stack-down` then `stack-up`, so the volume is rebuilt).

### Running the Inspector against Postgres on the host instead

If you'd rather run Postgres yourself and just run the Inspector binary
locally (not in Docker):

```sh
make run-postgres PG_DSN='postgres://user:pass@host:port/db?sslmode=disable'
```

Stop the Compose stack first — both use `:8765`.

## Everyday commands

| Command | What it does |
|---|---|
| `make help` | List all commands with a one-line description |
| `make test` | gofmt check, `go vet`, and the test suite (both the inspector and site Go modules) |
| `make test USE_DOCKER=1` | Same, but runs the Go toolchain inside a container (use this if Go isn't installed on your machine) |
| `make build` | Build the Inspector binary to `inspector/bin/inspector` |
| `make run-mock` | Inspector on the host, mock adapter |
| `make run-postgres` | Inspector on the host, Postgres adapter (point `PG_DSN` at your database) |
| `make stack-up` / `make stack-down` | Postgres + Inspector in Docker |
| `make stack-restart` | `stack-down` then `stack-up` — picks up code or init-script changes |
| `make stack-logs` | Tail the stack's logs |
| `make demo` | Same as `sh adapters/postgres/demo.sh` |
| `make site-build` | Build the static articles into `site/dist` (also regenerates the Inspector's action list used by the articles) |
| `make clean` | Remove the built binary |

Run `make help` any time for the live version of this table (sourced from
the Makefile's own comments).

## Working on specific pieces

- **Inspector backend** — code lives in `inspector/internal/`. The adapter
  contract is `internal/adapter/adapter.go`; the fixed, allow-listed actions
  are in `internal/adapter/actions.go`; `internal/server/` is the HTTP API and
  every security rule (and where its tests live); `internal/hub/` is the
  event history / SSE fan-out. After changing Go code, run `make test` before
  committing.
- **Viewer frontend** — plain ES modules under `inspector/web/static/`, no
  build step. `views/registry.js` maps a snapshot's `type` to the view that
  renders it. Reload the browser after editing; `make run-mock` or
  `make stack-restart` picks up changes (rebuild for stack mode since the
  image is built from source).
- **Postgres adapter / lab** — `adapters/postgres/compose.yml` is
  self-contained by design (it's pulled straight from a URL by readers), so
  don't split it into sibling files. See `adapters/postgres/README.md` for
  the privilege-boundary details (why the Inspector role never gets
  `pageinspect` directly) and `demo.sh` for a scripted before/after.
- **Articles / site** — Markdown sources in `site/articles/`, built by
  `make site-build` (which also regenerates `site/actions.txt` from the live
  Inspector so the article text can't drift from the real action list). The
  build is a separate Go module (`site/`) with its own `go.mod`, `go vet`,
  and tests — `make test` covers it too.
- **Schema / event contract** — `schema/README.md` documents the
  snapshot/event envelope. Keep it in sync with
  `inspector/internal/adapter/adapter.go` and bump `SchemaVersion` on any
  breaking change.

## Security model (why the lab is safe to run)

- The Inspector binary only binds to `127.0.0.1`/`localhost`; there's no flag
  to widen that. The only exception is `-container`, used solely inside the
  Docker image, and even then Compose publishes the ports back to `127.0.0.1`
  on the host.
- Every request is checked against a Host-header allowlist (blocks DNS
  rebinding) and an Origin allowlist (CORS + a 403 for unknown origins);
  `/health` is the only open route.
- The browser can only invoke a fixed list of pre-scripted `/actions/<name>`
  operations — never arbitrary SQL or shell. Actions ignore the request body
  and require a custom header a cross-site form can't send.
- The Inspector reads its database DSN from an environment variable, never
  from the command line, so it never shows up in `ps` output.
- Compose is never driven from the browser — the Inspector has no Docker
  access. You start and stop the lab yourself, from your own terminal.

See `CLAUDE.md` for the full list of non-negotiable rules and the gotchas
hit while building this, if you're modifying the Inspector or adapter code.

## Deeper dives

- `adapters/postgres/README.md` — running the Postgres lab from a published
  release tag (not a local checkout), and how the `pageinspect` privilege
  boundary is enforced.
- `schema/README.md` — the snapshot/event JSON contract.
- `docs/superpowers/specs/` — design specs for work in progress (e.g. the
  website redesign, the planned replication adapter).
