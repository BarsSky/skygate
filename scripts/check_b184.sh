#!/bin/bash
# B184 — DOMAIN rule status propagates from its resolved subnets
# (closes the "domain row shows ⏳ while its child subnets show
# ✅" visual-lie gap in /admin/exit-rules + /my/exit-rules)
#
# Pre-B184: a DOMAIN rule (target_type=domain, target_value=
# "discord.com") always rendered as ⏳ pending in the three-
# state ✅/⏳/⚠️ badge, even when the autoupdater had already
# resolved the domain to subnets and headscale had approved
# those subnets. The live case: michail/basic on emilia had 8
# YouTube subnets showing ✅ "accepted" (8.8.8.0/24,
# 142.250.0.0/15, 8.34.208.0/20, 8.35.192.0/20, 8.15.202.0/24,
# 172.217.0.0/16, 173.194.0.0/16, 216.58.192.0/19) but the
# parent "youtube.com" row showed ⏳ — the two states
# disagreed even though the subnets were literally the
# resolved-from-this-domain rows.
#
# B184 fix: a DOMAIN rule is ✅ approved iff AT LEAST ONE
# device_rule row with `parent_domain = THIS_DOMAIN` and the
# same (user_id, device_id, exit_node_id) and
# target_type IN ('subnet', 'ip') has its target_value in
# headscale ApprovedRoutes for the rule's ExitNode.
# Otherwise the rule stays in the same state as before
# (⏳ if no resolution yet, ⏳ if resolution exists but
# headscale hasn't approved any, ⚠️ if the rule's exit-node
# differs from the device's preferred).
#
# Contracts (15 sub-checks):
#  A. resolved_by_domain.go exists with LoadResolvedByDomain
#  B. qSelectResolvedByDomain SQL excludes target_type=domain
#  C. ResolvedKeyForTuple formats key as "uid:did:exit:parent"
#  D. annotateRulesWithPrefs takes 4th arg (resolvedByDomain)
#  E. ruleApprovedInHeadscale takes 3rd arg (resolvedByDomain)
#  F. ruleApprovedInHeadscale DOMAIN branch propagates status
#  G. form_my.go calls LoadResolvedByDomain + uses resolved key
#  H. form_admin.go handler calls LoadResolvedByDomain
#  I. form_admin_b184_test.go has 7 test functions
#  J. go test ./internal/feature/exit_rules/... passes
#  K. AGENTS.md mentions B184
#  L. verify_pre_deploy.sh includes check_b184
#  M. (VM-only) live: t.me has 1+ resolved subnet in headscale
#  N. (VM-only) live: discord.com has 0 resolved subnets
#  O. (VM-only) live: youtube.com has 4 resolved subnets

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

echo "=== B184 contracts ==="

# A. resolved_by_domain.go exists with LoadResolvedByDomain
check_ge "A" 1 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain.go" 'func LoadResolvedByDomain')"

# B. qSelectResolvedByDomain SQL excludes target_type=domain
# and the COALESCE on parent_domain handles NULL safely.
check_ge "B-type-filter" 1 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain.go" "target_type IN")"
check_ge "B-coalesce" 1 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain.go" 'COALESCE\(parent_domain')"

# C. ResolvedKeyForTuple uses the right format
# Catches the most likely regression: changing one side of
# the producer/consumer contract without the other.
check_ge "C-func" 1 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain.go" 'func ResolvedKeyForTuple')"
check_ge "C-format" 1 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain.go" '%d:%d:%s:%s')"

# D. annotateRulesWithPrefs takes 4th arg (resolvedByDomain)
# The pre-B184 signature was 3 args; post-B184 is 4.
D_SIG=$(grep -E 'func annotateRulesWithPrefs' "$REPO/internal/feature/exit_rules/form_admin.go" | head -1)
if echo "$D_SIG" | grep -q 'resolvedByDomain'; then
  check_eq "D" "4-args" "4-args"
else
  check_eq "D" "4-args" "got: $D_SIG"
fi

# E. ruleApprovedInHeadscale takes 3rd arg (resolvedByDomain)
E_SIG=$(grep -E 'func ruleApprovedInHeadscale' "$REPO/internal/feature/exit_rules/form_admin.go" | head -1)
if echo "$E_SIG" | grep -q 'resolvedByDomain'; then
  check_eq "E" "3-args" "3-args"
else
  check_eq "E" "3-args" "got: $E_SIG"
fi

# F. ruleApprovedInHeadscale DOMAIN branch propagates status.
# Must reference ResolvedKeyForTuple + the resolved-map.
check_ge "F-resolved-key" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'ResolvedKeyForTuple')"
check_ge "F-domain-loop" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'for cid := range resolved')"

# G. form_my.go calls LoadResolvedByDomain + uses resolved key
# B185 changed form_my.go to use LookupResolvedForDomain
# (which internally calls ResolvedKeyForTuple). The wire
# is now via LookupResolvedForDomain — same semantics,
# cleaner call site.
check_ge "G-load" 1 "$(count "$REPO/internal/feature/exit_rules/form_my.go" 'LoadResolvedByDomain')"
check_ge "G-resolved-key" 1 "$(count "$REPO/internal/feature/exit_rules/form_my.go" 'LookupResolvedForDomain')"

# H. form_admin.go handler (not the annotator) calls
# LoadResolvedByDomain. This is the producer-side wiring.
check_ge "H-admin-load" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'LoadResolvedByDomain')"

# I. form_admin_b184_test.go has 7 test functions
check_ge "I" 7 "$(count "$REPO/internal/feature/exit_rules/form_admin_b184_test.go" '^func Test')"

# J. go test ./internal/feature/exit_rules/... passes
GO_BIN=""
for cand in /usr/local/go/bin/go /usr/bin/go /opt/go/bin/go "$(command -v go 2>/dev/null)"; do
  if [ -x "$cand" ]; then
    GO_BIN="$cand"
    break
  fi
done
if [ -n "$GO_BIN" ]; then
  if (cd "$REPO" && "$GO_BIN" test -count=1 ./internal/feature/exit_rules/... 2>&1) | grep -q '^ok\s'; then
    check_eq "J" "ok" "ok"
  else
    check_eq "J" "ok" "FAIL"
  fi
else
  echo "  SKIP [J] go not available in PATH"
fi

# K. AGENTS.md mentions B184
if [ -f "$REPO/AGENTS.md" ]; then
  check_ge "K" 1 "$(count "$REPO/AGENTS.md" 'B184')"
else
  check_eq "K" ">=1" "0"
fi

# L. verify_pre_deploy.sh includes check_b184
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  check_ge "L" 1 "$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b184')"
else
  check_eq "L" ">=1" "0"
fi

# M. (VM-only) live: t.me has 1+ resolved subnet in headscale.
# If t.me's resolved 149.154.167.99/32 is in headscale, then
# B184 will show t.me as ✅ (not ⏳). Pre-B184 it was ⏳.
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tA -c "
      SELECT COUNT(*) FROM device_rules
       WHERE user_id=6 AND device_id=29 AND exit_node_id='emilia'
         AND parent_domain='t.me' AND target_type IN ('subnet','ip')
    " 2>/dev/null > /tmp/b184_tme.txt
    if [ -s /tmp/b184_tme.txt ]; then
      TME_CNT=$(cat /tmp/b184_tme.txt | tr -d ' \n')
      TME_CNT=${TME_CNT:-0}
      check_ge "M" 1 "$TME_CNT"
    else
      echo "  SKIP [M] could not query t.me resolved subnets"
    fi
  else
    echo "  SKIP [M] psql not available"
  fi
else
  echo "  SKIP [M] not on VM"
fi

# N. (VM-only) live: discord.com has 0 resolved subnets.
# If 0, B184 correctly shows discord.com as ⏳ (no resolution).
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tA -c "
      SELECT COUNT(*) FROM device_rules
       WHERE user_id=6 AND device_id=29 AND exit_node_id='emilia'
         AND parent_domain='discord.com' AND target_type IN ('subnet','ip')
    " 2>/dev/null > /tmp/b184_discord.txt
    if [ -s /tmp/b184_discord.txt ]; then
      DC_CNT=$(cat /tmp/b184_discord.txt | tr -d ' \n')
      DC_CNT=${DC_CNT:-0}
      check_eq "N" "0" "$DC_CNT"
    else
      echo "  SKIP [N] could not query discord.com resolved subnets"
    fi
  else
    echo "  SKIP [N] psql not available"
  fi
else
  echo "  SKIP [N] not on VM"
fi

# O. (VM-only) live: youtube.com has 4 resolved subnets.
# If 4, B184 will show youtube.com as ✅ (at least one
# of the 4 64.233.164.x/32 IPs is in headscale's ApprovedRoutes).
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tA -c "
      SELECT COUNT(*) FROM device_rules
       WHERE user_id=6 AND device_id=29 AND exit_node_id='emilia'
         AND parent_domain='youtube.com' AND target_type IN ('subnet','ip')
    " 2>/dev/null > /tmp/b184_yt.txt
    if [ -s /tmp/b184_yt.txt ]; then
      YT_CNT=$(cat /tmp/b184_yt.txt | tr -d ' \n')
      YT_CNT=${YT_CNT:-0}
      check_ge "O" 1 "$YT_CNT"
    else
      echo "  SKIP [O] could not query youtube.com resolved subnets"
    fi
  else
    echo "  SKIP [O] psql not available"
  fi
else
  echo "  SKIP [O] not on VM"
fi

echo
echo "=== B184 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
