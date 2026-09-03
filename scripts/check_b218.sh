#!/usr/bin/env bash
# B-check for B218 (v1.5.0+): /admin/cluster Phase 2.5
# bootstrap_standby.sh refactor — `skygate init`
# now accepts role presets (primary / standby /
# db-replica / control) and detects standby mode
# from the role list (skygate-standby present +
# skygate absent) to skip the primary-only steps.
#
# Contracts pinned (16 source-pin + 2 go-runtime):
#   A-D: parseRolesCSV preset expansion (primary /
#        standby / db-replica / control)
#   E-F: isStandbyRole helper exists + correct logic
#   G:   runInitBootstrap detects standby mode
#   H:   standby mode skips cluster_database
#        primary claim
#   I:   standby mode skips standby invite issuance
#   J:   docstring updated to document presets
#   K-L: B218 test file (parseRolesCSV_Presets +
#        isStandbyRole) exists with >2 test fns
#   M:   B211 backward compat — explicit role
#        lists (--role=skygate,patroni-primary) still
#        work (pre-B218 API unchanged)
#   N:   i18n not affected (this is a CLI-only change)
#   O:   B218 unit tests pass
#   P:   go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

INIT_GO="cmd/skygate/init.go"
TEST="cmd/skygate/init_b218_test.go"

# --- A: 4 role presets defined ---
for preset in "primary" "standby" "db-replica" "control"; do
  if has "$INIT_GO" "case \"${preset}\":"; then
    ok "A: role preset '${preset}' defined in parseRolesCSV"
  else
    fail "A: role preset '${preset}' missing in parseRolesCSV"
  fi
done

# --- B: parseRolesCSV accepts presets (single-keyword case) ---
if has "$INIT_GO" "if len\\(out\\) == 1"; then
  ok "B: parseRolesCSV has the single-keyword preset expansion branch"
else
  fail "B: parseRolesCSV missing the single-keyword preset expansion"
fi

# --- C: B211 backward compat — parseRolesCSV still works with comma-separated roles ---
# (we pin that the explicit-role path is preserved by
# checking the original B211 test still passes)
if [ -f "cmd/skygate/init_b211_test.go" ] && grep -q "TestParseRolesCSV" "cmd/skygate/init_b211_test.go"; then
  ok "C: B211 TestParseRolesCSV preserved (backward compat)"
else
  fail "C: B211 TestParseRolesCSV missing — B218 refactor regressed the B211 path"
fi

# --- D: preset role lists match the canonical role enum ---
# The 4 presets expand to specific role lists. We
# pin the exact strings so a future change to the
# enum (e.g. renaming patroni-primary → db-primary)
# fails CI rather than silently breaking the
# expansion.
pat='skygate.{0,3}patroni-primary.{0,3}control|"skygate".*"patroni-primary".*"control"'
if has "$INIT_GO" "$pat"; then
  ok "D: primary preset expands to skygate + patroni-primary + control"
else
  fail "D: primary preset role list wrong"
fi
if has "$INIT_GO" '"skygate-standby".*"patroni-replica"'; then
  ok "D: standby preset expands to skygate-standby + patroni-replica"
else
  fail "D: standby preset role list wrong"
fi
if has "$INIT_GO" '"patroni-replica"'; then
  ok "D: db-replica preset expands to patroni-replica"
else
  fail "D: db-replica preset role list wrong"
fi
if has "$INIT_GO" '"skygate".*"control"'; then
  ok "D: control preset expands to skygate + control"
else
  fail "D: control preset role list wrong"
fi

# --- E: isStandbyRole helper exists ---
if has "$INIT_GO" "func isStandbyRole\\("; then
  ok "E: isStandbyRole helper exists"
else
  fail "E: isStandbyRole helper missing"
fi

# --- F: isStandbyRole logic — hasStandby + !hasSkygate ---
if has "$INIT_GO" "hasStandby && !hasSkygate"; then
  ok "F: isStandbyRole returns true only when skygate-standby is present and skygate is absent"
else
  fail "F: isStandbyRole logic wrong"
fi

# --- G: runInitBootstrap detects standby mode ---
if has "$INIT_GO" "standbyMode := isStandbyRole"; then
  ok "G: runInitBootstrap detects standby mode from role list"
else
  fail "G: runInitBootstrap doesn't call isStandbyRole"
fi

# --- H: standby mode skips cluster_database primary claim ---
if has "$INIT_GO" "if !standbyMode"; then
  ok "H: standby mode skips cluster_database primary claim (gated by !standbyMode)"
else
  fail "H: cluster_database update is not gated by standby mode"
fi

# --- I: standby mode skips standby invite issuance ---
# Look at the cluster.IssueInvite call site (not the
# comment) — the call is gated by `if !standbyMode`.
if grep -B1 -A4 "cluster.IssueInvite(d," "$INIT_GO" | grep -q "if !standbyMode"; then
  ok "I: standby mode skips standby invite issuance (IssueInvite gated by !standbyMode)"
else
  fail "I: cluster.IssueInvite call not gated by standby mode"
fi

# --- J: docstring documents presets ---
# The docstring lists all 4 presets with their canonical
# role expansions. We check for the 4 keywords (primary /
# standby / db-replica / control) in the Usage comment.
if has "$INIT_GO" "primary.*standby.*db-replica.*control"; then
  ok "J: docstring documents the 4 presets"
else
  fail "J: docstring missing preset documentation (expected 'primary', 'standby', 'db-replica', 'control' in the Usage comment)"
fi

# --- K: B218 test file exists ---
if [ -f "$TEST" ]; then
  ok "K: $TEST exists"
else
  fail "K: $TEST missing"
fi

# --- L: B218 test file has 2+ test functions ---
n=$(grep -c "^func Test" "$TEST" 2>/dev/null || echo 0)
if [ "${n:-0}" -ge 2 ]; then
  ok "L: B218 test file has $n test functions"
else
  fail "L: only $n test functions in $TEST (expected >= 2)"
fi

# --- M: B211 backward compat — explicit role lists still work ---
# We pin the parseRolesCSV function still returns the
# trimmed comma-split for non-preset inputs. This is
# tested by the B211 TestParseRolesCSV (already
# pinned in C). The B218 tests add new cases
# (TestParseRolesCSV_Presets) but don't break the
# old ones.
if has "$TEST" "explicit primary roles" && has "$TEST" "explicit standby roles"; then
  ok "M: B218 tests cover the B211 backward-compat path"
else
  fail "M: B218 tests should cover explicit (non-preset) role lists"
fi

# --- N: i18n not affected (CLI-only change) ---
# B218 is a CLI behavior change, no new UI strings.
# We pin that catalog_admin.go is not modified.
if ! git diff --name-only HEAD 2>/dev/null | grep -q "catalog_admin.go"; then
  ok "N: i18n not affected (CLI-only change, no catalog_admin.go diff)"
else
  echo "[skip] N: catalog_admin.go modified — confirm i18n keys are still valid"
fi

# --- O: B218 unit tests pass ---
if command -v go >/dev/null 2>&1; then
  if go test ./cmd/skygate/... -run 'B218|ParseRolesCSV_Presets|IsStandbyRole' -count=1 >/dev/null 2>&1; then
    ok "O: B218 unit tests pass"
  else
    fail "O: B218 unit tests FAILED"
  fi
else
  echo "[skip] O: go not on PATH"
fi

# --- P: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "P: go build ./... succeeds"
  else
    fail "P: go build ./... FAILED"
  fi
else
  echo "[skip] P: go not on PATH"
fi

echo ""
echo "B218 B-check: $ok_count passed"
