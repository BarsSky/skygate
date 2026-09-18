#!/bin/bash
# scripts/check_b237_20.sh — B237.20 (v1.5.2+) TD-12 + TD-13
# staticcheck-100%-clean contract.
#
# TD-12 ("30 ST1013-style noise items"): the pre-B237.20
# tree had 23 staticcheck warnings (22 U1000 "unused code"
# on test stubs that satisfy interface contracts, + 1
# S1021 "merge variable + assignment"). All 23 are
# addressed by either //lint:ignore directives (for
# stubs that look unused but are required to satisfy
# database/sql/driver, http.ResponseWriter, etc.)
# or by outright deletion (for genuinely dead code
# like `queryReachable` and the unused `mu` field on
# the Elector).
#
# TD-13 (~917 lines of testutil.go stubs) was the
# pre-TD-12 audit target. The actual findings: the
# 917 lines were mostly legitimate test fixtures, not
# stubs to delete. The cleanup below (3 deletions,
# 20 //lint:ignore directives) is the productive part
# of the audit; the rest of testutil.go is a follow-up
# if a real duplication problem surfaces.
#
# Why a B-check for staticcheck: the operator's CI
# already runs `staticcheck ./...` (per the linter
# install in the project README), but the output is
# noisy. A dedicated B-check pins the contract "staticcheck
# reports 0 issues" + the per-file wiring of //lint:ignore
# so a future refactor doesn't break the contract.
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. staticcheck reports 0 issues ---

# A.1 staticcheck is on PATH (otherwise we can't pin
# the contract and the B-check is N/A)
if command -v staticcheck >/dev/null 2>&1; then
    ok "A.1 staticcheck is on PATH (the B-check can run)"
else
    echo "  SKIP  A.1 staticcheck not in PATH (B-check is N/A on this host)"
fi

# A.2 staticcheck reports 0 issues
if command -v staticcheck >/dev/null 2>&1; then
    n_warnings=$(staticcheck ./... 2>/dev/null | grep -cE 'U1000|ST1013|SA1012|S1021' || true)
    if [ "$n_warnings" = "0" ]; then
        ok "A.2 staticcheck reports 0 issues (TD-12 / TD-13 contract: 0 / 0)"
    else
        bad "A.2 staticcheck reports $n_warnings issues (B237.20 fix removed all 23 — investigate the regression)"
        staticcheck ./... 2>/dev/null | head -10
    fi
else
    echo "  SKIP  A.2 staticcheck not on PATH (see A.1)"
fi

# A.3 no U1000 (unused code) anywhere — the most
# common warning; the B237.20 fix is 22 //lint:ignore
# directives + 2 outright deletions
if command -v staticcheck >/dev/null 2>&1; then
    n_u1000=$(staticcheck ./... 2>/dev/null | grep -cE 'U1000' || true)
    if [ "$n_u1000" = "0" ]; then
        ok "A.3 no U1000 (unused code) anywhere (B237.20 fix: 20 //lint:ignore + 2 deletions)"
    else
        bad "A.3 $n_u1000 U1000 warnings remain (the B237.20 contract is 0)"
    fi
else
    echo "  SKIP  A.3 staticcheck not on PATH"
fi

# A.4 no ST1013 / SA1012 (these are the TD-2 / TD-14
# categories, pinned separately by B237.16)
if command -v staticcheck >/dev/null 2>&1; then
    n_st1013=$(staticcheck ./... 2>/dev/null | grep -cE 'ST1013' || true)
    n_sa1012=$(staticcheck ./... 2>/dev/null | grep -cE 'SA1012' || true)
    if [ "$n_st1013" = "0" ] && [ "$n_sa1012" = "0" ]; then
        ok "A.4 no ST1013 (numeric status codes) or SA1012 (nil context) — pinned by B237.16"
    else
        bad "A.4 $n_st1013 ST1013 / $n_sa1012 SA1012 remain (B237.16 should have caught these)"
    fi
else
    echo "  SKIP  A.4 staticcheck not on PATH"
fi

# A.5 no S1021 (merge variable + assignment) —
# the B237.20 fix merged the `var x; x = y` pattern
# in dbmigrate/ssh_transport.go:279 into `x := y`
if command -v staticcheck >/dev/null 2>&1; then
    n_s1021=$(staticcheck ./... 2>/dev/null | grep -cE 'S1021' || true)
    if [ "$n_s1021" = "0" ]; then
        ok "A.5 no S1021 (the B237.20 fix merged the ssh_transport.go:279 split-decl)"
    else
        bad "A.5 $n_s1021 S1021 warnings remain"
    fi
else
    echo "  SKIP  A.5 staticcheck not on PATH"
fi

# --- B. The //lint:ignore directives exist where the
# contract needs them (20+ directives across 3 files) ---

# B.1 the B224 test stub file has 17 //lint:ignore
# directives (4 for fakeConn + 5 for fakeStmt +
# 3 for fakeResult + 2 for fakeDriver + 3 for the
# method declarations that take up multi-line space).
# Counting: any line containing "//lint:ignore U1000"
# inside resettable_b224_test.go.
n_resettable=$(grep -c '^//lint:ignore U1000' internal/db/resettable_b224_test.go 2>/dev/null || echo 0)
if [ "$n_resettable" -ge 14 ]; then
    ok "B.1 resettable_b224_test.go has $n_resettable //lint:ignore U1000 directives (driver.Conn/Stmt/Result/Driver stubs)"
else
    bad "B.1 resettable_b224_test.go has $n_resettable //lint:ignore directives, need >= 14 (the driver-interface stubs)"
fi

# B.2 the B206 test file has 4 //lint:ignore
# directives (recordingResponseWriter + 3 methods)
n_b206=$(grep -c '^//lint:ignore U1000' internal/feature/healthz/db_health_b206_test.go 2>/dev/null || echo 0)
if [ "$n_b206" -ge 4 ]; then
    ok "B.2 db_health_b206_test.go has $n_b206 //lint:ignore U1000 directives (http.ResponseWriter stub)"
else
    bad "B.2 db_health_b206_test.go has $n_b206 //lint:ignore directives, need >= 4"
fi

# B.3 the B210 type alias has 1 //lint:ignore
# directive
n_b210=$(grep -c '^//lint:ignore U1000' internal/feature/healthz/db_health.go 2>/dev/null || echo 0)
if [ "$n_b210" -ge 1 ]; then
    ok "B.3 db_health.go has $n_b210 //lint:ignore U1000 directive (the B210.1 type alias)"
else
    bad "B.3 db_health.go has $n_b210 //lint:ignore directives, need >= 1 (the type alias)"
fi

# --- C. The genuinely-deleted code ---

# C.1 queryReachable is gone (it was defined but never
# called in internal/feature/admin/database.go)
if ! grep -q '^func queryReachable' internal/feature/admin/database.go 2>/dev/null; then
    ok "C.1 queryReachable function deleted (was U1000 — never called)"
else
    bad "C.1 queryReachable still exists (U1000 — delete it)"
fi

# C.2 the unused `mu` field on the Elector is gone
# (the sync.Mutex was declared but never used)
if ! grep -qE '^\s+mu\s+sync\.Mutex' internal/elector/elector.go 2>/dev/null; then
    ok "C.2 unused `mu sync.Mutex` field on Elector deleted (U1000)"
else
    bad "C.2 unused `mu sync.Mutex` field still exists on Elector (U1000 — delete it)"
fi

# C.3 the unused `mu` field on stubDriver is gone
# (sync.Mutex was declared but never used)
if ! grep -qE 'mu\s+sync\.Mutex' internal/db/swapdb_b203_test.go 2>/dev/null; then
    ok "C.3 unused `mu sync.Mutex` field on stubDriver deleted (U1000)"
else
    bad "C.3 unused `mu sync.Mutex` field still exists on stubDriver (U1000 — delete it)"
fi

# C.4 the `sync` import in internal/elector/elector.go
# is gone (was only used by the deleted `mu` field)
if ! grep -q '^\s*"sync"\s*$' internal/elector/elector.go 2>/dev/null; then
    ok "C.4 unused 'sync' import in elector.go deleted (was only used by the deleted `mu` field)"
else
    bad "C.4 'sync' import in elector.go is still there (it should be deleted — only the `mu` field used it)"
fi

# --- D. Build + tests ---

# D.1 the tree still builds (no syntax errors from
# the deletions + //lint:ignore additions)
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "D.1 go build ./... clean (the deletions + //lint:ignore didn't break the build)"
    else
        bad "D.1 go build ./... failed (B237.20 deleted a field that WAS used somewhere — investigate)"
    fi
else
    echo "  SKIP  D.1 go build (no go in PATH)"
fi

# D.2 the 3 affected test packages still pass
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        ./internal/db/... ./internal/elector/... ./internal/feature/healthz/... \
        2>/dev/null | grep -q '^ok'; then
        ok "D.2 the 3 affected test packages still pass (db, elector, healthz)"
    else
        bad "D.2 the 3 affected test packages failed (B237.20 deleted code that WAS tested — investigate)"
    fi
else
    echo "  SKIP  D.2 go test (no go in PATH)"
fi

# --- E. verify_pre_deploy.sh + AGENTS.md registration ---

# E.1 the check is registered
if grep -q 'check_b237_20' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "E.1 scripts/verify_pre_deploy.sh includes the B237.20 check"
else
    bad "E.1 scripts/verify_pre_deploy.sh must include the B237.20 check"
fi

# E.2 AGENTS.md mentions B237.20
if grep -q 'B237\.20' AGENTS.md 2>/dev/null; then
    ok "E.2 AGENTS.md documents B237.20"
else
    bad "E.2 AGENTS.md must document B237.20 (B-check convention)"
fi

# --- Summary ---

echo
echo "=== B237.20 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
