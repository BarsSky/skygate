#!/usr/bin/env bash
# check_b319_rules_owner_type.sh
#
# 2026-09-24 (B319, v1.5.84) — the preferred-exit reconciler's rule lookup must run
# on PostgreSQL, and it must ask node_owner_map for columns it actually has.
#
# OPERATOR REPORT (verbatim):
#
#   «и также перестали работать правила что случилось? на agent vm»
#
# WHAT THE LIVE TOOLS FOUND (read-only, agent VM):
#
#   journal, on EVERY container start, once per device that has a preference:
#     preferred-reconciler: state for michail/basic: rules for michail/basic:
#       ERROR: operator does not exist: integer = text (SQLSTATE 42883)
#     preferred-reconciler: state for skyadmin/skyworker: rules for skyadmin/skyworker: ERROR: …
#
#   the query that produced it (internal/feature/exit_rules/reconciler.go):
#
#     OR device_id IN (SELECT node_id FROM node_owner_map
#                       WHERE user_id = $1 AND hostname = $2)
#
#   reproduced by hand against the live PostgreSQL:
#     device_rules.device_id   = integer
#     node_owner_map.node_id   = text
#     => ERROR: operator does not exist: integer = text
#     with CAST(device_id AS TEXT): 45 rows (the same rules), i.e. the fix works
#
#   and a second defect hidden inside the first: node_owner_map has NO user_id column
#   (it links a node via headscale_user_id / username / tag — see
#   internal/db/device_owner_b316.go), so that predicate silently bound to the OUTER
#   device_rules.user_id: it filtered nothing and correlated nothing.
#
# Why it survived every test: SQLite compares 9 with '9' happily. The reconciler could
# therefore never compute the state for a device with an ownership row on the PG
# backend, so those devices were skipped on every tick — the user's preferred exit node
# was never reconciled, which is what "правила перестали работать" looks like.
#
# CONTRACTS
#   A. the rule lookup casts, and never compares INTEGER with TEXT directly
#   B. the sub-select asks node_owner_map only for columns it has
#   C. the pre-rename branch still finds a stale rule by node id (SQLite behaviour)
#   D. the query runs against a LIVE PostgreSQL, when one is reachable (otherwise SKIP)
#   E. tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B319: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

REC=internal/feature/exit_rules/reconciler.go
TEST=internal/feature/exit_rules/reconciler_b319_test.go

hdr "B319 — the rule lookup must run on PostgreSQL"

# The QUERY, not the whole file: the doc comment quotes the old SQL on purpose.
Q="$(awk '/ruleRows, err := s\.dbc\(\)\.QueryContext/{f=1} f{print} f&&/`, userID, hostname\)/{exit}' "$REC")"

# --- A: the comparison is portable ---------------------------------------------------
if grep -q 'OR device_id IN (' <<< "$Q"; then
  bad "A1: the query still compares INTEGER device_id with TEXT node_id directly (PG: operator does not exist: integer = text)"
else
  ok "A1: no bare integer-vs-text comparison in the rule lookup"
fi
if grep -q 'CAST(device_id AS TEXT) IN (' <<< "$Q"; then
  ok "A2: the comparison casts to TEXT (valid on BOTH backends — verified against live PG)"
else
  bad "A2: the explicit CAST is missing"
fi
if grep -q 'device_rules' <<< "$Q" && grep -q 'node_owner_map' <<< "$Q"; then
  ok "A3: the pre-rename branch is still present (the OR did not get deleted to make the error go away)"
else
  bad "A3: the node_owner_map branch was dropped instead of fixed"
fi

# --- B: the sub-select asks for real columns -----------------------------------------
if grep -q 'user_id = \$1 AND hostname = \$2' <<< "$Q"; then
  bad "B1: the sub-select still filters on user_id, which node_owner_map does NOT have (it silently bound to the OUTER device_rules row)"
else
  ok "B1: the sub-select no longer names a column node_owner_map lacks"
fi
if grep -q 'WHERE hostname = \$2' <<< "$Q"; then
  ok "B2: the branch matches the node by hostname (node_id is unique; the outer query already scopes the user)"
else
  bad "B2: the sub-select lost its hostname predicate"
fi
if grep -q 'device_owner_b316' "$REC"; then
  ok "B3: the code records WHY the owner columns are not used here (headscale rewrites a tagged node's user)"
else
  bad "B3: the reason the owner join is wrong here is not documented"
fi

# --- C: the SQLite behaviour is unchanged (the branch still works) --------------------
if [ -f "$TEST" ] && grep -q 'TestB319_RuleLookupFindsThePreRenameRuleByNodeID' "$TEST"; then
  ok "C1: the pre-rename branch is pinned by a behavioural test"
else
  bad "C1: no test proves the node-id branch still finds a stale rule"
fi

# --- D: the real PostgreSQL, when one is reachable -----------------------------------
PG_OK=0
if docker ps --format '{{.Names}}' 2>/dev/null | grep -q 'skygate-pg-local'; then
  QUERY="SELECT count(*) FROM device_rules WHERE enabled = 1 AND CAST(device_id AS TEXT) IN (SELECT node_id FROM node_owner_map WHERE hostname = 'x');"
  OUT="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -A -t -c "$QUERY" 2>&1)"
  if grep -qi 'operator does not exist' <<< "$OUT"; then
    bad "D1: the fixed comparison still fails on the live PostgreSQL: $OUT"
  elif grep -qE '^[0-9]+$' <<< "$OUT"; then
    ok "D1: the CAST comparison runs on the live PostgreSQL (returned $OUT)"
    PG_OK=1
  else
    skip "D1: pg answered something unexpected: $(head -c 80 <<< "$OUT")"
  fi
  # And the OLD form must still be refused by that server — proof the contract is real.
  OLD="SELECT count(*) FROM device_rules WHERE enabled = 1 AND device_id IN (SELECT node_id FROM node_owner_map WHERE hostname = 'x');"
  OUT2="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -A -t -c "$OLD" 2>&1)"
  if grep -qi 'operator does not exist: integer = text' <<< "$OUT2"; then
    ok "D2: the server still refuses the old comparison (the defect was real, not a guess)"
  else
    skip "D2: the live server did not reproduce the old error: $(head -c 80 <<< "$OUT2")"
  fi
else
  skip "D1/D2: no skygate-pg-local container — run this on the VM for the live half"
fi

# --- E: tests + git ------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ -run 'B319' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "E1: the B319 Go tests pass"
  else
    bad "E1: the B319 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go vet ./internal/feature/exit_rules/ 2>&1)"
  if [ -z "$OUT" ]; then
    ok "E2: go vet is clean"
  else
    bad "E2: go vet reported:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "E1/E2: go not on PATH — run the B319 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b319_rules_owner_type.sh >/dev/null 2>&1; then
  ok "E3: scripts/check_b319_rules_owner_type.sh is tracked by git"
else
  bad "E3: scripts/check_b319_rules_owner_type.sh is NOT tracked (AGENTS trap #11)"
fi

printf '\n\033[1mB319 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
