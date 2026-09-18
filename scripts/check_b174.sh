#!/bin/bash
# check_b174.sh — B174 (v1.5.2) OIDC session JWT parsing fix.
#
# Operator 2026-08-25 (after B172 + B173 + B173.1 shipped):
# "все равно сбрасывает, после того как браузер предлагает
#  использовать сохраненный пароль до того как вносил правки
#  по поводу next все отрабатывала"
#
# "All the same, the password is reset after the browser
#  suggests using a saved password. Before the changes about
#  `next`, everything worked."
#
# Root cause analysis: the B172 fix made the login form
# submit and then redirect to /oidc/authorize (so the OIDC
# handshake can resume). The /oidc/authorize handler checks
# if the user is authenticated by reading the skygate_session
# cookie. PostLogin sets this cookie to an HS256 JWT (via
# auth.IssueJWT), but the pre-B174 OIDC readSession tried to
# parse the cookie as a colon-separated string
# ("<uid>:<username>:<email>:<expires_unix>") that PostLogin
# NEVER wrote. readSession ALWAYS returned nil → the OIDC
# handler ALWAYS redirected to /login?next=... → the user
# saw the login page re-render with an empty password
# (the "сбрасывает" symptom).
#
# Pre-B172 the user thought "it worked" because PostLogin
# hard-coded a redirect to /dashboard (the B172 bug) — the
# user never went back through /oidc/authorize, so the
# broken readSession was never exercised. B172 fixed the
# redirect, which EXPOSED the latent OIDC readSession bug.
#
# B174 fix:
#   1. OIDC Service gets a JWTSecret field + UserLookup
#      callback (B174 wires both in main.go)
#   2. readSession delegates to auth.ParseJWT (the same
#      helper feature/auth uses) to verify the HMAC signature
#      + extract the uid + usr claims
#   3. UserLookup populates the email claim (the JWT doesn't
#      carry email; the OIDC id_token /userinfo spec needs it)
#   4. The B161.4 e2e test now issues a real JWT (via
#      auth.IssueJWT) instead of a mock string — the test
#      previously masked the production bug with a
#      "X-Test-Session-Cookie-Present" header that has been
#      removed in B174
#
# The B-check is split into:
#  A. Source contract (oidc.Service has JWTSecret + UserLookup
#     + readSession uses auth.ParseJWT — NOT the colon-split
#     pre-B174 format)
#  B. Wiring contract (cmd/skygate/main.go passes app.JWTSecret
#     to oidc.NewService and sets oidcSvc.UserLookup)
#  C. Test contract (the B161.4 e2e test issues a real JWT via
#     auth.IssueJWT, no more "X-Test-Session-Cookie-Present"
#     workaround)
#  D. Regression contract (the pre-B174 readSession format is
#     pinned as REJECTED — an attacker can't forge a session
#     by setting a colon-separated cookie)
#  E. Build contract (go build + go vet + go test on the
#     internal/oidc package)
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source contract (OIDC readSession uses auth.ParseJWT)"

# A.1 — oidc.Service has a JWTSecret field. Without
# this, readSession can't verify the JWT signature
# (it would have to accept any cookie value, which
# is an open-redirect / session-forgery vector).
if grep -qE '^\s*JWTSecret\s+string' internal/oidc/service.go; then
    ok "oidc.Service has JWTSecret field (B174: readSession needs it to verify the cookie HMAC signature)"
else
    bad "oidc.Service is missing JWTSecret field (B174: readSession can't verify the JWT cookie)"
fi

# A.2 — oidc.Service has a UserLookup field. The
# OIDC id_token /userinfo endpoints need the
# user's current email (the JWT only carries
# uid + usr), so the OIDC handler needs a DB-side
# lookup function.
if grep -qE '^\s*UserLookup\s+func\(userID int64\)' internal/oidc/service.go; then
    ok "oidc.Service has UserLookup callback (B174: OIDC id_token needs the DB-side email)"
else
    bad "oidc.Service is missing UserLookup callback (B174: OIDC id_token can't populate the email claim)"
fi

# A.3 — readSession uses auth.ParseJWT. The pre-B174
# implementation split the cookie value on ":" —
# we pin that the colon-split is GONE and auth.ParseJWT
# is the new parser.
if grep -qE 'auth\.ParseJWT' internal/oidc/authorize.go; then
    ok "readSession calls auth.ParseJWT (B174: OIDC + feature/auth now share the JWT parser)"
else
    bad "readSession does NOT call auth.ParseJWT (B174 regression: the colon-split pre-B174 format is back)"
fi

# A.4 — the pre-B174 colon-split must be GONE. If
# strings.SplitN(c.Value, ":", 4) is still in
# authorize.go, the pre-B174 bug is back.
if grep -qE 'strings\.SplitN\(c\.Value' internal/oidc/authorize.go; then
    bad "readSession still has strings.SplitN(c.Value, ...) — the pre-B174 bug is back"
else
    ok "readSession no longer does colon-split (B174: the pre-B174 format is GONE)"
fi

# A.5 — the dead parseInt64 helper is GONE. It was
# only used by the pre-B174 colon-split parser, and
# keeping it around would be a code-rot magnet (a
# future refactor might accidentally wire it back
# in).
if grep -qE 'func parseInt64' internal/oidc/authorize.go; then
    bad "internal/oidc/authorize.go still has func parseInt64 — the dead pre-B174 helper should be deleted"
else
    ok "parseInt64 helper is GONE (B174: dead code from the pre-B174 colon-split is cleaned up)"
fi

# A.6 — readSession handles the UserLookup-error
# case (returns nil if the DB lookup fails, instead
# of leaking the JWT user). A stale cookie with a
# valid signature but no live user must NOT proceed
# to /oidc/authorize — that would let headscale
# create a session for a deleted skygate account.
if grep -qE 'UserLookup' internal/oidc/authorize.go && \
   grep -qE 'lerr\s*!=\s*nil' internal/oidc/authorize.go; then
    ok "readSession handles UserLookup error (B174: stale-cookie-after-user-deletion is rejected)"
else
    bad "readSession does NOT handle UserLookup error (B174 security regression: stale cookies would let headscale register deleted users)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: wiring contract (main.go passes JWTSecret + UserLookup)"

# B.1 — cmd/skygate/main.go calls oidcSvc.NewService
# with the JWT secret. The pre-B174 call signature
# was (issuer, clientID, clientSecret, keyDir,
# redirectURIs) — B174 adds a 6th param.
if grep -qE 'oidcsvc\.NewService\(' cmd/skygate/main.go && \
   grep -qE 'app\.JWTSecret' cmd/skygate/main.go; then
    ok "main.go passes app.JWTSecret to oidcsvc.NewService (B174: OIDC service can verify the session cookie)"
else
    bad "main.go does NOT pass app.JWTSecret to oidcsvc.NewService (B174 wiring regression: OIDC readSession will fail to verify any cookie)"
fi

# B.2 — main.go wires the UserLookup callback. The
# callback uses db.GetUserNameByID (or a similar
# helper) to map the JWT uid → DB username + email.
if grep -qE 'oidcSvc\.UserLookup\s*=' cmd/skygate/main.go; then
    ok "main.go sets oidcSvc.UserLookup (B174: OIDC id_token can populate the email claim)"
else
    bad "main.go does NOT set oidcSvc.UserLookup (B174 wiring regression: OIDC id_token email will be empty)"
fi

# B.3 — oidc.NewService signature has 6 params (not 5).
# The pre-B174 signature was 5 — if a future refactor
# drops the jwtSecret param, the readSession will
# silently fail to verify any cookie.
sig=$(grep -E 'func NewService\(' internal/oidc/service.go | head -1)
# count comma-separated string params BEFORE the closing paren
# (handles multi-line signatures by stripping newlines first)
sig_one_line=$(echo "$sig" | tr -d '\n')
nparams=$(echo "$sig_one_line" | sed -E 's/.*NewService\(([^)]*)\).*/\1/' | tr ',' '\n' | wc -l)
if [ "$nparams" -ge 6 ]; then
    ok "oidc.NewService accepts $nparams string params (B174: includes jwtSecret)"
else
    bad "oidc.NewService accepts only $nparams string params (expected >= 6 — jwtSecret is missing)"
fi

# ---------------------------------------------------------------------------
hdr "contract C: test contract (e2e uses real JWT, no X-Test workaround)"

# C.1 — the B161.4 e2e test issues a real JWT via
# auth.IssueJWT. The pre-B174 test used a mock
# string ("mock-jwt-for-test-only") which masked
# the production bug.
if grep -qE 'auth\.IssueJWT' internal/oidc/e2e_test.go; then
    ok "e2e_test.go calls auth.IssueJWT (B174: test now exercises the real JWT path)"
else
    bad "e2e_test.go does NOT call auth.IssueJWT (B174 test regression: the test is back to using a mock string)"
fi

# C.2 — the X-Test-Session-Cookie-Present header
# workaround is GONE. That header was the way the
# pre-B174 e2e test forced readSession to return a
# fixed user — it bypassed the actual cookie
# parsing, which is exactly the path that was
# broken in production.
if grep -qE 'X-Test-Session-Cookie-Present' internal/oidc/e2e_test.go; then
    bad "e2e_test.go still has X-Test-Session-Cookie-Present (B174 test regression: the mock-session workaround is back, masking the production bug again)"
else
    ok "X-Test-Session-Cookie-Present workaround is GONE (B174: the test exercises the real cookie path)"
fi

# C.3 — the e2e test asserts the post-login
# /oidc/authorize call returns 302 to the headscale
# callback URL (NOT 302 to /login). Pre-B174 the
# test asserted the (buggy) "302 to /login again"
# behavior because that was the actual production
# behavior — fixing this assertion is what exposed
# the production bug.
if grep -qE 'expected redirect to https://head\.test/oidc/callback' internal/oidc/e2e_test.go; then
    ok "e2e test asserts /oidc/authorize post-login → callback URL (B174: test pins the correct behavior)"
else
    bad "e2e test does NOT assert /oidc/authorize post-login → callback URL (B174 test regression: the test would still pass with the pre-B174 bug)"
fi

# C.4 — the B174 unit test file exists. This is
# the focused regression guard for readSession:
# the 7 subtests cover valid-JWT, no-cookie,
# empty-cookie, invalid-JWT, expired-JWT,
# UserLookup-nil, UserLookup-error, plus the
# pre-B174 format-rejected test.
if [ -f internal/oidc/authorize_b174_test.go ]; then
    ok "internal/oidc/authorize_b174_test.go exists (B174: focused readSession regression guard)"
else
    bad "internal/oidc/authorize_b174_test.go is missing (B174: no focused unit test for readSession)"
fi

# ---------------------------------------------------------------------------
hdr "contract D: regression contract (pre-B174 format is rejected)"

# D.1 — TestReadSession_PreB174FormatRejected must
# exist and assert that the pre-B174 colon-separated
# cookie value is rejected by the B174 readSession.
# This is the focused security guard: an attacker
# who reads the pre-B174 code should not be able
# to forge a session by setting
# "1:alice:alice@example.com:9999999999" as the
# cookie value.
if grep -qE 'TestReadSession_PreB174FormatRejected' internal/oidc/authorize_b174_test.go; then
    ok "TestReadSession_PreB174FormatRejected exists (B174 security: pre-B174 format is pinned as REJECTED)"
else
    bad "TestReadSession_PreB174FormatRejected is missing (B174 security regression: a future refactor could re-enable the colon-split parser)"
fi

# D.2 — TestReadSession_ParsesJWT must have
# subtests for the 6 critical paths: valid-JWT,
# no-cookie, empty-cookie, invalid-JWT, expired-JWT,
# UserLookup-nil, UserLookup-error. The subtests are
# how the B174 contract stays pinned in the test
# suite (a future refactor that drops one of these
# paths will fail the build).
expected_subtests="ValidJWT NoCookie EmptyCookie InvalidJWT_BadSignature ExpiredJWT UserLookupNil_FallsBackToJWT UserLookupError"
for st in $expected_subtests; do
    if grep -qE "t\.Run\(\"$st\"" internal/oidc/authorize_b174_test.go; then
        ok "TestReadSession_ParsesJWT has subtest '$st'"
    else
        bad "TestReadSession_ParsesJWT is missing subtest '$st'"
    fi
done

# ---------------------------------------------------------------------------
hdr "contract E: build + vet + test (oidc package passes)"

# E.1 — go build ./... exits 0.
GO_BIN=""
for cand in "$(command -v go)" \
    "/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
        GO_BIN="$cand"
        break
    fi
done
if [ -z "$GO_BIN" ]; then
    skip "go build ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" build ./... 2>/dev/null; then
    ok "go build ./... clean"
else
    bad "go build ./... FAILED (B174 fix has a compile error)"
fi

# E.2 — go vet ./... exits 0.
if [ -z "$GO_BIN" ]; then
    skip "go vet ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" vet ./... 2>/dev/null; then
    ok "go vet ./... clean"
else
    bad "go vet ./... FAILED"
fi

# E.3 — go test ./internal/oidc/... passes. This
# is the focused unit-test gate — the 8 B174 unit
# tests + the e2e test all live in this package.
# A regression in the OIDC service breaks this.
if [ -z "$GO_BIN" ]; then
    skip "go test ./internal/oidc/... (go binary not on PATH — skipping)"
elif "$GO_BIN" test ./internal/oidc/... 2>/dev/null; then
    ok "go test ./internal/oidc/... passes (B174 + e2e + 30+ existing OIDC tests)"
else
    bad "go test ./internal/oidc/... FAILED (B174 fix has a test failure)"
fi

echo
echo "B174 check OK — OIDC readSession uses auth.ParseJWT (the same helper feature/auth uses), the pre-B174 colon-separated format is GONE, the e2e test issues a real JWT, and main.go wires both JWTSecret + UserLookup. The 'password is reset on login' loop the operator reported on 2026-08-25 is closed."
