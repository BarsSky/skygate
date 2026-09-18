# ACL rules reference

This document explains how skygate builds, applies, and reconciles
the Tailscale ACL that headscale enforces. It's written for the
operator who needs to understand the current logic — to add a
rule, debug a "why is this denied?", or audit who can reach what.

If you're a new operator and just want to add a rule, jump to
[§5 — Working with /admin/acls](#5-working-with-adminacls).

If you're debugging a "denied" symptom, start at
[§7 — Common gotchas](#7-common-gotchas).

---

## 1. The big picture

Skygate doesn't store its own ACL policy in headscale directly.
Instead, it builds a **hujson** ACL (headscale's superset of HuJSON
JSON) every time `/admin/acls` is saved, and ships it to headscale
via `SetPolicy`. The pipeline is:

```
operator edits a rule on /admin/acls
   │
   ▼
ApplyACLPipelineForPlane (internal/acl/acl.go)
   │
   ├─ 1. GenerateACL  — build the hujson string from portal_rules
   │                    + portal_users + nodes
   ├─ 2. SaveACLSnapshot — persist the generated hujson in
   │                      global_settings[acl.policy.hujson]
   ├─ 3. SetPolicy (HS API) — push the hujson to headscale
   └─ 4. Mark + Log — audit row + cache invalidate
```

The pipeline is intentionally narrow: it does the 4 DB+HS steps and
nothing more. Caller-specific side effects (Telegram alert, advertised
routes sync) stay at the call site because some callers (the Telegram
bot) skip them while the web form does both.

The ACL itself is standard Tailscale ACL syntax — see the official
[Tailscale ACL docs](https://tailscale.com/kb/1018/acls/) for the
field-level reference. This document focuses on what **skygate does
on top** of that: how rules are stored, how they're grouped, and the
non-obvious defaults.

---

## 2. Rule shape (what you can add on /admin/acls)

A rule in skygate has these fields:

| Field | Required | What it means |
|---|---|---|
| `name` | yes | Display name (must be unique per portal user) |
| `action` | yes | `accept` (allow) or `deny` (block) |
| `src_user` | yes | The portal user (or `*` for everyone) whose outbound traffic this rule applies to |
| `src_device` | optional | A specific device hostname under `src_user`. Empty = "any device of this user" |
| `dst_user_or_tag` | yes | A portal user, a tailnet tag (`tag:foo`), or `*` |
| `dst_ports` | yes | Either `*:*` (any) or a comma-separated list of `port` or `host:port` |

**Tags** are how you grant cross-user access without naming specific
devices. Every skygate-managed device gets a `tag:dev-<portal_username>-<device_hostname>` tag automatically (via the
node-ownership reconciler — see `internal/nodeownership/`). Other
commonly-used tags:

- `tag:private` — internal-only traffic (skygate adds this by default)
- `tag:exit-node` — devices that other clients can route through
- `tag:infra` — infrastructure nodes (operator-managed)

The full tag set is in `tagOwners` (see §3).

### What's NOT a field here

- **Protocol** — all rules are TCP+UDP. There's no "TCP only" toggle.
  If you need that, use `dst_ports` to constrain to TCP-only ports
  (e.g. SSH = 22) and use `deny` rules to exclude UDP variants.
- **Wildcard hostname** — `dst_user_or_tag=*` matches everyone
  (port-agnostic). If you want "any device of user X", list each
  hostname explicitly or use `src_device=*`.
- **Time-based rules** — headscale ACL doesn't support schedules.
  Use the bot or the API for ephemeral rules.

---

## 3. `tagOwners` — who can apply which tag

headscale's `tagOwners` map declares **which users can apply which
tag to their nodes**. Without an entry in `tagOwners`, a user can't
self-tag — `headscale nodes tag` will reject with
`InvalidArgument: requested tags are invalid or not permitted`.

skygate maintains `tagOwners` automatically:

| Tag pattern | Owner |
|---|---|
| `tag:dev-<USER>-*` | `<USER>@tsnet.example.com` (the user owns their own device tags) |
| `tag:dev-infra-<DEVICE>` | `infra@tsnet.example.com` (infra user owns infrastructure device tags) |
| `tag:exit-<DEVICE>` | `infra@tsnet.example.com` |
| `tag:private` | `*` (every user can self-apply this) |

So if `alice` adds a new device `laptop-2`, the auto-reconciler
applies `tag:dev-alice-laptop-2` to it because alice owns the
`tag:dev-alice-*` pattern in `tagOwners`.

If you need a new tag pattern (e.g. `tag:dev-team-design-*`),
add the owner to `tagOwners` first via `/admin/acls` → "Edit
tagOwners" → "Add owner".

**Gotcha**: when a tag pattern is changed in `tagOwners`, headscale
doesn't re-tag existing nodes — you have to manually run
`headscale nodes tag --force <node-id> <tag>` for any node that
should have the new tag. The auto-tag only fires for NEW nodes or
for nodes that re-register.

---

## 4. Per-device grants (the per-user ACL layer)

The standard `tagOwners` + `acls` model grants access at the **user**
level — "alice can reach tag:dev-bob-laptop". But many real
deployments need **device-level** control — "alice's exit-node can
reach ALL devices of bob, but alice's laptop can only reach bob's
laptop".

skygate implements this via a second layer: per-device grants. Each
portal user can register a "device grant" that says "my device X can
reach the following (user, device) pairs":

```
/admin/exit-rules → "Add device grant"
  src_user: alice
  src_device: exit-node        ← only this device
  dst: [
    { user: bob, device: laptop, ports: 22 },
    { user: bob, device: *     , ports: * },
    ...
  ]
```

These grants are written into the hujson policy as a separate
`acls: [...]` block AFTER the user-level rules. headscale evaluates
them with **last-wins** semantics — the per-device grant can narrow
the broader user-level grant, but never widen it.

### Per-device grant ordering

skygate sorts per-device grants by:
1. Most specific first (longest tag pattern)
2. Then by `src_user` ascending
3. Then by `src_device` ascending

This is deterministic across regenerations — the same set of grants
in any order produces the same hujson output.

### Dead rules

If a per-device grant references a device that's been deleted, the
rule is marked "dead" (greyed-out in `/admin/exit-rules`) but kept
in the policy. Removing a dead rule requires explicit operator
action. The rationale: the operator might be in the middle of
re-creating the device, and the rule should re-activate
automatically when the new device appears.

---

## 5. Working with /admin/acls

The `/admin/acls` page is the primary authoring surface. It's a form
with:

- A **rule list** (the `acls: []` block) — add/edit/delete rules
- A **tagOwners editor** — add/remove owners for tag patterns
- A **"Generate" button** — manually re-runs the pipeline (rarely
  needed; auto-saves trigger it)
- A **"Dry-run" button** — shows the hujson that WOULD be pushed,
  without pushing it. Use this to verify before applying.

### Save flow

When you click "Save":

1. Form POST → `/admin/acls/save`
2. `ApplyACLPipelineForPlane` runs (§1)
3. On success: green flash + audit row + Telegram alert (if notifier
   is configured)
4. On failure: red flash + error message + no audit row

If the save fails, the previous hujson stays in headscale (the
pipeline is atomic — either the new hujson is in place everywhere,
or the old one is).

### Reading the generated hujson

Click "Dry-run" to see the exact hujson headscale will receive. It's
re-indented and color-coded so you can spot mis-grouped rules.

Three things to look for:

1. **The `tagOwners` block** — verify your new tag pattern + owner
   is there. If not, your grant won't work.
2. **The `acls: []` ordering** — first match wins for `accept` rules
   but `deny` always wins over `accept` regardless of position.
   Order matters for the auto-generated catch-all (see §6).
3. **The auto-generated exit-node catch-all** — every exit-node
   device gets a `tag:exit-<HOST>` AND a `tag:dev-infra-<HOST>`. The
   ACL includes a default `accept` rule that lets `tag:exit-*`
   reach all members. If you want a more restrictive exit-node
   policy, edit the catch-all (it's a special row in `/admin/acls`).

---

## 6. The auto-generated defaults

Every time the pipeline runs, it injects a few default rules so
that the system "just works" out of the box:

| Default rule | Why it's there |
|---|---|
| `* → tag:private:*:*` (accept) | Internal traffic between all skygate devices |
| `* → tag:exit-*:*:*` (accept) | Anyone can route through any exit-node (the "I need to reach the internet via this VPN" case) |
| `tag:dev-infra-* → *:*:*` (accept) | Infrastructure devices can reach anything (for monitoring + DERP relays) |
| `tag:dev-<USER>-* → tag:dev-<USER>-*:*:*` (accept) | A user's own devices can reach each other (the "my laptop talks to my phone" case) |

These are added at the END of the `acls: []` block. You can override
any of them by adding a more specific `deny` rule ABOVE them in the
form.

### Subnet router rules

When a device advertises a subnet route (e.g. `192.168.1.0/24`), the
pipeline adds:

- `tag:dev-<USER>-<DEVICE> → 192.168.1.0/24:*` (accept) — the
  subnet is reachable from this device
- `* → 192.168.1.0/24:*` (deny, unless you accept) — the default
  for "everyone else". To allow other users to reach the subnet,
  add an explicit `accept` rule above this deny.

The deny-by-default is what makes subnet routers safe by default —
without an explicit grant, no other user can reach the routed
subnet.

---

## 7. Common gotchas

### "I added a rule but it's still denied"

1. Check `/admin/acls` → "Dry-run" — is your rule actually in the
   generated hujson? If not, the form might have a validation
   failure (check the URL hash for an error message).
2. If it IS in the hujson, headscale might have rejected it. Check
   `headscale logs` for a parse error.
3. If headscale accepted it, check `tagOwners` — your rule might
   reference a tag that no one owns. Without an owner, headscale
   silently drops tags that don't apply.
4. If the tag is owned but the device doesn't have the tag yet,
   wait for the auto-tagger (next tick, ≤5 min) OR run
   `headscale nodes tag --force <node-id> <tag>` manually.

### "I removed a rule and now everyone's denied"

Most likely cause: the rule you removed was the catch-all for some
default group (see §6). Without it, headscale defaults to deny.

Fix: re-add the rule, OR add a more specific rule that covers the
denied group.

### "Per-device grant doesn't fire for user X"

The per-device grant is anchored on `src_user` (the portal user who
owns the grant). If `X` is the source device owner, the grant
should fire. If `X` is the destination device owner, the grant is
on the WRONG side — switch to a rule where `src_user=X`'s portal
account is the source.

### "Subnet router is reachable from everyone but not from user X"

Default subnet-routing is deny-by-default for non-owners. Add an
explicit `accept` rule:

```
src: tag:dev-<X>-*
dst: <SUBNET_CIDR>:*:*
action: accept
```

### "headscale accepts the ACL but tailnet clients can't see each other"

This is the "headscale netmap gotcha" (documented in
`docs/ha.md`). Grants-based
policy does NOT include user-owned (un-tagged) nodes in other nodes'
peer list, even when grants formally allow the traffic. Only tagged
nodes appear in the netmap.

Fix: pre-tag new nodes with `tag:dev-<USER>-<DEVICE>` AND make
sure the matching `tagOwners` entry exists. The auto-tagger does
this for new nodes, but legacy un-tagged nodes need a manual
`headscale nodes tag --force` once.

### "ACL is huge, takes forever to render on /admin/acls"

The hujson is re-generated and re-rendered on every page load (it's
pulled from `global_settings[acl.policy.hujson]`). For very large
policies (1000+ rules), the page can take 5-10s.

Fix: archive unused rules via `/admin/acls` → "Archive" (a soft-delete
that hides them from the form but keeps them in the audit trail).

---

## 8. Where the ACL lives

| Storage | Key | Contents |
|---|---|---|
| `global_settings` (PG) | `acl.policy.hujson` | The latest generated hujson (last applied to headscale) |
| `portal_rules` (PG) | (table) | Per-user rules as edited on /admin/acls (source-of-truth for the next generation) |
| `device_rules` (PG) | (table) | Per-device grants (separate source-of-truth, merged into the final policy) |
| `node_owner_map` (PG) | (table) | Maps device hostname ↔ portal user ↔ headscale node ID (used by the auto-tagger) |
| headscale | (in-memory) | The live ACL. Synced from `acl.policy.hujson` on every `SetPolicy` call. Not persisted by headscale. |

If you need to recover the live ACL after a headscale restart, the
pipeline re-runs from the PG tables. You don't need a headscale
backup of the policy.

---

## 9. Pipeline hooks (advanced)

`ApplyACLPipelineForPlane` is the single entry point. It takes a
`Plane` (admin or user scope) and an optional `Alerter`. Both web
form, bot, and (in v1.5.x) the Telegram relay command all call
this same function.

If you need to add a new caller (e.g. a CLI tool that edits rules),
import `internal/acl` and call:

```go
err := acl.ApplyACLPipelineForPlane(ctx, deps, acl.PlaneAdmin, &alerter)
```

Don't reimplement the 4 steps inline — the ordering (Generate →
Save → SetPolicy → Mark+Log) is order-sensitive for a reason (see
the comment in `internal/acl/acl.go`).

---

## 10. Where to read the code

| File | What it contains |
|---|---|
| `internal/acl/acl.go` | The pipeline (`ApplyACLPipelineForPlane`), hujson generation, tag parsing |
| `internal/acl/acl_perdevice.go` | The per-device grant layer |
| `internal/acl/acl_b188_3_integration_test.go` | E2E tests for the pipeline + headscale round-trip |
| `internal/feature/exit_rules/` | The `/admin/exit-rules` UI + handlers |
| `internal/nodeownership/` | The auto-tagger that applies `tag:dev-<USER>-<DEVICE>` to new nodes |
| `internal/handlers/templates/admin/acls.html` | The `/admin/acls` form template |
| `scripts/check_b111.sh` | B-check for `tagOwners` integrity |
| `scripts/check_b188_*.sh` | B-checks for the per-device grant layer |

If a behaviour isn't documented here, the source is the next
authority — open the file, read the function, look at the test.
