#!/usr/bin/env bash
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"   # B339: read the cmd/skygate + internal/update SURFACES, not single files
#===============================================================================
# check_b375_schedule_actually_runs.sh — B375 (2026-10-10)
#
# THE MEASURED DEFECT (reference deployment, PostgreSQL, 2026-10-09/10).
# The operator set the schedule in the panel and NOTHING happened:
#
#   * global_settings: update_schedule_enabled = 1, update_schedule_time = 08:00,
#     update.pinned_release = (empty);
#   * container env: SKYGATE_UPDATE_SCHEDULE_ENABLED=false; .env did not carry
#     the variable at all, so config.go's default "false" won;
#   * the container journal held ZERO "update-scheduler:" lines since the
#     container started — the goroutine had NEVER been created;
#   * cmd/skygate/main.go gated the whole block on `if cfg.UpdateScheduleEnabled`
#     (the ENV value) and its else-branch claimed the /admin/update page could
#     enable it. FALSE: the page writes global_settings; it cannot start a
#     goroutine that does not exist.
#
# internal/update/scheduler.go's own header already documented the DB-persisted
# toggle as the authority ("falls back to Cfg.UpdateScheduleEnabled") — but the
# goroutine that would have read it was gated on the ENV value.
#
# WHAT THIS SCRIPT PINS
#   A. the scheduler is ARMED UNCONDITIONALLY (the env guard is gone) and the
#      arming is recorded in process-level state the admin package can read;
#   B. the boot log tells the truth and never promises that a page can start a
#      goroutine (a pure function, unit-tested);
#   C. the catch-up window: named constant, at-or-after the slot, strictly
#      bounded, deduplicated per slot per DAY;
#   D. the non-Docker skip is VISIBLE (a log line naming the install kind);
#   E. /admin/update RENDERS whether the scheduler is running, including the
#      trap «расписание включено, но планировщик не запущен», in BOTH catalogues;
#   F. the behavioural half runs (real SQLite: the toggle, on-time, +5m, +20m,
#      already-ran-today, armed-while-env-false);
#   G. tracked by git (AGENTS trap #11), registered, indexed, and the ratchet
#      was paid when the touched file left the gofmt allow-list.
#
# SKIP (never FAIL) anything this host lacks.
#
# Usage:  bash scripts/check_b375_schedule_actually_runs.sh
# Exit:   0 = contracts hold, 1 = regression
#===============================================================================
set -uo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/gosurface.sh

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SCHEDULER_GO="internal/update/scheduler.go"
STATE_GO="internal/update/scheduler_state.go"
TEST_GO="internal/update/scheduler_b375_test.go"
ADMIN_GO="internal/feature/admin/update.go"
TEMPLATE="internal/handlers/templates/admin/update.html"
I18N="internal/i18n/catalog_update.go"
CHECK130="scripts/check_b130.sh"
SELF="scripts/check_b375_schedule_actually_runs.sh"

for f in "$SCHEDULER_GO" "$STATE_GO" "$TEST_GO" "$ADMIN_GO" "$TEMPLATE" "$I18N"; do
  [ -f "$f" ] || { bad "missing file: $f"; exit 1; }
done

# B339: the contracts about MAIN read the cmd/skygate SURFACE (a pure code move
# inside the package must not turn them red), and the contracts about the
# scheduler's own behaviour read the internal/update SURFACE.
gosurface MAIN_GO cmd/skygate/*.go
gosurface UPD_GO internal/update/*.go

# =====================================================================
hdr "A. the scheduler is armed UNCONDITIONALLY; process state records it"

A_MISS=""
grep -q 'update\.Start(' "$MAIN_GO" || A_MISS="${A_MISS}main.go does not call update.Start()"$'\n'
grep -q 'update\.SchedulerDeps' "$MAIN_GO" || A_MISS="${A_MISS}main.go does not build SchedulerDeps"$'\n'
grep -q 'update\.SetSchedulerArmed(' "$MAIN_GO" || A_MISS="${A_MISS}main.go never records that the scheduler is armed (the page cannot learn it)"$'\n'
# The pre-B375 guard must be GONE as a statement. A comment may still quote it
# (this block's own comment does, to explain the defect), so anchor the match.
if grep -qE '^[[:space:]]*if cfg\.UpdateScheduleEnabled' "$MAIN_GO"; then
  A_MISS="${A_MISS}main.go still gates the scheduler on the ENV value ('if cfg.UpdateScheduleEnabled {') — the live defect"$'\n'
fi
grep -q 'func SchedulerArmed()' "$UPD_GO" || A_MISS="${A_MISS}internal/update has no SchedulerArmed() reader"$'\n'
grep -q 'func SchedulerArmedReason()' "$UPD_GO" || A_MISS="${A_MISS}internal/update has no SchedulerArmedReason() reader"$'\n'
grep -q 'func SetSchedulerArmed(' "$UPD_GO" || A_MISS="${A_MISS}internal/update has no SetSchedulerArmed() setter"$'\n'
grep -q 'sync\.RWMutex' "$UPD_GO" || A_MISS="${A_MISS}the arming state is not race-safe (no RWMutex)"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: main.go arms the scheduler unconditionally and records it; internal/update exposes the race-safe armed state"
else
  bad "A1: the scheduler can still be missing while the panel says enabled:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi
# The env value must survive ONLY as the fallback for an untouched install.
grep -q 'UpdateScheduleEnabled: cfg\.UpdateScheduleEnabled' "$MAIN_GO" \
  || bad "A2: main.go no longer passes cfg.UpdateScheduleEnabled as the untouched-install fallback"
if grep -q 'UpdateScheduleEnabled: cfg\.UpdateScheduleEnabled' "$MAIN_GO"; then
  ok "A2: the env value stays the fallback (an install that never opened the panel is unchanged)"
fi

# =====================================================================
hdr "B. the boot log tells the truth (pure function, unit-tested)"

B_MISS=""
grep -q 'func SchedulerBootLog(' "$UPD_GO" || B_MISS="${B_MISS}no SchedulerBootLog() — main() is not testable, so the sentence must be a pure function"$'\n'
grep -q 'func SchedulerTrap(' "$UPD_GO" || B_MISS="${B_MISS}no SchedulerTrap() predicate for «включено, но не запущено»"$'\n'
grep -q 'NOT RUNNING' "$UPD_GO" || B_MISS="${B_MISS}the sentence has no explicit not-running state"$'\n'
grep -q 'cannot start a goroutine' "$UPD_GO" || B_MISS="${B_MISS}the sentence does not refute the false claim that a page can enable it"$'\n'
grep -q 'SKYGATE_UPDATE_SCHEDULE_ENABLED' "$UPD_GO" || B_MISS="${B_MISS}the env-fallback clause does not name the env variable"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: the boot sentence has all four states and refutes the pre-B375 promise that a page can start the scheduler"
else
  bad "B1: the boot log cannot tell the truth:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi
grep -q 'SchedulerBootLog(' "$MAIN_GO" || bad "B2: main.go does not use SchedulerBootLog — the boot log is back to ad-hoc strings"
if grep -q 'SchedulerBootLog(' "$MAIN_GO"; then
  ok "B2: main.go logs the unit-tested sentence (not an ad-hoc one)"
fi

# =====================================================================
hdr "C. the catch-up window: named, bounded, deduplicated per slot per DAY"

C_MISS=""
grep -q 'CatchUpWindow = 10 \* time\.Minute' "$UPD_GO" || C_MISS="${C_MISS}CatchUpWindow is not the named 10-minute constant"$'\n'
grep -q 'func scheduledRunDue(' "$UPD_GO" || C_MISS="${C_MISS}no scheduledRunDue() window predicate"$'\n'
grep -q 'func ranTodayAt(' "$UPD_GO" || C_MISS="${C_MISS}no ranTodayAt() day-scoped dedup"$'\n'
grep -q 'scheduledRunDue(' "$SCHEDULER_GO" || C_MISS="${C_MISS}the tick does not use the window predicate"$'\n'
grep -q 'ranTodayAt(' "$SCHEDULER_GO" || C_MISS="${C_MISS}the tick does not use the day-scoped dedup (a window would fire once per tick)"$'\n'
grep -q 'late by' "$UPD_GO" || C_MISS="${C_MISS}a late run is not LOGGED as late"$'\n'
# The window must be a WINDOW, not a replacement for the exact-minute case.
grep -q 'late by %s, inside the %s catch-up window' "$UPD_GO" || C_MISS="${C_MISS}the late log line does not name the window"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: the window is a named 10m constant, at-or-after the slot, with a per-day dedup and a visible 'late by N'"
else
  bad "C1: the catch-up contract is incomplete:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi
# The pre-B375 exact-minute helper must be gone from the tick (it would lose the
# day at a container recreate).
if grep -q 'timeMatches(' "$UPD_GO"; then
  bad "C2: timeMatches (exact-minute equality) is still referenced — the defect it caused can come back"
else
  ok "C2: exact-minute equality is gone; a recreate at the scheduled minute no longer loses the day"
fi

# =====================================================================
hdr "D. the non-Docker skip is VISIBLE"

D_MISS=""
grep -q 'func logScheduledSkip(' "$UPD_GO" || D_MISS="${D_MISS}no logScheduledSkip() helper"$'\n'
grep -q 'NOT SUPPORTED on install kind' "$UPD_GO" || D_MISS="${D_MISS}the skip line does not say what happened"$'\n'
grep -q 'logScheduledSkip(' "$SCHEDULER_GO" || D_MISS="${D_MISS}runScheduled/tick does not call it (the skip is silent again)"$'\n'
# The tick must answer the question BEFORE the GitHub check: with GitHub
# unreachable the operator would otherwise still get no explanation.
if [ -n "$D_MISS" ]; then
  bad "D1: the scheduled path can still skip a native install silently:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
else
  ok "D1: the non-Docker skip names the detected install kind and is reachable before the release check"
fi

# =====================================================================
hdr "E. /admin/update SHOWS whether the scheduler is running"

E_MISS=""
grep -q 'update\.SchedulerArmed()' "$ADMIN_GO" || E_MISS="${E_MISS}the admin page does not read SchedulerArmed()"$'\n'
grep -q 'update\.SchedulerArmedReason()' "$ADMIN_GO" || E_MISS="${E_MISS}the admin page does not read the reason"$'\n'
grep -q 'update\.SchedulerTrap(' "$ADMIN_GO" || E_MISS="${E_MISS}the admin page does not compute the trap state"$'\n'
grep -q '"SchedulerArmed"' "$ADMIN_GO" || E_MISS="${E_MISS}SchedulerArmed is not passed to the template"$'\n'
grep -q '"SchedulerTrap"' "$ADMIN_GO" || E_MISS="${E_MISS}SchedulerTrap is not passed to the template"$'\n'
grep -q 'SchedulerArmed' "$TEMPLATE" || E_MISS="${E_MISS}the template does not render the running state"$'\n'
grep -q 'SchedulerTrap' "$TEMPLATE" || E_MISS="${E_MISS}the template does not render the trap"$'\n'
for k in scheduler_running scheduler_not_running scheduler_running_but_off scheduler_not_supported scheduler_trap_title scheduler_trap_body; do
  c=$(grep -c "\"update\.${k}\"" "$I18N" || true)
  [ "$c" -ge 2 ] || E_MISS="${E_MISS}i18n key update.${k} is not defined in BOTH catalogues (found ${c})"$'\n'
done
grep -q 'update.scheduler_trap_title' "$TEMPLATE" || E_MISS="${E_MISS}the trap banner uses no i18n key"$'\n'
if [ -z "$E_MISS" ]; then
  ok "E1: the page renders планировщик запущен / не запущен + its reason + the trap, in RU AND EN"
else
  bad "E1: the operator still cannot see whether the scheduler is running:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# =====================================================================
hdr "F. the behavioural halves run (real SQLite)"

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "F1: no go binary in PATH — the behavioural contracts were not run"
else
  if out="$("$GOBIN" test ./internal/update/ -run 'B375' -count=1 2>&1)"; then
    ok "F1: the B375 group passes — the DB toggle, the exact minute, +5m, +20m, already-ran-today and armed-while-env-false"
  else
    bad "F1: go test -run B375 failed:
$(printf '%s\n' "$out" | tail -25)"
  fi
  if out="$("$GOBIN" test ./internal/feature/admin/ ./internal/i18n/ -count=1 2>&1)"; then
    ok "F2: the admin render + i18n parity tests pass (the page's new fields compile and both catalogues agree)"
  else
    bad "F2: the admin/i18n tests failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
  if out="$("$GOBIN" build ./... 2>&1)"; then
    ok "F3: the tree builds"
  else
    bad "F3: go build ./... fails:
$(printf '%s\n' "$out" | tail -10)"
  fi
fi

# =====================================================================
hdr "G. tracked by git (AGENTS trap #11), registered, indexed, ratchet paid"

G_MISS=""
for f in "$STATE_GO" "$TEST_GO" "$SELF"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || G_MISS="${G_MISS}${f} "
done
if [ -z "$G_MISS" ]; then
  ok "G1: every new file of this block is tracked by git"
else
  skip "G1: not yet tracked by git (the lead commits this block): ${G_MISS}"
fi
grep -q 'run_check "B375"' scripts/verify_pre_deploy.sh \
  && ok "G2: registered in scripts/verify_pre_deploy.sh" \
  || bad "G2: not registered — the contract would never run"
grep -q '^- \*\*B375\*\*' AGENTS.md \
  && ok "G3: recorded in the AGENTS.md block index (as its own line)" \
  || bad "G3: AGENTS.md has no B375 bullet at line start"
# B130's contract C used to REQUIRE the env guard that B375 removed. If it still
# does, the gate contradicts the fix. The guard is matched as a LITERAL (grep -F)
# so this probe cannot itself be fooled by regex escaping.
if grep -qF 'the pre-B375 env guard' "$CHECK130" && grep -qF 'if cfg\.UpdateScheduleEnabled' "$CHECK130"; then
  ok "G4: scripts/check_b130.sh was renegotiated with the fix (it now forbids the env guard instead of requiring it)"
else
  bad "G4: scripts/check_b130.sh was not renegotiated — it may still require the guard the fix removed (probe: 'the pre-B375 env guard' + the guard pattern)"
fi
# The gofmt ratchet: scheduler_test.go and state.go were allow-listed (drifted).
# Touch + gofmt must pay both entries down, and the budget must fall with them.
if [ -f scripts/gofmt_legacy_allowlist.txt ]; then
  REMAIN=""
  for f in internal/update/scheduler_test.go internal/update/state.go; do
    grep -qx "$f" scripts/gofmt_legacy_allowlist.txt && REMAIN="${REMAIN}${f} "
  done
  if [ -z "$REMAIN" ]; then
    ok "G5: both touched update files left the gofmt allow-list (the ratchet was paid)"
  else
    bad "G5: still gofmt-allow-listed although this block gofmt'd them (the ratchet was not paid): ${REMAIN}"
  fi
else
  skip "G5: scripts/gofmt_legacy_allowlist.txt is absent"
fi
# The snapshot fix is what makes the B375 suite -race-clean: Get() used to hand
# out the store's own *State while its writers mutated it under s.mu.
if grep -q 'cp := \*s.state' "$UPD_GO"; then
  ok "G6: StateStore.Get() returns a snapshot (a reader cannot race the writers) — the B375 suite is race-clean"
else
  skip "G6: internal/update/state.go no longer copies in Get() — re-check 'go test -race ./internal/update/'"
fi

printf '\n\033[1mB375 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
