# Tailscale subnet-routes on skygate-host-1 (B236)

**Audience:** operator who runs `tailscaled` on a Tailscale node
(skygate-host-1) and needs to understand what `tailscale set
--advertise-routes=` should and shouldn't contain.

**Why this exists (B236 root cause, 2026-09-04):**
`skygate-host-1` was advertising
`172.17.0.0/16,192.168.13.0/24,172.18.0.0/16` as Tailscale
subnet routes. The `192.168.13.0/24` is **the LAN the host
sits in** — the host is **inside** the subnet it was
advertising. That created a routing loop on every other
Tailscale client: `client → 13.69 → 192.168.13.0/24
(via Tailscale) → 13.69 itself → loop`. Symptom: LAN
clients on the same subnet (e.g. `skyworker` at
`192.168.13.20`) lost direct IP access to siblings
(`192.168.13.67` NPM) — Tailscale shadowed the local
Ethernet route.

After B236 (`sudo tailscale set --advertise-routes=""`
+ `sudo systemctl restart tailscaled`), the loop is
broken: `ip route get 192.168.13.1` on `skyworker` now
returns `dev eth0` (direct Ethernet), not
`dev tailscale0 table 52` (Tailscale).

**The hard rule (B236):**

> **Never advertise a subnet that the advertising
> host is itself inside of.** The advertising host sits
> in the LAN it advertises → routing loop on every
> other Tailscale client. This is the most common
> subnet-router misconfiguration.

**What skygate-host-1 should advertise:**

| CIDR | Should advertise? | Why |
|---|---|---|
| `192.168.13.0/24` (the LAN the host is in) | **NO** | skygate-host-1 is in 192.168.13.x — it's the wrong direction (loop). The LAN is reachable via direct Ethernet from every Tailscale client on the same LAN. |
| `172.17.0.0/16` (Docker bridge) | **NO** | The docker bridge is on the host itself; only skygate-host-1 needs to reach it. Remote Tailscale clients have no use for it. |
| `172.18.0.0/16` (Docker custom bridge) | **NO** | Same — host-local. |
| `10.0.1.0/24` (skygate per-user subnet, e.g.) | **YES** (if applicable) | If skygate is the subnet-router for a user's per-user subnet, the Tailscale nodes on that subnet need to be reached via skygate. This is what `docs/internal/subnet-router.md` covers. |
| `0.0.0.0/0` (default route) | **NO** | Subnet router + exit node must be a separate concern. Use `tag:exit-node` for exit-node pinning, not subnet routes. |

**Verification (after B236):**

```bash
# On skygate-host-1:
ip route get 192.168.13.1
# Expected: 192.168.13.1 dev ens18 src 192.168.13.69
# (direct LAN, not tailscale0)

# Verify nothing is being advertised:
tailscale status --json | python3 -c "
import sys, json
d = json.load(sys.stdin)
print('Self.PrimaryRoutes:', d['Self'].get('PrimaryRoutes', []))
print('Self.Prefs.AdvertiseRoutes:', d['Self'].get('Prefs', {}).get('AdvertiseRoutes', []))"
# Expected: both lists empty (no subnet-router mode)

# On skyworker (a LAN client):
ip route get 192.168.13.1
# Expected: via 192.168.13.1 dev eth0 (direct Ethernet, not Tailscale)
```

**Future tooling (B237.8 — planned, not implemented):**
`/admin/tailscale` will surface peer advertised-routes with
markers for "shadowed self-LAN" / "public IP (anti-pattern)"
so the operator can see the problem without SSH'ing into
every peer.

**Related:**
- B229 / B237.7 (`internal/feature/exit_rules/`) — the
  exit-rules reconciler that pins device→exit_node. The
  `via: tag:dev-infra-emilia` clause in headscale grants
  is what makes YouTube on `cyborg`/`basic` use `emilia`
  (not the default route) — different mechanism than
  subnet routes.
- `docs/internal/subnet-router.md` — the per-user
  subnet-router use case (advertise `10.0.6.0/24` for
  user `michail`). **Different** from the self-LAN
  anti-pattern — per-user subnets are *external* to the
  advertising host.
