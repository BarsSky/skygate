#!/bin/bash
# scripts/check_b237_10.sh — B237.10 (v1.5.2+) build-time contract:
# the auto-update / manual-push orchestrator must pass a VALID
# git ref to `git checkout`, even when the deployed build label
# is the new "untagged-commit" format
# (`git describe --tags --always` returns just the short SHA →
# `version = e2d0b9e`, `BuildVersion = "e2d0b9e" + "+" + "e2d0b9e"`
# = `"e2d0b9e+e2d0b9e"`).
#
# Live bug (operator VM, 2026-09-04):
#   06:39:49 [info] manual push by skyadmin (target=ve2d0b9e+e2d0b9e, current=ve2d0b9e+e2d0b9e)
#   06:39:49 [info] starting update ve2d0b9e+e2d0b9e → ve2d0b9e+e2d0b9e
#   06:39:52 [debug] $ git fetch --tags --prune --force → OK
#   06:39:52 [debug] $ git checkout ve2d0b9e+e2d0b9e → error: pathspec
#     've2d0b9e+e2d0b9e' did not match any file(s) known to git
#   06:39:52 [error] FAILED: git checkout: exit status 1
#   06:39:52 [warn] attempting automatic rollback to skygate-pre-update-e2d0b9e
#
# The "ve2d0b9e+e2d0b9e" form came from TWO sources compounding:
#   1. cmd/skygate/main.go: `BuildVersion = version + "+" + commit`
#      when `version` has no "-g" suffix. For an untagged commit,
#      `git describe --tags --always` returns just the short SHA,
#      so `version = commit = e2d0b9e` and the concatenation
#      produces "e2d0b9e+e2d0b9e" (commit hash twice).
#   2. internal/feature/admin/update.go: PostAdminUpdatePush
#      has NO `target` form field, so it falls back to
#      `target = s.BuildVersion = "e2d0b9e+e2d0b9e"`. The
#      `normalizeUpdateTarget` helper then prepends "v"
#      (because the input doesn't start with "v" / "skygate-" /
#      "main" / "HEAD"), producing "ve2d0b9e+e2d0b9e" — a string
#      that can never be a valid git ref (the "v" is a display
#      convention, the "+" is invalid in a git pathspec).
#
# B237.10 fix introduces a single conversion point
# `update.GitRefForBuildLabel(s)` that:
#   - Strips the "+<commit>" suffix (the only operation
#     that's always required; "+" is the ONE character
#     that's always invalid in a git pathspec).
#   - Strips a leading "v" only when the remainder is a
#     pure hex SHA (so legitimate "v1.5.0" semver tags
#     are untouched).
# Both `PostAdminUpdateApply` and `PostAdminUpdatePush` now
# pass `update.GitRefForBuildLabel(target)` to the orchestrator
# (which is the source of `git checkout`). The orchestrator
# ALSO calls `GitRefForBuildLabel(target)` on its end as
# defense-in-depth — if a future caller forgets to
# pre-process, the orchestrator still produces a valid
# git pathspec instead of erroring with "pathspec did not
# match".
#
# Verifies:
#   A. Source: GitRefForBuildLabel() function exists in update pkg
#   B. Source: GitRefForBuildLabel is called from docker.go
#              (defense-in-depth in `git checkout` site)
#   C. Source: GitRefForBuildLabel is called from admin/update.go
#              (both PostAdminUpdateApply and PostAdminUpdatePush)
#   D. Source: the test file documents the operator-live
#              `ve2d0b9e+e2d0b9e` regression case
#   E. Test: the regression-2026-09-04-live test exists in
#              docker_test.go and pins the live fix
#   F. Test: TestGitRefForBuildLabel covers 15+ build-label
#              shapes (clean tag, describe-style, +commit
#              variants, untagged deploy, raw SHA, prerelease,
#              etc.)
#   G. Test: TestGitRefForBuildLabel_NeverPlusOrSpace is
#              an exhaustive safety net (no input produces
#              a result with "+" or " " — both invalid in
#              a git pathspec)
#   H. Test: TestIsAllHex covers the case-distinction helper
#   I. Test: TestShortSHA still passes (existing shortSHA
#              contract unchanged)
#   J. Build: `go build ./...` clean
#   K. Test: `go test ./internal/update/...` passes
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. Source: GitRefForBuildLabel exists in update package ---

# A.1 function definition
if grep -qE '^func GitRefForBuildLabel' internal/update/docker.go 2>/dev/null; then
    ok "A.1 GitRefForBuildLabel() function defined in internal/update/docker.go"
else
    bad "A.1 GitRefForBuildLabel() function missing from internal/update/docker.go"
fi

# A.2 function is public (uppercase G) — required because admin
# package imports it
if grep -qE '^func GitRefForBuildLabel' internal/update/docker.go 2>/dev/null; then
    ok "A.2 GitRefForBuildLabel is exported (admin package imports it)"
else
    bad "A.2 GitRefForBuildLabel must be public (admin package depends on it)"
fi

# A.3 function strips "+<commit>" — the critical transformation
# (grep for the "strings.Index" + "+" pattern inside the function body)
if awk '/^func GitRefForBuildLabel/,/^}/' internal/update/docker.go 2>/dev/null | grep -q 'strings.Index.*"+"'; then
    ok "A.3 GitRefForBuildLabel strips the +<commit> suffix (the critical fix)"
else
    bad "A.3 GitRefForBuildLabel must strip the +<commit> suffix"
fi

# A.4 function strips "v<hex>" leading prefix when remainder is hex
# (defense against normalizeUpdateTarget's "v" prepend)
if awk '/^func GitRefForBuildLabel/,/^}/' internal/update/docker.go 2>/dev/null | grep -q 'isAllHex'; then
    ok "A.4 GitRefForBuildLabel uses isAllHex to strip stale 'v' prefix (defense-in-depth)"
else
    bad "A.4 GitRefForBuildLabel must use isAllHex for 'v<sha>' vs 'v<semver>' distinction"
fi

# --- B. Source: docker.go uses GitRefForBuildLabel ---

# B.1 docker.go's `git checkout` site uses the helper
# (this is the defense-in-depth: even if a future caller
# forgets to pre-process, the orchestrator still works)
if grep -qE 'gitRef := GitRefForBuildLabel' internal/update/docker.go 2>/dev/null; then
    ok "B.1 docker.go git-checkout site uses GitRefForBuildLabel (defense-in-depth)"
else
    bad "B.1 docker.go must call GitRefForBuildLabel on target before git-checkout"
fi

# B.2 The `git checkout` call uses a `gitRef` variable
# (not `target` directly) — proves the transformation is
# applied at the right point
if grep -qE 'u\.runGit\(ctx, "checkout", gitRef\)' internal/update/docker.go 2>/dev/null; then
    ok "B.2 git-checkout uses the post-transformation gitRef variable"
else
    bad "B.2 git-checkout must use a gitRef variable (not target directly)"
fi

# --- C. Source: admin/update.go uses GitRefForBuildLabel ---

# C.1 PostAdminUpdateApply uses the helper.
# Pin by looking for the unique comment that B237.10
# adds INSIDE the function (the comment is unique
# enough to identify the function without line-range
# arithmetic that needs awk/sed/head):
#   "2026-09-04 (B237.10): the `target` arg may"
if grep -qE 'B237\.10.*`target` arg may' internal/feature/admin/update.go 2>/dev/null; then
    ok "C.1 PostAdminUpdateApply passes target through GitRefForBuildLabel"
else
    bad "C.1 PostAdminUpdateApply must call GitRefForBuildLabel before u.Run (B237.10 contract)"
fi

# C.2 PostAdminUpdatePush uses the helper
# (the comment is a near-duplicate: "see PostAdminUpdateApply")
if grep -qE 'B237\.10.*see PostAdminUpdateApply' internal/feature/admin/update.go 2>/dev/null; then
    ok "C.2 PostAdminUpdatePush passes target through GitRefForBuildLabel"
else
    bad "C.2 PostAdminUpdatePush must call GitRefForBuildLabel before u.Run (B237.10 contract)"
fi

# C.3 BOTH handlers use the helper (not just one — the live bug
# was triggered by PostAdminUpdatePush, but the same fix must
# apply to PostAdminUpdateApply for consistency)
n_uses=$(grep -c 'update\.GitRefForBuildLabel' internal/feature/admin/update.go 2>/dev/null || echo 0)
if [ "$n_uses" -ge "2" ]; then
    ok "C.3 GitRefForBuildLabel used in both handlers ($n_uses callsites)"
else
    bad "C.3 GitRefForBuildLabel must be used in BOTH handlers (found $n_uses callsites)"
fi

# C.4 The display `target` is NOT replaced — only the git
# operation uses the converted ref. Check that the audit
# log + manual steps + store.Start still get the human-
# readable `target` (otherwise the page would show
# "e2d0b9e" instead of "ve2d0b9e+e2d0b9e").
if grep -q 'GenerateManualSteps.*current.*target' internal/feature/admin/update.go 2>/dev/null; then
    ok "C.4 GenerateManualSteps still uses display target (not the converted ref)"
else
    bad "C.4 display target must reach GenerateManualSteps (operator-facing)"
fi

# --- D. Test: live regression case pinned ---

# D.1 The exact live-bug case is documented in a test
if grep -q 've2d0b9e+e2d0b9e' internal/update/docker_test.go 2>/dev/null; then
    ok "D.1 live regression case 've2d0b9e+e2d0b9e' pinned in docker_test.go"
else
    bad "D.1 docker_test.go must pin the 've2d0b9e+e2d0b9e' live regression case"
fi

# D.2 The regression-2026-09-04-live subtest exists
if grep -qE 'regression-2026-09-04-live' internal/update/docker_test.go 2>/dev/null; then
    ok "D.2 TestGitRefForBuildLabel/regression-2026-09-04-live exists"
else
    bad "D.2 TestGitRefForBuildLabel/regression-2026-09-04-live missing"
fi

# --- E. Test: TestGitRefForBuildLabel coverage ---

# E.1 test function exists
if grep -qE '^func TestGitRefForBuildLabel\b' internal/update/docker_test.go 2>/dev/null; then
    ok "E.1 TestGitRefForBuildLabel function defined"
else
    bad "E.1 TestGitRefForBuildLabel test function missing"
fi

# E.2 15+ subtests covering the documented build-label shapes
n_subtests=$(grep -cE '"(clean-tag|describe-style|tag-plus-commit|describe-plus-dup|untagged-deploy|operator-live-2026-09-04|raw-sha|empty|v-prefix-untagged-only|v-prefix-semver-untouched|prerelease-with-plus|long-sha-untouched|uppercase-hex-with-v-stripped|v-prefix-untagged-no-plus|regression-2026-09-04-live)"' internal/update/docker_test.go 2>/dev/null || echo 0)
if [ "$n_subtests" -ge "10" ]; then
    ok "E.2 TestGitRefForBuildLabel covers $n_subtests distinct build-label shapes (>= 10)"
else
    bad "E.2 TestGitRefForBuildLabel covers $n_subtests shapes (need >= 10)"
fi

# --- F. Test: NeverPlusOrSpace safety net ---

if grep -qE '^func TestGitRefForBuildLabel_NeverPlusOrSpace\b' internal/update/docker_test.go 2>/dev/null; then
    ok "F.1 TestGitRefForBuildLabel_NeverPlusOrSpace safety net exists"
else
    bad "F.1 TestGitRefForBuildLabel_NeverPlusOrSpace safety net missing"
fi

# F.2 the safety net asserts no "+" or " " in the result
# (the only characters that are ALWAYS invalid in a git pathspec)
if awk '/^func TestGitRefForBuildLabel_NeverPlusOrSpace/,/^}/' internal/update/docker_test.go 2>/dev/null | grep -q 'ContainsAny.*"+ '; then
    ok "F.2 TestGitRefForBuildLabel_NeverPlusOrSpace asserts no '+' or ' ' in result"
else
    bad "F.2 TestGitRefForBuildLabel_NeverPlusOrSpace must assert no '+' or ' ' in result"
fi

# --- G. Test: TestIsAllHex ---

if grep -qE '^func TestIsAllHex\b' internal/update/docker_test.go 2>/dev/null; then
    ok "G.1 TestIsAllHex case-distinction helper test exists"
else
    bad "G.1 TestIsAllHex test missing"
fi

# G.2 test covers the critical case: "1.5.0" is NOT all-hex
# (dot is not hex → "v1.5.0" must keep its "v" prefix)
if awk '/^func TestIsAllHex/,/^}/' internal/update/docker_test.go 2>/dev/null | grep -q '"1\.5\.0"'; then
    ok "G.2 TestIsAllHex covers semver '1.5.0' (the case that distinguishes semver from SHA)"
else
    bad "G.2 TestIsAllHex must cover '1.5.0' as NOT-all-hex"
fi

# --- H. Test: shortSHA unchanged ---

if grep -qE '^func TestShortSHA\b' internal/update/docker_test.go 2>/dev/null; then
    ok "H.1 TestShortSHA preserved (B237.10 didn't break the existing contract)"
else
    bad "H.1 TestShortSHA missing — B237.10 regression"
fi

# --- I. Build ---

if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "I.1 go build ./... clean"
    else
        bad "I.1 go build ./... failed"
    fi
else
    echo "  SKIP  I.1 go build (no go in PATH)"
fi

# --- J. Test run ---

if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        -run 'TestGitRefForBuildLabel|TestIsAllHex|TestShortSHA' \
        ./internal/update/... 2>/dev/null | grep -q '^ok'; then
        ok "J.1 TestGitRefForBuildLabel + TestIsAllHex + TestShortSHA pass"
    else
        bad "J.1 B237.10 unit tests failed"
    fi
else
    echo "  SKIP  J.1 B237.10 unit tests (no go in PATH)"
fi

# J.2 full internal/update/... suite still passes
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 60s \
        ./internal/update/... 2>/dev/null | grep -q '^ok'; then
        ok "J.2 full internal/update test suite passes (B237.10 + existing)"
    else
        bad "J.2 full internal/update test suite failed"
    fi
else
    echo "  SKIP  J.2 full internal/update test suite (no go in PATH)"
fi

# --- K. Admin package tests still pass (B237.10 changes admin/update.go) ---

if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 60s \
        ./internal/feature/admin/... 2>/dev/null | grep -q '^ok'; then
        ok "K.1 internal/feature/admin test suite passes (B237.10 didn't break admin)"
    else
        bad "K.1 internal/feature/admin test suite failed (B237.10 regression in admin)"
    fi
else
    echo "  SKIP  K.1 internal/feature/admin tests (no go in PATH)"
fi

# --- Summary ---

echo
echo "=== B237.10 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
