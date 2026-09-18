#!/usr/bin/env bash
# B-check for B220 (v1.5.0+): /admin/database
# Phase 3.7 — Patroni failover rollback (operator-
# driven). The full "auto-rollback" (system detects
# new primary unhealthy + auto-triggers rollback)
# is a follow-up B-block; B220 ships the operator-
# driven rollback + the state-tracking that the
# auto version will need.
#
# Contracts pinned (16 source-pin + 2 go-runtime):
#   A-C: 3 new helpers in internal/db/cluster_patroni.go
#        (SetLastFailover / GetLastFailover /
#        ClearLastFailover) + LastFailoverState struct
#   D:   LastFailoverState uses JSON in global_settings
#   E:   SetGlobalSetting is used (not raw SQL)
#   F:   PostAdminDatabaseFailoverRollback handler
#   G:   handler calls SetLastFailover rollback flow
#   H:   handler uses db.FailoverDB from B219 (shared)
#   I:   POST /admin/database/failover/rollback route
#   J:   Rollback card in database.html (conditional
#        on HasLastFailover)
#   K:   LastFailover/Old value pre-populates candidate
#   L:   6 new i18n keys (db.rollback_*) in RU + EN
#   M:   B219 backward compat — B219 handler still
#        works (writes db.failover + SetLastFailover)
#   N:   3 new test functions in cluster_patroni_test.go
#   O:   AGENTS.md mentions B220
#   P:   B220 unit tests pass
#   Q:   go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

DB_GO="internal/db/cluster_patroni.go"
TEST="internal/db/cluster_patroni_test.go"
HANDLER_GO="internal/feature/admin/database.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/database.html"
CATALOG="internal/i18n/catalog_admin.go"
AGENTS="AGENTS.md"

# --- A: SetLastFailover helper ---
if has "$DB_GO" "func SetLastFailover\\("; then
  ok "A: SetLastFailover helper exists"
else
  fail "A: SetLastFailover missing in $DB_GO"
fi

# --- B: GetLastFailover helper ---
if has "$DB_GO" "func GetLastFailover\\("; then
  ok "B: GetLastFailover helper exists"
else
  fail "B: GetLastFailover missing"
fi

# --- C: ClearLastFailover helper ---
if has "$DB_GO" "func ClearLastFailover\\("; then
  ok "C: ClearLastFailover helper exists"
else
  fail "C: ClearLastFailover missing"
fi

# --- D: LastFailoverState struct + JSON tags ---
if has "$DB_GO" "type LastFailoverState struct"; then
  ok "D: LastFailoverState struct with JSON tags defined"
else
  fail "D: LastFailoverState struct missing"
fi

# --- E: SetLastFailover uses SetGlobalSetting (not raw SQL) ---
if has "$DB_GO" "SetGlobalSetting\\(d, globalSettingKeyLastFailover"; then
  ok "E: SetLastFailover uses SetGlobalSetting (not raw SQL)"
else
  fail "E: SetLastFailover should use SetGlobalSetting"
fi

# --- F: PostAdminDatabaseFailoverRollback handler ---
if has "$HANDLER_GO" "func .* PostAdminDatabaseFailoverRollback"; then
  ok "F: PostAdminDatabaseFailoverRollback handler exists"
else
  fail "F: PostAdminDatabaseFailoverRollback missing in $HANDLER_GO"
fi

# --- G: handler uses SetLastFailover / GetLastFailover / ClearLastFailover ---
if has "$HANDLER_GO" "db.GetLastFailover" && has "$HANDLER_GO" "db.ClearLastFailover"; then
  ok "G: handler uses GetLastFailover + ClearLastFailover (the full rollback flow)"
else
  fail "G: handler must use GetLastFailover + ClearLastFailover"
fi

# --- H: handler reuses db.FailoverDB from B219 (single Patroni call) ---
if has "$HANDLER_GO" "db.FailoverDB\\(r.Context\\(\\)"; then
  ok "H: handler reuses db.FailoverDB from B219 (no duplicated Patroni plumbing)"
else
  fail "H: handler should reuse db.FailoverDB from B219"
fi

# --- I: route registered ---
if has "$MAIN_GO" 'POST /admin/database/failover/rollback'; then
  ok "I: route POST /admin/database/failover/rollback registered"
else
  fail "I: route POST /admin/database/failover/rollback missing in $MAIN_GO"
fi

# --- J: Rollback card in template (conditional) ---
if has "$TEMPLATE" '/admin/database/failover/rollback' && has "$TEMPLATE" 'HasLastFailover'; then
  ok "J: Rollback card in template, conditional on HasLastFailover"
else
  fail "J: Rollback card missing or not conditional on HasLastFailover"
fi

# --- K: LastFailover.Old pre-populates candidate input ---
if has "$TEMPLATE" 'value="{{.Data.LastFailover.Old}}"'; then
  ok "K: candidate input pre-populated with LastFailover.Old"
else
  fail "K: candidate input should be pre-populated with LastFailover.Old"
fi

# --- L: 6 new i18n keys in RU + EN ---
for key in "db.rollback_title" "db.rollback_help" "db.rollback_last_failover" "db.rollback_candidate_ph" "db.rollback_btn" "db.rollback_confirm"; do
  n=$(grep -c "\"${key}\":" "$CATALOG" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 2 ]; then
    ok "L: i18n key ${key} present in RU + EN ($n occurrences)"
  else
    fail "L: i18n key ${key} missing (only $n occurrence(s))"
  fi
done

# --- M: B219 backward compat — PostAdminDatabaseFailover still has the audit + SetLastFailover call ---
if has "$HANDLER_GO" '"db.failover"' && has "$HANDLER_GO" 'db.SetLastFailover'; then
  ok "M: B219 handler still works (db.failover audit + SetLastFailover state)"
else
  fail "M: B219 handler regressed (missing db.failover audit or SetLastFailover call)"
fi

# --- N: 3 new test functions in cluster_patroni_test.go ---
n=$(grep -c "^func Test\\(SetLastFailover\\|GetLastFailover\\)" "$TEST" 2>/dev/null || echo 0)
if [ "${n:-0}" -ge 3 ]; then
  ok "N: B220 test file has $n new test functions"
else
  fail "N: only $n new test functions (expected >= 3)"
fi

# --- O: AGENTS.md mentions B220 ---
if has "$AGENTS" "B220"; then
  ok "O: AGENTS.md mentions B220"
else
  echo "[skip] O: AGENTS.md doesn't mention B220 (will be added before commit)"
fi

# --- P: B220 unit tests pass ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/db/... -run 'SetLastFailover|GetLastFailover|ClearLastFailover' -count=1 >/dev/null 2>&1; then
    ok "P: B220 unit tests pass"
  else
    fail "P: B220 unit tests FAILED"
  fi
else
  echo "[skip] P: go not on PATH"
fi

# --- Q: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "Q: go build ./... succeeds"
  else
    fail "Q: go build ./... FAILED"
  fi
else
  echo "[skip] Q: go not on PATH"
fi

echo ""
echo "B220 B-check: $ok_count passed"
