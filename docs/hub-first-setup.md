# Hub-first install & setup (design)

> Status: **design, being built.** The retail flow. Companion to
> [hosted-hub.md](./hosted-hub.md) (hub architecture),
> [auth-login-design.md](./auth-login-design.md) (identity rules — all of them
> apply here), and [local-first-design.md](./local-first-design.md).

## The scenario this optimizes

You bought a **Traego Hub** and a **controller module** (or a pre-configured
server) together. You plug them in. Like a UniFi Dream Machine, the out-of-box
experience is: find the hub, sign in, and it *finds your hardware for you* —
one continuous wizard from unboxing to an adopted, running site.

## Three setup entry points, one identity model

| Entry point | Who it's for | First screen |
|---|---|---|
| **A. Hub-first** (this doc) | bought the hardware; the retail path | hub setup wizard |
| B. Controller-first | software-only homelab, single site | controller `/setup` ([auth doc](./auth-login-design.md)) |
| C. Cloud-first | traego.ai account exists, hardware arrives later | traego.ai "add a site" |

All three converge on the same objects: an **owner identity** (traego.ai
account or local admin — the auth doc's rules apply verbatim, including the
always-works local recovery admin), an **org**, and **controllers enrolled in
the org**. A controller set up via B can be enrolled into a hub later; nothing
about B is a dead end.

## The hub-first wizard

```
1. FIND THE HUB
   Hub boots → announces `_traego-hub._tcp` via mDNS + serves http://traego.local
   (plain HTTP, per the TLS-default decision). User opens it from any LAN browser.

2. OWNER
   Same screen as the controller's setup wizard, one level up:
   [ ⚡ Sign in with your Traego account ]   ← device flow, links the org
   ────────────── or ──────────────
   Set up a local owner instead →            ← always available, offline-safe
   (+ claim code printed on the hub console/label — physical-access proof)

3. DETECT HARDWARE                            ← the step this doc exists for
   "Looking for Traego hardware…"
   ┌──────────────────────────────────────────────┐
   │ ● controller module — attached to this hub   │  auto-trusted (see below)
   │ ● homelab-r730 — found on your network       │  pairing code required
   │                                              │
   │ Nothing you expected? → Install a controller │
   └──────────────────────────────────────────────┘

4. ADOPT & NAME
   Pick the controller → (pairing code if LAN-discovered) → site name →
   the hub provisions it (identity, org enrollment, owner) in one shot.

5. LAND
   Straight into the hub's site list with site #1 live — and the controller's
   own node-adoption flow takes over for adding machines to that site.
```

### Step 3 mechanics — how detection works

Two discovery classes, two trust levels (deliberately consistent with the
node-adoption gesture language):

- **Physically attached module** (compute module in the hub chassis, or
  direct-cabled to the hub's provisioning port): enumerated over the hardware
  bus / link-local on a known interface. Possession of the chassis *is* the
  proof — **zero-touch adopt**, no code. This is the "bought them together"
  retail case; it should feel like the hub simply *has* a controller.
- **LAN-discovered controller** (pre-plugged server the user racked before
  running setup): an unconfigured controller boots into **setup-pending
  beacon** mode — mDNS `_traego._tcp` with `state=unclaimed` — and the hub
  lists it by hostname/specs. Adoption requires the controller's **claim code**
  (printed in its console log, same as human-driven setup). A random LAN
  device must never be silently absorbed.
- **Nothing found** → "Install a controller":
  - *on this hub* — if the hub hardware can host the controller module itself
    (UDM-style all-in-one), one click installs/enables it locally;
  - *on another machine* — show the `curl -fsSL https://get.traego.io | sh`
    one-liner with the org enroll token baked into the command; the wizard
    live-waits for the beacon to appear (same UX as "waiting for adoption").

### Provisioning (what "adopt" does here)

When the hub adopts a controller during setup it plays the role the human
played in controller-first setup, over one authenticated call:

- consumes the controller's **setup mode** (auth doc) via a **provisioning
  token** — valid only while the controller is unclaimed;
- writes: controller identity (stable ID), org membership + hub URL for
  outbound enroll/heartbeat, owner identity (the hub's owner), and the local
  recovery admin (generated, surfaced once in the wizard — the offline
  guarantee holds even for hub-provisioned controllers);
- the controller then starts its normal outbound enroll/heartbeat to the hub
  ([hosted-hub.md](./hosted-hub.md) data flows).

One rule kept absolute: **provisioning only works on an unclaimed
controller.** A controller with an owner can be *enrolled* into an org (its
owner approves from its own UI), but never silently re-owned by a hub.

### Auth in the wizard

No second login anywhere in the flow: the hub session carries through. The
hub-asserted session on the controller is the relay-SSO mechanism from the
auth doc (Phase 3) — hub-first setup is the reason that phase moves earlier
than "later": the wizard needs it for step 4/5 to feel continuous.

## What this adds to the build list

1. **Hub app** (`cmd/hub`): org + controller registry, enroll/report API,
   discovery + provision-forward endpoints. **Built** (`internal/hub`,
   `internal/hublink`); wizard UI pending.
2. **Controller: setup-pending beacon** — mDNS announce while unclaimed
   (`internal/discovery`), and a one-shot claim-code-gated provisioning
   endpoint (`internal/provision`) that persists the claim and starts the
   outbound hub link. **Built** — verified end to end: beacon → hub discovery
   → wrong code rejected → claim → enroll-back → claim survives restart.
3. **Wizard UI** on the hub over discovery/provisioning; attached-module
   auto-trust (the link-local provisioning channel) rides the same endpoint.
4. **get.traego.io installer** accepting a pre-baked org token (`sh -s -- join
   --org <token>`).

Build order: ~~hub registry/enroll (1)~~ → ~~beacon + provisioning (2)~~ →
wizard UI over discovery (3) → installer plumbing (4).
