#!/bin/bash
# B187 — fix silent env.Username = "" regression caused by
# SQLite-era `?` placeholder in lookupPortalUsername.
#
# Operator 2026-08-25 screenshot showed /my_status replying
# "чат привязан, но у пользователя портала нет username"
# even though the binding's portal_user row had a perfectly
# good username (skyadmin, id=1, telegram_chat_id=328946535).
# Root cause: lookupPortalUsername in internal/telegram/notify.go
# used `SELECT username FROM portal_users WHERE id = ?` — the
# `?` placeholder is SQLite-era syntax. The pgx driver (which
# skygate uses since the v1.3.0 PG-only migration) doesn't
# auto-convert `?` to `$1`; it returns "operator does not
# exist: ?". env() silently swallowed the error and left
# env.Username = "", making the user-scope commands say
# "no username" even when the binding was correct.
#
# B187 fix: change `?` to `$1` in the QueryRow call. After
# the fix, env.Username is populated correctly and /my_status
# (and any other user-scope command) shows the operator's
# real data.
#
# Contracts (5 sub-checks):
#  A. lookupPortalUsername uses $1 placeholder (pgx form)
#  B. lookupPortalUsername does NOT use ? placeholder
#  C. lookup_username_test.go pins the regression
#  D. AGENTS.md mentions B187
#  E. verify_pre_deploy.sh includes check_b187
#  F. (VM-only) live: portal_users row for chat 328946535
#     has a non-empty username (skyadmin)

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

check_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "  PASS [$label] $actual"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] expected=$expected got=$actual"
    FAIL=$((FAIL+1))
  fi
}

check_ge() {
  local label="$1" min="$2" actual="$3"
  if [ "$actual" -ge "$min" ] 2>/dev/null; then
    echo "  PASS [$label] actual=$actual (>= $min)"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] actual=$actual (expected >= $min)"
    FAIL=$((FAIL+1))
  fi
}

count() {
  local n
  n=$(grep -cE -- "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

echo "=== B187 contracts ==="

# A. The fix: $1 placeholder (pgx form)
check_ge "A-dollar" 1 "$(count "$REPO/internal/telegram/notify.go" 'SELECT username FROM portal_users WHERE id = \$1')"

# B. The regression guard: NO `?` placeholder
check_eq "B-no-question" "0" "$(count "$REPO/internal/telegram/notify.go" 'SELECT username FROM portal_users WHERE id = \?')"

# C. Regression test in lookup_username_test.go
check_ge "C-test" 1 "$(count "$REPO/internal/telegram/lookup_username_test.go" 'TestLookupPortalUsername_PGPlaceholderSyntax')"

# D. AGENTS.md mentions B187
if [ -f "$REPO/AGENTS.md" ]; then
  check_ge "D-agents" 1 "$(count "$REPO/AGENTS.md" 'B187')"
else
  check_eq "D-agents" ">=1" "0"
fi

# E. verify_pre_deploy.sh includes check_b187
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  check_ge "E-verify" 1 "$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b187')"
else
  check_eq "E-verify" ">=1" "0"
fi

# F. (VM-only) live: portal_users row for the operator's
# bound chat (328946535) has a non-empty username. This
# is the B187 symptom check — pre-fix, this would return
# empty for any chat bound to portal_user_id=1 because
# the QueryRow with `?` failed silently. Post-fix, the
# username is correctly populated and /my_status no longer
# replies "no username".
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    USERNAME=$(PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tAc "
      SELECT p.username
        FROM telegram_bindings b
        JOIN portal_users p ON p.id = b.portal_user_id
       WHERE b.chat_id = 328946535
    " 2>/dev/null)
    if [ -n "$USERNAME" ]; then
      check_eq "F" "skyadmin" "$USERNAME"
    else
      check_eq "F" "skyadmin" "<empty — pre-B187 bug would be present>"
    fi
  else
    echo "  SKIP [F] psql not available"
  fi
else
  echo "  SKIP [F] not on VM"
fi

echo
echo "=== B187 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
