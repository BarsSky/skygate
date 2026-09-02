#!/usr/bin/env bash
# check_b212.sh — B212 (v1.5.0+) `skygate join` cluster
# onboarding CLI. Pins the structural + wiring contracts
# so a future refactor that silently breaks the path
# (e.g. drops the DSN bootstrap, renames the subcommand,
# breaks the help text) is caught.
#
# Each `check` is one row; pass = exit 0, fail = exit 1.
# Run from the repo root:
#
#   bash scripts/check_b212.sh
#
# The script does NOT touch the DB (no live-verify
# needed for these — they're pure source / wiring
# pins). Live-verify on the agent is documented in
# AGENTS.md §B212 and exercised via
# scripts/b212_join_verify.sh.
set -euo pipefail

# REPO_ROOT resolution (same pattern as check_b211.sh).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ -n "${SKYGATE_REPO_ROOT:-}" ]]; then
  REPO_ROOT="$SKYGATE_REPO_ROOT"
else
  REPO_ROOT="$SCRIPT_DIR"
  while [[ "$REPO_ROOT" != "/" ]] && [[ ! -f "$REPO_ROOT/go.mod" ]]; do
    REPO_ROOT="$(dirname "$REPO_ROOT")"
  done
  if [[ ! -f "$REPO_ROOT/go.mod" ]]; then
    REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
  fi
fi
cd "$REPO_ROOT"

# Go binary resolution (auto-detect common install paths).
if [[ -n "${GO_BIN:-}" ]]; then
  GO="$GO_BIN"
elif command -v go >/dev/null 2>&1; then
  GO="go"
elif [[ -x "/c/Program Files/Go/bin/go.exe" ]]; then
  GO="/c/Program Files/Go/bin/go.exe"
elif [[ -x "/usr/local/go/bin/go" ]]; then
  GO="/usr/local/go/bin/go"
else
  GO="go"  # will fail Q/R/S, that's OK
fi

PASS=0
FAIL=0

check() {
  local label="$1"
  local want="$2"
  local got="$3"
  if [[ "$got" == "$want" ]]; then
    echo "[ok]   $label"
    PASS=$((PASS + 1))
  else
    echo "[FAIL] $label"
    echo "       want: $want"
    echo "       got:  $got"
    FAIL=$((FAIL + 1))
  fi
}

# A: cmd/skygate/join.go exists
[[ -f "$REPO_ROOT/cmd/skygate/join.go" ]]
check "A: cmd/skygate/join.go exists" "0" "$?"

# B: join.go has runJoin
grep -q "^func runJoin" "$REPO_ROOT/cmd/skygate/join.go"
check "B: runJoin dispatcher exists" "0" "$?"

# C: join.go has runJoinStatus
grep -q "^func runJoinStatus" "$REPO_ROOT/cmd/skygate/join.go"
check "C: runJoinStatus function exists" "0" "$?"

# D: main.go has the "join" case
grep -q 'case "join":' "$REPO_ROOT/cmd/skygate/main.go"
check "D: case \"join\" in main.go switch" "0" "$?"

# E: main.go dispatches to runJoin
grep -q "runJoin(os.Args\[2:\])" "$REPO_ROOT/cmd/skygate/main.go"
check "E: main.go dispatches to runJoin" "0" "$?"

# F: help text mentions "join [verb]"
grep -q "join \[verb\]" "$REPO_ROOT/cmd/skygate/main.go"
check "F: help text mentions 'join [verb]'" "0" "$?"

# G: runClusterJoin is enhanced (parseJoinArgs helper)
grep -q "^func parseJoinArgs" "$REPO_ROOT/cmd/skygate/cluster.go"
check "G: parseJoinArgs extracted helper" "0" "$?"

# H: runClusterJoin has local token sanity check
grep -q "cluster.VerifyToken" "$REPO_ROOT/cmd/skygate/cluster.go"
check "H: runClusterJoin has local token verify" "0" "$?"

# I: runClusterJoin has DSN bootstrap (--write-dsn-to flag)
grep -q "write-dsn-to" "$REPO_ROOT/cmd/skygate/cluster.go"
check "I: --write-dsn-to flag in runClusterJoin" "0" "$?"

# J: writeDSNEnvFile helper exists
grep -q "^func writeDSNEnvFile" "$REPO_ROOT/cmd/skygate/cluster.go"
check "J: writeDSNEnvFile helper exists" "0" "$?"

# K: JoinResponse has DSN field
grep -q "^	DSN " "$REPO_ROOT/internal/cluster/join.go"
check "K: JoinResponse has DSN field" "0" "$?"

# L: JoinResponse has PrimaryHost field
grep -q "^	PrimaryHost " "$REPO_ROOT/internal/cluster/join.go"
check "L: JoinResponse has PrimaryHost field" "0" "$?"

# M: substituteDSNTemplate helper exists
grep -q "^func substituteDSNTemplate" "$REPO_ROOT/internal/cluster/join.go"
check "M: substituteDSNTemplate helper exists" "0" "$?"

# N: readPrimaryHost helper exists
grep -q "^func readPrimaryHost" "$REPO_ROOT/internal/cluster/join.go"
check "N: readPrimaryHost helper exists" "0" "$?"

# O: cluster.Join calls substituteDSNTemplate
grep -q "substituteDSNTemplate" "$REPO_ROOT/internal/cluster/join.go"
check "O: cluster.Join calls substituteDSNTemplate" "0" "$?"

# P: unit tests exist for substituteDSNTemplate
[[ -f "$REPO_ROOT/internal/cluster/join_b212_test.go" ]]
check "P: internal/cluster/join_b212_test.go exists" "0" "$?"

# Q: unit tests exist for parseTokenAge / parseJoinArgs
[[ -f "$REPO_ROOT/cmd/skygate/join_b212_test.go" ]]
check "Q: cmd/skygate/join_b212_test.go exists" "0" "$?"

# R: go test passes (no DB required for B212 tests)
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO test ./internal/cluster/... ./cmd/skygate/ -run "TestSubstituteDSNTemplate|TestJoinResponse|TestParseTokenAge|TestParseJoinArgs|TestJoinState" -count=1 >/dev/null 2>&1; then
    check "R: go test (B212 unit tests) passes" "pass" "pass"
  else
    check "R: go test (B212 unit tests) passes" "pass" "fail"
  fi
else
  echo "[skip] R: go not on PATH — run on a host with go installed (e.g. the agent)"
fi

# S: go build works
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO build ./... >/dev/null 2>&1; then
    check "S: go build ./... succeeds" "pass" "pass"
  else
    check "S: go build ./... succeeds" "pass" "fail"
  fi
else
  echo "[skip] S: go not on PATH — run on a host with go installed"
fi

# T: skygate binary actually exposes the join subcommand
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  SKYGATE_TMP="$(mktemp -d)"
  trap 'rm -rf "$SKYGATE_TMP"' EXIT
  if $GO build -o "$SKYGATE_TMP/skygate" ./cmd/skygate 2>/dev/null; then
    JOIN_HELP="$("$SKYGATE_TMP/skygate" join --help 2>&1 | head -1)"
    if [[ "$JOIN_HELP" == "skygate join <verb> [args]" ]]; then
      check "T: skygate join --help prints usage" "match" "match"
    else
      check "T: skygate join --help prints usage" "match" "mismatch: $JOIN_HELP"
    fi
  else
    check "T: skygate join --help prints usage" "match" "could not build"
  fi
else
  echo "[skip] T: go not on PATH — run on a host with go installed"
fi

echo ""
echo "B212 B-check: $PASS passed, $FAIL failed"
[[ "$FAIL" == "0" ]]
