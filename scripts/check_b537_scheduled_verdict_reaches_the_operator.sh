#!/usr/bin/env bash
# check_b537_scheduled_verdict_reaches_the_operator.sh
#
# B537 (2026-11-28) — «Не смог обновиться skygate» was a MISSING MESSAGE, not a failed update.
#
# The live pair: 08:00 «⏰ Плановое обновление skygate начинается: v1.5.265+4d594e2e →
# v1.5.270 (job 95eae8f5)» and then silence; at 08:51 the operator concluded the update had
# failed while the panel's state file said done. `runScheduled` sends the verdict from INSIDE
# the goroutine that performs the swap, and on Docker the swap recreates the container — the
# goroutine dies before its own SendAlert, so every SUCCESSFUL scheduled update is silent
# while a pre-swap failure still arrives.
#
# WHAT THIS CONTRACT PINS
#   A. the verdict is recorded as FACTS (not a rendered string) in the state file by whoever
#      finalizes the job, and survives a reload from disk — the file is the carrier, not the
#      process that died;
#   B. delivery is at-least-once and at-most-once: a refusing sink leaves the verdict pending,
#      an accepting one clears it, and it is never sent twice;
#   C. the three reasons the operator would still go uninformed are closed: the boot resolver
#      records a verdict on the success path, on the swap-log failure path AND on the stalled
#      path, and a job started by the panel owes nothing (the browser already watches it);
#   D. the wire-up actually happens: the scheduler marks its jobs, a sink is registered where
#      the Telegram client exists, and the scheduler flushes a verdict a previous boot left;
#   E. the message is defined in BOTH catalogues (RU + EN), so the delivered text is a
#      sentence and not the key `alert.update.scheduled_ok`.
#
# WHERE IT SKIPS: nowhere — everything here is a source contract or a unit test.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B537: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
NL=$'\n'

STATE=internal/update/state.go
NOTIFY=internal/update/notify_b537.go
SWAP=internal/update/swapverify.go
SCHED=internal/update/scheduler.go
WIRE=cmd/skygate/main_update_scheduler.go
TEST=internal/update/notify_b537_test.go

hdr "B537 — a scheduled update's verdict must outlive the swap that kills its reporter"

# --- A: the verdict is facts, and the file is the carrier ---------------------
A_MISS=""
grep -q 'PendingNotify \*PendingNotification' "$STATE" \
  || A_MISS="${A_MISS}State.PendingNotify is missing — the verdict has nowhere to survive$NL"
grep -q 'func (s \*StateStore) MarkPendingNotify' "$STATE" \
  || A_MISS="${A_MISS}MarkPendingNotify is missing$NL"
grep -q 'func (s \*StateStore) TakePendingNotify' "$STATE" \
  || A_MISS="${A_MISS}TakePendingNotify is missing$NL"
# The facts, not a pre-rendered sentence: a message composed before the i18n catalogue is
# installed is the KEY, which is how a "translated" alert ships as alert.update.scheduled_ok.
grep -q 'DurationSeconds float64' "$NOTIFY" \
  || A_MISS="${A_MISS}PendingNotification does not carry the duration as a fact$NL"
grep -q 'func (p PendingNotification) Text() string' "$NOTIFY" \
  || A_MISS="${A_MISS}PendingNotification.Text() is missing — facts are never rendered$NL"
grep -q 'func (s \*StateStore) MarkScheduled' "$STATE" \
  || A_MISS="${A_MISS}MarkScheduled is missing — the state cannot say WHO started the job$NL"
if [ -z "$A_MISS" ]; then
  ok "A: the verdict is facts (job, ends, duration, error) recorded in the state FILE"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "A: $l"; done <<< "$A_MISS"
fi

# --- B: at-least-once, at-most-once -----------------------------------------
B_MISS=""
grep -q 'func AlertPendingScheduledVerdict(store \*StateStore) bool' "$NOTIFY" \
  || B_MISS="${B_MISS}AlertPendingScheduledVerdict is missing$NL"
grep -q 'store.MarkPendingNotify(\*p)' "$NOTIFY" \
  || B_MISS="${B_MISS}a refused message is consumed instead of put back — the operator would never see it$NL"
grep -q 'if sink == nil {' "$NOTIFY" \
  || B_MISS="${B_MISS}the no-sink case is not handled — the boot order would drop the verdict$NL"
grep -q 'alertSinkMu' "$NOTIFY" \
  || B_MISS="${B_MISS}the registered sink is not mutex-guarded$NL"
if [ -z "$B_MISS" ]; then
  ok "B: delivery clears the marker only after a sink accepted it, and never twice"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"
fi

# --- C: every silent reason is closed ---------------------------------------
C_MISS=""
# the three resolver exits that end a job
n_owed=$(grep -c 'owedScheduledVerdict(store, store.Get())' "$SWAP" || true)
[ "${n_owed:-0}" -ge 3 ] \
  || C_MISS="${C_MISS}the resolver records a verdict on only ${n_owed:-0} of its 3 finalizing paths$NL"
grep -q 'func owedScheduledVerdict' "$NOTIFY" \
  || C_MISS="${C_MISS}owedScheduledVerdict is missing$NL"
grep -q 'st.Trigger != TriggerScheduled' "$NOTIFY" \
  || C_MISS="${C_MISS}a manual job is not excluded — its own browser already watches it$NL"
grep -q 'case PhaseFailed, PhaseRolledBack:' "$NOTIFY" \
  || C_MISS="${C_MISS}a rolled-back job is reported as nothing$NL"
if [ -z "$C_MISS" ]; then
  ok "C: success, swap-log failure, stalled job and rollback all record a verdict; manual jobs do not"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "C: $l"; done <<< "$C_MISS"
fi

# --- D: the wire-up exists ---------------------------------------------------
D_MISS=""
grep -q 'deps.State.MarkScheduled()' "$SCHED" \
  || D_MISS="${D_MISS}the scheduler does not mark its own jobs$NL"
grep -q 'update.SetAlertSink(schedulerNotifierSink(app.Notifier))' "$WIRE" \
  || D_MISS="${D_MISS}no sink is registered where the Telegram client exists$NL"
grep -q 'AlertPendingScheduledVerdict(deps.State)' "$SCHED" \
  || D_MISS="${D_MISS}the scheduler never flushes a verdict left by a previous boot$NL"
grep -q 'voidedScheduledVerdict\|func owedScheduledVerdict' "$NOTIFY" \
  || D_MISS="${D_MISS}owedScheduledVerdict is not defined$NL"
if [ -z "$D_MISS" ]; then
  ok "D: jobs are marked scheduled, the sink is registered at the wire-up, and a leftover verdict is flushed"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "D: $l"; done <<< "$D_MISS"
fi

# --- E: the sentence exists in BOTH catalogues -------------------------------
CAT=internal/i18n/catalog_bot.go
E_MISS=""
for k in alert.update.scheduled_ok alert.update.scheduled_failed; do
  n=$(grep -c "\"$k\":" "$CAT" || true)
  [ "${n:-0}" -ge 2 ] || E_MISS="${E_MISS}${k} is not defined twice (RU + EN) in ${CAT}$NL"
done
if [ -z "$E_MISS" ]; then
  ok "E: both verdict lines exist in RU and EN, so the delivered text is a sentence, never the key"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "E: $l"; done <<< "$E_MISS"
fi

# --- F: the unit tests run ---------------------------------------------------
OUT="$(go test ./internal/update/ -count=1 -run 'TestB537' 2>&1)"; rc=$?
if [ "$rc" -eq 0 ]; then
  ok "F: the B537 unit tests pass ($(printf '%s' "$OUT" | tail -1))"
else
  bad "F: TestB537* failed: $(printf '%s' "$OUT" | tail -3)"
fi

# --- G: bookkeeping ----------------------------------------------------------
G_MISS=""
git ls-files --error-unmatch scripts/check_b537_scheduled_verdict_reaches_the_operator.sh >/dev/null 2>&1 \
  || G_MISS="${G_MISS}this script is NOT tracked by git (trap 11)$NL"
for f in "$NOTIFY" "$TEST"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 \
    || G_MISS="${G_MISS}${f} is NOT tracked by git$NL"
done
grep -q '"B537"' scripts/verify_pre_deploy.sh || G_MISS="${G_MISS}verify_pre_deploy.sh does not register B537$NL"
grep -q '^- \*\*B537\*\*' AGENTS.md || G_MISS="${G_MISS}AGENTS.md's block index carries no B537 line$NL"
if [ -z "$G_MISS" ]; then ok "G: tracked, registered and indexed"
else while IFS= read -r l; do [ -n "$l" ] && bad "G: $l"; done <<< "$G_MISS"; fi

printf '\nB537: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
