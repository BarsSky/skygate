# Fault-tolerance test plan — v0.26.0

**Goal**: verify that the v0.26.0 health probes (`/healthz`, `/readyz`)
plus the existing graceful-degradation paths actually work under
real failure conditions. NOT testing actual HA (no hot standby yet) —
that's a v0.27.0 Tier-1 thing. This is about catching failure
modes + documenting them for the operator.

**Operator**: skyadmin@192.168.13.69, deploy `v0.25.1-15-ge992c76+e992c76`
(commit e992c76). All tests run live on the VM. Each test takes
5-30 min including the wait for the sidecar's 30s tick / ACL rebuild.

## Test matrix

| # | Scenario | Expected | Status |
|---|---|---|---|
| 1 | `/healthz` + `/readyz` baseline (all up) | 200/200, all checks ok | [ ] |
| 2 | Bring headscale down (docker stop) | `/readyz` returns 503 with headscale="fail" | [ ] |
| 3 | Bring headscale back up | `/readyz` flips back to 200 within 30s | [ ] |
| 4 | SQLite DB locked by another process | `/readyz` returns 503 with db="fail" | [ ] |
| 5 | skygate restart while user has open session | session expires, login again works | [ ] |
| 6 | headscale restart while skygate is running | sidecar auto-recovers within 30s, no manual intervention | [ ] |
| 7 | exit-node (emilia) goes down | exit-node health monitor alerts via Telegram (or logs) | [ ] |
| 8 | subnet-router container killed | sidecar.SyncOnce marks `disabled` within 30s | [ ] |
| 9 | DB file deleted/copied away mid-operation | skygate returns 500 on writes, reads still work via WAL | [ ] |
| 10 | Stress: 100 concurrent /healthz scrapes in 1s | none return 5xx, p99 < 50ms | [ ] |
| 11 | DR restore from backup (docs/disaster-recovery.md) | skygate comes back functional, all 4 prod users present | [ ] |
| 12 | Network partition: skygate can reach DB but not headscale | `/readyz` 503; user requests that need headscale return 500/empty | [ ] |

## How to record results

For each test, capture:
- start_time, end_time
- command sequence
- observed response (HTTP code, body excerpt)
- side effects (DB rows, headscale state, alert messages)
- verdict: PASS / PARTIAL / FAIL + notes

Final report: `docs/fa-test-report-v0.26.0.md`.

## Test order

Run 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12.
Each test should leave the system in a clean state for the next
(stop+start containers, restore DB, etc.).
