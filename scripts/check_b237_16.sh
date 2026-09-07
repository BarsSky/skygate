#!/bin/bash
# scripts/check_b237_16.sh — B237.16 (v1.5.2+) staticcheck contract.
#
# Pins the TD-2 (ST1013) and TD-14 (SA1012) cleanup that the
# v1.2.0 staticcheck-100%-clean work (commit 38b2fb9e) achieved
# + 2 stragglers that snuck back in via the v1.5.0 release:
#   - internal/headscale/tags_test.go:66 (404 → http.StatusNotFound)
#   - internal/oidc/e2e_test.go:399       (302 → http.StatusFound)
#
# The 5 SA1012 false-positives PLANS.md mentioned (TD-14) were
# never seen by `staticcheck` on the current tree — the false
# positives were either fixed, moved, or never landed. This
# check pins their continued absence.
#
# Why a check (and not just "run staticcheck in CI"):
#   - `staticcheck ./...` is the right tool but it's a
#     noise-heavy output (23+ lines of U1000 false-positives
#     in test stubs); a human skimming the output will miss
#     a single 404/500/302/400/401/403/409 regression
#   - This check is 5 specific greps that catch the 5
#     numeric status codes that have shipped as bugs in
#     the past (the v0.34.0 → v1.2.0 cleanup had 73 of them;
#     post-cleanup + this check = 0)
#   - The check also asserts `staticcheck` is installed
#     and on PATH (or SKIPs), so future CI runs that
#     install it get a free contract verification.
#
# Why this is B237.16 (not B240 or similar):
#   - The 200+ numbering is reserved for major version
#     features. The 237.x numbering is the v1.5.2+ series.
#   - This is a small contract pin (no new code), so the
#     .16 sub-number reflects "16th contract pin in the
#     v1.5.2 series" (B237.10 was the pathspec fix,
#     B237.15 was the deployment variants).
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. ST1013 contract: no numeric HTTP status codes in http.Error / http.Redirect ---

# A.1 no `http.Error(..., <num>)` calls anywhere in the source
# (matches 3-digit numeric literals in the http.Error call
# signature; excludes doc strings + PLANS.md which describe
# the cleanup itself)
n_st1013_error=$(grep -rE 'http\.Error\([^)]*,\s*[^,]+,\s*[0-9]{3}\)' \
    --include='*.go' . 2>/dev/null | wc -l)
if [ "$n_st1013_error" -eq 0 ]; then
    ok "A.1 no http.Error(..., <num>) calls (TD-2 contract: 0 occurrences)"
else
    bad "A.1 found $n_st1013_error http.Error calls with numeric status codes (should use http.StatusXxx constants)"
    grep -rnE 'http\.Error\([^)]*,\s*[^,]+,\s*[0-9]{3}\)' --include='*.go' . 2>/dev/null | head -5
fi

# A.2 no `http.Redirect(..., <num>)` calls anywhere in the source
# (separate from http.Error because http.Redirect has a different
# signature: (w, r, url, code))
n_st1013_redirect=$(grep -rE 'http\.Redirect\([^)]+,\s*[0-9]{3}\)' \
    --include='*.go' . 2>/dev/null | wc -l)
if [ "$n_st1013_redirect" -eq 0 ]; then
    ok "A.2 no http.Redirect(..., <num>) calls (TD-2 contract: 0 occurrences)"
else
    bad "A.2 found $n_st1013_redirect http.Redirect calls with numeric status codes (should use http.StatusXxx constants)"
    grep -rnE 'http\.Redirect\([^)]+,\s*[0-9]{3}\)' --include='*.go' . 2>/dev/null | head -5
fi

# A.3 no `w.WriteHeader(<num>)` calls in non-test code
# (WriteHeader is a related lint; the operator's
# v1.2.0 cleanup also caught these. Same pattern.)
n_write_header=$(grep -rE 'WriteHeader\([0-9]{3}\)' \
    --include='*.go' --exclude='*_test.go' . 2>/dev/null | wc -l)
if [ "$n_write_header" -eq 0 ]; then
    ok "A.3 no WriteHeader(<num>) calls in non-test code (TD-2 contract: 0 occurrences)"
else
    bad "A.3 found $n_write_header WriteHeader calls with numeric status codes (should use http.StatusXxx constants)"
fi

# --- B. SA1012 contract: no false-positive nil context warnings ---
# PLANS.md's TD-14 mentioned 5 false-positives in test files
# where nil context was intentional. staticcheck no longer
# reports them on the current tree (either they were fixed,
# moved, or the test patterns changed). The check pins the
# absence so a future test that DOES trip SA1012 fails this
# check.

# B.1 staticcheck is available (so we can run the SA1012 query)
if command -v staticcheck >/dev/null 2>&1; then
    # B.2 no SA1012 false-positives in the current tree
    n_sa1012=$(staticcheck ./... 2>/dev/null | grep -cE 'SA1012' || true)
    if [ "$n_sa1012" = "0" ]; then
        ok "B.1 staticcheck reports 0 SA1012 (TD-14 contract: no false-positives on nil context)"
    else
        bad "B.1 staticcheck reports $n_sa1012 SA1012 issues (add //nolint:staticcheck // intentional nil test comments)"
    fi

    # B.3 no ST1013 in staticcheck output either (belt-and-suspenders:
    # both the manual grep + the tool agree)
    n_st1013_tool=$(staticcheck ./... 2>/dev/null | grep -cE 'ST1013' || true)
    if [ "$n_st1013_tool" = "0" ]; then
        ok "B.2 staticcheck reports 0 ST1013 (TD-2 contract: no numeric status codes)"
    else
        bad "B.2 staticcheck reports $n_st1013_tool ST1013 issues (the manual grep in A.1/A.2 is supposed to catch these first)"
    fi
else
    echo "  SKIP  B.1 staticcheck not in PATH (B.1 + B.2 can't run; A.1-A.3 still pin the contract via manual grep)"
fi

# --- C. Sanity: the constants the v1.2.0 cleanup introduced are still in use ---

# C.1 the most common constant (http.StatusForbidden) is referenced
# from the admin package — proves the constant map is the
# canonical replacement
if grep -q 'http\.StatusForbidden' internal/feature/admin/*.go 2>/dev/null; then
    n=$(grep -rh 'http\.StatusForbidden' internal/feature/admin/ 2>/dev/null | wc -l)
    ok "C.1 http.StatusForbidden is the canonical replacement ($n usages in admin/*.go)"
else
    bad "C.1 no http.StatusForbidden references in admin/*.go (expected dozens after the v1.2.0 cleanup)"
fi

# C.2 spot-check 4 other common constants are also in use
for c in StatusBadRequest StatusNotFound StatusInternalServerError StatusMethodNotAllowed; do
    if grep -rq "http\.${c}" --include='*.go' . 2>/dev/null; then
        ok "C.2 http.${c} is in use (post-cleanup canonical replacement)"
    else
        bad "C.2 http.${c} not found anywhere (v1.2.0 cleanup should have introduced this constant)"
    fi
done

# --- D. Build + tests ---
# (the actual staticcheck contract is the A/B checks above;
# this just confirms the project still builds + tests pass
# after the straggler fixes)

# D.1 go build clean
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "D.1 go build ./... clean (stragglers fixed without breaking anything)"
    else
        bad "D.1 go build ./... failed"
    fi
else
    echo "  SKIP  D.1 go build (no go in PATH)"
fi

# D.2 go vet clean
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go vet ./... 2>/dev/null; then
        ok "D.2 go vet ./... clean (stragglers fixed without breaking anything)"
    else
        bad "D.2 go vet ./... failed"
    fi
else
    echo "  SKIP  D.2 go vet (no go in PATH)"
fi

# D.3 go test on the two packages where the stragglers lived
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        ./internal/headscale/... ./internal/oidc/... 2>/dev/null | grep -q '^ok'; then
        ok "D.3 packages with the stragglers (headscale + oidc) still pass go test"
    else
        bad "D.3 headscale or oidc tests failed (straggler fix introduced a regression)"
    fi
else
    echo "  SKIP  D.3 go test (no go in PATH)"
fi

# --- E. verify_pre_deploy.sh registration ---

# E.1 the check is registered
if grep -q 'check_b237_16' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "E.1 scripts/verify_pre_deploy.sh includes the B237.16 check"
else
    bad "E.1 scripts/verify_pre_deploy.sh must include the B237.16 check (see the run_check block for B237.16)"
fi

# E.2 AGENTS.md mentions B237.16
if grep -q 'B237\.16' AGENTS.md 2>/dev/null; then
    ok "E.2 AGENTS.md documents B237.16 (so future agents know the staticcheck contract is pinned)"
else
    bad "E.2 AGENTS.md must document B237.16 (B-check convention)"
fi

# --- Summary ---

echo
echo "=== B237.16 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
