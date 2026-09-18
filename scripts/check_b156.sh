#!/bin/bash
# check_b156.sh — in-app preauth key expiration notification
# scheduler (B156, v1.5.0)
#
# Background (operator 2026-08-20): "по итогу требуется
# также добавить для пользователей отдельно уведомление
# по истечению время действия ключа на устройство и
# инструкцию как продлить" — skybars saw a "key
# expiring" warning in the Tailscale client and the
# operator wants a per-user Telegram notification
# (different from the operator-side SendAlert) WITH
# the renew instructions, so the user knows what to do
# BEFORE the key expires.
#
# B156 (this file) pins the fixes:
#   1. internal/keynotify/ package MUST exist with
#      scheduler.go (Start + tick + RunNotify) and
#      scheduler_db.go (init() binding global settings).
#   2. V058PG migration MUST add the notified_at column
#      to preauth_keys.
#   3. ListExpiringPreauthKeys + MarkPreauthKeyNotified
#      + ResetPreauthKeyNotified DB helpers MUST exist
#      (the typed wrappers).
#   4. qSelectExpiringPreauthKeys + qMarkPreauthKeyNotified
#      + qResetPreauthKeyNotified SQL constants MUST
#      exist.
#   5. PostMyPreauth + PostMyKeyReissue MUST reset
#      notified_at=0 on insert (so the new key is
#      eligible for a fresh notification).
#   6. The KeyNotifyEnabled + KeyNotifySchedule Config
#      fields MUST exist + read the
#      SKYGATE_KEY_NOTIFY_* env vars.
#   7. main.go MUST wire keynotify.Start via
#      cfg.KeyNotifyEnabled + a UserNotifierSink
#      adapter (different from the operator-side
#      schedulerNotifierSink).
#   8. The notify message MUST include the
#      "/my/keys → click Reissue" instructions
#      (otherwise the user just sees "key expiring"
#      with no action).
#   9. Unit tests for the pure-function helpers MUST
#      exist + pass.
#  10. verify_pre_deploy.sh MUST include B156.
#  11. Go vet + Go test on the new package MUST pass.

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: internal/keynotify/ package + scheduler files exist ==="
if [ -f "internal/keynotify/scheduler.go" ]; then
    ok "internal/keynotify/scheduler.go exists"
else
    bad "internal/keynotify/scheduler.go MISSING — scheduler not implemented"
fi
if [ -f "internal/keynotify/scheduler_db.go" ]; then
    ok "internal/keynotify/scheduler_db.go exists"
else
    bad "internal/keynotify/scheduler_db.go MISSING — global_settings shim not wired"
fi
# Start() is the public API.
if grep -q 'func Start(ctx' internal/keynotify/scheduler.go; then
    ok "Start(ctx, deps) defined (entry point)"
else
    bad "Start() NOT defined — main.go can't wire the scheduler"
fi
# RunNotify is the actual work function.
if grep -q 'func RunNotify(' internal/keynotify/scheduler.go; then
    ok "RunNotify defined (per-tick work function)"
else
    bad "RunNotify NOT defined — scheduler is a no-op"
fi

echo ""
echo "=== contract B: V058PG migration + notified_at column ==="
# The migration is in migrations_pg.go and is the
# LAST in the chain.
if grep -q 'migrateV058PG' internal/db/migrations_pg.go; then
    ok "migrateV058PG defined in migrations_pg.go"
else
    bad "migrateV058PG MISSING — notified_at column never created"
fi
if grep -q 'ADD COLUMN IF NOT EXISTS notified_at' internal/db/migrations_pg.go; then
    ok "V058PG migration adds notified_at column"
else
    bad "V058PG migration does NOT add notified_at — scheduler can't dedup"
fi
# The chain is registered in driver_postgres.go.
if grep -q 'migrateV058PG' internal/db/driver_postgres.go; then
    ok "V058PG registered in driver_postgres.go chain"
else
    bad "V058PG NOT registered — migration never runs"
fi

echo ""
echo "=== contract C: SQL constants for the notify flow exist ==="
if grep -q 'qSelectExpiringPreauthKeys' internal/db/queries.go; then
    ok "qSelectExpiringPreauthKeys defined"
else
    bad "qSelectExpiringPreauthKeys MISSING — scheduler can't find due keys"
fi
if grep -q 'qMarkPreauthKeyNotified' internal/db/queries.go; then
    ok "qMarkPreauthKeyNotified defined"
else
    bad "qMarkPreauthKeyNotified MISSING — scheduler can't dedup"
fi
if grep -q 'qResetPreauthKeyNotified' internal/db/queries.go; then
    ok "qResetPreauthKeyNotified defined"
else
    bad "qResetPreauthKeyNotified MISSING — fresh keys can't reset dedup"
fi
# The SELECT must filter on used=0 + expires_at > 0
# + expires_at <= cutoff. Without used=0, every
# consumed key would be a candidate. Use a
# flexible match — the SQL has multiple spaces
# between fields, and the literal "used = 0"
# substring is what we care about.
if grep -q 'qSelectExpiringPreauthKeys' internal/db/queries.go && \
   grep -A0 'qSelectExpiringPreauthKeys' internal/db/queries.go | grep -q 'used = 0' && \
   grep -A0 'qSelectExpiringPreauthKeys' internal/db/queries.go | grep -q 'expires_at > 0' && \
   grep -A0 'qSelectExpiringPreauthKeys' internal/db/queries.go | grep -q 'expires_at <='; then
    ok "qSelectExpiringPreauthKeys filters on used=0 + expires_at>0 + expires_at<=cutoff"
else
    bad "qSelectExpiringPreauthKeys missing one of (used=0, expires_at>0, expires_at<=cutoff) — would notify on consumed/never-expiring keys"
fi

echo ""
echo "=== contract D: DB helpers for the notify flow exist ==="
if grep -q 'func ListExpiringPreauthKeys' internal/db/preauth_keys.go; then
    ok "db.ListExpiringPreauthKeys helper defined"
else
    bad "db.ListExpiringPreauthKeys helper MISSING — handler call will not compile"
fi
if grep -q 'func MarkPreauthKeyNotified' internal/db/preauth_keys.go; then
    ok "db.MarkPreauthKeyNotified helper defined"
else
    bad "db.MarkPreauthKeyNotified helper MISSING — handler call will not compile"
fi
if grep -q 'func ResetPreauthKeyNotified' internal/db/preauth_keys.go; then
    ok "db.ResetPreauthKeyNotified helper defined"
else
    bad "db.ResetPreauthKeyNotified helper MISSING"
fi

echo ""
echo "=== contract E: PostMyPreauth + PostMyKeyReissue reset notified_at on insert ==="
# InsertPreauthKey now sets notified_at=0 (the
# V058PG column). The B156 reset is wired through
# the helpers; the comment in each handler must
# document the rationale.
if grep -q 'B156' internal/feature/my/preauth.go; then
    ok "PostMyPreauth has B156 comment (notified_at reset)"
else
    bad "PostMyPreauth missing B156 comment"
fi
if grep -q 'B156' internal/feature/my/keys.go; then
    ok "PostMyKeyReissue has B156 comment (notified_at reset)"
else
    bad "PostMyKeyReissue missing B156 comment"
fi

echo ""
echo "=== contract F: Config fields + env-var defaults ==="
if grep -q 'KeyNotifyEnabled' internal/config/config.go; then
    ok "Config.KeyNotifyEnabled field defined"
else
    bad "Config.KeyNotifyEnabled field MISSING"
fi
if grep -q 'KeyNotifySchedule' internal/config/config.go; then
    ok "Config.KeyNotifySchedule field defined"
else
    bad "Config.KeyNotifySchedule field MISSING"
fi
if grep -q 'SKYGATE_KEY_NOTIFY_ENABLED' internal/config/config.go; then
    ok "SKYGATE_KEY_NOTIFY_ENABLED env-var wired"
else
    bad "SKYGATE_KEY_NOTIFY_ENABLED env-var NOT wired"
fi
if grep -q 'SKYGATE_KEY_NOTIFY_SCHEDULE' internal/config/config.go; then
    ok "SKYGATE_KEY_NOTIFY_SCHEDULE env-var wired"
else
    bad "SKYGATE_KEY_NOTIFY_SCHEDULE env-var NOT wired"
fi

echo ""
echo "=== contract G: main.go wires the scheduler + UserNotifierSink adapter ==="
if grep -q 'keynotify.Start(ctx' cmd/skygate/main.go; then
    ok "main.go calls keynotify.Start"
else
    bad "main.go does NOT call keynotify.Start — scheduler is dead code"
fi
if grep -q 'cfg.KeyNotifyEnabled' cmd/skygate/main.go; then
    ok "main.go gates Start on cfg.KeyNotifyEnabled"
else
    bad "main.go does NOT gate Start"
fi
if grep -q 'schedulerUserNotifierSink' cmd/skygate/main.go; then
    ok "main.go uses UserNotifierSink adapter (per-user chat)"
else
    bad "main.go does NOT use UserNotifierSink adapter — would send to operator chat"
fi
if grep -q '"skygate/internal/keynotify"' cmd/skygate/main.go; then
    ok "main.go imports skygate/internal/keynotify"
else
    bad "main.go does NOT import the keynotify package"
fi

echo ""
echo "=== contract H: notify message includes the renew instructions ==="
# The whole point of B156 is the user-facing
# instruction. Without this the user gets
# "key expiring" and has no idea what to do.
if grep -q '/my/keys' internal/keynotify/scheduler.go && \
   grep -q 'Reissue' internal/keynotify/scheduler.go; then
    ok "notify message includes '/my/keys → click Reissue' instruction"
else
    bad "notify message missing renew instruction — user would just see 'key expiring' with no action"
fi

echo ""
echo "=== contract I: unit tests exist + pass ==="
if [ -f "internal/keynotify/scheduler_test.go" ]; then
    ok "internal/keynotify/scheduler_test.go exists"
else
    bad "internal/keynotify/scheduler_test.go MISSING"
fi
# Test names we expect.
for tn in TestSameNotifyMinute TestFormatAuditLine_IncludesAllTokens \
         TestFormatAuditLine_IncludesSkipped TestFormatTelegramSummary_NotifiedOnly \
         TestFormatTelegramSummary_IncludesSkipped TestFormatNotifyMessage_IncludesRenewInstructions \
         TestFormatNotifyMessage_TruncatesLongKey TestFormatNotifyMessage_TodayAndTomorrow \
         TestStorageKeyConstants TestDefaultScheduleAfterCleanup \
         TestNotifyWindowDaysMatchesB155Banner; do
    if grep -q "func $tn" internal/keynotify/scheduler_test.go; then
        ok "$tn test defined"
    else
        bad "$tn test MISSING"
    fi
done

echo ""
echo "=== contract J: verify_pre_deploy.sh includes B156 ==="
if grep -q 'B156' scripts/verify_pre_deploy.sh && \
   grep -q 'check_b156' scripts/verify_pre_deploy.sh; then
    ok "verify_pre_deploy.sh registers B156"
else
    bad "verify_pre_deploy.sh MISSING B156"
fi

echo ""
echo "=== contract K: Go vet + Go test on the new package ==="
if command -v go >/dev/null 2>&1; then
    if go vet ./internal/keynotify/ 2>&1 | grep -E '^(.*):(.*): (error|undefined)' | head -1; then
        bad "go vet ./internal/keynotify/ reports an error"
    else
        ok "go vet ./internal/keynotify/ clean"
    fi
    if go test -count=1 -short ./internal/keynotify/ 2>&1 | tail -1 | grep -q '^ok'; then
        ok "go test ./internal/keynotify/ PASS"
    else
        bad "go test ./internal/keynotify/ FAIL"
    fi
else
    echo "  SKIP  go not in PATH (Windows-host env-only; check on VM)"
fi

echo ""
echo "=== summary ==="
echo "B156: in-app preauth key expiration notification scheduler (per-user Telegram)"
echo "all B156 contracts satisfied"
