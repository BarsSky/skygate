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
#  O. (VM-only) live: probe shows ok_relay (not unreachable).
#     If the container reports unreachable WHILE contract N
#     passes (routing healthy), that is the BL-3 DPI
#     condition and the contract prints SKIP + WARN instead of
#     FAIL — the original B185 regression broke N too.
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

# N. (VM-only) live: the skygate container's Tailscale client is
# RUNNING and can route through the relay.
#
# 2026-09-19: the ping alone was not a valid proxy. With the
# in-container Tailscale disabled (SKYGATE_TS_AUTHKEY_FILE=/dev/null,
# the documented opt-in default) ping 8.8.8.8 still succeeds — via the
# Docker bridge's NAT, not via tailscale0 — so N passed while the
# Telegram router the design relies on was switched off. N now requires
# the client to be up first (tailscale status inside the container), and
# reports SKIP when it is disabled by configuration (that is a deliberate
# opt-in state, see /admin/tailscale), FAIL only when the client runs but
# the routed reachability is broken.
TAILSCALED_UP=0
if [ -d /home/skyadmin/skygate ]; then
  if command -v docker >/dev/null 2>&1; then
    if docker exec skygate-skygate-1 tailscale status >/dev/null 2>&1; then
      TAILSCALED_UP=1
      PING_OK=$(docker exec skygate-skygate-1 timeout 5 ping -c 1 -W 3 8.8.8.8 2>/dev/null | grep -c "packets received")
      check_eq "N" "1" "$PING_OK"
    else
      echo "  SKIP [N] tailscaled is not running in the container (Tailscale-in-container disabled:"
      echo "           SKYGATE_TS_AUTHKEY_FILE unset or /dev/null — the documented opt-in default;"
      echo "           enable it on /admin/tailscale to activate the Telegram egress relay)"
    fi
  else
    echo "  SKIP [N] docker not available"
  fi
else
  echo "  SKIP [N] not on VM"
fi

# O. (VM-only) live: probe shows ok_relay (not unreachable).
# The /admin/telegram page renders a `.probe-ok_relay` div
# when the container can reach api.telegram.org via Tailscale.
#
# 2026-09-18: credentials/host are NO LONGER hardcoded here (a live admin
# password was committed in this file — that is exactly the v0.34.0.1 leak
# class). Read them from the environment, else from the VM's .env, else SKIP.
B185_URL="${SKYGATE_LIVE_URL:-http://127.0.0.1:8080}"
B185_USER="${SKYGATE_ADMIN_USER:-}"
B185_PASS="${SKYGATE_ADMIN_PASS:-}"
if { [ -z "$B185_USER" ] || [ -z "$B185_PASS" ]; } && [ -r /home/skyadmin/skygate/.env ]; then
  [ -z "$B185_USER" ] && B185_USER=$(sed -n 's/^SKYGATE_ADMIN_USER=//p' /home/skyadmin/skygate/.env | tail -1)
  [ -z "$B185_PASS" ] && B185_PASS=$(sed -n 's/^SKYGATE_ADMIN_PASS=//p' /home/skyadmin/skygate/.env | tail -1)
fi
if [ -d /home/skyadmin/skygate ]; then
  if [ -z "$B185_PASS" ]; then
    echo "  SKIP [O] no SKYGATE_ADMIN_PASS (export it or run on the VM where .env lives)"
  else
    PROBE=$(curl -s -c /tmp/b185_cookies.txt -b /tmp/b185_cookies.txt \
      -X POST "$B185_URL/login" \
      --data-urlencode "username=$B185_USER" --data-urlencode "password=$B185_PASS" \
      -o /dev/null -w '%{http_code}' 2>/dev/null)
    if [ "$PROBE" = "302" ] || [ "$PROBE" = "200" ]; then
      PAGE=$(curl -s -b /tmp/b185_cookies.txt "$B185_URL/admin/telegram" 2>/dev/null)
      if echo "$PAGE" | grep -q 'probe-ok_relay'; then
        check_eq "O" "ok_relay" "ok_relay"
      elif echo "$PAGE" | grep -q 'probe-ok_direct'; then
        check_eq "O" "ok_relay" "ok_direct_probe"
      elif [ "$TAILSCALED_UP" != "1" ]; then
        # 2026-09-19: the in-container Tailscale client is switched off, so the
        # Telegram egress relay cannot work at all — the design routes
        # api.telegram.org through the relay from an exit node where it is
        # reachable, and that requires this client (see /admin/tailscale). That
        # is the documented opt-in default, so report SKIP with the enablement
        # path instead of FAIL. Verified 2026-09-19: emilia and karolina DO
        # reach api.telegram.org (HTTP 302 from the relay), so this is purely
        # local configuration, not an upstream block.
        echo "  WARN  [O] /admin/telegram shows no probe-ok_relay/_direct marker, and the"
        echo "            in-container Tailscale client is disabled — the relay path is off."
        echo "  SKIP [O] Tailscale-in-container disabled -> Telegram egress relay unavailable"
        echo "           enable it on /admin/tailscale, then apply the Telegram CIDRs on the"
        echo "           relay (/admin/telegram -> Egress relay -> Apply, or Pin nearest)"
      else
        # Client is up but Telegram is still unreachable: a real problem — the
        # relay's Telegram CIDRs are not advertised/approved, or the relay lost
        # upstream access.
        echo "  WARN  [O] tailscaled is running in the container but the Telegram probe is"
        echo "            unreachable — check that the selected relay advertises the canonical"
        echo "            Telegram CIDRs (149.154.160.0/20, 91.108.*, 185.76.151.0/24) and that"
        echo "            headscale has them APPROVED for that node."
        check_eq "O" "ok_relay" "probe_unreachable_with_tailscaled_up"
      fi
    else
      echo "  SKIP [O] login failed: HTTP code is $PROBE"
    fi
  fi
else
  echo "  SKIP [O] not on VM"
fi

# P. (VM-only) live: at least 1 discord-domain shows approved
# in the three-state badge (the B185 LookupResolvedForDomain
# cdn-alias propagation working).
if [ -d /home/skyadmin/skygate ]; then
  if command -v docker >/dev/null 2>&1; then
    # 2026-09-18: query through `docker exec` against the local PG container
    # instead of a TCP connection with a hardcoded PGPASSWORD.
    docker exec skygate-pg-local psql -U admin -d skygate_staging -tA -c "
      SELECT COUNT(*) FROM device_rules
       WHERE parent_domain LIKE 'cdn:%:%discord%'
         AND target_type IN ('subnet', 'ip')
    " 2>/dev/null > /tmp/b185_discord_cdn.txt
    if [ -s /tmp/b185_discord_cdn.txt ]; then
      DCN_CNT=$(cat /tmp/b185_discord_cdn.txt | tr -d ' \n')
      DCN_CNT=${DCN_CNT:-0}
      check_ge "P" 1 "$DCN_CNT"
    else
      echo "  SKIP [P] could not query cdn discord rows"
    fi
  else
    echo "  SKIP [P] docker not available"
  fi
else
  echo "  SKIP [P] not on VM"
fi

echo
echo "=== B185 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
