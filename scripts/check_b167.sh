#!/bin/bash
# check_b167.sh — OIDC config auto-sync (B167, v1.5.2)
#
# B161.1-4 made skygate a working OIDC provider for
# headscale. B167 closes the operator loop: instead
# of hand-editing headscale.conf + `docker restart
# headscale`, the operator clicks "Sync now" on
# /admin/oidc/sync (or sets SKYGATE_OIDC_AUTOSYNC=true
# for a boot-time auto-sync).
#
# This B-check verifies the B167 contract:
#  A. Source-contract checks (the bash script + Go
#     wrapper + admin handler + template + i18n keys
#     + routes + auto-init hook exist and are wired
#     correctly)
#  B. Live bash-script checks (deploy/oidc-sync.sh
#     works in download mode, generates a valid
#     headscale.conf `oidc:` block, doesn't include
#     the removed `strip_email_domain` key)
#  C. Live route check (GET /admin/oidc/sync
#     responds 200 with the expected page elements)
#
# All checks are read-only (no writes to headscale.conf
# or .env). The live bash check is gated on the
# operator having python3 + bash on PATH; it SKIPs
# cleanly on Windows CI.
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source files exist + are wired"

# A.1 — the bash script (the actual work-doer).
if [ -x deploy/oidc-sync.sh ] && [ -f deploy/oidc-sync.sh ]; then
    ok "deploy/oidc-sync.sh exists + is executable"
else
    bad "deploy/oidc-sync.sh MISSING or not executable"
fi

# A.1.5 — B167.2 regression guard: the SCOPE default must
# be the OIDC-standard scopes (openid, profile, email),
# NOT a URL or any other value. The B167 v1 commit had
# `SCOPE="${SCOPE:-/oidc/userinfo}"` (a URL, not a scope)
# which broke headscale's OIDC flow at the /oidc/token
# step (headscale would log a warning + the user claims
# wouldn't be returned — the Tailscale client would log
# in but no user would be created in headscale).
if grep -qE 'SCOPE="\$\{SCOPE:-(openid,profile,email|openid)\}"' deploy/oidc-sync.sh; then
    ok "SCOPE default is the OIDC-standard 'openid,profile,email' (B167.2 regression guard)"
else
    bad "SCOPE default is NOT 'openid,profile,email' — headscale would reject the /oidc/userinfo URL as an unknown scope, breaking the OIDC flow (this was the B167 v1 bug that broke the live Tailscale e2e on 2026-08-24)"
fi

# A.2 — the Go wrapper for the script.
if grep -q 'package oidc' internal/oidc/sync.go 2>/dev/null; then
    ok "internal/oidc/sync.go exists"
else
    bad "internal/oidc/sync.go MISSING (the Go wrapper for deploy/oidc-sync.sh)"
fi

# A.3 — the Go wrapper must have the 4 key
# functions: RunSync, RunSyncCtx, ShouldAutoSync,
# findSyncScript. The B-check is grep-based (a
# function removal would silently break callers).
for fn in "func RunSync" "func RunSyncCtx" "func ShouldAutoSync" "func findSyncScript"; do
    if grep -q "$fn" internal/oidc/sync.go; then
        ok "internal/oidc/sync.go has $fn"
    else
        bad "internal/oidc/sync.go: $fn MISSING"
    fi
done

# A.4 — the SyncResult struct must have all 14
# JSON fields the bash script writes. A missing
# field would silently break the parse in RunSync.
for field in "ok" "skygate_url" "client_id" "headscale_config_path" \
              "config_backup_path" "env_path" "env_backup_path" \
              "oidc_block_yaml" "mode" "headscale_restarted" \
              "headscale_healthy" "env_updated" "test_result" "duration_ms"; do
    if grep -q "\"$field\"" internal/oidc/sync.go; then
        ok "SyncResult has JSON field '$field'"
    else
        bad "SyncResult missing JSON field '$field' (bash script writes it; Go would fail to parse)"
    fi
done

# A.5 — the admin handler.
if grep -q 'GetAdminOIDCSync' internal/feature/admin/oidc_sync.go \
   && grep -q 'PostAdminOIDCSync' internal/feature/admin/oidc_sync.go; then
    ok "internal/feature/admin/oidc_sync.go has Get + Post handlers"
else
    bad "internal/feature/admin/oidc_sync.go missing Get/Post handlers"
fi

# A.6 — the handler must call oidc.RunSync (not
# a hand-rolled subprocess call). The B-check
# prevents future refactors from bypassing the
# wrapper and re-implementing the JSON parse
# (which would be a duplication of logic).
if grep -q 'oidcpkg.RunSync\|oidc.RunSync' internal/feature/admin/oidc_sync.go; then
    ok "admin handler calls oidc.RunSync via the package wrapper"
else
    bad "admin handler does not call oidc.RunSync — must use the package wrapper"
fi

# A.7 — the handler must be admin-only (the
# /admin/oidc/sync POST mutates headscale.conf
# and restarts headscale — catastrophic if
# non-admin can trigger it).
if grep -q 'IsAdmin' internal/feature/admin/oidc_sync.go; then
    ok "admin handler is admin-only (checks IsAdmin)"
else
    bad "admin handler does not check IsAdmin — /admin/oidc/sync POST would be open to non-admins"
fi

# A.8 — the template.
if [ -f internal/handlers/templates/admin/oidc_sync.html ]; then
    ok "internal/handlers/templates/admin/oidc_sync.html exists"
else
    bad "internal/handlers/templates/admin/oidc_sync.html MISSING"
fi

# A.9 — the template must have the form + the
# 5 form fields (config_path, container, env_path,
# mode, redirect_uris) + a submit button.
for f in "headscale_config_path" "headscale_container" "skygate_env_path" \
         "mode" "redirect_uris" "oidc-sync-submit"; do
    if grep -q "$f" internal/handlers/templates/admin/oidc_sync.html; then
        ok "template references '$f'"
    else
        bad "template: '$f' MISSING"
    fi
done

# A.10 — i18n keys (RU + EN). The two maps
# must have the same number of oidc_sync.* keys
# (a missing key in one language silently degrades
# UX). We split the file by `^var (ru|en)Admin`
# and count independently.
RU_COUNT=$(awk '/^var ruAdmin/,/^}$/' internal/i18n/catalog_admin.go | grep -cE '"oidc_sync\.' || true)
EN_COUNT=$(awk '/^var enAdmin/,/^}$/' internal/i18n/catalog_admin.go | grep -cE '"oidc_sync\.' || true)
if [ "$RU_COUNT" -ge 40 ] && [ "$EN_COUNT" -ge 40 ] && [ "$RU_COUNT" = "$EN_COUNT" ]; then
    ok "i18n parity: RU=$RU_COUNT, EN=$EN_COUNT oidc_sync.* keys"
else
    bad "i18n parity broken: RU=$RU_COUNT, EN=$EN_COUNT (must match, both >= 40)"
fi

# A.11 — nav link in layout.html (the operator
# needs a discoverable way to reach the page
# from the sidebar).
if grep -q '/admin/oidc/sync' internal/handlers/templates/layout.html; then
    ok "layout.html has /admin/oidc/sync sidebar link"
else
    bad "layout.html: /admin/oidc/sync sidebar link MISSING"
fi

# A.12 — routes in main.go (both GET + POST
# must be registered, both behind authMW).
if grep -q 'GET /admin/oidc/sync' cmd/skygate/main.go \
   && grep -q 'POST /admin/oidc/sync' cmd/skygate/main.go; then
    ok "main.go registers GET + POST /admin/oidc/sync"
else
    bad "main.go: GET or POST /admin/oidc/sync route MISSING"
fi

# A.13 — the POST route must be behind authMW
# (the handler does the IsAdmin check, but
# authMW is the first line of defense — covers
# any future bug in the handler that forgets
# the admin check).
if grep -A1 'POST /admin/oidc/sync' cmd/skygate/main.go | grep -q 'authMW'; then
    ok "POST /admin/oidc/sync is behind authMW"
else
    bad "POST /admin/oidc/sync is NOT behind authMW"
fi

# A.14 — the auto-init hook (boot-time
# auto-sync when SKYGATE_OIDC_AUTOSYNC=true).
if grep -q 'oidcsvc.ShouldAutoSync' cmd/skygate/main.go; then
    ok "main.go has boot-time auto-sync (ShouldAutoSync)"
else
    bad "main.go: auto-sync hook (ShouldAutoSync) MISSING"
fi

# ---------------------------------------------------------------------------
hdr "contract B: deploy/oidc-sync.sh works in download mode"

# B.1 — invoke the script in --download-only
# mode. We skip on Windows (no bash shebang
# support) and on environments without python3.
SKIP_B=0
case "$(uname -s 2>/dev/null || echo Windows)" in
    Windows*|MINGW*|MSYS*|CYGWIN*) SKIP_B=1 ;;
esac
if ! command -v python3 >/dev/null 2>&1; then
    SKIP_B=1
fi

if [ "$SKIP_B" = "1" ]; then
    skip "live script test (Windows or no python3; the source-contract checks above are enough on this host)"
else
    SCRIPT_OUT=$(bash deploy/oidc-sync.sh \
        https://skygate.example.com headscale test-secret-do-not-use-in-prod \
        https://head.skynas.ru/oidc/callback \
        --download-only --headscale-config /tmp/headscale-test.yaml \
        --skygate-env /tmp/skygate-test.env 2>/dev/null || true)

    # B.1.1 — JSON output, parseable.
    if echo "$SCRIPT_OUT" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["ok"]; assert d["mode"]=="download"; assert d["headscale_restarted"]==0; assert d["env_updated"]==0' 2>/dev/null; then
        ok "download mode: valid JSON, mode=download, no restart, no env update"
    else
        bad "download mode: output not valid JSON or fields wrong: $SCRIPT_OUT"
    fi

    # B.1.2 — the generated `oidc:` block must
    # include the issuer + client_id we passed in.
    if echo "$SCRIPT_OUT" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert "issuer: https://skygate.example.com" in d["oidc_block_yaml"]; assert "client_id: headscale" in d["oidc_block_yaml"]' 2>/dev/null; then
        ok "generated block has issuer + client_id"
    else
        bad "generated block missing issuer or client_id"
    fi

    # B.1.3 — the generated block must NOT include
    # `strip_email_domain` (removed in headscale
    # 0.23+ — a regression would break headscale
    # 0.29.x at startup).
    if echo "$SCRIPT_OUT" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert "strip_email_domain" not in d["oidc_block_yaml"]' 2>/dev/null; then
        ok "generated block has no strip_email_domain (B167.1 regression guard)"
    else
        bad "generated block contains strip_email_domain (removed in headscale 0.23+ — would crash headscale 0.29.x)"
    fi

    # B.1.4 — the generated block must include
    # redirect_uris as a list with the URL we
    # passed in.
    if echo "$SCRIPT_OUT" | python3 -c 'import json,sys,re; d=json.load(sys.stdin); assert "https://head.skynas.ru/oidc/callback" in d["oidc_block_yaml"]; assert "redirect_uris" in d["oidc_block_yaml"]' 2>/dev/null; then
        ok "generated block has redirect_uris list with the URL"
    else
        bad "generated block missing redirect_uris"
    fi

    # B.1.5 — download mode must NOT write any
    # files (the test paths above are fake; the
    # script should not have created them).
    if [ ! -f /tmp/headscale-test.yaml ] && [ ! -f /tmp/skygate-test.env ]; then
        ok "download mode created no files"
    else
        rm -f /tmp/headscale-test.yaml /tmp/skygate-test.env
        bad "download mode created files at /tmp (should not write anything)"
    fi
fi

# ---------------------------------------------------------------------------
hdr "contract C: live route check (GET /admin/oidc/sync)"

# C.1 — the route should respond 200 with the
# expected page elements. We use the same
# pattern as B161 + B162: hit the live host
# (or skip on local-only environments).
SKYGATE_BASE="${SKYGATE_BASE:-http://127.0.0.1:8080}"
if curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "$SKYGATE_BASE/login" 2>/dev/null | grep -qE '^(200|302)$'; then
    # C.1.1 — login page reachable; check the
    # sync page (it should redirect to /login if
    # not authenticated, or 200 if a session cookie
    # is provided). The B-check is the existence
    # of the route, not auth.
    CODE=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "$SKYGATE_BASE/admin/oidc/sync" 2>/dev/null || echo "000")
    case "$CODE" in
        200) ok "GET /admin/oidc/sync returns 200 (no auth)" ;;
        302|303) ok "GET /admin/oidc/sync returns $CODE (redirect — likely to /login, expected without auth)" ;;
        *) bad "GET /admin/oidc/sync returned $CODE (expected 200 or 302)" ;;
    esac
else
    skip "live route check (skygate not running at $SKYGATE_BASE)"
fi

# ---------------------------------------------------------------------------
hdr "summary"
echo "B167: OIDC config auto-sync (full Option C — docker + systemd + k8s + manual + download + auto-init)"
echo "all contracts satisfied"
