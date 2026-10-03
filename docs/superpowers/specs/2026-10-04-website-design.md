# Glasshouse Website: design

Date: 2026-10-04
Status: draft for review

## Purpose

A public website of short articles. Each article teaches one practice about a
real running system (PostgreSQL first). The reader pulls one compose file, starts
it, and then works with the live lab from a panel at the end of the article.

Success: a reader with Docker and a browser can follow any article end to end
without a backend of ours running anywhere, and every number shown on the page
comes from their own running Postgres.

## Scope

In scope (phase 1):

- Static article pages on a public host.
- One pulled compose file per lab. It starts Postgres and the Inspector.
- The Inspector serves the viewer and runs the fixed, named actions that each
  article declares.
- The Postgres heap-page walkthrough as the first article.

Out of scope (deferred):

- Custom reader commands in the browser. The user said to skip them for now.
- Compose control from the browser. Compose always runs in the reader's terminal.
- Index and replication adapters, and Redis, Mongo, MinIO.
- User accounts, shared hosting, multi-tenant anything.

## Decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Articles | Static pages, no backend | Matches "articles store statically". |
| Who runs compose | The reader, in their terminal | The browser never controls Docker. |
| Controller | The existing Inspector, as a container in the lab's compose | It already serves endpoints and inspects Postgres. No new service. |
| Docker access for the Inspector | None | Compose is not run from inside the stack. No socket mount, no proxy. |
| Browser commands | Fixed, named actions per article | Keeps the rule that the browser never supplies shell or SQL input. |
| Deployment shape | Public static site + loopback Inspector on the reader's machine | Option 3 from the design discussion. No hosted multi-tenant backend. |

## Components

### 1. Static site (new, `site/`)

- Articles are Markdown files. A small build script turns them into static HTML.
  No framework.
- Each article has front matter that names its lab and the actions its panel may
  call. The build fails if a panel names an action the Inspector does not define.
- Each article's last section embeds the panel. The panel is the Inspector viewer
  at `http://127.0.0.1:8765/`, using the same origin rules as today.
- The site is plain files. It has no server code and no secrets.

### 2. Lab compose (existing `adapters/postgres/compose.yml`, extended)

- Services: `postgres` (unchanged) and `inspector` (already built from `inspector/`).
- Both published ports stay bound to `127.0.0.1`.
- The compose file stays self-contained, because it is pulled from a URL.
- Start command is unchanged: `curl … | docker compose -f - up -d --build --wait`.

### 3. Inspector (existing, changed)

- Serves the viewer and the existing `GET /snapshot`, `GET /events` and
  `POST /actions/<name>` routes.
- New: the `-origin` allowlist gets the public article origin. No wildcard.
- New: an article's named actions are registered in the Inspector. Each action is
  a fixed operation with constant SQL and integer arguments only.
- Unchanged: loopback-only bind, Host allowlist, Origin check on every route,
  `X-Glasshouse-Action` header on actions, DSN from `GLASSHOUSE_PG_DSN` only.

## Data flow

1. Reader opens an article on the public host.
2. Reader runs the lab's compose command in a terminal. Postgres and the Inspector start.
3. The panel at the end of the article loads the viewer from `127.0.0.1:8765`.
4. The viewer calls `GET /snapshot` and `POST /actions/<name>`. The Inspector reads Postgres
   through the adapter and returns the snapshot and events.
5. Every displayed value comes from the reader's running database.

## Error handling

- Inspector not running: the panel shows "lab not running" with the start command
  from the article. It does not show stale data as if it were live.
- Postgres not ready: the Inspector returns a clear error from `/health` or the
  affected route. The viewer shows it with `textContent`.
- Unknown action name: 404 from the Inspector. The build step should have
  prevented this.

## Security

- No Docker socket anywhere in the stack.
- The public site holds no secrets and does not call any service of ours.
- The Inspector trusts only the configured article origin, and only for CORS and
  the Origin check. Other origins get 403.
- Actions never read the request body and never take SQL or shell input from
  the browser.
- Known risk to verify before shipping: the public page loads a loopback address.
  Browsers treat `127.0.0.1` as a secure context, but Chrome's Private Network
  Access rules can block a public page from calling loopback without preflight
  headers. The first implementation step is a spike that tests this in Chrome,
  Firefox and Safari. If it fails, the fallback is an explicit PNA preflight
  response from the Inspector for the configured origin only.

## Testing

- Go: extend `inspector/internal/server/server_test.go` for the new origin
  configuration and action registration. Run through `make test`.
- Site build: a test that every article's named actions exist in the Inspector
  action list, and that the build fails when one does not.
- Compose: the existing demo script (`adapters/postgres/demo.sh`) still passes
  after the `inspector` service is added to the compose file.
- Manual: follow the first article end to end in each browser on the risk list.

## Documentation changes

- `CLAUDE.md`: state that static articles are allowed, that compose is never run
  from the browser, and that the Website is no longer fully deferred. Add the
  PNA note under "Gotchas" once verified.
- `schema/README.md`: only if the action list changes the event contract. The
  envelope is not expected to change.

## Open points

These are decided here so review can change them:

1. Article format: Markdown plus a small build script.
2. First article: the Postgres heap-page walkthrough, using the existing sample
   and demo script.
3. Hosting: a static host, to be chosen at implementation time. Not yet picked.
4. The actions list for article one: the existing `/actions` operations, with no
   new SQL.
