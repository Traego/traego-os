# Runbook — stand up a controller, then join a worker node

The two steps every Traego deployment starts with. Uses `docker run` directly
(not compose) so each step is explicit. Build the images first:

```bash
cd backend
docker build -f deploy/Dockerfile.controller -t traego/controller:dev .
docker build -f deploy/Dockerfile.traegod    -t traego/traegod:dev    .
docker network create traego-net   # a private network for the pool
```

## Step 1 — The controller (always first)

It bootstraps the pool: holds desired state, serves the API + built-in UI, and
issues the join token every node uses.

```bash
docker run -d --name traego-controller --network traego-net -p 8443:8443 \
  -e TRAEGO_ADMIN_KEY=dev-admin-key \
  -e TRAEGO_JOIN_TOKEN=dev-join-token \
  -e TRAEGO_HB_TIMEOUT=15s \
  traego/controller:dev

curl -s localhost:8443/healthz                 # -> {"status":"ok"}
open http://localhost:8443                      # the dashboard (admin key prefilled)
```

`TRAEGO_ADMIN_KEY` authenticates operators (the UI/CLI). `TRAEGO_JOIN_TOKEN` is
the shared secret a node presents to announce. In production both are generated,
not `dev-*`.

## Step 2 — A worker node (in a container)

A worker needs only the controller's address and the join token. It announces
itself, prints a pairing code, and waits to be adopted.

```bash
docker run -d --name traego-worker-1 --network traego-net \
  -e TRAEGO_CONTROLLER=http://traego-controller:8443 \
  -e TRAEGO_JOIN_TOKEN=dev-join-token \
  -e TRAEGO_NODE_NAME=worker-gpu-01 \
  -e TRAEGO_NODE_CLASS=ephemeral \
  -e TRAEGO_GPU_VRAM_GB=24 \
  -e TRAEGO_MEMORY_GB=32 \
  -e TRAEGO_HB_INTERVAL=3s \
  traego/traegod:dev

docker logs traego-worker-1
# -> announced as n-... — pairing code: K6QR-CGJC (confirm this in the controller to adopt)
```

The node now shows up as `pending`:

```bash
curl -s -H "Authorization: Bearer dev-admin-key" localhost:8443/api/v1/nodes
```

## Step 3 — Adopt it (the verified handshake)

Confirm the pairing code from the node's console matches, and assign a role.
Do it in the UI (pick a role, click Adopt) or via the API:

```bash
ID=...        # from the list above
CODE=K6QR-CGJC
curl -s -X POST -H "Authorization: Bearer dev-admin-key" \
  -d "{\"pairing_code\":\"$CODE\",\"role\":\"inference\"}" \
  localhost:8443/api/v1/nodes/$ID/adopt
```

Within a heartbeat the node goes `online` (its log prints `adopted as
role=inference — credential issued`). The pairing code is the security gate:
network presence alone never adopts a node.

## Step 2b — An app module over mTLS

Same join, but the node uses a CA-signed certificate and heartbeats over mutual
TLS (`:8444`). Set `TRAEGO_SECURE=true`:

```bash
docker run -d --name traego-app-module --network traego-net \
  -e TRAEGO_CONTROLLER=http://traego-controller:8443 -e TRAEGO_JOIN_TOKEN=dev-join-token \
  -e TRAEGO_NODE_NAME=app-module-01 -e TRAEGO_SECURE=true \
  traego/traegod:dev
```

After you adopt it, its log shows the handshake, and the node reports `secured: true`:

```
adopted as role=app — credential issued
mTLS established — controller verified identity as n-... (cert serial ..., expires ...)
```

The node fetched a CA-signed client cert (`POST /api/v1/nodes/{id}/certificate`)
and now authenticates by certificate, not bearer token. In the UI it gets an
"mTLS" badge.

## Graceful drain

Stopping the container sends SIGTERM; traegod calls `leave` and the controller
marks the node `offline` immediately (no waiting for the heartbeat timeout):

```bash
docker stop traego-worker-1     # log: "left the pool cleanly"; state -> offline
```

If a node dies hard instead (power loss), the controller's reaper flips it to
`offline` after `TRAEGO_HB_TIMEOUT`.

## Teardown

```bash
docker rm -f traego-controller traego-worker-1
docker network rm traego-net
```

> Shortcut: `docker compose -f deploy/compose.yml up --build` runs a controller
> plus three workers in one go. This runbook is the manual version that shows
> what compose does under the hood.
