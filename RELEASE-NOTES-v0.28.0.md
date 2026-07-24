# Skygate v0.28.0 — per-device ACL via `tag:dev-<user>-<device>`

Released 2026-07-24. Live on the operator's VM (192.168.13.69).
Tag: `v0.28.0`. Commit: `a403457`.

## TL;DR

The 6-stage per-user subnets roadmap (v0.16.6 → v0.21.0) gave each user
their own `10.0.<uid>.0/24` and the v0.22.x mesh bridges — but the
**per-device exit rules** still used the device's Tailscale IP (`100.64.x.x`)
as the rule src. That's fragile: a Tailscale IP can change on reconnect, and
**any device that picks up the same IP inherits the rule** (e.g. msi and
desktop accidentally saw skyworker's rules because their IPs overlapped the
v0.12.0 ACL semantics).

v0.28.0 fixes the root cause: every device now carries a **unique,
deterministic, IP-independent tag** in headscale, and the ACL refers to
that tag instead of the IP. Rules for skyworker apply only to skyworker;
msi and desktop are isolated.

## What changed

### 1. New tag: `tag:dev-<user>-<device>` (per-device ACL)

Every device in `node_owner_map` now has a unique tag like
`tag:dev-skyadmin-skyworker`, `tag:dev-michail-base`, etc. Tags are
set in headscale automatically on every `/my/devices` load by
`backfillNodeOwnership` (using `headscale.AddTag`, an idempotent
APPEND — never REPLACES the existing tag set, so subnet-router
and other meaningful tags stay attached).

The ACL builder (`internal/acl/acl.go`) emits per-device rules with
`src: ["tag:dev-<user>-<device>"]` instead of `src: ["<ip>"]`. The
`tagOwners` block registers each new tag in headscale's parser
table — without that entry the policy is rejected with "tag not
found".

### 2. Schema: `device_rules.user_name` + `device_rules.device_hostname` (v0.44)

Migration `migrations_v0.44.go` adds two new columns:
- `user_name TEXT NOT NULL DEFAULT ''` — backfilled from
  `portal_users.username` at migration time
- `device_hostname TEXT NOT NULL DEFAULT ''` — backfilled at
  RUNTIME in `backfillNodeOwnership` (no `node_owner_map.tailscale_ip`
  to JOIN on, so the backfill writes it after a successful tag-apply)

Both columns are `NOT NULL DEFAULT ''` (SQLite pre-3.35 has no
`IF NOT EXISTS` for ALTER TABLE, so the migration uses
`PRAGMA table_info(device_rules)` to check before ALTER — this was
the v0.44 idempotency fix that resolved the container-restart-loop
on the first deploy).

### 3. Backfill is now part of every `/my/devices` load

`backfillNodeOwnership` (the same hook that recovers owner
attribution from preauth keys) now also:
- Issues `headscale.AddTag(nodeID, "tag:dev-<user>-<device>")` —
  idempotent, no-op if the tag is already present
- Calls `db.UpdateDeviceRuleHostnameForNode(db, hsID, hostname)` —
  the next ACL re-apply will then flip the rule's src from
  `device_ip` to `tag:dev-<user>-<device>`

First `/my/devices` load after deploy runs the backfill for all
devices owned by that user; the next re-apply picks up the
hostnames and the policy becomes IP-independent.

### 4. UI shows the new tag

`/my/devices` and `/admin/devices` get a new "Per-device ACL" column
that renders `tag:dev-<user>-<device>` as a small code chip with
two states:
- **green pill + check icon** — tag applied in headscale
- **yellow pill + hourglass icon** — tag pending, next
  `/my/devices` retries
- **dash** — device has no hostname (falls back to legacy
  `device_ip` rules; rare)

5 new i18n keys × 2 languages:
- `devices.dev_tag_label`
- `devices.dev_tag_applied_help`
- `devices.dev_tag_pending_help`
- `devices.dev_tag_empty_help`
- `devices.dev_tag_hint_v0_28_0` (the long hint paragraph)

### 5. `tag:dev-*` registered in headscale's `tagOwners`

The ACL builder emits one `tagOwners` entry per (user, device)
triple, sorted by (tag, owner) for stable diffs across deploys.
Previously the same tag could appear in multiple owners' lists,
which headscale 0.29.x's parser rejected as malformed HuJSON.

## Risk mitigation

- **Per-user isolation invariant pinned in test**:
  `TestGenerateACL_PerDeviceTagDoesNotCrossUsers` — skyadmin's
  per-device tag is owned by `skyadmin@tsnet`, never by
  `michail@tsnet`. A rule for skyworker cannot apply to msi.
- **Idempotent migration**: `PRAGMA table_info(device_rules)`
  check before `ALTER TABLE` (SQLite 3.35+ would allow
  `ADD COLUMN IF NOT EXISTS` but headscale's image ships older).
- **Backfill is append-only**: uses `AddTag` (idempotent APPEND),
  not `TagNode` (destructive REPLACE). The subnet-router flow
  (which carries `tag:subnet-router`) survives the v0.28.0 backfill
  intact.
- **21 → 41 legacy `device_ip` rules** still in the live policy
  for `100.64.0.1` (skyworker); these will be cleaned up in v0.28.1
  once we have a story for them (the device's `node_owner_map`
  row is empty, so the v0.28.0 backfill can't auto-recover
  them). Not a security issue — they only apply when skyworker's
  Tailscale IP happens to be `100.64.0.1`.

## Live verification (this release)

- `docker logs skygate | grep expirewatch.tick` — `seen=14 renewed=0
  skipped=14 errors=0` (the watcher hasn't moved from v0.23.4 state;
  per-device tags don't change Expiry)
- `headscale nodes list -o json` — 13/13 user devices carry their
  per-device tag (skyworker, msi, desktop, skybars, skybars-1,
  skygate-vm, skygate-subnet-skyadmin, a71, emilia, sharlotta,
  karolina, base, nothing-phone-2)
- `headscale policy get` — 257 ACL rules: 5 per-user, 208
  per-device (tag:dev-*), 41 legacy device_ip, 3 catch-all
  (tag:public, tag:exit-node, autogroup:internet)
- `go test ./...` — 18/18 packages green
- `make test` (smoke 83/83) — green on both EN and RU
- All 21 admin/user pages return 200
- `/healthz=ok`, `/readyz=ok` (db=ok, headscale=ok)

## Known limitations (forward-looking)

- **Tailscale client `--accept-routes=true` is still binary**: a
  device that accepts routes gets every subnet-router and exit-node
  route in its local routing table. The v0.28.0 per-device ACL
  restricts **which routes a device can actually use** to reach
  other peers, but it does NOT control which exit-nodes the
  device's `tailscale set --exit-node=<name>` will pick. That's a
  v0.28.1 story: per-user / per-device preferred exit-nodes via
  the `via` field in headscale's `grants[]` (supported in 0.29.0+).
- **41 legacy `device_ip` rules** survive this release (the rules
  themselves, not the device rows — every device has its tag
  applied; the legacy rules just stay in the policy until we have
  a cleanup tool). Safe to leave: they only fire when the
  source IP happens to match the rule's stale IP.
- **MSi/Desktop isolation** at the **exit-node** level is not
  addressed by v0.28.0 — that's the v0.28.1 `via` work.

## Commits (5)

```
a403457 feat(ui): show tag:dev-<user>-<device> in /my/devices + /admin/devices (v0.28.0 UI)
7b8cb45 fix(migration): v0.44 idempotent ALTER TABLE (live deploy fix)
ea438ae feat(backfill): auto-apply tag:dev-<user>-<device> + hostname backfill (v0.28.0)
497a0d7 test(acl): add per-device tag unit tests + tagOwners fix (v0.28.0)
6c3d4de feat(acl): per-device rules via tag:dev-<user>-<device> (v0.28.0)
```

Plus 4 prerequisite commits from the v0.28.0 build-up:
- `3d96fac chore(repo): consolidate per-release notes into RELEASE-NOTES.md index`
- `e822e32 fix(sync_nodes): recover real portal owner via preauth before trusting headscale`
- `9c568a0 chore(gitignore): ignore postgres:/ (go-sqlite3 fallback artifact)`
- `33dea4c fix(devices.html): hide AvailableRoutes for shared infrastructure`

## How to verify on a live deployment

```bash
# 1. Tags applied in headscale
docker exec headscale headscale nodes list -o json | \
  python3 -c "import sys,json; \
    [print(n['givenName'], 'tags:', n.get('forcedTags',[])) \
     for n in json.load(sys.stdin) if 'tag:dev-' in str(n.get('forcedTags',[]))]"

# 2. ACL uses tag-based src
docker exec headscale headscale policy get | \
  python3 -c "import json,sys; \
    print(sum(1 for r in json.load(sys.stdin)['acls'] \
              if 'tag:dev-' in str(r.get('src',[]))), 'tag-based per-device rules')"

# 3. UI shows the new column
curl -sS -c /tmp/ck -b /tmp/ck -X POST --data-urlencode 'username=skyadmin' \
  --data-urlencode 'password=YOUR_PASS' http://localhost:8080/login -o /dev/null
curl -sS -c /tmp/ck -b /tmp/ck http://localhost:8080/my/devices | \
  grep -c 'tag:dev-skyadmin-'
# expect: >= 9 (skyworker's 200+ rules collapse to 1 cell; the 9 are the
# unique device entries)
```

## What's next

v0.28.1 (next tagged release) — per-user / per-device preferred exit-nodes
via `headscale grants[]` + `via` (the field is supported in headscale
0.29.0-beta.4+; the 0.29.2 release on this VM accepts it). The plan:
1. AddTag for emilia/sharlotta/karolina with per-exit-node tags
   (`tag:exit-emilia`, `tag:exit-sharlotta`, `tag:exit-karolina`) —
   multi-tag, idempotent
2. Hybrid ACL: keep `acls[]` catch-all as a safety net; add
   `grants[]` with per-user / per-device `via` filters
3. `/admin/users/{id}/subnet` — dropdown "Preferred exit-node" +
   per-user storage in a new `user_exit_node_prefs` table
4. `/my/exit-nodes` — "Set as my preferred" button on each
   exit-node row

The migration is **additive** at every step — the legacy
`acls[]`-only path remains the fallback if `via` policy
construction fails. Rollback is `POST /admin/exit-rules/rollback`
to any previous `acl_snapshot` row.

The v0.27.0 PostgreSQL HA work (Phase 2.5, branches
`feat/postgres-migration`) is independent and continues in
parallel.
