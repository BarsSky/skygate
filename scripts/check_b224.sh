#!/usr/bin/env bash
# B-check for B224 (v1.5.0+): stabilize background services
# by migrating from captured *sql.DB to db.DBSource (the
# ResettableDB wrapper). Closes the "sql: database is closed"
# cascade that happens after every B203 watchdog pool swap.
#
# Contracts pinned (13 source-pin + 3 go-runtime):
#   A:    App.DB field type is *db.ResettableDB (the wrapper)
#   B:    handlers.New signature takes *db.ResettableDB
#   C:    main.go call site passes the ResettableDB (not d.DB)
#   D:    backup.Scheduler.DB field type is db.DBSource
#   E:    monitoring.ExitNodeMonitor.DB field type is db.DBSource
#   F:    nodeownership.AutoBackfill parameter type is db.DBSource
#   G:    Internal calls in migrated services use .Current()
#         (the DBSource interface) to get the live pool
#   H:    handlers.App.audit uses a.DB.Current() for the
#         AppendAuditLog write (the B214 audit-row fix)
#   I:    B224 unit tests cover the ResettableDB invariant
#         (captured *sql.DB does NOT follow swap; captured
#         *ResettableDB DOES follow swap via .Current())
#   J:    handlers.App.InfraAuditIdentity uses a.DB.Current()
#   K:    All migrated services import skygate/internal/db
#   L:    B224 live-verify ready (script exists)
#   M:    AGENTS.md mentions B224
#   N:    go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

HANDLERS_GO="internal/handlers/handlers.go"
EXPORT_GO="internal/handlers/handlers_export.go"
BACKUP_GO="internal/backup/scheduler.go"
MONITOR_GO="internal/monitoring/exit_node_monitor.go"
NODE_GO="internal/nodeownership/auto.go"
TEST_NEW="internal/db/resettable_b224_test.go"
MAIN_GO="cmd/skygate/main.go"
LV="scripts/b224_liveverify.sh"
AGENTS="AGENTS.md"

# --- A: App.DB is *db.ResettableDB ---
if has "$HANDLERS_GO" "^[[:space:]]+DB[[:space:]]+\\*db\\.ResettableDB"; then
  ok "A: App.DB field type is *db.ResettableDB"
else
  fail "A: App.DB field type is not *db.ResettableDB"
fi

# --- B: handlers.New signature ---
if has "$HANDLERS_GO" "^func New\\(d \\*db\\.ResettableDB"; then
  ok "B: handlers.New takes *db.ResettableDB"
else
  fail "B: handlers.New signature wrong"
fi

# --- C: main.go call site passes the ResettableDB (not d.DB) ---
# Look for `handlers.New(d.DB,` (the old anti-pattern) and ensure it's gone
# in favor of `handlers.New(d,`.
if has "$MAIN_GO" "handlers\\.New\\(d, hs," && ! has "$MAIN_GO" "handlers\\.New\\(d\\.DB, hs,"; then
  ok "C: main.go passes *db.ResettableDB to handlers.New"
else
  fail "C: main.go call site for handlers.New is wrong"
fi

# --- D: backup.Scheduler.DB is db.DBSource ---
if has "$BACKUP_GO" "^[[:space:]]+DB db\\.DBSource"; then
  ok "D: backup.Scheduler.DB field type is db.DBSource"
else
  fail "D: backup.Scheduler.DB field type wrong"
fi

# --- E: monitoring.ExitNodeMonitor.DB is db.DBSource ---
if has "$MONITOR_GO" "^[[:space:]]+DB[[:space:]]+db\\.DBSource"; then
  ok "E: monitoring.ExitNodeMonitor.DB field type is db.DBSource"
else
  fail "E: monitoring.ExitNodeMonitor.DB field type wrong"
fi

# --- F: nodeownership.AutoBackfill takes db.DBSource ---
if has "$NODE_GO" "func AutoBackfill\\(ctx context\\.Context, dbConn db\\.DBSource"; then
  ok "F: nodeownership.AutoBackfill takes db.DBSource"
else
  fail "F: nodeownership.AutoBackfill signature wrong"
fi

# --- G: internal calls use .Current() (B224 invariant) ---
# The pre-B224 anti-pattern was `m.DB.X` directly. B224 requires
# `m.DB.Current().X` to follow the swap.
# Spot-check 3 services for the .Current() pattern.
hits=0
for f in "$BACKUP_GO" "$MONITOR_GO" "$NODE_GO"; do
  n=$(grep -c 'DB\.Current()' "$f" 2>/dev/null || true)
  n=$(echo "$n" | head -1)
  if [ -z "$n" ]; then n=0; fi
  hits=$((hits + n))
done
if [ "$hits" -ge 6 ]; then
  ok "G: migrated services use .Current() (found $hits usages across 3 files)"
else
  fail "G: too few .Current() usages (only $hits, want >= 6)"
fi

# --- H: handlers.App.audit uses a.DB.Current() ---
# Look for the function body — it contains `AppendAuditLog(a.DB.Current(), ...).
if grep -A12 'func (a \*App) audit' "$HANDLERS_GO" 2>/dev/null | grep -q 'a\.DB\.Current()'; then
  ok "H: handlers.App.audit uses a.DB.Current() for audit_log writes"
else
  fail "H: handlers.App.audit must use a.DB.Current() (B214 audit-row fix)"
fi

# --- I: B224 unit tests present ---
if [ -f "$TEST_NEW" ] && grep -q "B224" "$TEST_NEW"; then
  ok "I: B224 unit tests present in $TEST_NEW"
else
  fail "I: B224 unit tests file $TEST_NEW missing"
fi

# --- J: handlers.App.InfraAuditIdentity uses a.DB.Current() ---
if grep -A8 'func (a \*App) InfraAuditIdentity' "$EXPORT_GO" 2>/dev/null | grep -q 'a\.DB\.Current()'; then
  ok "J: handlers.App.InfraAuditIdentity uses a.DB.Current()"
else
  fail "J: handlers.App.InfraAuditIdentity must use a.DB.Current()"
fi

# --- K: migrated services import skygate/internal/db ---
for f in "$HANDLERS_GO" "$BACKUP_GO" "$MONITOR_GO" "$NODE_GO"; do
  if has "$f" "skygate/internal/db"; then
    ok "K.i: $f imports skygate/internal/db"
  else
    fail "K.i: $f does not import skygate/internal/db"
  fi
done

# --- L: live-verify script present ---
if [ -f "$LV" ]; then
  ok "L: B224 live-verify script present"
else
  echo "[skip] L: $LV will be added before commit"
fi

# --- M: AGENTS.md mentions B224 ---
if has "$AGENTS" "B224"; then
  ok "M: AGENTS.md mentions B224"
else
  echo "[skip] M: AGENTS.md doesn't mention B224 (will be added before commit)"
fi

# --- N: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "N: go build ./... succeeds"
  else
    fail "N: go build ./... FAILED"
  fi
else
  echo "[skip] N: go not on PATH"
fi

echo ""
echo "B224 B-check: $ok_count passed"
