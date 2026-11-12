# Glasshouse Hosted Lab: design

Date: 2026-11-08
Status: draft for review

## Intent

Glasshouse today asks the reader to pull a compose file and run a lab in their
own terminal. This pivot hosts the lab instead. A reader reads an article on the
Glasshouse site, scrolls through a diagram that explains each step, and at the
end signs in and gets their own live PostgreSQL to poke at with fixed buttons.

Why: removing "install Docker and run compose" removes the biggest drop-off
between reading about Postgres internals and seeing them on a real instance.

Success:

- A reader with only a browser can follow an article from the first paragraph to
  pressing buttons against a real Postgres, in under a minute from reaching the
  last section.
- Every state shown in the diagram, in the scroll steps and in the live view,
  traces back to data pulled from a real running Postgres. Nothing is drawn by
  hand or simulated.
- One reader's activity can never affect another reader's lab.

## Goals

- Static article pages (SSG). Article text never depends on who is viewing.
- A pinned right-hand panel that follows scroll: each article section maps to one
  diagram state (for example "row added to the heap", "index entry added").
- Scrolling back up returns the diagram to that section's state.
- GitHub and Google sign-in.
- A "Try it yourself" section at the end of each article with a terminal display
  and a live diagram of the reader's own database.
- One separate Postgres per reader, started on demand, deleted on idle.

## Non-goals

- Typing into the terminal. There is no free-form SQL and no shell.
- Persistence. Nothing survives a lab's deletion.
- A toggle between "article" and "my database" views. The two never share a
  panel: scroll states are in the article body, the live view is only in the
  final section.
- More than one lab per reader at a time.
- Accounts, profiles, or any data about the reader beyond sign-in identity.
- Redis, Mongo, MinIO, and the replication adapter (still deferred).

## Anti-goals (a failure even if it technically works)

- Any reader input reaching SQL or a shell. Adapter SQL stays constant.
- Scroll-state diagrams that are hand-authored rather than captured from a real
  Postgres.
- One reader's inserts, vacuums, or load slowing or altering another's lab.
- A page that is blank or broken without JavaScript for the article text itself.

## Decided

These came from the owner and are not up for re-opening by the builder.

| Topic | Decision |
| --- | --- |
| Pages | Static, SSG. Article content is the same for every reader. |
| Layout | Reading column on the left, sticky diagram panel on the right (the "A" layout from the mockups). |
| Scroll behaviour | Diagram state follows the section in view, both scrolling down and back up. The live database is not used for explanation. |
| Interactive lab | Only at the end of the article. Terminal and live diagram, no toggle. |
| Terminal input | Buttons only. No typing. |
| Isolation | Separate Postgres per reader (not a shared cluster with per-reader databases). |
| Sign-in | GitHub or Google. |
| Lab lifecycle | Starts when the reader presses "Start my lab" in the final section (not at sign-in). Deleted after about 15 minutes idle. One lab per reader. No persistence. |
| Hosting | A small Hetzner VPS. Docker-in-Docker (dind) hosts the labs. |
| Isolation level | Basic for version one: privileged dind on the same VPS as the gateway. Gateway secrets (OAuth secrets, session-signing key) must stay out of the dind environment and out of any lab. Hardening (rootless or user-namespaced runtime, separate lab host) is a later step. |
| Identity policy | Open sign-up: anyone with a GitHub or Google account. The provider must report a verified email, otherwise sign-in is refused. Per-account caps on lab starts apply (numbers are the builder's call). The owner must supply a domain and register the GitHub and Google OAuth apps; gateway work can start with local-only test apps before then. |

## Decided by the assistant (the owner may override)

- **Frontend:** Next.js with static export (`output: "export"`), TypeScript, in a
  new top-level `web/`. Not Vite. Reason: SSG with a good content story. Note that
  a static export has no server routes, so sign-in lives in the Go backend, not in
  the Next.js app.
- **Backend:** Go, a new service in the monorepo (`gateway/`), reusing the
  Inspector's adapter code and security conventions. It is separate from the
  frontend.
- **Single origin:** the VPS serves the static export and the backend under one
  origin, with a reverse proxy (for example Caddy, which also handles TLS).
  This avoids cross-site cookie and CORS problems for sign-in.
- **Stateless sessions:** a signed, `HttpOnly`, `SameSite=Lax`, `Secure` cookie
  carrying provider and subject id. No user database.
- **Capacity:** a small VPS fits roughly 10 to 15 concurrent labs. A configurable
  cap applies; when full, a reader waits in a short queue and sees their position.
- **Order of work:** frontend (works with no backend), then backend, then the
  final section that joins them.

## Architecture

```
Browser ──HTTPS──> Reverse proxy ──┬─> static export (web/out)
                                   └─> gateway (Go)
                                          │  docker API, only client of the dind socket
                                          ▼
                                   dind daemon
                                     ├─ lab-<id-A>: postgres + inspector, own network
                                     ├─ lab-<id-B>: postgres + inspector, own network
                                     └─ ...
```

- The browser never talks to a lab directly. The gateway authenticates the
  request, finds the caller's lab, and forwards only named actions and a
  read-only snapshot stream.
- A lab is the same pair of services as today (Postgres 17 with pageinspect
  wrappers, plus the Inspector), started by the gateway through the dind Docker
  API using the existing `adapters/postgres/compose.yml` contents as the template.
- Each lab gets its own Docker network, no outbound internet, and hard limits
  (memory, CPU, pids). Exact numbers are the builder's call after measuring.
- Idle means no action or stream activity from the owner. At the timeout the
  gateway removes the lab's containers, network, and volumes.
- On gateway restart it removes any lab containers it finds by label, so
  nothing is orphaned.

### Components

**1. Article frontend (`web/`).** Markdown articles with front matter naming the
article's ordered steps, each with a snapshot file. The page renders the text
statically and a sticky panel that looks up the snapshot for the section in view
(IntersectionObserver or equivalent). The panel renders the snapshot with the
Inspector's existing view mapping (`snapshot.type` to a view). Existing articles
(`postgres-heap-page`, `postgres-index-page`) become the first two.

**2. Snapshot capture (build time).** A script runs a real lab through the
article's declared action sequence and records the snapshot after each step into
static JSON files shipped with the article. The build fails if an article names a
step with no captured snapshot, or an action the Inspector does not define.

**3. Gateway (`gateway/`).**
- OAuth2 / OIDC login for GitHub and Google, session cookie, logout.
- `POST /api/lab` start (or queue), `DELETE /api/lab` stop, `GET /api/lab/events`
  proxy of the lab Inspector's SSE stream, `POST /api/lab/actions/<name>`.
- Lab manager: create, health-wait, idle reaping, cap and queue, orphan cleanup.

**4. Final section (in `web/`).** "Start my lab" button, queue/starting state,
then a terminal display plus live diagram. Each action button posts to the
gateway; the response carries the echoed command and `psql`-style output, which
the terminal component renders as text.

## Security rules

The rules in `CLAUDE.md` still hold inside each lab and in the Inspector. What
changes at the edges:

| Existing rule | Hosted version |
| --- | --- |
| Binds 127.0.0.1 only | Labs and Inspectors are unreachable from outside; only the gateway can reach them. The gateway listens only on the proxy's interface. |
| No hosted multi-tenant backend | **Reversed on purpose.** Hosting per-reader labs is the point of this pivot. |
| Inspector has no Docker access | Still true. Only the gateway holds dind access. |
| Browser picks an action name; no body read | Same, enforced by the gateway too. Keep the `X-Glasshouse-Action: 1` header and origin checks. |
| Never pass browser input to SQL or shell | Unchanged. The terminal display is output-only. |
| Viewer writes API data with `textContent` only | Unchanged for the terminal and diagram. |
| DSN from env, never argv | Unchanged. Per-lab credentials are generated and never reach the browser. |

New: OAuth state and PKCE, secure cookies, per-user rate limits on start and
actions, CSRF protection on every state-changing route, and no outbound network
from labs. dind needs elevated privileges; the builder should note the risk and
may propose hardening (for example a user-namespaced or rootless runtime).

## Constraints

- One small VPS. Memory is the limiting resource, so the cap must be enforced.
- No persistence anywhere, including logs of reader activity beyond what is
  needed for abuse control.
- Article pages must stay SSG; nothing in the article body may need the backend.
- Keep the schema contract in `schema/README.md` in step with any change to
  `adapter.go`; bump `SchemaVersion` on a breaking change.

## Calls made after review (assistant's, the owner may override)

- **Images:** the lab Postgres and Inspector images are built in CI, pushed to a
  registry, and pre-pulled into the dind daemon at deploy time. A lab start never
  pulls from the internet, which also keeps the "no outbound network" rule and the
  under-a-minute start target.
- **Restarts:** gateway state (labs, queue) is in memory. On restart the gateway
  removes all lab containers by label, so a deploy ends active labs and clears the
  queue. A returning reader sees the page resume at "Start my lab". One lab per
  reader is enforced by the gateway keyed on provider and subject, so extra tabs
  share one lab.
- **Inspector changes in place:** the action result gains the echoed command and
  `psql`-style output in the Inspector itself. `inspector/internal/adapter/actions.go`
  is the authoritative action list for the build-time check. The gateway is a
  trusted client on the lab network and is the only entry on the Host and Origin
  allowlists there. Snapshot schema changes follow the `schema/README.md` rule.
- **Snapshot capture:** captured snapshots are committed and refreshed manually
  with a make target, from a fresh cluster. Article text may quote only values that
  are stable on a fresh cluster (ctids, line pointer offsets, page layout) and never
  XIDs, LSNs, or timestamps. A CI check re-captures and fails if a quoted value
  differs.
- **Existing viewer:** the Inspector's `web/static` viewer and the Go-built site
  keep working for the local lab until the hosted version ships. The hosted
  frontend gets its own React views, not shared modules.
- **Accessibility and mobile:** article text works without JavaScript; the sticky
  panel needs JavaScript. On narrow screens the panel becomes an inline diagram
  under each section. The same person signing in with both providers counts as two
  readers.

## Left to the builder

- The diagram renderer: reuse the existing 2D views as React components first;
  whether and when to add a 3D model is open. (Assistant's guess: 2D first.)
- How the action response carries `psql`-style output (new field on the action
  result versus a separate endpoint), and how closely it mimics real `psql`.
- Exact lab resource limits, idle timeout tuning, cap, and queue behaviour.
- Domain, TLS, and reverse proxy setup. (The owner has not named a domain.)
- What happens to the current local-lab path (`make stack-up`, the GitHub Pages
  site). Assistant's guess: keep both working until the hosted version ships, then
  decide.
- Deployment and CI for the VPS.

## Review (2026-11-12)

Findings from reading the design against the code, with the resolution the
build follows. Each overrides the text above where they disagree.

1. **The gateway cannot reach labs inside dind as drawn.** Networks created by
   the dind daemon exist only in dind's network namespace. Resolution: the
   gateway and Caddy run with `network_mode: service:dind`, so all three share
   one namespace. Each lab gets its own `internal: true` network (verified on
   Docker 29: the daemon's namespace reaches a container on an internal
   network, the container has no egress). The gateway dials the lab
   Inspector's container IP. The gateway listens on `127.0.0.1` and dind's API
   is a unix socket on a volume shared only with the gateway, so a lab
   container (which sees only its bridge address) can reach neither.
   Containers keep separate environments, so gateway secrets are not in dind's
   or any lab's env. Caveat, stated plainly: a privileged container can mount
   host block devices, so "secrets out of dind" is nominal until dind is
   replaced by a user-namespaced runtime (sysbox) or moved to another host.
   Readers run no code in a lab, which keeps the practical risk small.
2. **No Inspector change is needed for the gateway as trusted client.** The
   Inspector's Host allowlist accepts `localhost:8765`, and a request with no
   `Origin` passes. The gateway proxies with `Host: localhost:8765` and strips
   `Origin`.
3. **The live view needs the snapshot, not only events.** The viewer renders a
   snapshot and refreshes it when an event arrives. The gateway adds
   `GET /api/lab/snapshot`, and `GET /api/lab` returns lab status plus the
   Inspector's action list (from `/health`).
4. **Idle must not count an open stream.** A forgotten tab would keep a lab
   forever. Idle is time since the last action or snapshot fetch (15 min), and
   every lab has a hard lifetime cap (60 min).
5. **`update_rows` and `delete_rows` pick random rows; payloads are random.**
   A CI re-capture would differ on every run. Resolution: article text quotes
   only layout values from deterministic steps (line pointer offsets, ctids of
   inserted rows, page header fields). The CI re-capture check is deferred;
   `make capture` refreshes the committed snapshots by hand.
6. **psql-style output** comes from the Inspector: every action result gains a
   `tag` field holding the real command tag (`INSERT 0 10`, `VACUUM`), as
   psql prints it. Additive, so no `SchemaVersion` bump. The terminal echoes
   the action's `query` and prints the tag.
7. **CSRF:** `SameSite=Lax` cookie, the gateway's exact-`Origin` check on
   every non-GET, and the `X-Glasshouse-Action: 1` header together cover it;
   no CSRF token.
8. **Labs are started with `docker compose`** against dind, using the
   unchanged `adapters/postgres/compose.yml` plus a fixed override file
   (`gateway/lab.override.yml`): prebuilt Inspector image, no published ports,
   internal network, resource limits, a `glasshouse.lab` label. Project names
   and passwords are generated by the gateway; passwords go through the
   environment, never argv. Orphans are removed by label at start-up.
9. **Google:** the verified-email check uses the userinfo endpoint over TLS
   with the access token, so no ID-token signature verification is needed.
   GitHub uses `/user/emails` and requires the primary email to be verified.
10. **Toolchain:** this machine has Node 18, so the frontend uses Next 15
    (Next 16 needs Node 20). The gateway is a separate stdlib-only Go module.
11. **Existing drift, fixed in passing:** `site/actions.txt` lacks `vacuum`.
