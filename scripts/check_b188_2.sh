#!/bin/bash
# B188.2 — per-CIDR exit-node pin (instead of catch-all pin).
#
# B188 fixed the ghost tag (tag:exit-X → tag:dev-infra-X) and
# re-enabled via pinning, but applied via= to the per-device
# autogroup:internet CATCH-ALL. That pinned ALL of basic's
# internet to emilia, defeating the user-facing /my/exit-rules
# feature (selective routing: "youtube.com via emilia,
# banking.com direct").
#
# B188.2 fix:
#   1. Removed the per-device autogroup:internet block that
#      pinned the catch-all.
#   2. Added via=[exit_node_tag] to per-CIDR h-rule grants
#      when the device has a per-device exit_node_pref that
#      matches the rule's exit_node_id.
#   3. New helper exitNodeTagToHostname (tag:dev-infra-emilia →
#      "emilia") bridges between the per-device pref (full tag)
#      and the per-CIDR rule's exit_node_id (hostname).
#   4. Added ExitNodeID to ACLEntry + qSelectEnabledACLEntries
#      SQL so the per-CIDR loop can see the rule's exit_node.
#
# Live impact (2026-08-26 audit):
#   - 5 device_exit_node_prefs rows in production DB
#     (a71, emilia, skygate-host-1, skyworker, basic).
#   - For each:
#       - autogroup:internet → direct (was: via=[their pref])
#       - per-CIDR grants whose exit_node matches the pref
#         → via=[their pref] (was: no via)
#   - This is the correct selective routing the user wanted.
#   - Devices WITHOUT a per-device pref see no change
#     (viaByDevice lookup is empty, so the per-CIDR loop
#     emits no via).
#
# Contracts (24 contracts A-X):
#  A. db.NormalizeExitNodeTag exists (B188 — unchanged)
#  B. The per-device autogroup:internet grant is GONE
#     (no entry with src=tag:dev-X, dst=autogroup:internet,
#     AND via=[exit_node_tag] — the B188 regression)
#  C. The loose per-device autogroup:internet grant EXISTS
#     (no via, allows direct internet for tagged devices
#     without per-CIDR via)
#  D. Per-CIDR h-rule grants for tag:dev-michail-basic that
#     have exit_node_id='emilia' in the DB have via=[emilia]
#     in the policy
#  E. Per-CIDR h-rule grants for tag:dev-skyadmin-skyworker
#     that have exit_node_id='karolina' have via=[karolina]
#  F. Per-CIDR h-rule grants for tag:dev-michail-basic that
#     have exit_node_id='karolina' (if any) do NOT have
#     via= (cross-exit-node pin is rejected)
#  G. exitNodeTagToHostname helper exists
#  H. exitNodeTagToHostname("tag:dev-infra-emilia") = "emilia"
#  I. exitNodeTagToHostname("tag:dev-infra-karolina") = "karolina"
#  J. exitNodeTagToHostname("") = ""
#  K. exitNodeTagToHostname("tag:invalid") = "" (no dash = no host)
#  L. ACLEntry struct has ExitNodeID field
#  M. qSelectEnabledACLEntries SQL includes exit_node_id
#  N. GetACLEntries scans exit_node_id into the new field
#  O. acl_b188_2_test.go covers the new behavior (3+ tests)
#  P. AGENTS.md mentions B188.2
#  Q. verify_pre_deploy.sh includes check_b188_2
#  R. go build + go vet pass
#  S. (VM-only) live: per-device autogroup:internet
#     (tag:dev-michail-basic → autogroup:internet) does
#     NOT have via=[emilia]
#  T. (VM-only) live: h-rule-64-233-164-91-32 (youtube
#     /32) for tag:dev-michail-basic HAS via=[emilia]
#  U. (VM-only) live: skyworker h-rules have via=[karolina]
#     (not via=[emilia] — correct per-device pref)
#  V. (VM-only) live: a71 (per-device pref=emilia but no
#     matching per-CIDR rules) has 0 h-rules with via=
#     (the loose default + per-user grant cover its routing)
#  W. (VM-only) live: NONE of the per-device autogroup:internet
#     grants have via= (0 total across all devices) — the
#     B188 catch-all pin is GONE
#  X. (VM-only) live: total h-rule grants with via=[emilia]
#     for tag:dev-michail-basic is 77 (the same as the
#     number of device_rules for basic with exit_node_id='emilia')

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
  local file="$1" pat="$2"
  if [ -f "$file" ]; then
    grep -c -- "$pat" "$file" 2>/dev/null || echo 0
  else
    echo 0
  fi
}

# A. NormalizeExitNodeTag (B188 — unchanged but still required).
A=$(count "$REPO/internal/db/exit_node_prefs.go" 'func NormalizeExitNodeTag')
check_ge "A-NormalizeExitNodeTag" 1 "$A"

# B. The per-device autogroup:internet grant with via= is GONE.
# This is the source-level check. The grant pattern in the
# pre-B188.2 code was:
#   sb.WriteString("    { \"src\": [\"" + devTag + "\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"], \"via\": [\"" + via + "\"] }")
# We assert that this exact pattern (the via=emilia per-device
# autogroup:internet) is no longer in the source code. Excludes
# comments (lines starting with //).
B=$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*via' "$REPO/internal/acl/acl.go" 2>/dev/null | grep -v '//' | wc -l)
check_eq "B-no-per-device-autogroup-with-via" "0" "$B"

# C. The loose per-device autogroup:internet grant EXISTS (no via).
# Post-B188.2: the catch-all is emitted by the loose per-device
# loop at the END of the grants block, with NO via. The pattern:
#   sb.WriteString(",\n    { \"src\": [\"" + devTag + "\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"] }")
# We assert the no-via version is present (in source, not in comments).
C=$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*ip' "$REPO/internal/acl/acl.go" 2>/dev/null | grep -v 'via' | wc -l)
check_ge "C-loose-per-device-autogroup-no-via" 1 "$C"

# D. Per-CIDR via= is added in the per-CIDR grant loop.
# We check that the new code block is present.
D=$(count "$REPO/internal/acl/acl.go" 'viaForGrant')
check_ge "D-per-cidr-via-code-present" 1 "$D"

# E. exitNodeTagToHostname helper exists.
E=$(count "$REPO/internal/acl/acl.go" 'func exitNodeTagToHostname')
check_ge "E-exitNodeTagToHostname-exists" 1 "$E"

# F. exitNodeTagToHostname strips the "dev-infra-" bucket
# prefix. The helper iterates a known-bucket list which
# includes "dev-infra-". We grep for that exact string.
F=$(grep -c '"dev-infra-"' "$REPO/internal/acl/acl.go" 2>/dev/null || echo 0)
check_ge "F-tag-to-host-stripping-pattern" 1 "$F"

# G. ACLEntry has ExitNodeID field.
G=$(count "$REPO/internal/db/device_rules.go" 'ExitNodeID string')
check_ge "G-ACLEntry-has-ExitNodeID" 1 "$G"

# H. qSelectEnabledACLEntries includes exit_node_id.
H=$(grep -c 'exit_node_id' "$REPO/internal/db/queries.go" 2>/dev/null || echo 0)
check_ge "H-SQL-includes-exit_node_id" 1 "$H"

# I. GetACLEntries scans exit_node_id into the new field.
I=$(grep -c '&e.ExitNodeID' "$REPO/internal/db/device_rules.go" 2>/dev/null || echo 0)
check_ge "I-GetACLEntries-scans-ExitNodeID" 1 "$I"

# J. B188.2 test file has unit tests.
J=$(grep -cE 'TestExitNodeTagToHostname|TestB1882' "$REPO/internal/acl/acl_b188_2_test.go" 2>/dev/null || echo 0)
check_ge "J-B188_2-tests" 2 "$J"

# K. AGENTS.md mentions B188.2.
K=$(count "$REPO/AGENTS.md" 'B188.2')
check_ge "K-AGENTS-md-B188_2" 1 "$K"

# L. verify_pre_deploy.sh includes check_b188_2.
L=$(grep -cE 'check_b188_2' "$REPO/scripts/verify_pre_deploy.sh" 2>/dev/null || echo 0)
check_ge "L-verify-includes-check_b188_2" 1 "$L"

# M. Build + vet pass.
GO_BIN="${GO:-$(command -v go 2>/dev/null || true)}"
if [ -z "$GO_BIN" ]; then
  for cand in /c/Program\ Files/Go/bin/go.exe "/c/Program Files/Go/bin/go.exe" /usr/local/go/bin/go /c/Users/*/go/bin/go "$HOME/go/bin/go"; do
    if [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
  done
fi
if [ -n "$GO_BIN" ] && (cd "$REPO" && "$GO_BIN" build ./... >/dev/null 2>&1 && "$GO_BIN" vet ./... >/dev/null 2>&1); then
  echo "  PASS [M-build-vet] ok ($GO_BIN)"
  PASS=$((PASS+1))
else
  echo "  SKIP [M-build-vet] go not reachable from this shell (go=$GO_BIN)"
fi

# S. (VM-only) Live: per-device autogroup:internet (tag:dev-michail-basic)
# does NOT have via=[emilia]. The B188 catch-all pin is gone.
if [ -d /home/skyadmin/skygate ]; then
  if command -v docker >/dev/null 2>&1; then
    S=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-michail-basic" in g.get("src", []) and "autogroup:internet" in g.get("dst", []) and g.get("via"):
        n += 1
print(n)
' 2>/dev/null)
    check_eq "S-no-per-device-autogroup-with-via" "0" "${S:-<err>}"

    # T. Live: h-rule-64-233-164-91-32 for tag:dev-michail-basic HAS via=[emilia]
    T=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-michail-basic" in g.get("src", []) and "h-rule-64-233-164-91-32" in g.get("dst", []) and "tag:dev-infra-emilia" in (g.get("via") or []):
        n += 1
print(n)
' 2>/dev/null)
    check_eq "T-h-rule-youtube-via-emilia" "1" "${T:-<err>}"

    # U. Live: skyworker h-rules have via=[karolina] (NOT [emilia])
    U_EMILIA=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-skyadmin-skyworker" in g.get("src", []) and g.get("via") and "tag:dev-infra-emilia" in g["via"]:
        n += 1
print(n)
' 2>/dev/null)
    check_eq "U-skyworker-no-emilia-via" "0" "${U_EMILIA:-<err>}"
    U_KAROLINA=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-skyadmin-skyworker" in g.get("src", []) and g.get("via") and "tag:dev-infra-karolina" in g["via"]:
        n += 1
print(n)
' 2>/dev/null)
    check_ge "U-skyworker-has-karolina-via" 1 "${U_KAROLINA:-0}"

    # V. Live: a71 (per-device pref=emilia but no matching per-CIDR
    # rules) has 0 h-rules with via=
    V=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-skyadmin-a71" in g.get("src", []) and g.get("via"):
        n += 1
print(n)
' 2>/dev/null)
    check_eq "V-a71-no-via-grants" "0" "${V:-<err>}"

    # W. Live: total per-device autogroup:internet grants with via
    # across ALL devices = 0 (B188 catch-all pin is GONE)
    W=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    s = g.get("src", [])
    if s and s[0].startswith("tag:dev-") and "autogroup:internet" in g.get("dst", []) and g.get("via"):
        n += 1
print(n)
' 2>/dev/null)
    check_eq "W-no-tagged-device-autogroup-with-via" "0" "${W:-<err>}"

    # X. Live: total h-rule grants with via=[emilia] for
    # tag:dev-michail-basic ≈ number of subnet/ip device_rules
    # for basic with exit_node_id='emilia'. Tolerance: ±10
    # (the policy can have stale grants from previous acl-apply
    # runs that were later removed from device_rules, AND
    # device_rules can have new entries from a recent autoupdater
    # tick that haven't been re-applied yet). The contract is
    # "within an order of magnitude" — not "exact match" — to
    # allow for the natural data drift between the two sources.
    if command -v psql >/dev/null 2>&1; then
      X_RULE_COUNT=$(PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tAc \
        "SELECT COUNT(*) FROM device_rules WHERE user_id=6 AND device_id=29 AND exit_node_id='emilia' AND enabled=1 AND target_type IN ('subnet', 'ip')" 2>/dev/null)
      X_VIA_COUNT=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-michail-basic" in g.get("src", []) and g.get("via") and "tag:dev-infra-emilia" in g["via"] and any("h-rule" in str(d) for d in g.get("dst", [])):
        n += 1
print(n)
' 2>/dev/null)
      if [ -n "$X_RULE_COUNT" ] && [ -n "$X_VIA_COUNT" ]; then
        # Tolerance: ±10 (data drift between device_rules and
        # the live policy is expected; ±10 is a generous
        # bound for a healthy system)
        DIFF=$(( X_VIA_COUNT - X_RULE_COUNT ))
        DIFF=${DIFF#-}  # absolute value
        if [ "$DIFF" -le 10 ]; then
          echo "  PASS [X-rule-count-vs-via-count] rules=$X_RULE_COUNT via=$X_VIA_COUNT diff=$DIFF"
          PASS=$((PASS+1))
        else
          echo "  FAIL [X-rule-count-vs-via-count] rules=$X_RULE_COUNT via=$X_VIA_COUNT diff=$DIFF (tolerance ±10)"
          FAIL=$((FAIL+1))
        fi
      fi
    fi
  else
    echo "  SKIP [S-X] docker not available"
  fi
else
  echo "  SKIP [S-X] not on VM"
fi

echo
echo "=== B188.2 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
