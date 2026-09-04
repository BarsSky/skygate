# Skygate v1.5.0 — DERP relay integration, headscale policy via:-clause, /admin/derp fixes

**Date:** 2026-09-04

> v1.5.0 is the stable release of the B145/B147/B148/B149/B150
> HA-chain work that shipped as `v1.5.0-alpha1` on 2026-08-19,
> plus the full set of DERP / Tailscale / exit-rules fixes
> that were developed in the 2026-09-04 incident response
> (B235 / B235.1 / B235.2 / B235.3 / B236 / B237 / B237.1 /
> B237.2 / B237.7 / B237.8). Read the post-mortem
> [docs/internal/2026-09-04-tailnet-fixes.md](docs/internal/2026-09-04-tailnet-fixes.md)
> for the full chain of root causes and the current
> configuration.

---

## What's in v1.5.0

### HA chain (B145/B149/B150 — shipped in v1.5.0-alpha1)

- **HA chain + elector + pluggable DNS provider** (B145):
  leader election between skygate instances with
  pluggable DNS provider (reg.ru or mock).
- **`/admin/certificates` page** (B148 / BL-2 Phase 4):
  upload + reg.ru DNS-01 toggle.
- **In-app certsync scheduler** (B147 / BL-2 Phase 3).
- **`/admin/ha` page** (B149): HA chain editor + failover
  controls + reg.ru credentials.
- **`/admin/deploy` page + skygate deploy CLI** (B150 /
  BL-2 Phase 6).

### DERP relay integration (B116 / B235 / B237 / B237.2)

- **Single source of truth for DERP relays**:
  `derp_relays` PG table — operator-managed
  (per-row CRUD) plus the bundled `controlplane.tailscale.com`
  entry (`is_bundled=0` — managed like any other row).
- **Tailscale-shaped `derpmap.json` endpoint** (B237):
  `GET /admin/derp/relays/derpmap.json` serves a
  Tailscale-compatible map (RegionID, RegionCode,
  RegionName, Nodes: [{Name, RegionID, HostName,
  DERPPort:443, STUNPort:3478, STUNOnly:false,
  InsecureForTests:false}]). headscale fetches this
  and merges with the public Tailscale derpmap.
- **One-click "Apply to headscale" button** (B237):
  `POST /admin/derp/relays/apply-headscale` rewrites
  the headscale config.yaml's `derp.urls` block
  (idempotent rewriteDerpURLs), writes atomically
  (tmp + mv), and `docker restart headscale` (10s
  timeout). Audit row `derp_apply_headscale`.
- **Correct Public IP display on /admin/derp** (B237.2):
  pre-B237.2 showed the skygate container's
  docker-bridge egress IP (e.g. `172.18.0.3`) — the new
  `resolvePublicDERPIP()` uses `net.LookupHost` of
  `SKYGATE_DERP_HOSTNAME` to return the real public IP
  the Tailscale clients dial, with a `(dns:env)` /
  `(dns:derper)` / `(egress)` annotation explaining
  which source was used.
- **/admin/derp/dashboard fix** (B235 + B235.1 + B235.2
  + B235.3): the public-DERP regions used to show
  `degraded` with empty latency because `FetchPublicDERPs`
  used the Tailscale node's short label (`1f`, `22w`)
  as the Host. Now uses the FQDN (`derp1f.tailscale.com`)
  via the new `HostName` field, with `RegionCode` on
  the region (not the node), a `.Name=<short>`
  pill next to the host, an `id` tooltip explaining
  the Tailscale region_id semantics, and
  `derp_health.name` column to persist the short label.

### Tailscale subnet-routes management on /admin/tailscale (B236)

- **One-click "Clear all" and per-CIDR "Apply"** for
  `tailscale set --advertise-routes=` on `skygate-host-1`.
- **Validation** (refused with a 4xx-style flash):
  - **Hard rule: never advertise a subnet the
    advertising host is itself in.** Pre-B236 the
    `192.168.13.0/24` (the LAN skygate-host-1 sits in)
    was being advertised, creating a routing loop on
    every Tailscale client (LAN traffic → Tailscale →
    skygate-host-1 → LAN, 540-1700ms). Post-B236 the
    LAN goes direct via Ethernet.
  - **Docker bridge ranges (172.17-172.32) refused.**
    These are host-local; remote Tailscale clients
    have no use for them.
  - Each CIDR must parse via `net.ParseCIDR`. Max 32
    entries sanity cap.
- **`WhiteIPSource` field** records which source the
  resolver used; the template shows a small
  `(dns:env)` annotation so the operator knows the
  data provenance.
- **Pluggable path resolution** (B237.1):
  `SKYGATE_HEADSCALE_CONFIG_PATH` env var (default
  `/home/admin/headscale/config/config.yaml`,
  operator-set to `/home/skyadmin/headscale/...` in
  this deployment), `SKYGATE_HEADSCALE_HOST_DIR` env
  var for the bind-mount path, fallback path resolver
  that tries `admin` → `skyadmin` → `opt` → env override.

### Exit-rules auto-reconciler (B229 + B237.7)

- **Three-layer architecture**:
  1. `device_rules` — operator's intent, what traffic
     should route via which exit node
  2. `device_exit_node_prefs` — the preferred-exit
     pin per (user, device), auto-derived from the
     dominant `exit_node_id` across the device's rules
     (B229)
  3. headscale policy `via:` grants — the final
     enforced constraint on the Tailscale client
- **Default-flip to LIVE** (B237.7): the B229
  reconciler used to default to DRY-RUN
  (`SKYGATE_PREFERRED_RECONCILER_LIVE=false`),
  which meant the system silently logged changes it
  would have made — for ~24h cyborg+basic had
  YouTube rules with no `via:` clause in headscale,
  so Tailscale clients weren't pinned to `emilia`
  and YouTube failed. New default: LIVE
  (auto-reconcile ON). Opt-out via
  `SKYGATE_PREFERRED_RECONCILER_LIVE=false/0/no/off`.
- **8 build-time tests** in
  `reconciler_b237_7_test.go` cover the full
  `PlanDevicePrefChange` decision matrix, including
  the **orphan user_id regression guard**
  (user_id=6/michail has no `portal_users` row, but
  the function must still produce a CREATE change
  without crashing). Updated old B229 tests
  (`reconciler_b229_test.go`) replace
  `TestPreferredExitReconcilerLive_DefaultOff` with
  `TestPreferredExitReconcilerLive_DefaultOn_B237_7`.

### Documentation (B237.8)

- **`docs/internal/tailnet-advertised-routes.md`** — the
  subnet-router hard rule, the 540-1700ms routing
  loop, the verification commands.
- **`docs/internal/exit-rules-reconciler.md`** — the
  three-layer architecture, the decision matrix,
  the B237.7 default-flip, the live-verify checklist.
- **`docs/internal/2026-09-04-tailnet-fixes.md`** — the
  one-stop post-mortem for the 2026-09-04 incident:
  4 separate issues, the commits that fixed each, the
  current `.env` configuration, the verification
  checklist.
- **`docs/derp.md`** (public) — new sections for B237
  + B237.2 (uses RFC 5737 example IPs to stay
  portable across operators).

---

## Breaking changes from v1.4.x

None at the API or DB schema level. Operators upgrading
from v1.4.x can do so with no migration other than the
normal `deploy/deploy.sh` flow.

**Behavioural change**: `SKYGATE_PREFERRED_RECONCILER_LIVE`
default flipped from `false` to `true`. Operators who
were depending on the dry-run default (i.e. never set
the env var and expected the reconciler to only log)
will see writes happening on every tick. To restore the
old behavior, set `SKYGATE_PREFERRED_RECONCILER_LIVE=false`
in `.env`.

---

## Migration from v1.4.x

```bash
# 1. Pull the tag
git fetch --tags
git checkout v1.5.0

# 2. Build + deploy (same as any other v1.5.x release)
./deploy/deploy.sh

# 3. Confirm the new env vars are set in .env
grep -E '^SKYGATE_(DERP_HOSTNAME|HEADSCALE|ACL_VIA_ENABLED|PREFERRED_RECONCILER_LIVE)' .env
# Expect:
#   SKYGATE_DERP_HOSTNAME=...
#   SKYGATE_HEADSCALE_CONFIG_PATH=...
#   SKYGATE_HEADSCALE_HOST_DIR=...
#   SKYGATE_ACL_VIA_ENABLED=true
#   SKYGATE_PREFERRED_RECONCILER_LIVE=true

# 4. Click "Apply to headscale" on /admin/derp/relays
#    to push the new skygate-managed derpmap URL into
#    headscale's `derp.urls`.

# 5. (Optional) On skygate-host-1:
sudo tailscale set --advertise-routes=""
sudo systemctl restart tailscaled
# This clears any pre-B236 bad advertised-routes
# (e.g. 192.168.13.0/24 — the LAN itself, which
# created a routing loop).
```

## Files added / changed in v1.5.0

| File | B-block |
|---|---|
| `internal/derphealth/types.go` | B235 |
| `internal/derphealth/map.go` | B235, B235.1, B235.2 |
| `internal/derphealth/probe.go` | B235.3 |
| `internal/db/migrations_v0_69_b235_3.go` | B235.3 |
| `internal/feature/admin/derp_dashboard.go` | B237 (derpmap endpoint) |
| `internal/feature/admin/derp_apply_headscale_b237.go` | B237 (Apply to headscale button) |
| `internal/feature/admin/derp.go` | B237.2 (resolvePublicDERPIP) |
| `internal/feature/admin/tailscale.go` | B236 (subnet-routes management) |
| `internal/feature/exit_rules/reconciler.go` | B237.7 (default-flip to LIVE) |
| `internal/feature/exit_rules/reconciler_b237_7_test.go` | B237.7 (8 unit tests) |
| `internal/feature/exit_rules/reconciler_b229_test.go` | B237.7 (updated 2 tests) |
| `internal/handlers/templates/admin/tailscale.html` | B236 |
| `internal/handlers/templates/admin/derp_relays.html` | B237 (Apply button) |
| `internal/handlers/templates/admin/derp.html` | B237.2 (Public IP annotation) |
| `internal/i18n/catalog_derp.go` | B237, B237.2 |
| `internal/i18n/catalog_tailscale.go` | B236 |
| `docker-compose.yml` | B237.1 (bind-mount) |
| `cmd/skygate/main.go` | B237, B236 (routes) |
| `scripts/check_b235.sh`, `check_b236.sh`, `check_b237.sh`, `check_b237_2.sh`, `check_b237_7.sh` | (regression guards) |
| `scripts/verify_pre_deploy.sh` | (B-block catalog) |
| `AGENTS.md` | (per-B-block section) |
| `docs/internal/tailnet-advertised-routes.md` | B237.8 |
| `docs/internal/exit-rules-reconciler.md` | B237.8 |
| `docs/internal/2026-09-04-tailnet-fixes.md` | B237.8 |
| `docs/derp.md` | B237.8 (B237 + B237.2 sections) |
| `docs/internal/README.md` | B237.8 (index) |
| `RELEASE-NOTES-v1.5.0.md` | (this file) |
| `CHANGELOG.md` | (v1.5.0 entry) |

## Test coverage

- `go test -count=1 -short ./...` — 43 packages, all
  green. Total test runtime ~60s.
- `bash scripts/check_b235.sh` — B235 regression
  contracts.
- `bash scripts/check_b236.sh` — B236 regression
  contracts.
- `bash scripts/check_b237.sh` — B237 regression
  contracts.
- `bash scripts/check_b237_2.sh` — B237.2 regression
  contracts.
- `bash scripts/check_b237_7.sh` — B237.7 regression
  contracts.
- `bash scripts/verify_pre_deploy.sh` — full B-block
  catalog.

## Known limitations

- **B237.8 is not yet implemented** (peer advertised-routes
  loop detection UI on /admin/tailscale). The B236 form
  surfaces the current skygate-host-1 advertised routes,
  but doesn't yet show the full list of peer advertised
  routes with the "shadowed self-LAN" / "public IP
  (anti-pattern)" markers. Operators with non-standard
  setups (multiple Tailscale peers, exit nodes on a
  different host than the subnet-router) should still SSH
  into those peers manually to check their
  `--advertise-routes`.
- **`SKYGATE_ACL_VIA_ENABLED=true` is required** for
  the headscale `via:` clauses to reach the policy. If
  you upgrade from v1.4.x and don't set this in `.env`,
  the per-device exit-node pinning will be cosmetic (the
  per-CIDR grants are emitted, but without the `via:`
  clause the Tailscale client is free to pick any exit
  node). The B237 default is `false` for backward
  compat — set it explicitly in `.env`.
- **The B229 reconciler still doesn't know about
  per-user exit-rules** (managed by `/my/exit-nodes`,
  not B229). The reconciler logs the count of per-user
  prefs for visibility but doesn't write them.

## Live-verify on the operator's deployment (192.168.13.69)

```text
# 1. Subnet-router (B236)
tailscale status --json | python3 -c "import sys, json; d=json.load(sys.stdin); print('Self.PrimaryRoutes:', d['Self'].get('PrimaryRoutes', []))"
# Expect: Self.PrimaryRoutes: []  (empty, after B236)

# 2. DERP latency (B236 fix verified)
ip route get 95.165.170.190
# Expect: 95.165.170.190 via 192.168.13.1 dev ens18 src 192.168.13.69
ping -c 1 -W 2 95.165.170.190
# Expect: 0.6-0.9ms

# 3. Public IP display (B237.2)
curl -s https://derp.skynas.ru/ -o /dev/null -w "code=%{http_code} time=%{time_total}s\n"
# Expect: 200 4-16ms
# /admin/derp shows "Публичный IP: 95.165.170.190 (dns:env)"

# 4. Exit-rules reconciler (B237.7 default-flip)
docker logs skygate-skygate-1 --since 1h | grep "preferred-reconciler: starting"
# Expect: "(interval=1h0m0s, live=true)"

# 5. headscale policy has via: clauses
docker exec headscale headscale policy get | python3 -c "
import sys, json; d=json.load(sys.stdin)
print(f'grants with via: {sum(1 for g in d.get(\"grants\", []) if \"via\" in g)}')"
# Expect: 213

# 6. cyborg + basic prefs
sudo -u postgres psql -d skygate_staging -c "
SELECT user_id, device_hostname, exit_node_tag, via_enabled
FROM device_exit_node_prefs
WHERE device_hostname IN ('cyborg', 'basic')
ORDER BY device_hostname"
# Expect: cyborg (user=1) + basic (user=6), both
#   tag:dev-infra-emilia, via_enabled=1
```

## See also

- **`docs/internal/2026-09-04-tailnet-fixes.md`** — the
  2026-09-04 incident post-mortem (one-stop reference
  for the four issues + the current `.env` state +
  the verification checklist).
- **`docs/internal/tailnet-advertised-routes.md`** —
  B236: the subnet-router hard rule, the verification
  commands, the loop it caused.
- **`docs/internal/exit-rules-reconciler.md`** —
  B229/B237.7: the three-layer architecture, the
  decision matrix, the default-flip rationale, the
  build-time contract tests.
- **`AGENTS.md`** — B235 / B236 / B237 / B237.2 /
  B237.7 sections with code-level details + file
  lists.
- **`docs/derp.md`** — DERP relay integration, B237
  (skygate-managed derpmap.json), B237.2 (correct
  Public IP display).
- **B146 (reg.ru IP sub-limit)** is the only
  v1.5.0-alpha1 item still blocked. The B148 / B149
  / B150 admin pages work without it; the only
  feature gated on B146 is the auto-DNS-01 for
  Let's Encrypt via reg.ru. Track at
  `https://github.com/BarsSky/skygate/issues/146`.
