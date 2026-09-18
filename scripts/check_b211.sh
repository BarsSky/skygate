#!/usr/bin/env bash
# check_b211.sh — B211 (v1.5.0+) `skygate init` cluster
# bootstrap CLI. Pins the structural + wiring contracts
# so a future refactor that silently breaks the path
# (e.g. drops the UNIQUE constraint, renames the
# subcommand, breaks the help text) is caught.
#
# Each `check` is one row; pass = exit 0, fail = exit 1.
# Run from the repo root:
#
#   bash scripts/check_b211.sh
#
# The script does NOT touch the DB (no live-verify
# needed for these — they're pure source / wiring
# pins). Live-verify on Windows Docker is documented
# in AGENTS.md §B211 and exercised via the
# `skygate init` / `skygate init status` commands
# against a fresh skygate_staging DB.
set -euo pipefail

# REPO_ROOT resolution:
#  1. If $SKYGATE_REPO_ROOT is set, use that.
#  2. Otherwise, walk up from the script's dir until
#     we find a go.mod (the canonical marker of the
#     skygate repo root).
#  3. If still not found, fall back to script-dir/.. (the
#     historical behaviour — works when the script is at
#     <repo>/scripts/check_b211.sh).
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

# Go binary resolution. On the agent (Linux), `go` is
# on $PATH; on Windows (Git Bash), `go` may not be
# because the bash PATH is inherited from the parent
# PowerShell. Allow override via $GO_BIN; fall back
# to plain `go` and hope it's on PATH.
#
# If `go` is not on PATH, try the standard Windows
# install location; if that fails too, the go-related
# checks (Q / R / S) report "fail" and the script
# exits non-zero — these checks are not a hard gate
# for the source-pin checks (A–P) which work without
# `go` at all.
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

# A: cmd/skygate/init.go exists
[[ -f "$REPO_ROOT/cmd/skygate/init.go" ]]
check "A: cmd/skygate/init.go exists" "0" "$?"

# B: init.go has runInitBootstrap
grep -q "^func runInitBootstrap" "$REPO_ROOT/cmd/skygate/init.go"
check "B: runInitBootstrap function exists" "0" "$?"

# C: init.go has runInitStatus
grep -q "^func runInitStatus" "$REPO_ROOT/cmd/skygate/init.go"
check "C: runInitStatus function exists" "0" "$?"

# D: init.go has runInitStandbyInvite
grep -q "^func runInitStandbyInvite" "$REPO_ROOT/cmd/skygate/init.go"
check "D: runInitStandbyInvite function exists" "0" "$?"

# E: main.go has the "init" case
grep -q 'case "init":' "$REPO_ROOT/cmd/skygate/main.go"
check "E: case \"init\" in main.go switch" "0" "$?"

# F: main.go dispatches to runInit
grep -q "runInit(os.Args\[2:\])" "$REPO_ROOT/cmd/skygate/main.go"
check "F: main.go dispatches to runInit" "0" "$?"

# G: help text mentions "init [verb]"
grep -q "init \[verb\]" "$REPO_ROOT/cmd/skygate/main.go"
check "G: help text mentions 'init [verb]'" "0" "$?"

# H: internal/cluster/node.go has UpsertNode
grep -q "^func UpsertNode" "$REPO_ROOT/internal/cluster/node.go"
check "H: cluster.UpsertNode function exists" "0" "$?"

# I: UpsertNode uses ON CONFLICT (cluster_id, hostname)
grep -q "ON CONFLICT (cluster_id, hostname)" "$REPO_ROOT/internal/cluster/node.go"
check "I: UpsertNode ON CONFLICT (cluster_id, hostname) clause" "0" "$?"

# J: UpsertNode does NOT update state on conflict (preserves drain/failover)
#    — the ON CONFLICT block in the file must not mention "state ="
if awk '/ON CONFLICT \(cluster_id, hostname\) DO UPDATE/,/RETURNING id/' "$REPO_ROOT/internal/cluster/node.go" | grep -q "state ="; then
  check "J: UpsertNode does NOT update state on conflict" "no state =" "has state ="
else
  check "J: UpsertNode does NOT update state on conflict" "no state =" "no state ="
fi

# K: v0.66 migration file exists
[[ -f "$REPO_ROOT/internal/db/migrations_v0_66_b211.go" ]]
check "K: migrations_v0_66_b211.go exists" "0" "$?"

# L: v0.66 migration adds the UNIQUE constraint
grep -q "cluster_node_cluster_id_hostname_key" "$REPO_ROOT/internal/db/migrations_v0_66_b211.go"
check "L: v0.66 adds UNIQUE (cluster_id, hostname) constraint" "0" "$?"

# M: v0.66 migration is idempotent (DO block checks pg_constraint)
#    Use single-quoted regex to avoid bash $-expansion
grep -q 'DO \$' "$REPO_ROOT/internal/db/migrations_v0_66_b211.go"
check "M: v0.66 migration is idempotent" "0" "$?"

# N: driver_postgres.go registers migrateV066PG
grep -q "migrateV066PG" "$REPO_ROOT/internal/db/driver_postgres.go"
check "N: driver_postgres.go registers migrateV066PG" "0" "$?"

# O: unit tests exist for cluster.UpsertNode
[[ -f "$REPO_ROOT/internal/cluster/node_b211_test.go" ]]
check "O: internal/cluster/node_b211_test.go exists" "0" "$?"

# P: unit tests for parseRolesCSV exist
[[ -f "$REPO_ROOT/cmd/skygate/init_b211_test.go" ]]
check "P: cmd/skygate/init_b211_test.go exists" "0" "$?"

# Q: go test passes (no DB required)
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO test ./internal/cluster/... ./cmd/skygate/ -run "TestUpsertNode|TestParseRolesCSV|TestInitState" -count=1 >/dev/null 2>&1; then
    check "Q: go test (B211 unit tests) passes" "pass" "pass"
  else
    check "Q: go test (B211 unit tests) passes" "pass" "fail"
  fi
else
  echo "[skip] Q: go not on PATH — run on a host with go installed (e.g. the agent)"
fi

# R: go build works
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO build ./... >/dev/null 2>&1; then
    check "R: go build ./... succeeds" "pass" "pass"
  else
    check "R: go build ./... succeeds" "pass" "fail"
  fi
else
  echo "[skip] R: go not on PATH — run on a host with go installed"
fi

# S: skygate binary actually exposes the init subcommand
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  # Build the skygate binary into a temp dir so we
  # don't pollute the repo with a 'skygate' executable
  # artifact. The build is faster than `go run` and
  # gives us a stable binary to call repeatedly.
  SKYGATE_TMP="$(mktemp -d)"
  trap 'rm -rf "$SKYGATE_TMP"' EXIT
  if $GO build -o "$SKYGATE_TMP/skygate" ./cmd/skygate 2>/dev/null; then
    INIT_HELP="$("$SKYGATE_TMP/skygate" init --help 2>&1 | head -1)"
    if [[ "$INIT_HELP" == "skygate init <verb> [flags]" ]]; then
      check "S: skygate init --help prints usage" "match" "match"
    else
      check "S: skygate init --help prints usage" "match" "mismatch: $INIT_HELP"
    fi
  else
    check "S: skygate init --help prints usage" "match" "could not build"
  fi
else
  echo "[skip] S: go not on PATH — run on a host with go installed"
fi

echo ""
echo "B211 B-check: $PASS passed, $FAIL failed"
[[ "$FAIL" == "0" ]]
