# Exit-rules architecture + preferred-exit auto-reconciler (B229 / B237.7)

**Audience:** operator who needs to understand how
`device_rules` + `device_exit_node_prefs` + the B229
auto-reconciler work together to pin Tailscale clients
to a specific exit node via `headscale` policy.

## The three layers

There are **three** distinct things in the exit-rules
subsystem, each with a different lifetime and trigger.
Confusing them is what caused the 2026-09-04 cyborg/basic
YouTube outage (B237.7 root cause).

```
┌─────────────────────────────────────────────────────────────┐
│ Layer 1: device_rules                                       │
│ ────────────────────────                                    │
│ WHAT: per-(user, device, exit_node) "allow this traffic  │
│       on this exit node" rules. E.g.                        │
│       (user=1 cyborg  emilia  youtube.com)                 │
│       (user=6 basic   emilia  142.250.0.0/15)              │
│ TRIGGER: created via /my/exit-rules or admin/derp.         │
│ LIFETIME: permanent (the operator's intent).                │
│ ENFORCEMENT: headscale policy grants (if the            │
│              device's preferred-exit matches).            │
└─────────────────────────────────────────────────────────────┘
            ▲                              │
            │ auto-create                  │ auto-pin
            │ (B229, unanimous)            │ (the only purpose)
            │                              ▼
┌─────────────────────────────────────────────────────────────┐
│ Layer 2: device_exit_node_prefs (the preferred-exit)        │
│ ──────────────────────────────────────────────            │
│ WHAT: per-(user, device) "this device should use         │
│       exit node X for ALL internet-bound traffic".        │
│       E.g. (user=1 cyborg  tag:dev-infra-emilia via=1).  │
│ TRIGGER: B229 auto-reconciler (every 1h) OR manual         │
│          /my/devices/preferred-exit click.                 │
│ LIFETIME: updated when device's dominant exit_node        │
│          changes (stale tag → canonical).                  │
│ ENFORCEMENT: headscale policy grant's `via:` clause.      │
│              Without this, the device_rules grant is     │
│              decorative (the per-CIDR grant exists but    │
│              has no `via:` clause, so Tailscale client    │
│              picks default exit, not the user's choice).  │
└─────────────────────────────────────────────────────────────┘
            ▲
            │ derived from device_rules (the only source
            │ the reconciler looks at)
            │
┌─────────────────────────────────────────────────────────────┐
│ Layer 3: headscale policy grants                           │
│ ───────────────────────────────                           │
│ WHAT: the final rendered HuJSON. Each grant is:          │
│       {src: [michail@...],                                │
│        dst: [..., autogroup:internet],                    │
│        via: [tag:dev-infra-emilia]}                       │
│ TRIGGER: POST /admin/exit-rules/reapply.                   │
│ LIFETIME: overwritten on each reapply.                     │
│ ENFORCEMENT: Tailscale packet filter. If a client tries   │
│              to use a different exit node for internet     │
│              traffic, the packet is dropped.                │
└─────────────────────────────────────────────────────────────┘
```

**The flow operator-side**: create rules in
`/my/exit-rules` (Layer 1) → reconciler auto-creates
the preferred-exit (Layer 2) → reapply pushes the
`via:` clause to headscale (Layer 3) → Tailscale
client is pinned to the exit node.

## The decision function (B229 / B237.7)

`PlanDevicePrefChange` is the pure function that decides
what to do with each `(user, device_hostname)` pair.
Eight cases are unit-tested in
`internal/feature/exit_rules/reconciler_b237_7_test.go`:

| Case | State | Decision | Reason |
|---|---|---|---|
| 1 | empty pref, 1 distinct exit_node, valid CanonicalTag | **CREATE** | `missing-pref-unanimous` |
| 2 | empty pref, 2+ distinct exit_nodes | **SKIP** | `missing-pref-split` (operator must pick) |
| 3 | empty pref, no CanonicalTag (host deleted from node_owner_map) | **no-op** | don't clobber — host may return |
| 4 | existing pref with wrong tag | **UPDATE** | `stale-tag` (e.g. operator typed `tag:dev-michail-basic` instead of the canonical `tag:dev-infra-emilia`) |
| 5 | existing pref canonical, via=0 (V061 migration's intentional skip) | **UPDATE** | `via-disabled-but-canonical` |
| 6 | existing pref canonical, via=1 | **no-op** | no churn in audit log |
| 7 | orphan user_id (michail — no portal_users row) | **CREATE** (with empty Username) | the B237.7 root cause category — must not crash on missing usernames |
| 8 | `SKYGATE_PREFERRED_RECONCILER_LIVE` unset | **live=true** | B237.7 default-flip; opt-out via `false/0/no/off` |

## Live mode (B237.7 — DEFAULT TRUE)

```go
func PreferredExitReconcilerLive() bool {
    v := strings.ToLower(strings.TrimSpace(os.Getenv("SKYGATE_PREFERRED_RECONCILER_LIVE")))
    if v == "false" || v == "0" || v == "no" || v == "off" {
        return false
    }
    return true // default: live / auto-reconcile ON
}
```

**B237.7 flipped the default** from `false` (DRY-RUN) to
`true` (LIVE). The original false default was a footgun:
the operator never knew `SKYGATE_PREFERRED_RECONCILER_LIVE`
had to be set explicitly. For ~24h the reconciler ran
every hour on cyborg+basic, correctly identified
"unanimous emilia → CREATE pref", and **silently logged
without writing** because of the false default. YouTube
was decorative.

Operators who want dry-run can still set
`SKYGATE_PREFERRED_RECONCILER_LIVE=false` to opt out
(re-read on every tick, no redeploy needed).

## Reconciler scheduling

| Mode | Source | Interval |
|---|---|---|
| Enabled via env (`SKYGATE_PREFERRED_RECONCILE_ENABLED=true/false/1/0/yes/no`) | runtime override | per-tick |
| Enabled via DB (`global_settings.preferred_reconcile_enabled`) | per-tick override | per-tick |
| Default | `true` (B229) | per-tick |

Default interval: `SKYGATE_PREFERRED_RECONCILE_INTERVAL=1h`.
Set to `0` to disable startup goroutine entirely.

## Live-verify checklist

After adding new `device_rules`, wait for next tick or
force a reapply:

```bash
# 1. Reconciler ran in live mode (not dry-run)?
docker logs skygate-skygate-1 --since 1h 2>&1 | \
  grep "preferred-reconciler: starting"
# Expect: "...(interval=1h0m0s, live=true)"

# 2. The new pref exists?
SELECT user_id, device_hostname, exit_node_tag, via_enabled
FROM device_exit_node_prefs
WHERE device_hostname IN ('cyborg', 'basic')
ORDER BY device_hostname;
# Expect: cyborg / basic rows with tag:dev-infra-emilia, via=1

# 3. Reapply headscale policy
curl -s -c /tmp/c.txt -b /tmp/c.txt \
  --data-urlencode "username=skyadmin" --data-urlencode "password=admin" \
  https://skygate.skynas.ru/login >/dev/null
CSRF=$(grep -oE 'skygate_ts_csrf\s+[A-Za-z0-9]+' /tmp/c.txt | head -1 | awk '{print $2}')
curl -s -c /tmp/c.txt -b /tmp/c.txt -X POST \
  --data-urlencode "csrf=$CSRF" \
  https://skygate.skynas.ru/admin/exit-rules/reapply
# Expect 303 → /admin/exit-rules?ok=applied

# 4. Verify grant has via: tag
docker exec headscale headscale policy get | python3 -c "
import sys, json
d = json.load(sys.stdin)
for g in d.get('grants', []):
    if 'via' in g and 'tag:dev-infra-emilia' in g.get('via', []):
        print('OK:', g)"
```

## Build-time tests (B237.7 contract)

8 unit tests in `internal/feature/exit_rules/reconciler_b237_7_test.go`
+ updated B229 tests in `reconciler_b229_test.go`:

```bash
go test -count=1 -short ./internal/feature/exit_rules/...
# Expect: ok skygate/internal/feature/exit_rules
```

**Critical test for the B237.7 root cause**:
`TestPlanDevicePrefChange_OrphanUserID` pins the contract
that user_id without a `portal_users` row (e.g. `michail`
at id=6) must still produce a CREATE change. Any future
change that re-introduces the silent swallow of missing
usernames will fail this test.

## Related

- `docs/derp.md` — DERP relay config; the DERP map is
  independent of the exit-rules subsystem.
- B236 / `docs/internal/tailnet-advertised-routes.md` —
  Tailscale subnet routes (a different layer; this
  doc covers exit-node pinning, B236 covers LAN access).
- `internal/feature/exit_rules/form_reapply.go` — the
  POST /admin/exit-rules/reapply handler.
- `internal/feature/exit_rules/reconciler.go` — the
  B229 reconciler loop.
