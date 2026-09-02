#!/usr/bin/env bash
# B210.1 (v1.5.0+) — DBSource consolidation: 5 (now 6)
# local copies of the same one-method interface → one
# canonical copy in skygate/internal/db.
#
# Phase 3 of docs/internal/cluster-management.md. Closes
# the "5 copies of the same interface" duplication that
# B208.1 (admin) + B210 (auth/my/exit_rules/cluster) +
# earlier B204 (elector) + B206 (healthz) each introduced
# locally. Pre-B210.1, every service that used the
# B203 hot-reload pattern re-declared:
#
#   type DBSource interface { Current() *sql.DB }
#
# B210.1 moves the interface + the FixedDBSource test
# wrapper + a DBCurrent() free function into
# skygate/internal/db/dbsource.go. Each service's local
# dbsource.go now just hosts the per-Service `dbc()`
# method (a one-liner) and re-exports DBSource as a type
# alias for source-compat.
#
# The contracts:
#
#   1. internal/db/dbsource.go exists
#   2. internal/db/dbsource.go declares type DBSource interface { Current() *sql.DB }
#   3. internal/db/dbsource.go declares type FixedDBSource struct { DB *sql.DB } with Current() *sql.DB method
#   4. internal/db/dbsource.go declares func DBCurrent(s DBSource) *sql.DB
#   5. internal/feature/admin/dbsource.go imports skygate/internal/db + uses type alias
#   6. internal/feature/auth/dbsource.go imports skygate/internal/db + uses type alias
#   7. internal/feature/my/dbsource.go imports skygate/internal/db + uses type alias
#   8. internal/feature/exit_rules/dbsource.go imports skygate/internal/db + uses type alias
#   9. internal/feature/cluster/dbsource.go imports skygate/internal/db + uses type alias
#  10. internal/feature/healthz/db_health.go imports skygate/internal/db + NewFixedDBSource delegates to skygatedb.FixedDBSource
#  11. internal/elector/elector.go imports skygate/internal/db + type alias + NewElectorWithDB delegates
#  12. build + vet + tests pass
#  13. AGENTS.md mentions B210.1
#  14. verify_pre_deploy.sh has a B210.1 run_check

set -u

if [ -n "${SKYGATE_PROJECT_DIR:-}" ]; then
  cd "$SKYGATE_PROJECT_DIR"
else
  cd "$(dirname "$0")/.."
fi

PASS=0
FAIL=0
fails=()

check() {
  local name="$1"
  local result="$2"
  if [ "$result" = "ok" ]; then
    printf "  \033[32m✓\033[0m %s\n" "$name"
    PASS=$((PASS+1))
  else
    printf "  \033[31m✗\033[0m %s\n" "$name"
    FAIL=$((FAIL+1))
    fails+=("$name")
  fi
}

file_exists() { [ -f "$1" ]; }
file_grep() { grep -qE "$1" "$2" 2>/dev/null; return $?; }

# 1. dbsource.go exists in internal/db
file_exists "internal/db/dbsource.go" \
  && check "internal/db/dbsource.go exists" ok \
  || check "internal/db/dbsource.go exists" fail

# 2. DBSource interface
file_grep "^type DBSource interface" "internal/db/dbsource.go" \
  && check "internal/db declares type DBSource interface" ok \
  || check "internal/db declares type DBSource interface" fail

# 3. FixedDBSource type + Current method
file_grep "^type FixedDBSource struct" "internal/db/dbsource.go" \
  && file_grep "func \(f FixedDBSource\) Current\(\) \*sql\.DB" "internal/db/dbsource.go" \
  && check "internal/db declares type FixedDBSource + Current() method" ok \
  || check "internal/db declares type FixedDBSource + Current() method" fail

# 4. DBCurrent free function
file_grep "^func DBCurrent\(s DBSource\) \*sql\.DB" "internal/db/dbsource.go" \
  && check "internal/db declares func DBCurrent(s DBSource) *sql.DB" ok \
  || check "internal/db declares func DBCurrent(s DBSource) *sql.DB" fail

# 5-9. Each service's dbsource.go imports skygate/internal/db + uses type alias
for pkg in admin auth my exit_rules cluster; do
  FILE="internal/feature/$pkg/dbsource.go"
  if file_exists "$FILE" && file_grep '"skygate/internal/db"' "$FILE" && file_grep "type DBSource = db.DBSource" "$FILE"; then
    check "internal/feature/$pkg/dbsource.go imports db + uses type alias" ok
  else
    check "internal/feature/$pkg/dbsource.go imports db + uses type alias" fail
  fi
done

# 10. healthz delegates to skygatedb
file_grep '"skygate/internal/db"' "internal/feature/healthz/db_health.go" \
  && file_grep "type DBSource = skygatedb.DBSource" "internal/feature/healthz/db_health.go" \
  && check "healthz/db_health.go imports db + uses type alias" ok \
  || check "healthz/db_health.go imports db + uses type alias" fail

# 11. elector delegates to skygatedb
file_grep '"skygate/internal/db"' "internal/elector/elector.go" \
  && file_grep "type DBSource = skygatedb.DBSource" "internal/elector/elector.go" \
  && file_grep "skygatedb.FixedDBSource" "internal/elector/elector.go" \
  && check "elector/elector.go imports db + uses type alias + delegates FixedDBSource" ok \
  || check "elector/elector.go imports db + uses type alias + delegates FixedDBSource" fail

# 12. build + vet + tests
GO=""
if command -v go >/dev/null 2>&1; then
  GO="go"
else
  for cand in \
    "C:/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go"; do
    [ -x "$cand" ] && GO="$cand" && break
  done
fi
if [ -n "$GO" ]; then
  if "$GO" build ./... >/dev/null 2>&1; then
    check "go build ./... passes" ok
  else
    check "go build ./... passes" fail
  fi
  if "$GO" vet ./internal/db/... ./internal/feature/... ./internal/elector/... >/dev/null 2>&1; then
    check "go vet on the consolidated packages passes" ok
  else
    check "go vet on the consolidated packages passes" fail
  fi
  if "$GO" test ./internal/db/... ./internal/feature/... ./internal/elector/... -count=1 >/dev/null 2>&1; then
    check "go test on the consolidated packages passes" ok
  else
    check "go test on the consolidated packages passes" fail
  fi
else
  check "go binary not found (skipping build/vet/test)" fail
fi

# 13. AGENTS.md
if [ -f "AGENTS.md" ] && grep -qE "B210\.1" "AGENTS.md"; then
  check "AGENTS.md mentions B210.1" ok
else
  check "AGENTS.md mentions B210.1" fail
fi

# 14. verify_pre_deploy.sh
if [ -f "scripts/verify_pre_deploy.sh" ] && grep -q 'run_check "B210.1"' "scripts/verify_pre_deploy.sh"; then
  check "verify_pre_deploy.sh has B210.1 run_check" ok
else
  check "verify_pre_deploy.sh has B210.1 run_check" fail
fi

echo
echo "=== B210.1: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "FAILURES:"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
exit 0
