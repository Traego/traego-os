# Login & first-boot auth (design)

> Status: **design, not built.** Companion to
> [hosted-hub.md](./hosted-hub.md) (traego.ai is the "hub" referenced here),
> [hub-first-setup.md](./hub-first-setup.md) (the retail flow: a hub discovers
> and provisions controllers — it consumes the setup mode defined here), and
> [local-first-design.md](./local-first-design.md). Today the only human auth
> is `TRAEGO_ADMIN_KEY` pasted into the topbar — fine for dev, wrong for the
> product.

## Goal

UniFi-style sign-in: the **traego.ai account is the front door** (one identity
across every controller you own, enables the hosted hub + relay), but a
**direct local login always works** — on the LAN, with the cloud down, forever.
First boot prompts you to pick.

## The UniFi lesson we copy — and the one we don't

**Copy:** cloud account first in the UX. UniFi's setup wizard leads with
"Sign in with your Ui.com account"; local-only is behind "Advanced setup."
The cloud identity is what makes multi-site (unifi.ui.com → relay into a
console) feel like one product. That maps 1:1 onto our hub design.

**Don't copy:** UniFi shipped consoles where the *only* admin was the cloud
account, and during ui.com outages people couldn't log into hardware in their
own house. They later walked it back. Our rule from day one:
**every controller has a working local credential, even when the owner chooses
the cloud path.** The cloud is a convenience layer over local auth, never a
dependency of it.

## First-boot setup wizard

The controller boots into **setup mode** when no owner exists: every endpoint
except `/healthz`, the setup API, and the node join plane returns
`409 setup-required`, and the SPA routes to `/setup`. While unclaimed it also
beacons over mDNS so a hub can discover and provision it — the wizard below
and hub provisioning are two consumers of the same one-shot setup mode (see
[hub-first-setup.md](./hub-first-setup.md)); whichever claims it first, gated
by the claim code, wins.

```
┌─────────────────────────────────────────────┐
│  Welcome to Traego                          │
│  Set up the owner of this controller.       │
│                                             │
│  ┌───────────────────────────────────────┐  │
│  │ ⚡ Sign in with your Traego account   │  │   ← primary
│  └───────────────────────────────────────┘  │
│  Multi-site dashboard, remote access,       │
│  backups. Works with the traego.ai hub.     │
│                                             │
│  ───────────── or ─────────────             │
│                                             │
│  Set up a local admin instead →             │   ← secondary, plain link
└─────────────────────────────────────────────┘
```

- **Cloud path** → OAuth **device flow** (below) → owner account created,
  linked to the traego.ai identity → **then the wizard still generates a local
  recovery admin** (`recovery` + a one-time-displayed generated password,
  "store this somewhere safe — it works when the internet doesn't"). This is
  the deliberate divergence from UniFi.
- **Local path** → username + password (argon2id) → owner created. A "link
  Traego account later" nudge lives in Settings.
- **LAN-race hardening:** setup mode prints a 6-character **claim code** in the
  controller log/console; the wizard asks for it before creating the owner.
  Same physical-access proof as node pairing codes — one gesture language
  everywhere.
- Offline at first boot? The cloud button explains and falls through to the
  local path. Setup never requires internet.

## Why the device flow (RFC 8628), not redirect OAuth

A controller lives at `http://192.168.x.x:8443` — there is no stable, public
redirect URI to register, and (post-TLS-default-change) often no HTTPS. The
device grant sidesteps all of it:

1. Controller (outbound) asks traego.ai for a device code.
2. UI shows: **"Go to traego.ai/activate and enter `WDJB-MJHT`"** (and a QR).
3. User authenticates on traego.ai — their browser, their password manager,
   their 2FA — nothing brand-new to trust on the LAN.
4. Controller polls, receives tokens, stores the link (org + account id +
   refresh token, encrypted at rest in bbolt meta).

Outbound-only, NAT-proof — the same shape as node announce/adopt and the
planned controller→hub enrollment. The pairing-code gesture users learn at
adoption is the same gesture that links their cloud account.

## Login screen (after setup)

```
┌─────────────────────────────────────────────┐
│  traego · sign in                           │
│                                             │
│  ┌───────────────────────────────────────┐  │
│  │ ⚡ Sign in with Traego account        │  │   ← primary when linked
│  └───────────────────────────────────────┘  │
│  ───────────── or ─────────────             │
│  username  [____________]                   │
│  password  [____________]                   │
│  [ Sign in ]                                │
└─────────────────────────────────────────────┘
```

- Both paths always visible (UniFi does the same). If traego.ai is
  unreachable: the button disables with "traego.ai unreachable — local sign-in
  still works." No dead ends.
- Cloud button on a linked controller runs the device flow inline (or, once
  the hub relay exists, a hub-side session can open the controller directly —
  remote access is a hub feature, not a login-page feature).
- The public `/chat` page stays unauthenticated (it's the end-user surface);
  only the admin console is gated. Optional later: chat access codes.

## Sessions, accounts, tokens

- **Browser sessions:** HTTP-only cookie, `SameSite=Lax`, 30-day sliding
  expiry; state-changing requests additionally require a custom header the SPA
  always sends (CSRF belt-and-braces). Sessions live in bbolt → survive
  controller restarts (we just built that muscle for the AI module).
- **Local accounts:** bbolt `users` bucket — id, username, argon2id hash,
  role (`owner` | `admin` | `viewer`-later), created, last-login. Login
  failures feed the existing per-IP limiter; **successes must pass even when
  the IP is limited** (fail2ban semantics — see the NAT lockout we hit on
  2026-07-04 with the credential poll).
- **API keys replace `TRAEGO_ADMIN_KEY`:** named, hashed-at-rest keys managed
  in Settings ("CI deploy", "Home Assistant"…), sent as `Authorization:
  Bearer`. The env var stays working as a bootstrap key named `env` with a
  deprecation note in the log. The topbar "set admin key" pill becomes an
  account menu (user, sign out, manage users & keys).
- **Machine plane untouched:** join tokens, pairing codes, enroll secrets, and
  the :8444 mTLS plane are node identity, not human identity. Different doors,
  different keys.

## Recovery

- **Local:** `controller reset-password <username>` subcommand — proves host
  access, prints a new one-time password. Gentler than UniFi's factory reset.
- **Cloud:** standard traego.ai account recovery; the linked controller
  honors the refreshed identity.
- **Recovery admin** (from the cloud-path wizard) is the "internet is down and
  I forgot everything" answer.

## What it needs / build order

1. **Phase 1 — local auth (no cloud dependency):** users bucket + argon2id,
   session cookies, setup wizard (local path + claim code), login page, API
   keys page, admin-key back-compat. Replaces the admin-key UX outright.
2. **Phase 2 — traego.ai link:** device-flow client in the controller, account
   linking in Settings + wizard cloud path, cloud button on the login page.
   Needs the hub's identity service (OAuth AS + `/activate`) from
   [hosted-hub.md](./hosted-hub.md) — this is the hub's first concrete API.
3. **Phase 3 — hub SSO/relay:** traego.ai session → site list → relayed,
   hub-asserted session on the controller (the relay carries an org-scoped
   assertion the controller trusts from enrollment).

Phase 1 is self-contained and immediately deletes the worst current UX (a
bearer token in a `window.prompt`). Phases 2–3 ride on hub work that's already
planned.
