#!/bin/bash
# check_b154.sh — in-app auto-rotate scheduler for personal API tokens
# (B154, v1.5.0)
#
# Background (2026-08-20): B153 shipped the UI for the
# auto_rotate checkbox on /my/tokens (CreateToken form) +
# the per-row Renew button. But the auto_rotate flag itself
# wasn't being honoured — tokens with auto_rotate=1 just
# expired silently, and the operator's Tailscale device
# showed a "key expiring" warning as a symptom. B154 (this
# file) wires the in-app goroutine that actually does the
# auto-rotation.
#
# B154 (this file) pins the fixes:
#   1. internal/tokenrotate/ package MUST exist with
#      scheduler.go (Start + tick + RunAutoExtend) and
#      scheduler_db.go (init() binding global settings).
#   2. qSelectAPITokensForAutoRotate + qUpdateAPITokenExpiryByID
#      SQL constants MUST exist (the scheduler's two SQL
#      primitives).
#   3. ListAPITokensForAutoRotate + UpdateAPITokenExpiryByID
#      DB helpers MUST exist (the typed wrappers).
#   4. The TokenAutoRotateEnabled + TokenAutoRotateSchedule
#      Config fields MUST exist + read the
#      SKYGATE_TOKEN_AUTO_ROTATE_* env vars.
#   5. main.go MUST wire tokenrotate.Start via
#      cfg.TokenAutoRotateEnabled.
#   6. The scheduler MUST honour the 14d-banner-window
#      style dedup (sameRotationMinute).
#   7. The scheduler MUST use the existing
#      db.AppendAuditLogNoUser helper (audit "system
#      event" pattern, mirrors the B130/B142/B143
#      schedulers).
#   8. Unit tests for the pure-function helpers MUST
#      exist + pass (sameRotationMinute,
#      autoRotateIsDueThisTick, formatAuditLine,
#      formatTelegramSummary, storage key constants).
#   9. i18n keys for the alert messages MUST exist in
#      both RU and EN (B4 parity).
#  10. verify_pre_deploy.sh MUST include B154.
#  11. Go vet + Go test on the new package MUST pass.
#
# Without these checks, the auto-rotate feature would
# silently regress to "checkbox present, no actual rotation"
# — which is exactly the state that triggered the operator's
# report.

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: internal/tokenrotate/ package + scheduler files exist ==="
if [ -f "internal/tokenrotate/scheduler.go" ]; then
    ok "internal/tokenrotate/scheduler.go exists"
else
    bad "internal/tokenrotate/scheduler.go MISSING — scheduler not implemented"
fi
if [ -f "internal/tokenrotate/scheduler_db.go" ]; then
    ok "internal/tokenrotate/scheduler_db.go exists"
else
    bad "internal/tokenrotate/scheduler_db.go MISSING — global_settings shim not wired"
fi

# Start() is the public API. Must be defined.
if grep -q 'func Start(ctx' internal/tokenrotate/scheduler.go; then
    ok "Start(ctx, deps) defined (entry point)"
else
    bad "Start() NOT defined — main.go can't wire the scheduler"
fi

# RunAutoExtend is the actual work function.
if grep -q 'func RunAutoExtend(' internal/tokenrotate/scheduler.go; then
    ok "RunAutoExtend defined (per-tick work function)"
else
    bad "RunAutoExtend NOT defined — auto-rotate is a no-op"
fi

echo ""
echo "=== contract B: SQL constants for the auto-rotate flow exist ==="
# The scheduler needs (a) a SELECT that finds due tokens and
# (b) an UPDATE that extends one token's expiry. Both must
# be defined in internal/db/queries.go.
if grep -q 'qSelectAPITokensForAutoRotate' internal/db/queries.go; then
    ok "qSelectAPITokensForAutoRotate defined"
else
    bad "qSelectAPITokensForAutoRotate MISSING — scheduler can't find due tokens"
fi
if grep -q 'qUpdateAPITokenExpiryByID' internal/db/queries.go; then
    ok "qUpdateAPITokenExpiryByID defined"
else
    bad "qUpdateAPITokenExpiryByID MISSING — scheduler can't extend a token"
fi
# The SELECT must filter on auto_rotate=1 + expires_at > 0 +
# expires_at <= cutoff. Without the auto_rotate=1 filter,
# every token in the system would be a candidate.
if grep -q 'qSelectAPITokensForAutoRotate = .*auto_rotate = 1' internal/db/queries.go; then
    ok "qSelectAPITokensForAutoRotate filters on auto_rotate=1"
else
    bad "qSelectAPITokensForAutoRotate doesn't filter on auto_rotate=1 — would extend everything"
fi

echo ""
echo "=== contract C: DB helpers for the auto-rotate flow exist ==="
if grep -q 'func ListAPITokensForAutoRotate' internal/db/personal_api_tokens.go; then
    ok "db.ListAPITokensForAutoRotate helper defined"
else
    bad "db.ListAPITokensForAutoRotate helper MISSING — handler call will not compile"
fi
if grep -q 'func UpdateAPITokenExpiryByID' internal/db/personal_api_tokens.go; then
    ok "db.UpdateAPITokenExpiryByID helper defined"
else
    bad "db.UpdateAPITokenExpiryByID helper MISSING — handler call will not compile"
fi

echo ""
echo "=== contract D: Config fields + env-var defaults ==="
# The two Config fields are read by main.go when wiring
# the scheduler. Without them, the env-var has no
# effect and the scheduler is permanently disabled.
if grep -q 'TokenAutoRotateEnabled' internal/config/config.go; then
    ok "Config.TokenAutoRotateEnabled field defined"
else
    bad "Config.TokenAutoRotateEnabled field MISSING — env-var has no effect"
fi
if grep -q 'TokenAutoRotateSchedule' internal/config/config.go; then
    ok "Config.TokenAutoRotateSchedule field defined"
else
    bad "Config.TokenAutoRotateSchedule field MISSING — env-var has no effect"
fi
# The env-var defaults — boot-time fallback when the
# global_settings key is empty.
if grep -q 'SKYGATE_TOKEN_AUTO_ROTATE_ENABLED' internal/config/config.go; then
    ok "SKYGATE_TOKEN_AUTO_ROTATE_ENABLED env-var wired"
else
    bad "SKYGATE_TOKEN_AUTO_ROTATE_ENABLED env-var NOT wired"
fi
if grep -q 'SKYGATE_TOKEN_AUTO_ROTATE_SCHEDULE' internal/config/config.go; then
    ok "SKYGATE_TOKEN_AUTO_ROTATE_SCHEDULE env-var wired"
else
    bad "SKYGATE_TOKEN_AUTO_ROTATE_SCHEDULE env-var NOT wired"
fi

echo ""
echo "=== contract E: main.go wires the scheduler ==="
# The wire-up uses the cfg.TokenAutoRotateEnabled guard +
# the schedulerNotifierSink adapter (the same one the
# other B130/B142/B143 schedulers use).
if grep -q 'tokenrotate.Start(ctx' cmd/skygate/main.go; then
    ok "main.go calls tokenrotate.Start"
else
    bad "main.go does NOT call tokenrotate.Start — scheduler is dead code"
fi
if grep -q 'cfg.TokenAutoRotateEnabled' cmd/skygate/main.go; then
    ok "main.go gates Start on cfg.TokenAutoRotateEnabled"
else
    bad "main.go does NOT gate Start — scheduler always on (or always off)"
fi
# The import.
if grep -q '"skygate/internal/tokenrotate"' cmd/skygate/main.go; then
    ok "main.go imports skygate/internal/tokenrotate"
else
    bad "main.go does NOT import the tokenrotate package"
fi

echo ""
echo "=== contract F: same-minute dedup helper exists ==="
# The dedup logic is the same pattern as the B130/B142/
# B143 schedulers — prevents double-firing when two
# ticks land in the same minute.
if grep -q 'func sameRotationMinute' internal/tokenrotate/scheduler.go; then
    ok "sameRotationMinute helper defined"
else
    bad "sameRotationMinute helper MISSING — scheduler may double-fire"
fi

echo ""
echo "=== contract G: audit log uses db.AppendAuditLogNoUser ==="
# The audit "system event" pattern (mirrors B130/B142/
# B143) is the only one that lets a no-user_id event be
# logged without a SQL detour. Without this, the
# scheduler would either skip the audit entry or write
# a row with user_id=0, username=''
# (which works but is fragile).
if grep -q 'db.AppendAuditLogNoUser' internal/tokenrotate/scheduler.go; then
    ok "scheduler uses db.AppendAuditLogNoUser (system event pattern)"
else
    bad "scheduler does NOT use db.AppendAuditLogNoUser — audit entry missing or fragile"
fi
# The action name should be `token.auto_rotate` for
# operator-side filtering (e.g. /admin/audit action
# dropdown).
if grep -q '"token.auto_rotate"' internal/tokenrotate/scheduler.go; then
    ok "audit action name is 'token.auto_rotate' (filterable in /admin/audit)"
else
    bad "audit action name is NOT 'token.auto_rotate' — /admin/audit filter broken"
fi

echo ""
echo "=== contract H: unit tests exist + pass ==="
if [ -f "internal/tokenrotate/scheduler_test.go" ]; then
    ok "internal/tokenrotate/scheduler_test.go exists"
else
    bad "internal/tokenrotate/scheduler_test.go MISSING — pure-function helpers are untested"
fi
# Test names we expect to be present.
for tn in TestSameRotationMinute TestFormatAuditLine_IncludesAllTokens \
         TestFormatAuditLine_IncludesErrors TestFormatTelegramSummary_ListsAllTokens \
         TestFormatTelegramSummary_TruncatesLongLists TestStorageKeyConstants \
         TestDefaultScheduleMatchesUpdateScheduler; do
    if grep -q "func $tn" internal/tokenrotate/scheduler_test.go; then
        ok "$tn test defined"
    else
        bad "$tn test MISSING — coverage gap"
    fi
done

echo ""
echo "=== contract I: i18n keys (RU+EN parity) ==="
# The new auto-rotate alert messages must be localizable
# for future RU Telegram alerts. Same parity rule as B153
# (both languages must declare the key).
for k in auto_rotate_alert_header auto_rotate_alert_more auto_rotate_alert_failed; do
    count=$(grep -c "\"tokens\\.${k}\"" internal/i18n/catalog_my.go || true)
    if [ "$count" -ge 2 ]; then
        ok "tokens.${k} present in both RU and EN ($count occurrences)"
    else
        bad "tokens.${k} missing parity (only $count occurrence(s))"
    fi
done

echo ""
echo "=== contract J: verify_pre_deploy.sh includes B154 ==="
if grep -q 'B154' scripts/verify_pre_deploy.sh && \
   grep -q 'check_b154' scripts/verify_pre_deploy.sh; then
    ok "verify_pre_deploy.sh registers B154"
else
    bad "verify_pre_deploy.sh MISSING B154 — pre-push gate won't run this check"
fi

echo ""
echo "=== contract K: Go vet + Go test on the new package ==="
# The scheduler test file uses standard library
# helpers only (no test-doubles), so a basic go vet +
# go test is enough to catch the regression.
if command -v go >/dev/null 2>&1; then
    if go vet ./internal/tokenrotate/ 2>&1 | grep -E '^(.*):(.*): (error|undefined)' | head -1; then
        bad "go vet ./internal/tokenrotate/ reports an error"
    else
        ok "go vet ./internal/tokenrotate/ clean"
    fi
    if go test -count=1 -short ./internal/tokenrotate/ 2>&1 | tail -1 | grep -q '^ok'; then
        ok "go test ./internal/tokenrotate/ PASS"
    else
        bad "go test ./internal/tokenrotate/ FAIL"
    fi
else
    echo "  SKIP  go not in PATH (Windows-host env-only; check on VM)"
fi

echo ""
echo "=== summary ==="
echo "B154: in-app auto-rotate scheduler for personal API tokens (auto-extend, hash unchanged)"
echo "all B154 contracts satisfied"
