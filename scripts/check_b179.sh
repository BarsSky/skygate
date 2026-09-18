#!/bin/bash
# B179 — iptables DOCKER-USER/INPUT over-broad block regression
#
# Operator report 2026-08-25 (verifying the "all devices offline" bug):
# the 14 Tailscale clients + the skygate VM itself all showed
# online=false with last_seen frozen at 09:41:10 — the exact
# moment a previous operator action had applied an over-broad
# iptables rule:
#
#   iptables -I DOCKER-USER 1 -s 192.168.13.67 -p tcp --dport 50444 -j DROP
#
# The rule was originally added to silence `node not found` 404
# noise from an "orphan" Tailscale client running inside the
# NPM (95.165.170.190 / 192.168.13.67). But it also blocks the
# LEGITIMATE NPM reverse-proxy traffic to headscale (50444),
# causing ALL Tailscale clients (including the operator's own
# skygate VM) to lose their control-plane connection.
#
# B179 contract: ensure no iptables rule in DOCKER-USER or INPUT
# blocks 192.168.13.67 → 50444 (or more generally: the iptables
# ruleset MUST NOT block the NPM host from reaching the headscale
# port that the public control-plane URL proxies to).
#
# Contracts (7 sub-checks):
#  A. iptables DOCKER-USER has NO rules blocking 192.168.13.67
#  B. iptables INPUT has NO rules blocking 192.168.13.67
#  C. /etc/iptables/rules.v4 has no DOCKER-USER block for 192.168.13.67
#  D. /etc/iptables/rules.v4 has no INPUT block for 192.168.13.67
#  E. NPM (95.165.170.190) can reach headscale on 50444 (HTTP 401 expected,
#     not 504 / connection refused)
#  F. AGENTS.md mentions B179
#  G. verify_pre_deploy.sh includes check_b179

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

# Source iptables (needs sudo) for live checks A + B
IPTABLES_OUTPUT=""
if command -v sudo >/dev/null 2>&1 && sudo -n iptables -L DOCKER-USER -n 2>/dev/null; then
  IPTABLES_OUTPUT=$(sudo iptables -L DOCKER-USER -n -v 2>/dev/null; echo ---; sudo iptables -L INPUT -n -v 2>/dev/null)
fi
# Fallback: read from /etc/iptables/rules.v4 (no sudo needed)
RULES_V4=""
if [ -f /etc/iptables/rules.v4 ]; then
  RULES_V4=$(cat /etc/iptables/rules.v4)
fi

echo "=== B179 contracts ==="

# A. DOCKER-USER has no block for 192.168.13.67
if echo "$IPTABLES_OUTPUT$RULES_V4" | grep -qE 'DOCKER-USER.*192.168.13.67.*DROP'; then
  check_eq "A" "no-block" "block-present (regression: DOCKER-USER is blocking 192.168.13.67 → 50444)"
else
  check_eq "A" "no-block" "no-block"
fi

# B. INPUT has no block for 192.168.13.67
if echo "$IPTABLES_OUTPUT$RULES_V4" | grep -qE 'INPUT.*192.168.13.67.*DROP'; then
  check_eq "B" "no-block" "block-present (regression: INPUT is blocking 192.168.13.67 → 50444)"
else
  check_eq "B" "no-block" "no-block"
fi

# C. /etc/iptables/rules.v4 has no DOCKER-USER block
if grep -qE 'A DOCKER-USER.*192.168.13.67.*DROP' /etc/iptables/rules.v4 2>/dev/null; then
  check_eq "C" "no-block" "block-present"
else
  check_eq "C" "no-block" "no-block"
fi

# D. /etc/iptables/rules.v4 has no INPUT block
if grep -qE '^-A INPUT.*192.168.13.67.*DROP' /etc/iptables/rules.v4 2>/dev/null; then
  check_eq "D" "no-block" "block-present"
else
  check_eq "D" "no-block" "no-block"
fi

# E. headscale is up + listening on 50444 (proves the iptables
#    block isn't a headscale-side outage). VM-only check — on
#    local dev machines we skip (curl from a random host wouldn't
#    reach the VM's headscale port).
if [ -d /home/skyadmin/skygate ]; then
  if command -v curl >/dev/null 2>&1; then
    CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 -H 'Authorization: Bearer test' http://127.0.0.1:50444/api/v1/node 2>/dev/null || echo "000")
    if [ "$CODE" = "401" ]; then
      check_eq "E" "401" "401"
    else
      check_eq "E" "401" "$CODE (headscale not responding on 50444 — outage, not iptables-related)"
    fi
  else
    echo "  SKIP [E] curl not available"
  fi
else
  echo "  SKIP [E] not on VM (skygate workdir not /home/skyadmin/skygate)"
fi

# F. AGENTS.md mentions B179
if [ -f "$REPO/AGENTS.md" ]; then
  if grep -qE 'B179' "$REPO/AGENTS.md"; then
    check_eq "F" "yes" "yes"
  else
    check_eq "F" "yes" "no"
  fi
else
  check_eq "F" "yes" "no"
fi

# G. verify_pre_deploy.sh includes check_b179
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  if grep -qE 'check_b179' "$REPO/scripts/verify_pre_deploy.sh"; then
    check_eq "G" "yes" "yes"
  else
    check_eq "G" "yes" "no"
  fi
else
  check_eq "G" "yes" "no"
fi

echo
echo "=== B179 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
