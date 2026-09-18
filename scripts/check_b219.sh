#!/usr/bin/env bash
# B-check for B219 (v1.5.0+): /admin/database
# Phase 3.3 — Patroni /switchover plumbing.
#
# Contracts pinned (14 source-pin + 2 go-runtime):
#   A-B: db.FailoverDB helper (function + Patroni
#        /switchover contract)
#   C:   Service.PatroniURL field
#   D:   main.go wires SKYGATE_PATRONI_URL → PatroniURL
#   E-F: 2 new admin fields (PostAdminDatabaseFailover +
#        Service.PatroniURL)
#   G:   POST /admin/database/failover route
#   H:   PG Failover card in database.html
#   I:   7 new i18n keys (db.failover_*) in RU + EN
#   J:   B219 unit tests (8 sub-cases in
#        TestFailoverDB_*)
#   K:   B218 backward compat (skygate init still
#        works — B219 doesn't touch init.go)
#   L:   audit row written on success AND failure
#        (operator can see failed attempts)
#   M:   B219 unit tests pass
#   N:   go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

DB_GO="internal/db/cluster_patroni.go"
TEST="internal/db/cluster_patroni_test.go"
SVC_GO="internal/feature/admin/service.go"
HANDLER_GO="internal/feature/admin/database.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/database.html"
CATALOG="internal/i18n/catalog_admin.go"

# --- A: db.FailoverDB helper exists ---
if has "$DB_GO" "func FailoverDB\\("; then
  ok "A: db.FailoverDB helper exists"
else
  fail "A: db.FailoverDB missing in $DB_GO"
fi

# --- B: helper calls Patroni /switchover ---
if has "$DB_GO" "POST" && has "$DB_GO" "/switchover"; then
  ok "B: helper calls POST {URL}/switchover (Patroni contract)"
else
  fail "B: helper doesn't reference POST or /switchover"
fi

# --- C: Service has PatroniURL field ---
if has "$SVC_GO" "PatroniURL[[:space:]]+string"; then
  ok "C: Service.PatroniURL field added"
else
  fail "C: Service.PatroniURL field missing in $SVC_GO"
fi

# --- D: main.go wires SKYGATE_PATRONI_URL ---
if has "$MAIN_GO" "SKYGATE_PATRONI_URL"; then
  ok "D: main.go reads SKYGATE_PATRONI_URL env var"
else
  fail "D: main.go doesn't wire SKYGATE_PATRONI_URL"
fi

# --- E: PostAdminDatabaseFailover handler ---
if has "$HANDLER_GO" "func .* PostAdminDatabaseFailover"; then
  ok "E: PostAdminDatabaseFailover handler exists"
else
  fail "E: PostAdminDatabaseFailover missing in $HANDLER_GO"
fi

# --- F: handler calls db.FailoverDB ---
if has "$HANDLER_GO" "db.FailoverDB\\("; then
  ok "F: handler calls db.FailoverDB"
else
  fail "F: handler doesn't call db.FailoverDB"
fi

# --- G: route registered ---
if has "$MAIN_GO" 'POST /admin/database/failover'; then
  ok "G: route POST /admin/database/failover registered"
else
  fail "G: route POST /admin/database/failover missing"
fi

# --- H: PG Failover card in template ---
if has "$TEMPLATE" "/admin/database/failover" && has "$TEMPLATE" 'name="candidate"'; then
  ok "H: PG Failover card with candidate field rendered"
else
  fail "H: PG Failover card missing in $TEMPLATE"
fi

# --- I: 7 i18n keys in RU + EN ---
for key in "db.failover_title" "db.failover_help" "db.failover_candidate_ph" "db.failover_leader_ph" "db.failover_reason_ph" "db.failover_btn" "db.failover_confirm"; do
  n=$(grep -c "\"${key}\":" "$CATALOG" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 2 ]; then
    ok "I: i18n key ${key} present in RU + EN ($n occurrences)"
  else
    fail "I: i18n key ${key} missing (only $n occurrence(s))"
  fi
done

# --- J: B219 test file with 8+ test functions ---
n=$(grep -c "^func TestFailoverDB" "$TEST" 2>/dev/null || echo 0)
if [ "${n:-0}" -ge 8 ]; then
  ok "J: B219 test file has $n test functions"
else
  fail "J: only $n TestFailoverDB functions in $TEST (expected >= 8)"
fi

# --- K: B218 backward compat — B219 doesn't break skygate init ---
# We pin that init.go is NOT in the B219 diff (the
# B218 unit tests still pass). Simpler check: the
# B218 B-check still has its O-test pass.
if has "cmd/skygate/init.go" 'func isStandbyRole' && has "cmd/skygate/init.go" 'func parseRolesCSV'; then
  ok "K: skygate init still has B218 standby role logic (no regression)"
else
  fail "K: B219 accidentally removed B218's standby role code"
fi

# --- L: audit row written on both success AND failure ---
# The handler writes "db.failover" on success and
# "db.failover.error" on failure. Both must be in
# the handler.
if has "$HANDLER_GO" '"db.failover"'; then
  ok "L: handler writes db.failover audit row on success"
else
  fail "L: handler doesn't write db.failover audit row"
fi
if has "$HANDLER_GO" '"db.failover.error"'; then
  ok "L: handler writes db.failover.error audit row on failure"
else
  fail "L: handler doesn't write db.failover.error audit row on failure"
fi

# --- M: B219 unit tests pass ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/db/... -run 'FailoverDB' -count=1 >/dev/null 2>&1; then
    ok "M: B219 unit tests pass"
  else
    fail "M: B219 unit tests FAILED"
  fi
else
  echo "[skip] M: go not on PATH"
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
echo "B219 B-check: $ok_count passed"
