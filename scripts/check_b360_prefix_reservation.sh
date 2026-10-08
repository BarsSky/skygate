#!/usr/bin/env bash
# check_b360_prefix_reservation.sh
#
# 2026-10-07 (B360) — a failover must be a RESERVATION, and a recovered exit node
# must get its prefixes back.
#
# THE LIVE CASE (measured on the reference deployment)
# ----------------------------------------------------
#   during the karolina outage  the operator set the panel override
#                               global_settings.prefix_owner_force_relay = emilia
#                               (audit prefix_owner_force, 2026-10-06 19:49:23Z)
#   while it was set            prefix_owner held 139 rows, all emilia, all
#                               source='global', while LoadClaims saw 194
#                               ip/subnet claims naming karolina — the data plane
#                               disagreed with the rules for ~19 hours
#   karolina returned           all three relays online, healthy=1, routes
#                               approved — and NOTHING returned, because the move
#                               had never been recorded as a failover and the
#                               override is a latch with no expiry and no surface
#   operator cleared it         the very next pass returned all 139 prefixes
#                               (source='explicit', changed=139) — the engine CAN
#                               return; it had no memory and no obligation to ask
#
# WHAT THIS CONTRACT GUARDS
#   A. the schema (V078, both chains, additive, SQLite through execDDL)
#   B. the RECORDING rules (a failover is reserved; a manual pin never is; a
#      genuine decision change clears the reservation — L-60)
#   C. the RETURN rules (hysteresis, proven relays, named skip reasons)
#   D. the PROTECTION (the counter is persisted; the window doubles and is capped)
#   E. the operator override is a tool, never a latch, and its state is visible
#   F. gofmt + the package tests
#   G. bookkeeping (the catalog registration is the lead's handoff — see the note)
#
# Exit: 0 = no FAIL, 1 = at least one FAIL. Live-state contracts SKIP, never FAIL.
set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B360: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

# Per-run scratch directory (B340 / AGENTS trap #13: never a fixed /tmp path — on
# the reference VM uid 0 cannot overwrite another user's file there).
SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MIG=internal/db/migrations_v0_78_prefix_reservation.go
PGREG=internal/db/driver_postgres.go
LITEREG=internal/db/driver_sqlite.go
PLANNER=internal/prefixowner/reservation_b360.go
PKG=internal/prefixowner/prefixowner.go
TESTS=internal/prefixowner/reservation_b360_test.go
# sync.go was split three ways (2026-10-08, pure move); the ownership
# reconcile call lives in sync_acl.go.
SYNC=internal/feature/exit_rules/sync_acl.go

# body_of <file> <header-regex> — the lines of one function, so a contract reads
# the CODE and not a comment that happens to quote it (the B352 D2 lesson).
body_of() {
  awk -v hdr="$2" '
    $0 ~ hdr {inside=1}
    inside {print}
    inside && /^}/ {exit}
  ' "$1"
}

hdr "B360 — the failover reservation, the return, and the protection"

# ─── A. the schema ──────────────────────────────────────────────────────────────
A_MISS=""
[ -f "$MIG" ] || A_MISS="${A_MISS}${MIG} does not exist (V078)"$'\n'
grep -q 'func migrateV078PG' "$MIG" 2>/dev/null || A_MISS="${A_MISS}migrateV078PG is missing"$'\n'
grep -q 'func migrateV078SQLite' "$MIG" 2>/dev/null || A_MISS="${A_MISS}migrateV078SQLite is missing"$'\n'
grep -q 'execSQLiteDDL' "$MIG" 2>/dev/null || A_MISS="${A_MISS}the SQLite side does not go through execSQLiteDDL (AGENTS rule 9)"$'\n'
for col in failover_from failover_at failover_flaps; do
  grep -q "ADD COLUMN IF NOT EXISTS $col" "$MIG" 2>/dev/null \
    || A_MISS="${A_MISS}the migration does not add prefix_owner.$col"$'\n'
done
# Destructive DDL is forbidden (contract B11): additive columns only.
if grep -nE 'DROP[[:space:]]+(TABLE|COLUMN|INDEX)|RENAME[[:space:]]+(TO|COLUMN)|TRUNCATE' "$MIG" 2>/dev/null \
    | grep -viE 'IF[[:space:]]+EXISTS' | grep -q .; then
  A_MISS="${A_MISS}the migration contains destructive DDL (B11 forbids DROP/RENAME/TRUNCATE)"$'\n'
fi
# Both chains, same version, same label, same SourceFile (B333 lock-step).
PG_LINE="$(grep -E '\{78, "' "$PGREG" 2>/dev/null | head -1)"
LITE_LINE="$(grep -E '\{78, "' "$LITEREG" 2>/dev/null | head -1)"
if [ -z "$PG_LINE" ] || [ -z "$LITE_LINE" ]; then
  A_MISS="${A_MISS}V078 is not registered in BOTH migration chains (a version in one chain only never reaches the other backend)"$'\n'
else
  PG_LABEL="$(printf '%s' "$PG_LINE" | sed -E 's/^[[:space:]]*\{78, "([^"]*)".*/\1/')"
  LITE_LABEL="$(printf '%s' "$LITE_LINE" | sed -E 's/^[[:space:]]*\{78, "([^"]*)".*/\1/')"
  [ "$PG_LABEL" = "$LITE_LABEL" ] \
    || A_MISS="${A_MISS}the V078 registry label drifted between the chains"$'\n'
  printf '%s' "$PG_LINE" | grep -q 'migrations_v0_78_prefix_reservation.go' \
    || A_MISS="${A_MISS}the PG entry does not name the migration file"$'\n'
  printf '%s' "$LITE_LINE" | grep -q 'migrations_v0_78_prefix_reservation.go' \
    || A_MISS="${A_MISS}the SQLite entry does not name the migration file"$'\n'
fi
if [ -z "$A_MISS" ]; then
  ok "A1: V078 adds failover_from/failover_at/failover_flaps to prefix_owner in BOTH chains, additively, SQLite through execSQLiteDDL"
else
  bad "A1: the reservation schema is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi
grep -q 'failover_flaps' internal/db/migrations_sqlite_schema_test.go 2>/dev/null \
  && ok "A2: the SQLite schema test pins the new columns (a native install cannot silently lose them)" \
  || bad "A2: internal/db/migrations_sqlite_schema_test.go does not mention the V078 columns"

# ─── B. the RECORDING rules ────────────────────────────────────────────────────
B_MISS=""
grep -q 'func reserveOnMove' "$PLANNER" || B_MISS="${B_MISS}reserveOnMove does not exist"$'\n'
grep -q 'reserveOnMove(&out\[i\]' "$PKG" \
  || B_MISS="${B_MISS}AssignWithReservations never records a reservation on the way out"$'\n'
grep -q 'failover_from' "$PKG" || B_MISS="${B_MISS}LoadExisting/Save do not carry failover_from"$'\n'
grep -q 'failover_flaps' "$PKG" || B_MISS="${B_MISS}LoadExisting/Save do not carry failover_flaps"$'\n'
# A manual pin is NEVER reserved: the guard must live inside reserveOnMove itself.
RESERVE_BODY="$(body_of "$PLANNER" '^func reserveOnMove')"
if printf '%s' "$RESERVE_BODY" | grep -q 'prev.Source == "manual"'; then
  ok "B1: reserveOnMove refuses to reserve a source='manual' row"
else
  bad "B1: reserveOnMove does not check for a manual pin — an operator's pin could be recorded as a failover and later RETURNED"
fi
if [ -z "$B_MISS" ]; then
  ok "B2: the reservation is recorded in the assignment pass and persisted in BOTH read and write paths"
else
  bad "B2: the recording path is incomplete:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi
# L-60: a reservation must not outlive its reason.
grep -qi 'genuine decision change' "$PLANNER" \
  && ok "B3: a move away from a HEALTHY owner is documented as a genuine decision change that clears the reservation (L-60)" \
  || bad "B3: the planner does not document the genuine-decision-change rule"

# ─── C. the RETURN rules ───────────────────────────────────────────────────────
C_MISS=""
grep -q 'func PlanReturns' "$PLANNER" || C_MISS="${C_MISS}PlanReturns does not exist"$'\n'
for r in return-not-healthy return-not-proven return-no-longer-claimed return-too-soon return-quarantined; do
  grep -q "\"$r\"" "$PLANNER" || C_MISS="${C_MISS}the named skip reason $r is gone"$'\n'
done
grep -q 'Waited < r.Required' "$PLANNER" \
  || C_MISS="${C_MISS}the planner allows a return without comparing the waited time against the required window"$'\n'
grep -q 'PlanReturns(' "$PKG" || C_MISS="${C_MISS}Reconcile does not plan returns"$'\n'
grep -q 'ApplyPlan(as, returns, kept, now)' "$PKG" || C_MISS="${C_MISS}the plan is not folded back into the assignment"$'\n'
grep -q 'ProvenRelays(d)' "$PKG" || C_MISS="${C_MISS}the pass does not read the proven-relay set"$'\n'
grep -q 'HealthySince(d, now)' "$PKG" || C_MISS="${C_MISS}the pass does not read the healthy-since set"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: PlanReturns is wired into the pass with the proven set, the healthy-since set and every named skip reason"
else
  bad "C1: the return path is incomplete:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi
# The return pass must run on the EXISTING maintenance tick, not only on a change
# (L-60): sync.go calls ReconcileWithPreference, and that is what runs the plan.
grep -q 'prefixowner.ReconcileWithPreference(s.dbc()' "$SYNC" \
  && grep -q 'func ReconcileWithPreference' "$PKG" \
  && grep -q 'ReconcileWithReservationConfig(d, healthyRelays, prefer, DefaultReservationConfig())' "$PKG" \
  && ok "C2: the reservation pass runs inside the call the existing maintenance tick already makes (no change-triggered convergence)" \
  || bad "C2: the sync path no longer reaches the reconciliation, so a recovered relay would wait for a human (L-60)"

# ─── D. the PROTECTION (persisted counter, doubling window) ────────────────────
D_MISS=""
grep -q 'failover_flaps' "$MIG" || D_MISS="${D_MISS}the flap streak is not persisted in the schema"$'\n'
grep -q 'func requiredHysteresis' "$PLANNER" || D_MISS="${D_MISS}requiredHysteresis does not exist"$'\n'
grep -q 'func nextFlapStreak' "$PLANNER" || D_MISS="${D_MISS}nextFlapStreak does not exist"$'\n'
grep -q 'MaxHysteresis' "$PLANNER" || D_MISS="${D_MISS}the doubling is not capped"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: the quick-flap streak persists in prefix_owner and the required window doubles and is capped"
else
  bad "D1: the anti-flap protection is incomplete:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# ─── E. the operator override is a tool, never a latch ─────────────────────────
E_MISS=""
grep -q 'func DescribeOverride' "$PLANNER" || E_MISS="${E_MISS}DescribeOverride (the override's age + the would-be assignment) does not exist"$'\n'
grep -q 'func ForceRelaySetAt' "$PLANNER" || E_MISS="${E_MISS}ForceRelaySetAt does not exist"$'\n'
grep -q 'func WouldBeAssignmentsWithoutForce' "$PLANNER" || E_MISS="${E_MISS}the would-be assignment helper does not exist"$'\n'
grep -q 'func warnWhileOverrideSuperseded' "$PLANNER" || E_MISS="${E_MISS}the override warning does not exist"$'\n'
grep -q 'warnWhileOverrideSuperseded(d, healthyRelays' "$PKG" || E_MISS="${E_MISS}the pass never warns about a superseded override"$'\n'
# The human's decision is never auto-cleared: no write of an empty override (or a
# delete of its key) may appear outside the test files.
if grep -rn 'SetForceRelay(d, "")' --include='*.go' internal/ | grep -v '_test.go' | grep -q .; then
  E_MISS="${E_MISS}something in internal/ clears the operator's override by itself (SetForceRelay(d, \"\"))"$'\n'
fi
if grep -rn 'DeleteGlobalSetting(d, forceRelaySettingKey)' --include='*.go' internal/ | grep -q .; then
  E_MISS="${E_MISS}something deletes the override's setting key"$'\n'
fi
if printf '%s' "$(body_of "$PLANNER" '^func warnWhileOverrideSuperseded')" | grep -q 'SetForceRelay'; then
  E_MISS="${E_MISS}the warning path writes the override instead of only reporting it"$'\n'
fi
if [ -z "$E_MISS" ]; then
  ok "E1: the override's age and its would-be owners are readable, the 19-hour state warns, and nothing auto-clears a human's choice"
else
  bad "E1: the override surface is incomplete:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# ─── F. behaviour: the tests ARE the operator's artificial tests ───────────────
if command -v go >/dev/null 2>&1; then
  GO_OUT="$SKY_TMP/b360-go-test.txt"
  go test ./internal/prefixowner/ -count=1 >"$GO_OUT" 2>&1
  if grep -q '^ok' "$GO_OUT" && ! grep -q '^FAIL' "$GO_OUT"; then
    ok "F1: the prefixowner package tests pass (the scripted timelines included)"
  else
    bad "F1: go test ./internal/prefixowner/ failed: $(tail -n 6 "$GO_OUT" | tr '\n' ' ')"
  fi
  # Contract: a return can NEVER happen without the hysteresis input.
  HYS_OUT="$SKY_TMP/b360-hysteresis.txt"
  go test ./internal/prefixowner/ -run 'TestB360_NoSustainedInputBlocksEveryReturn' -count=1 >"$HYS_OUT" 2>&1
  if grep -q '^ok' "$HYS_OUT"; then
    ok "F2: a return is impossible without the hysteresis input (TestB360_NoSustainedInputBlocksEveryReturn)"
  else
    bad "F2: the hysteresis input is not load-bearing — a prefix could return with no evidence of sustained health: $(tail -n 4 "$HYS_OUT" | tr '\n' ' ')"
  fi
  # Contract: a manual row is never reserved, never returned.
  MAN_OUT="$SKY_TMP/b360-manual.txt"
  go test ./internal/prefixowner/ -run 'TestB360_ManualPinIsNeverReserved' -count=1 >"$MAN_OUT" 2>&1
  if grep -q '^ok' "$MAN_OUT"; then
    ok "F3: a source='manual' row is never reserved and never returned (TestB360_ManualPinIsNeverReserved)"
  else
    bad "F3: a manual pin can be written as a failover reservation: $(tail -n 4 "$MAN_OUT" | tr '\n' ' ')"
  fi
  # Contract: the timeline, the flap and the live override case.
  for tn in TestB360_ScriptedTimeline_FailoverThenReturn TestB360_QuickFlapDoublesTheWindow TestB360_GlobalOverrideIsNotALatch TestB360_DegradedRelayIsNeverReturned TestB360_UnprovenRelayIsNeverReturned; do
    grep -q "$tn" "$TESTS" || bad "F4: the artificial test $tn is missing"
  done
  grep -q 'TestB360_ScriptedTimeline_FailoverThenReturn' "$TESTS" \
    && ok "F4: the scripted timelines (failover → too-soon → return, flap, degraded, unproven, override) are present"
else
  skip "F: the Go toolchain is not on PATH — run this check where 'go test' works"
fi

# gofmt on the touched files (rule 3). SKIP without gofmt rather than guess.
if command -v gofmt >/dev/null 2>&1; then
  DIRTY="$(gofmt -l "$MIG" "$PLANNER" "$PKG" "$TESTS" 2>/dev/null)"
  if [ -z "$DIRTY" ]; then
    ok "F5: every B360 file is gofmt-clean"
  else
    bad "F5: not gofmt-clean: $(printf '%s' "$DIRTY" | tr '\n' ' ')"
  fi
else
  skip "F5: gofmt is not on this host"
fi

# ─── G. bookkeeping ────────────────────────────────────────────────────────────
# B360 is registered centrally by the lead (the block's own run_check line and
# AGENTS.md bullet are handed over in the report). Until then this section SKIPs;
# once the registration exists it becomes a real contract.
if grep -q 'check_b360_prefix_reservation.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  G_MISS=""
  grep -q '\*\*B360\*\*' AGENTS.md || G_MISS="${G_MISS}AGENTS.md has no B360 bullet"$'\n'
  git ls-files --error-unmatch scripts/check_b360_prefix_reservation.sh >/dev/null 2>&1 \
    || G_MISS="${G_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
  if [ -z "$G_MISS" ]; then
    ok "G1: registered in the catalog, indexed in AGENTS.md, tracked by git"
  else
    bad "G1: the block bookkeeping is incomplete:"
    printf '%s' "$G_MISS" | sed 's/^/       /' >&2
  fi
else
  skip "G1: not registered in verify_pre_deploy.sh yet — the lead adds the B360 catalog line and the AGENTS.md bullet (this section then becomes a real contract)"
fi

# The live half is REPORT-ONLY: whether the deployment's prefix_owner actually
# carries reservations is a post-deploy fact, and a pre-deploy catalog must not
# redden a commit for a state the running build has not produced yet (the B351 D2
# lesson). The assertions belong in verify_post_deploy.sh.
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^skygate-pg-local$'; then
  RES="$(timeout 20 docker exec skygate-pg-local psql -U admin -d skygate_staging -tAc \
    "SELECT COUNT(*) FROM prefix_owner WHERE COALESCE(failover_from,'') <> ''" 2>/dev/null | tr -d '[:space:]')"
  if [ -n "$RES" ]; then
    skip "G2: live prefix_owner carries ${RES} active reservation(s) — informational; a return is asserted by the post-deploy checks"
  else
    skip "G2: live prefix_owner not reachable from here (run this script on the reference VM)"
  fi
else
  skip "G2: docker / the local PostgreSQL container is unavailable"
fi

printf '\n\033[1mB360 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
