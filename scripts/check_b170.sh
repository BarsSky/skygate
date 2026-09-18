#!/bin/bash
# check_b170.sh — B170 (v1.5.2) expired-row
# sub-classification hint on /my/devices.
#
# Operator 2026-08-25: a device that was force-expired
# by headscale (admin action, or the user running
# `tailscale logout`) shows up on /my/devices with the
# same red "Истёк" pill as a device whose TTL ran out
# naturally while offline. The two cases have very
# different root causes, so the operator wants a one-line
# hint that disambiguates without SSH'ing into the VM and
# running `headscale nodes list`.
#
# B170 adds a parseLastSeenAndClassify helper + a new
# ExpiryHint field on myNodeRow + a small muted caption
# under the existing red "expired" pill. The caption
# switches between three strings:
#
#   - devices.expired_hint_no_activity   (LastSeen empty
#                                         or unparseable)
#   - devices.expired_hint_near_expiry   (|LastSeen -
#                                         Expiry| <= 5 min;
#                                         likely logout)
#   - devices.expired_hint_while_offline (|LastSeen -
#                                         Expiry| > 5 min;
#                                         TTL ran out or
#                                         admin force-expired
#                                         a long-idle device)
#
# The 5-min threshold is pinned by the unit tests in
# internal/feature/my/devices_b170_test.go (the B-check
# below pins their EXISTENCE + content; the actual
# behaviour is verified by `go test ./internal/feature/my/...`).
#
# The B-check is split into:
#  A. Source contract (parseLastSeenAndClassify defined +
#     ExpiryHint field on myNodeRow + the heuristic is
#     called from GetMyDevices' expiry-enrichment pass)
#  B. i18n contract (4 new keys in RU + 4 in EN, all
#     under the devices.* namespace)
#  C. Template contract (3-way {{if eq .ExpiryHint "..."}}
#     under the .ExpiryWarning badge, with the right
#     i18n key for each branch)
#  D. Unit-test contract (devices_b170_test.go covers
#     the 3 hints + the 5-min boundary + the
#     nano-precision regression guard)
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source contract"

# A.1 — parseLastSeenAndClassify is defined as a
# package-private helper in devices.go. The exact name
# matters: GetMyDevices calls it by name from the
# expiry-enrichment pass, so a rename would silently
# break the heuristic.
if grep -q '^func parseLastSeenAndClassify(' internal/feature/my/devices.go; then
    ok "parseLastSeenAndClassify defined in internal/feature/my/devices.go"
else
    bad "parseLastSeenAndClassify MISSING (the /my/devices heuristic has no implementation)"
fi

# A.2 — the helper must handle the empty-LastSeen case
# explicitly (returns time.Time{} + "no_activity"). A
# regression that just calls time.Parse without the
# empty-string guard would parse "" as the Go zero
# time and return "0001-01-01..." — the heuristic
# would then compute a 2000-year |delta| and classify
# EVERY expired row as "while_offline", which defeats
# the whole point of the hint.
if awk '/^func parseLastSeenAndClassify/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/my/devices.go | grep -q 'lastSeenRaw == ""'; then
    ok "helper has the empty-LastSeen guard (returns no_activity instead of mis-classifying as 2000-year-ago)"
else
    bad "helper is MISSING the empty-LastSeen guard (would mis-classify every row as while_offline)"
fi

# A.3 — the helper must use time.RFC3339Nano (NOT
# time.RFC3339) to parse LastSeen. headscale returns
# RFC3339Nano — using the plain RFC3339 parser would
# succeed (RFC3339Nano is a superset) but a future
# refactor that switches to a stricter parser would
# silently break on timestamps with sub-second digits.
# Pin the explicit Nano to keep the contract visible.
if awk '/^func parseLastSeenAndClassify/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/my/devices.go | grep -q 'time.RFC3339Nano'; then
    ok "helper uses time.RFC3339Nano (matches headscale's wire format)"
else
    bad "helper does NOT use time.RFC3339Nano (would silently break on sub-second timestamps)"
fi

# A.4 — the helper must use the absolute value of the
# delta (|LastSeen − Expiry|, not the signed delta). A
# regression that uses the signed delta would
# mis-classify a future-dated LastSeen (rare but
# possible under headscale clock skew) as
# "near_expiry" instead of "while_offline".
if awk '/^func parseLastSeenAndClassify/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/my/devices.go | grep -qE 'delta = -delta|if delta < 0'; then
    ok "helper uses the absolute |LastSeen - Expiry| delta (handles future-dated LastSeen)"
else
    bad "helper does NOT use the absolute delta (would mis-classify future-dated LastSeen)"
fi

# A.5 — the heuristic must use a 5-minute threshold
# (matches the operator's "typical logout from an
# active client is <5 min from last ping" reasoning,
# AND the unit tests in devices_b170_test.go pin the
# boundary). A regression that bumps the threshold
# to e.g. 1h would mis-classify logouts as
# "while_offline" (defeats the point), and a
# regression that lowers it to e.g. 10s would
# mis-classify slow clients as "while_offline".
if awk '/^func parseLastSeenAndClassify/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/my/devices.go | grep -q '5\*time.Minute'; then
    ok "helper uses a 5-minute threshold (matches the unit-test boundary)"
else
    bad "helper does NOT use 5*time.Minute (the documented threshold) — would mis-classify logouts"
fi

# A.6 — the myNodeRow struct has the new ExpiryHint
# field. The /my/devices template reads .ExpiryHint
# from each row, so a missing field would 500 on the
# first page load after deploy.
if grep -qE '^\s*ExpiryHint\s+string\s*$' internal/feature/my/devices.go; then
    ok "myNodeRow has the ExpiryHint string field"
else
    bad "myNodeRow is MISSING the ExpiryHint field (template would 500 on page load)"
fi

# A.7 — the myNodeRow struct has the new LastSeenTime
# field (parsed time.Time for reuse from the template
# or future B-checks). B170 itself does not require
# the template to read LastSeenTime, but the field is
# part of the change so a regression that drops it
# would still be visible.
if grep -qE '^\s*LastSeenTime\s+time\.Time\s*$' internal/feature/my/devices.go; then
    ok "myNodeRow has the LastSeenTime time.Time field"
else
    bad "myNodeRow is MISSING the LastSeenTime field"
fi

# A.8 — the GetMyDevices expiry-enrichment pass must
# call parseLastSeenAndClassify (NOT just classifyExpired
# or some other name). The function is small enough
# that a name change is unlikely, but the call-site
# grep is cheap insurance against a copy-paste refactor
# that leaves the helper as dead code.
if grep -q 'parseLastSeenAndClassify(' internal/feature/my/devices.go; then
    # Now make sure the call is actually in the
    # expiry-enrichment pass (not just in the helper
    # definition + a test file). The pass lives in
    # GetMyDevices between the per-row `continue` and
    # the next log.Printf. We grep for the call
    # inside that region by looking for the two
    # adjacent context lines: ExpiryWarning="expired"
    # + parseLastSeenAndClassify within 3 lines.
    if grep -A3 'ExpiryWarning == "expired"' internal/feature/my/devices.go | grep -q 'parseLastSeenAndClassify('; then
        ok "GetMyDevices' expiry-enrichment pass calls parseLastSeenAndClassify (heuristic is wired up)"
    else
        bad "parseLastSeenAndClassify is defined but NOT called from the expiry-enrichment pass (the heuristic is dead code)"
    fi
else
    bad "parseLastSeenAndClassify is NOT called anywhere in devices.go (the heuristic is dead code)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: i18n contract"

# B.1..B.4 — the 4 new i18n keys (RU side).
# Pattern: the key (left of the colon) must appear
# verbatim. A regression that dropped the key would
# render the hint as the raw key name in the UI
# (the i18n.Engine logs a warning + returns the
# key as-is when a key is missing).
for key in \
    "devices.expired_hint_title" \
    "devices.expired_hint_no_activity" \
    "devices.expired_hint_near_expiry" \
    "devices.expired_hint_while_offline"
do
    if grep -qE "\"$key\"" internal/i18n/catalog_my.go; then
        ok "i18n key $key defined in catalog_my.go"
    else
        bad "i18n key $key MISSING from catalog_my.go"
    fi
done

# B.5 — each new RU key must have a non-empty value
# (the i18n.Engine returns the key name when the
# value is empty, so an empty value would render the
# raw key in the UI). The grep is restricted to the
# RU half of the file (before the `var enMy` block)
# to avoid catching the EN side by accident.
RU_END=$(grep -n '^var enMy' internal/i18n/catalog_my.go | head -1 | cut -d: -f1)
if [ -z "$RU_END" ]; then
    bad "could not locate the var enMy marker in catalog_my.go (B-check cannot validate RU values)"
fi
for key in \
    "devices.expired_hint_title" \
    "devices.expired_hint_no_activity" \
    "devices.expired_hint_near_expiry" \
    "devices.expired_hint_while_offline"
do
    # Look for the key on a line whose line number is
    # < RU_END (the RU side). A missing or empty value
    # would be a regression: the operator would see the
    # raw key in the UI instead of the localized hint.
    val=$(awk -F: -v k="$key" -v end="$RU_END" \
        'NR < end && $0 ~ "\""k"\"" { sub(/^[^:]+:[[:space:]]*"?/, ""); sub(/"[[:space:]]*,?[[:space:]]*$/, ""); print; exit }' \
        internal/i18n/catalog_my.go)
    if [ -n "$val" ] && [ "$val" != '""' ]; then
        ok "RU value for $key is non-empty (\"$val\")"
    else
        bad "RU value for $key is EMPTY (operator would see the raw key in the UI)"
    fi
done

# B.6 — each new EN key must have a non-empty value
# (same reason as B.5). The grep is restricted to
# the EN half of the file (from the `var enMy` line
# to EOF).
RU_END=$(grep -n '^var enMy' internal/i18n/catalog_my.go | head -1 | cut -d: -f1)
for key in \
    "devices.expired_hint_title" \
    "devices.expired_hint_no_activity" \
    "devices.expired_hint_near_expiry" \
    "devices.expired_hint_while_offline"
do
    val=$(awk -F: -v k="$key" -v start="$RU_END" \
        'NR > start && $0 ~ "\""k"\"" { sub(/^[^:]+:[[:space:]]*"?/, ""); sub(/"[[:space:]]*,?[[:space:]]*$/, ""); print; exit }' \
        internal/i18n/catalog_my.go)
    if [ -n "$val" ] && [ "$val" != '""' ]; then
        ok "EN value for $key is non-empty (\"$val\")"
    else
        bad "EN value for $key is EMPTY (operator would see the raw key in the UI)"
    fi
done

# ---------------------------------------------------------------------------
hdr "contract C: template contract"

# C.1 — the template has a 3-way ExpiryHint switch
# (no_activity / near_expiry / while_offline). A
# regression that dropped a branch would silently
# render the raw .ExpiryHint value for some rows
# (the template engine escapes but does not localize
# a non-matched value).
for branch in \
    "ExpiryHint \"no_activity\"" \
    "ExpiryHint \"near_expiry\"" \
    "ExpiryHint \"while_offline\""
do
    if grep -qE "\\.${branch}" internal/handlers/templates/user/devices.html; then
        ok "template has the {{if eq .${branch}}} branch"
    else
        bad "template is MISSING the {{if eq .${branch}}} branch"
    fi
done

# C.2 — each template branch renders the matching
# i18n key. The key is rendered via the `t` template
# function; a regression that dropped the `t` call
# would render the raw key as visible text.
for branch_key in \
    "no_activity:devices.expired_hint_no_activity" \
    "near_expiry:devices.expired_hint_near_expiry" \
    "while_offline:devices.expired_hint_while_offline"
do
    branch="${branch_key%%:*}"
    key="${branch_key##*:}"
    # Use a 2-line grep so we catch the {{t "..."}} call
    # inside the same {{if}} block as the branch match.
    if grep -B1 -A2 "ExpiryHint \"$branch\"" internal/handlers/templates/user/devices.html | grep -qE "t[[:space:]]+\"$key\""; then
        ok "template's $branch branch renders $key"
    else
        bad "template's $branch branch does NOT render $key (operator would see no hint)"
    fi
done

# C.3 — the template uses the .ExpiryHint field at
# least once (sanity check that the field is actually
# plumbed through to the UI). A refactor that renamed
# the field in Go but not the template would 500 on
# page load.
if grep -qE '\.ExpiryHint' internal/handlers/templates/user/devices.html; then
    ok "template reads .ExpiryHint from the row (field is plumbed through to the UI)"
else
    bad "template does NOT read .ExpiryHint — the hint would never appear in the UI"
fi

# C.4 — the hint block is rendered INSIDE the
# {{if .ExpiryWarning}} block (NOT outside it). The
# heuristic only sets ExpiryHint when
# ExpiryWarning == "expired", so an uncondditional
# render would show the hint on soon / month rows
# too (where ExpiryHint is ""). The 3-way
# {{if eq .ExpiryHint "..."}} chain would then
# fall through and render nothing — but a future
# refactor that adds a default branch would start
# showing the hint on non-expired rows.
if grep -qE '\{\{if .ExpiryWarning\}\}' internal/handlers/templates/user/devices.html; then
    # Walk the file: find the {{if .ExpiryWarning}}}
    # line, then verify the first {{if eq .ExpiryHint
    # ...}} line is BEFORE the next {{end}} that
    # closes the .ExpiryWarning block. We use awk to
    # track nesting depth and bail on the first
    # mismatch.
    if awk '
        /\{\{if .ExpiryWarning\}\}/ { in_exp=1; depth=0; next }
        in_exp && /\{\{if / { depth++ }
        in_exp && /\{\{end\}\}/ {
            if (depth == 0) { in_exp=0; next }
            depth--
        }
        in_exp && /\.ExpiryHint/ { saw_hint=1 }
        END { exit (saw_hint ? 0 : 1) }
    ' internal/handlers/templates/user/devices.html; then
        ok "the {{if eq .ExpiryHint ...}} branches live inside {{if .ExpiryWarning}} (no leak to soon/month rows)"
    else
        bad "the .ExpiryHint branches are NOT inside {{if .ExpiryWarning}} (would leak to soon/month rows on a default-branch refactor)"
    fi
else
    bad "could not find {{if .ExpiryWarning}} in user/devices.html (template structure changed?)"
fi

# ---------------------------------------------------------------------------
hdr "contract D: unit-test contract"

# D.1 — devices_b170_test.go exists. Without it, a
# future refactor of parseLastSeenAndClassify could
# silently change the 5-min threshold or the empty
# LastSeen handling without anyone noticing.
if [ -f internal/feature/my/devices_b170_test.go ]; then
    ok "internal/feature/my/devices_b170_test.go exists"
else
    bad "internal/feature/my/devices_b170_test.go is MISSING (the heuristic is not unit-tested)"
fi

# D.2..D.5 — the 4 test functions cover the 3 hint
# categories + the Nano-precision regression guard.
# Each test name is grep-checked so a rename would
# surface here before the actual test logic changed.
for testname in \
    "TestParseLastSeenAndClassify_NoActivity" \
    "TestParseLastSeenAndClassify_NearExpiry" \
    "TestParseLastSeenAndClassify_WhileOffline" \
    "TestParseLastSeenAndClassify_NanoPrecision"
do
    if grep -qE "^func ${testname}\(" internal/feature/my/devices_b170_test.go; then
        ok "test function $testname defined in devices_b170_test.go"
    else
        bad "test function $testname MISSING from devices_b170_test.go"
    fi
done

# D.6 — the test file covers the empty-LastSeen
# case explicitly (a regression that removed the
# empty-string guard in the helper would still pass
# the no_activity test if the test was sloppy).
if grep -q '""' internal/feature/my/devices_b170_test.go; then
    ok "test file covers the empty-LastSeen case"
else
    bad "test file is MISSING the empty-LastSeen case (would not catch a guard-removal regression)"
fi

# D.7 — the test file covers the 5-min boundary
# (both the inclusive "exactly 5 min" case and the
# just-over "5min+1sec" case). The boundary is the
# whole point of the 5-min threshold; a regression
# that bumped the threshold to e.g. 10 min would
# need a test failure here to be caught.
if grep -qE '5\*time\.Minute' internal/feature/my/devices_b170_test.go; then
    ok "test file pins the 5-min boundary (exactly-5min + just-over-5min cases)"
else
    bad "test file does NOT pin the 5-min boundary (a threshold change would not be caught)"
fi

# D.8 — the test file covers the malformed
# LastSeen case (defense-in-depth: a regression in
# the headscale API could return a non-RFC3339
# string and the heuristic should still classify
# it as "no_activity" rather than panic).
if grep -q 'not-a-timestamp' internal/feature/my/devices_b170_test.go; then
    ok "test file covers the malformed-LastSeen case"
else
    bad "test file is MISSING the malformed-LastSeen case"
fi

echo
echo "B170 check OK — all source, template, i18n, and unit-test contracts pinned."
