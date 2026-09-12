#!/bin/bash
# check_b_prod_health.sh — pre-deploy sanity check that the
# CURRENT production stack is alive before the operator runs
# auto-deploy.
#
# Why this exists (operator 2026-09-12):
#   Before the skygate container OOMed and 13.69 went silent,
#   the operator had no pre-deploy health gate. Auto-deploy
#   fired blindly — if the target was already broken, the new
#   binary would inherit the broken state instead of recovering.
#   This script pins 4 contracts that prove the public-facing
#   stack is healthy:
#
#     1. https://skygate.skynas.ru/healthz              → 200
#     2. https://skygate.skynas.ru/login                → 200
#     3. https://skygate.skynas.ru/.well-known/openid-configuration → 200
#     4. build != "dev" (catches accidental dev-binary prod deploy)
#
# Optional contract 5 (skipped when SKYGATE_PROD_HOST is unset
# or the SSH key is absent) does a deeper check via SSH:
#     - docker ps shows skygate-skygate-1 with status=Up
#     - curl http://127.0.0.1:8080/healthz returns 200
#
# Run via:
#   bash scripts/check_b_prod_health.sh                # all checks
#   SKYGATE_PROD_HOST=<polygon-vm-host> bash scripts/check_b_prod_health.sh
#                                                       # include SSH probe
#
# Exit codes:
#   0 = all checks PASS
#   1 = at least one check FAIL (DO NOT auto-deploy)

set -euo pipefail

PROD_URL="${SKYGATE_PROD_URL:-https://skygate.skynas.ru}"
# Default to the LAN prod VM. Override SKYGATE_PROD_HOST to use
# a different target. Per the operator's 2026-09-11 redaction
# convention, the actual LAN IP lives in the operator's local
# env (or ~/.ssh/config `Host agent`), not in git-tracked files.
PROD_HOST="${SKYGATE_PROD_HOST:-<polygon-vm-host>}"

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

# curl helper with timeout + fail-on-error
http_get_status() {
    local url="$1"
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$url" 2>/dev/null || echo "000"
}

http_get_body() {
    local url="$1"
    curl -s --max-time 10 "$url" 2>/dev/null || echo ""
}

echo "=== contract 1: skygate /healthz returns 200 ==="
status=$(http_get_status "${PROD_URL}/healthz")
if [ "$status" = "200" ]; then
    ok "/healthz returns 200"
else
    bad "/healthz returned ${status} (want 200) — DO NOT auto-deploy"
fi

echo "=== contract 2: skygate /login page renders ==="
status=$(http_get_status "${PROD_URL}/login")
if [ "$status" = "200" ]; then
    ok "/login returns 200"
else
    bad "/login returned ${status} (want 200) — DO NOT auto-deploy"
fi

echo "=== contract 3: OIDC discovery endpoint reachable ==="
# OIDC A71 device auth flow depends on this endpoint
status=$(http_get_status "${PROD_URL}/.well-known/openid-configuration")
if [ "$status" = "200" ]; then
    ok "/.well-known/openid-configuration returns 200"
else
    bad "OIDC discovery returned ${status} (want 200) — A71 device auth WILL BREAK"
fi

echo "=== contract 4: build is NOT 'dev' (catches dev-binary prod deploy) ==="
body=$(http_get_body "${PROD_URL}/healthz")
# JSON shape: {"build":"v1.5.2-61-g9230130","status":"ok",...}
# dev builds have "build":"dev" — easy regex miss, use case-insensitive match
if echo "$body" | grep -qiE '"build"[[:space:]]*:[[:space:]]*"v[0-9]+\.[0-9]+\.[0-9]'; then
    build=$(echo "$body" | grep -oiE '"build"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1)
    ok "production build is a release tag ($build)"
elif echo "$body" | grep -qiE '"build"[[:space:]]*:[[:space:]]*"dev"'; then
    bad "build=dev detected — accidental dev-binary prod deploy"
else
    bad "could not parse build field from /healthz: ${body}"
fi

# Optional contract 5 — SSH-based deeper check.
# Skips silently when SKYGATE_PROD_HOST is unset OR SSH key absent.
# Cross-platform HOME: try $HOME first, then the WSL path /mnt/c/Users/<user>/.ssh
# (the user might be running this from Windows-PowerShell→bash-bridge or
# from native WSL bash — both need to find ~/.ssh/id_ed25519).
SSH_KEY=""
for k in "${HOME}/.ssh/id_ed25519" "/mnt/c/Users/${USER}/.ssh/id_ed25519" "/c/Users/${USER}/.ssh/id_ed25519" "/c/Users/knaga/.ssh/id_ed25519"; do
    if [ -f "$k" ]; then
        SSH_KEY="$k"
        break
    fi
done
if [ -n "$PROD_HOST" ] && [ -n "$SSH_KEY" ]; then
    echo "=== contract 5 (optional): SSH probe — skygate container Up + loopback healthz 200 ==="
    # Use explicit user@host (not 'agent' alias) so the script works on
    # both Windows-bash (with ~/.ssh/config) and bare WSL bash (without it).
    # Skip the script if the SSH key is found via /mnt/c (Windows path) —
    # the key needs mode 0600 which Windows doesn't set, so re-copy on the
    # fly to /tmp with proper perms before ssh uses it.
    if [[ "$SSH_KEY" == /mnt/c/* || "$SSH_KEY" == /c/* ]]; then
        SAFE_KEY="/tmp/check_b_prod_health_key_$$"
        cp "$SSH_KEY" "$SAFE_KEY" && chmod 0600 "$SAFE_KEY"
        SSH_KEY="$SAFE_KEY"
        trap "rm -f $SAFE_KEY" EXIT
    fi
    ssh_out=$(ssh -i "$SSH_KEY" -o ConnectTimeout=5 -o BatchMode=yes -o StrictHostKeyChecking=no \
        "hermes-debug@${PROD_HOST}" \
        'docker ps --format "{{.Names}} {{.Status}}" | grep -E "^skygate-skygate-1 " || echo NOT_PRESENT;
         curl -s -m 3 -o /dev/null -w "%{http_code}" http://127.0.0.1:8080/healthz' 2>&1) || ssh_out="SSH_FAIL"

    if echo "$ssh_out" | grep -q "NOT_PRESENT\|SSH_FAIL"; then
        echo "  SKIP  SSH probe failed (host unreachable or skygate container absent)"
    else
        docker_line=$(echo "$ssh_out" | head -1)
        loop_status=$(echo "$ssh_out" | tail -1)
        if echo "$docker_line" | grep -qE "Up.*\(healthy\)|^skygate-skygate-1 Up"; then
            ok "docker: skygate-skygate-1 status=$(echo "$docker_line" | awk '{print $2}')"
        else
            bad "docker: skygate-skygate-1 not 'Up (healthy)' (got: $docker_line)"
        fi
        if [ "$loop_status" = "200" ]; then
            ok "loopback: 127.0.0.1:8080/healthz returns 200"
        else
            bad "loopback: 127.0.0.1:8080/healthz returned $loop_status"
        fi
    fi
else
    echo "=== contract 5 (optional): SSH probe — SKIPPED (set SKYGATE_PROD_HOST='' to disable, or check SSH key exists) ==="
fi

echo ""
echo "All 4 mandatory contracts PASS. Production is healthy — auto-deploy safe."
