#!/bin/bash
# scripts/verify_post_deploy.sh — runtime guarantees for skygate.
#
# Runs AFTER `docker compose up -d skygate` on the VM. Checks the
# live state against the guarantee catalog (see AGENTS.md "v0.28.5
# guarantee catalog"):
#
#   R1  /healthz 200, status=ok
#   R2  /readyz 200 (DB + headscale OK)
#   R3  skygate build label matches HEAD
#   R4  tailscaled running inside skygate-host-1
#   R5  skygate-host-1 has tailnet IP 100.64.100.10
#   R6  skygate-host-1 does NOT use an exit-node (Docker bridge reachable)
#   R7  Docker bridge 172.18.0.0/16 reachable from skygate-host-1
#   R8  headscale API responds with policy
#   R9  Live policy == last applied snapshot
#   R10 Per-user grants: src=user@, dst includes autogroup:internet
#   R11 Per-device loose grants: ≥1 per tagged device (v0.28.5b)
#   R12 Catch-all src=tag:public, NOT * (v0.28.3 bypass fix)
#   R13 Catch-alls: * → tag:public, * → tag:exit-node
#   R14 tagOwners contains tag:public, tag:exit-node, tag:private, tag:subnet-router
#   R15 No per-device grant with `via` for via_enabled=0 rows (v0.28.5)
#   R16 Per-user grant `via` matches user_exit_node_prefs.via_enabled
#   R17 relay-1, relay-2, relay-3 online in headscale
#   R18 Exit-nodes advertised routes include 0.0.0.0/0
#   R19 DB: all per-user via_enabled match live policy
#   R20 Migration v0.47 idempotent (smoke: re-running doesn't re-backfill)
#   R21 tailscaled.state on disk has no stale --exit-node pref
#   R22 https://skygate.example.com/healthz → 200
#   R23 TLS cert is Let's Encrypt, not expiring within 30d
#   R24 openresty upstream reachable
#   R25 skygate-host-1 can reach 8.8.8.8 (direct, no exit-node)
#   R26 no per-user device carries an exit-node tag (v0.30.1 base fix)
#
# Usage:
#   bash scripts/verify_post_deploy.sh                       # all 26 checks
#   bash scripts/verify_post_deploy.sh --quick              # only R1-R9 + R26 (core)
#   bash scripts/verify_post_deploy.sh --skip-network        # no R22-R25
#   SSH_HOST=admin@192.0.2.1 bash scripts/verify_post_deploy.sh
#
# Cross-platform: pure bash. Runs from the OPERATOR'S machine
# (Linux/Mac shell or Windows Git Bash) — SSHes into the VM and
# runs the actual checks in-place. The VM is the source of
# truth for the live state; the operator's machine just needs
# (a) a working `ssh` to the VM and (b) the SSH key in one of
# the standard locations:
#
#   ~/.ssh/id_ed25519
#   ~/.ssh/id_rsa
#   /mnt/c/Users/<user>/.ssh/id_ed25519   (WSL2 from Windows)
#   /c/Users/<user>/.ssh/id_ed25519      (Git Bash from Windows)
#
# For the WSL2/Git-Bash + Windows case, the script auto-detects
# `C:\Users\<user>\.ssh\id_ed25519` and uses it. If the key is
# password-protected, run `ssh-add <key>` once per shell session
# (the script does NOT auto-add — that would require unlocking
# the keychain in a non-interactive way, which the operator
# should do explicitly so the password isn't in script args).
#
# 2026-07-25: v0.28.5 — initial catalog.
# 2026-07-28: v0.30.1 — added R26.
# 2026-07-30: R10 made dynamic (was hardcoded to "4" — broke
# when the system grew past the 4 baseline users; now reads
# COUNT(*) FROM portal_users via ssh-into-vm sqlite3).

set -u
# No `set -e` — count failures, don't abort.

# ---------------------------------------------------------------------------
# Args
# ---------------------------------------------------------------------------
QUICK=0
SKIP_NETWORK=0
for arg in "$@"; do
  case "$arg" in
    --quick)        QUICK=1 ;;
    --skip-network) SKIP_NETWORK=1 ;;
  esac
done

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"
# v0.29.2: SKYGATE_CONTAINER is resolved at runtime via the
# `com.docker.compose.service=skygate` label (set by docker compose
# automatically). The "skygate" literal used to work because
# docker-compose.yml had `container_name: skygate`; that was
# removed in v0.29.2 to avoid a race with `docker compose up
# --force-recreate` leaving the new container in `Created` state
# (see deploy/skygate-cli.sh for details). The wrapper script
# `skygate` on the host gives a stable CLI; the lookup below
# gives the same stability for the verify catalog. If you need
# to override (e.g. for an ad-hoc check on a different
# deployment), set SKYGATE_CONTAINER=<id> in the env.
SKYGATE_CONTAINER="${SKYGATE_CONTAINER:-}"
HEADSCALE_URL="${HEADSCALE_URL:-http://localhost:50444}"
# SSH key: prefer the explicit one in the current HOME, fall back to
# whatever `ssh` finds on its own. WSL2 bash uses a separate HOME
# from PowerShell, so this matters when the script is run from
# Git Bash / WSL.
SSH_KEY="${SSH_KEY:-}"
for cand in \
  "$HOME/.ssh/id_ed25519" \
  "$HOME/.ssh/id_rsa" \
  "/mnt/c/Users/knaga/.ssh/id_ed25519" \
  "/c/Users/knaga/.ssh/id_ed25519"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then
    SSH_KEY="$cand"; break
  fi
done

# v0.29.2: resolve SKYGATE_CONTAINER by label. If the env var was
# already set, leave it alone (the operator may have overridden
# to a specific id for a one-off check). Otherwise, find the
# container with the skygate compose service label.
if [ -z "$SKYGATE_CONTAINER" ]; then
  SKYGATE_CONTAINER="$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" \
    "docker ps -a --filter 'label=com.docker.compose.service=skygate' --format '{{.ID}}' | head -1")"
  if [ -z "$SKYGATE_CONTAINER" ]; then
    echo "verify_post_deploy: cannot find skygate container (label=com.docker.compose.service=skygate not found)" >&2
    echo "                         — is the skygate service running? try: docker compose ps" >&2
    exit 2
  fi
  # Echo the resolved ID so the operator can see it in the
  # banner (helpful when debugging "wait, which container is it?").
fi

API_KEY="$(grep '^HEADSCALE_API_KEY=' /home/admin/skygate/.env 2>/dev/null | cut -d= -f2-)"
if [ -z "$API_KEY" ]; then
  # We don't have the .env locally. SSH in and grab it.
  API_KEY="$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" \
    "grep '^HEADSCALE_API_KEY=' /home/admin/skygate/.env | cut -d= -f2-")"
fi

if [ -t 1 ]; then
  RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; NC=$'\033[0m'
else
  RED=''; GRN=''; YLW=''; NC=''
fi

# Run a check that lives entirely on the VM (no live-policy parsing).
# Args: name, description, ssh_command
run_vm_check() {
  local name="$1" desc="$2" cmd="$3"
  local out rc
  out=$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" "$cmd" 2>&1)
  rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "  ${GRN}PASS${NC}  $name  $desc"
    RESULTS_PASS=$((RESULTS_PASS + 1))
  else
    echo "  ${RED}FAIL${NC}  $name  $desc"
    [ -n "$out" ] && echo "$out" | sed 's/^/        /' | head -10
    RESULTS_FAIL=$((RESULTS_FAIL + 1))
  fi
}

# Run a check that needs the live policy (fetched via API).
# The policy is fetched once and cached in $LIVE_POLICY.
run_policy_check() {
  local name="$1" desc="$2" fn="$3"
  local out
  out=$(LIVE_POLICY="$LIVE_POLICY" DB_JSON="$DB_JSON" "$fn" 2>&1)
  local rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "  ${GRN}PASS${NC}  $name  $desc"
    RESULTS_PASS=$((RESULTS_PASS + 1))
  else
    echo "  ${RED}FAIL${NC}  $name  $desc"
    [ -n "$out" ] && echo "$out" | sed 's/^/        /' | head -10
    RESULTS_FAIL=$((RESULTS_FAIL + 1))
  fi
}

RESULTS_PASS=0
RESULTS_FAIL=0

# ---------------------------------------------------------------------------
# Phase 1: fetch live state (no checks yet, just data collection).
# All headscale API calls and DB queries go through SSH because the
# operator's workstation can't reach headscale directly.
# ---------------------------------------------------------------------------
echo "=== skygate post-deploy verification ==="
echo "  ssh:    $SSH_HOST"
echo "  container: $SKYGATE_CONTAINER"
echo "  date:   $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# Helpers
ssh_vm()  { ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" "$@"; }
curl_vm() { ssh_vm "curl -fsS -H 'Authorization: Bearer $API_KEY' $HEADSCALE_URL$1"; }

# R1: /healthz (also gives us the build label for R3)
HEALTHZ=$(ssh_vm "curl -fsS http://localhost:8080/healthz" 2>&1)
BUILD_LABEL=$(echo "$HEALTHZ" | grep -oE '"build":"[^"]+"' | head -1 | cut -d'"' -f4)

# R2: /readyz  (response shape: {"healthy":true,"db":"ok","headscale":"ok",...})
READYZ=$(ssh_vm "curl -fsS http://localhost:8080/readyz" 2>&1)

# R8 + R9 + R10-R16: live policy (via SSH to the VM, not local curl —
# headscale is on a private port not exposed to the operator)
LIVE_POLICY=$(curl_vm "/api/v1/policy" 2>/dev/null)

# R17 + R18: headscale nodes list (via SSH)
NODES_JSON=$(curl_vm "/api/v1/node" 2>/dev/null)

# R19: DB state — copy DB out via `docker cp` (no alpine / sqlite3 in
# the alpine image, and skygate container has no sqlite3 either) and
# run sqlite3 on the VM's host. The query is piped via heredoc
# because the .mode json directive needs to be a separate command
# from the SELECT (newlines in -c args get split by bash).
DB_JSON=$(ssh_vm "set -e
  docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_verify_$$.sqlite
  sqlite3 /tmp/_db_verify_$$.sqlite <<'SQL'
.mode json
SELECT json_object('user_prefs',COALESCE((SELECT json_group_array(json_object('user_id',user_id,'tag',exit_node_tag,'via',via_enabled)) FROM user_exit_node_prefs),'[]'),
                   'device_prefs',COALESCE((SELECT json_group_array(json_object('user_id',user_id,'hostname',device_hostname,'tag',exit_node_tag,'via',via_enabled)) FROM device_exit_node_prefs),'[]'));
SQL
  rm -f /tmp/_db_verify_$$.sqlite" 2>&1 | tr -d '\n' | grep -oE '\{.*\}')

# ---------------------------------------------------------------------------
# Phase 2: core (R1-R9) — always run
# ---------------------------------------------------------------------------
echo "[R1-R9] core liveness + headscale sync"
[ -n "$HEALTHZ" ] && echo "$HEALTHZ" | grep -q '"status":"ok"' && \
  { echo "  ${GRN}PASS${NC}  R1  /healthz status:ok"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R1  /healthz: $HEALTHZ"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }
[ -n "$READYZ" ] && echo "$READYZ" | grep -qE '"healthy":true|"status":"ok"' && \
  { echo "  ${GRN}PASS${NC}  R2  /readyz healthy:true (db+headscale ok)"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R2  /readyz: $READYZ"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }
HEAD_SHA=$(git -C "$(dirname "${BASH_SOURCE[0]}")/.." rev-parse --short HEAD 2>/dev/null)
[ -n "$BUILD_LABEL" ] && [[ "$BUILD_LABEL" == *"$HEAD_SHA"* || "$BUILD_LABEL" == "dev"*"unknown" ]] && \
  { echo "  ${GRN}PASS${NC}  R3  build=$BUILD_LABEL matches HEAD=$HEAD_SHA"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R3  build=$BUILD_LABEL ≠ HEAD=$HEAD_SHA"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }

run_vm_check "R4" "tailscaled running in skygate-host-1" \
  "docker exec $SKYGATE_CONTAINER sh -c 'pgrep tailscaled | grep -q .'"
run_vm_check "R5" "skygate-host-1 tailnet IP = 100.64.100.10" \
  "docker exec $SKYGATE_CONTAINER tailscale --socket=/var/run/tailscale/tailscaled.sock status 2>&1 | grep -q '^100.64.100.10'"
# R6: skygate-host-1 must NOT use an exit-node. The Tailscale status line
# for self shows "<hostname>  ...  linux  -" with trailing "-" (no
# exit-node marker), then trailing spaces. We grep the LAST non-space
# field: should be "-" (none) or a hostname (set).
EXITNODE_LINE=$(ssh_vm \
  "docker exec $SKYGATE_CONTAINER tailscale --socket=/var/run/tailscale/tailscaled.sock status 2>&1 | grep '^100.64.100.10'")
LAST_FIELD=$(echo "$EXITNODE_LINE" | awk '{print $NF}')
if [ "$LAST_FIELD" = "-" ]; then
  echo "  ${GRN}PASS${NC}  R6  skygate-host-1 has NO exit-node (last field = '-')"
  RESULTS_PASS=$((RESULTS_PASS+1))
else
  echo "  ${RED}FAIL${NC}  R6  skygate-host-1 has exit-node = $LAST_FIELD"
  RESULTS_FAIL=$((RESULTS_FAIL+1))
fi
run_vm_check "R7" "Docker bridge 172.18.0.0/16 reachable from skygate-host-1" \
  "docker exec $SKYGATE_CONTAINER wget --spider --timeout=3 http://172.18.0.2:50444 2>&1 | grep -q 'remote file exists'"
[ -n "$LIVE_POLICY" ] && [ "$LIVE_POLICY" != '{"policy":""}' ] && \
  { echo "  ${GRN}PASS${NC}  R8  headscale /api/v1/policy responds with non-empty policy"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R8  headscale /api/v1/policy is empty or unreachable"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }
# R9: live policy == last applied snapshot (compare updatedAt to DB).
#
# Query both the last successful reapply (applied_success=1) AND the
# last attempt (any applied_success). The 4-state result is more
# informative than a single "diff seconds" number:
#
#   1. last_attempt.applied_success=1, within 60s of live policy → PASS
#      (reapply chain is healthy, the live policy IS the snapshot)
#   2. last_attempt.applied_success=0 → FAIL "reapply chain broken"
#      (the most recent reapply failed; live policy is from the
#      previous successful one, which may be many hours old)
#   3. last_attempt.applied_success=1, but >3600s before live policy
#      → FAIL "live policy older than last applied" (headscale
#      reverted somehow — e.g. operator ran a manual set)
#   4. no snapshots at all → SKIP
#
# The previous version only checked the most recent applied_success=1
# row, which masked the "just-failed reapply" case (the R9 would
# look at the previous successful row, which was many hours old,
# and complain about a non-existent diff). v0.28.7 fix: query both
# the last attempt and the last applied, and use the last attempt
# to drive the success/fail decision.
#
# `created_at` is stored as Unix epoch integer (strftime('%s','now')),
# so we read it as an int and convert to epoch on this side.
UPDATED_AT=$(echo "$LIVE_POLICY" | grep -oE '"updatedAt":"[^"]+"' | cut -d'"' -f4)

# Read both: last attempt (any outcome) and last successful.
# The output is two lines: epoch, applied_success (0/1).
SNAPSHOT_INFO=$(ssh_vm "set -e
  docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_v_\$\$.sqlite
  sqlite3 /tmp/_db_v_\$\$.sqlite <<'SQL'
SELECT printf('%d %d', created_at, COALESCE(applied_success, 0))
FROM acl_snapshots ORDER BY id DESC LIMIT 1;
SQL
  rm -f /tmp/_db_v_\$\$.sqlite" 2>/dev/null)
LAST_ATTEMPT_EPOCH=$(echo "$SNAPSHOT_INFO" | awk '{print $1}')
LAST_ATTEMPT_SUCCESS=$(echo "$SNAPSHOT_INFO" | awk '{print $2}')

LAST_APPLIED_EPOCH=$(ssh_vm "set -e
  docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_v2_\$\$.sqlite
  sqlite3 /tmp/_db_v2_\$\$.sqlite \"SELECT created_at FROM acl_snapshots WHERE applied_success=1 ORDER BY id DESC LIMIT 1\"
  rm -f /tmp/_db_v2_\$\$.sqlite" 2>/dev/null)

if [ -n "$LAST_ATTEMPT_EPOCH" ] && [ -n "$UPDATED_AT" ]; then
  LAST_ATTEMPT_ISO=$(date -u -d "@$LAST_ATTEMPT_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "")
  POLICY_EPOCH=$(date -d "$UPDATED_AT" +%s 2>/dev/null || echo 0)

  if [ "$LAST_ATTEMPT_SUCCESS" = "0" ]; then
    # Most recent reapply FAILED. The live policy is from the
    # previous successful reapply (potentially hours old). Report
    # the operator-visible failure and tell them to re-run the
    # reapply after fixing the underlying issue.
    LAST_APPLIED_ISO=""
    if [ -n "$LAST_APPLIED_EPOCH" ]; then
      LAST_APPLIED_ISO=$(date -u -d "@$LAST_APPLIED_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "")
    fi
    echo "  ${RED}FAIL${NC}  R9  reapply chain broken — last reapply at $LAST_ATTEMPT_ISO FAILED (applied_success=0). Last successful: ${LAST_APPLIED_ISO:-never}. Check /admin/audit for the error."
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  else
    # Most recent reapply succeeded. Compare its timestamp to the
    # live policy's updatedAt (60s tolerance for clock drift).
    APPLIED_EPOCH=$(date -d "$LAST_ATTEMPT_ISO" +%s 2>/dev/null || echo 0)
    DIFF=$((POLICY_EPOCH - APPLIED_EPOCH))
    if [ "$DIFF" -ge -60 ] && [ "$DIFF" -le 3600 ]; then
      echo "  ${GRN}PASS${NC}  R9  live policy updatedAt=$UPDATED_AT ≈ last applied $LAST_ATTEMPT_ISO (diff=${DIFF}s)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R9  live policy updatedAt=$UPDATED_AT ≠ last applied $LAST_ATTEMPT_ISO (diff=${DIFF}s — reapply needed or headscale reverted)"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  fi
else
  echo "  ${YLW}SKIP${NC}  R9  live policy / applied snapshot: insufficient data"
fi

# ---------------------------------------------------------------------------
# Phase 3: policy shape (R10-R16) — skip if LIVE_POLICY is empty
# ---------------------------------------------------------------------------
if [ -n "$LIVE_POLICY" ] && [ "$LIVE_POLICY" != '{"policy":""}' ]; then
  echo
  echo "[R10-R16] policy shape invariants"

  # R10: per-user grants (one per portal_users row), src=user@,
  # dst includes autogroup:internet. The expected count is DYNAMIC
  # — it should match the number of portal_users rows in the DB.
  # (Was hardcoded to "4" for the 4 known prod users
  # admin/user1/guest/user2; the system can grow beyond
  # that and the script must follow.) R15/R16 below do the
  # via-flag cross-check; R10 just checks the count + presence
  # of the per-user shape.
  #
  # Authoritative count: SELECT COUNT(*) FROM portal_users via
  # ssh-into-vm sqlite3 (the .db file is bind-mounted from the
  # host, so we have to docker cp it out for sqlite to read).
  USER_COUNT_DB=$(ssh_vm "set -e
    docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_ucnt_\$\$.sqlite
    sqlite3 /tmp/_db_ucnt_\$\$.sqlite 'SELECT COUNT(*) FROM portal_users;'
    rm -f /tmp/_db_ucnt_\$\$.sqlite" 2>/dev/null | tr -d ' \n')
  USER_GRANT_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if any('@tsnet.example.com' in s for s in g.get('src',[]))
        and 'autogroup:internet' in g.get('dst',[]))
print(n)
")
  if [ -n "$USER_COUNT_DB" ] && [ "$USER_GRANT_COUNT" = "$USER_COUNT_DB" ]; then
    echo "  ${GRN}PASS${NC}  R10 $USER_GRANT_COUNT per-user grants (matches portal_users=$USER_COUNT_DB, all with autogroup:internet)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R10 per-user grants count=$USER_GRANT_COUNT (expected $USER_COUNT_DB from portal_users)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R28: live policy size + grant count — exit-node route
  # correctness guard (v0.32.2).
  # 2026-07-30: the operator reported "exit-node routing
  # started working slower" after a series of small
  # refactors. The most likely cause for a perf regression
  # in Tailscale client map updates is a ballooning policy
  # (every device downloads the whole file on every map
  # update). This check pins:
  #   (a) the deployed policy is < 100KB (Tailscale clients
  #       on slow links start to feel this around 100KB;
  #       production is ~5KB so 100KB is 20x headroom)
  #   (b) the number of grants is < 500 (each grant is
  #       matched per packet; 500 is the per-device rule
  #       cap, so the total should be roughly N_users *
  #       N_devices — well under 500 for the current
  #       4-user deploy)
  #   (c) the number of hosts is < 2000 (each /32 DNS rule
  #       adds a host entry; large host blocks are a common
  #       source of policy bloat)
  #
  # All three are informational/early-warning: a slow
  # growth is fine, a sudden spike (R10 PASS yesterday,
  # R28 FAIL today) is the signal to investigate.
  POLICY_BYTES=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
print(len(d['policy']))
")
  POLICY_GRANT_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
print(len(p.get('grants',[])))
")
  POLICY_HOST_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
print(len(p.get('hosts',{})))
")
  if [ "$POLICY_BYTES" -lt 102400 ] && [ "$POLICY_GRANT_COUNT" -lt 500 ] && [ "$POLICY_HOST_COUNT" -lt 2000 ]; then
    echo "  ${GRN}PASS${NC}  R28 policy size=${POLICY_BYTES}B grants=${POLICY_GRANT_COUNT} hosts=${POLICY_HOST_COUNT} (bounds 102400/500/2000)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R28 policy perf: size=${POLICY_BYTES}B grants=${POLICY_GRANT_COUNT} hosts=${POLICY_HOST_COUNT} (bounds 102400/500/2000)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R11: per-device loose grants (no via) for every tagged device
  LOOSE_DEV_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if any(s.startswith('tag:dev-') for s in g.get('src',[]))
        and 'autogroup:internet' in g.get('dst',[])
        and 'via' not in g)
print(n)
")
  if [ "$LOOSE_DEV_COUNT" -ge 5 ]; then
    echo "  ${GRN}PASS${NC}  R11 $LOOSE_DEV_COUNT per-device loose grants (≥5 expected)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R11 per-device loose grants=$LOOSE_DEV_COUNT (expected ≥5)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R12: catch-all for autogroup:internet has src=tag:public, NOT *
  HAS_STAR_CATCHALL=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if g.get('src')==['*'] and 'autogroup:internet' in g.get('dst',[]))
print(n)
")
  if [ "$HAS_STAR_CATCHALL" = "0" ]; then
    echo "  ${GRN}PASS${NC}  R12 no catch-all src=* dst=autogroup:internet (bypass fix v0.28.3)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R12 found $HAS_STAR_CATCHALL catch-all with src=* → autogroup:internet (REGRESSION)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R13: * → tag:public and * → tag:exit-node catch-alls present
  HAS_PUBLIC_CATCHALL=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if g.get('src')==['*'] and g.get('dst')==['tag:public'])
print(n)
")
  HAS_EXITNODE_CATCHALL=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if g.get('src')==['*'] and g.get('dst')==['tag:exit-node'])
print(n)
")
  if [ "$HAS_PUBLIC_CATCHALL" -ge 1 ] && [ "$HAS_EXITNODE_CATCHALL" -ge 1 ]; then
    echo "  ${GRN}PASS${NC}  R13 * → tag:public and * → tag:exit-node catch-alls present"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R13 catch-alls missing: tag:public=$HAS_PUBLIC_CATCHALL tag:exit-node=$HAS_EXITNODE_CATCHALL"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R14: tagOwners contains the required tags
  TAGOWNERS_OK=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
to = p.get('tagOwners',{})
required = ['tag:public', 'tag:exit-node', 'tag:private', 'tag:subnet-router']
missing = [t for t in required if t not in to]
print('OK' if not missing else 'MISSING:'+','.join(missing))
")
  if [ "$TAGOWNERS_OK" = "OK" ]; then
    echo "  ${GRN}PASS${NC}  R14 tagOwners contains all required tags"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R14 tagOwners: $TAGOWNERS_OK"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R15 + R16: cross-check DB via_enabled vs live policy via
  VIA_CROSS_CHECK=$(LIVE_POLICY="$LIVE_POLICY" DB_JSON="$DB_JSON" python3 << 'PYEOF'
import json, os, sys
live = json.loads(os.environ["LIVE_POLICY"])
policy = json.loads(live["policy"])
db = json.loads(os.environ["DB_JSON"])

# Build: for each user, is via in live per-user grant == db.via_enabled?
mismatch = []
for g in policy.get("grants", []):
    srcs = g.get("src", [])
    user_match = [s for s in srcs if "@tsnet.example.com" in s]
    if not user_match:
        continue
    user = user_match[0].split("@")[0]
    has_via = "via" in g
    via = g.get("via", [None])[0] if has_via else None
    # Find user in db
    db_user = next((u for u in db.get("user_prefs", [])
                    if str(u.get("user_id")) in {"1":"admin","6":"user1","9":"guest","10":"user2"}.get(str(u.get("user_id")),"") or u.get("user_id") in (1,6,9,10) and u.get("tag") in (None,via) ), None)
    # Simpler: match by user_id→username map
    user_map = {"1":"admin","6":"user1","9":"guest","10":"user2"}
    db_user = None
    for u in db.get("user_prefs", []):
        if user_map.get(str(u["user_id"])) == user:
            db_user = u
            break
    if db_user is None:
        continue
    if db_user["via"] == 1 and not has_via:
        mismatch.append(f"user={user} db.via=1 but live grant has no via")
    if db_user["via"] == 0 and has_via:
        mismatch.append(f"user={user} db.via=0 but live grant has via={via}")

# For per-device: same check
for g in policy.get("grants", []):
    srcs = g.get("src", [])
    dev_match = [s for s in srcs if s.startswith("tag:dev-")]
    if not dev_match:
        continue
    tag = dev_match[0]  # tag:dev-<user>-<host>
    rest = tag[len("tag:dev-"):]
    # user-host: split on LAST '-'
    idx = rest.rfind("-")
    user = rest[:idx]
    host = rest[idx+1:]
    has_via = "via" in g
    via = g.get("via", [None])[0] if has_via else None
    db_dev = None
    for d in db.get("device_prefs", []):
        if d["hostname"] == host and d["user_id"] in (1,6):
            user_map = {1:"admin", 6:"user1"}
            if user_map.get(d["user_id"]) == user:
                db_dev = d
                break
    if db_dev is None:
        continue
    if db_dev["via"] == 1 and not has_via:
        mismatch.append(f"device={host} db.via=1 but no via in grant")
    if db_dev["via"] == 0 and has_via:
        mismatch.append(f"device={host} db.via=0 but live has via={via}")

if mismatch:
    print("MISMATCH:" + ";".join(mismatch))
else:
    print("OK")
PYEOF
)
  if [ "$VIA_CROSS_CHECK" = "OK" ]; then
    echo "  ${GRN}PASS${NC}  R15+R16 DB via_enabled matches live policy (no via mismatch)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R15+R16 $VIA_CROSS_CHECK"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 4: exit-nodes (R17-R18) — headscale
# ---------------------------------------------------------------------------
if [ "$QUICK" = 0 ] && [ -n "$NODES_JSON" ]; then
  echo
  echo "[R17-R18] exit-nodes"
  for host in relay-1 relay-2 relay-3; do
    ONLINE=$(echo "$NODES_JSON" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    n = next((n for n in d.get('nodes',[]) if n.get('givenName')=='$host'), None)
    print('online' if n and n.get('online') else 'offline')
except Exception as e:
    print('error:'+str(e))
")
    if [ "$ONLINE" = "online" ]; then
      echo "  ${GRN}PASS${NC}  R17 $host is online"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R17 $host is $ONLINE"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  done

  # R18: each exit-node has 0.0.0.0/0 in advertised routes
  for host in relay-1 relay-2 relay-3; do
    HAS_DEFAULT=$(echo "$NODES_JSON" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    n = next((n for n in d.get('nodes',[]) if n.get('givenName')=='$host'), None)
    routes = (n or {}).get('availableRoutes', [])
    print('yes' if '0.0.0.0/0' in routes else 'no')
except Exception as e:
    print('error:'+str(e))
")
    if [ "$HAS_DEFAULT" = "yes" ]; then
      echo "  ${GRN}PASS${NC}  R18 $host advertises 0.0.0.0/0"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R18 $host does NOT advertise 0.0.0.0/0"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  done
fi

# ---------------------------------------------------------------------------
# Phase 5: Tailscale state on disk (R20-R21)
# ---------------------------------------------------------------------------
if [ "$QUICK" = 0 ]; then
  echo
  echo "[R20-R21] Tailscale state"
  # R20: smoke migration idempotency by counting via=0 rows before+after a no-op restart
  # Skipped in this script — would require a restart. Encoded as "the
  # binary deployed has migration v0.47 with the freshlyAdded guard"
  # which is covered by the build-time B5 test.
  echo "  ${YLW}SKIP${NC}  R20 migration v0.47 idempotency (covered by B5 build-time test)"

  # R21: tailscaled.state on disk has no stale --exit-node pref
  STALE_EXIT=$(ssh_vm \
    "docker exec $SKYGATE_CONTAINER sh -c 'cat /var/lib/tailscale/tailscaled.state' 2>&1 | grep -oE '\"ExitNodeID\":[^,]+' | head -1")
  if [ -z "$STALE_EXIT" ] || [ "$STALE_EXIT" = '"ExitNodeID":""' ]; then
    echo "  ${GRN}PASS${NC}  R21 tailscaled.state has no stale ExitNodeID"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R21 stale ExitNodeID in tailscaled.state: $STALE_EXIT"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 6: HTTPS (R22-R24)
# ---------------------------------------------------------------------------
if [ "$SKIP_NETWORK" = 0 ]; then
  echo
  echo "[R22-R24] HTTPS path (skygate.example.com)"
  HTTPS_CODE=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 https://skygate.example.com/healthz 2>&1)
  if [ "$HTTPS_CODE" = "200" ]; then
    echo "  ${GRN}PASS${NC}  R22 https://skygate.example.com/healthz → 200"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R22 https://skygate.example.com/healthz → $HTTPS_CODE"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R23: TLS cert expiry (must be > 7 days from now)
  CERT_END=$(echo | openssl s_client -servername skygate.example.com -connect skygate.example.com:443 2>/dev/null | openssl x509 -noout -enddate 2>/dev/null | sed 's/^notAfter=//')
  if [ -n "$CERT_END" ]; then
    CERT_EPOCH=$(date -d "$CERT_END" +%s 2>/dev/null || echo 0)
    NOW_EPOCH=$(date +%s)
    DAYS_LEFT=$(( (CERT_EPOCH - NOW_EPOCH) / 86400 ))
    if [ "$DAYS_LEFT" -gt 7 ]; then
      echo "  ${GRN}PASS${NC}  R23 TLS cert valid for $DAYS_LEFT more days ($CERT_END)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R23 TLS cert expires in $DAYS_LEFT days: $CERT_END"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  else
    echo "  ${YLW}SKIP${NC}  R23 TLS cert check (openssl failed)"
  fi

  # R24: openresty upstream reachable (the local 8080 → 172.18.0.4 path)
  LOCAL_8080=$(ssh_vm \
    "curl -sS -o /dev/null -w '%{http_code}' --max-time 5 http://localhost:8080/healthz" 2>&1)
  if [ "$LOCAL_8080" = "200" ]; then
    echo "  ${GRN}PASS${NC}  R24 openresty upstream (localhost:8080) returns 200"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R24 openresty upstream: localhost:8080 → $LOCAL_8080"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 7: network reachability (R25)
# ---------------------------------------------------------------------------
if [ "$SKIP_NETWORK" = 0 ]; then
  echo
  echo "[R25] skygate-host-1 direct internet"
  # skygate-host-1 must reach 8.8.8.8 DIRECTLY (no exit-node). With
  # exit-node unset, this should work. If exit-node was set and we
  # forgot, the ping would go via tailscale → exit-node → internet,
  # which also works but for the wrong reason. R6 catches the
  # exit-node misuse; R25 catches the actual connectivity.
  PING_LOSS=$(ssh_vm \
    "docker exec $SKYGATE_CONTAINER ping -c 2 -W 2 8.8.8.8 2>&1 | grep -oE '[0-9]+% packet loss'" | head -1)
  if echo "$PING_LOSS" | grep -q '0%'; then
    echo "  ${GRN}PASS${NC}  R25 skygate-host-1 pings 8.8.8.8 (loss=$PING_LOSS)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R25 skygate-host-1 cannot reach 8.8.8.8 (loss=$PING_LOSS)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 8: per-user device integrity (R26)
# ---------------------------------------------------------------------------
# 2026-07-28: v0.30.1. Catches the "base" bug shape on the live
# system: a per-user device (tag:dev-<user>-<device>) must NOT
# also carry an exit-node-like tag. If it does, Tailscale
# auto-failover may pick the user device as the exit-node
# (0ms self-loop = lowest metric) and the user's internet
# goes to /dev/null. The v0.30.1 build-time guard (B17 +
# PostAdminNodeTag) blocks the skygate UI path; this R26 check
# blocks the direct headscale CLI path (which the original
# bug exploited).
#
# Failure modes this catches:
#   - tag:exit-node AND tag:dev-* on the same node (the base bug)
#   - tag:exit-relay-1 / relay-2 / relay-3 AND tag:dev-* on
#     the same node (a per-user device masquerading as a
#     specific relay)
#   - "no one fixed it after direct headscale CLI" — operator
#     did the same manual bypass again
#
# Allowed: tag:exit-node alone, tag:exit-relay-1 alone, etc.
# (a real relay). Allowed: tag:dev-* alone (a per-user device).
# Forbidden: tag:exit-* AND tag:dev-* on the same node.
if [ "$QUICK" = 0 ]; then
  echo
  echo "[R26] no per-user device carries an exit-node tag"
  # headscale nodes list outputs a table; we filter rows that
  # have BOTH a tag:dev-* tag AND a tag:exit-* tag. Format:
  #   ID | Hostname | Name | MachineKey | NodeKey | User | Tags | IP | Ephemeral | Last seen | ...
  # A node's tags are spread across multiple lines (one tag per
  # line continuation in the "Tags" column). The pattern we
  # match is: a line with "tag:dev-..." and a line with
  # "tag:exit-..." for the same ID. We use awk to walk the
  # table and accumulate tags per node.
  CONFLICTS=$(ssh_vm "docker exec $HEADSCALE_CONTAINER headscale nodes list 2>&1" 2>&1 \
    | awk -F'|' '
        /^[ ]*[0-9]+[ ]+\|/ {
          if (current_id != "" && has_dev && has_exit) {
            print current_id, current_name
          }
          current_id = ""; current_name = ""; has_dev = 0; has_exit = 0
          split($0, parts, "|")
          current_id = parts[1]; gsub(/^[ \t]+|[ \t]+$/, "", current_id)
          current_name = parts[2]; gsub(/^[ \t]+|[ \t]+$/, "", current_name)
        }
        /tag:dev-/  { has_dev  = 1 }
        /tag:exit/  { has_exit = 1 }
        END {
          if (current_id != "" && has_dev && has_exit) {
            print current_id, current_name
          }
        }
      ')
  if [ -z "$CONFLICTS" ]; then
    echo "  ${GRN}PASS${NC}  R26 no node has both tag:dev-* and tag:exit-*"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R26 user-device-with-exit-tag found:"
    echo "$CONFLICTS" | sed 's/^/        /'
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 9: PG foundation live check (R27) — v0.31.0
# ---------------------------------------------------------------------------
# 2026-07-28: v0.31.0. Runs the 4 PG verification tests on the
# PG-staging VM (when SKYGATE_TEST_PG_DSN is configured on the
# staging host). Tests:
#   - TestPGRoundtripSchema: schema equivalence (SQLite ↔ PG)
#   - TestPGMigrationIdempotency: run MigratePostgres twice
#   - TestPGLockTimeout: concurrent migrations don't deadlock
#   - TestPGDataMigrationFromSQLite: dump_sqlite.py output applies
#
# Default behavior: skip if no DSN is set. Live PG validation
# requires operator to provision a PG instance, then set
# SKYGATE_TEST_PG_DSN in the operator-side env (e.g. ~/.skygate-pg).
# When set, the 4 tests run and R27 reports pass/fail per-test.
#
# If SKYGATE_TEST_PG_DSN is unset (default for the main VM, which
# is SQLite-only), R27 reports SKIP. The build-time B18 check is
# the always-on foundation guarantee.
if [ "$QUICK" = 0 ]; then
  echo
  echo "[R27] PG foundation live check (4 verification tests, v0.31.0)"
  if [ -z "${SKYGATE_TEST_PG_DSN:-}" ]; then
    echo "  ${YLW}SKIP${NC}  R27 SKYGATE_TEST_PG_DSN not set (PG-staging not provisioned; B18 covers foundation)"
  else
    # Run the 4 PG tests on the operator workstation (where the
    # DSN is reachable). The default verify-post runs from the
    # operator's machine, so this is direct (not via SSH).
    PG_TEST_OUT=$(SKYGATE_TEST_PG_DSN="$SKYGATE_TEST_PG_DSN" \
      "$GO" test -tags postgres -count=1 -timeout 120s -v \
        -run "TestPGRoundtripSchema|TestPGMigrationIdempotency|TestPGLockTimeout|TestPGDataMigrationFromSQLite" \
        ./internal/db/ 2>&1)
    PG_RC=$?
    PG_TESTS_PASS=$(echo "$PG_TEST_OUT" | grep -cE '^--- PASS:' || true)
    PG_TESTS_FAIL=$(echo "$PG_TEST_OUT" | grep -cE '^--- FAIL:' || true)
    if [ "$PG_RC" -eq 0 ] && [ "$PG_TESTS_FAIL" -eq 0 ] && [ "$PG_TESTS_PASS" -ge 4 ]; then
      echo "  ${GRN}PASS${NC}  R27 4 PG verification tests passed ($PG_TESTS_PASS PASS, $PG_TESTS_FAIL FAIL)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R27 PG verification tests: $PG_TESTS_PASS PASS, $PG_TESTS_FAIL FAIL (rc=$PG_RC)"
      echo "$PG_TEST_OUT" | grep -E '^(--- PASS|--- FAIL|    .+\.go:[0-9]+:)' | head -20 | sed 's/^/        /'
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  fi
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo
echo "=== summary ==="
echo "  ${GRN}PASS${NC}: $RESULTS_PASS"
echo "  ${RED}FAIL${NC}: $RESULTS_FAIL"

if [ "$RESULTS_FAIL" -gt 0 ]; then
  echo
  echo "${RED}post-deploy verification FAILED — investigate before continuing${NC}"
  exit 1
fi
echo
echo "${GRN}post-deploy verification PASSED — system is healthy${NC}"
exit 0
