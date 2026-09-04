# 2026-09-04 tailnet fixes — B235 / B236 / B237 / B237.2 / B237.7

**Audience:** operator who was involved in the 2026-09-04
incident and wants the full picture of what was wrong,
what was fixed, and what's the current configuration.

## TL;DR

Four separate issues, all caused by configuration drift
that had accumulated over weeks. Each was invisible until
something forced the operator to look at `/admin/derp` and
`/admin/tailscale` together.

| # | Issue | Blocked | Fix (commit) | Re-verify |
|---|---|---|---|---|
| 1 | `/admin/derp/dashboard` showed all 28 public DERP regions as `degraded` (no latency) — `n.Name` short label was used as Host | YouTube/Telegram rule auditing on `/admin/derp/dashboard` | **B235 + B235.1 + B235.2 + B235.3** (commits `b17470ba…327671b…b755f7d…9c9ccf7`) | `tail -c 200 /admin/derp/relays/derpmap.json` shows 30 regions with FQDN |
| 2 | 500-1700ms latency from `skygate-host-1` (13.69) → `derp.skynas.ru` (95.165.170.190) | DERP access from skygate-host-1 (and possibly other LAN Tailscale clients) | **B236** (commit `aed2cea`) | `ip route get 95.165.170.190` → direct LAN; `ping` 0.6-0.9ms; TLS 4-16ms |
| 3 | `/admin/derp` showed `Public IP: 172.18.0.3` (skygate container's docker-bridge egress) instead of `95.165.170.190` (the public IP Tailscale clients actually dial) | Trust in the DERP health page | **B237.2** (commit `f131ef8b`) | `/admin/derp` shows `95.165.170.190 (dns:env)` |
| 4 | YouTube on `cyborg` and `basic` failed for ~24h because the headscale policy had no `via: tag:dev-infra-emilia` clause (Tailscale clients on those devices weren't pinned to `emilia`) | Reliable YouTube access from cyborg/basic | **B237 + B237.1 + B237.7** (commits `b17470ba`, `2a3c106d`, `3f9ef43a`, `e2d0b9ec`) | `headscale policy get` shows 213 grants with `via: ['tag:dev-infra-emilia']` for skyadmin and michail users |

## Issue #2 in detail — the subnet-router loop

**Symptom:** Operator noticed that `13.69 → 95.165.170.190`
took ~500ms via TLS, and grew to 1700ms after recent
changes. They asked why Tailscale was involved at all on
a direct LAN path.

**Root cause:** `skygate-host-1` was running `tailscaled`
with
```
tailscale set --advertise-routes=172.17.0.0/16,192.168.13.0/24,172.18.0.0/16
```
The `192.168.13.0/24` is **the LAN skygate-host-1 sits in**
(LAN IP `192.168.13.69`). Advertising a subnet the
advertising host is itself in creates a routing loop on
every Tailscale client that accepts routes: the kernel
sees `192.168.13.0/24 dev tailscale0` (more specific than
the default `0.0.0.0/0`), and the Tailscale daemon
sends the packet to the relay — which routes it back
through the same LAN via the same skygate-host-1, which
sends it back to Tailscale, etc. The 137ms RTT to the
Tailscale peer (`karolina` at 100.64.0.2) multiplied by
3-4 hops = 540-1700ms.

**Fix (`sudo tailscale set --advertise-routes=""` +
`sudo systemctl restart tailscaled` on `skygate-host-1`):**

- `192.168.13.0/24` removed (the LAN doesn't need
  Tailscale routing; direct Ethernet works for everyone on
  the LAN).
- `172.17.0.0/16` removed (docker bridge is host-local;
  no remote client needs it).
- `172.18.0.0/16` removed (same).

**Result:**

```
13.69 → ip route get 95.165.170.190
   95.165.170.190 via 192.168.13.1 dev ens18 src 192.168.13.69

13.69 → ping 95.165.170.190
   0.6-0.9ms

13.69 → curl https://derp.skynas.ru/
   4-16ms (TLS + WAN round-trip)
```

**Operator's other Tailscale peers (karolina, emilia,
sharlotta) are all currently at 0 advertised-routes** —
the operator has fully transitioned to a no-subnet-router
model where every Tailscale client's internet-bound traffic
goes through the LAN gateway, not through Tailscale. Exit
node pinning (`tag:exit-node` for emilia/karolina/sharlotta)
is separate and continues to work via headscale's `via:`
clauses.

## Issue #4 in detail — the cyborg+basic YouTube outage

**Symptom:** Operator noticed that YouTube on `cyborg` and
`basic` failed (other rules on `basic` like Discord/Telegram
worked). Both devices had rules like
`(cyborg, emilia, youtube.com)` and
`(basic, emilia, 142.250.0.0/15)` in `device_rules`, but
the YouTube traffic wasn't being pinned to emilia.

**Root cause:** The B229 preferred-exit auto-reconciler
exists to **write the `device_exit_node_prefs` rows** that
headscale needs to attach a `via:` clause to the per-CIDR
grants. Without a `(user=1, hostname=cyborg, exit=emilia)`
pref in `device_exit_node_prefs`, headscale's grant for
`cyborg → autogroup:internet` has no `via:` clause →
Tailscale client on cyborg is free to use any exit node
for internet traffic → defaults to whichever exit node
Tailscale picks (often `karolina` for the Russian operator's
tailnet) → YouTube fails because karolina doesn't
advertise YouTube's CIDR.

The reconciler was running every hour and correctly
identifying the unanimous emilia → CREATE pref change
for cyborg+basic. But `SKYGATE_PREFERRED_RECONCILER_LIVE`
defaulted to `false` (DRY-RUN), so the writes were
silently skipped. The `exit_rule_logs` table had 0 rows
for B229 reconciler activity; the operator never knew
the env var existed.

**Fix (`B237.7`, commit `e2d0b9ec`):**

1. `PreferredExitReconcilerLive()` default flipped from
   `false` to `true`. Opt-out via
   `SKYGATE_PREFERRED_RECONCILER_LIVE=false/0/no/off`.
2. Manual `INSERT INTO device_exit_node_prefs` for
   cyborg and basic as an interim fix (operator
   impact was already felt).
3. `SKYGATE_ACL_VIA_ENABLED=true` set in `.env` (was
   unset, so the reapply generated grants without
   `via:` clauses).
4. `POST /admin/exit-rules/reapply` re-pushed the
   policy with 213 grants including
   `via: ['tag:dev-infra-emilia']` for skyadmin and
   michail users.

**Result:**

```
headscale policy get | jq '.grants | length'
213

# Sample grant (the one that matters):
{
  "src": ["michail@tsnet.skynas.ru"],
  "dst": ["michail@tsnet.skynas.ru:*", "h-user-michail-subnet", "autogroup:internet"],
  "ip": ["*"],
  "via": ["tag:dev-infra-emilia"]    ← basic pinned to emilia
}
```

Tailscale clients on `cyborg` (skyadmin user) and `basic`
(michail user) are now **enforced** to use `emilia` as
their exit node for all internet-bound traffic. YouTube
works.

**Build-time test added** (B237.7 contract):
`TestPlanDevicePrefChange_OrphanUserID` in
`internal/feature/exit_rules/reconciler_b237_7_test.go`
pins that the function must not crash on missing usernames
(e.g. `michail` at `user_id=6` has no `portal_users`
row). 8 new unit tests cover the full decision matrix.

## Issue #3 in detail — wrong Public IP display

**Symptom:** `/admin/derp` showed `Public IP: 172.18.0.3`
(an obviously wrong value — that's the skygate
container's docker-bridge IP, not the public IP anyone
would dial).

**Root cause:** `internal/feature/admin/derp.go`'s
`detectEgressIP()` function dials a UDP discard socket
and reads the local address. That gives the source IP
of the dialing process (the skygate container's egress
on the docker bridge) — **not** the public IP of the
derper. The semantic was wrong: "public IP" should
be "where clients reach us", which is the DNS A record
of the derper's hostname.

**Fix (`B237.2`, commit `f131ef8b`):** New
`resolvePublicDERPIP()` helper tries:
1. `SKYGATE_DERP_HOSTNAME` env var (operator's
   hostname override, e.g. `derp.skynas.ru`).
2. The derper status page's parsed "TLS hostname"
   (when reachable).
3. Last-resort fallback to `detectEgressIP()`.

The DNS A record of `derp.skynas.ru` is `95.165.170.190`
— the public IP Tailscale clients actually dial. A new
`WhiteIPSource` field records which source the resolver
used (`dns:env` / `dns:derper` / `egress`) so the
template can show a small annotation explaining the data
provenance.

**Result:**

```
/admin/derp → "СЕРВИС" panel:
  Публичный IP: 95.165.170.190  (dns:env)
```

## Current `.env` configuration

```ini
# B236 fix (was set during 2026-09-04 incident)
SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate
# (SKYGATE_HOST_REPO_PATH is the pre-existing var; no
# SKYGATE_*_ROUTES env vars are set on skygate-host-1 anymore)

# B237.1 fix
SKYGATE_HEADSCALE_CONFIG_PATH=/home/skyadmin/headscale/config/config.yaml
SKYGATE_HEADSCALE_HOST_DIR=/home/skyadmin/headscale

# B237.2 fix
SKYGATE_DERP_HOSTNAME=derp.skynas.ru

# B237 + B237.7 fix
SKYGATE_ACL_VIA_ENABLED=true
SKYGATE_PREFERRED_RECONCILER_LIVE=true

# Pre-existing (unchanged)
SKYGATE_HEADSCALE_POLL_INTERVAL=168h
SKYGATE_HEADSCALE_VERSION_PIN=0.29.2
DERP_ENABLED=true
DERP_MAP_PORT=8765
HEADSCALE_DERP_URLS=https://controlplane.tailscale.com/derpmap/default
DERP_STUN_PORT=3478
DERP_PRIVATE_KEY=...
DERP_HTTP_PORT=8443
```

## Verification after deploy

```bash
# 1. Tailscale subnet-router status (should be empty on
# all peers)
for ip in 100.64.0.2 100.64.0.3 100.64.0.4; do
  echo "Peer $ip: $(tailscale status --json | python3 -c \
    "import sys, json; d=json.load(sys.stdin); p=d['Peer'].get('$ip', {}); print(len(p.get('PrimaryRoutes', [])))")"
done
# Expected: 0, 0, 0

# 2. DERP latency
ping -c 1 -W 2 95.165.170.190
# Expected: 0-1ms

# 3. Public IP display
curl -s https://derp.skynas.ru/ -o /dev/null -w \
  "code=%{http_code} time=%{time_total}s\n"
# Expected: 200 4-16ms

# 4. Exit-rules reconciler
docker logs skygate-skygate-1 --since 1h | grep preferred-reconcil
# Expected: "starting (interval=1h0m0s, live=true)"

# 5. headscale policy has via: clauses
docker exec headscale headscale policy get | \
  python3 -c "import sys, json; d=json.load(sys.stdin); \
    via_count = sum(1 for g in d.get('grants', []) if 'via' in g); \
    print(f'grants with via: {via_count}')"
# Expected: ~213

# 6. cyborg + basic prefs exist
sudo -u postgres psql -d skygate_staging -c "
SELECT user_id, device_hostname, exit_node_tag, via_enabled
FROM device_exit_node_prefs
WHERE device_hostname IN ('cyborg', 'basic')
ORDER BY device_hostname"
# Expected: cyborg row (user=1) + basic row (user=6), both with
# tag:dev-infra-emilia, via_enabled=1
```

## What changed in the code (commits)

| Commit | What | B-block |
|---|---|---|
| `b17470ba` | `/admin/derp/relays/derpmap.json` endpoint + "Apply to headscale" button + headscale config rewrite | B237 |
| `2a3c106d` | `SKYGATE_HEADSCALE_CONFIG_PATH` env var + fallback path resolver | B237.1 |
| `3f9ef43a` | `SKYGATE_HEADSCALE_HOST_DIR` bind mount (docker-compose) | B237.1 |
| `aed2cea` | `/admin/tailscale` subnet-routes management (form + handler + validation) | B236 |
| `f131ef8b` | `resolvePublicDERPIP` (DNS-based Public IP display) | B237.2 |
| `e2d0b9ec` | B229 default-flip to live + 8 build-time tests | B237.7 |

(B235, B235.1, B235.2, B235.3 are in the chain
`b17470ba…327671b…b755f7d…9c9ccf7`, fixing the
`/admin/derp/dashboard` public-DERP degraded display —
operational impact was less acute but no less
embarrassing.)

## Lessons for the operator

1. **Tailscale subnet-routes are the most common
   misconfiguration source.** Any `tailscale set
   --advertise-routes=X` where X contains a subnet
   the host is itself in is a loop waiting to
   happen. `docs/internal/tailnet-advertised-routes.md`
   documents the hard rule.

2. **DRY-RUN defaults are footguns.** B229 defaulted
   to dry-run (operator must flip `live=true`), which
   meant the system silently logged changes it
   would have made. B237.7 flipped the default to
   `live=true` so the system does the right thing by
   default and operators have to explicitly opt out.

3. **"Public IP" of a derper is the DNS A record.**
   `detectEgressIP()` (UDP-dial local addr) gives
   the dialing process's IP, which is wrong. Use
   `net.LookupHost(derper.Hostname)` and document the
   source (`dns:env` / `dns:derper` / `egress`).

4. **The exit-rules subsystem has 3 layers.**
   `device_rules` (operator's intent) →
   `device_exit_node_prefs` (reconciler-derived
   preferred-exit) → headscale policy grants (with
   `via:` clause). All three must be present for a
   Tailscale client to be enforced-pinned to an
   exit node. Confusing them is what made the
   cyborg+basic YouTube outage invisible for 24h.
   `docs/internal/exit-rules-reconciler.md`
   documents the layers.
