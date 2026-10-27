#!/usr/bin/env bash
# check_b428_update_panel_submit.sh — B428 (2026-10-27)
#
# «С ВЕРСИИ 1.5.70 НЕ МОГУ ОБНОВИТЬ ЧЕРЕЗ ПАНЕЛЬ: ОКНО ПОЯВЛЯЕТСЯ, ОБНОВЛЕНИЕ НЕ
#  НАЧИНАЕТСЯ, ОШИБОК НЕТ» — воспроизведено и объяснено по живым данным.
#
# Measured on the operator's install (2026-10-27):
#
#	/healthz                     build=v1.5.170+4ec90a26
#	last update_apply audit row  2026-10-26 07:40:12  job=bf263082 target=v1.5.170
#	/data/skygate-update-status.json
#	                             phase="verify", finished_at=0001-01-01 (never)
#	update_page_view audit rows  ~150 an hour, forever (every ~6 s)
#	update_apply rows after that NONE, although the operator kept clicking
#
# Two defects held each other up:
#
#  (1) resolvePendingSwap admitted ONLY build_done / rolled_back, so a job whose
#      orchestrator died at `verify` was never resolved. `verify` is not an exotic
#      state: the swap RECREATES the container, so the goroutine that polls
#      /healthz dies with the old process — every successful Docker update can
#      leave its job at `verify` forever. That is what happened here: the swap
#      worked (v1.5.170 is live) and the panel never said so, for 22 hours.
#
#  (2) the template carried its OWN hardcoded list of "finished" phases —
#      ['done','failed','rolled_back','pending'] — that omitted `verify` and
#      wrongly included `pending`. So the stale job armed `location.reload()`
#      every 5 s (the ~150 page views an hour) while a freshly started job never
#      refreshed at all. And the reload was fatal to the operator's own action:
#      clicking «Обновить» opens a confirm() dialog, which PAUSES the timer, so
#      the queued reload fired the moment the dialog closed and CANCELLED the
#      form submission. No POST reached the server (no audit row), no error
#      appeared — exactly the report.
#
# The fix has three halves, each pinned below: every non-terminal phase is
# resolved by the same evidence (with a grace period that matches the work the
# phase still has to do), the live/finished decision comes from ONE definition
# (update.Phase.IsTerminal) instead of a copy in JavaScript, and the refresh
# refuses to race a submission.
#
# CONTRACTS (20)
#   A  a job that dies in ANY phase is resolved (the resolver's gate)
#   B  the two grace periods (pre-swap work vs post-swap wait)
#   C  behaviour: the phase matrix, the live case, and IsTerminal
#   D  the panel's refresh cannot cancel the operator's submit
#   E  registration / index / tracked

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

VERDICT=internal/update/swapverify.go
STATE=internal/update/state.go
TPL=internal/handlers/templates/admin/update.html
ADMIN=internal/feature/admin/update.go
TESTF=internal/update/verify_phase_b428_test.go
GATE=scripts/verify_pre_deploy.sh
for f in "$VERDICT" "$STATE" "$TPL" "$ADMIN" "$TESTF" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. a job that dies in ANY phase is resolved"
# The pre-B428 gate was `if st.Phase != PhaseBuildDone && st.Phase != PhaseRolledBack { return false }`.
VERDICT_CODE="$(awk '/^func resolvePendingSwap\(/,/^}/' "$VERDICT" | grep -v '^[[:space:]]*//')"
case "$VERDICT_CODE" in
  *"PhaseBuildDone && st.Phase != PhaseRolledBack"*)
    bad "A1: the resolver still admits only build_done/rolled_back — a job left at verify hangs forever" ;;
  *) ok "A1: the resolver no longer hardcodes build_done/rolled_back (that gate is what made verify a 22-hour hang)" ;;
esac
case "$VERDICT_CODE" in
  *"case PhaseDone, PhaseFailed:"*) ok "A2: only the two genuinely finished phases are skipped (rolled_back still needs its verdict — a rollback spawns its own swap subprocess)" ;;
  *) bad "A2: the resolver's skip list does not name exactly done/failed" ;;
esac
case "$VERDICT_CODE" in
  *"grace := stalledJobGrace(st.Phase)"*) ok "A3: one grace period, chosen by the phase, instead of the swap's constant everywhere" ;;
  *) bad "A3: the resolver still applies the swap grace to every phase" ;;
esac
case "$VERDICT_CODE" in
  *"stopped in phase"*|*"unfinishedSwapMessage(st, age, grace"*) ok "A4: the failure message is produced with the phase and grace in hand (it can name where the job died)" ;;
  *) bad "A4: the failure message cannot name the phase the job died in" ;;
esac

hdr "B. the two grace periods"
if grep -qF 'const PreSwapGracePeriod = 10 * time.Minute' "$VERDICT"; then
  ok "B1: a pre-swap phase gets the orchestrator's own 10-minute budget before it is declared dead"
else
  bad "B1: no separate pre-swap grace — a healthy cold build would be failed after 4 minutes"
fi
if grep -qF 'func stalledJobGrace(p Phase) time.Duration' "$VERDICT" && grep -qF 'case PhaseSwap, PhaseVerify, PhaseBuildDone, PhaseRolledBack:' "$VERDICT"; then
  ok "B2: the post-swap phases keep B393's SwapGracePeriod; the pre-swap ones use the longer budget"
else
  bad "B2: stalledJobGrace does not split the phases the way the verdict needs"
fi

hdr "C. behaviour: the phase matrix and the live case"
if OUT="$(go test ./internal/update/ -run 'TestB428' -count=1 2>&1)"; then
  ok "C1: every non-terminal phase resolves (target build serving → done, anything else after grace → failed naming the phase), the pre-swap grace is respected, and the 22-hour verify hang resolves"
else
  bad "C1: the phase-matrix tests failed: $(printf '%s' "$OUT" | grep -E '^(---|FAIL|\s+verify)' | head -4 | tr '\n' ' ')"
fi
for probe in TestB428_PhaseIsTerminal TestB428_EveryNonTerminalPhaseIsResolved TestB428_PreSwapGraceIsLongerThanSwapGrace TestB428_TheLiveCase; do
  grep -qF "func $probe(" "$TESTF" && ok "C2: $probe exists" || bad "C2: $probe is missing"
done

hdr "D. the panel's refresh cannot cancel the operator's submit"
if grep -qF 'data-update-live="{{' "$TPL" || grep -qF '{{if not $st.Phase.IsTerminal}} data-update-live="1"{{end}}' "$TPL"; then
  ok "D1: the live/finished decision is rendered by the SERVER from update.Phase.IsTerminal"
else
  bad "D1: the template still decides on its own whether a phase is finished"
fi
if grep -qF "var done = ['done', 'failed', 'rolled_back', 'pending']" "$TPL"; then
  bad "D2: the hardcoded phase list is back — it omitted verify (reload loop) and included pending (a started job never refreshed)"
else
  ok "D2: no hardcoded phase list in the page"
fi
if grep -qF "document.addEventListener('submit'" "$TPL" && grep -qF 'if (submitting) return;' "$TPL"; then
  ok "D3: the refresh refuses to run once a form submission has started (a queued reload used to cancel the POST)"
else
  bad "D3: the refresh does not guard against a pending submission"
fi
if grep -qF 'document.hidden' "$TPL"; then
  ok "D4: a background tab stops reloading (the pre-B428 loop produced ~150 renders an hour for 22 hours)"
else
  bad "D4: a background tab keeps reloading the page"
fi
if grep -qF 'remoteInFlight' "$ADMIN" || grep -qF 'updaterInFlight()' "$ADMIN"; then
  ok "D5: the render path resolves a stale job only when no updater is running in THIS process (the goroutine is per-process, the state file is not)"
else
  bad "D5: the render path does not distinguish an in-flight job from one left by a replaced container"
fi
if grep -qF 'func updaterInFlight() bool' "$ADMIN"; then
  ok "D6: updaterInFlight() exists as the single question 'is an update running here?'"
else
  bad "D6: no updaterInFlight helper"
fi

hdr "E. registration / index / tracked"
grep -q 'run_check "B428"' "$GATE" && ok "E1: registered in the catalog" || bad "E1: not registered"
grep -q '\*\*B428\*\*' AGENTS.md && ok "E2: AGENTS.md block index carries B428" || bad "E2: no AGENTS entry"
if git ls-files --error-unmatch scripts/check_b428_update_panel_submit.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap 11)"
else
  bad "E3: this script is not tracked by git yet — a check nobody can run is not a check"
fi

printf '\n\033[1mB428 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
