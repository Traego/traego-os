# Traego OS — management interface prototype

A Vue 3 SPA prototype for **Traego**, a software-plus-hardware company built on a
simple bet: as building software gets cheap and AI becomes a great DevOps manager,
businesses move app development back in-house. The first product is the **Traego T2**,
a 2U blade chassis you fill with slot-in modules — managed the way UniFi manages a rack.

## Run it

```bash
npm install
npm run dev      # http://localhost:5173
npm run build    # production build to dist/
```

No backend — all data is mocked in a Pinia store and jitters live so the UI breathes.

> For where this goes beyond the prototype, see **[ARCHITECTURE.md](./ARCHITECTURE.md)** —
> a proposed software-first, open-source, container-heavy deployment model where you run a
> one-line script to add any machine to the pool.

## What's in here

**The rack.** A UniFi-style rendering of the T2 chassis (`RackChassis.vue` + `BladeModule.vue`):
8 blade bays, live activity LEDs, clickable modules, and empty bays you can slot new gear
into. Open **Hardware**, click an empty bay, pick a module — Atlas (the agent) provisions it
into the rack in real time and logs the action.

**The modules** (the "gear", defined in `stores/system.js → CATALOG`):
- **Controller** — control plane + agent host. Two installed for HA (auto-failover).
- **AI Inference** — versioned AI OS, model library, usage telemetry, benchmarks, end-user feedback.
- **Storage** — auto-provisioning NVMe pools on the 10GbE backplane, attached to DBs/apps.
- **App Pool** — self-serve "build a SaaS" compute with slot-based DB/cache workers.
- **Database** — optional dedicated module (shown as an installable upsell when absent).

**The software focus** (the manager's job):
- **Atlas, the AI DevOps agent** (`/agent`) — monitors every module, auto-resolves most
  events, recommends actions, and exposes an autonomy dial (observe / supervised / autonomous).
- **Adaptive UI** — the **Guided / Pro** toggle (top bar) changes how much detail every page
  shows. Atlas tailors the interface to the operator's technical level.
- **Updates** — versioned firmware/software across modules with zero-downtime rollout.
- **Storage usage**, **model performance & feedback**, all surfaced on the dashboard.

## Structure

```
src/
  stores/system.js        # mock system state, module catalog, live simulation
  components/
    RackChassis.vue       # the 2U rack rendering
    BladeModule.vue       # a single slotted blade with LEDs
    AddModuleDrawer.vue   # slot-in-a-module flow
    AppSidebar / AppTopbar / Icon / Sparkline / AgentNote
  views/                  # Dashboard, Hardware, AI, Controller, Storage,
                          # AppPool, Database, Updates, Agent
```

It's a prototype: state is in-memory and resets on reload.

## License

Traego OS is **source-available** under the [Business Source License 1.1](LICENSE).

You may read, modify, self-host, and make production use of the code freely. The
one thing you may **not** do is offer Traego (or a derivative) to third parties as
a competing hosted or managed service — see the Additional Use Grant in the
[LICENSE](LICENSE). Each release automatically converts to the **Apache License,
Version 2.0** on its Change Date (four years after publication).

For commercial licensing outside these terms, contact licensing@traego.com.
