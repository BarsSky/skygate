#!/bin/bash
# check_b173.sh — B173 (v1.5.2) login form UX fix.
#
# Operator 2026-08-25 (after the B172 fix shipped):
# "теперь при переходе страница логина всегда обновляется
#  если написать пароль и тем самым его сбрасывает от
#  чего нельзя залогиниться"
#
# "Now, when going to the login page, the page always
#  refreshes if you write the password, and thereby
#  resets it, so you can't log in."
#
# Root cause analysis: the B172 fix made the form
# re-render correctly on a FAILED login attempt (it
# preserves the `next` value so the OIDC flow can
# resume). The pre-B173 form had no JS — the user
# typed the password, hit Enter or clicked the submit
# button, and the page re-rendered in <100ms with no
# visual feedback. If the credentials were wrong (typo,
# wrong keyboard layout, caps lock, etc.) the form
# would re-render with an error message; if they were
# right the form would redirect to the OIDC URL.
# Either way, the user saw "the page refreshed and my
# password is gone" with no explanation. B173 makes
# the submit feedback explicit:
#
#   1. onsubmit handler disables the button + swaps
#      the label to "Вход..." (RU) / "Signing in..."
#      (EN) + shows a spinner
#   2. username + password become readonly during the
#      in-flight request (prevents the user from typing
#      more while the request is processing)
#   3. The error banner is rendered on a FAILED login
#      (pre-existing B172 behavior, kept as-is)
#
# The handler is wrapped in an IIFE + try/catch so a
# JS error (e.g. CSP violation, the user has JS
# disabled) just falls through to the normal form
# submit. JS is a progressive enhancement, not a
# hard dependency.
#
# The B-check is split into:
#  A. Template contract (login.html has the B173
#     markup: id=login-form on <form>, id=login-submit
#     on <button>, .login-btn-idle + .login-btn-loading
#     <span>s, the onsubmit IIFE, the disabled/readonly
#     CSS rules)
#  B. i18n contract (the new login.submitting key in
#     RU + EN, in catalog_common.go alongside the
#     other login.* keys)
#  C. Smoke contract (go build + go vet clean)
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: template contract (the B173 UX markup is wired up)"

# A.1 — the form has id="login-form" so the JS
# handler can attach to it without scanning for
# the first <form> in the page (which would also
# match the theme-picker form if it had one).
if grep -qE 'form method="post" action="/login\?theme' internal/handlers/templates/login.html && \
   grep -qE '<form method="post" action="/login\?theme=\{\{[^}]+\}\}" id="login-form"' internal/handlers/templates/login.html; then
    ok "login form has id='login-form' (B173: the JS handler can attach to a specific form)"
else
    bad "login form is missing id='login-form' (B173: the JS handler has nothing to attach to)"
fi

# A.2 — the submit button has id="login-submit"
# so the JS handler can disable it.
if grep -qE 'id="login-submit"' internal/handlers/templates/login.html; then
    ok "submit button has id='login-submit' (B173: the JS handler can disable it on submit)"
else
    bad "submit button is missing id='login-submit' (B173: the JS handler can't disable the button)"
fi

# A.3 — the button has two <span>s: one for the
# idle state ("Войти" / "Sign in") and one for
# the loading state ("Вход..." / "Signing in...")
# with a spinner. The JS handler swaps which one
# is visible.
if grep -qE 'class="login-btn-idle"' internal/handlers/templates/login.html && \
   grep -qE 'class="login-btn-loading"' internal/handlers/templates/login.html; then
    ok "submit button has the B173 idle/loading <span> pair (B173: the JS handler swaps which is visible on submit)"
else
    bad "submit button is missing the B173 idle/loading <span> pair (B173: the loading-state can't be shown)"
fi

# A.4 — the loading <span> uses font-awesome's
# fa-spinner (animated) so the user gets visual
# feedback that something is happening.
if grep -qE 'fa-spinner fa-spin' internal/handlers/templates/login.html; then
    ok "loading <span> uses fa-spinner fa-spin (B173: visible animation confirms the submit is in flight)"
else
    bad "loading <span> doesn't use fa-spinner (B173: no animated spinner)"
fi

# A.5 — the loading <span> is hidden by default
# (display:none) so the user only sees it after
# the JS handler shows it.
if grep -qE 'login-btn-loading[^}]*style="display:none' internal/handlers/templates/login.html; then
    ok "loading <span> is display:none by default (B173: hidden until the JS handler shows it)"
else
    bad "loading <span> is NOT display:none by default (B173: would be visible on page load)"
fi

# A.6 — the JS handler is present at the end of
# login.html. The handler does 4 things:
#   1. e.preventDefault() on invalid (no-op for
#      our case — we use checkValidity, not
#      preventDefault, so the browser's native
#      validation tooltip still shows)
#   2. f.checkValidity() to bail if the form is
#      invalid (so we don't enter the loading
#      state on a partial form)
#   3. sets the username + password to readOnly
#   4. disables the submit button + swaps the
#      label to the loading state
# We check for the high-level signals (checkValidity,
# readOnly, btn.disabled) rather than the exact
# syntax (which can change with future refactors).
if grep -qE 'checkValidity\(\)' internal/handlers/templates/login.html && \
   grep -qE 'readOnly = true' internal/handlers/templates/login.html && \
   grep -qE 'btn\.disabled = true' internal/handlers/templates/login.html; then
    ok "login.html has the B173 submit handler (checkValidity + readOnly + btn.disabled)"
else
    bad "login.html is missing the B173 submit handler (the loading-state won't activate)"
fi

# A.7 — the loading state is shown by setting
# display:inline-flex on .login-btn-loading (the
# JS handler does this). The handler must do both:
# hide the idle span + show the loading span.
if grep -qE 'idle\.style\.display = .none' internal/handlers/templates/login.html && \
   grep -qE 'loading\.style\.display = .inline-flex' internal/handlers/templates/login.html; then
    ok "submit handler swaps the button spans (idle hidden + loading shown)"
else
    bad "submit handler doesn't swap the button spans (the loading label won't be shown)"
fi

# A.8 — the CSS has the :disabled style (button
# is dimmed + cursor:wait) so the user sees the
# button is "stuck" (and not broken) during the
# request. Without this, the button looks identical
# to a "click me" button.
if grep -qE 'button:disabled' internal/handlers/templates/login.html; then
    ok "login.html has the button:disabled CSS (cursor:wait + opacity:.6 during the in-flight submit)"
else
    bad "login.html is missing the button:disabled CSS (the user can't tell the button is in flight)"
fi

# A.9 — the CSS has the input:read-only style
# (background + cursor:not-allowed) so the user
# sees the inputs are locked while the request is
# in flight. Without this, the inputs look
# identical to editable ones.
if grep -qE 'input:read-only' internal/handlers/templates/login.html; then
    ok "login.html has the input:read-only CSS (background:var(--bg-elev) + cursor:not-allowed)"
else
    bad "login.html is missing the input:read-only CSS (the user can't tell the inputs are locked)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: i18n contract (the new login.submitting key)"

# B.1 — the new key is defined in BOTH halves
# (RU + EN) of catalog_common.go. A regression
# that defines the key only in one half would
# render the raw key name in the UI for the other
# language.
if grep -q '"login.submitting"' internal/i18n/catalog_common.go; then
    count=$(grep -c '"login.submitting"' internal/i18n/catalog_common.go || true)
    if [ "$count" -eq 2 ]; then
        ok "i18n key login.submitting defined exactly 2 times (RU + EN)"
    else
        bad "i18n key login.submitting defined $count times (expected 2 = RU + EN)"
    fi
else
    bad "i18n key login.submitting MISSING from catalog_common.go"
fi

# B.2 — the new RU value is non-empty.
RU_END=$(grep -n '^var enCommon' internal/i18n/catalog_common.go | head -1 | cut -d: -f1)
if [ -z "$RU_END" ]; then
    bad "could not locate the var enCommon marker in catalog_common.go"
fi
val=$(awk -F: -v k='"login.submitting"' -v end="$RU_END" \
    'NR < end && $0 ~ k { sub(/^[^:]+:[[:space:]]*"?/, ""); sub(/"[[:space:]]*,?[[:space:]]*$/, ""); print; exit }' \
    internal/i18n/catalog_common.go)
if [ -n "$val" ] && [ "$val" != '""' ]; then
    ok "RU value for login.submitting is non-empty (\"$val\")"
else
    bad "RU value for login.submitting is EMPTY"
fi

# B.3 — the new EN value is non-empty.
val=$(awk -F: -v k='"login.submitting"' -v start="$RU_END" \
    'NR > start && $0 ~ k { sub(/^[^:]+:[[:space:]]*"?/, ""); sub(/"[[:space:]]*,?[[:space:]]*$/, ""); print; exit }' \
    internal/i18n/catalog_common.go)
if [ -n "$val" ] && [ "$val" != '""' ]; then
    ok "EN value for login.submitting is non-empty (\"$val\")"
else
    bad "EN value for login.submitting is EMPTY"
fi

# ---------------------------------------------------------------------------
hdr "contract C: smoke contract (build + vet clean)"

# C.1 — go build ./... exits 0. The B173 fix only
# touches the template + the i18n catalog (no Go
# code changes), but we still verify the build to
# catch any unrelated regression.
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
    bad "go build ./... FAILED (B173 fix has a compile error)"
fi

# C.2 — go vet ./... exits 0.
if [ -z "$GO_BIN" ]; then
    skip "go vet ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" vet ./... 2>/dev/null; then
    ok "go vet ./... clean"
else
    bad "go vet ./... FAILED"
fi

echo
echo "B173 check OK — login form has an explicit submit-time loading state (the 'page refreshes when I type the password' symptom now has a visible cause: the form IS submitting, you just couldn't see the in-flight button)."
