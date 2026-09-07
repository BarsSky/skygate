#!/bin/bash
# scripts/check_b237_17.sh — B237.17 (v1.5.2+) smoke-artifact
# cleanup contract (TD-9 from docs/PLANS.md).
#
# Pins the script + systemd unit + crontab pattern that
# keeps the live skygate DB from accumulating smoke.sh
# test artifacts (portal_users with username LIKE
# 'smoke_mesh_%' + meshes with name LIKE 'smoke-mesh-%').
# Pre-B237.17, the only path was: re-run smoke.sh to
# completion (which re-runs step 13.8 cleanup), OR the
# operator manually DELETE'd rows via psql. A
# failed/interrupted smoke.sh run left 2 rows per
# incident, accumulating over weeks.
#
# Why this check is small (only 11 contracts):
#   - the script's correctness is the SQL's correctness
#     (we use a tested transaction pattern + CASCADE; the
#     rest is bash)
#   - the systemd unit is mechanical (Type=oneshot, runs
#     the script, no Restart)
#   - the B237.15 B-check (deployment variants) already
#     pins install-{debian,rh}.sh's systemd unit template;
#     this is just a sibling for the cleanup service
#
# Why "skymate" (not "skygate") in the unit file names:
#   - to avoid colliding with the future skygate-watchdog
#     service (B203) when HA adds more systemd units to
#     this host. The watchdog + the cleanup are both
#     "skygate-internal" services that the operator
#     doesn't see day-to-day; using a different prefix
#     makes the unit list cleaner.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. The script itself ---

# A.1 the script exists
if [ -x scripts/cleanup_smoke_artifacts.sh ]; then
    ok "A.1 scripts/cleanup_smoke_artifacts.sh exists and is executable"
else
    bad "A.1 scripts/cleanup_smoke_artifacts.sh missing or not executable"
fi

# A.2 the script uses the canonical sudo -u postgres psql
# pattern (matches clear_test_dsn.sh:36, the other
# operator-side psql helper)
if grep -q 'sudo -u postgres psql' scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.2 script uses sudo -u postgres psql (matches clear_test_dsn.sh pattern)"
else
    bad "A.2 script must use sudo -u postgres psql (operator-side canonical pattern)"
fi

# A.3 the script targets skygate_staging (the live DB
# name; matches clear_test_dsn.sh:36)
if grep -q 'skygate_staging' scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.3 script targets the skygate_staging DB (matches clear_test_dsn.sh:36)"
else
    bad "A.3 script must target the skygate_staging DB explicitly (matches clear_test_dsn.sh)"
fi

# A.4 the script queries BOTH portal_users (smoke_mesh_*)
# AND meshes (smoke-mesh-*) — those are the 2 places
# smoke.sh leaves artifacts
if grep -q "smoke_mesh_%" scripts/cleanup_smoke_artifacts.sh 2>/dev/null && \
   grep -q "smoke-mesh-%" scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.4 script covers BOTH smoke artifact types (users + meshes)"
else
    bad "A.4 script must cover BOTH smoke_mesh_% users AND smoke-mesh-% meshes"
fi

# A.5 the deletion is wrapped in BEGIN/COMMIT (defense
# against partial deletes)
if grep -q '^BEGIN;' scripts/cleanup_smoke_artifacts.sh 2>/dev/null && \
   grep -q '^COMMIT;' scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.5 script wraps the deletion in BEGIN/COMMIT (atomic)"
else
    bad "A.5 script must wrap the deletion in BEGIN/COMMIT (no partial deletes)"
fi

# A.6 the script uses a 24h grace window (don't kill
# smoke.sh runs that started 5 minutes ago)
if grep -q "INTERVAL '24 hours'" scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.6 script has a 24h grace window (won't kill in-flight smoke.sh runs)"
else
    bad "A.6 script must use a 24h grace window (don't kill in-flight smoke.sh)"
fi

# A.7 the script writes an audit_log row (operator can see
# "when did the last cleanup happen" via /admin/audit)
if grep -q 'smoke_artifacts_purge' scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.7 script writes an audit_log row (action=smoke_artifacts_purge)"
else
    bad "A.7 script must write an audit_log row (operator visibility via /admin/audit)"
fi

# A.8 the script is idempotent (running twice in a row
# is safe — finds 0 candidates on the second run)
if grep -q 'idempotent' scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.8 script documents idempotency (safe to re-run)"
else
    bad "A.8 script must document idempotency in the header"
fi

# A.9 bash syntax check
if bash -n scripts/cleanup_smoke_artifacts.sh 2>/dev/null; then
    ok "A.9 scripts/cleanup_smoke_artifacts.sh has valid bash syntax"
else
    bad "A.9 scripts/cleanup_smoke_artifacts.sh has bash syntax errors"
fi

# --- B. The systemd unit + timer (Option A from the header) ---

# B.1 service file exists
if [ -f deploy/systemd/skymate-cleanup-smoke.service ]; then
    ok "B.1 deploy/systemd/skymate-cleanup-smoke.service exists"
else
    bad "B.1 deploy/systemd/skymate-cleanup-smoke.service missing"
fi

# B.2 timer file exists
if [ -f deploy/systemd/skymate-cleanup-smoke.timer ]; then
    ok "B.2 deploy/systemd/skymate-cleanup-smoke.timer exists"
else
    bad "B.2 deploy/systemd/skymate-cleanup-smoke.timer missing"
fi

# B.3 service has Type=oneshot (run-and-done, no forking)
if grep -q '^Type=oneshot' deploy/systemd/skymate-cleanup-smoke.service 2>/dev/null; then
    ok "B.3 service is Type=oneshot (matches the cron-job pattern)"
else
    bad "B.3 service must be Type=oneshot"
fi

# B.4 timer has OnCalendar (the actual schedule)
if grep -q '^OnCalendar=' deploy/systemd/skymate-cleanup-smoke.timer 2>/dev/null; then
    ok "B.4 timer has OnCalendar (daily schedule)"
else
    bad "B.4 timer must have OnCalendar (otherwise the timer never fires)"
fi

# B.5 timer has RandomizedDelaySec (avoid fleet-wide
# thundering herd if the operator runs this on multiple
# skygate hosts)
if grep -q '^RandomizedDelaySec=' deploy/systemd/skymate-cleanup-smoke.timer 2>/dev/null; then
    ok "B.5 timer has RandomizedDelaySec (avoids fleet-wide thundering herd)"
else
    bad "B.5 timer should have RandomizedDelaySec (multi-host HA deployments)"
fi

# B.6 timer has Persistent=true (catch up on missed
# runs if the host was off at 04:00)
if grep -q '^Persistent=true' deploy/systemd/skymate-cleanup-smoke.timer 2>/dev/null; then
    ok "B.6 timer is Persistent=true (catches up after VM reboot)"
else
    bad "B.6 timer should be Persistent=true (catches up after VM reboot)"
fi

# --- C. verify_pre_deploy.sh registration ---

# C.1 the check is registered
if grep -q 'check_b237_17' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "C.1 scripts/verify_pre_deploy.sh includes the B237.17 check"
else
    bad "C.1 scripts/verify_pre_deploy.sh must include the B237.17 check (see the run_check block for B237.17)"
fi

# C.2 AGENTS.md mentions B237.17
if grep -q 'B237\.17' AGENTS.md 2>/dev/null; then
    ok "C.2 AGENTS.md documents B237.17 (so future agents know the cleanup contract is pinned)"
else
    bad "C.2 AGENTS.md must document B237.17 (B-check convention)"
fi

# --- D. PLANS.md update ---

# D.1 TD-9 marked DONE in PLANS.md
# The DONE marker can be in different forms:
#   - "Status:** DONE in ..."
#   - "Status:** DONE-ONCE; ..." (with a follow-up note)
#   - "v1.5.3 — Subnet-router auto-cleanup cron (TD-9) — DONE in v1.4.3"
# Any of these count. We just need to confirm the section
# isn't still in the "DEFERRED" state.
if grep -B1 -A2 'TD-9' docs/PLANS.md 2>/dev/null | grep -qE 'DONE\b'; then
    ok "D.1 docs/PLANS.md marks TD-9 as DONE"
else
    bad "D.1 docs/PLANS.md should mark TD-9 as DONE (B237.17 closed it)"
fi

# --- Summary ---

echo
echo "=== B237.17 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
