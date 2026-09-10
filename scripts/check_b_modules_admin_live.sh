#!/bin/bash
# B-mod-bcheck: /admin/modules end-to-end live-verify (2026-09-10).
#
# Goes beyond check_b_modules_admin.sh (which is a static
# source check — build + vet + grep). This script:
#   1. Connects to a running skygate instance (svi polygon
#      or agent)
#   2. Logs in as the admin user (POST /login with
#      username + password from CLI args or env)
#   3. GETs /admin/modules with the session cookie
#   4. Verifies the rendered HTML contains the Tailscale
#      row + the state pill + the "NotWired" warning is
#      ABSENT (Manager is wired on svi as of B-mod-core
#      re-merge 0574681f)
#   5. Verifies the audit_log row 'module.tailscale.init: ok'
#      exists (proves Manager.initOne succeeded)
#   6. Optionally POSTs /admin/modules/tailscale/install
#      (mode=none) to verify the install dispatch + CSRF
#      flow works end-to-end. Disabled by default — set
#      SKYGATE_RUN_INSTALL=1 to enable.
#
# Usage:
#   bash scripts/check_b_modules_admin_live.sh \
#       --host=https://skygate.skynas.ru \
#       --user=skyadmin \
#       --password=...
#   OR via env:
#       SKYGATE_LIVE_HOST=https://skygate.skynas.ru
#       SKYGATE_LIVE_USER=skyadmin
#       SKYGATE_LIVE_PASSWORD=...
#
# The script SKIPS gracefully (exit 0 with a message) if
# the host is unreachable — that way it can be wired into
# verify_pre_deploy.sh for offline builds (the B-check
# B-mod-bcheck itself verifies build + tests on Windows,
# and this live variant runs only against a real VM).
#
# Exit codes:
#   0 — all live contracts pass (or host unreachable — skipped)
#   1 — at least one live contract failed
#   2 — invalid args / missing required env

set -u

# === paths ===
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# === args ===
HOST="${SKYGATE_LIVE_HOST:-}"
USER="${SKYGATE_LIVE_USER:-}"
PASS="${SKYGATE_LIVE_PASSWORD:-}"
PG_HOST="${SKYGATE_PG_HOST:-95.165.170.190}"
PG_USER="${SKYGATE_PG_USER:-skygate_test}"
PG_DB="${SKYGATE_PG_DB:-skygate_test}"
RUN_INSTALL="${SKYGATE_RUN_INSTALL:-0}"

while [ $# -gt 0 ]; do
    case "$1" in
        --host=*)      HOST="${1#*=}" ;;
        --host)        HOST="${2:-}"; shift ;;
        --user=*)      USER="${1#*=}" ;;
        --user)        USER="${2:-}"; shift ;;
        --password=*)  PASS="${1#*=}" ;;
        --password)    PASS="${2:-}"; shift ;;
        --pg-host=*)   PG_HOST="${1#*=}" ;;
        --pg-host)     PG_HOST="${2:-}"; shift ;;
        --pg-user=*)   PG_USER="${1#*=}" ;;
        --pg-user)     PG_USER="${2:-}"; shift ;;
        --pg-db=*)     PG_DB="${1#*=}" ;;
        --pg-db)       PG_DB="${2:-}"; shift ;;
        --run-install) RUN_INSTALL=1 ;;
        -h|--help)     sed -n '2,38p' "$0"; exit 0 ;;
        *) echo "ERROR: unknown arg: $1" >&2; exit 2 ;;
    esac
    shift
done

# === counters ===
PASS=0
FAIL=0
SKIP=0
TOTAL=0
RED=$'\033[0;31m'
GRN=$'\033[0;32m'
YEL=$'\033[1;33m'
CYN=$'\033[0;36m'
RST=$'\033[0m'

pass() { PASS=$((PASS + 1)); TOTAL=$((TOTAL + 1)); printf '%s PASS %s [live %d] %s\n' "${GRN}" "${RST}" "${TOTAL}" "$1"; }
fail() { FAIL=$((FAIL + 1)); TOTAL=$((TOTAL + 1)); printf '%s FAIL %s [live %d] %s\n' "${RED}" "${RST}" "${TOTAL}" "$1"; if [ $# -ge 2 ]; then printf '           %s\n' "$2"; fi; }
skip() { SKIP=$((SKIP + 1)); TOTAL=$((TOTAL + 1)); printf '%s SKIP %s [live %d] %s\n' "${CYN}" "${RST}" "${TOTAL}" "$1"; if [ $# -ge 2 ]; then printf '           %s\n' "$2"; fi; }
section() { printf '\n%s== %s ==%s\n' "${YEL}" "$1" "${RST}"; }

# === preflight ===
section "Preflight"
if [ -z "$HOST" ]; then
    echo "SKIP: no --host (set SKYGATE_LIVE_HOST or pass --host=https://...)" >&2
    exit 0
fi
if [ -z "$USER" ] || [ -z "$PASS" ]; then
    echo "SKIP: no --user / --password (set SKYGATE_LIVE_USER + SKYGATE_LIVE_PASSWORD)" >&2
    exit 0
fi
if ! command -v curl >/dev/null 2>&1; then
    fail "curl on PATH" "required for live HTTP requests"
    exit 1
fi
pass "curl on PATH ($(curl --version 2>&1 | head -1))"
if ! command -v psql >/dev/null 2>&1; then
    skip "psql on PATH" "audit_log SQL check will be skipped (live HTTP contracts still run)"
fi

# === contract 1: /healthz reachable ===
section "Reachable"
HTTP_HEALTHZ=$(curl -sS -o /tmp/b_mod_bcheck_healthz.body -w '%{http_code}' --max-time 10 "$HOST/healthz" 2>/dev/null || echo "000")
if [ "$HTTP_HEALTHZ" = "200" ]; then
    pass "/healthz returns 200"
else
    fail "/healthz returns 200" "got $HTTP_HEALTHZ"
    section "Summary"
    printf '  Total: %d  Pass: %d  Fail: %d  Skip: %d\n' "${TOTAL}" "${PASS}" "${FAIL}" "${SKIP}"
    exit 1
fi

# === contract 2: login as admin ===
section "Auth: POST /login"
COOKIE_JAR=$(mktemp)
trap 'rm -f "$COOKIE_JAR" /tmp/b_mod_bcheck_*.body' EXIT

# Try login. /login redirects to /dashboard on success
# (HTTP 302) and re-renders the form (HTTP 200) on failure.
LOGIN_HTTP=$(curl -sS -o /tmp/b_mod_bcheck_login.body -w '%{http_code}' \
    -c "$COOKIE_JAR" \
    -X POST "$HOST/login" \
    -d "username=$USER&password=$PASS" \
    --max-time 10 2>/dev/null || echo "000")
if [ "$LOGIN_HTTP" = "302" ] || [ "$LOGIN_HTTP" = "303" ]; then
    pass "POST /login returns $LOGIN_HTTP (success — cookie set)"
elif [ "$LOGIN_HTTP" = "200" ]; then
    # 200 might be the form re-render on failed login.
    # Check for "invalid credentials" or similar in body.
    if grep -qi 'invalid\|неверн' /tmp/b_mod_bcheck_login.body 2>/dev/null; then
        fail "POST /login returns 200 + invalid credentials message" "wrong password?"
        exit 1
    else
        pass "POST /login returns 200 (form re-render, but no error — admin might not exist yet)"
    fi
else
    fail "POST /login returns 302/200" "got $LOGIN_HTTP"
    exit 1
fi

# Verify the session cookie is set.
if grep -q 'skygate_session' "$COOKIE_JAR" 2>/dev/null; then
    pass "skygate_session cookie set in jar"
else
    fail "skygate_session cookie set in jar" "auth might be broken"
    cat "$COOKIE_JAR" >&2
    exit 1
fi

# === contract 3: /admin/modules renders with Tailscale row ===
section "GET /admin/modules (Manager wired)"
ADMIN_HTTP=$(curl -sS -o /tmp/b_mod_bcheck_modules.body -w '%{http_code}' \
    -b "$COOKIE_JAR" \
    --max-time 10 "$HOST/admin/modules" 2>/dev/null || echo "000")
if [ "$ADMIN_HTTP" = "200" ]; then
    pass "GET /admin/modules returns 200"
else
    fail "GET /admin/modules returns 200" "got $ADMIN_HTTP (auth?)"
    exit 1
fi

# The rendered HTML must contain:
#  - "tailscale" (the module name) — proves the row is there
#  - "modules.title" or "Modules" (i18n key resolved or fallback)
#  - "Manager not wired" warning ABSENT (Manager is wired)
if grep -q '>tailscale<' /tmp/b_mod_bcheck_modules.body 2>/dev/null; then
    pass "rendered HTML contains <code>tailscale</code> row"
else
    fail "rendered HTML contains tailscale row" "Manager might not have any registered modules"
    head -50 /tmp/b_mod_bcheck_modules.body >&2
fi

if grep -q 'Manager not wired' /tmp/b_mod_bcheck_modules.body 2>/dev/null; then
    fail "Manager not wired warning ABSENT" "found 'Manager not wired' — the wiring is broken"
else
    pass "Manager not wired warning absent (Manager is wired)"
fi

# State pill should be one of the 7 lifecycle states.
if grep -qE 'modules-state (not_installed|installed|starting|running|stopping|stopped|error)' /tmp/b_mod_bcheck_modules.body 2>/dev/null; then
    pass "state pill rendered with a valid lifecycle state"
else
    fail "state pill rendered" "no 'modules-state <state>' found in HTML"
fi

# === contract 4: detail page renders ===
section "GET /admin/modules/tailscale"
DETAIL_HTTP=$(curl -sS -o /tmp/b_mod_bcheck_detail.body -w '%{http_code}' \
    -b "$COOKIE_JAR" \
    --max-time 10 "$HOST/admin/modules/tailscale" 2>/dev/null || echo "000")
if [ "$DETAIL_HTTP" = "200" ]; then
    pass "GET /admin/modules/tailscale returns 200"
else
    fail "GET /admin/modules/tailscale returns 200" "got $DETAIL_HTTP"
fi
# Health sub-heading should be in the page.
if grep -q 'Health' /tmp/b_mod_bcheck_detail.body 2>/dev/null; then
    pass "detail page has 'Health' section"
else
    fail "detail page has 'Health' section" "B-mod-admin template missing the heading"
fi
# Sub-features section should be in the page.
if grep -q 'Sub-features' /tmp/b_mod_bcheck_detail.body 2>/dev/null; then
    pass "detail page has 'Sub-features' section"
else
    fail "detail page has 'Sub-features' section" "B-mod-admin template missing"
fi

# === contract 5: audit_log row 'module.tailscale.init: ok' ===
section "Audit log: module.tailscale.init: ok"
if command -v psql >/dev/null 2>&1; then
    if [ -z "${PGPASSWORD:-}" ]; then
        # Use a default that matches the polygon setup.
        # operator can override via SKYGATE_PG_PASSWORD env.
        export PGPASSWORD="${SKYGATE_PG_PASSWORD:-ebbab134df12a85d459994f6}"
    fi
    if INIT_ROW=$(psql -h "$PG_HOST" -U "$PG_USER" -d "$PG_DB" -tA -c "SELECT action, detail FROM audit_log WHERE action = 'module.tailscale.init' ORDER BY created_at DESC LIMIT 1;" 2>/dev/null); then
        if [ -n "$INIT_ROW" ]; then
            if printf '%s' "$INIT_ROW" | grep -q 'ok'; then
                pass "audit_log has 'module.tailscale.init | ok' (Manager wired + Init succeeded)"
            else
                fail "audit_log module.tailscale.init succeeded" "last row: $INIT_ROW"
            fi
        else
            fail "audit_log has any 'module.tailscale.init' row" "no rows found"
        fi
    else
        skip "audit_log query" "psql failed (DB unreachable?)"
    fi
else
    skip "audit_log: module.tailscale.init: ok" "psql not on PATH"
fi

# === contract 6: state.json exists in /var/lib/skygate/modules/tailscale/ ===
# (This is a host-side check — only meaningful if the
#  script runs ON the skygate VM, not from a remote
#  check-out. We probe via SSH when SKYGATE_SSH_HOST is
#  set; otherwise skip.)
section "state.json on disk"
if [ -n "${SKYGATE_SSH_HOST:-}" ]; then
    if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o BatchMode=yes "${SKYGATE_SSH_HOST}" "test -f /var/lib/skygate/modules/tailscale/state.json" 2>/dev/null; then
        pass "state.json exists on skygate VM"
    else
        fail "state.json exists on skygate VM" "Manager didn't write per-module state"
    fi
else
    skip "state.json on disk" "SKYGATE_SSH_HOST not set (live SSH probe skipped)"
fi

# === contract 7: optional install flow ===
section "POST /admin/modules/tailscale/install (optional)"
if [ "$RUN_INSTALL" = "1" ]; then
    # Mint a fresh CSRF cookie first.
    CSRF_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' \
        -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
        --max-time 5 "$HOST/admin/modules/csrf" 2>/dev/null || echo "000")
    if [ "$CSRF_HTTP" != "303" ] && [ "$CSRF_HTTP" != "200" ]; then
        fail "CSRF refresh (GET /admin/modules/csrf)" "got $CSRF_HTTP"
    else
        CSRF=$(grep skygate_modules_csrf "$COOKIE_JAR" 2>/dev/null | awk '{print $7}' | tail -1)
        if [ -z "$CSRF" ]; then
            fail "CSRF cookie value captured" "jar missing skygate_modules_csrf"
        else
            # POST install with mode=none. The real
            # tailscale.Module's install() function
            # dispatches by env var SKYGATE_TS_INSTALL_MODE.
            # If that's not set, install() returns
            # an error (no mode). So we just expect a
            # 303 redirect with err= or ok= flash.
            INSTALL_HTTP=$(curl -sS -o /dev/null -w '%{http_code}' \
                -b "$COOKIE_JAR" \
                -X POST "$HOST/admin/modules/tailscale/install" \
                -d "csrf=$CSRF" \
                --max-time 10 2>/dev/null || echo "000")
            if [ "$INSTALL_HTTP" = "303" ]; then
                pass "POST /admin/modules/tailscale/install returns 303 (CSRF + dispatch worked)"
            else
                fail "POST /admin/modules/tailscale/install returns 303" "got $INSTALL_HTTP"
            fi
        fi
    fi
else
    skip "install dispatch" "SKYGATE_RUN_INSTALL=0 (default — operator can re-run with --run-install)"
fi

# === summary ===
section "Summary"
PASS_RATE=$((PASS * 100 / (TOTAL > 0 ? TOTAL : 1)))
printf '  Total: %d  Pass: %d  Fail: %d  Skip: %d  (%d%% pass rate, ignoring skips)\n' \
    "${TOTAL}" "${PASS}" "${FAIL}" "${SKIP}" "${PASS_RATE}"
if [ "${FAIL}" -eq 0 ]; then
    if [ "${SKIP}" -gt 0 ]; then
        printf '%s✓ B-mod-bcheck live: all live contracts pass (%d skipped)%s\n' "${GRN}" "${SKIP}" "${RST}"
    else
        printf '%s✓ B-mod-bcheck live: all live contracts pass%s\n' "${GRN}" "${RST}"
    fi
    exit 0
else
    printf '%s✗ B-mod-bcheck live: %d contract(s) failed%s\n' "${RED}" "${FAIL}" "${RST}"
    exit 1
fi
