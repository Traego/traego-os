# Traego — software-first architecture & deployment model (v1 proposal)

Ship Traego as **open-source software you self-host on machines you already own**,
before any hardware exists. The product feel stays UniFi-like: one controller, a
one-line script to add a box to the pool, and everything else hidden behind the UI
and the Atlas agent. Hardware later becomes "a really nice node that happens to
ship pre-joined."

The mental model carries over cleanly:

| Hardware concept | Software-first equivalent |
| --- | --- |
| T2 chassis | Your **pool** (the set of joined machines) |
| Blade / bay | A **node** (any Linux box) and the **roles** it runs |
| Slotting a module | Assigning a workload role to a node, scheduled as containers |
| Backplane | A **WireGuard mesh** connecting every node privately |
| Controller module | The **control plane** (containers): API, UI, scheduler, Atlas, auth |

---

## 1. Topology

Two binaries, one mesh.

```
              ┌────────────────────── Traego Controller (control plane) ───────────────────────┐
              │  api  ·  web UI  ·  scheduler  ·  Atlas agent  ·  identity (OIDC)  ·  registry   │
              │  state: SQLite (single node) → embedded etcd / Postgres (HA, 3 nodes)            │
              └───────────────▲───────────────────────────▲───────────────────────────▲─────────┘
                    mTLS + gRPC│                           │                            │
        WireGuard mesh ════════╪═══════════════════════════╪════════════════════════════╪═══════
                               │                           │                            │
                        ┌──────┴──────┐             ┌──────┴──────┐              ┌──────┴──────┐
                        │  traegod    │             │  traegod    │              │  traegod    │
                        │  node-01    │             │  node-02    │              │  node-03    │
                        │  GPU 24GB   │             │  8c / 64GB  │              │  12 TB HDD  │
                        │  AI Inference│            │  App Pool   │              │  Storage    │
                        └─────────────┘             └─────────────┘              └─────────────┘
              home mini-PC                    home tower                    cloud VM (any provider)
```

- **`traegod`** — one Go binary per node (the agent). Joins the mesh, enrolls over
  mTLS, reports capabilities (GPU model + VRAM, cores, RAM, disk, arch), and runs the
  containers the controller assigns it. This is the thing the join script installs.
- **Controller** — the brain. Holds desired state, decides placement, exposes the API +
  UI, hosts Atlas, issues OTA updates, and is the identity provider. Runs as containers
  itself, so it's just another role; on a hobbyist single box it co-locates with workloads.
- **Mesh** — every node gets a WireGuard interface and a stable `100.x` address.
  This is what makes "add a cloud VM and a box in your garage to the same pool" work
  the way UniFi site-to-site does. A small **coordination/relay service** handles NAT
  traversal and key exchange (self-hostable; think headscale, not a mandatory cloud).
  Local-subnet multicast discovery (§2) is just how a node gets *noticed*; the mesh is what
  actually carries traffic once it's adopted — including across subnets and out to cloud nodes.

---

## 2. The join flow (the hero interaction): zero-config discovery

The UniFi magic is that you **don't** type a controller address or a token on each box.
You run one command; the controller **discovers the node on the LAN and prompts you to
adopt it.** Being on the same subnet is the whole configuration.

First machine bootstraps the pool:

```bash
curl -fsSL https://get.traego.io | sh -s -- init
# brings up the controller containers, generates org + admin + root CA and a WireGuard key,
# then starts ANNOUNCING itself on the LAN (mDNS/DNS-SD + a small UDP broadcast beacon).
```

Every other machine, same subnet, just runs the installer — **no flags**:

```bash
curl -fsSL https://get.traego.io | sh
```

What happens, no further input:
1. Installs `traegod` + a container runtime (containerd/Podman) + NVIDIA toolkit if a GPU is present.
2. `traegod` both **listens for the controller's beacon** and **announces itself** over
   mDNS (`_traego._tcp.local`) + subnet broadcast, advertising hostname, detected specs,
   and its public key.
3. The controller — continuously listening — sees the new node and surfaces it in the UI as
   **Pending adoption** with its specs. This is the UniFi "a new device appeared" moment.
4. You click **Adopt**. To stop a rogue controller or node on a shared subnet from hijacking
   anything, adoption is a **verified handshake**: `traegod` prints a short pairing code /
   key fingerprint to its console, the UI shows the same code, you confirm they match
   (SSH-known-hosts / Bluetooth-pairing style trust-on-first-use). Only then does the
   controller issue the node its mTLS identity and bring it onto the WireGuard mesh.
5. Atlas applies the baseline config, assigns a role (or suggests one from the hardware), and
   the node comes online — exactly the prototype's `addModule` flow, now a real machine.

**Discovery is announce-only; it carries no secrets.** Sharing a subnet gets a node *visible*,
never *trusted* — all trust comes from the verified handshake. So multicast discovery is safe
even on a noisy office or home network.

**Cross-subnet / cloud fallback** (multicast doesn't route off the local segment). Two options,
both still zero-touch where possible:
- **DHCP option / DNS SRV record** (`_traego._tcp`) hands nodes the controller address
  automatically — the L3 analog of UniFi's DHCP option 43 / `set-inform`. Run the bare
  installer and it finds the controller across subnets.
- **Explicit** `join --controller <url> --token <one-time-token>` for a cloud VM with no
  shared network and no DNS hook — the manual escape hatch, not the default.

Leaving is symmetric: `traego leave` or "Decommission" in the UI drains workloads, then
revokes the identity and tears down the mesh interface.

---

## 3. Orchestration: hide Kubernetes, don't expose it

**Recommendation: embed k3s as the invisible substrate; Traego is the opinionated layer on top.**

k3s is a single ~70MB binary, runs fine on one node, self-heals, has GPU scheduling, and
brings a mature operator ecosystem that *is* the module catalog (databases, storage,
ingress). We get scheduling, health, and rollouts for free and spend our effort on the
UX and Atlas instead of rebuilding a scheduler.

The hard rule: **users never see `kubectl`, YAML, or the word "Kubernetes."** `traegod`
wraps `k3s agent`; the controller talks to the embedded API and translates everything into
Traego concepts (pool, node, module, app). If this constraint ever fights us, the fallback
is a DIY control loop driving Podman directly over gRPC — simpler, but we'd own scheduling
and healing. Start on k3s.

---

## 4. How each module maps to containers

| Module | Implementation (all containers/operators on the substrate) |
| --- | --- |
| **Controller** | API (Go) · web UI · scheduler · Atlas · OIDC provider · Traefik ingress · state store. HA = 3 controller nodes with embedded etcd/raft (this is the "install two controllers" idea, generalized). |
| **AI Inference** | **vLLM** (or Ollama for small/hobbyist) behind a **LiteLLM** gateway exposing one OpenAI-compatible endpoint. A model registry pulls from HF/Ollama. "AI OS version" = a pinned, signed stack manifest. Requires a node tagged `gpu`. |
| **App Pool** | A small PaaS: **Nixpacks/Buildpacks** build any repo, run it as a workload. "Slot-based DB workers" = one-click operators (**CloudNativePG** for Postgres, etc.) that consume node capacity. This is the self-serve "make a SaaS" surface. |
| **Storage** | Pools = storage classes. **Longhorn** for replicated block volumes across nodes, **Garage/MinIO** for S3-compatible object. Auto-provision = a default class that grows volumes on demand. |
| **Database** | The dedicated-DB module becomes a node tagged `db` (high-RAM) that operators schedule onto, isolating stateful workloads from the App Pool. |

Everything is an OCI image, so **updates** are signed image bundles + a version manifest
per channel (stable/beta), pulled and rolled out node-by-node — exactly what the existing
Updates page already models.

---

## 5. Dynamic capacity — ephemeral & spot nodes (the thing UniFi can't do)

UniFi assumes every device is bolted in and always on. Real fleets aren't: someone has a
workstation with a 4090, a gaming PC, a laptop with a big GPU that's on the LAN evenings and
weekends. That idle silicon should **opportunistically join the pool when present and drain
cleanly when it leaves** — burst capacity with zero babysitting.

**Two node classes, set at adoption:**
- **Persistent** — always-on servers. Can hold the controller, primary databases, the only
  copy of a storage volume, long-lived stateful apps.
- **Ephemeral (spot / burst)** — laptops, workstations, anything that comes and goes. Runs only
  preemptible, stateless, or checkpointable work. Never the sole home of state.

**Eligibility is a policy the owner sets — "on the network" plus guardrails**, so we never melt
someone's laptop mid-meeting:
- on an allowed network (home/office subnet or SSID), not tethered to a coffee-shop hotspot,
- on AC power, not battery,
- below a CPU / GPU / thermal threshold, or only during off-hours,
- with a hard resource cap (lend ≤ 80% of the GPU; pause instantly if the owner launches a game),
- and an always-available **Pause** the owner controls.

`traegod` runs in a lightweight **contributor mode** on personal machines: a menu-bar/tray app
that shows "Contributing GPU to Northwind · 3 jobs · Pause," respects battery and thermals
automatically, and sandboxes pool workloads hard (rootless, no host mounts, network-isolated) so
the owner's data and the pool's data never touch.

**What runs on burst capacity:** stateless **AI inference** is the killer case — the laptop's GPU
registers as extra capacity and the LiteLLM/vLLM gateway load-balances onto it while it's present.
Also batch/async work: embeddings backfill, transcoding, nightly benchmarks, CI builds. Never the
controller, primary DB replicas, or single-copy storage. A confidential workload stays pinned to
persistent nodes — a laptop can literally walk out the door (data-locality policy: ephemeral = an
untrusted *location*, independent of how much you trust the owner).

**Presence & graceful drain — treat it like spot instances:**
- **Join** is instant after the first adoption: the node already has its identity, so it just
  re-announces, the controller marks it Online, and capacity returns — no re-adopt, no pairing.
- **Leave** (sleep hook, network change, owner hits Pause, or missed heartbeats): the controller
  immediately stops routing *new* requests to it, lets in-flight inference finish within a short
  grace window (idempotent, so it can also retry elsewhere), checkpoints and requeues batch jobs
  onto other nodes, and keeps the model cache warm locally for the next visit.
- The scheduler models this as two tiers — **guaranteed** (persistent) and **burst** (ephemeral) —
  with a disruption budget so nothing critical ends up depending only on capacity that might
  vanish. (k3s mechanics: taints + tolerations + PodDisruptionBudgets + a presence-driven
  autoscaler, all hidden behind Traego concepts.)

**Atlas makes it feel alive:** "Pat's workstation joined — +24 GB VRAM. Spun up a second Llama 3.3
replica and cleared the embedding backlog (1,240 docs) in 6 min." …and on departure: "Pat's
workstation left; drained 2 jobs back to the pool, no user impact." This is the dynamic-fleet
story UniFi never tells.

---

## 6. Identity & auth (ships in the box)

The controller **is** an OIDC provider (embed **Zitadel** or **Authentik**). Two modes,
pick per-org, same as the UniFi local-vs-SSO choice:

- **Self-hosted identity** — local Traego accounts, the built-in provider is the source of truth.
- **Federated** — bring your own IdP via OIDC/SAML (Google Workspace, Okta, Entra) or social (GitHub).

Every app deployed to the App Pool gets SSO for free: the **Traefik ingress runs
forward-auth**, so unauthenticated requests bounce to the Traego login and arrive at the app
with identity headers. One login, every internal app. (This also sets up the GitHub and other
integrations the parent thread asked for — GitHub is both a login provider and the source for
App Pool deploys.)

---

## 7. Security model

- **mTLS** for all control-plane and agent traffic; node identities are short-lived and rotated.
- **WireGuard** for the data plane; nothing is exposed to the public internet by default.
- **Signed artifacts** (cosign) for every release image and the version manifest; `traegod`
  refuses unsigned bundles.
- **Adoption is a verified handshake, not just network presence**: LAN discovery only makes a
  node *visible*; adopting it requires confirming a pairing code shown on both the node console
  and the UI (TOFU), so a rogue controller/node on the same subnet can't silently hijack. Tokens
  are short-TTL, one-time, and only needed for the cross-subnet/cloud fallback. All revocable per node.
- **Secrets** via an embedded store (OpenBao/age) — DB creds, model keys, app env never sit in plaintext.
- Offline-first: a pool keeps running with no internet; only OTA fetch and relay-assisted
  NAT traversal want connectivity.

---

## 8. Open-source / business shape

Open-core, so the hobbyist story is genuinely free and self-hostable:

- **Apache-2.0 core**: `traegod`, controller, scheduler glue, UI, the CLI, module recipes.
- **Self-hostable everything**, including the coordination/relay server, so there is no
  mandatory cloud dependency (the Tailscale-vs-Headscale escape hatch).
- **Paid later**: a hosted relay/coordination convenience, enterprise SSO + audit, fleet
  management across many pools, support, and — eventually — the hardware that ships pre-joined.

---

## 9. Suggested repo / component layout

```
traego/
  cmd/traegod/        # node agent (Go): mesh, enrollment, capability probe, runtime shim
  cmd/traego/         # CLI: init, join, leave, status
  controller/
    api/              # gRPC (agent) + REST/GraphQL (UI)
    scheduler/        # placement: capability match + bin-pack over the substrate
    atlas/            # the DevOps agent: watch → diagnose → act, with autonomy levels
    identity/         # OIDC provider + federation
    catalog/          # module + app recipes (declarative)
  web/                # the Vue 3 UI in this repo
  install/get.sh      # the curl|sh entrypoint (init / join)
  charts/             # bundled operators: cnpg, longhorn, traefik, vllm, litellm
```

---

## 10. Phasing

- **v1 (hobbyist, this proposal):** single-node `init` + multi-node `join`, WireGuard mesh,
  AI Inference (Ollama/vLLM), App Pool with Postgres, local + replicated storage, local +
  GitHub auth, OTA updates, Atlas in observe/supervised mode. One box gets you everything;
  a second box proves the pool.
- **v1.5:** controller HA (3-node), object storage, App Pool git-push deploys, more IdPs.
- **v2:** the hardware ships as pre-joined nodes — same software, same UI, the rack rendering
  becomes literal.

---

## 11. Default integrations to ship

| Integration | What it does |
| --- | --- |
| **GitHub** | Login provider **and** App Pool source: repo → Nixpacks build → deploy; PR previews; status back to the PR |
| **OIDC / SAML providers** | Google Workspace, Okta, Entra, Authentik for operator + app SSO |
| **Container registries** | Pull private images (GHCR, Docker Hub, ECR) |
| **S3-compatible storage** | Backup target for DBs and volumes (incl. a `storage`-role node running Garage/MinIO) |
| **Slack / Discord / webhooks** | Where Atlas sends alerts and asks for approvals |
| **Hugging Face / Ollama registry** | Model source for AI Inference, cached on the pool |
| **Prometheus / OpenTelemetry** | Export the telemetry Atlas already collects |

---

## 12. Deployment tiers

The code path is identical across tiers; a tier is just node count + which optional services run.

1. **Solo** — one machine. `traego init`, controller co-located with workloads. The hobbyist
   on-ramp; runs on a laptop or a ~$300 mini-PC.
2. **Homelab pool** — 2–10 mixed machines (towers, NUCs, a half-filled rack, a cloud VM).
   Adopt nodes, assign roles, optionally 3 controllers for HA.
3. **Small business** — same core + federated SSO, scheduled backups, the relay for remote
   access, audit log.

---

## 13. What changes in the existing prototype UI

- **"Hardware" → "Fleet."** Keep the rack rendering for people who rack-mount, but add a
  "shelf / loose machines" layout for towers and mini-PCs. Same blade/module visual language;
  each unit is now a real node.
- **Adopt flow** replaces "add module": discovered nodes appear in a **Pending adoption** tray,
  you confirm a pairing code and assign a role. The existing `AddModuleDrawer` is ~80% of this.
- **Burst capacity is first-class**: ephemeral nodes (a laptop with a big GPU) render as a
  distinct "spot" unit that flips Online/Offline as it comes and goes, with a contribution
  policy and a live offload story from Atlas — the dynamic-fleet concept made visible.
- **New surfaces**: an **Apps** deploy flow wired to GitHub, an **Access** page (providers +
  members), an **Integrations** page, and a **Node** detail page (specs, role, health, join command).
- Atlas, the autonomy dial, Guided/Pro, Updates, Storage, and the AI model library carry over
  unchanged — they were always about the software, not the metal.

---

## 14. Decisions worth making early

1. **k3s vs Podman control loop** as the hidden substrate (recommendation: k3s, with the strict
   "nothing talks to it but the Traego API" firewall so we can swap later).
2. **Agent push vs pull** for workloads (recommendation: pull + a control bus — survives flaky home networks).
3. **App runtime opinionation** — Nixpacks/Buildpacks-only (simplest) vs also allowing raw
   Dockerfiles/compose (more flexible, more support surface).
4. **Relay**: ship the self-hostable coordination/relay in core day one, or start hosted-only?
5. **Single-tenant per pool** to start (recommendation: yes — multi-tenant is a v2 problem).
