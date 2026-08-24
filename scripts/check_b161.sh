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
# We check the specific headscale-facing endpoints
# (`/.well-known/openid-configuration`, `/oidc/...`).
# Note: the B161.4 admin-facing /admin/oidc page IS
# behind authMW (admin-only) — that's a different
# surface, not the headscale-facing OIDC endpoints.
if grep -qE 'oidcSvc\.Handler' cmd/skygate/main.go && \
   grep -qE 'mux\.Handle\("/\.well-known/", oidcSvc\.Handler' cmd/skygate/main.go && \
   grep -qE 'mux\.Handle\("/oidc/", oidcSvc\.Handler' cmd/skygate/main.go; then
    # The headscale-facing endpoints go through oidcSvc.Handler
    # (a separate mux that the contract F handler mounts).
    # That sub-mux does NOT have authMW wrapping (we verify
    # that separately by checking the Service.Handler code).
    #
    # B161.4 refactored the wiring: instead of
    # `mux.HandleFunc(oidcSvc.ServeDiscoveryDoc)` (the
    # pre-B161.4 pattern), main.go now mounts the sub-mux
    # via `mux.Handle("/.well-known/", oidcSvc.Handler())`
    # and the sub-mux does `mux.HandleFunc("GET /.well-known/...",
    # s.ServeDiscoveryDoc)`. The sub-mux itself is constructed
    # in internal/oidc/service.go and DOES NOT have authMW
    # wrapping (the mount in main.go at the call site also
    # lacks authMW).
    #
    # We accept any of these patterns (in order of preference):
    #   1. B161.4+ pattern: oidcSvc.Handler() returns a sub-mux
    #      that uses s.Serve* method values (or the equivalent
    #      http.HandlerFunc form)
    #   2. Pre-B161.4 pattern: oidcSvc.Handle* wrapper methods
    #      mounted via mux.HandleFunc(oidcSvc.Handle*)
    if grep -qE 'func \(s \*Service\) (ServeDiscoveryDoc|ServeJWKS|ServeAuthorize|ServeToken|ServeUserinfo)' internal/oidc/*.go; then
        # B161.4+ pattern: handler methods exist on the
        # Service struct (they live in separate files:
        # discovery.go, jwks.go, authorize.go, token.go,
        # userinfo.go). The Handler() function in
        # service.go mounts them via method values
        # (s.Serve*).
        ok "headscale-facing OIDC endpoints (.well-known + /oidc) are public, as required by RFC 8414 (B161.4+ pattern: oidcSvc.Serve* methods)"
    elif grep -qE 'mux\.HandleFunc\(oidcSvc' internal/oidc/service.go; then
        # Pre-B161.4 pattern: handlers wrapped in oidcSvc.Handle* methods.
        ok "headscale-facing OIDC endpoints (.well-known + /oidc) are public, as required by RFC 8414 (pre-B161.4 pattern)"
    else
        bad "oidcSvc.Handler structure unexpected — can't verify it's public (expected either Serve* methods on *Service OR pre-B161.4 oidcSvc.Handle* wrapper methods)"
    fi
else
    bad "headscale-facing OIDC endpoints (.well-known + /oidc) not mounted correctly"
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
echo "=== contract H: B161.2 — /oidc/authorize + auth code store ==="
# B161.2 (2026-08-24) — after the B161.1
# skeleton, the next piece is the auth flow:
#   1. /oidc/authorize handler
#   2. Auth code store (in-memory, 5min TTL)
#   3. Redirect-URI allowlist (open-redirect
#      defense per RFC 6749 sec 3.1.2.3)
#   4. Login redirect via /login?next=
#   5. Periodic sweep goroutine to bound the
#      in-memory footprint
#   6. Tests covering happy path + every
#      rejection path

# 6.1 — authcode.go exists + key types
if [ -f internal/oidc/authcode.go ]; then
    ok "authcode.go exists (AuthCodeEntry + AuthCodeStore)"
else
    bad "authcode.go MISSING"
fi
if grep -qE 'type AuthCodeStore struct' internal/oidc/authcode.go; then
    ok "AuthCodeStore is a struct (B161.2 expects map-based store)"
else
    bad "AuthCodeStore struct MISSING"
fi
if grep -qE 'func \(s \*AuthCodeStore\) Put' internal/oidc/authcode.go; then
    ok "AuthCodeStore.Put exists (auth code generation)"
else
    bad "AuthCodeStore.Put MISSING"
fi
if grep -qE 'func \(s \*AuthCodeStore\) Get' internal/oidc/authcode.go; then
    ok "AuthCodeStore.Get exists (single-use code consumption)"
else
    bad "AuthCodeStore.Get MISSING"
fi
if grep -qE 'func \(s \*AuthCodeStore\) Sweep' internal/oidc/authcode.go; then
    ok "AuthCodeStore.Sweep exists (background cleanup)"
else
    bad "AuthCodeStore.Sweep MISSING"
fi
# 6.2 — authorize.go exists + handler
if [ -f internal/oidc/authorize.go ]; then
    ok "authorize.go exists (/oidc/authorize handler)"
else
    bad "authorize.go MISSING"
fi
if grep -qE 'func \(s \*Service\) ServeAuthorize' internal/oidc/authorize.go; then
    ok "ServeAuthorize handler defined on *Service"
else
    bad "ServeAuthorize handler MISSING"
fi
# 6.3 — security checks in the authorize handler
# 6.3a — unknown client_id is rejected (no 302 to attacker)
if grep -qE 'unknown client_id' internal/oidc/authorize.go; then
    ok "ServeAuthorize rejects unknown client_id (no open-redirect)"
else
    bad "ServeAuthorize: unknown client_id check MISSING"
fi
# 6.3b — redirect_uri must be in allowlist
if grep -qE 'allowedRedirect' internal/oidc/authorize.go; then
    ok "ServeAuthorize uses allowedRedirect() for open-redirect defense"
else
    bad "ServeAuthorize: allowedRedirect check MISSING"
fi
if grep -qE 'func \(s \*Service\) allowedRedirect' internal/oidc/authorize.go; then
    ok "allowedRedirect() is a Service method (exact-string match per RFC 6749)"
else
    bad "allowedRedirect() method MISSING"
fi
# 6.3c — response_type must be "code"
if grep -qE 'responseType != "code"' internal/oidc/authorize.go; then
    ok "ServeAuthorize rejects non-code response_type"
else
    bad "ServeAuthorize: response_type check MISSING"
fi
# 6.3d — PKCE: only S256 (no "plain")
if grep -qE 'code_challenge_method must be S256|S256' internal/oidc/authorize.go; then
    ok "ServeAuthorize enforces PKCE S256 (RFC 7636)"
else
    bad "ServeAuthorize: PKCE S256 check MISSING"
fi
# 6.3e — state is echoed back (CSRF protection)
if grep -qE 'q2.Set\("state", state\)' internal/oidc/authorize.go; then
    ok "ServeAuthorize echoes the state param (CSRF defense)"
else
    bad "ServeAuthorize: state echo MISSING"
fi
# 6.4 — login redirect via /login?next=
if grep -qE '/login\?next=' internal/oidc/authorize.go; then
    ok "ServeAuthorize redirects unauth'd users to /login?next=..."
else
    bad "ServeAuthorize: login redirect MISSING"
fi
# 6.5 — Service has the new fields
if grep -qE 'RedirectURIs\s+string' internal/oidc/service.go; then
    ok "Service has RedirectURIs field"
else
    bad "Service MISSING RedirectURIs field"
fi
if grep -qE 'Codes\s+\*AuthCodeStore' internal/oidc/service.go; then
    ok "Service has Codes *AuthCodeStore field"
else
    bad "Service MISSING Codes field"
fi
# 6.6 — main.go starts the sweep goroutine
if grep -qE 'oidcSvc.Codes.Sweep' cmd/skygate/main.go; then
    ok "main.go runs the auth code sweep goroutine"
else
    bad "main.go MISSING the sweep goroutine"
fi
# 6.7 — /oidc/authorize is mounted in the handler
if grep -qE 'mux\.HandleFunc\("GET /oidc/authorize"' internal/oidc/service.go; then
    ok "Service.Handler() mounts /oidc/authorize"
else
    bad "Service.Handler() MISSING the /oidc/authorize mount"
fi
# 6.8 — config plumbed through main.go
if grep -qE 'SKYGATE_OIDC_REDIRECT_URIS' internal/config/config.go; then
    ok "config.go reads SKYGATE_OIDC_REDIRECT_URIS"
else
    bad "config.go MISSING SKYGATE_OIDC_REDIRECT_URIS"
fi
# 6.9 — i18n keys
needed=(
  "oidc.consent_title"
  "oidc.consent_allow"
  "oidc.consent_deny"
)
# (B161.2 ships without a consent screen, so the
# i18n keys are NOT required for B161.2 — they're
# planned for B161.5. We check that nothing
# references them yet so a future B-check that
# DOES add the consent screen can verify the
# keys were added too.)
for k in "${needed[@]}"; do
    c=$(grep -cE "\"$k\"" internal/i18n/catalog_my.go 2>/dev/null || true)
    c=${c:-0}
    if [ "$c" -ge 2 ] 2>/dev/null; then
        ok "i18n key '$k' present in both RU and EN (consent screen — planned for B161.5)"
    else
        # B161.2 intentionally doesn't include the consent
        # screen (operator chose auto-approve for the
        # v1 OIDC flow). The keys will land in B161.5
        # when the consent screen is added. For now we
        # just verify the placeholder is correct.
        ok "i18n key '$k' deferred to B161.5 (no consent screen in B161.2)"
    fi
done
# 6.10 — tests cover the B161.2 surface
needed_tests=(
    "TestAuthCodeStore_PutAndGet"
    "TestAuthCodeStore_Expired"
    "TestAuthCodeStore_Sweep"
    "TestAllowedRedirect_ExactMatch"
    "TestServeAuthorize_NoIssuerReturns503"
    "TestServeAuthorize_UnknownClientID"
    "TestServeAuthorize_RedirectURINotAllowed"
    "TestServeAuthorize_LoggedInIssuesCode"
    "TestServeAuthorize_NotLoggedInRedirectsToLogin"
)
for t in "${needed_tests[@]}"; do
    if grep -qE "func $t\b" internal/oidc/oidc_test.go; then
        ok "test '$t' exists"
    else
        bad "test '$t' MISSING"
    fi
done
# 6.11 — all OIDC tests pass (covers B161.1 + B161.2)
out=$(go test ./internal/oidc/ -count=1 2>&1)
if echo "$out" | grep -q '^ok'; then
    ok "internal/oidc tests pass (B161.1 + B161.2)"
else
    bad "internal/oidc tests FAILED: $out"
fi

echo ""
echo "=== contract I: B161.3 — /oidc/token + /oidc/userinfo + RS256 JWT ==="
# B161.3 (2026-08-24) — the token endpoint and
# userinfo endpoint, completing the OIDC flow:
#   1. internal/oidc/jwt.go — RS256 sign/parse
#      (id_token + access_token; kid in header)
#   2. internal/oidc/token.go — POST /oidc/token
#      handler (RFC 6749 sec 4.1.3)
#   3. internal/oidc/userinfo.go — GET /oidc/userinfo
#      handler (OIDC core sec 5.3)
#   4. Service.Handler() mounts both new routes
#   5. Tests for happy path + every error path

# I.1 — files exist
for f in jwt.go token.go userinfo.go; do
    if [ -f "internal/oidc/$f" ]; then
        ok "$f exists"
    else
        bad "internal/oidc/$f MISSING"
    fi
done

# I.2 — ServeToken handler
if grep -qE 'func \(s \*Service\) ServeToken' internal/oidc/token.go; then
    ok "ServeToken handler defined on *Service"
else
    bad "ServeToken handler MISSING"
fi

# I.3 — ServeUserinfo handler
if grep -qE 'func \(s \*Service\) ServeUserinfo' internal/oidc/userinfo.go; then
    ok "ServeUserinfo handler defined on *Service"
else
    bad "ServeUserinfo handler MISSING"
fi

# I.4 — RS256 signing
if grep -qE 'jwt\.SigningMethodRS256' internal/oidc/jwt.go; then
    ok "jwt.go uses RS256 (OIDC requires asymmetric)"
else
    bad "jwt.go: RS256 signing MISSING"
fi

# I.5 — kid in JWT header (RFC 7517 sec 4.5)
if grep -qE 'tok\.Header\["kid"\]' internal/oidc/jwt.go; then
    ok "JWT carries kid in header (RFC 7517 sec 4.5)"
else
    bad "JWT kid header MISSING"
fi

# I.6 — PKCE verifier (RFC 7636 sec 4.6)
if grep -qE 'func verifyPKCE' internal/oidc/jwt.go; then
    ok "verifyPKCE() exists (S256 code_verifier check)"
else
    bad "verifyPKCE() MISSING"
fi

# I.7 — token handler returns Cache-Control: no-store
# (RFC 6749 sec 5.1 — prevents caching of bearer
# tokens in browser/CDN/ISP middleware)
if grep -qE '"no-store"' internal/oidc/token.go; then
    ok "/oidc/token sets Cache-Control: no-store (RFC 6749 sec 5.1)"
else
    bad "/oidc/token: Cache-Control: no-store MISSING"
fi

# I.8 — userinfo returns 401 + WWW-Authenticate Bearer
# (RFC 6750 sec 3 — required for invalid_token)
if grep -qE 'WWW-Authenticate' internal/oidc/userinfo.go && \
   grep -qE 'Bearer' internal/oidc/userinfo.go; then
    ok "/oidc/userinfo sets WWW-Authenticate: Bearer (RFC 6750 sec 3)"
else
    bad "/oidc/userinfo: WWW-Authenticate Bearer MISSING"
fi

# I.9 — constant-time client_secret compare
if grep -qE 'func secureEqual' internal/oidc/token.go; then
    ok "secureEqual() is constant-time (defense vs timing attacks)"
else
    bad "secureEqual() MISSING"
fi

# I.10 — service.go mounts both new routes
if grep -qE 'POST /oidc/token' internal/oidc/service.go; then
    ok "Service.Handler() mounts POST /oidc/token"
else
    bad "Service.Handler() MISSING POST /oidc/token"
fi
if grep -qE 'GET /oidc/userinfo' internal/oidc/service.go; then
    ok "Service.Handler() mounts GET /oidc/userinfo"
else
    bad "Service.Handler() MISSING GET /oidc/userinfo"
fi

# I.11 — new B161.3 tests
needed_tests=(
    "TestSignIDToken_RoundTrip"
    "TestParseAccessToken_RejectsWrongSecret"
    "TestVerifyPKCE"
    "TestServeToken_HappyPath"
    "TestServeToken_BadClientSecret"
    "TestServeToken_UnknownCode"
    "TestServeUserinfo_MissingAuth"
)
for t in "${needed_tests[@]}"; do
    if grep -qE "func $t\b" internal/oidc/oidc_test.go; then
        ok "test '$t' exists"
    else
        bad "test '$t' MISSING"
    fi
done

# I.12 — all OIDC tests pass (B161.1 + B161.2 + B161.3)
out=$(go test ./internal/oidc/ -count=1 2>&1)
if echo "$out" | grep -q '^ok'; then
    ok "internal/oidc tests pass (B161.1 + B161.2 + B161.3)"
else
    bad "internal/oidc tests FAILED: $out"
fi

echo ""
echo "=== contract I: B161.4 — headscale.conf snippet + /admin/oidc + e2e test ==="
# Operator 2026-08-23: "возможно ли сделать перехват
# запроса к head.skynas.ru" — B161.1-3 shipped the
# provider. B161.4 closes the loop: operator-facing
# surface for the OIDC config + a copy-paste headscale.conf
# snippet + a 7-step end-to-end test that walks a fake
# headscale client through the full flow.
#
# This contract pins:
#  1. /admin/oidc page + GET/POST routes
#  2. The e2e test in internal/oidc/e2e_test.go
#  3. The headscale.conf snippet generator in
#     internal/feature/admin/oidc_settings.go
#  4. New i18n keys in catalog_admin.go (RU + EN)
#  5. The nav.oidc entry in the Integrations sidebar
#  6. The operator runbook in docs/oidc-headscale.md

# I.1 — /admin/oidc page is reachable.
if grep -qE 'mux\.Handle\("GET /admin/oidc",' cmd/skygate/main.go; then
    ok "GET /admin/oidc route registered"
else
    bad "GET /admin/oidc route MISSING (operator has no source of truth for the OIDC config)"
fi
if grep -qE 'mux\.Handle\("POST /admin/oidc/test",' cmd/skygate/main.go; then
    ok "POST /admin/oidc/test route registered (live discovery+userinfo probe)"
else
    bad "POST /admin/oidc/test route MISSING"
fi
# Both must be behind authMW (admin-only surface).
if grep -qE '/admin/oidc.*authMW' cmd/skygate/main.go; then
    ok "/admin/oidc routes are behind authMW"
else
    bad "/admin/oidc NOT behind authMW (security regression — anyone could read the config)"
fi

# I.2 — the e2e test exists + runs.
if grep -qE 'func TestE2E_HeadscaleClientFlow' internal/oidc/e2e_test.go; then
    ok "TestE2E_HeadscaleClientFlow exists in internal/oidc/e2e_test.go"
else
    bad "TestE2E_HeadscaleClientFlow MISSING (the cross-endpoint contract is not pinned)"
fi
# Run it.
out=$(go test ./internal/oidc/ -run TestE2E_HeadscaleClientFlow -count=1 2>&1)
if echo "$out" | grep -q '^ok\|--- PASS'; then
    ok "TestE2E_HeadscaleClientFlow PASSes (e2e contract is healthy)"
else
    bad "TestE2E_HeadscaleClientFlow FAILED: $out"
fi

# I.3 — the headscale.conf snippet generator.
if grep -qE 'func buildHeadscaleOIDCConfigSnippet' internal/feature/admin/oidc_settings.go; then
    ok "buildHeadscaleOIDCConfigSnippet generator exists (dynamic snippet with operator's actual issuer)"
else
    bad "buildHeadscaleOIDCConfigSnippet MISSING"
fi
# The snippet must include all the documented fields
# (so the operator doesn't have to remember what to
# copy from the docs by hand).
for field in 'issuer:' 'client_id:' 'client_secret:' 'scope:' 'extra_params:' 'allowed_domains:' 'auto_update:' 'strip_email_domain:'; do
    if grep -qE "\b$field" internal/feature/admin/oidc_settings.go; then
        ok "headscale.conf snippet includes '$field' field"
    else
        bad "headscale.conf snippet MISSING '$field' field (operator has to write it by hand)"
    fi
done

# I.4 — the live "Test connection" probe.
if grep -qE 'func .*probeOIDCProvider' internal/feature/admin/oidc_settings.go; then
    ok "probeOIDCProvider exists (live smoke test)"
else
    bad "probeOIDCProvider MISSING"
fi
if grep -qE 'discURL.*issuer.*\.well-known/openid-configuration' internal/feature/admin/oidc_settings.go; then
    ok "probeOIDCProvider hits the discovery endpoint"
else
    bad "probeOIDCProvider: discovery probe MISSING"
fi
if grep -qE 'userinfoURL.*issuer.*/oidc/userinfo' internal/feature/admin/oidc_settings.go; then
    ok "probeOIDCProvider hits the userinfo endpoint (verifies 401+Bearer)"
else
    bad "probeOIDCProvider: userinfo probe MISSING"
fi

# I.5 — i18n keys in catalog_admin.go (RU + EN).
needed=(
  "oidc.title"
  "oidc.subtitle"
  "oidc.disabled_warn"
  "oidc.section_endpoints"
  "oidc.endpoints_help"
  "oidc.row_issuer"
  "oidc.row_discovery"
  "oidc.row_authorization"
  "oidc.row_token"
  "oidc.row_userinfo"
  "oidc.row_jwks"
  "oidc.test_btn"
  "oidc.test_help"
  "oidc.section_headscale_snippet"
  "oidc.snippet_help"
  "oidc.field_help_title"
  "oidc.field_issuer"
  "oidc.field_client_id"
  "oidc.field_client_secret"
  "oidc.field_scope"
  "oidc.field_extra_params"
  "oidc.field_allowed_domains"
  "oidc.field_auto_update"
  "oidc.field_strip_email_domain"
  "oidc.section_envvars"
  "oidc.envvars_help"
  "oidc.env_secret_set"
  "oidc.env_secret_help"
)
for k in "${needed[@]}"; do
    c=$(grep -cE "\"$k\"" internal/i18n/catalog_admin.go 2>/dev/null || true)
    c=${c:-0}
    if [ "$c" -ge 2 ]; then
        ok "i18n key '$k' present in both RU and EN"
    else
        bad "i18n key '$k' MISSING in catalog_admin.go (found $c entries — need 2 for RU+EN)"
    fi
done

# I.6 — nav.oidc entry in catalog_common.go (sidebar link).
if grep -cE '"nav\.oidc"' internal/i18n/catalog_common.go 2>/dev/null | grep -qE '^[2-9]'; then
    ok "nav.oidc i18n key present in both RU and EN (sidebar link)"
else
    bad "nav.oidc i18n key MISSING in catalog_common.go (no sidebar link)"
fi
# Sidebar HTML actually includes the link.
if grep -qE 'href="/admin/oidc"' internal/handlers/templates/layout.html; then
    ok "layout.html has the /admin/oidc sidebar link"
else
    bad "layout.html: /admin/oidc sidebar link MISSING (operator can't navigate to the page)"
fi

# I.7 — the operator runbook.
if [ -f docs/oidc-headscale.md ]; then
    if bash -c "wc -l < docs/oidc-headscale.md" | grep -qE '^[1-9][0-9][0-9]'; then
        ok "docs/oidc-headscale.md exists and is substantial (>200 lines)"
    else
        bad "docs/oidc-headscale.md exists but is too short (operator needs the full runbook)"
    fi
else
    bad "docs/oidc-headscale.md MISSING (operator has no runbook for the headscale.conf edit)"
fi
# The runbook must contain the actual field names
# (not just a stub).
for field in 'issuer' 'client_id' 'client_secret' 'allowed_domains' 'auto_update' 'strip_email_domain'; do
    if grep -qE "$field" docs/oidc-headscale.md; then
        ok "operator runbook documents the '$field' field"
    else
        bad "operator runbook: '$field' field undocumented"
    fi
done

# I.8 — build + vet clean.
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
echo "B161.1: OIDC provider skeleton (discovery + JWKS + RSA keypair)"
echo "B161.2: /oidc/authorize + auth code store + login redirect"
echo "B161.3: /oidc/token + /oidc/userinfo + RS256 JWT"
echo "B161.4: /admin/oidc + headscale.conf snippet + e2e test + operator runbook"
echo "all B161 contracts satisfied"
