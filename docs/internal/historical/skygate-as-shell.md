# Skygate as a shell (roadmap)

> **Status**: architectural roadmap, no code in v0.10.12. 2026-07-15.
> See `AGENTS.md` "Not done yet" → "Skygate-as-shell" for the
> short-form context.

## What is the "shell" idea?

Today, Skygate ships as a complete bundle: a single Go binary
plus a docker-compose stack that brings up headscale, optionally
Headplane, and optionally a DERP relay. The operator pulls the
bundle, runs `./deploy/deploy.sh`, and gets a working tailnet
portal.

The "shell" idea is to flip that around: Skygate becomes a
self-service portal you bolt onto an **existing** tailnet
operator stack. The operator runs Headscale, Headplane, and
DERP however they want — in Kubernetes, on multiple VPSes,
behind a load balancer — and Skygate provides:

- the user-facing exit-rule CRUD
- the per-user admin panel
- the Telegram bot
- the personal API tokens

…on top of the existing headscale API. No co-deployed sidecars,
no assumption that Skygate's docker-compose stack is the only
thing in town.

This unlocks three real-world deployments that the bundled
model can't serve well:

1. **Brownfield tailnets** — the operator already has
   Headscale + Headplane + DERP running, with a working ACL
   policy, devices, and users. Skygate shouldn't make them
   start over; it should plug into the existing data.
2. **Multi-cluster / multi-tenant** — different headscale
   control servers per region / per team. Skygate connects to
   each, presents a unified user portal.
3. **Air-gapped + restricted networks** — the operator runs
   their own bespoke stack (custom DERP, custom OIDC, custom
   ACL tooling) and just wants Skygate as the user-facing
   panel.

## What v0.10.12 already does in this direction

The first two layers are already in place; they just need to
be discovered by the operator:

- **A1 — `HEADPLANE_EXTERNAL_URL`** ([docs/headplane.md](headplane.md)):
  Skygate can point at an existing Headplane instead of
  starting a bundled sidecar. The /admin/acls view links to
  the existing UI.
- **A2 — `DERP_EXTERNAL_URLS`** ([docs/derp.md](derp.md)):
  Skygate can wire in third-party DERP relays (alongside, or
  instead of, the bundled one). No code on the operator's
  existing DERP nodes needs to change.

Both features are deploy-time toggles. The next step is
moving them to **runtime configuration** — admin can change
them through the web UI without re-running
`./deploy/deploy.sh`.

## Roadmap (versioned)

### v0.11.0 — admin-side runtime config

The two deploy-time toggles become web-UI editable:

- **/admin/derp/config** — list of external DERP URLs,
  editable inline. "Test connection" button per URL runs the
  same 5s probe `/admin/derp` already uses. Save → re-render
  headscale config → re-apply via `SetACL` / `SetConfig`.
- **/admin/headplane** — "Use bundled sidecar" / "Use
  existing instance" toggle + URL input for the existing one.
  Save writes to `global_settings` (same pattern as
  backup-config-ui from v0.10.6) and updates the dashboard
  link target.
- **/admin/integrations** — landing page that lists every
  "pluggable" component (DERP, Headplane, Headscale) with
  the current mode + a "Configure" button per row. The goal:
  a single page where an operator can see and change the
  wiring of their whole tailnet stack.

No new code architecture; just lifting the existing env vars
into `global_settings` and the existing deploy-time Python
rewriting into runtime re-renderers.

### v0.12.0 — pluggable headscale (multi-control-plane)

The biggest architectural change: Skygate stops assuming a
single headscale control server. Each `portal_user` row gets
a `headscale_url` + `headscale_api_key` column (instead of
the global `HEADSCALE_URL`); each bot / web request routes
to the right control plane based on the user's binding.

Use cases this unlocks:

- **Multi-region tailnets** — US users hit `head-us.example.com`,
  EU users hit `head-eu.example.com`. Skygate is the single
  portal for both, presenting a unified exit-rule catalogue.
- **Migration** — moving users from one control plane to
  another is a "change their headscale_url" admin action,
  not a redeploy.
- **Read-only audits** — the operator can give Skygate a
  read-only API key against a headscale they don't own
  (e.g. a customer's tailnet) and Skygate renders the
  device/rule view without write access.

The /admin dashboard gains a "Control planes" page listing
each plane + its user count + its health (last API call OK,
last error).

### v0.13.0 — ACL import / export (B+C in this doc)

The big one: when an operator starts using Skygate, they
already have a working headscale ACL policy (created via
Headplane, or hand-written, or whatever). Skygate's
`GenerateACL()` currently writes a **different** policy shape
(per-user isolation). Importing the existing policy as-is is
the missing piece for "plug in without breaking anything".

The plan:

- **Import** — "Read current headscale ACL, parse it into
  Skygate's `device_rules` representation, write the rows."
  Best-effort: some hand-crafted policies (e.g. group
  nesting) won't round-trip perfectly; the import surface a
  diff and asks the operator to confirm.
- **Export** — "Generate the current Skygate-managed ACL
  as a hand-editable HuJSON file the operator can review
  in Headplane." Mirror of the import, in reverse. Useful
  for the operator to audit what Skygate is about to push
  to headscale.
- **Dry-run** — `/admin/exit-rules/preview` shows the next
  `GenerateACL()` output without applying it. Lets the
  operator sanity-check a rule change before it goes live.

This is the v0.13.0 step because the import requires the
multi-control-plane plumbing from v0.12.0 to be solid — you
need to import "into the right headscale" without ambiguity.

## What does NOT change

The user-facing surface (Telegram bot, /my/exit-rules, bot
i18n) stays exactly the same. The shell idea is purely about
how Skygate integrates with the operator's stack, not about
how users see Skygate. A user on a Skygate-as-shell install
sees the same `/my_rules` page, the same `/add_rule`
workflow, the same Telegram bot as on a bundled install.

## Relationship to Headplane

Skygate and Headplane are complementary in the shell model:

- **Headplane** = the operator's cockpit. Visual ACL editor,
  machine management, OIDC, route approval. The thing you
  reach for when you need to *see* or *change* the tailnet's
  structure.
- **Skygate** = the user's portal. Exit-rule CRUD, preauth
  key issuance, the Telegram bot. The thing the user reaches
  for when they need to *use* the tailnet.

In a shell install, Headplane runs wherever the operator
wants it (often on the operator's workstation, not on a
server), and Skygate just embeds links to it from the
relevant admin pages. The two don't share sessions, data, or
processes — they each talk to headscale independently, the
same way they do today.

## Open questions

These are deliberately not decided yet; the plan above
captures the direction but not the details:

- **Auth model for multi-control-plane** — if Skygate
  manages 5 headscale instances, does the operator need 5
  service accounts, or does headscale gain a
  cross-instance admin token in some future version? We
  assume per-instance tokens for v0.12.0.
- **Cloud discovery** — would an operator ever want Skygate
  to *find* their headscale via DNS / Tailscale SSH
  magic-net? Tempting but the deployment story gets murky
  fast; we keep it explicit (URL + key per plane) for v0.12.0
  and revisit.
- **Per-plane backup** — when there are multiple control
  planes, does each get its own backup artifact, or do we
  roll them into a single tarball with subdirectories? The
  current `deploy/backup.sh` is single-plane; the multi-plane
  variant is a v0.12.0 design question.
- **Tailscale ACL → Skygate device_rules mapping** — the
  shape mismatch is real. A Tailscale group `group:devs` is
  not the same as a Skygate `device_rules` row. v0.13.0 will
  need a clear "this maps to N Skygate rules" surface so the
  operator isn't surprised by a 1:1 import.

## See also

- `docs/headplane.md` — the existing integration contract that
  v0.11.0 will lift into the web UI.
- `docs/derp.md` — same, for the DERP side.
- `docs/deploy.md` — the deploy story that v0.11.0 starts
  moving away from.
- `AGENTS.md` "Not done yet" — the live list of deferred
  items, including this one.
