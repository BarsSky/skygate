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
#     1. https://<prod>/healthz                          → 200
#     2. https://<prod>/login                            → 200
#     3. https://<prod>/.well-known/openid-configuration → 200
#     4. build != "dev" (catches accidental dev-binary prod deploy)
#
# Optional contract 5 (skipped when SKYGATE_PROD_HOST is unset
# or the SSH key is absent) does a deeper check via SSH:
#     - docker ps shows skygate-skygate-1 with status=Up
#     - curl http://127.0.0.1:8080/healthz returns 200
#
# ─────────────────────────────────────────────────────────────────────────────
# 2026-09-25 (B327, v1.5.93) — THIS SCRIPT WAS REDDENING CI FOR A REASON THAT
# HAS NOTHING TO DO WITH THE COMMIT UNDER TEST.
#
# Measured: CI run 36165848857 on a docs-only commit went red with
#
#     FAIL  /healthz returned 502 (want 200) — DO NOT auto-deploy
#
# because the operator was updating skygate to v1.5.92 at that moment: nginx was
# up, the container was being recreated, so the proxy answered 502. Re-running the
# SAME commit gave `catalog clean: 376 PASS, 1 SKIP, 0 FAIL` — the verdict tracked
# the operator's deploy, not the code. Two consequences, both bad:
#
#   * AGENTS.md rule 1 says a check that needs live state must report SKIP, NEVER
#     FAIL, when that state is unavailable. A 502 from the reverse proxy (or 000:
#     no connection at all) IS "unavailable" — it is not evidence about the commit;
#   * the check is registered as a plain `run_check`, so it runs on EVERY push, and
#     CI fails the job on any `^  FAIL`. A red commit then blocks the next tag,
#     because release.yml's preflight refuses a commit whose CI is not green.
#
# THE FIX: the reachability verdict is now TRI-STATE, and the distinction is
# *whose* answer it is.
#
#   PASS  200                     the application answered, healthy.
#   SKIP  000 / 502 / 503 / 504   NOT the application's answer: no connection, or a
#                                 proxy/gateway that is up while its upstream is
#                                 restarting. Reported as SKIP with the reason, and
#                                 the script still exits 0 — unless strict mode is on.
#   FAIL  anything else           we REACHED something that answered wrongly: a 4xx
#                                 or 5xx from the app itself, or a reachable body
#                                 whose build is `dev` or unparseable. That IS a
#                                 property of the deployment and must be red.
#
# STRICT MODE: `SKYGATE_PROD_REQUIRE_HEALTHY=1` turns the SKIP rows back into FAIL,
# which is what the operator's pre-deploy gate wants (an unreachable production is
# exactly when you must NOT deploy). Set it in the pre-deploy flow, NOT in CI — see
# docs/operations.md §1.7. Usage:
#
#   bash scripts/check_b_prod_health.sh                       # tolerant (CI-safe)
#   SKYGATE_PROD_REQUIRE_HEALTHY=1 bash scripts/check_b_prod_health.sh   # pre-deploy
#   bash scripts/check_b_prod_health.sh --classify 502        # explain one status
#
# Run via:
#   bash scripts/check_b_prod_health.sh                # all checks
#   SKYGATE_PROD_HOST=<polygon-vm-host> bash scripts/check_b_prod_health.sh
#                                                       # include SSH probe
#
# Exit codes:
#   0 = no contract FAILed (rows may have SKIPped in tolerant mode)
#   1 = at least one contract FAILed (DO NOT auto-deploy)
#   2 = usage error

set -uo pipefail

PROD_URL="${SKYGATE_PROD_URL:-https://skygate.skynas.ru}"
# Default to the LAN prod VM. Override SKYGATE_PROD_HOST to use a
# different target. Per the operator's 2026-09-11 redaction
# convention, the actual LAN IP lives in the operator's local
# env (or ~/.ssh/config `Host agent`), not in git-tracked files.
PROD_HOST="${SKYGATE_PROD_HOST:-<polygon-vm-host>}"
STRICT="${SKYGATE_PROD_REQUIRE_HEALTHY:-0}"

PASS=0; FAIL=0; SKIP=0
ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP  $1"; SKIP=$((SKIP+1)); }

# ── The reachability classifier (pure; also reachable as `--classify`) ────────
# Prints "<verdict>  <reason>" and exits 0/1: OK and SKIP are not failures.
prod_classify() {
    local status="$1" strict="${2:-0}"
    case "$status" in
        200)
            printf 'OK  the application answered 200\n'; return 0 ;;
        000)
            if [ "$strict" = "1" ]; then
                printf 'FAIL  no connection at all — curl could not reach %s (pre-deploy strict mode)\n' "$PROD_URL"; return 1
            fi
            printf 'SKIP  no connection at all — curl could not reach %s (DNS, network, or the host is down); this is not the application answer\n' "$PROD_URL"; return 0 ;;
        502|503|504)
            if [ "$strict" = "1" ]; then
                printf 'FAIL  the proxy answered %s, not the application — the upstream is unavailable (pre-deploy strict mode)\n' "$status"; return 1
            fi
            printf 'SKIP  the proxy answered %s, not the application — the reverse proxy is up while its upstream is unavailable or restarting; this is not the application answer\n' "$status"; return 0 ;;
        4*|5*)
            printf 'FAIL  the application answered %s — a wrong answer from a reachable stack is a real defect\n' "$status"; return 1 ;;
        *)
            printf 'FAIL  unexpected status %s\n' "$status"; return 1 ;;
    esac
}

# The build-field classifier (pure). It only ever sees a body that carried an
# APPLICATION answer, so an empty body here means a reachable 200 with no payload.
prod_build_classify() {
    local body="$1" strict="${2:-0}"
    if printf '%s' "$body" | grep -qiE '"build"[[:space:]]*:[[:space:]]*"v[0-9]+\.[0-9]+\.[0-9]'; then
        printf 'OK  production build is a release tag (%s)\n' \
            "$(printf '%s' "$body" | grep -oiE '"build"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1)"
        return 0
    fi
    if printf '%s' "$body" | grep -qiE '"build"[[:space:]]*:[[:space:]]*"dev"'; then
        printf 'FAIL  build=dev detected — accidental dev-binary prod deploy\n'; return 1
    fi
    if [ -z "$(printf '%s' "$body" | tr -d '[:space:]')" ]; then
        if [ "$strict" = "1" ]; then
            printf 'FAIL  /healthz answered 200 with an empty body, so the build field cannot be read (pre-deploy strict mode)\n'; return 1
        fi
        printf 'SKIP  /healthz answered 200 with an empty body, so the build field cannot be read\n'; return 0
    fi
    printf 'FAIL  could not parse the build field from /healthz: %s\n' "$body"; return 1
}

# `--classify <status> [strict]` — explain one decision without touching the network.
# The strict flag defaults to SKYGATE_PROD_REQUIRE_HEALTHY, so an operator who has the
# env var set and asks `--classify 502` gets the answer that run would actually give;
# the positional third argument still overrides it. Used by
# scripts/check_b327_prod_health_skip.sh and handy for the operator.
if [ "${1:-}" = "--classify" ]; then
    case "${2:-}" in
        "") echo "usage: $0 --classify <http-status> [strict]" >&2; exit 2 ;;
    esac
    prod_classify "$2" "${3:-$STRICT}"
    exit $?
fi

# curl helper with timeout. `%{http_code}` prints 000 on ANY transport failure
# AND curl exits non-zero, so the old `|| echo "000"` appended a second 000 and the
# classifier received the six-character string "000000" — an "unexpected status",
# i.e. a FAIL, which is precisely the flapping this block removes. Normalise to a
# three-digit code and never concatenate.
http_get_status() {
    local url="$1" code
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$url" 2>/dev/null)"
    printf '%s' "${code:-000}"
}

http_get_body() {
    local url="$1"
    curl -s --max-time 10 "$url" 2>/dev/null || true
}

# One contract: fetch a URL, classify the status, count it. A FAIL of the fetch
# does not stop the script: all contracts are reported, then the summary decides.
prod_contract() {
    local label="$1" url="$2" status verdict reason
    status="$(http_get_status "$url")"
    verdict="$(prod_classify "$status" "$STRICT")"
    reason="${verdict#*  }"
    case "${verdict%%  *}" in
        OK)   ok   "$label returned 200" ;;
        SKIP) skip "$label — $reason" ;;
        *)    bad  "$label — $reason" ;;
    esac
}

if [ "$STRICT" = "1" ]; then
    echo "=== strict mode (SKYGATE_PROD_REQUIRE_HEALTHY=1): an unreachable stack is a FAIL ==="
else
    echo "=== tolerant mode: an unreachable stack / a 502-504 from the proxy is a SKIP ==="
fi

echo "=== contract 1: skygate /healthz returns 200 ==="
healthz_status="$(http_get_status "${PROD_URL}/healthz")"
healthz_verdict="$(prod_classify "$healthz_status" "$STRICT")"
healthz_ok=0
case "${healthz_verdict%%  *}" in
    OK)   ok   "/healthz returned 200"; healthz_ok=1 ;;
    SKIP) skip "/healthz — ${healthz_verdict#*  }" ;;
    *)    bad  "/healthz — ${healthz_verdict#*  }" ;;
esac

echo "=== contract 2: skygate /login page renders ==="
prod_contract "/login" "${PROD_URL}/login"

echo "=== contract 3: OIDC discovery endpoint reachable ==="
# OIDC A71 device auth flow depends on this endpoint
prod_contract "/.well-known/openid-configuration" "${PROD_URL}/.well-known/openid-configuration"

echo "=== contract 4: build is NOT 'dev' (catches dev-binary prod deploy) ==="
# Only read the build field from an APPLICATION answer. A 502 error page (or a
# 502 error page on a 4xx/5xx app response) is not a health document: parsing it
# used to produce "could not parse the build field", which is how a proxy restart
# became a red gate. When contract 1 did not get an OK, this contract has nothing
# to judge and says so.
if [ "$healthz_ok" != "1" ]; then
    skip "build field — /healthz did not return 200, so there is no application health document to read it from"
else
    body="$(http_get_body "${PROD_URL}/healthz")"
    verdict="$(prod_build_classify "$body" "$STRICT")"
    reason="${verdict#*  }"
    case "${verdict%%  *}" in
        OK)   ok   "$reason" ;;
        SKIP) skip "$reason" ;;
        *)    bad  "$reason" ;;
    esac
fi

# Optional contract 5 — SSH-based deeper check.
# Skips silently when SKYGATE_PROD_HOST is unset OR SSH key absent.
# Cross-platform HOME: try $HOME first, then the WSL path /mnt/c/Users/<user>/.ssh
# (the user might be running this from Windows-PowerShell→bash-bridge or
# from native WSL bash — both need to find ~/.ssh/id_ed25519).
SSH_KEY=""
for k in "${HOME}/.ssh/id_ed25519" "/mnt/c/Users/${USER}/.ssh/id_ed25519" "/c/Users/${USER}/.ssh/id_ed25519"; do
    if [ -f "$k" ]; then
        SSH_KEY="$k"
        break
    fi
done
if [ -n "$PROD_HOST" ] && [ "$PROD_HOST" != "<polygon-vm-host>" ] && [ -n "$SSH_KEY" ]; then
    echo "=== contract 5 (optional): SSH probe — skygate container Up + loopback healthz 200 ==="
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
        skip "SSH probe failed (host unreachable or skygate container absent)"
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
            skip "loopback: 127.0.0.1:8080/healthz returned $loop_status (classified like the public path: an unavailable upstream is not a verdict about the deployment)"
        fi
    fi
    echo ""
    echo "=== contract 6 (optional): headscale OIDC + DERP config not disabled ==="
    # 2026-09-12 (post-incident): the docker restart triggered a chicken-
    # and-egg deadlock between skygate (pre-flight waits for headscale)
    # and headscale (OIDC init blocks on skygate). Recovery required
    # commenting out oidc.issuer + skygate DERP URL in headscale config.
    # Contract 6 catches the inverse: if either is commented out, fail.
    headscale_state=$(ssh -i "$SSH_KEY" -o ConnectTimeout=5 -o BatchMode=yes -o StrictHostKeyChecking=no \
        "hermes-debug@${PROD_HOST}" \
        '# Check for the OIDC-disabled marker comment OR commented-out issuer line
         if sudo -n grep -qE "^[[:space:]]*#[[:space:]]*oidc:.*disabled.*deadlock" /home/skyadmin/headscale/config/config.yaml 2>/dev/null; then
             echo OIDC_DISABLED;
         elif sudo -n grep -qE "^[[:space:]]*#[[:space:]]*issuer:" /home/skyadmin/headscale/config/config.yaml 2>/dev/null; then
             echo OIDC_DISABLED;
         elif sudo -n grep -qE "^[[:space:]]*#[[:space:]]*- http://skygate:8080" /home/skyadmin/headscale/config/config.yaml 2>/dev/null; then
             echo DERP_DISABLED;
         else
             echo OK;
         fi' 2>&1) || headscale_state="SSH_FAIL"
    if [ "$headscale_state" = "OK" ]; then
        ok "headscale config: OIDC + DERP enabled"
    elif [ "$headscale_state" = "OIDC_DISABLED" ]; then
        bad "headscale config: OIDC block is COMMENTED OUT (was disabled to break a startup deadlock; should be re-enabled now that skygate is Up)"
    elif [ "$headscale_state" = "DERP_DISABLED" ]; then
        bad "headscale config: skygate DERP URL is COMMENTED OUT (was disabled to break a startup deadlock; should be re-enabled now that skygate is Up)"
    else
        skip "SSH probe failed (host unreachable)"
    fi
else
    echo "=== contract 5 (optional): SSH probe — SKIPPED (set SKYGATE_PROD_HOST to enable, or the SSH key is absent) ==="
    echo "=== contract 6 (optional): headscale config check — SKIPPED (contract 5 not available) ==="
fi

echo ""
echo "B-prod-health summary: $PASS PASS, $FAIL FAIL, $SKIP SKIP  (strict=$STRICT, url=$PROD_URL)"
if [ "$FAIL" -gt 0 ]; then
    echo "Production is NOT healthy — DO NOT auto-deploy."
    exit 1
fi
if [ "$SKIP" -gt 0 ]; then
    echo "No contract FAILed, but $SKIP row(s) could not be evaluated — this run is INCONCLUSIVE, not a clean bill of health."
    echo "Re-run with SKYGATE_PROD_REQUIRE_HEALTHY=1 before a deploy to make an unreachable stack red."
    exit 0
fi
echo "All 4 mandatory contracts PASS. Production is healthy — auto-deploy safe."
exit 0
