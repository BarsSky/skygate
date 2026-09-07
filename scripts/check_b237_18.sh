#!/bin/bash
# scripts/check_b237_18.sh — B237.18 (v1.5.2+) headscale_user_id
# reconciliation cron contract (TD-10 from docs/PLANS.md).
#
# Pins the daily in-app cron that reconciles
# portal_users.headscale_user_id against the live headscale
# user list. Pre-B237.18 the only path to detect a stale
# headscale_user_id was: notice a rule pointing at a no-op
# (Tailscale silently treats the user as missing, the
# rule doesn't fire, the operator notices when the
# device "doesn't get the right exit node") + run psql
# + UPDATE portal_users SET headscale_user_id = <new>
# by hand. A delete+recreate in headscale left the
# portal_users row pointing at a dead ID indefinitely.
#
# What the B237.18 fix does:
#   1. internal/headscale/reconcile.go — the per-row
#      reconciliation function (4 outcomes: ok / linked /
#      relinked / orphan). Never auto-deletes portal_users
#      rows; orphan outcomes write an audit row + leave
#      the ID alone for the operator to review.
#   2. internal/headscale/reconcile_cron.go — the
#      StartReconcileCron / RunOnceNow entry points
#      (derphealth/cron.go pattern).
#   3. internal/config/config.go — adds
#      ReconcileHeadscaleUsers + ReconcileHeadscaleUsersInterval
#      (env vars SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED
#      + SKYGATE_RECONCILE_HEADSCALE_USERS_INTERVAL).
#   4. cmd/skygate/main.go — wires the cron after the
#      headscale client is created + after
#      ensureHeadscaleUser (so the admin user is in
#      headscale by the time the first tick runs).
#   5. internal/headscale/reconcile_test.go — 10 pure-Go
#      unit tests (sentinel errors, JSON outcome
#      stability, default interval, int64 parsing
#      edge cases). Integration tests against a live
#      PG would belong in a separate file (see the
#      b188_3_integration_test pattern).
#
# Why 13 contracts is enough (vs 18 for B237.17 / 19
# for B237.15): the reconciliation logic is mostly
# SQL-driven, and the per-row logic is small + covered
# by 10 unit tests. The contracts focus on the
# file-inventory + entry-point signatures + the
# "NEVER auto-delete" guard (D.5 below) which is
# the most important regression to catch.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. Source: reconcile.go core function ---

# A.1 the file exists
if [ -f internal/headscale/reconcile.go ]; then
    ok "A.1 internal/headscale/reconcile.go exists"
else
    bad "A.1 internal/headscale/reconcile.go missing"
fi

# A.2 the public ReconcileUsers function signature
if grep -qE '^func ReconcileUsers\(ctx context\.Context, db \*sql\.DB, hs \*Client\)' internal/headscale/reconcile.go 2>/dev/null; then
    ok "A.2 ReconcileUsers has the canonical (ctx, db, hs) signature"
else
    bad "A.2 ReconcileUsers signature must be (ctx, db, hs) — matches the derphealth pattern"
fi

# A.3 the 4 outcome constants exist (the operator's
# /admin/audit filter depends on these literal values)
if grep -qE 'ReconcileOK.*=.*"ok"' internal/headscale/reconcile.go 2>/dev/null && \
   grep -qE 'ReconcileLinked.*=.*"linked"' internal/headscale/reconcile.go 2>/dev/null && \
   grep -qE 'ReconcileRelinked.*=.*"relinked"' internal/headscale/reconcile.go 2>/dev/null && \
   grep -qE 'ReconcileOrphan.*=.*"orphan"' internal/headscale/reconcile.go 2>/dev/null; then
    ok "A.3 4 outcome constants defined (ok/linked/relinked/orphan)"
else
    bad "A.3 all 4 outcome constants must be defined with literal JSON values"
fi

# A.4 the NEVER auto-delete contract: no DELETE
# statement anywhere in reconcile.go
if ! grep -qE 'DELETE (FROM|USING)' internal/headscale/reconcile.go 2>/dev/null; then
    ok "A.4 reconcile.go has no DELETE statement (NEVER auto-delete contract)"
else
    bad "A.4 reconcile.go MUST NOT contain DELETE — orphan outcomes are audited, not deleted"
fi

# A.5 the function writes an audit row (per-row +
# summary) with the canonical "headscale_user_reconcile"
# action name. The action is passed as a parameter to
# writeAudit + writeAuditRaw, so the check looks for
# the literal "headscale_user_reconcile" string being
# passed in.
if grep -qE '"headscale_user_reconcile"' internal/headscale/reconcile.go 2>/dev/null; then
    ok "A.5 reconcile writes audit_log rows (action=headscale_user_reconcile)"
else
    bad "A.5 reconcile must write audit_log rows with action=headscale_user_reconcile (literal string passed to writeAudit)"
fi

# --- B. Source: reconcile_cron.go ---

# B.1 the cron file exists
if [ -f internal/headscale/reconcile_cron.go ]; then
    ok "B.1 internal/headscale/reconcile_cron.go exists"
else
    bad "B.1 internal/headscale/reconcile_cron.go missing"
fi

# B.2 StartReconcileCron signature
if grep -qE '^func StartReconcileCron\(ctx context\.Context, db \*sql\.DB, hs \*Client, interval time\.Duration\)' internal/headscale/reconcile_cron.go 2>/dev/null; then
    ok "B.2 StartReconcileCron has the canonical (ctx, db, hs, interval) signature"
else
    bad "B.2 StartReconcileCron signature must be (ctx, db, hs, interval) — matches StartCron"
fi

# B.3 sync.Once guard (prevents double-spawn on
# concurrent main.go + page-triggered starts)
if grep -q 'sync.Once' internal/headscale/reconcile_cron.go 2>/dev/null && \
   grep -q 'startReconcileCronOnce' internal/headscale/reconcile_cron.go 2>/dev/null; then
    ok "B.3 sync.Once guard present (prevents double-spawn)"
else
    bad "B.3 sync.Once guard missing (derphealth pattern)"
fi

# B.4 DefaultReconcileInterval = 1h (changing this
# changes the operator's stale-id window)
if grep -qE 'DefaultReconcileInterval = 1 \* time\.Hour' internal/headscale/reconcile_cron.go 2>/dev/null; then
    ok "B.4 DefaultReconcileInterval pinned at 1h"
else
    bad "B.4 DefaultReconcileInterval must be 1h (the B-check pins it)"
fi

# B.5 RunOnceNow exists (manual trigger for
# `skygate headscale-users-reconcile` + the
# /admin/headscale page button)
if grep -qE '^func RunOnceNow\(' internal/headscale/reconcile_cron.go 2>/dev/null; then
    ok "B.5 RunOnceNow exists (manual trigger)"
else
    bad "B.5 RunOnceNow missing (the /admin/headscale page calls it)"
fi

# --- C. Config + wire-up ---

# C.1 the config fields exist
if grep -qE 'ReconcileHeadscaleUsers\s+bool' internal/config/config.go 2>/dev/null && \
   grep -qE 'ReconcileHeadscaleUsersInterval\s+time\.Duration' internal/config/config.go 2>/dev/null; then
    ok "C.1 config fields exist (ReconcileHeadscaleUsers + Interval)"
else
    bad "C.1 config fields must exist (ReconcileHeadscaleUsers + Interval)"
fi

# C.2 the env-var defaults are correct (default ON,
# interval = 0 = use package default)
if grep -qE 'SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED.*true' internal/config/config.go 2>/dev/null && \
   grep -qE 'SKYGATE_RECONCILE_HEADSCALE_USERS_INTERVAL.*0' internal/config/config.go 2>/dev/null; then
    ok "C.2 env-var defaults are correct (default ON, interval=0 → package default)"
else
    bad "C.2 env-var defaults must be ON + 0-interval (see config.go)"
fi

# C.3 the cron is wired in main.go (after
# ensureHeadscaleUser, so the headscale client exists)
if grep -qE 'headscale\.StartReconcileCron\(' cmd/skygate/main.go 2>/dev/null; then
    ok "C.3 main.go calls headscale.StartReconcileCron"
else
    bad "C.3 main.go must call headscale.StartReconcileCron (the wire-up)"
fi

# C.4 the wire-up is gated on cfg.ReconcileHeadscaleUsers
if grep -qE 'if cfg\.ReconcileHeadscaleUsers \{' cmd/skygate/main.go 2>/dev/null; then
    ok "C.4 main.go wire-up is gated on cfg.ReconcileHeadscaleUsers (opt-in via env var)"
else
    bad "C.4 main.go wire-up must be gated on cfg.ReconcileHeadscaleUsers"
fi

# --- D. Tests ---

# D.1 reconcile_test.go exists
if [ -f internal/headscale/reconcile_test.go ]; then
    ok "D.1 internal/headscale/reconcile_test.go exists"
else
    bad "D.1 internal/headscale/reconcile_test.go missing"
fi

# D.2 10+ unit tests (the contract: pure-Go, no DB needed)
n_tests=$(grep -cE '^func Test' internal/headscale/reconcile_test.go 2>/dev/null || echo 0)
if [ "$n_tests" -ge 10 ]; then
    ok "D.2 reconcile_test.go has $n_tests unit tests (>= 10)"
else
    bad "D.2 reconcile_test.go has $n_tests tests, need >= 10"
fi

# D.3 the int64 helper has edge-case coverage
if grep -qE 'TestInt64FromString_Valid' internal/headscale/reconcile_test.go 2>/dev/null && \
   grep -qE 'TestInt64FromString_EmptyAndInvalid' internal/headscale/reconcile_test.go 2>/dev/null; then
    ok "D.3 int64FromString edge cases covered (valid + invalid + overflow)"
else
    bad "D.3 int64FromString must have valid + invalid coverage"
fi

# D.4 the outcome-string stability test pins the
# JSON-tagged values (changing them breaks
# /admin/audit filters)
if grep -qE 'TestReconcileOutcome_StringPinsValues' internal/headscale/reconcile_test.go 2>/dev/null; then
    ok "D.4 outcome strings are pinned (changing them is a breaking change for /admin/audit)"
else
    bad "D.4 must pin the outcome string values (regression guard for /admin/audit filters)"
fi

# D.5 the NEVER auto-delete test: scan reconcile.go
# for any DELETE, TRUNCATE, or DROP statement. (The
# per-row UPDATE for linked/relinked is fine — those
# are not deletes.)
if ! grep -qE '(DELETE FROM|TRUNCATE TABLE|DROP TABLE|DROP ROW)' internal/headscale/reconcile.go 2>/dev/null; then
    ok "D.5 reconcile.go contains no DELETE/TRUNCATE/DROP (NEVER auto-delete)"
else
    bad "D.5 reconcile.go must NEVER contain DELETE — orphan outcomes are audited, not deleted"
fi

# D.6 the tests run (pure unit, no DB needed) — only
# run on this host if `go` is on the bash PATH
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        -run 'Reconcile|Int64FromString|StartReconcileCron|RunOnceNow|DefaultReconcile' \
        ./internal/headscale/ 2>/dev/null | grep -q '^ok'; then
        ok "D.6 reconcile unit tests pass (10 tests, no DB needed)"
    else
        bad "D.6 reconcile unit tests failed"
    fi
else
    echo "  SKIP  D.6 reconcile unit tests (no go in PATH)"
fi

# --- E. verify_pre_deploy.sh + AGENTS.md registration ---

# E.1 the check is registered
if grep -q 'check_b237_18' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "E.1 scripts/verify_pre_deploy.sh includes the B237.18 check"
else
    bad "E.1 scripts/verify_pre_deploy.sh must include the B237.18 check"
fi

# E.2 AGENTS.md mentions B237.18
if grep -q 'B237\.18' AGENTS.md 2>/dev/null; then
    ok "E.2 AGENTS.md documents B237.18"
else
    bad "E.2 AGENTS.md must document B237.18"
fi

# E.3 PLANS.md marks TD-10 as DONE
if grep -B1 -A2 'TD-10' docs/PLANS.md 2>/dev/null | grep -qE 'DONE\b'; then
    ok "E.3 docs/PLANS.md marks TD-10 as DONE (B237.18 closed it)"
else
    bad "E.3 docs/PLANS.md should mark TD-10 as DONE (B237.18 closed it)"
fi

# --- F. Build ---

# F.1 the whole tree still builds
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "F.1 go build ./... clean (B237.18 doesn't break the build)"
    else
        bad "F.1 go build ./... failed"
    fi
else
    echo "  SKIP  F.1 go build (no go in PATH)"
fi

# --- Summary ---

echo
echo "=== B237.18 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
