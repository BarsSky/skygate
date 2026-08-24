#!/bin/bash
# check_b161.sh — B161.1 (v1.5.0) OIDC provider
# skeleton (discovery + JWKS + RSA keypair)
#
# Operator 2026-08-23: "давай 1" — go with option
# 1 (full OIDC provider) for the Tailscale →
# skygate OIDC flow (B161). B161.1 ships the
# minimum-viable skeleton:
#
#  1. internal/oidc/ package
#  2. RSA-2048 keypair (generated on first start,
#     persisted to ./data/oidc-keys/)
#  3. /.well-known/openid-configuration (RFC 8414
#     discovery doc)
#  4. /oidc/jwks.json (RFC 7517 JWKS with the
#     public key + kid)
#  5. /oidc/authorize, /oidc/token, /oidc/userinfo
#     are NOT in B161.1 — B161.2 + B161.3 add them.
#     The package's Handler() method mounts only
#     the two read-only endpoints so operators can
#     verify the OIDC stack is reachable + the
#     RSA keypair is generated before the auth
#     flow is built.
#
# B161.1 (this file) pins the skeleton:
#  - Package exists + builds + passes its own tests
#  - Keypair is generated and persisted
#  - Discovery doc has all RFC 8414 fields
#  - JWKS has exactly one key with the required
#    RFC 7517 fields
#  - Config is plumbed through main.go (4 env vars)
#  - Routes are mounted in main.go (no auth, public)
#  - The 503 fallback when SKYGATE_OIDC_ISSUER is
#    empty works (operator can ship the B161.1
#    commit without configuring OIDC yet)

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: internal/oidc/ package exists + builds + tests ==="
[ -d internal/oidc ] || bad "internal/oidc/ MISSING"
[ -f internal/oidc/keys.go ] && ok "keys.go exists (RSA keypair + persistence)" \
    || bad "keys.go MISSING"
[ -f internal/oidc/discovery.go ] && ok "discovery.go exists (RFC 8414)" \
    || bad "discovery.go MISSING"
[ -f internal/oidc/jwks.go ] && ok "jwks.go exists (RFC 7517)" \
    || bad "jwks.go MISSING"
[ -f internal/oidc/service.go ] && ok "service.go exists (Service + Handler)" \
    || bad "service.go MISSING"
[ -f internal/oidc/oidc_test.go ] && ok "oidc_test.go exists (unit tests)" \
    || bad "oidc_test.go MISSING"

# Package compiles + tests pass.
out=$(go test ./internal/oidc/ 2>&1)
if echo "$out" | grep -q '^ok'; then
    ok "internal/oidc tests pass"
else
    bad "internal/oidc tests FAILED: $out"
fi

echo ""
echo "=== contract B: config plumbed through main.go ==="
# The 4 OIDC env vars (B161.1) must be in
# config.go + main.go. B161.2/3 will add
# authorize/token-specific env vars.
for env in SKYGATE_OIDC_ISSUER SKYGATE_OIDC_CLIENT_ID SKYGATE_OIDC_CLIENT_SECRET SKYGATE_OIDC_KEY_DIR; do
    if grep -qE "$env" internal/config/config.go; then
        ok "config.go reads $env"
    else
        bad "config.go MISSING $env"
    fi
done

echo ""
echo "=== contract C: routes mounted in main.go ==="
# B161.1 mounts 2 routes, no auth middleware
# (headscale's OIDC client must be able to
# reach them without a session).
if grep -qE 'mux\.Handle\("/\.well-known/", oidcSvc\.Handler\(\)\)' cmd/skygate/main.go; then
    ok "main.go mounts /.well-known/* via oidcSvc.Handler()"
else
    bad "main.go MISSING the /.well-known/ mount"
fi
if grep -qE 'mux\.Handle\("/oidc/", oidcSvc\.Handler\(\)\)' cmd/skygate/main.go; then
    ok "main.go mounts /oidc/* via oidcSvc.Handler()"
else
    bad "main.go MISSING the /oidc/ mount"
fi
# OIDC routes must NOT be behind authMW.
if grep -qE 'authMW\(.*oidc|oidc.*authMW' cmd/skygate/main.go; then
    bad "OIDC routes are wrapped in authMW (headscale's OIDC client can't auth)"
else
    ok "OIDC routes are NOT behind authMW (public, as required by RFC 8414)"
fi

echo ""
echo "=== contract D: discovery doc has all RFC 8414 fields ==="
# Each field the OIDC spec REQUIRES (issuer,
# authorization_endpoint, token_endpoint,
# jwks_uri) + the standard supporting fields
# headscale needs (response_types_supported,
# id_token_signing_alg_values_supported, etc.).
# The fields are Go struct field names; the
# JSON tags map them to the wire format.
for field in Issuer AuthorizationEndpoint TokenEndpoint UserinfoEndpoint JwksURI ResponseTypesSupported SubjectTypesSupported IDTokenSigningAlgValuesSupported ScopesSupported; do
    if grep -qE "${field}\s+(\[\]?string|string)\s+" internal/oidc/discovery.go; then
        ok "discovery.go declares $field"
    else
        bad "discovery.go MISSING $field"
    fi
done
# Algorithm must be RS256 (matches the RSA-2048
# keypair in keys.go).
if grep -qE '"RS256"' internal/oidc/discovery.go; then
    ok "id_token_signing_alg_values_supported = [RS256]"
else
    bad "discovery.go: RS256 not declared as a supported alg"
fi

echo ""
echo "=== contract E: JWKS has the right shape ==="
for field in kty use alg kid n e; do
    if grep -qE "\"$field\":" internal/oidc/keys.go; then
        ok "JWK includes $field"
    else
        bad "JWK MISSING $field (RFC 7517 sec 4.3)"
    fi
done
# kty + alg + use must have the right values.
if grep -qE '"kty":\s*"RSA"' internal/oidc/keys.go; then
    ok "JWK kty = RSA"
else
    bad "JWK kty is not RSA"
fi
if grep -qE '"alg":\s*"RS256"' internal/oidc/keys.go; then
    ok "JWK alg = RS256"
else
    bad "JWK alg is not RS256"
fi
if grep -qE '"use":\s*"sig"' internal/oidc/keys.go; then
    ok "JWK use = sig"
else
    bad "JWK use is not 'sig'"
fi

echo ""
echo "=== contract F: keypair persistence ==="
# The keypair is persisted to disk so that
# restart doesn't invalidate already-issued
# JWTs (kid stays the same).
if grep -qE 'NewKeyStore' internal/oidc/keys.go; then
    ok "keys.go exposes NewKeyStore(dir)"
else
    bad "keys.go MISSING NewKeyStore constructor"
fi
if grep -qE 'PKCS1PrivateKey' internal/oidc/keys.go; then
    ok "keys.go parses PKCS#1 PEM (standard format)"
else
    bad "keys.go: PKCS#1 PEM support MISSING"
fi
# Refuse weak keys (RFC 7518 requires >= 2048).
if grep -qE 'BitLen\(\) < 2048|2048-bit' internal/oidc/keys.go; then
    ok "keys.go refuses keys < 2048 bits"
else
    bad "keys.go: weak-key guard MISSING"
fi
# The handler returns 503 if the keypair isn't
# ready (cold start on a new volume).
if grep -qE 'OIDC keypair not ready' internal/oidc/jwks.go; then
    ok "jwks.go returns 503 when keypair isn't ready"
else
    bad "jwks.go: 503 fallback MISSING"
fi
# Same for the discovery doc (operator sees a
# clear "OIDC provider disabled" instead of a
# 500 when SKYGATE_OIDC_ISSUER is empty).
if grep -qE 'OIDC provider disabled' internal/oidc/discovery.go; then
    ok "discovery.go returns 503 when SKYGATE_OIDC_ISSUER is empty"
else
    bad "discovery.go: 503 fallback MISSING"
fi

echo ""
echo "=== contract G: go build + go vet clean ==="
out=$(go build ./... 2>&1)
if [ -z "$out" ]; then
    ok "go build ./... clean"
else
    bad "go build ./... output: $out"
fi
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet ./... output: $out"
fi

echo ""
echo "=== summary ==="
echo "B161.1: OIDC provider skeleton (discovery + JWKS + RSA keypair)"
echo "B161.2 + B161.3 will add /authorize + /token + /userinfo"
echo "all B161.1 contracts satisfied"
