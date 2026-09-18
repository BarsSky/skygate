#!/bin/bash
# B180 — /admin/exit-nodes per-row "Re-sync" button: raw JSON → redirect
#
# The pre-B180 handler PostAdminExitNodeSync returned
# `Content-Type: application/json` to the browser. The per-row
# "Пере-синхронизировать" button in admin/exit_nodes.html is a
# regular `<form method="post">` (no JS), so the browser treated
# the JSON response as a raw text file and rendered it as
# "Качественная печать" (Chrome raw printout) instead of
# returning the operator to /admin/exit-nodes with a success
# flash.
#
# B180 fixes the handler to redirect to /admin/exit-nodes?ok=...
# or ?err=... like every other admin POST handler in the file.
# The page already has the flash mechanism (template line 38-42
# renders {{.FlashSuccess}} / {{.FlashError}} alerts; the GET
# handler reads r.URL.Query().Get("ok") / ("err") at line 300-301).
#
# Contracts (5 sub-checks):
#  A. PostAdminExitNodeSync does NOT use json.NewEncoder (the bug)
#  B. PostAdminExitNodeSync DOES use http.Redirect (the fix)
#  C. The redirect target is /admin/exit-nodes?ok= or ?err=
#  D. AGENTS.md mentions B180
#  E. verify_pre_deploy.sh includes check_b180

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
  n=$(grep -cE "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

echo "=== B180 contracts ==="

# Find PostAdminExitNodeSync body boundaries (start func to next ^})
SRC="$REPO/internal/feature/admin/exit_nodes.go"
START=$(grep -n 'func (s \*Service) PostAdminExitNodeSync' "$SRC" | head -1 | cut -d: -f1)
if [ -z "$START" ]; then
  echo "  FAIL [A] could not find PostAdminExitNodeSync in $SRC"
  exit 1
fi
# The function ends at the first "}" at column 0 AFTER the start line
END=$(awk -v s="$START" 'NR > s && /^}/ { print NR; exit }' "$SRC")
BODY=$(sed -n "${START},${END}p" "$SRC")

# A. NO json.NewEncoder in PostAdminExitNodeSync
A=$(echo "$BODY" | grep -cE 'json\.NewEncoder|w\.Write\(\[\]byte\(.*\)\)|fmt\.Fprintf\(w,.*"\{' || true)
# Count the json.NewEncoder specifically
A_ENC=$(echo "$BODY" | grep -c 'json\.NewEncoder' || true)
A_ENC=${A_ENC:-0}
if [ "$A_ENC" = "0" ]; then
  check_eq "A" "0" "0"
else
  check_eq "A" "0" "$A_ENC (regression: handler still returns raw JSON)"
fi

# B. http.Redirect IS used
B=$(echo "$BODY" | grep -c 'http\.Redirect' || true)
B=${B:-0}
check_ge "B" 1 "$B"

# C. redirect target is /admin/exit-nodes?ok= or ?err=
C=$(echo "$BODY" | grep -cE '"/admin/exit-nodes\?ok=|"/admin/exit-nodes\?err=' || true)
C=${C:-0}
check_ge "C" 1 "$C"

# D. AGENTS.md mentions B180
if [ -f "$REPO/AGENTS.md" ]; then
  D=$(count "$REPO/AGENTS.md" 'B180')
  check_ge "D" 1 "$D"
else
  check_eq "D" ">=1" "0"
fi

# E. verify_pre_deploy.sh includes check_b180
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  E=$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b180')
  check_ge "E" 1 "$E"
else
  check_eq "E" ">=1" "0"
fi

echo
echo "=== B180 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
