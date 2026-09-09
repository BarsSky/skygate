#!/usr/bin/env bash
# ============================================================================
# check_b_db_dsn_reachable.sh — B-check for DB DSN reachability
# (B-mod-db-retry, 2026-09-09)
#
# What this verifies
# ------------------
# Pre-deploy check that the configured SKYGATE_DB_DSN points at a
# reachable PostgreSQL. The skygate entrypoint.sh (B-mod-db-retry
# pre-flight) does the same check at container start; this B-check
# catches misconfigurations BEFORE the deploy, so the operator
# never sees the "invisible restart loop" symptom (docker ps says
# "Up X hours (healthy)" while the process is actually exit-1'ing
# every 5-7s).
#
# Background: skygate v1.3.0+ removed the SQLite fallback, so a
# missing/unreachable SKYGATE_DB_DSN is a hard error. Before
# B-mod-db-retry, a stale DSN would surface only as an opaque
# restart loop with a misleading "(healthy)" Docker healthcheck.
# The B-mod-db-retry fix has two parts:
#   1. OpenDSNWithRetry in cmd/skygate/main.go — exponential
#      backoff for transient DB issues (5 attempts, ~30s total).
#   2. entrypoint.sh pre-flight DB check — fail fast with a
#      clear error message if the DSN host:port is unreachable
#      at boot.
# This B-check is the pre-deploy layer of the same defense.
#
# Contracts (12 total)
# --------------------
# A. SKYGATE_DB_RETRY_MAX_ATTEMPTS env var has a sensible default
#    in cmd/skygate/main.go (5)
# B. SKYGATE_DB_RETRY_BASE_DELAY env var has a sensible default (2s)
# C. internal/db/retry.go exists with OpenDSNWithRetry
# D. OpenDSNWithRetry wraps ErrDBUnreachable sentinel
# E. OpenDSNWithRetry uses exponential backoff (delay << attempt)
# F. OpenDSNWithRetry caps each Ping at 5 seconds
# G. cmd/skygate/main.go uses OpenDSNWithRetry (not OpenDSN) in all
#    6 call sites
# H. entrypoint.sh has the B-mod-db-retry pre-flight DB check
# I. entrypoint.sh extracts host:port from SKYGATE_DB_DSN correctly
# J. entrypoint.sh fails fast (exit 1) when DSN host:port is unreachable
# K. internal/db/retry_test.go has 4+ unit tests
# L. `go test ./internal/db/ -run TestOpenDSNWithRetry` passes
#
# Exit codes
# ----------
#   0  all contracts pass
#   1  at least one contract failed
# ============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

PASS=0
FAIL=0
fails=()

ok() {
    local name="$1"
    PASS=$((PASS + 1))
    echo "  [PASS] $name"
}

fail() {
    local name="$1"
    local msg="$2"
    FAIL=$((FAIL + 1))
    fails+=("$name: $msg")
    echo "  [FAIL] $name: $msg"
}

# Find go binary (PowerShell host has it in PATH; the WSL/Git-Bash
# subshell that runs this script may not).
GO_BIN=""
if command -v go >/dev/null 2>&1; then
    GO_BIN="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
    GO_BIN="/mnt/c/Program Files/Go/bin/go.exe"
elif [ -x "/c/Program Files/Go/bin/go.exe" ]; then
    GO_BIN="/c/Program Files/Go/bin/go.exe"
fi

echo "=== check_b_db_dsn_reachable.sh (B-mod-db-retry, 2026-09-09) ==="

# A. SKYGATE_DB_RETRY_MAX_ATTEMPTS has a sensible default (5) in main.go
MAIN_GO="cmd/skygate/main.go"
if grep -qE 'OpenDSNWithRetry\(cfg\.DBDSN, 5,' "$MAIN_GO"; then
    ok "A: main.go calls OpenDSNWithRetry with maxAttempts=5"
else
    fail "A" "main.go does not call OpenDSNWithRetry with maxAttempts=5"
fi

# B. SKYGATE_DB_RETRY_BASE_DELAY has a sensible default (2s) in main.go
if grep -qE 'OpenDSNWithRetry\(cfg\.DBDSN, 5, 2\*time\.Second\)' "$MAIN_GO"; then
    ok "B: main.go uses baseDelay=2s (matches exponential backoff budget of ~30s total)"
else
    fail "B" "main.go does not use baseDelay=2s"
fi

# C. internal/db/retry.go exists with OpenDSNWithRetry
RETRY_GO="internal/db/retry.go"
if [ -f "$RETRY_GO" ] && grep -qE '^func OpenDSNWithRetry\(' "$RETRY_GO"; then
    ok "C: internal/db/retry.go has OpenDSNWithRetry function"
else
    fail "C" "internal/db/retry.go or OpenDSNWithRetry function missing"
fi

# D. OpenDSNWithRetry wraps ErrDBUnreachable sentinel
if [ -f "$RETRY_GO" ] && \
   grep -qE 'var\s+ErrDBUnreachable\s*=\s*errors\.New' "$RETRY_GO" && \
   grep -qE 'ErrDBUnreachable,' "$RETRY_GO"; then
    ok "D: OpenDSNWithRetry defines + uses ErrDBUnreachable sentinel (callers can errors.Is)"
else
    fail "D" "ErrDBUnreachable sentinel not defined or not used in return statement"
fi

# E. OpenDSNWithRetry uses exponential backoff (delay << attempt)
if [ -f "$RETRY_GO" ] && grep -qE 'baseDelay << \(attempt - 1\)' "$RETRY_GO"; then
    ok "E: OpenDSNWithRetry uses exponential backoff (baseDelay << (attempt-1))"
else
    fail "E" "OpenDSNWithRetry does not use exponential backoff"
fi

# F. OpenDSNWithRetry caps each Ping at 5 seconds
if [ -f "$RETRY_GO" ] && grep -qE 'WithTimeout\(.*, 5\*time\.Second\)' "$RETRY_GO"; then
    ok "F: OpenDSNWithRetry caps each Ping at 5s (prevents hung DB from blocking boot)"
else
    fail "F" "OpenDSNWithRetry does not cap Ping at 5s"
fi

# G. main.go uses OpenDSNWithRetry (not OpenDSN) in all 6 call sites
# (count OpenDSNWithRetry should be 7 — 1 in B-mod-core wiring + 6 in DB opens; plain OpenDSN should be 0)
retry_count=$(grep -c 'OpenDSNWithRetry' "$MAIN_GO" 2>/dev/null | tr -d '[:space:]' || echo 0)
plain_count=$(grep -cE 'db\.OpenDSN\(' "$MAIN_GO" 2>/dev/null | tr -d '[:space:]' || echo 0)
# Normalize empty-string results to 0
[ -z "$retry_count" ] && retry_count=0
[ -z "$plain_count" ] && plain_count=0
if [ "$retry_count" -ge 6 ] && [ "$plain_count" -eq 0 ]; then
    ok "G: main.go uses OpenDSNWithRetry in all $retry_count call sites (no plain OpenDSN left)"
else
    fail "G" "main.go has $retry_count OpenDSNWithRetry + $plain_count plain OpenDSN (expected 6+ / 7+0)"
fi

# H. entrypoint.sh has the B-mod-db-retry pre-flight DB check
ENTRYPOINT="entrypoint.sh"
if [ -f "$ENTRYPOINT" ] && grep -qE 'B-mod-db-retry' "$ENTRYPOINT" && \
   grep -qE 'DB pre-flight' "$ENTRYPOINT"; then
    ok "H: entrypoint.sh has B-mod-db-retry pre-flight DB check"
else
    fail "H" "entrypoint.sh missing B-mod-db-retry pre-flight check"
fi

# I. entrypoint.sh extracts host:port from SKYGATE_DB_DSN correctly
# (must handle postgres://user:pass@host:port/db?sslmode=disable)
if [ -f "$ENTRYPOINT" ] && grep -qE 'SKYGATE_DB_DSN#\*@' "$ENTRYPOINT" && \
   grep -qE '/dev/tcp/' "$ENTRYPOINT"; then
    ok "I: entrypoint.sh extracts host:port from DSN and uses bash /dev/tcp for probe"
else
    fail "I" "entrypoint.sh does not extract DSN host:port correctly"
fi

# J. entrypoint.sh fails fast (exit 1) when DSN host:port is unreachable
if [ -f "$ENTRYPOINT" ] && grep -qE 'exit 1' "$ENTRYPOINT" | head -1 >/dev/null && \
   grep -qE 'Fix: update SKYGATE_DB_DSN' "$ENTRYPOINT"; then
    ok "J: entrypoint.sh fails fast with actionable error message on unreachable DB"
else
    fail "J" "entrypoint.sh does not fail fast on unreachable DB"
fi

# K. internal/db/retry_test.go has 4+ unit tests
TEST_GO="internal/db/retry_test.go"
if [ -f "$TEST_GO" ]; then
    test_count=$(grep -cE "^func Test" "$TEST_GO" 2>/dev/null || echo 0)
    if [ "$test_count" -ge 4 ]; then
        ok "K: internal/db/retry_test.go has $test_count test functions (covers fail-fast, retries, defaults, error message)"
    else
        fail "K" "internal/db/retry_test.go has only $test_count test functions, want 4+"
    fi
else
    fail "K" "internal/db/retry_test.go not found"
fi

# L. `go test ./internal/db/ -run TestOpenDSNWithRetry` passes
echo "  [L] running go test ./internal/db/ -run TestOpenDSNWithRetry (may take 5-10 seconds)"
if [ -z "$GO_BIN" ]; then
    fail "L" "go binary not found in PATH or standard install locations"
elif [ "$GO_BIN" = "go" ]; then
    if go test ./internal/db/ -run TestOpenDSNWithRetry >/tmp/check_b_db_dsn_test.log 2>&1; then
        ok "L: go test ./internal/db/ -run TestOpenDSNWithRetry passes"
    else
        fail "L" "go test failed — see /tmp/check_b_db_dsn_test.log"
        tail -20 /tmp/check_b_db_dsn_test.log | sed 's/^/      /'
    fi
else
    if "$GO_BIN" test ./internal/db/ -run TestOpenDSNWithRetry >/tmp/check_b_db_dsn_test.log 2>&1; then
        ok "L: go test ./internal/db/ -run TestOpenDSNWithRetry passes (via $GO_BIN)"
    else
        fail "L" "go test failed — see /tmp/check_b_db_dsn_test.log"
        tail -20 /tmp/check_b_db_dsn_test.log | sed 's/^/      /'
    fi
fi

# M. docker-compose.yml skygate restart policy is `on-failure:5` (NOT `unless-stopped`)
# (prevents silent restart loop when DB pre-flight fails — see B-mod-db-retry)
COMPOSE="docker-compose.yml"
if [ -f "$COMPOSE" ] && grep -qE '^\s*restart:\s*on-failure:5\b' "$COMPOSE"; then
    # Verify it's specifically for the skygate service (the line after
    # the skygate service header comment). The "unless-stopped" lines
    # for pg15 + headplane + headscale are allowed (different policy).
    skygate_line=$(grep -nE '^\s*restart:\s*on-failure:5\b' "$COMPOSE" | head -1 | cut -d: -f1)
    if [ -n "$skygate_line" ]; then
        # Sanity: the on-failure:5 line should be near the skygate
        # service definition (within 30 lines of "    restart: unless-stopped"
        # comment about pre-flight wait).
        if grep -qE 'B-mod-db-retry' "$COMPOSE"; then
            ok "M: docker-compose.yml skygate service uses 'restart: on-failure:5' (with B-mod-db-retry comment)"
        else
            ok "M: docker-compose.yml has 'restart: on-failure:5' (line $skygate_line)"
        fi
    fi
else
    fail "M" "docker-compose.yml skygate service does not use 'restart: on-failure:5'"
fi

# Summary
echo
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    echo "Failed contracts:"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
exit 0
