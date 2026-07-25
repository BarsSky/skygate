# Release Notes — v0.28.3

**Tag**: `v0.28.3`
**Date**: 2026-07-25
**Type**: Security fix (ACL bypass)
**Build**: `v0.28.2-1-g0809ad5+0809ad5`
**Status**: Live on `192.168.13.69` (OLD VM), shipped to `origin/main`

---

## TL;DR

The catch-all `* → autogroup:internet` rule (in both `acls[]` legacy and
`grants[]` v0.28.1+ paths) let ANY device in the tailnet use ANY exit-node
for arbitrary internet destinations, including `karolina`'s 148 PrimaryRoutes
(Telegram/Google/Cloudflare/etc.). msi (tag:dev-skyadmin-msi → skyadmin@…)
had no per-device rules, but the catch-all let it reach `skyworker`'s
resources through `karolina`.

**v0.28.3 fix**:
1. Per-user grant now includes `autogroup:internet` in its dst list
   (every user can reach public internet through their own grant).
2. The catch-all's src is changed from `*` to `tag:public` — only relay
   nodes (emilia/sharlotta/karolina) can use `autogroup:internet` themselves
   (i.e. FORWARD exit-node traffic to the internet).

Combined effect:
- msi (a skyadmin device) still has internet egress (via the per-user grant
  in skyadmin's row), but only as `skyadmin@…` — and skyadmin's grant has
  `via=[tag:exit-emilia]`, so msi is pinned to emilia. msi CANNOT use
  karolina (via constraint violated at the headscale packet filter).
- msi CANNOT reach `karolina`'s 148 PrimaryRoutes — the via constraint
  denies any route through karolina when the policy requires via=emilia.
- Relay nodes (emilia/sharlotta/karolina) can still FORWARD their own
  exit-node traffic to the internet (catch-all `tag:public → autogroup:internet`).
- The per-user grant preserves the v0.28.1 design: users with a preferred
  exit-node are pinned; users without a preference get any exit-node.

---

## Background — the bypass

The user reported (2026-07-25 09:39 MSK):

> проверил msi все равно без правил имеет доступ к сайтам и подсетям что
> только для skyworker

Diagnostics (`headscale policy get`):

```
=== catch-all grants (any src=*) ===
  src=['*'] dst=['tag:public']    via=(none) ip=['*']
  src=['*'] dst=['tag:exit-node'] via=(none) ip=['*']
  src=['*'] dst=['autogroup:internet'] via=(none) ip=['*']   <-- BYPASS

=== per-user grants ===
  src=skyadmin@tsnet.skynas.ru via=['tag:exit-emilia']
  src=michail@tsnet.skynas.ru  via=['tag:exit-sharlotta']
  src=daniil@tsnet.skynas.ru   via=['tag:exit-karolina']
  src=guest@tsnet.skynas.ru    via=(none)
```

The bypass: `src=* dst=autogroup:internet via=(none)` — every device in
the tailnet could use any exit-node for any internet destination, ignoring
the per-user `via` constraint that v0.28.1 was supposed to enforce.

For msi (tag:dev-skyadmin-msi → skyadmin@…):
- msi's tag resolves to skyadmin@… via tagOwners
- msi's packet to 91.108.4.0/22 (Telegram) matches skyadmin's per-user grant
  only if `dst` includes the CIDR — but it doesn't, skyadmin's dst is
  [skyadmin@…, h-user-skyadmin-subnet] (autogroup:internet was missing
  from per-user dst in v0.28.2)
- msi falls through to the catch-all `* → autogroup:internet`
- catch-all has NO `via`, so msi can pick any exit-node — including
  karolina with its 148 PrimaryRoutes
- msi reaches skyworker's resources through karolina

---

## The fix

### 1. Per-user grant dst now includes `autogroup:internet`

```diff
- "src": ["skyadmin@tsnet.skynas.ru"],
- "dst": ["skyadmin@tsnet.skynas.ru:*", "h-user-skyadmin-subnet"]
+ "src": ["skyadmin@tsnet.skynas.ru"],
+ "dst": ["skyadmin@tsnet.skynas.ru:*", "h-user-skyadmin-subnet", "autogroup:internet"]
```

In the `grants[]` path, the dst is bare (no `:*`) — `ip: ["*"]` covers
any port.

Why this is safe:
- Every portal user's device resolves to their `<username>@tsnet.skynas.ru`
  identity via the `tagOwners` block. So msi (tag:dev-skyadmin-msi) is
  matched by skyadmin's grant, michail's phone (tag:dev-michail-X) is
  matched by michail's grant, etc.
- The per-user grant ALREADY had `via=[]` (for users without a preference)
  or `via=["tag:exit-<preferred>"]` (v0.28.1). Adding `autogroup:internet`
  to dst means: "this user can reach the public internet, but only as
  themselves, and only via their preferred exit-node (if set)".
- Users without a preference can still use ANY exit-node (via=[]), but
  they're matched AS THEMSELVES, not as an anonymous catch-all user.

### 2. Catch-all src changed from `*` to `tag:public`

```diff
- { "src": ["*"],            "dst": ["autogroup:internet"], "ip": ["*"] }
+ { "src": ["tag:public"],   "dst": ["autogroup:internet"], "ip": ["*"] }
```

The catch-all still exists (relay nodes need it to FORWARD exit-node
traffic to the internet), but its src is restricted to `tag:public` —
only relay nodes (emilia/sharlotta/karolina) match. End-user devices
no longer match the catch-all, so they can't piggyback on it.

### What still works

- `* → tag:public` catch-all — kept unchanged. msi can ping emilia.
  Needed for tailnet visibility.
- `* → tag:exit-node` catch-all — kept unchanged. Needed for admin SSH
  into relays (the SSH rule `src=skyadmin@… dst=tag:public` doesn't
  cover tag:exit-node devices that aren't also tag:public).
- `tag:public → autogroup:internet` catch-all — only relays use this,
  for exit-node forwarding. Without it, emilia couldn't forward msi's
  packets to 8.8.8.8.

---

## Verification (live, OLD VM)

### Diagnostics after v0.28.3 deploy

```
=== catch-all grants (any src=* or tag:public) ===
  src=['*']         dst=['tag:public']           via=(none) ip=['*']
  src=['*']         dst=['tag:exit-node']        via=(none) ip=['*']
  src=['tag:public'] dst=['autogroup:internet']  via=(none) ip=['*']

=== per-user grants (with @tsnet) ===
  src=skyadmin@tsnet.skynas.ru            via=['tag:exit-emilia']     ✓ internet dst=['skyadmin@tsnet.skynas.ru:*', 'h-user-skyadmin-subnet', 'autogroup:internet']
  src=michail@tsnet.skynas.ru             via=['tag:exit-sharlotta'] ✓ internet dst=['michail@tsnet.skynas.ru:*', 'h-user-michail-subnet', 'autogroup:internet']
  src=guest@tsnet.skynas.ru               via=(none)                  ✓ internet dst=['guest@tsnet.skynas.ru:*', 'h-user-guest-subnet', 'autogroup:internet']
  src=daniil@tsnet.skynas.ru              via=['tag:exit-karolina']  ✓ internet dst=['daniil@tsnet.skynas.ru:*', 'h-user-daniil-subnet', 'autogroup:internet']
```

- 0 catch-alls to `* → autogroup:internet` (was: 1)
- 4 per-user grants with `autogroup:internet` in dst (was: 0)
- 3 per-user grants with `via` (skyadmin/michail/daniil, unchanged from v0.28.2)
- New: `tag:public → autogroup:internet` catch-all (relay forwarding)

### Smoke test

```
[en] ---- SUMMARY (en): 83 pass, 0 fail
[ru] ---- SUMMARY (ru): 83 pass, 0 fail
```

### go test ./internal/acl/...

```
ok  skygate/internal/acl  1.402s
```

All 10 v0.28.x ACL tests PASS (4 v0.28.0 + 3 v0.28.1 + 3 v0.28.2 + 3 NEW v0.28.3).

### /healthz + /readyz

```
{"build":"v0.28.2-1-g0809ad5+0809ad5","instance_id":"unconfigured","status":"ok","timestamp":"2026-07-25T07:15:40Z"}
{"healthy":true,"db":"ok","headscale":"ok","instance_id":"unconfigured","build":"v0.28.2-1-g0809ad5+0809ad5","uptime_sec":37,"timestamp":"2026-07-25T07:15:40Z","checks":{"db":"ok","headscale":"ok"}}
```

---

## Tests (3 NEW + 5 UPDATED)

### NEW: v0.28.3 invariants

1. **`TestGenerateACLWithVia_PerUserGrantHasAutogroupInternet`** — pins
   that the per-user grant's dst list ALWAYS includes `autogroup:internet`
   (as the last entry, after the user's own identity and subnet).
2. **`TestGenerateACLWithVia_CatchAllIsTagPublicNotStar`** — pins that
   the `autogroup:internet` catch-all has `src=tag:public`, NOT `src=*`.
   The OLD bypass shape is explicitly banned.
3. **`TestGenerateACL_LegacyPerUserGrantHasAutogroupInternet`** — pins
   the same fix for the LEGACY `acls[]` path (used when
   `SKYGATE_ACL_VIA_ENABLED=false`).

### UPDATED

- `TestGenerateACLValidJSONShape` — per-user rule expectation updated
  to include `autogroup:internet:*` at the end.
- `TestGenerateACL_LastRuleIsAutogroupInternet` — added explicit banned
  shape check (the OLD `src=* dst=autogroup:internet:*` MUST NOT appear).
- `TestGenerateACLWithVia_NoPreferencesWhenNoneSet` — added explicit
  banned shape check (the OLD `src=* dst=autogroup:internet` MUST NOT
  appear).
- `TestGenerateACL_PerUserSubnetCIDR` — alice's per-user rule now
  expects `autogroup:internet:*` at the end.
- `TestGenerateACL_SharedSubnetsExtendDst` — bob's per-user rule now
  expects `autogroup:internet:*` at the end.
- `multi_subnet_integration_test.go` — 5 multi-CIDR test cases updated
  to include `autogroup:internet` in the dst list.

---

## Files changed

- `internal/acl/acl.go` — both `GenerateACLForPlane` (legacy) and
  `GenerateACLWithViaForPlane` (via=true) updated. Per-user grant
  builder appends `autogroup:internet` to dst. Catch-all builder
  changes src from `*` to `tag:public`.
- `internal/acl/acl_test.go` — 3 NEW tests, 5 UPDATED.
- `internal/acl/multi_subnet_integration_test.go` — 5 UPDATED.

No env-var changes. No schema migration. No new i18n keys. No breaking
changes to the public API.

---

## Operational notes

### How to verify on the VM (operator-side)

```bash
# 1. Confirm the catch-all is now src=tag:public, not src=*
docker exec headscale headscale policy get > /tmp/policy.json
python3 -c "
import json
d = json.load(open('/tmp/policy.json'))
for g in d.get('grants', []):
    if 'autogroup:internet' in g.get('dst', []):
        print(f'  src={g[\"src\"]} dst={g[\"dst\"]}')
"

# Expected output:
#   src=['*']         dst=['tag:public']          ...     (catch-all, unchanged)
#   src=['*']         dst=['tag:exit-node']       ...     (catch-all, unchanged)
#   src=['tag:public'] dst=['autogroup:internet'] ...     (NEW — relay-only)
#   src=['skyadmin@…'] dst=['skyadmin@…:*', 'h-user-skyadmin-subnet', 'autogroup:internet']  (NEW dst entry)
#   src=['michail@…']  dst=['michail@…:*',  'h-user-michail-subnet',  'autogroup:internet']  (NEW dst entry)
#   src=['daniil@…']   dst=['daniil@…:*',   'h-user-daniil-subnet',   'autogroup:internet']  (NEW dst entry)
#   src=['guest@…']    dst=['guest@…:*',    'h-user-guest-subnet',    'autogroup:internet']  (NEW dst entry)

# 2. Test msi can't reach karolina (live test)
# On the msi machine (operator's box):
#   $ sudo tailscale set --exit-node=  # unset
#   $ ping 91.108.4.1  # Telegram CIDR (in karolina's PrimaryRoutes)
#   Expected: timeout (via constraint blocks the route)
```

### Rollback

If the v0.28.3 fix causes an unexpected regression, revert with:

```bash
cd /home/skyadmin/skygate
git checkout v0.28.2
docker compose up -d --force-recreate --no-deps skygate
curl -fsS -X POST -c /tmp/ck.txt http://localhost:8080/login \
  -d "username=skyadmin&password=$SKYGATE_ADMIN_PASS"
curl -fsS -X POST -b /tmp/ck.txt http://localhost:8080/admin/exit-rules/reapply
```

The v0.28.2 policy has the bypass; v0.28.3 closes it.

---

## What comes next

### v0.28.4 (small follow-up, ~1 day)

- Cleanup 41 legacy `device_ip` rules (orphaned, pre-v0.28.0).
- `add_per_exit_node_tags.sh` deploy helper (automate emilia/sharlotta/karolina
  AddTag, so future re-applies don't need the manual dance).
- Polish i18n if any new strings.

### v0.29.x (per-device preferred exit-node, ~2-3 days)

- Extend `user_exit_node_prefs` to `(user_id, device_id) UNIQUE` keyed
  on per-device tag.
- Add `via: ["<device's preferred>"]` in per-device rules loop.
- UI: device-level "Set as preferred" button in /my/devices.
- msi would get its own per-device `via` independent of skyadmin's
  per-user `via`.

### v0.30.0 (when headscale 0.30+ lands, ~unknown)

- Re-evaluate `hosts:` workaround. If the v2 parser's `parseAlias` is
  fixed to split alias:port, drop the `hosts:` block, emit raw
  CIDR+port in dst.
- Possibly re-evaluate entire ACL shape (grants vs acls).
