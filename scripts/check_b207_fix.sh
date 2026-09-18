#!/usr/bin/env bash
# B207_fix (v1.5.0+, 2026-09-02) — clear the B207 verify
# test artifact from cluster_database.current_dsn so the
# B203 skygate-watchdog doesn't keep swapping on every 5s
# tick after the verify.
#
# Background
#
# B207-verify set cluster_database.current_dsn to a
# deliberately wrong DSN so the /admin/audit UNION could
# exercise the cross-table join. The literal "skygate_admin_pass"
# password is the test artifact. B203 reads current_dsn on
# every tick; if it differs from the env DSN (which it
# does, because the password is wrong), the watchdog
# closes the old pool and tries to open a new one with
# the literal DSN. That swap fails (auth error), and
# every background service that captured *sql.DB at boot
# (backup scheduler, autoupdater, exit-node-monitor, AND
# the auth flow) gets "sql: database is closed" on every
# subsequent query — pre-B210 the login page would render
# "Неверные учётные данные" indefinitely until the
# operator manually clears the row. B210 fixed the auth/
# my/exit_rules/cluster services; the clear_test_dsn.sh
# fix prevents the bug from re-occurring on every verify
# run.
#
# Contracts:
#
#   1. scripts/clear_test_dsn.sh exists
#   2. clear_test_dsn.sh has correct shebang
#   3. clear_test_dsn.sh uses sudo -u postgres (NOT the
#      skygate_admin user — that's the wrong auth path for
#      cluster_database writes; the env DSN uses
#      user=admin which the postgres superuser can write
#      to without a password)
#   4. b208_verify.sh calls clear_test_dsn.sh as the final
#      step (so every b208 verify cleans up after itself)
#   5. AGENTS.md mentions B207_fix

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

# 1. clear_test_dsn.sh exists
file_exists "scripts/clear_test_dsn.sh" \
  && check "scripts/clear_test_dsn.sh exists" ok \
  || check "scripts/clear_test_dsn.sh exists" fail

# 2. shebang is correct
if [ -f "scripts/clear_test_dsn.sh" ]; then
  if head -1 "scripts/clear_test_dsn.sh" | grep -qE "^#!.*bash"; then
    check "clear_test_dsn.sh has correct shebang" ok
  else
    check "clear_test_dsn.sh has correct shebang" fail
  fi
fi

# 3. uses sudo -u postgres
if file_exists "scripts/clear_test_dsn.sh" && grep -qE "sudo -u postgres" "scripts/clear_test_dsn.sh"; then
  check "clear_test_dsn.sh uses sudo -u postgres (peer auth for cluster_database writes)" ok
else
  check "clear_test_dsn.sh uses sudo -u postgres (peer auth for cluster_database writes)" fail
fi

# 4. b208_verify.sh calls it as the final step
if [ -f "scripts/b208_verify.sh" ] && grep -qE "clear_test_dsn\.sh" "scripts/b208_verify.sh"; then
  check "b208_verify.sh calls clear_test_dsn.sh as the final step" ok
else
  check "b208_verify.sh calls clear_test_dsn.sh as the final step" fail
fi

# 5. AGENTS.md mentions B207_fix
if [ -f "AGENTS.md" ] && grep -qE "B207_fix" "AGENTS.md"; then
  check "AGENTS.md mentions B207_fix" ok
else
  check "AGENTS.md mentions B207_fix" fail
fi

echo
echo "=== B207_fix: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "FAILURES:"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
exit 0
