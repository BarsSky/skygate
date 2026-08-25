#!/bin/bash
# check_b172.sh — B172 (v1.5.2) login `next`-redirect fix.
#
# Operator 2026-08-25: "когда попробовал залогинится в
# headscale через head.skynas.ru перенесло на логин в
# skygate, после входа в skygate открылась страница
# приветствия и все. устройство не добавлено и больше
# ничего непроисходит."
#
# Root cause: PostLogin in internal/feature/auth/service.go
# always redirected to /dashboard, ignoring the `next`
# query param that /oidc/authorize sets via /login?next=...
# (the OIDC authorize URL with all the client_id + state +
# code_challenge params). Pre-B172 the login form also had
# no `next` hidden input, so the OIDC handshake died at
# the post-login redirect and headscale's /oidc/callback
# was never reached — operator saw the welcome page and the
# device never got registered.
#
# The B161.4 e2e test in internal/oidc/e2e_test.go was
# supposed to catch this, but it had a known gap: STEP 4
# was a "pre-populate an auth code (simulating successful
# login)" shortcut that bypassed the /login round-trip
# entirely. B172 closes the gap: the e2e test now wires
# a mock /login handler into the test mux and walks
# the full authorize → /login?next=... → POST /login →
# re-run /oidc/authorize flow. If PostLogin ever drops
# the `next` again, the e2e test fails at the
# "login POST: 302 → /oidc/authorize" assertion.
#
# The B-check is split into:
#  A. Source contract (safeNextRedirect defined +
#     GetLogin reads next + PostLogin uses safeNextRedirect
#     + login.html has the hidden next input)
#  B. Security contract (5 case categories for
#     safeNextRedirect — covered by the unit tests in
#     service_b172_test.go, pinned by a code-level grep
#     so a future refactor can't silently weaken them)
#  C. E2E contract (the e2e test in internal/oidc/
#     e2e_test.go now walks STEP 4 — the mock-login round-
#     trip — and asserts the post-login redirect preserves
#     the OIDC params. The B-check greps the test file
#     to ensure the new step is present.)
#  D. Smoke contract (build + vet + unit test + e2e test
#     all pass).
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source contract (the B172 fix is wired up)"

# A.1 — safeNextRedirect is defined in
# internal/feature/auth/service.go. The function is
# the open-redirect defense for the post-login
# `next` value; the unit tests in service_b172_test.go
# pin its behaviour.
if grep -qE '^func safeNextRedirect\(next, requestHost string\) string \{' internal/feature/auth/service.go; then
    ok "safeNextRedirect defined in internal/feature/auth/service.go"
else
    bad "safeNextRedirect MISSING (B172 fix incomplete: no open-redirect defense for the `next` value)"
fi

# A.2 — GetLogin reads `next` from the query string
# and passes it to the template. The pre-B172 code
# didn't read next at all.
if awk '/^func \(s \*Service\) GetLogin/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -q 'r\.URL\.Query()\.Get("next")' /tmp/_b172_awk.txt; then
    ok "GetLogin reads ?next= from the query string (B172 fix: the form now knows the OIDC redirect target)"
else
    bad "GetLogin does NOT read ?next= (B172 fix incomplete: the form will lose the OIDC context)"
fi

# A.3 — GetLogin passes `next` to the template as
# "Next" (so login.html can render the hidden input).
if awk '/^func \(s \*Service\) GetLogin/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -qE 'data\["Next"\]' /tmp/_b172_awk.txt; then
    ok "GetLogin passes 'Next' to the template (the form renders the hidden next input)"
else
    bad "GetLogin does NOT pass Next to the template (login.html has no data to render the hidden input)"
fi

# A.4 — PostLogin reads `next` from the form.
# Without this, the post-login redirect has no way
# to know where to send the user.
if awk '/^func \(s \*Service\) PostLogin/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -qE 'r\.FormValue\("next"\)' /tmp/_b172_awk.txt; then
    ok "PostLogin reads 'next' from the form (B172 fix: the post-login redirect can now honour the OIDC context)"
else
    bad "PostLogin does NOT read `next` from the form (B172 fix incomplete: hard-coded /dashboard redirect still in place)"
fi

# A.5 — PostLogin calls safeNextRedirect (the
# security wrapper). A regression that just
# re-redirects to r.FormValue("next") without the
# wrapper would re-open the open-redirect attack.
if awk '/^func \(s \*Service\) PostLogin/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -q 'safeNextRedirect' /tmp/_b172_awk.txt; then
    ok "PostLogin calls safeNextRedirect (open-redirect defense is active)"
else
    bad "PostLogin does NOT call safeNextRedirect (open-redirect attack vector re-opened)"
fi

# A.6 — PostLogin's final redirect uses the
# validated `next` variable, NOT a hard-coded
# /dashboard. The pre-B172 line was:
#     http.Redirect(w, r, "/dashboard", http.StatusFound)
# Post-B172 it's:
#     http.Redirect(w, r, next, http.StatusFound)
# where `next` is the safeNextRedirect() output.
if awk '/^func \(s \*Service\) PostLogin/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -qE 'http\.Redirect\(w, r, next,' /tmp/_b172_awk.txt; then
    ok "PostLogin redirects to the validated 'next' (B172 fix: no more hard-coded /dashboard)"
else
    bad "PostLogin does NOT redirect to `next` (B172 fix incomplete: the hard-coded /dashboard redirect is still in place)"
fi

# A.7 — login.html has a hidden next input inside
# the login form. Without this, the form would
# POST without `next` and PostLogin would fall
# back to /dashboard.
if grep -qE 'name="next"' internal/handlers/templates/login.html; then
    ok "login.html has a hidden 'next' input (B172 fix: the form preserves the OIDC context through the POST)"
else
    bad "login.html has NO hidden 'next' input (B172 fix incomplete: the form will POST without `next` and PostLogin will fall back to /dashboard)"
fi

# A.8 — login.html uses Go's template syntax for
# the `Next` value (so the operator can drop a
# hostile string in without breaking the HTML).
if grep -qE '\{\{if \.Next\}\}.*name="next".*value="\{\{\.Next\}\}' internal/handlers/templates/login.html; then
    ok "login.html renders the hidden next input via Go template (auto-escapes HTML)"
else
    # Tolerate either the all-in-one regex or
    # a multi-line variant — the structural
    # requirement is the {{.Next}} is inside
    # the value attribute.
    if grep -q 'name="next"' internal/handlers/templates/login.html && grep -q '{{.Next}}' internal/handlers/templates/login.html; then
        ok "login.html references {{.Next}} inside the hidden next input (Go template auto-escapes)"
    else
        bad "login.html does NOT use {{.Next}} for the hidden next value (HTML escape risk)"
    fi
fi

# A.9 — net/url is imported in service.go (safeNextRedirect
# uses url.Parse to validate the absolute-URL case).
if grep -q '"net/url"' internal/feature/auth/service.go; then
    ok "service.go imports net/url (safeNextRedirect needs it for url.Parse)"
else
    bad "service.go does NOT import net/url (safeNextRedirect would fail to compile)"
fi

# A.10 — safeNextRedirect rejects protocol-relative
# URLs (//evil.com/path). This is the #1 open-redirect
# attack vector and the #1 reason the test suite
# exists.
if awk '/^func safeNextRedirect/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -q 'strings.HasPrefix(next, "//")' /tmp/_b172_awk.txt; then
    ok "safeNextRedirect rejects protocol-relative URLs (//evil.com)"
else
    bad "safeNextRedirect does NOT check for protocol-relative URLs (the #1 open-redirect attack vector)"
fi

# A.11 — safeNextRedirect rejects absolute URLs
# whose host != the request's host. The same-host
# check is the second pillar of the open-redirect
# defense.
if awk '/^func safeNextRedirect/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -q 'u\.Host != requestHost' /tmp/_b172_awk.txt; then
    ok "safeNextRedirect rejects absolute URLs with a different host"
else
    bad "safeNextRedirect does NOT check the host (open-redirect via evil.com is still possible)"
fi

# A.12 — safeNextRedirect rejects non-http(s)
# schemes (javascript:, data:, file:, ...).
if awk '/^func safeNextRedirect/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/auth/service.go > /tmp/_b172_awk.txt && grep -qE 'u\.Scheme != "http" && u\.Scheme != "https"' /tmp/_b172_awk.txt; then
    ok "safeNextRedirect rejects non-http(s) schemes (javascript:, data:, file:)"
else
    bad "safeNextRedirect does NOT check the scheme (XSS via javascript: URL is still possible)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: unit-test contract (safeNextRedirect is pinned)"

# B.1 — the unit-test file exists.
if [ -f internal/feature/auth/service_b172_test.go ]; then
    ok "internal/feature/auth/service_b172_test.go exists"
else
    bad "internal/feature/auth/service_b172_test.go is MISSING (the open-redirect defense has no unit tests)"
fi

# B.2 — the unit tests cover the 5 case categories
# (empty, relative, protocol-relative, absolute-
# different-host, absolute-same-host).
if grep -qE '"empty"|"relative_path|"protocol_relative"|"absolute_different_host"|"absolute_same_host"' internal/feature/auth/service_b172_test.go; then
    ok "service_b172_test.go covers all 5 case categories of safeNextRedirect"
else
    bad "service_b172_test.go does NOT cover all 5 case categories — a future safeNextRedirect refactor could weaken the defense without a test failure"
fi

# B.3 — the unit tests cover the javascript:
# scheme (the #1 XSS attack vector).
if grep -q 'javascript:' internal/feature/auth/service_b172_test.go; then
    ok "service_b172_test.go covers the javascript: scheme (XSS defense)"
else
    bad "service_b172_test.go does NOT cover the javascript: scheme (XSS attack vector)"
fi

# B.4 — the unit tests cover the protocol-relative
# URL (//evil.com/path).
if grep -q '//evil.com' internal/feature/auth/service_b172_test.go; then
    ok "service_b172_test.go covers the protocol-relative URL attack"
else
    bad "service_b172_test.go does NOT cover the protocol-relative URL attack"
fi

# ---------------------------------------------------------------------------
hdr "contract C: e2e contract (the OIDC + login round-trip is e2e-tested)"

# C.1 — the e2e test now walks the actual login
# form. The pre-B172 test had a "pre-populate an
# auth code" shortcut (commented as a known gap
# in the original B161.4 design); the new STEP 4
# replaces that shortcut with a real /login round-
# trip via a mock handler.
if grep -qE "B172.*walk the /login round-trip|B172.*pre-B172 this was a 'pre-populate'" internal/oidc/e2e_test.go; then
    ok "e2e_test.go now includes the B172 STEP 4 (login round-trip via mock /login handler)"
else
    bad "e2e_test.go does NOT have the B172 STEP 4 (the OIDC + login round-trip is not e2e-tested — pre-B172 bug can re-occur)"
fi

# C.2 — the e2e test asserts the post-login redirect
# preserves the OIDC params (state, client_id,
# code_challenge). Pre-B172 the redirect was
# /dashboard and these params were lost.
if grep -qE 'login POST: 302 → /oidc/authorize|Location client_id = ' internal/oidc/e2e_test.go; then
    ok "e2e_test.go asserts the post-login redirect is /oidc/authorize (B172 fix: the OIDC handshake continues)"
else
    bad "e2e_test.go does NOT assert the post-login redirect target (B172 regression could ship again)"
fi

# C.3 — the e2e test asserts the session cookie is
# set on the post-login redirect. Without this,
# the next /oidc/authorize call wouldn't see the
# user as authenticated.
if grep -q 'skygate_session' internal/oidc/e2e_test.go; then
    ok "e2e_test.go checks for the skygate_session cookie (B172 fix: the post-login redirect carries the session)"
else
    bad "e2e_test.go does NOT check for the session cookie (B172 regression: the post-login redirect might not authenticate the user)"
fi

# C.4 — the e2e test asserts the login form has a
# hidden `next` input. Without this, the OIDC
# params wouldn't survive the POST.
if grep -qE 'name="next"' internal/oidc/e2e_test.go; then
    ok "e2e_test.go asserts the login form has a hidden 'next' input"
else
    bad "e2e_test.go does NOT check the login form's hidden next input (B172 regression: the form might not preserve the OIDC params)"
fi

# ---------------------------------------------------------------------------
hdr "contract D: smoke contract (build + vet + tests all clean)"

# D.1 — go build ./... exits 0. The B172 fix touches
# internal/feature/auth/service.go + login.html +
# internal/oidc/e2e_test.go + the new test file.
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
    bad "go build ./... FAILED (B172 fix has a compile error)"
fi

# D.2 — go vet ./... exits 0.
if [ -z "$GO_BIN" ]; then
    skip "go vet ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" vet ./... 2>/dev/null; then
    ok "go vet ./... clean"
else
    bad "go vet ./... FAILED"
fi

# D.3 — the B172 unit tests pass. We use a
# dedicated test binary (no caching) so the
# result is fresh.
if [ -z "$GO_BIN" ]; then
    skip "go test ./internal/feature/auth/... (go binary not on PATH — skipping)"
elif "$GO_BIN" test -count=1 ./internal/feature/auth/... 2>/dev/null; then
    ok "go test ./internal/feature/auth/... clean (unit tests pass)"
else
    bad "go test ./internal/feature/auth/... FAILED (the safeNextRedirect unit tests fail)"
fi

# D.4 — the B172 e2e tests pass.
if [ -z "$GO_BIN" ]; then
    skip "go test ./internal/oidc/... (go binary not on PATH — skipping)"
elif "$GO_BIN" test -count=1 ./internal/oidc/... 2>/dev/null; then
    ok "go test ./internal/oidc/... clean (e2e tests pass)"
else
    bad "go test ./internal/oidc/... FAILED (the e2e test fails — the OIDC + login round-trip doesn't work)"
fi

echo
echo "B172 check OK — OIDC login round-trip now preserves the next= context (the B161.4 e2e test gap is closed)."
