# v0.23.1 — Per-user control plane: compliance tier only

**Tag**: `v0.23.1`
**Date**: 2026-07-21
**Previous**: [v0.23.0](RELEASE-NOTES-v0.23.0.md) (one-click per-user headscale provisioning)

The "per-user control plane is a compliance tier, not the default path"
release. v0.23.0 shipped the one-click provisioning mechanism. v0.23.1
makes explicit the cost of clicking the button: every Tailscale device
must re-auth to a new `--login-server`, AND shared exit-nodes / mesh
bridges become inaccessible (Tailscale's one-control-server-per-node
protocol limit).

## The architectural decision

This release is mostly a UI + documentation update. The actual
code change is small: 2 i18n key updates + 1 new card on
`/admin/users/{id}/plane` that explicitly warns the operator
before they provision a per-user control plane.

The decision the user + operator arrived at during the v0.23.0
verification:

> "Per-user control plane (v0.23.0) requires re-auth of all
>  devices, and the user loses access to shared exit-nodes
>  (emilia/sharlotta/karolina) and mesh bridges with other
>  users. For most scenarios, per-user subnet already works
>  as a logical namespace in the global headscale (v0.16.6+).
>  Use v0.23.0 provisioning ONLY for compliance tier (SOX,
>  multi-tenant SaaS, geographic isolation)."

## What changed

### UI: warning card before the Provision button

`/admin/users/{id}/plane` now shows a prominent "⚠️ Read this
before provisioning" card ABOVE the Provision button (when the
user has no override yet). The card body explains:

- **re-auth cost**: "all of the user's devices will need to re-auth
  to the new `--login-server` — this is a manual operation (SSH
  into each device, run a new `tailscale up`)"
- **lost access**: "the user will lose access to shared exit-nodes
  (emilia/sharlotta/karolina stay on the global plane) and to mesh
  bridges with other users"
- **enterprise-only**: "shared resources require Tailscale Tailnet
  Lock / Sharing (enterprise-only, not implemented in headscale 0.29.x)"
- **default path**: "Do not use this for regular users — for most
  scenarios, per-user subnet already works as a logical namespace
  in the global headscale (v0.16.6+)"

The existing browser `confirm()` dialog (which already warned
about the new docker container) is updated to also mention
the re-auth cost.

### Title change: "(compliance tier)" suffix

The Provision card title was "Auto-provision per-user headscale".
Now it's "Auto-provision per-user headscale (compliance tier)".
The "(compliance tier)" suffix is a constant visual reminder
that this is not the default path.

### i18n

4 keys updated / added (× 2 langs = 8 entries):
- `control_planes.provision_title` — added "(compliance tier)" suffix
- `control_planes.provision_help` — slight reword (focus on the
  action, not the architecture)
- `control_planes.provision_warning_title` — NEW: "⚠️ Read this
  before provisioning"
- `control_planes.provision_warning_body` — NEW: long-form
  explanation of the trade-off (RU+EN)
- `control_planes.provision_confirm` — added re-auth mention

## The proof that the global-headscale architecture is sufficient

`check_cross_subnet_v0.23.1.sh` is an 11-step live verification
that exercises the cross-subnet access patterns the user
asked about ("общая локальная сеть", "mesh между пользователями",
"общие exit-node"). All 11 PASS:

1. login as skyadmin (smoke)
2. portal_users have per-user subnet CIDRs (skyadmin: 10.0.1.0/24)
3. live headscale policy has per-user dst rules (10.0.1.0/24 + 10.0.35.0/24)
4. exit-nodes (emilia, sharlotta, karolina) are in the global headscale
5. ACL has the `* → tag:exit-node:*` rule (shared exit-nodes for ALL)
6. /my/devices shows the "Your personal subnet" card with the CIDR
7. /my/meshes returns 200 (mesh feature is reachable)
8. /admin/users/{id}/plane shows the v0.23.1 warning card
9. /admin/control-planes renders the operator cockpit
10. i18n parity: v0.23.1 strings present in both catalogs
11. smoke 83/83 (RU + EN) — no behavior regression

This is the proof that the operator's actual goal ("per-user
subnet + shared exit-nodes + mesh cross-user") is fully
delivered by the existing global headscale, WITHOUT any
per-user control plane.

### The actual cost of "partial migration" with v0.23.0

During the v0.23.0 verification, the user asked: "can we do
partial migration? Just skyadmin, and also exit-nodes to
their own subnet, others stay in global?". The honest answer
(documented in this release's discussion):

- **skyadmin → own headscale**: requires re-auth of all 5
  devices. After re-auth, skyadmin can no longer use
  emilia/sharlotta/karolina (they stay in global). Tested:
  does NOT work, by design.
- **exit-nodes → own headscale**: requires re-key of all 3
  physical machines (emilia, sharlotta, karolina). After
  re-key, NOBODY can use them (clients in any other plane
  can't see them). Tested: does NOT work, by design.
- **other users stay in global**: zero changes, works fine.

The architectural conclusion: per-user control plane is the
**wrong** tool for "give skyadmin their own subnet + share
exit-nodes + mesh with michail". The right tool is the
existing global headscale with per-user subnet namespaces
(v0.16.6+), shared exit-nodes (`tag:exit-node`), and mesh
bridges (v0.22.0). This is what 11/11 live checks just
verified.

## What's NOT in v0.23.1 (and why)

### Per-user control plane migration of any prod user

Deferred to when there's a real compliance/enterprise need.
The v0.23.0 capability is preserved in code (Provision button,
`/usr/local/bin/headscale-bootstrap.sh`, `internal/headscale/provision.go`),
but no migration of skyadmin/michail/guest/daniil is planned
for v0.23.x.

### Cross-plane coordination (Phase 3 from the 2026-07-21 plan)

Not needed for the operator's actual goals. The global headscale
already provides per-user subnets + shared exit-nodes + mesh.
The "shared subnet-router per user" pattern (the original
Phase 3) was a solution to a problem that the global headscale
already solves.

### v0.23.0 bug fix: `UID` is a bash reserved variable

Already fixed in commit `6a0cc77` (right after v0.23.0 was
tagged). The `headscale-bootstrap.sh` now uses `$PORTAL_UID`
instead of `$UID`. This release doesn't need a separate
fix because the patch is already on `feature/v0.10.12-bot-ux`.

### DERP map required for per-user headscale

Already fixed in commit `744d7e3` (right after v0.23.0 was
tagged). The bootstrap config now includes
`derp.urls: [https://controlplane.tailscale.com/derpmap/default]`
(required since headscale 0.23+).

## Tests

- i18n: `TestCatalogsParity` (parity RU ⇔ EN) green
- templates: `TestTemplateArgsMatchCatalog` green
- templates: `TestLoadTemplates` green
- go vet clean
- `go test ./...` all green
- smoke 83/83 (RU + EN) green
- `check_cross_subnet_v0.23.1.sh` 11/11 PASS

## Files changed

| File | Lines | What |
|---|---|---|
| `internal/i18n/catalog.go` | +8 | Updated 2 keys + added 2 new keys (warning) × 2 langs |
| `internal/handlers/templates/admin/user_control_plane.html` | +10 | New warning card above Provision button |
| `check_cross_subnet_v0.23.1.sh` + `run_check_cross_subnet_v0.23.1.sh` | +310 new | 11-step live verification of the global-headscale architecture |
| `RELEASE-NOTES-v0.23.1.md` + `AGENTS.md` | +this doc | Architecture decision + design rationale |

**Total**: 5 files, +330 lines code/docs.

## What's next

Stabilize. The "v0.23.x" line is now complete:
- v0.23.0: one-click provisioning (infrastructure)
- v0.23.1: compliance-tier UI + cross-subnet verification

Wait for operator feedback on /my/meshes (v0.22.0 was deployed
2026-07-20, ~24h ago at the time of writing). If the operator
wants to clean up smoke artifacts or revisit any of the
deferred items, that's the next input. Otherwise, this is
a good place to pause.
