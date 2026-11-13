#!/usr/bin/env bash
# ============================================================================
# check_b466_slow_probe.sh — B466
#
# A SLOW PROBE IS NOT A SICK DATABASE, AND A VERDICT MUST CARRY ITS AGE.
#
# Live case (restored SQLite copy on the VPS stand, 121 MB file, 1 vCPU,
# published v1.5.212, 2026-11-13):
#
#   db_health: tick: db_health: 2 query error(s): [db.integrity_check:
#   context deadline exceeded db.journal_mode: context deadline exceeded]
#
# every 30 seconds, on a perfectly healthy database, which also means a
# permanently "degraded" DB-health badge. Two independent defects:
#
#   1. DBHealthConfig.QueryTimeout is DOCUMENTED as a per-query deadline, but the
#      sampler built ONE context around the whole collect(). `PRAGMA quick_check`
#      is O(database size) and consumed the 3 s, so `PRAGMA journal_mode` behind
#      it failed instantly with the same deadline: one slow statement turned into
#      a whole-sample outage.
#   2. A full-file consistency scan is not a "is the database answering" question.
#      It ran on every tick, was budgeted like a cheap pragma, and its TIMEOUT was
#      reported as an integrity FAILURE — "unmeasured" masquerading as "broken",
#      the exact class B465 fixed for relay verdicts.
#
# Contracts:
#   A. one budget per QUERY, and the integrity probe has its own budget
#   B. three-state integrity verdict: ok / unmeasured / failed — only failed is a finding
#   C. the probe runs on its own schedule, asynchronously, and the sample carries its AGE
#   D. the response mirrors the new fields (the page/JSON can show them)
#   E. tests pin all of it (ok, unmeasured, schedule, defaults)
#   F. the detector discriminates the pre-B466 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b466.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

DBH=internal/feature/healthz/db_health.go
TEST=internal/feature/healthz/db_health_b466_test.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$DBH" "$TEST"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: budgets ---------------------------------------------------------------
if has "$DBH" 'IntegrityTimeout time.Duration' && has "$DBH" 'IntegrityInterval time.Duration'; then
  ok "A1: the integrity probe has its own budget and its own interval"
else
  bad "A1: the integrity probe still shares the ordinary query budget"
fi
# A2 — the per-query property. The sampler must not hand ONE context to every
# statement: every query goes through rowScan, which derives its own deadline.
_direct="$(grep -c 'db\.QueryRowContext(ctx,' "$DBH" || true)"
_helper="$(grep -c 'func (s \*Sampler) rowScan' "$DBH" || true)"
_qctx="$(grep -c 'func (s \*Sampler) qctx' "$DBH" || true)"
_rs_uses="$(awk '/^func \(s \*Sampler\) rowScan/,/^}/' "$DBH" | grep -c 's.qctx(ctx)' || true)"
if [ "$_direct" -eq 0 ] && [ "$_helper" -ge 1 ] && [ "$_qctx" -ge 1 ] && [ "$_rs_uses" -ge 1 ]; then
  ok "A2: not one statement inherits a shared deadline — every query is bounded on its own"
else
  bad "A2: a statement still runs under a budget shared with the others (direct=$_direct rowScan=$_helper/$_rs_uses)"
fi
if has "$DBH" 'WithTimeout(parent, s.cfg.QueryTimeout)'; then
  ok "A3: the per-query budget is the configured QueryTimeout, as documented"
else
  bad "A3: the helper does not apply QueryTimeout"
fi
# A3b — the tick itself must NOT impose a deadline: a parent that expires would
# re-create the very defect, because every per-query child inherits it. Comments are
# excluded on purpose — the fix's own comment quotes the old line (trap #15: a check
# must not turn its own documentation into the regression it is looking for).
_shared="$(grep -n 'WithTimeout(context.Background(), s.cfg.QueryTimeout)' "$DBH" | grep -vc ':[[:space:]]*//' || true)"
if has "$DBH" 'WithCancel(context.Background())' && [ "${_shared:-1}" -eq 0 ]; then
  ok "A3b: the tick hands out a deadline-free parent, so a slow statement cannot expire the rest"
else
  bad "A3b: the sample still runs under one shared deadline ($_shared code line(s))"
fi
if has "$DBH" 'WithTimeout(context.Background(), s.cfg.IntegrityTimeout)'; then
  ok "A4: the integrity probe is bounded by IntegrityTimeout, not by QueryTimeout"
else
  bad "A4: the integrity probe does not use its own budget"
fi
_a_defaults="$(grep -c 'cfg.IntegrityTimeout = 2 \* time.Minute' "$DBH" || true)"
_b_defaults="$(grep -c 'cfg.IntegrityInterval = 15 \* time.Minute' "$DBH" || true)"
_c_defaults="$(grep -c 'IntegrityTimeout:  2 \* time.Minute' "$DBH" || true)"
if [ "$_a_defaults" -ge 1 ] && [ "$_b_defaults" -ge 1 ] && [ "$_c_defaults" -ge 1 ]; then
  ok "A5: 2m/15m are the defaults AND are re-applied when a caller passes zero"
else
  bad "A5: a caller that sets nothing can end up with a zero budget (an instantly expired context)"
fi

# --- B: three-state verdict ---------------------------------------------------
if has "$DBH" 'status = "unmeasured"' && has "$DBH" 'status = "failed"' && has "$DBH" 'status, detail := "ok", ""'; then
  ok "B1: the verdict has three states, and only an ANSWERED 'not ok' is a finding"
else
  bad "B1: the probe still reports two states (finished / error)"
fi
if has "$DBH" 'This is NOT a database error'; then
  ok "B2: an unfinished probe says so in words, so nobody reads it as corruption"
else
  bad "B2: an unmeasured probe is still silent about what it is not"
fi
# B3 — the discriminator that matters: an unmeasured probe must not reach SampleError.
if has "$DBH" 'if status == "failed" {'; then
  ok "B3: only a failed verdict reaches SampleError (an unmeasured one cannot degrade the badge)"
else
  bad "B3: SampleError is still touched by something other than a failed verdict"
fi
if ! grep -q 'errs = append(errs, fmt.Sprintf("db.integrity_check' "$DBH"; then
  ok "B4: the probe no longer appends to the query-error list"
else
  bad "B4: the probe still counts as a query error"
fi

# --- C: schedule, asynchrony, age --------------------------------------------
if has "$DBH" 'integrityNext' && has "$DBH" 'now.Add(s.cfg.IntegrityInterval)'; then
  ok "C1: the probe is scheduled by IntegrityInterval, not by the sample tick"
else
  bad "C1: the probe still runs on every tick"
fi
if has "$DBH" 'go s.runIntegrityProbe(db)'; then
  ok "C2: the probe cannot delay a sample (a full-file scan runs in its own goroutine)"
else
  bad "C2: a full-file scan still blocks the health sample"
fi
# C3 — the claim must be taken BEFORE the goroutine starts, or two ticks launch two scans.
# AGENTS trap #9: never pipe a producer into grep -q under pipefail (grep exits at the first
# match, the producer dies of SIGPIPE, and pipefail reports the pipeline as failed even though
# it matched — this contract did exactly that inside the full gate while passing standalone).
# Capture first, then match with a shell builtin (trap #15).
_claim_block="$(awk '/Claim the slot BEFORE starting/{f=1} f{print}' "$DBH")"
case "$_claim_block" in
  *'s.integrityNext = now.Add'*)
    ok "C3: the slot is claimed before the goroutine starts (no overlapping scans)" ;;
  *)
    bad "C3: two ticks can start two scans of the same file" ;;
esac
if has "$DBH" 'AgeSec' && has "$DBH" 'CheckedAt' && has "$DBH" 'SampleError = strings.TrimSpace(out.SampleError'; then
  ok "C4: the verdict travels with when it was attempted and how old it is"
else
  bad "C4: a cached verdict can still be read as a fresh measurement"
fi

# --- D: the response mirrors it ----------------------------------------------
for k in IntegrityStatus IntegrityDetail IntegrityCheckedAt IntegrityAgeSeconds; do
  if has "$DBH" "resp.$k = sample.Integrity."; then
    ok "D: the response carries $k"
  else
    bad "D: the response does not carry $k — /db/health would hide the verdict"
  fi
done

# --- E: tests -----------------------------------------------------------------
_e_tests=0
for t in TestB466_QueryTimeoutBudgetIsObserved TestB466_UnmeasuredIntegrityIsNotAFailure \
         TestB466_MeasuredIntegrityIsOk TestB466_IntegrityRunsOnItsOwnSchedule \
         TestB466_ServerBudgetDefaults; do
  if has "$TEST" "func $t("; then _e_tests=$((_e_tests+1)); fi
done
if [ "$_e_tests" -eq 5 ]; then
  ok "E1: five tests pin the budgets, the three states, the schedule and the defaults"
else
  bad "E1: only $_e_tests of 5 B466 tests exist"
fi
if has "$TEST" 'if sample.SampleError != "" {' && has "$TEST" 'want empty — an unmeasured probe is not a database error'; then
  ok "E2: the test asserts the badge stays clean on an unmeasured probe"
else
  bad "E2: nothing asserts that an unmeasured probe leaves the sample healthy"
fi
if has "$TEST" 'IntegrityInterval = time.Hour'; then
  ok "E3: the schedule test pins its own interval instead of borrowing the default"
else
  bad "E3: the schedule test depends on the default interval"
fi

# --- F: the detector discriminates the pre-B466 shape -------------------------
cat > "$SKY_TMP/old_db_health.go" <<'EOF'
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.QueryTimeout)
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity); err != nil {
		errs = append(errs, fmt.Sprintf("db.integrity_check: %v", err))
	} else if integrity != "ok" {
EOF
cat > "$SKY_TMP/old_cfg.go" <<'EOF'
	// QueryTimeout is the per-query deadline for each of the sampler queries.
	QueryTimeout time.Duration
EOF
discriminates=1
for _marker in 'IntegrityTimeout' 'rowScan' 'unmeasured' 'integrityNext' 'runIntegrityProbe'; do
  has "$DBH" "$_marker" || discriminates=0
done
for _marker in 'IntegrityTimeout' 'unmeasured' 'integrityNext' 'runIntegrityProbe' 'rowScan'; do
  has "$SKY_TMP/old_db_health.go" "$_marker" && discriminates=0
  has "$SKY_TMP/old_cfg.go" "$_marker" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "F1: every predicate is present in the fixed file and absent from the pre-B466 shape"
else
  bad "F1: the predicates do not discriminate the pre-B466 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB466: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB466: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
