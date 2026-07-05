# Hosted multi-site hub (deferred — design note)

> Status: **design, not built.** Captured so we can build the local-first system
> first and add this layer later. See [local-first-design.md](./local-first-design.md)
> for what exists today.

## Goal

A hosted "sites" page, the way UniFi's Site Manager (unifi.ui.com) works: one
place that lists **all sites across all controllers** and connects *down* into
each individual on-prem site for live data and management.

## The key insight: UniFi's cloud is deliberately thin

- The **hosted plane** holds only: the account/org, a **registry** of
  controllers & sites (names, status, last-seen), and a **relay**. It is *not*
  the source of truth.
- Each on-prem console keeps a **persistent outbound connection** up to the
  cloud. Outbound ⇒ works behind NAT/firewall with **zero inbound rules**.
- The sites list is served from the pushed registry. Drilling into a site
  **relays** UI/API calls down the outbound tunnel to the on-prem controller.
  Real data never leaves the site; the cloud is an index + a pipe.

Why thin wins: cheap to host, works behind any firewall, on-prem stays
authoritative and keeps working if the cloud is down.

## Translating to Traego — the recursive pattern

The hosted model is the **same adopt/heartbeat pattern we already built, one
level up**:

```
node       ──announce / adopt / heartbeat──▶  controller    (built)
controller ──enroll / heartbeat (outbound)──▶  hub           (this doc)
```

A **hub** holds: org → controllers → sites registry + a relay. Each controller
**enrolls outbound** with an org token, pushes a periodic **summary** (its
sites, node counts, health), and keeps the socket open so the hub can relay
back down. Node identity, enrollment, and heartbeat all generalize directly,
including mTLS for controller↔hub auth.

## Two data flows (like UniFi)

1. **Replicated index** (eventual) — controllers push site/health summaries up;
   the hub serves the multi-site list fast and can still *show* an offline site.
2. **Relay** (live) — drilling into a site proxies API/UI through the
   controller's outbound socket, end-to-end authenticated. The hub is a dumb,
   authenticated pipe.

## Decisions (where we landed)

- **Relay transport:** a **WebSocket reverse-tunnel** — controller dials out,
  hub multiplexes browser requests down it. Lightest thing that works behind
  NAT, no VPN. (Site-to-site uses the customer's SD-WAN; only this site-to-cloud
  hop uses the tunnel.)
- **Hosted *and* self-hostable** — same binary in "hub mode." UniFi lets you
  self-host; for us it's also the **commercial wedge**: Traego runs a hosted
  Site Manager, and **BSL stops anyone else from offering a competing hosted
  Traego.** The cloud layer is where the business lives.
- **Thin index + relay, not full replication up** — source of truth stays
  on-prem.

## What it needs from the local-first system

1. **Controller identity** — a stable, persisted controller ID + the org it's
   enrolled in. (Persistence now makes this possible.)
2. **Report-summary capability** — the outbound version of `GET /sites`.
3. **Namespaced site IDs** — today `local`/`branch` are per-controller; under a
   hub aggregating many controllers, sites need org/controller scoping to avoid
   collisions. Cheap to fix while the schema is young.

## First build step (when we pick this up)

Controller identity + outbound `enroll`/heartbeat to a hub stub (push the site
index up) — the `announce/adopt` pattern one level up, enough to light up a
hosted multi-site *list*. The **relay** (live drill-down) is the larger second
piece.
