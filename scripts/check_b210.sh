#!/usr/bin/env bash
# B210 (v1.5.0+) — DBSource pattern for non-admin services
# (auth, my, exit_rules, feature/cluster).
#
# Phase 3 of docs/internal/cluster-management.md. Closes
# the B203 hot-reload regression for ALL services that
# previously captured `*sql.DB` at boot. The B208.1 fix
# only covered the admin package; the auth/my/exit_rules
# packages kept the captured-pointer pattern and broke
# on every B203 watchdog swap (the user saw "empty
# devices tab + unchanged theme" after every skygate
# restart, since auth login + the my/devices page were
# both broken — the user-reported symptom that triggered
# B210).
#
# The contracts:
#
#   1. internal/feature/auth/dbsource.go exists
#   2. auth.Service.DB field type is DBSource (not *sql.DB)
#   3. auth Service has a dbc() helper that returns s.DB.Current()
#   4. auth service has 0 remaining `s.DB.method` call sites
#      (all replaced with `s.dbc().method`)
#   5. main.go passes `d` (the ResettableDB) to authSvc.DB
#   6. internal/feature/my/dbsource.go exists
#   7. my.Service.DB field type is DBSource
#   8. my has 0 remaining `s.DB.method` call sites
#   9. main.go passes `d` to mySvc.DB
#  10. internal/feature/exit_rules/dbsource.go exists
#  11. exit_rules.Service.DB field type is DBSource
#  12. exit_rules has 0 remaining `s.DB.method` call sites
#  13. main.go passes `d` to exitRulesSvc.DB
#  14. internal/feature/cluster/dbsource.go exists
#  15. cluster feature Service.DB field type is DBSource
#  16. main.go passes `d` to clusterAPI.DB
#  17. build + vet + tests pass for all 4 packages
#  18. AGENTS.md mentions B210
#  19. verify_pre_deploy.sh has a B210 run_check
#
# Note: the healthz + elector + admin + B203 ResettableDB
# packages all have their own (or shared) DBSource
# patterns already — the consolidation into
# `internal/db/dbsource.go` is a follow-up (mentioned in
# B208.1's AGENTS.md).

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

# 1. auth dbsource.go
file_exists "internal/feature/auth/dbsource.go" \
  && check "internal/feature/auth/dbsource.go exists" ok \
  || check "internal/feature/auth/dbsource.go exists" fail

# 2. auth Service.DB type
file_grep "DB\s+DBSource" "internal/feature/auth/service.go" \
  && check "auth.Service.DB is DBSource (not *sql.DB)" ok \
  || check "auth.Service.DB is DBSource (not *sql.DB)" fail

# 3. auth dbc() helper
file_grep "func \(s \*Service\) dbc\(\) \*sql\.DB" "internal/feature/auth/dbsource.go" \
  && check "auth Service has dbc() helper" ok \
  || check "auth Service has dbc() helper" fail

# 4. auth zero remaining s.DB. call sites
auth_remaining=$(grep -cE 's\.DB\.[A-Za-z]' "internal/feature/auth/service.go" 2>/dev/null)
auth_remaining=${auth_remaining:-0}
[ "$auth_remaining" = "0" ] \
  && check "auth has 0 remaining s.DB.method call sites" ok \
  || check "auth has 0 remaining s.DB.method call sites (got $auth_remaining)" fail

# 5. main.go passes d to authSvc
file_grep "authSvc := &authsvc\.Service" "cmd/skygate/main.go" && \
  file_grep "DB:\s+d," "cmd/skygate/main.go" \
  && check "main.go passes ResettableDB to authSvc" ok \
  || check "main.go passes ResettableDB to authSvc" fail

# 6. my dbsource.go
file_exists "internal/feature/my/dbsource.go" \
  && check "internal/feature/my/dbsource.go exists" ok \
  || check "internal/feature/my/dbsource.go exists" fail

# 7. my Service.DB type
file_grep "DB\s+DBSource" "internal/feature/my/service.go" \
  && check "my.Service.DB is DBSource (not *sql.DB)" ok \
  || check "my.Service.DB is DBSource (not *sql.DB)" fail

# 8. my zero remaining s.DB. call sites
my_remaining=0
for f in internal/feature/my/*.go; do
  [ "$f" = "internal/feature/my/dbsource.go" ] && continue
  [ -f "$f" ] || continue
  c=$(grep -cE 's\.DB\.[A-Za-z]' "$f" 2>/dev/null)
  c=${c:-0}
  my_remaining=$((my_remaining + c))
done
[ "$my_remaining" = "0" ] \
  && check "my has 0 remaining s.DB.method call sites" ok \
  || check "my has 0 remaining s.DB.method call sites (got $my_remaining)" fail

# 9. main.go passes d to mySvc
file_grep "mySvc := &mysvc\.Service" "cmd/skygate/main.go" && \
  file_grep "DB:\s+d," "cmd/skygate/main.go" \
  && check "main.go passes ResettableDB to mySvc" ok \
  || check "main.go passes ResettableDB to mySvc" fail

# 10. exit_rules dbsource.go
file_exists "internal/feature/exit_rules/dbsource.go" \
  && check "internal/feature/exit_rules/dbsource.go exists" ok \
  || check "internal/feature/exit_rules/dbsource.go exists" fail

# 11. exit_rules Service.DB type
file_grep "DB\s+DBSource" "internal/feature/exit_rules/service.go" \
  && check "exit_rules.Service.DB is DBSource (not *sql.DB)" ok \
  || check "exit_rules.Service.DB is DBSource (not *sql.DB)" fail

# 12. exit_rules zero remaining s.DB. call sites
exr_remaining=0
for f in internal/feature/exit_rules/*.go; do
  [ "$f" = "internal/feature/exit_rules/dbsource.go" ] && continue
  [[ "$f" == *_test.go ]] && continue
  [ -f "$f" ] || continue
  c=$(grep -cE 's\.DB\.[A-Za-z]' "$f" 2>/dev/null)
  c=${c:-0}
  exr_remaining=$((exr_remaining + c))
done
[ "$exr_remaining" = "0" ] \
  && check "exit_rules has 0 remaining s.DB.method call sites" ok \
  || check "exit_rules has 0 remaining s.DB.method call sites (got $exr_remaining)" fail

# 13. main.go passes d to exitRulesSvc
file_grep "exitRulesSvc := &exitrules\.Service" "cmd/skygate/main.go" && \
  file_grep "DB:\s+d," "cmd/skygate/main.go" \
  && check "main.go passes ResettableDB to exitRulesSvc" ok \
  || check "main.go passes ResettableDB to exitRulesSvc" fail

# 14. feature/cluster dbsource.go
file_exists "internal/feature/cluster/dbsource.go" \
  && check "internal/feature/cluster/dbsource.go exists" ok \
  || check "internal/feature/cluster/dbsource.go exists" fail

# 15. cluster feature Service.DB type
file_grep "DB\s+DBSource" "internal/feature/cluster/handlers.go" \
  && check "cluster.Service.DB is DBSource (not *sql.DB)" ok \
  || check "cluster.Service.DB is DBSource (not *sql.DB)" fail

# 16. main.go passes d to clusterAPI
file_grep "clusterAPI := &clusterapi\.Service" "cmd/skygate/main.go" && \
  file_grep "DB:\s+d," "cmd/skygate/main.go" \
  && check "main.go passes ResettableDB to clusterAPI" ok \
  || check "main.go passes ResettableDB to clusterAPI" fail

# 17. build + vet + tests
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
  if "$GO" vet ./internal/feature/auth/... ./internal/feature/my/... ./internal/feature/exit_rules/... ./internal/feature/cluster/... >/dev/null 2>&1; then
    check "go vet on the 4 B210 packages passes" ok
  else
    check "go vet on the 4 B210 packages passes" fail
  fi
  if "$GO" test ./internal/feature/auth/... ./internal/feature/my/... ./internal/feature/exit_rules/... ./internal/feature/cluster/... -count=1 >/dev/null 2>&1; then
    check "go test on the 4 B210 packages passes" ok
  else
    check "go test on the 4 B210 packages passes" fail
  fi
else
  check "go binary not found (skipping build/vet/test)" fail
fi

# 18. AGENTS.md
if [ -f "AGENTS.md" ] && grep -qE "B210" "AGENTS.md"; then
  check "AGENTS.md mentions B210" ok
else
  check "AGENTS.md mentions B210" fail
fi

# 19. verify_pre_deploy.sh
if [ -f "scripts/verify_pre_deploy.sh" ] && grep -q 'run_check "B210"' "scripts/verify_pre_deploy.sh"; then
  check "verify_pre_deploy.sh has B210 run_check" ok
else
  check "verify_pre_deploy.sh has B210 run_check" fail
fi

echo
echo "=== B210: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "FAILURES:"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
exit 0
