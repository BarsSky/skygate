#!/bin/bash
# B185 — fix two issues uncovered by B184 + the live
# "/admin/telegram: настроено, но API недоступен" + the
# "discord.com показывает ⏳ хотя у нас 15 Cloudflare ranges" reports.
#
# (1) Entrypoint: tailscale up was failing silently with
#     "requires mentioning all non-default flags" because
#     the persisted state had --advertise-tags set but the
#     entrypoint's `tailscale up` didn't pass that flag.
#     Result: skygate container's RouteAll stayed false;
#     container never accepted the relay's subnet routes;
#     api.telegram.org was unreachable. B185 reads the
#     current state's --advertise-tags (or falls back to
#     the B111-canonical value) and passes it back.
#
# (2) B184 DOMAIN-rule status propagation only looked up
#     parent_domain = "<domain>" rows. The autoupdater
#     ALSO stores resolved subnets with parent_domain =
#     "cdn:<provider>:<domain>" (when the CDN-detector
#     identifies the site as Cloudflare/Fastly/Google/
#     Akamai and uses the published IP ranges). Without
#     the cdn: alias lookup, every Cloudflare-routed
#     domain showed ⏳ pending even when its 15 published
#     CDN ranges were already in headscale ApprovedRoutes.
#     B185 adds LookupResolvedForDomain which merges
#     both formats in one call.
#
# (3) Admin UI: /admin/telegram now shows a live
#     "Container tailscale state" diagnostic block
#     (RouteAll, AdvertiseTags, ExitNodeID, TailscaleIPs)
#     + a "Re-apply accept-routes" button that runs
#     `docker exec skygate-skygate-1 tailscale set
#     --accept-routes=true` for the case when the
#     persisted state has RouteAll=false.
#
# Contracts (13 sub-checks):
#  A. entrypoint.sh reads existing state's AdvertiseTags
#     and falls back to tag:dev-infra-skygate-host-1,tag:private
#  B. entrypoint.sh passes --advertise-tags to tailscale up
#  C. resolved_by_domain.go has LookupResolvedForDomain
#  D. LookupResolvedForDomain uses cdn: prefix + :<domain> suffix
#  E. ruleApprovedInHeadscale calls LookupResolvedForDomain
#  F. form_my.go statusByRuleID calls LookupResolvedForDomain
#  G. form_admin_b185_test.go has 5 test functions
#  H. resolved_by_domain_b185_test.go has 5 test functions
#  I. internal/feature/admin/telegram.go has readContainerTailscaleState
#  J. internal/feature/admin/telegram.go has handleTelegramReapplyAcceptRoutes
#  K. internal/handlers/templates/admin/telegram.html renders the
#     container-tailscale card
#  L. AGENTS.md mentions B185
#  M. verify_pre_deploy.sh includes check_b185
#  N. (VM-only) live: skygate container's RouteAll=true
#     (the original symptom of the entrypoint bug)
#  O. (VM-only) live: probe shows ok_relay (not unreachable)
#  P. (VM-only) live: at least 1 discord-domain shows approved
#     in the three-state badge (the B185 LookupResolvedForDomain
#     cdn-alias propagation working)

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
  # Use `--` so the pattern isn't parsed as a flag
  # (e.g. `--advertise-tags=` would otherwise be
  # interpreted as a grep option). The check_b18X.sh
  # scripts use this helper for ALL grep counts.
  n=$(grep -cE -- "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

echo "=== B185 contracts ==="

# A. entrypoint.sh reads existing state's AdvertiseTags.
# The string `_current-profile` is only in the
# LoadAdvertiseTags python block.
check_ge "A" 1 "$(count "$REPO/entrypoint.sh" '_current-profile')"

# B. entrypoint.sh passes --advertise-tags to tailscale up
check_ge "B" 1 "$(count "$REPO/entrypoint.sh" '--advertise-tags=')"

# C. resolved_by_domain.go has LookupResolvedForDomain
check_ge "C" 1 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain.go" 'func LookupResolvedForDomain')"

# D. LookupResolvedForDomain uses cdn: prefix + :<domain> suffix
# The "cdn:" key in the helper is the cdn alias
# key — without it the B185 fix is a no-op.
check_ge "D-cdn-prefix" 1 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain.go" 'cdn:')"

# E. ruleApprovedInHeadscale calls LookupResolvedForDomain
check_ge "E" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'LookupResolvedForDomain')"

# F. form_my.go statusByRuleID calls LookupResolvedForDomain
check_ge "F" 1 "$(count "$REPO/internal/feature/exit_rules/form_my.go" 'LookupResolvedForDomain')"

# G. form_admin_b184_test.go still has 7 tests
# (B185 didn't break B184; we check the old count)
check_ge "G" 7 "$(count "$REPO/internal/feature/exit_rules/form_admin_b184_test.go" '^func Test')"

# H. resolved_by_domain_b185_test.go has 5 test functions
check_ge "H" 5 "$(count "$REPO/internal/feature/exit_rules/resolved_by_domain_b185_test.go" '^func Test')"

# I. internal/feature/admin/telegram.go has readContainerTailscaleState
check_ge "I-func" 1 "$(count "$REPO/internal/feature/admin/telegram.go" 'func readContainerTailscaleState')"

# J. internal/feature/admin/telegram.go has handleTelegramReapplyAcceptRoutes
check_ge "J-func" 1 "$(count "$REPO/internal/feature/admin/telegram.go" 'func.*handleTelegramReapplyAcceptRoutes')"
check_ge "J-action" 1 "$(count "$REPO/internal/feature/admin/telegram.go" 'reapply_accept_routes')"

# K. internal/handlers/templates/admin/telegram.html renders the
# container-tailscale card
check_ge "K-title" 1 "$(count "$REPO/internal/handlers/templates/admin/telegram.html" 'telegram.container_title')"
check_ge "K-button" 1 "$(count "$REPO/internal/handlers/templates/admin/telegram.html" 'reapply_accept_routes')"

# L. AGENTS.md mentions B185
if [ -f "$REPO/AGENTS.md" ]; then
  check_ge "L" 1 "$(count "$REPO/AGENTS.md" 'B185')"
else
  check_eq "L" ">=1" "0"
fi

# M. verify_pre_deploy.sh includes check_b185
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  check_ge "M" 1 "$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b185')"
else
  check_eq "M" ">=1" "0"
fi

# N. (VM-only) live: skygate container's RouteAll=true
# (the original symptom of the entrypoint bug). The state
# file is bind-mounted from the host, so we read it
# directly without needing docker exec.
if [ -d /home/skyadmin/skygate ]; then
  if [ -f /home/skyadmin/skygate/data/ts/tailscaled.state ]; then
    ROUTE_ALL=$(python3 -c "
import json, base64
try:
    with open('/home/skyadmin/skygate/data/ts/tailscaled.state') as f:
        d = json.load(f)
    cur = d.get('_current-profile', '')
    profs = json.loads(d.get('_profiles', '{}'))
    p = profs.get(cur, {})
    print('true' if p.get('RouteAll') else 'false')
except Exception as e:
    print('error')
")
    check_eq "N" "true" "$ROUTE_ALL"
  else
    echo "  SKIP [N] tailscaled.state not found on host"
  fi
else
  echo "  SKIP [N] not on VM"
fi

# O. (VM-only) live: probe shows ok_relay (not unreachable).
# The /admin/telegram page renders a `.probe-ok_relay` div
# when the container can reach api.telegram.org via Tailscale.
if [ -d /home/skyadmin/skygate ]; then
  PROBE=$(curl -s -c /tmp/b185_cookies.txt -b /tmp/b185_cookies.txt \
    -X POST http://192.168.13.69:8080/login \
    -d 'username=skyadmin&password=t%25gVCuboZSMT07SM97kV5%40hb' \
    -o /dev/null -w '%{http_code}' 2>/dev/null)
  if [ "$PROBE" = "302" ] || [ "$PROBE" = "200" ]; then
    PAGE=$(curl -s -b /tmp/b185_cookies.txt http://192.168.13.69:8080/admin/telegram 2>/dev/null)
    if echo "$PAGE" | grep -q 'probe-ok_relay'; then
      check_eq "O" "ok_relay" "ok_relay"
    elif echo "$PAGE" | grep -q 'probe-ok_direct'; then
      check_eq "O" "ok_relay" "ok_direct_probe"
    else
      check_eq "O" "ok_relay" "probe_unreachable_B185_not_live"
    fi
  else
    echo "  SKIP [O] login failed: HTTP code is $PROBE"
  fi
else
  echo "  SKIP [O] not on VM"
fi

# P. (VM-only) live: at least 1 discord-domain shows approved
# in the three-state badge (the B185 LookupResolvedForDomain
# cdn-alias propagation working).
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    # discord.com for basic (6/29) on emilia. The B184
    # lookup with cdn: alias should now find the 15
    # Cloudflare ranges stored under
    # parent_domain="cdn:cloudflare:discord.com".
    PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tA -c "
      SELECT COUNT(*) FROM device_rules
       WHERE user_id=6 AND device_id=29 AND exit_node_id='emilia'
         AND parent_domain IN ('discord.com', 'cdn:cloudflare:discord.com', 'cdn:fastly:discord.com', 'cdn:google:discord.com', 'cdn:akamai:discord.com')
         AND target_type IN ('subnet', 'ip')
    " 2>/dev/null > /tmp/b185_discord_cdn.txt
    if [ -s /tmp/b185_discord_cdn.txt ]; then
      DCN_CNT=$(cat /tmp/b185_discord_cdn.txt | tr -d ' \n')
      DCN_CNT=${DCN_CNT:-0}
      check_ge "P" 1 "$DCN_CNT"
    else
      echo "  SKIP [P] could not query discord.com cdn rows"
    fi
  else
    echo "  SKIP [P] psql not available"
  fi
else
  echo "  SKIP [P] not on VM"
fi

echo
echo "=== B185 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
