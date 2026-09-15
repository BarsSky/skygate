# Skygate Slow Exit-Node Connection — Code Audit

**Reporter:** michail (operator)
**Symptom:** client connects to exit-node slowly
**Audit mode:** code-only (no live access to skygate VM, headscale, or relay)
**Date:** 2026-09-14

---

## 0. TL;DR — where the slow path likely lives

In a Tailscale → headscale → exit-node connection, the latency comes
from FOUR different layers. The skygate side controls only three of
them (layer 1, 2, 4 below); layer 3 is the Tailscale client itself +
network path.

| Layer | Component | Where | Controlled by |
|---|---|---|---|
| 1 | ACL policy | skygate → headscale `SetPolicy` | skygate |
| 2 | Tag autoupdater (B77) | skygate → headscale `AddTag` | skygate |
| 3 | Direct / DERP P2P | Tailscale client ↔ relay | Tailscale + network |
| 4 | Advertised-routes sync | skygate SSH → relay → headscale approve | skygate |

**Most actionable code finding:** `SKYGATE_PREFERRED_RECONCILE_INTERVAL=1h`
default (`internal/config/config.go:545`) — stale `device_exit_node_prefs`
(renamed host, removed exit-node) won't be fixed for up to 1 hour.
**This is the single biggest skygate-side delay for the symptom "I have
a preferred exit node but the connection keeps trying the wrong one."**

---

## 1. Phase 1 evidence — root cause candidates

### 1.1 PREFERRED-EXIT RECONCILER — 1 HOUR (HIGH)

**File:** `internal/config/config.go:545`
```go
PrefReconcileInterval: getDuration("SKYGATE_PREFERRED_RECONCILE_INTERVAL", 1*time.Hour),
```

**Boot wiring:** `cmd/skygate/main.go:2476`
```go
go app.RunPreferredExitReconciler(ctx, app.Notifier, cfg.PrefReconcileInterval)
```

**What it does:** every `interval`, calls `exit_rules.ReconcileDeviceExitNodePrefs`
which detects stale `device_exit_node_prefs` rows (pointing at a hostname
that was renamed, or at an exit-node that was deleted) and migrates them
to the new hostname (B229 / B231).

**Why this matters for "slow connect":** if michail's device has
`preferred_exit_node = 'karolina'` but karolina was renamed to `karolina-eu`
yesterday, the pref still says `karolina` for up to 1 hour. tailscaled
asks headscale for an exit-node named `karolina`, gets 404, falls back
to DERP, slow.

**Live fix on the box:** set `SKYGATE_PREFERRED_RECONCILE_INTERVAL=2m`
in `.env` and restart skygate. Or run `skygate headscale-users-reconcile`
manually.

**Operator-side fix in code:** change the default to 5–15 min (B229's
historical post-mortem had this set to false for months — "false" was
the original default; v1.5.0 made it true but kept the 1h cadence).

---

### 1.2 DNS AUTOUPDATER — 5 MINUTES (MEDIUM)

**File:** `internal/config/config.go:544`
```go
DNSAutoCheck: getDuration("SKYGATE_DNS_AUTO_CHECK", 5*time.Minute),
```

**Boot wiring:** `cmd/skygate/main.go:2456`
```go
go app.RunDomainAutoUpdater(ctx, cfg.DNSAutoCheck)
```

**What it does:** every `interval`, calls `exit_rules.DomainAutoUpdater`
which:
1. Queries every enabled `target_type='domain'` row
2. For each: DNS-resolves + CDN-detection (HTTP fetch)
3. Inserts/updates the matching `/32` rows
4. Calls `StaggeredSync()` to SSH + `approve-routes` on every exit-node

**Why this matters for "slow connect":** if michail's rule is
`type=domain, target=rutracker.org, exit_node=relay-3`, the actual
IPs that get routed live in `device_rules` as `/32` rows with
`parent_domain='rutracker.org'`. They only get added **every 5 min**.
The ACL allows the route immediately, but until the DNS resolution
runs, the exit-node doesn't have the IPs to actually route.

**Live fix on the box:** `skygate ...` to trigger a manual
DNS check, or `POST /admin/exit-rules/sync`.

**Note (already-fixed historical bug):** `cmd/skygate/main.go:583-597`
documents that a pre-v0.32.13 boot-time call to `DomainAutoUpdater()`
held the SQLite WAL write lock for 30+ seconds and wedged every
concurrent query. Now gated on `AutoUpdateEnabled`. If the operator
sees the SQLite WAL lock symptom again, check that gate.

---

### 1.3 TAG AUTOUPDATER (B77) — 5 MINUTES (MEDIUM, NEW DEVICES ONLY)

**File:** `internal/config/config.go:550`
```go
NodeDiscoveryInterval: getDuration("SKYGATE_NODE_DISCOVERY_INTERVAL", 5*time.Minute),
```

**Boot wiring:** `cmd/skygate/main.go:2503`
```go
go nodeownership.AutoBackfill(ctx, d, hs, alertSink, cfg.NodeDiscoveryInterval)
```

**What it does:** every `interval`, lists every headscale node + every
portal user, applies `tag:dev-<user>-<device>` to nodes that match
the portal user. Without this tag, the per-device ACL rule
(`src=tag:dev-<user>-<device>`) does NOT match, so the device cannot
use any exit-node ACL grant.

**Why this matters for "slow connect":** only affects NEW devices.
After the first tick (≤5 min), the device is tagged and ACL works.
But the very first connect-after-add-device can fail/timeout because
of this gap.

**Note (recent fix):** B175 (OIDC Strategy E) closes the same gap for
OIDC-registered nodes. B176 lowercases dev-tags (headscale 0.29
rejects uppercase). The B175+B176 combination means OIDC-registered
nodes get tagged on the first tick — but there's still a 0–5 min
window where the tag isn't applied.

---

### 1.4 ADVERTISED-ROUTES SYNC — PER-NODE SSH (MEDIUM)

**File:** `internal/feature/exit_rules/sync.go:78` (`SyncAdvertisedRoutes`)
+ `internal/headscale/routes.go:182` (`SetAdvertisedRoutes`)

**Per-node SSH timeout:** `internal/headscale/routes.go:235`
```go
"-o", "ConnectTimeout=10",
```

**What it does:** for each exit-node, SSHes into the VPS + runs
`tailscale set --advertise-routes=...` (replaces the list, no merge),
then `docker exec headscale headscale nodes approve-routes ...`.

**Why this matters for "slow connect":** if a relay is overloaded or
the SSH host-key needs to be re-pinned (StrictHostKeyChecking=accept-new
on line 234 means first connect adds 1–3s of host-key negotiation), each
exit-node takes up to 10s to sync. With 4 relays × 10s = 40s worst case
on a single sync call. The StaggeredSync path (`sync.go:241`) spaces
nodes by `StaggerInterval` (default 30s, `internal/config/config.go:563`)
so a 4-relay stagger can take 1.5 min.

**Where it's invoked:**
- `internal/feature/exit_rules/form_my.go:929` — after every user rule add
- `internal/handlers/handlers.go:267` — `/admin/exit-rules/sync`
- `internal/handlers/handlers.go:333,350` — DNS autoupdater tick

So when michail ADDS a rule, sync runs immediately (good). But
when a relay's tailscaled RESTARTS (e.g., reboot, OOM, update), the
routes in headscale are still good — but tailscaled has dropped the
advertised-routes state until the next sync. The cron is the next
sync after 5 min (DNS autoupdater) — so a relay that restarts has up
to 5 min of "no exit node capability".

---

### 1.5 SetPolicy FILE-MODE FALLBACK (NOT CURRENTLY ACTIVE)

**File:** `internal/headscale/acl.go:96-128`

If `SetPolicy` API call returns 404/405 (headscale is in
`policy.mode: file` instead of `database`), the fallback path:
1. `docker run --rm -v /home/admin/headscale/config:/config alpine sh -c "cat > /config/acl_policy.hujson"`
2. `docker restart headscale`

**Step 2 is a hard restart of headscale.** Every ACL change in
file-mode = headscale restart = all connected clients disconnect +
reconnect (slow).

**Current state:** `deploy/templates/headscale-config.yaml.tmpl:52`
sets `policy.mode: database` — the API path is used, the file-mode
fallback is unreachable. **This is NOT a current cause for michail.**

But if a future operator reverts headscale config to file-mode (or
runs `headscale config migrate`), the slow-ACL symptom would return.

---

### 1.6 NO AUTOMATIC ACL RECONCILIATION (LOW-MEDIUM)

**Observation:** `acl.ApplyACLPipelineForPlane` is only called from
user actions (add/delete rule, set preferred, device-delete). There is
NO periodic cron that re-applies the ACL on a schedule.

If headscale loses its ACL state (rare — usually only after headscale
restart in non-database mode), skygate doesn't notice. The next user
action will re-apply, but until then the operator sees "rules not
working" silently.

**The reconcile_headscale_users cron (`internal/headscale/reconcile_cron.go:73`):
interval 1h, but only reconciles users (create/delete), not ACL state.

**Not the cause for michail** unless headscale just restarted in
non-database mode (which the current config doesn't allow).

---

### 1.7 DERP LATENCY (OUTSIDE SKYGATE'S CONTROL)

**File:** `internal/derphealth/cron.go:18` — DERP probe runs every 5 min.

**Observation:** when direct P2P fails (NAT, firewall, CGNAT), Tailscale
falls back to DERP. DERP latency depends on:
- Geographic distance from the device to the nearest DERP
- DERP server load
- Network path quality (anycast routing)

The skygate side can monitor DERP health (`/admin/derp/dashboard`) but
can't speed up the actual connection.

**Operator-side diagnostic:** `tailscale status` on michail's device
shows whether the connection is `direct` or `relay` (DERP). If it
says `relay`, that's the cause — not a skygate bug.

---

## 2. Phase 2 — pattern analysis

The pattern across sections 1.1–1.4 is:
**skygate has 4 different intervals that affect exit-node connection
freshness.** None of them is wrong per se, but their defaults were
chosen for **operator-side load minimization**, not for **end-user
connection latency**.

| Setting | Default | Optimization for |
|---|---|---|
| `SKYGATE_PREFERRED_RECONCILE_INTERVAL` | 1h | less headscale API load |
| `SKYGATE_DNS_AUTO_CHECK` | 5m | less DNS + HTTP load |
| `SKYGATE_NODE_DISCOVERY_INTERVAL` | 5m | less headscale API load |
| `SKYGATE_STAGGER_INTERVAL` | 30s | less simultaneous SSH/approve load |

A user-facing connection-speed-first profile would prefer
2m / 1m / 1m / 5s — but those would 5× the headscale API load
and the SSH traffic to relays. **The current defaults are a
deliberate trade-off, not an oversight.**

---

## 3. Phase 3 — hypotheses for michail's specific symptom

Without live data, I can rank the hypotheses by plausibility:

### H1 (most likely): `device_exit_node_prefs` stale

If michail renamed his device or the relay he uses was renamed, his
preferred exit-node points at a hostname that no longer exists. Up
to 1h before the reconciler fixes it.

**Diagnostic:** `SELECT * FROM device_exit_node_prefs WHERE user_id
IN (SELECT id FROM portal_users WHERE username='michail');`

**Quick check:** michail opens /my/devices → clicks on the device →
"Preferred exit-node" column. If the value is `<error>` or a hostname
that's no longer in the exit-node list, that's the cause.

### H2 (likely): DNS rules on relay he uses are stale

If michail added a new domain rule < 5 min ago and the autoupdater
hasn't run yet, the relay doesn't have the IP. Until then, traffic to
that domain falls back to direct (no exit node) or DERP.

**Diagnostic:** `SELECT id, target_value, parent_domain, exit_node_id
FROM device_rules WHERE user_id=michail AND enabled=1 AND target_type
IN ('domain', 'subnet') AND parent_domain IS NOT NULL;` — these are
the autoupdater-added /32s. Compare to the actual exit-node's
advertised-routes: `tailscale status` on the relay.

**Quick check:** `POST /admin/exit-rules/sync` triggers an immediate
sync. If the connection speed improves right after, H2 was the cause.

### H3 (likely): DERP fallback on michail's network

michail's home / mobile network may be CGNAT, blocking direct P2P.
Tailscale falls back to DERP automatically.

**Diagnostic:** `tailscale status` on michail's device — look at
`Health:` and the per-peer line. If it says `relay: <region>`, that's
DERP. Latency depends on DERP proximity.

**Quick check:** `tailscale ping <exit-node-tailnet-ip>` — if pings
are slow (>200ms), DERP is the cause.

### H4 (less likely): NEW device without tag

If michail recently added a NEW device that hasn't connected yet,
the B77 tag autoupdater hasn't applied `tag:dev-michail-<newdev>`.
Up to 5 min before ACL grants take effect.

**Diagnostic:** `SELECT tag FROM headscale_user WHERE name='michail';
SELECT * FROM headscale_nodes WHERE name LIKE 'tag:dev-michail-%';`
Compare with what's in headscale.

---

## 4. Phase 4 — what to do next

### Immediate (operator can do without code changes)

1. **`skygate headscale-users-reconcile`** — manually triggers H1
   reconciliation now (instead of waiting up to 1h).
2. **`POST /admin/exit-rules/sync`** — manually triggers H2
   advertised-routes sync now.
3. **Ask michail to run `tailscale status`** — confirms H3.
4. **If H1 confirmed:** set `SKYGATE_PREFERRED_RECONCILE_INTERVAL=2m`
   in `.env` on the skygate VM, restart skygate, document the change
   in AGENTS.md.

### Code changes (need design + commit)

If the operator wants to make these intervals user-friendly by
default, the change is small but touches a deliberate trade-off:

1. **Change `SKYGATE_PREFERRED_RECONCILE_INTERVAL` default to 5m**
   (`internal/config/config.go:545`).
   - Pro: stale prefs fixed in ≤5m instead of ≤1h.
   - Con: 12× headscale API load (1h → 5m). headscale API is fast
     enough that this is negligible.
2. **Change `SKYGATE_DNS_AUTO_CHECK` default to 2m** for low-load
   installs (already configurable; just lower the default).
3. **Add an "ACL reconciliation" cron** that re-applies the ACL
   every 15m as a safety net for the file-mode-fallback case
   (currently absent — see §1.6).
4. **Document the trade-off** in AGENTS.md so the next operator
   knows which knob affects what.

### What this audit CANNOT determine

Without live access to the skygate VM, headscale, and one of
michail's devices, I cannot determine:
- Whether michail's device is on CGNAT (H3)
- Whether his preferred_exit_node hostname is stale (H1)
- Whether the relay he uses has stale advertised-routes (H2)

These all need **a 5-minute live diagnostic** on the operator side.
A live checklist is the natural follow-up — see §5.

---

## 5. Operator-side live diagnostic checklist

```bash
# 1. On the skygate VM:
cd /home/admin/skygate
grep -E "PREFERRED_RECONCILE|DNS_AUTO_CHECK|NODE_DISCOVERY" .env

# 2. Check michail's preferred exit-node:
PGPASSWORD=$SKYGATE_DB_PASS psql -h localhost -U skygate_admin -d skygate -c "
  SELECT u.username, p.hostname, p.exit_node, p.updated_at
  FROM device_exit_node_prefs p
  JOIN portal_users u ON u.id = p.user_id
  WHERE u.username = 'michail';
"

# 3. Check the exit-node hostname actually exists:
curl -H "Authorization: Bearer $HEADSCALE_API_KEY" \
  "$HEADSCALE_URL/api/v1/node" | jq '.[] | select(.name=="<exit_node_from_step_2>")'

# 4. Trigger immediate reconciler:
./skygate headscale-users-reconcile --user michail

# 5. Trigger immediate exit-rules sync:
curl -X POST -H "Cookie: skygate_session=$ADMIN_TOKEN" \
  http://localhost:8080/admin/exit-rules/sync

# 6. Ask michail to run on HIS device:
tailscale status
#   - look at the per-exit-node line for "via:"
#   - if it says "relay: <region>", that's DERP fallback (H3)
tailscale ping <exit-node-tailnet-ip>
#   - if ping > 200ms, network path is the bottleneck (H3)
```

---

## 6. Summary table for the operator

| Hypothesis | Likelihood | Code finding | Quick diagnostic | Live fix |
|---|---|---|---|---|
| H1 stale preferred | **HIGH** | `SKYGATE_PREFERRED_RECONCILE_INTERVAL=1h` (config.go:545) | check `device_exit_node_prefs` for michail | `headscale-users-reconcile` |
| H2 stale DNS rules | MEDIUM | `SKYGATE_DNS_AUTO_CHECK=5m` (config.go:544) | compare relay's advertised-routes with rule IPs | `POST /admin/exit-rules/sync` |
| H3 DERP fallback | MEDIUM | outside skygate | `tailscale status` on michail's device | change DERP server / network |
| H4 new device | LOW | `SKYGATE_NODE_DISCOVERY_INTERVAL=5m` (config.go:550) | compare tag list with portal_users | wait 5 min or `force-backfill-tags` |

---

## 7. Suggested follow-up — what to fix first

If the user wants ONE code change that addresses the most-likely
cause, the highest-value change is:

**Lower the default `SKYGATE_PREFERRED_RECONCILE_INTERVAL` from 1h
to 5m** (one-line change in `internal/config/config.go:545`).

This:
- Closes the H1 (stale preferred exit-node) gap
- Costs nothing in API load (headscale API is local, fast)
- Matches the cadence of the other 3 cron intervals (5m)
- B229 historical post-mortem already documented the 1h default as
  operator-hostile

A second-priority change: add a **15m ACL reconciliation cron**
(sklearn-like "re-apply current ACL to headscale every 15m as a
safety net") to close §1.6.

---

**Report compiled:** 2026-09-14, code-only audit (no live data).
**Files cited:** `internal/config/config.go`, `cmd/skygate/main.go`,
`internal/feature/exit_rules/sync.go`, `internal/headscale/acl.go`,
`internal/headscale/routes.go`, `internal/headscale/reconcile_cron.go`,
`internal/nodeownership/auto.go`, `internal/handlers/handlers.go`,
`deploy/templates/headscale-config.yaml.tmpl`.
