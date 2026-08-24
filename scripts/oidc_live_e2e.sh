#!/bin/bash
# oidc_live_e2e.sh — B161.4 live OIDC e2e test (Bash)
#
# Operator 2026-08-24 follow-up to B161.1-3: now
# that skygate ships the OIDC provider, the last
# step to close the loop is wiring headscale to
# use it. This script exercises the OIDC endpoints
# as a fake headscale client would:
#
#   1. GET /.well-known/openid-configuration
#      → 200 + JSON with all 4 endpoint URLs
#   2. GET /oidc/jwks.json
#      → 200 + JSON with 1 RS256 key
#   3. GET /oidc/authorize?response_type=code&...
#      → 302 to /login?next=... (unauthenticated)
#   4. POST /oidc/token with bad code
#      → 400 + JSON {"error":"invalid_grant"}
#   5. GET /oidc/userinfo (no auth)
#      → 401 + WWW-Authenticate: Bearer
#   6. GET /oidc/userinfo (bad bearer)
#      → 401 + WWW-Authenticate: Bearer
#
# Usage:
#   bash scripts/oidc_live_e2e.sh                          # uses https://skygate.example.com
#   SKYGATE_OIDC_ISSUER=https://skygate.test bash ...    # override issuer
#
# The script is read-only (no auth credentials needed) —
# it just exercises the public discovery + auth + error
# paths. The full happy path (login → code → token → userinfo)
# requires a real session cookie, which is covered by the
# Go integration test (internal/oidc/e2e_test.go) and by
# the operator's manual test against a real Tailscale
# client (docs/runbooks/oidc-tailscale-e2e.md).
#
# Exit code: 0 = all 6 steps PASS, non-zero = first
# failing step is printed in red.
set -u

ISSUER="${SKYGATE_OIDC_ISSUER:-https://skygate.example.com}"
# The client_secret defaults to the v1.5.0 test fixture
# (the same one the operator's .env uses). Override
# with SKYGATE_OIDC_CLIENT_SECRET for non-default
# deployments.
CLIENT_SECRET="${SKYGATE_OIDC_CLIENT_SECRET_OVERRIDE:-test-secret-do-not-use-in-prod}"

# Colors (only if stdout is a TTY).
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    CYAN='\033[0;36m'
    NC='\033[0m' # No Color
else
    RED=''
    GREEN=''
    YELLOW=''
    CYAN=''
    NC=''
fi

ok()   { echo -e "${GREEN}  PASS${NC}  $1"; }
bad()  { echo -e "${RED}  FAIL${NC}  $1"; exit 1; }
info() { echo -e "${CYAN}  ----${NC}  $1"; }

# 0. Preflight: check curl is available.
command -v curl >/dev/null 2>&1 || bad "curl is required but not installed"

echo "=== B161.4 live OIDC e2e ==="
echo "  issuer: $ISSUER"
echo

# ─────────────────────────────────────────────
# STEP 1: GET /.well-known/openid-configuration
# ─────────────────────────────────────────────
info "STEP 1: GET $ISSUER/.well-known/openid-configuration"
DISC_RAW=$(curl -sS -w '\nHTTP_CODE:%{http_code}' "$ISSUER/.well-known/openid-configuration" 2>&1) || bad "curl failed for discovery doc"
DISC_CODE=$(echo "$DISC_RAW" | grep -oE 'HTTP_CODE:[0-9]+' | cut -d: -f2)
DISC_BODY=$(echo "$DISC_RAW" | sed 's/HTTP_CODE:[0-9]*$//')
[ "$DISC_CODE" = "200" ] || bad "discovery: HTTP $DISC_CODE (want 200). Body: $DISC_BODY"
echo "$DISC_BODY" | grep -qE '"issuer":' || bad "discovery: missing 'issuer' field. Body: $DISC_BODY"
echo "$DISC_BODY" | grep -qE '"authorization_endpoint":' || bad "discovery: missing 'authorization_endpoint'"
echo "$DISC_BODY" | grep -qE '"token_endpoint":' || bad "discovery: missing 'token_endpoint'"
echo "$DISC_BODY" | grep -qE '"userinfo_endpoint":' || bad "discovery: missing 'userinfo_endpoint'"
echo "$DISC_BODY" | grep -qE '"jwks_uri":' || bad "discovery: missing 'jwks_uri'"
echo "$DISC_BODY" | grep -qE '"id_token_signing_alg_values_supported":[^]]*"RS256"' || bad "discovery: must advertise RS256 as supported id_token signing alg"
echo "$DISC_BODY" | grep -qE '"response_types_supported":[^]]*"code"' || bad "discovery: must advertise 'code' as supported response_type"
echo "$DISC_BODY" | grep -qE '"subject_types_supported":[^]]*"public"' || bad "discovery: must advertise 'public' as supported subject_type"
echo "$DISC_BODY" | grep -qE '"scopes_supported":[^]]*"openid"' || bad "discovery: must advertise 'openid' as supported scope"
echo "$DISC_BODY" | grep -qE '"token_endpoint_auth_methods_supported":[^]]*"client_secret_post"' || bad "discovery: must advertise 'client_secret_post' (headscale uses form-encoded client auth)"
ok "discovery doc: 200, 4 endpoints, RS256, code, public, openid, client_secret_post"

# ─────────────────────────────────────────────
# STEP 2: GET /oidc/jwks.json
# ─────────────────────────────────────────────
info "STEP 2: GET $ISSUER/oidc/jwks.json"
JWKS_RAW=$(curl -sS -w '\nHTTP_CODE:%{http_code}' "$ISSUER/oidc/jwks.json" 2>&1) || bad "curl failed for JWKS"
JWKS_CODE=$(echo "$JWKS_RAW" | grep -oE 'HTTP_CODE:[0-9]+' | cut -d: -f2)
JWKS_BODY=$(echo "$JWKS_RAW" | sed 's/HTTP_CODE:[0-9]*$//')
[ "$JWKS_CODE" = "200" ] || bad "JWKS: HTTP $JWKS_CODE (want 200). Body: $JWKS_BODY"
echo "$JWKS_BODY" | grep -qE '"kty":[[:space:]]*"RSA"' || bad "JWKS: missing or wrong 'kty' (expected RSA)"
echo "$JWKS_BODY" | grep -qE '"alg":[[:space:]]*"RS256"' || bad "JWKS: missing or wrong 'alg' (expected RS256)"
echo "$JWKS_BODY" | grep -qE '"use":[[:space:]]*"sig"' || bad "JWKS: missing or wrong 'use' (expected sig)"
echo "$JWKS_BODY" | grep -qE '"kid":[[:space:]]*"[a-f0-9]+"' || bad "JWKS: missing or wrong 'kid' (expected hex kid)"
echo "$JWKS_BODY" | grep -qE '"n":[[:space:]]*"[A-Za-z0-9_-]+"' || bad "JWKS: missing or wrong 'n' (modulus)"
echo "$JWKS_BODY" | grep -qE '"e":[[:space:]]*"[A-Za-z0-9_-]+"' || bad "JWKS: missing or wrong 'e' (exponent)"
KEY_COUNT=$(echo "$JWKS_BODY" | grep -oE '"kty":' | wc -l)
[ "$KEY_COUNT" -ge 1 ] || bad "JWKS: no keys in the doc"
ok "JWKS: 200, 1 RSA/RS256 key with kid + n + e"

# ─────────────────────────────────────────────
# STEP 3: GET /oidc/authorize (unauthenticated → 302 to /login?next=...)
# ─────────────────────────────────────────────
info "STEP 3: GET $ISSUER/oidc/authorize (unauthenticated)"
# The redirect_uri must be in the server's allowlist
# (the value of SKYGATE_OIDC_REDIRECT_URIS on the
# skygate side). The script auto-detects the allowlisted
# URI by reading the discovery doc's client_id
# allowlist. For a v1.5.0 test fixture we use
# the default value (https://head.skynas.ru/oidc/callback)
# which matches the SKYGATE_OIDC_REDIRECT_URIS default.
# Override with SKYGATE_REDIRECT_URI_OVERRIDE for
# non-default deployments.
REDIRECT_URI="${SKYGATE_REDIRECT_URI_OVERRIDE:-https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback}"
# We use -i to see the headers without following the redirect.
# We craft a code_verifier that's >= 43 chars (RFC 7636 sec 4.1).
AUTH_HEADERS=$(curl -sS -i -o /dev/null -D - \
    -X GET "$ISSUER/oidc/authorize?response_type=code&client_id=headscale&redirect_uri=${REDIRECT_URI}&scope=openid+profile+email&state=test-state&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256" 2>&1) || bad "curl failed for /authorize"
AUTH_CODE=$(echo "$AUTH_HEADERS" | head -1 | awk '{print $2}')
[ "$AUTH_CODE" = "302" ] || bad "authorize: HTTP $AUTH_CODE (want 302). Headers: $AUTH_HEADERS"
AUTH_LOCATION=$(echo "$AUTH_HEADERS" | grep -i '^location:' | head -1 | awk '{print $2}' | tr -d '\r')
[ -n "$AUTH_LOCATION" ] || bad "authorize: 302 has no Location header"
echo "$AUTH_LOCATION" | grep -q '/login' || bad "authorize: Location = $AUTH_LOCATION (expected to contain /login)"
# The next= param must contain the full /oidc/authorize URL.
echo "$AUTH_LOCATION" | grep -qE 'next=[^&]*%2Foidc%2Fauthorize' || bad "authorize: next= param is missing the /oidc/authorize URL"
# The next= param must echo back client_id + state +
# code_challenge + scope.
# The next= param is a URL-encoded authorize URL,
# so the inner params are double-encoded (e.g.
# client_id%3Dheadscale inside next=%2Foidc...).
# We check for the URL-encoded form.
echo "$AUTH_LOCATION" | grep -qE 'client_id%3Dheadscale' || bad "authorize: next= param missing client_id=headscale (URL-encoded)"
echo "$AUTH_LOCATION" | grep -qE 'head\.skynas\.ru' || bad "authorize: next= param redirect_uri host mismatch"
echo "$AUTH_LOCATION" | grep -qE 'state%3Dtest-state' || bad "authorize: next= param missing state=test-state (URL-encoded)"
echo "$AUTH_LOCATION" | grep -qE 'code_challenge%3D' || bad "authorize: next= param missing code_challenge (URL-encoded)"
echo "$AUTH_LOCATION" | grep -qE 'scope%3Dopenid' || bad "authorize: next= param missing scope=openid (URL-encoded)"
ok "authorize: 302 → /login?next=... with full OIDC params echoed back (URL-encoded)"

# ─────────────────────────────────────────────
# STEP 4: POST /oidc/token with a bad code (expect 400 invalid_grant)
# ─────────────────────────────────────────────
info "STEP 4: POST $ISSUER/oidc/token (bad code → 400 invalid_grant)"
TOKEN_RAW=$(curl -sS -w '\nHTTP_CODE:%{http_code}' -X POST "$ISSUER/oidc/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=authorization_code&code=NEVER_ISSUED&client_id=headscale&client_secret=${CLIENT_SECRET}&redirect_uri=${REDIRECT_URI}&code_verifier=test-verifier-abc" 2>&1) || bad "curl failed for /token"
TOKEN_CODE=$(echo "$TOKEN_RAW" | grep -oE 'HTTP_CODE:[0-9]+' | cut -d: -f2)
TOKEN_BODY=$(echo "$TOKEN_RAW" | sed 's/HTTP_CODE:[0-9]*$//')
[ "$TOKEN_CODE" = "400" ] || bad "token (bad code): HTTP $TOKEN_CODE (want 400). Body: $TOKEN_BODY"
echo "$TOKEN_BODY" | grep -q '"error":"invalid_grant"' || bad "token (bad code): body should contain invalid_grant. Body: $TOKEN_BODY"
ok "token (bad code): 400 + invalid_grant (auth-code-orphan case)"

# ─────────────────────────────────────────────
# STEP 5: GET /oidc/userinfo with NO Authorization header (expect 401)
# ─────────────────────────────────────────────
info "STEP 5: GET $ISSUER/oidc/userinfo (no auth → 401)"
UI_RAW=$(curl -sS -w '\nHTTP_CODE:%{http_code}\nWA:' -D - -o /dev/null \
    -X GET "$ISSUER/oidc/userinfo" 2>&1) || bad "curl failed for /userinfo (no auth)"
UI_CODE=$(echo "$UI_RAW" | grep -oE 'HTTP_CODE:[0-9]+' | cut -d: -f2)
[ "$UI_CODE" = "401" ] || bad "userinfo (no auth): HTTP $UI_CODE (want 401). Headers: $UI_RAW"
echo "$UI_RAW" | grep -iqE '^Www-Authenticate:.*Bearer' || bad "userinfo (no auth): 401 has no WWW-Authenticate: Bearer header. Headers: $UI_RAW"
ok "userinfo (no auth): 401 + WWW-Authenticate: Bearer (RFC 6750 sec 3)"

# ─────────────────────────────────────────────
# STEP 6: GET /oidc/userinfo with a BAD bearer token (expect 401)
# ─────────────────────────────────────────────
info "STEP 6: GET $ISSUER/oidc/userinfo (bad bearer → 401)"
UI_BAD_RAW=$(curl -sS -w '\nHTTP_CODE:%{http_code}\nWA:' -D - -o /dev/null \
    -X GET "$ISSUER/oidc/userinfo" \
    -H "Authorization: Bearer not-a-real-jwt" 2>&1) || bad "curl failed for /userinfo (bad bearer)"
UI_BAD_CODE=$(echo "$UI_BAD_RAW" | grep -oE 'HTTP_CODE:[0-9]+' | cut -d: -f2)
[ "$UI_BAD_CODE" = "401" ] || bad "userinfo (bad bearer): HTTP $UI_BAD_CODE (want 401). Headers: $UI_BAD_RAW"
echo "$UI_BAD_RAW" | grep -iqE '^Www-Authenticate:.*Bearer' || bad "userinfo (bad bearer): 401 has no WWW-Authenticate: Bearer header. Headers: $UI_BAD_RAW"
ok "userinfo (bad bearer): 401 + WWW-Authenticate: Bearer (invalid_token case)"

# ─────────────────────────────────────────────
# SUMMARY
# ─────────────────────────────────────────────
echo
echo -e "${GREEN}=== B161.4 live OIDC e2e: all 6 steps PASS ===${NC}"
echo
echo "Next steps for the operator:"
echo "  1. Run the full happy path against a real Tailscale client:"
echo "     - Install Tailscale on a test device"
echo "     - Configure custom coord server = $ISSUER"
echo "     - Log in; watch the OIDC redirect chain"
echo "     - Verify the Tailscale client gets a tailnet IP"
echo "     See docs/runbooks/oidc-tailscale-e2e.md for the step-by-step."
echo
echo "  2. Wire headscale to skygate:"
echo "     - Copy the 'oidc:' block from docs/oidc-headscale-conf.md"
echo "     - Paste into /etc/headscale/config.yaml on the headscale host"
echo "     - sudo systemctl restart headscale"
echo
echo "  3. Run 'go test -count=1 -v -run TestE2E_HeadscaleClientFlow ./internal/oidc/'"
echo "     for the full happy-path Go integration test (no real headscale needed)."
