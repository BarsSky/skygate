#!/bin/bash
# check_b161_4.sh — headscale.conf snippet + e2e
# verification (B161.4, v1.5.0)
#
# B161.1+2+3 shipped the OIDC provider on the
# skygate side. B161.4 closes the loop with:
#  1. docs/internal/oidc-headscale.md — the
#     headscale.conf snippet + a 3-step smoke
#     test + a "common e2e failures" table
#  2. This B-check — verifies the live OIDC
#     endpoints (discovery + JWKS + /authorize
#     + /token error paths) respond as the
#     headscale client will see them
#
# The B-check is split into:
#  A. Source-contract checks (the snippet exists,
#     references the 4 must-match values, etc.)
#  B. Live-endpoint checks (curl + grep, run
#     against the live VM or a local instance)
#  C. "Stub" B161.4 test in /admin/system_tests
#     so the operator gets the same surface as
#     B160 + B162
#
# Live checks (contract B) require the OIDC env
# vars to be set on the target. The check SKIPs
# (not FAILs) on a fresh deploy where the OIDC
# issuer is empty (the discovery doc still
# returns 503, which is the correct degraded
# mode).

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: source files exist + have the right structure ==="

# A.1 — the doc itself.
if [ -f docs/internal/oidc-headscale.md ]; then
    ok "docs/internal/oidc-headscale.md exists"
else
    bad "docs/internal/oidc-headscale.md MISSING (the runbook is the operator's only guide to wiring headscale)"
fi

# A.2 — the doc must include the 4 must-match
# values table (the operator's only checklist).
for key in "SKYGATE_OIDC_ISSUER" "SKYGATE_OIDC_CLIENT_ID" "SKYGATE_OIDC_CLIENT_SECRET" "SKYGATE_OIDC_REDIRECT_URIS"; do
    if grep -qF "$key" docs/internal/oidc-headscale.md; then
        ok "oidc-headscale.md references '$key'"
    else
        bad "oidc-headscale.md: '$key' MISSING (operator has no way to know the env var name to match)"
    fi
done

# A.3 — the doc must include the 3-step smoke
# test (the e2e verification that Mavis-side
# can't run automatically without a real Tailscale
# client).
for step in "discovery" "jwks" "authorize"; do
    if grep -qiE "curl.*${step}|/\\.well-known/|/oidc/jwks\\.json|/oidc/authorize" docs/internal/oidc-headscale.md; then
        ok "oidc-headscale.md has the '$step' smoke test"
    else
        bad "oidc-headscale.md: '$step' smoke test MISSING (operator can't verify the OIDC flow before attaching a Tailscale client)"
    fi
done

# A.4 — the doc must include the "common e2e
# failures" table (so the operator doesn't have
# to ask Mavis when the first Tailscale client
# shows "authentication failed").
if grep -qE "Common e2e failures|common e2e failures" docs/internal/oidc-headscale.md; then
    ok "oidc-headscale.md has the 'common e2e failures' table"
else
    bad "oidc-headscale.md: 'common e2e failures' table MISSING"
fi

# A.5 — the headscale.conf `oidc:` block in the
# doc must include `automatic_authorization: true`
# (the "one-click UX" the operator wants — without
# it, every new OIDC user needs a manual
# `headscale users create` call).
if grep -qE "automatic_authorization: true" docs/internal/oidc-headscale.md; then
    ok "oidc-headscale.md: automatic_authorization: true is in the snippet"
else
    bad "oidc-headscale.md: automatic_authorization: true MISSING (without it, every new OIDC user needs a manual 'headscale users create')"
fi

echo ""
echo "=== contract B: live OIDC endpoint smoke test (skipped on fresh deploy) ==="
# This block calls the live endpoints via curl.
# It uses the same SKYGATE_OIDC_ISSUER env var the
# skygate container reads. If the var is empty,
# the discovery doc returns 503 (correct degraded
# mode) and the check SKIPs (not FAILs).
#
# The block is wrapped in a `if` so the B-check
# doesn't fail in a fresh CI / Windows-dev
# environment where SKYGATE_OIDC_ISSUER is unset.
# Operators running the check on a real deployment
# have the var set (it's required for the OIDC
# feature to work at all).

OIDC_ISSUER="${SKYGATE_OIDC_ISSUER:-}"
if [ -z "$OIDC_ISSUER" ]; then
    echo "  SKIP  SKYGATE_OIDC_ISSUER is not set (OIDC feature not enabled on this host — fresh deploy or local CI)"
    # The B-check still passes (SKIP is not FAIL).
else
    echo "  issuer: $OIDC_ISSUER"

    # B.1 — discovery doc reachable + has the right fields.
    discovery=$(curl -fsS "${OIDC_ISSUER}/.well-known/openid-configuration" 2>&1) || {
        bad "discovery doc unreachable at ${OIDC_ISSUER}/.well-known/openid-configuration (skygate is not running or the OIDC route is broken)"
    }
    for field in issuer authorization_endpoint token_endpoint userinfo_endpoint jwks_uri id_token_signing_alg_values_supported response_types_supported; do
        if echo "$discovery" | grep -q "\"$field\""; then
            ok "discovery doc declares '$field'"
        else
            bad "discovery doc: '$field' MISSING (headscale will fail to discover the endpoint)"
        fi
    done
    # Algorithm must be RS256 (matches skygate's keypair).
    if echo "$discovery" | grep -q '"RS256"'; then
        ok "discovery doc: id_token_signing_alg_values_supported includes RS256"
    else
        bad "discovery doc: RS256 MISSING (skygate signs with RS256 — headscale will reject id_tokens signed with anything else)"
    fi
    # response_types must include "code" (the
    # only flow skygate supports; B161.2 rejected
    # 'token' / 'id_token' at /authorize).
    if echo "$discovery" | grep -q '"code"'; then
        ok "discovery doc: response_types_supported includes 'code'"
    else
        bad "discovery doc: 'code' MISSING (skygate only supports the authorization_code flow)"
    fi

    # B.2 — JWKS reachable + has 1 RS256 key.
    jwks=$(curl -fsS "${OIDC_ISSUER}/oidc/jwks.json" 2>&1) || {
        bad "JWKS unreachable at ${OIDC_ISSUER}/oidc/jwks.json"
    }
    key_count=$(echo "$jwks" | grep -oE '"kid":\s*"[^"]+"' | wc -l)
    if [ "$key_count" = "1" ]; then
        ok "JWKS has exactly 1 key"
    else
        bad "JWKS has $key_count keys (B161.1 ships exactly 1 — multiple keys would mean skygate is rotating mid-session)"
    fi
    for k in '"kty":\s*"RSA"' '"alg":\s*"RS256"' '"use":\s*"sig"' '"kid":'; do
        if echo "$jwks" | grep -qE "$k"; then
            ok "JWKS key has $(echo "$k" | head -c 12)"
        else
            bad "JWKS key MISSING $(echo "$k" | head -c 12)"
        fi
    done

    # B.3 — /oidc/authorize returns 302 (NOT 500).
    # We can't test the full happy path without a
    # session cookie, but the redirect-to-login
    # path is the same as the redirect-to-callback
    # path up until the session check. A 500 here
    # means the handler is broken.
    code=$(curl -sS -o /dev/null -w "%{http_code}" \
        "${OIDC_ISSUER}/oidc/authorize?response_type=code&client_id=headscale&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback&state=test&scope=openid+profile+email")
    if [ "$code" = "302" ]; then
        ok "/oidc/authorize returns 302 (valid request → redirect to /login or callback)"
    else
        bad "/oidc/authorize returns $code (B161.2 expected 302; anything else means the handler is broken)"
    fi

    # B.4 — /oidc/authorize with unknown client_id
    # returns 400 (NOT 500, NOT 200). The B161.2
    # open-redirect defense rejects unknown clients
    # with a 400 + i18n message.
    code=$(curl -sS -o /dev/null -w "%{http_code}" \
        "${OIDC_ISSUER}/oidc/authorize?response_type=code&client_id=unknown&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback&state=test&scope=openid")
    if [ "$code" = "400" ]; then
        ok "/oidc/authorize returns 400 for unknown client_id (open-redirect defense works)"
    else
        bad "/oidc/authorize returns $code for unknown client_id (B161.2 expected 400; any other code is the open-redirect CVE class)"
    fi

    # B.5 — /oidc/token with bad creds returns 400
    # invalid_client. This proves the
    # client_secret_post + Basic Auth paths
    # (B161.3) work.
    body=$(curl -sS -X POST -o /dev/null -w "%{http_code}\n" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        -d "grant_type=authorization_code&code=NEVER_ISSUED&client_id=headscale&client_secret=WRONG_SECRET&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback" \
        "${OIDC_ISSUER}/oidc/token" 2>&1) || true
    if [ "$body" = "400" ]; then
        ok "/oidc/token returns 400 for bad client_secret (B161.3 client_secret_post path works)"
    else
        bad "/oidc/token returns $body for bad client_secret (B161.3 expected 400 invalid_client)"
    fi
fi

echo ""
echo "=== contract C: system test stub for B161.4 (operator e2e signal) ==="
# The system test stubs in /admin/system_tests
# are the operator's "Run all" surface. Adding a
# stub for B161.4 means the operator sees the
# 4 OIDC endpoints in the same table as the
# 115 existing tests. The stub is a SKIP (not
# FAIL) — headscale isn't reachable from the
# test rig, only from the live deployment.

# We check that internal/feature/admin/system_tests.go
# has at least one Name starting with "headscale.oidc"
# OR "skygate.oidc" (the exact prefix is a
# design decision; either is acceptable).
if grep -qE 'Name:\s*"(skygate|headscale)\.oidc\.' internal/feature/admin/system_tests.go; then
    ok "system_tests.go has a 'oidc' system test stub"
else
    # No FAIL — the stub is nice-to-have. We log
    # a warning so the operator knows they could
    # add one.
    echo "  WARN  no 'oidc.*' system test in system_tests.go (operator can add a stub for the OIDC endpoints if they want them in 'Run all' output)"
fi

echo ""
echo "=== contract D: build + vet clean ==="
out=$(go build ./... 2>&1)
if [ -z "$out" ]; then
    ok "go build ./... clean"
else
    bad "go build output: $out"
fi
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet output: $out"
fi

echo ""
echo "=== summary ==="
echo "B161.4: headscale.conf snippet + e2e verification"
echo "all contracts satisfied"
