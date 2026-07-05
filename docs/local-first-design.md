# Local-first design

The on-prem system is the source of truth. A [hosted hub](./hosted-hub.md) can
aggregate many of these later, but every controller is fully functional alone.
Status tags below: **[built]**, **[in progress]**, **[designed]**.

## Modules — explicit, configured, supervised

A node either has a module **installed** (a real, configured, supervised thing)
or it doesn't. Nothing auto-appears.

- **Controller** **[built]** — the bootstrap module; automatic on the first node.
- **AI module** **[in progress]** — must be **explicitly installed** on a node,
  with a **soft VRAM reservation** ("set aside 16 of 24 GB"). The reservation is
  a *managed target*, not a hard cap — it's a signal the **DevOps agent** owns
  (monitors used-vs-reserved, adjusts, alerts). On NVIDIA it can become a real
  budget once the container backend exists; on Apple Silicon (unified memory)
  it's a shown target. Installing gates the AI page — **no AI nav until the
  module exists.**
- **Future** **[designed]** — DNS app, storage, database, app-pool.

Mechanically, **install = the ComponentRuntime starts/supervises the component**
(start Ollama with the VRAM config) **+ writes the module record.**

### ComponentRuntime — how we manage the separate processes

One interface, swappable backends, chosen per platform:

```
ComponentRuntime { Install · Start · Stop · Restart · Status }
```
- **Supervisor / launchd backend** — bare-metal + macOS dev (keeps Metal GPU,
  which containers can't reach).
- **Container backend** (Docker/containerd → k3s) — Linux production; GPU
  passthrough, scheduling.

`traegod` itself runs as a node service (systemd/launchd) so it's the supervised
root; everything else hangs off it.

## Sites — multi-site aware from day one **[built]**

- `store.Site` + `Node.Site`; the controller has a **home site**
  (`TRAEGO_SITE_ID`/`_NAME`, default `local`). Nodes declare a site at announce
  (`TRAEGO_SITE`); unknown sites **auto-register**. `GET/POST /api/v1/sites`.
- **No VPN.** Initial target is a customer network that already has **UniFi
  SD-WAN** routing between sites — Traego just needs **routable IPs + firewall
  rules**. Cross-site join already works today via the L3 path (a node given the
  controller's routable URL adopts over routed IP); the mTLS data plane already
  encrypts that traffic across the WAN.
- **Keep addresses routable** — no `localhost`/LAN assumptions baked into
  component-to-component addressing.

### Ports to open between sites

| Port | Purpose |
|------|---------|
| 8443 | Controller API + SPA (bearer) |
| 8444 | mTLS data plane (node ↔ controller) |
| 53   | DNS app (when deployed) |
| TBD  | App-level replication (DNS zone sync) |

## Persistence **[built]**

- `store.OpenBolt` — a bbolt-backed `store.Store` (single embedded file,
  `TRAEGO_DB`). Uses **gob**, not JSON, so the `json:"-"` secrets
  (`Credential`/`EnrollSecret`) persist — without that, nodes couldn't re-auth
  after a restart.
- A shared **contract test** runs the identical suite against `Memory` and
  `Bolt`; a reopen test proves the credential survives.
- **Verified:** an adopted node + its site survive a controller restart.
- **Next:** persist the CA (so mTLS certs stay valid across restart) and the
  inference enabled-set; then a Raft-backed store for controller HA (control
  state only — *not* bulk model blobs).

## Model deployment = pool-level desired state **[designed]**

"Deploy a model" means *the pool runs it*; every AI-module node converges:

1. **Desired set lives on the controller** (persisted).
2. **Each AI node reconciles** — pulls missing models, reports (in its
   heartbeat) which it actually has. Same desired-vs-actual pattern as adoption.
3. **A new AI node stands up by reconciling** — install the AI module on
   machine 2, it pulls the deployed set and becomes a full replica.
4. **Status is per-model-across-nodes** ("on 2/2 AI nodes"; rollout 1/2 → 2/2).
5. **LAN model cache** — node 2 pulls blobs from node 1 over the mesh, not the
   internet (Ollama blobs are content-addressed). Bulk data, *not* the
   consensus log.
6. **Gateway routing** — once ≥2 nodes have a model, `/chat` (and the OpenAI
   `/v1` for tools like `pi`) load-balances across them. HA: a node dies, the
   other serves. Route *around* a node still pulling a just-deployed model.

## Security

- **mTLS is the default** for nodes (CA-signed certs, :8444). **[built]**
- **Pending:** TLS-terminate the admin API + public `/chat` and require a token
  — needed before any cross-WAN/cross-site customer deploy.

## The recursive pattern

`node → controller → hub` is the same enroll/heartbeat shape at each level.
Building it cleanly at the node↔controller level is what lets the
[hosted hub](./hosted-hub.md) reuse it one level up.
