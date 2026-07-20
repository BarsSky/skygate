# v0.22.2 — Fix auto-apply tag:private for tagless nodes (MSI bug)

> Released: 2026-07-20
> Build: 9931e1a
> Predecessor: v0.22.1 (/my/meshes web UI)

A small, targeted hotfix. The user reported that **MSI**
(id=15), which was registered via the skygate preauth flow
(`/my/preauth` → preauth id=98), never received the
`tag:private` tag in headscale. The snapshot row in
skygate's `node_owner_map` had `tag="tag:untagged"` and
the headscale API reported `tags=[]` for the node.

This is the second of two `backfillNodeOwnership` bugs
(the first — Strategy C — was fixed on 2026-07-10 but
Strategy A had the same shape and was missed).

## What ships

1. **`backfillNodeOwnership` Strategy A fix** —
   when the preauth key was issued by skygate (matched
   via `preauth_keys.headscale_preauth_id`) AND the
   headscale node has no tags yet (`len(n.Tags) == 0`),
   `matchedTag` now defaults to `"tag:private"` instead
   of `firstTagOrFallback(n)` (which returns
   `"tag:untagged"` for tagless nodes). This ensures
   the subsequent `if matchedTag == "tag:private"`
   branch fires and `HS.TagNode(15, "tag:private")` is
   actually called to push the tag to headscale.

   For nodes that ALREADY have tags (e.g. `skygate-vm`
   has `tag:private` from the v0.16.7 sidecar code),
   `firstTagOrFallback(n)` is still used to preserve the
   existing tag — no behavior change.

2. **Two unit tests** that pin the fix:
   - `TestBackfillHelper_StrategyA_TaglessNode_PinsV0222Fix` —
     verifies the helper chain produces
     `tag="tag:private"` for a Strategy A match on a
     tagless node, and the re-apply is idempotent
     (the snapshot blocks any subsequent
     `tag:untagged` rewrite).
   - `TestBackfillHelper_TaglessNode_NoUpgradeFromPrivate` —
     regression guard: a node that already has
     `tag=tag:private` (e.g. `skygate-vm`) is NOT
     downgraded by a re-backfill.

3. **Live validation** — `check_v0.22.2.sh` on the VM
   exercises the full path: pull → rebuild → call
   `/my/devices` as skyadmin → verify MSI's
   `tags=['tag:private']` in headscale → verify
   `node_owner_map.tag='tag:private'` for MSI → call
   `/my/devices` again → verify idempotency (snapshot
   stable, headscale tag stable). All 8 checks PASS.

## Why this release matters

The v0.22.0 release shipped auto-apply `tag:private`
for new devices (the `/my/preauth` → `/my/devices`
flow). The auto-apply has TWO strategies:

- **Strategy A** (direct match): `n.PreAuthKeyID ==
  preauth_keys.headscale_preauth_id` — works for keys
  whose `headscale_preauth_id` was captured at issue
  time. The ORIGINAL path from v0.3.9 — fast and
  accurate, but vulnerable to API response shape changes
  (a preauth key issued when the response field name
  shifted will not have a stored `headscale_preauth_id`).
- **Strategy C** (temporal fallback): the user has at
  least one preauth key created within 1 hour BEFORE
  the node's `CreatedAt`. This recovers ownership for
  keys whose `headscale_preauth_id` was never captured.

Strategy C had a similar bug — fixed on 2026-07-10
(see the existing comment in the function around
the "matchedTag = tag:private" override). The fix
defaulted to `tag:private` for tagless nodes matched
temporally. But the SAME bug existed in Strategy A
and was missed.

The bug: for a freshly-registered node (tags=[] in
headscale), `firstTagOrFallback(n)` returns
`"tag:untagged"`. The subsequent branch check
`if matchedTag == "tag:private"` failed, so the
`HS.TagNode` push never fired. The snapshot row got
`tag="tag:untagged"`, which on the next backfill still
didn't trigger the upgrade (the function's
`if matchedTag == "tag:private"` check used the
freshly-computed matchedTag, which was still derived
from the empty tags). The node stayed tagless in
headscale forever.

For MSI specifically: skygate stored the preauth
(id=98, user_id=1=skyadmin, used=1), headscale stored
the preauth on MSI's node (pre_auth_key.id=98). The
backfill matched (Strategy A), computed matchedTag
from `firstTagOrFallback(MSI)` = `"tag:untagged"`,
inserted a `tag=untagged` snapshot row, and did NOT
call `HS.TagNode(15, "tag:private")`. The fix applies
to Strategy A the same override that Strategy C
already had.

## Design decisions

- **`tag:private` is the right default for skygate-issued
  preauth keys.** A node registered via `tailscale up
  --authkey=<skygate key>` is provably owned by the
  user who issued the key. Defaulting to `tag:private`
  is correct (and matches the Strategy C fix). The
  rare case of "skygate issued a key that the user
  passed to a Tailscale node of someone else" doesn't
  apply here (the `tagOwners.tag:private` policy
  already lists every portal user, so the tag is
  valid for any of them).

- **Existing nodes with `tag=tag:private` are
  unchanged.** The fix only changes the matchedTag
  computation for nodes with `len(n.Tags) == 0`. Nodes
  that already have `tag:private` in headscale
  (e.g. skygate-vm, which gets it from the v0.16.7
  sidecar) use `firstTagOrFallback(n)` which returns
  `"tag:private"` — same result as before. The
  `TestBackfillHelper_TaglessNode_NoUpgradeFromPrivate`
  test pins this non-regression.

- **Idempotency preserved.** The fix doesn't break the
  "re-backfill is a no-op" property: once the snapshot
  row is `tag=tag:private` AND `n.Tags = ['tag:private']`
  in headscale, subsequent backfills hit the
  `if !hasPrivate` skip branch (hasPrivate is true
  because the API now returns the freshly-applied tag).
  No second `HS.TagNode` call. The
  `TestBackfillHelper_StrategyA_TaglessNode_PinsV0222Fix`
  test pins this.

## Validation (operator's gate)

**Phase 1 (tests) — local, all green:**

```
ok  	skygate/internal/acl	1.055s
ok  	skygate/internal/handlers	1.998s
```

The two new tests in
`internal/handlers/handlers_node_ownership_test.go`
pin the fix. All pre-existing tests in
`internal/acl`, `internal/mesh`, `internal/invite`,
`internal/subnet`, `internal/headscale`,
`internal/headscale_version`, `internal/telegram`,
`internal/i18n` still pass — no regressions.

**Phase 1b (live validation on VM) — 8/8 PASS:**

A `check_v0.22.2.sh` script was scp'd to the VM and ran
the following (against the live skygate on the VM, with
the real operator's admin session):

1. Pull v0.22.2
2. Rebuild + restart skygate
3. Wait for `/version` to respond
4. Pre-fix state: `node_owner_map` has
   `tag=tag:untagged` for MSI; headscale's MSI
   `tags=[]`
5. Login + call `/my/devices` (triggers backfill)
6. Post-fix state: `node_owner_map` has
   `tag=tag:private` for MSI; headscale's MSI
   `tags=['tag:private']` ✓
7. Idempotency: call `/my/devices` again; MSI
   `tags=['tag:private']` (still applied) +
   snapshot `tag=tag:private` (stable) ✓
8. Full `make test` (smoke + check_exit_nodes +
   check_https): **83/83 PASS in both RU and EN**

The live validation also adds DBG log lines to the
backfill function (`DBG backfill node=N name=X
matchedTag=... api_tags=... hasPrivate=...`) so the
operator can trace what the function does on every
`/my/devices` load.

## What does NOT change

- The bot path is unchanged. `/mesh create|join|leave`
  and the v0.22.1 web `/my/meshes` work as before.
- The `/admin/devices` page is unchanged (still
  shows nodes with their current headscale tags).
- The `device_rules` table semantics are unchanged
  (per-DEVICE exit-rules, separate from the auto-tag
  layer).
- The `node_owner_map` table schema is unchanged.
- No new env vars. No new config flags.

## What does NOT ship in v0.22.2

- **Phase 3 (safe user migration tool)** — still
  deferred. The user's `SKYGATE_MIGRATE_USERS_TO_SUBNETS=true`
  opt-in flag is the v0.23.0+ plan.
- **butler voice v4** — deferred until the operator
  gives feedback on v3.
- **headscale 0.30+ v0.19.1 re-enable** — still blocked
  on headscale's `dns.extra_records` support. The mavis
  cron `headscale-milestone-16-check` (weekly) reports
  any progress on headscale milestone #16 (DNS Work).
- **per-DEVICE visibility control** — the user also
  asked "what about making exit-nodes per-DEVICE, not
  per-USER". This is a v0.23.0+ design (per-DEVICE tags
  + ACL update + admin UI). v0.22.2 doesn't change the
  per-USER ACL; it only fixes the auto-apply layer for
  that ACL.

## Files

- `internal/handlers/handlers_node_ownership.go`
  (modified) — Strategy A fix + DBG log lines
- `internal/handlers/handlers_node_ownership_test.go`
  (new, 208 lines) — two tests pin the fix
- `scripts/check_v0.22.2.sh` (new) — live validation
  on the VM

## Verification commands (operator's quick check)

```bash
# 1. Run the standard make test (smoke + check_exit_nodes + check_https)
cd /home/skyadmin/skygate && make test

# 2. Run the v0.22.2 live validation (8 checks)
scp check_v0.22.2.sh skyadmin@<vm>:/tmp/
ssh skyadmin@<vm> "chmod +x /tmp/check_v0.22.2.sh && bash /tmp/check_v0.22.2.sh"

# 3. Check that MSI (or any freshly-registered device) now has tag:private
ssh skyadmin@<vm> "docker exec headscale headscale nodes list 2>/dev/null | grep -A1 ' MSI'"
# Should show: tag:private

# 4. Check the skygate snapshot
ssh skyadmin@<vm> "docker exec skygate sqlite3 /data/skygate.db \
  'SELECT node_id, username, tag FROM node_owner_map WHERE node_id = 15;'"
# Should show: 15|skyadmin|tag:private
```

## What the operator can do now

1. Just visit `/my/devices` (as any user) — the backfill
   fires, and any node that was missing `tag:private`
   (because of the v0.22.0 bug) gets it applied
   automatically.
2. Or wait for the next `make test` run — the smoke
   doesn't trigger the backfill for MSI directly, but
   the v0.22.1 step 13 ("multi-user mesh") does (it
   creates + joins + leaves a mesh as a different user,
   which exercises the backfill). The 83/83 smoke
   confirms the test pass.
3. Or run `docker exec headscale headscale nodes tag -i 15 -t tag:private`
   on the VM as a one-off manual fix (skygate's backfill
   will do the same on the next `/my/devices` load).

## Build info

- Commit: 9931e1a (debug log on top of ff7e544 the fix)
- Build label on VM: v0.22.1-6-g9931e1a
- headscale version: 0.29.2 (unchanged)
- Go runtime: 1.23 (unchanged)
- Smoke: 83/83 (EN 83 + RU 83) — same as v0.22.1
- check_exit_nodes: 3/3 (emilia, sharlotta, karolina)
- check_https: PASS (TLS 1.3, SAN match, HSTS via / fallback)

## Next steps

- **v0.23.0 (planned)**: Phase 3 safe user migration
  tool. `SKYGATE_MIGRATE_USERS_TO_SUBNETS=true` opt-in,
  operator-driven, audit-row per user, pre-flight check,
  idempotent. Also: per-DEVICE visibility control
  (per-DEVICE tags + ACL update) so a user can have
  multiple devices with different exit-node scopes.
- **Backlog**: butler voice v4, headscale 0.30+
  v0.19.1 re-enable (still blocked on
  `dns.extra_records` in the policy schema).

## Credits

Designed and implemented by skygate in response to the
operator's "делал добавление четко по инструкции что в skygate и следовательно так и вышло" report on 2026-07-20.
