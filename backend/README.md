# Traego backend — controller + traegod (v0)

The first real vertical slice of the [architecture](../ARCHITECTURE.md): a node
**discovers → adopts → heartbeats → drains**, the flow everything else builds on.
Pure Go standard library, no external dependencies.

```
backend/
  cmd/controller/     # the control-plane API binary
  cmd/traegod/        # the node agent binary
  internal/store/     # node inventory + lifecycle state (in-memory; SQLite later)
  internal/api/       # HTTP API: announce/adopt/credential/heartbeat/leave + reaper
  internal/agent/     # traegod: announce, await adoption, heartbeat loop, graceful leave
  deploy/             # Dockerfiles, compose join harness, integration test
```

## The lifecycle

```
traegod                          controller
   │  POST /discovery/announce  ─────►  create node (pending), return
   │     (join token)                   { id, enroll_secret, pairing_code }
   │  prints pairing code  ◄──────────  operator sees the same code in the UI
   │
   │            operator: POST /nodes/{id}/adopt { pairing_code, role }  (admin auth)
   │                                    verify code → issue credential (adopted)
   │  POST /nodes/{id}/credential ───►  trade enroll_secret → { credential }
   │     (enroll secret)
   │  POST /nodes/{id}/heartbeat  ───►  mark online, stamp last-seen   (credential auth)
   │     (loop)                         reaper marks offline after the timeout
   │  POST /nodes/{id}/leave      ───►  graceful drain → offline
```

The **pairing code** is the security gate: LAN discovery only makes a node
*visible*; adoption requires confirming the code shown on the node's console, so a
rogue controller on the same subnet can't silently hijack it. Secrets are compared
in constant time and never serialize in API responses.

## Run the tests

Requires Go 1.23+.

```bash
make test          # all unit tests
make cover         # unit coverage with an 85% floor (currently ~90%)
make test-race     # race detector
make integration   # full join flow across real containers (needs Docker)
```

Coverage: **~88% over the library packages** (`store`, `api`, `ca`, `agent`),
gated at 85% in CI. The `cmd/` mains are thin and exercised by the Docker
integration test rather than unit tests.

## Try the join harness

```bash
docker compose -f deploy/compose.yml up --build
```

Brings up a controller plus three `traegod` "machines" (an always-on GPU box, an
app box, and an ephemeral workstation). They announce and wait. Adopt them:

```bash
# list discovered nodes (grab id + pairing_code)
curl -s -H "Authorization: Bearer dev-admin-key" localhost:8443/api/v1/nodes | jq

# adopt one
curl -s -X POST -H "Authorization: Bearer dev-admin-key" \
  -d '{"pairing_code":"XXXX-XXXX","role":"inference"}' \
  localhost:8443/api/v1/nodes/<id>/adopt
```

`make integration` does exactly this automatically and asserts all three reach
`online`.

## API

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | — | liveness |
| `POST` | `/api/v1/discovery/announce` | join token | register a node, get enroll secret + pairing code |
| `POST` | `/api/v1/nodes/{id}/adopt` | admin | verify pairing code, assign role, issue credential |
| `POST` | `/api/v1/nodes/{id}/credential` | enroll secret | trade enroll secret for the node credential |
| `POST` | `/api/v1/nodes/{id}/heartbeat` | node credential | liveness; marks online |
| `POST` | `/api/v1/nodes/{id}/leave` | node credential | graceful drain → offline |
| `GET` | `/api/v1/nodes` | admin | list nodes |
| `GET` | `/api/v1/nodes/{id}` | admin | get one node |
| `DELETE` | `/api/v1/nodes/{id}` | admin | remove a node |
| `GET` | `/api/v1/ca` | — | controller CA certificate (PEM) |
| `POST` | `/api/v1/nodes/{id}/certificate` | node credential | sign a node CSR → CA-signed client cert |
| `GET` | `/api/v1/system` | — | the controller's own live host metrics (CPU/mem/uptime) |

Nodes report their own CPU/memory in each heartbeat (`{"metrics": {...}}`), surfaced per-node in the UI.

## Web UI (SPA hosted by the controller)

The controller serves the designed Vue SPA itself — best-practice Go: the built
assets are embedded with `//go:embed all:dist` (`internal/web`) and served by a
handler that returns a real file if it exists, else falls back to `index.html`
for client-side routes. Build the SPA into the embed dir with `npm run build:embed`
(the Docker image does this in a node stage). Open `http://localhost:8443`.

**mTLS data plane** (separate listener, `:8444`, requires a CA-signed client cert):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/secure/whoami` | echo the verified client identity (cert CN) |
| `POST` | `/api/v1/nodes/{id}/heartbeat` | mTLS heartbeat; marks the node `secured` |

## Config (env)

**controller:** `TRAEGO_ADDR` (`:8443`), `TRAEGO_ADMIN_KEY` (req), `TRAEGO_JOIN_TOKEN` (req), `TRAEGO_HB_TIMEOUT` (`30s`), `TRAEGO_SECURE_ADDR` (`:8444`), `TRAEGO_SECURE_HOSTS` (`traego-controller,localhost,127.0.0.1`).

**traegod:** `TRAEGO_CONTROLLER` (req), `TRAEGO_JOIN_TOKEN` (req), `TRAEGO_NODE_NAME`, `TRAEGO_NODE_CLASS` (`persistent`|`ephemeral`), `TRAEGO_HB_INTERVAL` (`10s`), `TRAEGO_MEMORY_GB`, `TRAEGO_GPU_VRAM_GB`, `TRAEGO_SECURE` (`true` to use mTLS), `TRAEGO_SECURE_URL` (defaults to `https://<controller-host>:8444`).

## Secure mode (mTLS)

The controller runs an internal CA. After adoption, a node with `TRAEGO_SECURE=true`
generates a key + CSR, gets a CA-signed client certificate, and heartbeats over
**mutual TLS** on `:8444` — its identity is the verified cert CN, no bearer token.
Such nodes show `secured: true` (and an "mTLS" badge in the UI). The CA also signs
the controller's own server cert, so trust is mutual. v0 regenerates the CA on boot.

## Not yet (next slices)

Persisted CA + node certs (v0 regenerates on boot), real mDNS/broadcast discovery
(v0 uses the deterministic L3 announce), SQLite persistence, and the WireGuard mesh
/ relay for remote access.
