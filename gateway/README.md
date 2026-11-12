# Glasshouse gateway (hosted labs)

The gateway signs readers in with GitHub or Google and gives each one their
own throwaway PostgreSQL 17 + Inspector ("a lab"). It proxies a narrow API
to that lab. Design: `docs/superpowers/specs/2026-11-08-hosted-lab-design.md`
(see its "Review" section for how the build differs from the first draft).

```
Browser ─HTTPS─> Caddy ─┬─ /api/*, /auth/* ─> gateway 127.0.0.1:8080
                        └─ everything else ─> web/out (static export)
            all three in dind's network namespace
gateway ─unix socket─> dind ─> lab-<id>: postgres + inspector, internal network
```

## API (same origin as the site)

| Route | What it does |
| --- | --- |
| `GET /auth/{github,google}/login?next=/path` | Starts OAuth2 with PKCE and a signed state cookie. |
| `GET /auth/{provider}/callback` | Refuses accounts with no verified email. Sets the session cookie. |
| `POST /auth/logout` | Clears the session. |
| `GET /api/me` | `{provider, name}` or 401. |
| `GET /api/lab` | `{state, position?, actions?, expires_at?}`. State is `none`, `queued`, `starting`, `ready` or `failed`. |
| `POST /api/lab` | Starts a lab or joins the queue. 429 at the per-reader start limit or when the queue is full. |
| `DELETE /api/lab` | Stops the lab or leaves the queue. |
| `GET /api/lab/snapshot` | The lab Inspector's `/snapshot`. |
| `GET /api/lab/events` | The lab Inspector's SSE `/events` (`Last-Event-ID` is forwarded). |
| `POST /api/lab/actions/{name}` | Runs one named action from that lab's own list. |

Every non-GET request needs `Origin: <public URL>` and
`X-Glasshouse-Action: 1`. With `SameSite=Lax` cookies, that is the CSRF
defence. The Host header must be the public host. The gateway sends the lab
no cookies, no Origin header and no request body. It sends
`Host: localhost:8765`, so the Inspector's own checks accept it unchanged.

## Limits (flags)

| Flag | Default | Why |
| --- | --- | --- |
| `-max-labs` | 10 | An idle lab uses about 36 MiB. The caps are 256 MiB for Postgres and 64 MiB for the Inspector, so 10 labs at their caps plus about 150 MiB of overhead fit a 4 GB VPS. |
| `-max-queue` | 50 | Readers waiting for a slot. A queued reader who stops polling for 2 minutes is dropped. |
| `-idle` | 15m | Measured from the last action. An open tab or event stream does not count. |
| `-lifetime` | 60m | Hard cap from the time the lab started. |
| `-starts-per-hour` | 6 | Per reader. One lab per reader at a time. |
| actions | 20 per 10 s | Per reader, fixed in code. |

A lab starts in about 11 seconds on a laptop. Lab containers are
`internal: true` (no egress), publish no ports, and have memory, CPU and pid
limits (`lab.override.yml`). On start-up the gateway removes every container,
network and volume labelled `glasshouse.lab=1`, so a restart ends all labs.

## Deploying

1. Create the OAuth apps. Callback URLs:
   `https://<domain>/auth/github/callback` and
   `https://<domain>/auth/google/callback`. For local testing you can use
   `http://localhost:<port>` versions of the same URLs.
2. `cp deploy/env.example deploy/.env` and fill it in. Generate the session
   key with `openssl rand -hex 32`.
3. `make deploy-up`. This builds the site export and the gateway image,
   starts dind, the gateway and Caddy, loads the lab images into dind
   (a lab start never pulls), and restarts the gateway.

Caddy gets a certificate automatically for a bare domain in
`GLASSHOUSE_SITE_ADDRESS`. Ports 80 and 443 must be open on the VPS.

## Isolation: what holds and what does not

Verified on Docker 29 with the deploy stack:

- From inside a lab container, the gateway (`127.0.0.1:8080`), the dind API
  (a unix socket only; the image's default `tcp://0.0.0.0:2375` is turned
  off), Caddy's admin port and the internet are all unreachable. Only
  Caddy's public port 80 answers, and it serves the same public site.
- Labs are on separate internal networks and cannot reach each other.
- Gateway secrets are in the gateway container's environment only. Docker
  commands run with a minimal environment (`lab.DockerEnv`).

The weak point is **privileged dind**. A privileged container can mount the
host's block devices, so a process that escapes Postgres or the Inspector
inside a lab, and then escapes dind, could read the host, including
`deploy/.env`. Readers run no code in a lab: they pick one of a fixed set of
action names. So the practical attack surface is small, but this is not a
hard boundary. The hardening step is to run dind under sysbox (or rootless
Docker), or to move dind to a separate host.

## Tests

- `make test` runs the unit tests: security rules, the OAuth flow against a
  fake provider, the proxy, and the manager's cap, queue, idle and limits.
- `make gateway-integration` starts and removes one real lab through Docker.
  It also checks that the lab has no egress and no published ports.
