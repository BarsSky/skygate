#!/usr/bin/env bash
# check_b334_pg_test_coverage.sh
#
# 2026-10-01 (B334) — the PostgreSQL half of the test suite must actually run.
#
# THE MEASUREMENT
# ---------------
# `db.OpenTestPG` SKIPs when `SKYGATE_TEST_PG_DSN` is unset, and every
# PostgreSQL-gated test in the tree goes through it (or through the same
# `os.Getenv` check). Measured 2026-10-01, the packages that call
# `OpenTestPG(`:
#
#	internal/db             13 call sites
#	internal/feature/admin  18 call sites
#	internal/acl             4 call sites
#
# CI's `test-pg` job ran `go test ./internal/db/...`, and `verify_pre_deploy.sh`
# never set the variable at all. So the live-PG tests in
# `internal/feature/admin` and `internal/acl` ran NOWHERE: CI never reached
# those packages and the gate SKIPped them, while both reported green. That is
# the TD-24 gap, and it is not hypothetical — the 2026-09-25 gate audit found
# two real PostgreSQL-only regressions (a SQLite-only `INSERT OR IGNORE` on a
# shared path in `internal/mesh/mesh.go` and `internal/subnet/shares.go`) that
# no job exercised, because no job ran those paths against PostgreSQL.
#
# This block fixes the coverage and makes it impossible to lose silently:
# CI's job now runs `go test ./...`, and the gate prints whether PostgreSQL
# coverage is enabled on every single run.
#
# It also fixes a documentation defect found while measuring: the recipe in
# `docs/operations.md` §15 told the operator to prove the DSN can create a
# schema with `CREATE SCHEMA pg_gate_probe`. PostgreSQL REJECTS that name —
# `pg_` is reserved for system schemas ("unacceptable schema name") — so the
# documented sanity check failed on a correctly configured server and looked
# like the permission problem it was meant to rule out.
#
# CONTRACTS
#   A. CI's test-pg job covers the whole tree, not one package
#   B. the gate states its PostgreSQL coverage on every run, and cannot invent one
#   C. the documented DSN recipe is correct (no reserved pg_ schema name)
#   D. measurement: the two previously-uncovered packages really do run their
#      PG-gated tests when the DSN is present, and really do skip without it
#   E. tracked by git (AGENTS trap #11) and registered in the catalog

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B334: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CI=.github/workflows/ci.yml
GATE=scripts/verify_pre_deploy.sh
OPS=docs/operations.md

# Resolve the Go binary the same way scripts/verify_pre_deploy.sh does: bash's
# PATH lookup cannot handle the space in "C:\Program Files\Go\bin", so the
# Windows install is probed by absolute path.
GO_BIN=""
if [ -n "${GO:-}" ]; then
  GO_BIN="$GO"
elif command -v go >/dev/null 2>&1; then
  GO_BIN="go"
else
  for cand in \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go"; do
    if [ -x "$cand" ]; then
      GO_BIN="$cand"
      break
    fi
  done
fi

hdr "B334 — the PostgreSQL half of the suite must actually run"

# ---------------------------------------------------------------------------
hdr "A. CI's test-pg job covers the tree, not one package"
# ---------------------------------------------------------------------------
if [ -f "$CI" ]; then
  ok "A1: $CI exists"
else
  bad "A1: $CI is missing"
fi

# Extract the test-pg job body: from the 2-space-indented `test-pg:` line to
# the next 2-space-indented job key. Everything after `jobs:` is at that
# indent, so this is the whole job (services, steps and all).
JOB="$(awk '
  /^  test-pg:[[:space:]]*$/ { inside=1; print; next }
  inside && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { exit }
  inside { print }
' "$CI" 2>/dev/null)"

if [ -n "$JOB" ]; then
  ok "A2: $CI declares the test-pg job"
else
  bad "A2: could not extract the test-pg job from $CI"
fi

RUNTES="$(printf '%s\n' "$JOB" | grep -E '^[[:space:]]*run: go test' | sed 's/^[[:space:]]*run:[[:space:]]*//')"
if printf '%s' "$RUNTES" | grep -qE 'go test \./\.\.\.'; then
  ok "A3: the job runs the WHOLE tree (found: $RUNTES)"
else
  bad "A3: the job does not run './...' — found '$RUNTES'. A package-scoped run leaves every other OpenTestPG package skipping; the scope is the bug (TD-24)"
fi
if printf '%s' "$RUNTES" | grep -qE 'go test \./internal/db/\.\.\.'; then
  bad "A4: the job is still pinned to './internal/db/...' — internal/feature/admin (18 OpenTestPG sites) and internal/acl (4) would skip again"
else
  ok "A4: the old single-package scope is gone"
fi
if printf '%s\n' "$JOB" | grep -q 'SKYGATE_TEST_PG_DSN:'; then
  ok "A5: the job still passes SKYGATE_TEST_PG_DSN to the test run"
else
  bad "A5: the job no longer sets SKYGATE_TEST_PG_DSN — without it every PG-gated test skips and the job proves nothing"
fi
if printf '%s\n' "$JOB" | grep -qE '^[[:space:]]*timeout-minutes:'; then
  ok "A6: the job has an explicit timeout-minutes"
else
  bad "A6: the job has no timeout-minutes — a hung PG test would be reported as a cancelled job, which hides the reason (B281)"
fi
if printf '%s\n' "$JOB" | grep -qi 'internal/feature/admin' && printf '%s\n' "$JOB" | grep -qi 'internal/acl'; then
  ok "A7: the job's comment names the two packages the widening was measured for"
else
  bad "A7: the job's comment does not name internal/feature/admin and internal/acl — the scope change has to carry its evidence, otherwise the next reader narrows it back"
fi

# ---------------------------------------------------------------------------
hdr "B. the gate states its database coverage and cannot invent it"
# ---------------------------------------------------------------------------
if grep -q 'PostgreSQL test coverage' "$GATE"; then
  ok "B1: $GATE prints a PostgreSQL-coverage banner"
else
  bad "B1: $GATE says nothing about PostgreSQL coverage — the gate reports PASS while exercising SQLite alone, which is exactly the silent gap TD-24 describes"
fi
if grep -q 'PostgreSQL test coverage: ENABLED' "$GATE" && grep -q 'PostgreSQL test coverage: SQLite only' "$GATE"; then
  ok "B2: the banner distinguishes both states (ENABLED / SQLite only)"
else
  bad "B2: the banner does not distinguish the two states, so it cannot tell the operator which database was exercised"
fi
if grep -q 'docs/operations.md' "$GATE" && awk '/PostgreSQL test coverage/{found=1} found&&/docs\/operations.md/{hit=1} END{exit !hit}' "$GATE"; then
  ok "B3: the banner points at docs/operations.md for the recipe"
else
  bad "B3: the banner does not point at docs/operations.md §15 — the operator sees the gap and no way to close it"
fi
if grep -qE '^[[:space:]]*(export[[:space:]]+)?SKYGATE_TEST_PG_DSN=' "$GATE"; then
  bad "B4: the gate ASSIGNS SKYGATE_TEST_PG_DSN. It must only report what the environment provides: a wrong DSN guessed by the gate would create test schemas in whatever database it names"
else
  ok "B4: the gate only reports the DSN, it never invents one"
fi
if grep -qE "'\\\$GO' test \./\.\.\." "$GATE"; then
  ok "B5: B1 still runs go test ./... so an exported DSN reaches every package"
else
  bad "B5: B1's whole-tree 'go test ./...' is gone — a narrower run would leave most PG-gated tests skipping no matter what the operator exports"
fi

# ---------------------------------------------------------------------------
hdr "C. the documented DSN recipe is correct"
# ---------------------------------------------------------------------------
if grep -q '^## 15\. Running the guarantee catalog with PostgreSQL coverage' "$OPS"; then
  ok "C1: $OPS §15 is present"
else
  bad "C1: $OPS lost §15 — the DSN recipe and the CREATE SCHEMA permission contract have no home"
fi
# A flattened copy of the section's prose, so a phrase that happens to wrap
# across two lines still matches. The contract is about what the section SAYS,
# not about where the line breaks fall.
OPS_FLAT="$(tr '\n' ' ' < "$OPS" 2>/dev/null)"

if printf '%s' "$OPS_FLAT" | grep -q 'on the database' && grep -q 'CREATE SCHEMA' "$OPS" && grep -q '42501' "$OPS"; then
  ok "C2: the section explains that the role needs CREATE on the database OpenTestPG creates schemas in, and shows the 42501 error it produces when it does not"
else
  bad "C2: the section no longer explains the CREATE-on-database requirement, which is the failure an operator hits first (SQLSTATE 42501)"
fi
if grep -qE 'CREATE SCHEMA pg_gate_probe|CREATE SCHEMA pg_' "$OPS"; then
  bad "C3: the recipe still probes with a pg_-prefixed schema name. PostgreSQL reserves the pg_ prefix for system schemas, so 'CREATE SCHEMA pg_gate_probe' answers 'unacceptable schema name' and the sanity check fails on a correctly configured server (measured on PostgreSQL 18.4)"
else
  ok "C3: the recipe does not use a reserved pg_ schema name"
fi
if grep -q 'gate_probe' "$OPS"; then
  ok "C4: the recipe probes with a name PostgreSQL accepts (gate_probe)"
else
  bad "C4: the recipe has no working probe schema — the operator cannot verify the DSN before a long gate run"
fi
if grep -qi 'reserved for system schemas\|prefix pg_ is reserved\|reserved by' "$OPS"; then
  ok "C5: the section records WHY the probe name matters"
else
  bad "C5: the section does not explain the reserved prefix, so the next edit can reintroduce it"
fi

# ---------------------------------------------------------------------------
hdr "D. measurement — the previously-uncovered packages really run their PG tests"
# ---------------------------------------------------------------------------
ACL_TEST='TestGenerateACLForPlane_B1883_NoDevicePref_NoPin'
ADMIN_TEST='TestInfraHeadscaleUserID_B251_Happy'

# D1: the packages named in the CI comment and docs really are the ones that gate
# on PG — discovered through BOTH aliases.
#
# B338. The first version of this measurement grepped `OpenTestPG(` only and
# concluded "three packages: internal/db, internal/feature/admin, internal/acl".
# That was WRONG: `db.OpenForTest` is a second, equally live gate (it SKIPs
# without the DSN and delegates to OpenTestPG with it), and two packages use it —
# `internal/monitoring` (11 call sites) and `internal/invite` (7). The real set is
# FIVE. The gap was not academic: the PG-enabled gate run then failed B1 on three
# tests in internal/monitoring that had been silently skipping since B273/B279
# and had never run in CI either (test-pg was scoped to ./internal/db/...).
# A measurement that is narrower than the thing it measures is how a whole
# package's coverage disappears, so the discovery is asserted here.
missing_pkgs=""
for p in internal/db internal/feature/admin internal/monitoring internal/invite internal/acl; do
  if grep -rqsE 'OpenTestPG\(|OpenForTest\(' "$p" --include='*_test.go'; then
    :
  else
    missing_pkgs="$missing_pkgs $p"
  fi
done
if [ -z "$missing_pkgs" ]; then
  ok "D1: all five PG-gated packages call db.OpenTestPG or db.OpenForTest (the B338-measured set)"
else
  bad "D1: these packages no longer call db.OpenTestPG/db.OpenForTest:$missing_pkgs — the measured package set in the CI comment and docs is stale"
fi

# D1b: the SET guard. Tests reach PostgreSQL in three legitimate ways —
# db.OpenTestPG(t), db.OpenForTest(t) (which SKIPs without the DSN and delegates
# to OpenTestPG with it) and a raw os.Getenv("SKYGATE_TEST_PG_DSN") check — so
# "no other way" is not the contract. The contract is that the DISCOVERED set
# equals the DOCUMENTED set: a new package that starts gating on PG must show up
# here and force the CI comment and docs to be updated with it. That is the
# failure mode this exists for: the first measurement grepped one alias out of
# three and silently lost two packages.
DISCOVERED=""
for f in $(git ls-files '*_test.go' 2>/dev/null); do
  grep -qE 'OpenTestPG\(|OpenForTest\(|SKYGATE_TEST_PG_DSN|pgTestDSN\(' "$f" 2>/dev/null || continue
  d="$(dirname "$f")"
  case " $DISCOVERED " in *" $d "*) ;; *) DISCOVERED="$DISCOVERED $d" ;; esac
done
DISCOVERED="$(printf '%s\n' $DISCOVERED | sort | tr '\n' ' ' | sed 's/ *$//')"
# B367 (2026-10-09): internal/cluster joined the set. Its drain/remove and
# failover statements are the ones that broke with SQLSTATE 22P02 on PostgreSQL,
# so the regression tests that call the REAL functions now gate on the DSN there
# too — the discovery below is what forced this list to be updated, exactly as
# this contract intends.
DOCUMENTED="$(printf '%s\n' cmd/skygate internal/acl internal/backup internal/cluster internal/db internal/feature/admin internal/headscale internal/invite internal/monitoring | sort | tr '\n' ' ' | sed 's/ *$//')"
if [ "$DISCOVERED" = "$DOCUMENTED" ]; then
  ok "D1b: the PG-gated package set matches the documented one (9 packages: $DISCOVERED)"
else
  bad "D1b: the PG-gated package set CHANGED. discovered: '$DISCOVERED' documented: '$DOCUMENTED'. Update this check, the CI comment in .github/workflows/ci.yml and docs/operations.md §15 together — a measured set that is narrower than reality is how internal/monitoring and internal/invite lost their coverage (B338)."
fi

# D2/D3: without the DSN the tests must SKIP — this is what makes the WITH-DSN
# assertion below meaningful (it proves the probe can tell the two apart).
if [ -z "$GO_BIN" ]; then
  skip "D2-D4: no usable go binary found (set GO=/path/to/go) — run this check on the VM"
else
  no_dsn_out="$(env -u SKYGATE_TEST_PG_DSN "$GO_BIN" test ./internal/acl/ -run "^${ACL_TEST}\$" -count=1 -v 2>&1)"
  if printf '%s' "$no_dsn_out" | grep -q -- '--- SKIP'; then
    ok "D2: without the DSN, internal/acl's PG test SKIPs (the probe can detect missing coverage)"
  else
    bad "D2: internal/acl's PG-gated test did NOT skip without SKYGATE_TEST_PG_DSN. Either the test stopped going through OpenTestPG, or it now runs against something else: $(printf '%s' "$no_dsn_out" | tail -3 | tr '\n' ' ')"
  fi
fi

if [ -z "$GO_BIN" ]; then
  :
elif [ -n "${SKYGATE_TEST_PG_DSN:-}" ]; then
  acl_out="$("$GO_BIN" test ./internal/acl/ -run "^${ACL_TEST}\$" -count=1 -v 2>&1)"
  if printf '%s' "$acl_out" | grep -q -- '--- PASS'; then
    ok "D3: with the DSN set, internal/acl's PG test really ran (a package CI never reached before)"
  elif printf '%s' "$acl_out" | grep -q -- '--- SKIP'; then
    bad "D3: SKYGATE_TEST_PG_DSN is set but internal/acl's PG test STILL skipped — the DSN is not reaching the test or OpenTestPG rejects it: $(printf '%s' "$acl_out" | tail -3 | tr '\n' ' ')"
  else
    bad "D3: internal/acl's PG test neither passed nor skipped with the DSN set: $(printf '%s' "$acl_out" | tail -5 | tr '\n' ' ')"
  fi

  admin_out="$("$GO_BIN" test ./internal/feature/admin/ -run "^${ADMIN_TEST}\$" -count=1 -v 2>&1)"
  if printf '%s' "$admin_out" | grep -q -- '--- PASS'; then
    ok "D4: with the DSN set, internal/feature/admin's PG test really ran (a package CI never reached before)"
  elif printf '%s' "$admin_out" | grep -q -- '--- SKIP'; then
    bad "D4: SKYGATE_TEST_PG_DSN is set but internal/feature/admin's PG test STILL skipped: $(printf '%s' "$admin_out" | tail -3 | tr '\n' ' ')"
  else
    bad "D4: internal/feature/admin's PG test neither passed nor skipped with the DSN set: $(printf '%s' "$admin_out" | tail -5 | tr '\n' ' ')"
  fi

  # D5/D6 (B338): the two packages the FIRST measurement missed, because it
  # grepped `OpenTestPG(` alone and they gate through `db.OpenForTest`. With the
  # DSN set their PG tests must really run — that is what proves the discovery
  # widening is not cosmetic. Before this, internal/monitoring's PG tests had
  # been silently skipping since B273/B279 and three of them were broken.
  for pair in "internal/monitoring:TestTick_DegradedTransition_RecordedButNotAlerted" \
              "internal/monitoring:TestTick_AutoSync_ClassTagOnlyLeavesRowAlone_B338" \
              "internal/invite:TestApplyBridgeWritesShareRow"; do
    pkg="${pair%%:*}"; tname="${pair##*:}"
    pout="$("$GO_BIN" test "./${pkg}/" -run "^${tname}\$" -count=1 -v 2>&1)"
    if printf '%s' "$pout" | grep -q -- '--- PASS'; then
      ok "D5: with the DSN set, ${pkg}::${tname} really ran (a package the first measurement missed)"
    elif printf '%s' "$pout" | grep -q -- '--- SKIP'; then
      bad "D5: SKYGATE_TEST_PG_DSN is set but ${pkg}::${tname} still SKIPPED — the package gates through db.OpenForTest, so it must run: $(printf '%s' "$pout" | tail -3 | tr '\n' ' ')"
    else
      bad "D5: ${pkg}::${tname} neither passed nor skipped with the DSN set: $(printf '%s' "$pout" | tail -5 | tr '\n' ' ')"
    fi
  done
else
  skip "D3-D4: SKYGATE_TEST_PG_DSN is not set — the WITH-DSN half of the measurement skips (recipe: docs/operations.md §15). The gate printed 'PostgreSQL test coverage: SQLite only' for this run"
fi
# ---------------------------------------------------------------------------
hdr "E. tracked by git and registered in the catalog"
# ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b334_pg_test_coverage.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: scripts/check_b334_pg_test_coverage.sh is NOT tracked by git (AGENTS trap #11: .gitignore can eat a new script)"
fi
if grep -q 'run_check "B334"' "$GATE"; then
  ok "E2: $GATE registers B334"
else
  bad "E2: $GATE does not register B334 — the contract would never run"
fi
if grep -q '\*\*B334\*\*' AGENTS.md; then
  ok "E3: AGENTS.md's block index carries B334"
else
  bad "E3: AGENTS.md has no B334 entry (AGENTS rule 2)"
fi

printf '\n\033[1mB334 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
