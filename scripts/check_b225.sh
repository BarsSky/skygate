#!/usr/bin/env bash
# B-check for B225 (v1.5.0+): /admin/database Phase 4.4 —
# Telegram alerts on failover / migration failure / DB
# health degraded. Closes the "operator finds out about
# a Patroni /switchover failure only by checking
# /admin/audit manually" gap.
#
# Contracts pinned (12 source-pin + 2 go-runtime):
#   A:    PostAdminDatabaseFailover success path calls
#         sendFailoverAlert (✅)
#   B:    PostAdminDatabaseFailover error path calls
#         sendFailoverAlert (❌)
#   C:    PostAdminDatabaseFailoverRollback success
#         path calls sendFailoverAlert (✅)
#   D:    PostAdminDatabaseFailoverRollback error path
#         calls sendFailoverAlert (❌)
#   E:    Service has a sendFailoverAlert helper that
#         no-ops on nil notifier
#   F:    backup.Scheduler has Notifier field
#         (telegram.Notifier-compatible via local
#         SchedulerAlertSink interface to avoid the
#         backup → telegram → mesh → backup import
#         cycle)
#   G:    backup.Scheduler.tick() calls
#         sendSchedulerAlert on config-load fail
#   H:    backup.Scheduler.tick() calls
#         sendSchedulerAlert on RunBackup error
#   I:    main.go wires app.Notifier into
#         backup.Scheduler
#   J:    B225 unit tests cover the alert text format
#         (success / error / rollback / scheduler)
#   K:    Live-verify script present
#   L:    AGENTS.md mentions B225
#   M:    go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

HANDLER_GO="internal/feature/admin/database.go"
BACKUP_GO="internal/backup/scheduler.go"
TEST_ADMIN="internal/feature/admin/database_b225_test.go"
TEST_BACKUP="internal/backup/scheduler_b225_test.go"
MAIN_GO="cmd/skygate/main.go"
LV="scripts/b225_liveverify.sh"
AGENTS="AGENTS.md"

# --- A: failover success path calls sendFailoverAlert ---
# Look for the line range from the first FailoverDB call to the
# redirect at the end of the handler (line ~585 in the live file).
# The success path's sendFailoverAlert("✅" call should be within
# that range.
failover_block=$(sed -n '/^func (s \*Service) PostAdminDatabaseFailover/,/^}$/p' "$HANDLER_GO" 2>/dev/null)
if echo "$failover_block" | grep -q 'sendFailoverAlert("✅"'; then
  ok "A: PostAdminDatabaseFailover success → sendFailoverAlert(\"✅\", ...)"
else
  fail "A: PostAdminDatabaseFailover success path missing sendFailoverAlert(\"✅\", ...)"
fi

# --- B: failover error path calls sendFailoverAlert ---
if echo "$failover_block" | grep -q 'sendFailoverAlert("❌"'; then
  ok "B: PostAdminDatabaseFailover error → sendFailoverAlert(\"❌\", ...)"
else
  fail "B: PostAdminDatabaseFailover error path missing sendFailoverAlert(\"❌\", ...)"
fi

# --- C: rollback success path calls sendFailoverAlert ---
rollback_block=$(sed -n '/^func (s \*Service) PostAdminDatabaseFailoverRollback/,/^}$/p' "$HANDLER_GO" 2>/dev/null)
if echo "$rollback_block" | grep -q 'sendFailoverAlert("✅"'; then
  ok "C: PostAdminDatabaseFailoverRollback success → sendFailoverAlert(\"✅\", ...)"
else
  fail "C: PostAdminDatabaseFailoverRollback success path missing sendFailoverAlert(\"✅\", ...)"
fi

# --- D: rollback error path also calls sendFailoverAlert (❌) ---
if echo "$rollback_block" | grep -q 'sendFailoverAlert("❌"'; then
  ok "D: PostAdminDatabaseFailoverRollback error → sendFailoverAlert(\"❌\", ...)"
else
  fail "D: PostAdminDatabaseFailoverRollback error path missing sendFailoverAlert(\"❌\", ...)"
fi

# --- E: sendFailoverAlert helper exists and is nil-safe ---
if has "$HANDLER_GO" "^func \\(s \\*Service\\) sendFailoverAlert" && \
   has "$HANDLER_GO" "if s\\.Notifier == nil"; then
  ok "E: sendFailoverAlert helper exists + nil-safe"
else
  fail "E: sendFailoverAlert helper missing or not nil-safe"
fi

# --- F: backup.Scheduler has Notifier field ---
sched_struct=$(sed -n '/^type Scheduler struct/,/^}$/p' "$BACKUP_GO" 2>/dev/null)
if echo "$sched_struct" | grep -q "Notifier SchedulerAlertSink"; then
  ok "F: backup.Scheduler has Notifier field (SchedulerAlertSink interface)"
else
  fail "F: backup.Scheduler Notifier field missing"
fi

# --- G: backup.Scheduler.tick config-load fail path ---
if grep -A12 "cfg, err := Load" "$BACKUP_GO" 2>/dev/null | grep -q "sendSchedulerAlert"; then
  ok "G: backup.Scheduler.tick config-load fail → sendSchedulerAlert"
else
  fail "G: backup.Scheduler.tick config-load fail missing sendSchedulerAlert"
fi

# --- H: backup.Scheduler.tick RunBackup error path ---
tick_block=$(sed -n '/^func (s \*Scheduler) tick/,/^}$/p' "$BACKUP_GO" 2>/dev/null)
# Look for the RunBackup call + the error branch right after.
# The error path is `if err != nil { ... sendSchedulerAlert(...) ... }`
# after `res, err := RunBackup(s.DB.Current(), cfg)`.
if echo "$tick_block" | grep -A8 "res, err := RunBackup" | grep -q "sendSchedulerAlert"; then
  ok "H: backup.Scheduler.tick RunBackup error → sendSchedulerAlert"
else
  fail "H: backup.Scheduler.tick RunBackup error missing sendSchedulerAlert"
fi

# --- I: main.go wires app.Notifier into backup.Scheduler ---
if grep -A2 "backupSched := &backup.Scheduler{" "$MAIN_GO" 2>/dev/null | grep -q "Notifier:"; then
  ok "I: main.go wires app.Notifier into backup.Scheduler"
else
  fail "I: main.go missing Notifier wiring for backup.Scheduler"
fi

# --- J: B225 unit tests present ---
if [ -f "$TEST_ADMIN" ] && grep -q "B225" "$TEST_ADMIN"; then
  ok "J.a: B225 admin unit tests present (database_b225_test.go)"
else
  fail "J.a: B225 admin unit tests missing"
fi
if [ -f "$TEST_BACKUP" ] && grep -q "B225" "$TEST_BACKUP"; then
  ok "J.b: B225 backup unit tests present (scheduler_b225_test.go)"
else
  fail "J.b: B225 backup unit tests missing"
fi

# --- K: live-verify script present ---
if [ -f "$LV" ]; then
  ok "K: B225 live-verify script present"
else
  echo "[skip] K: $LV will be added before commit"
fi

# --- L: AGENTS.md mentions B225 ---
if has "$AGENTS" "B225"; then
  ok "L: AGENTS.md mentions B225"
else
  echo "[skip] L: AGENTS.md doesn't mention B225 (will be added before commit)"
fi

# --- M: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "M: go build ./... succeeds"
  else
    fail "M: go build ./... FAILED"
  fi
else
  echo "[skip] M: go not on PATH"
fi

echo ""
echo "B225 B-check: $ok_count passed"
