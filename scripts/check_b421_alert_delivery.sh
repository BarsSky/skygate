#!/usr/bin/env bash
# check_b421_alert_delivery.sh — B421 (2026-10-25)
#
# «УВЕДОМЛЕНИЯ ДОЛЖНЫ ЛИБО ПРИХОДИТЬ, ЛИБО НАЗЫВАТЬ ПРИЧИНУ, ПО КОТОРОЙ НЕ ПРИШЛИ.»
#
# The operator reported that notifications which should have arrived did not. Reading
# the code found a defect that produces exactly that, silently:
#
#	alerts and replies resolved their RECIPIENT differently.
#
# `/admin/telegram`'s «Отправить тест» sends a REPLY (SendTelegramErr), which falls back
# to a chat bound with /start + [Bind] when global_settings.telegram.chat_id is empty —
# that fallback exists on purpose (see the Notifier interface comment). Notifications
# went through sendAlert, which asked db.LoadTelegramSendTarget (token AND chat_id from
# global_settings) and did:
#
#	if err != nil || !ok { return 0 }
#
# — no log line, no counter, no audit row. On an installation where the bot was
# configured by binding a chat, the bot answered commands, the test said «отправлено»,
# and EVERY alert was dropped. A tester that exercises a different path than the product
# proves the wrong thing, and an outcome nobody records cannot be investigated.
#
# WHAT THIS BLOCK CHANGES
#  1. ONE resolution rule for alerts: global token+chat_id → an ADMIN binding → nothing,
#     and "nothing" is NAMED. A non-admin binding is never used: alerts name hostnames,
#     IPs and certificate subjects, so falling back to a member's chat would turn a
#     delivery fix into a leak.
#  2. A ledger: one counter per outcome (sent / suppressed / not configured / failed),
#     the last outcome with its reason, the resolved target (masked) and the transport
#     that carried the last delivery. Rendered on /admin/telegram, so «почему не пришло»
#     is answerable without SSH.
#  3. Rate-limited logging: the FIRST occurrence of an outcome logs immediately, repeats
#     at most once per window (a crash-looping producer cannot flood the journal).
#  4. A test button that walks the ALERT path (SendAlertEvent → resolution → HTML
#     transport) and reports the outcome, next to the existing reply test which is now
#     honestly a different thing.
#
# CONTRACTS (26)
#   A  one resolution rule, admin-only fallback (source + behaviour)
#   B  no silent drop: every non-sent outcome is recorded and named
#   C  the page renders the ledger (template + state + i18n RU/EN pairs)
#   D  the alert test goes through the ALERT path, not the reply path
#   E  behaviour: the ledger, the fallback and the real transport (fake Bot API)
#   F  the ~40 legacy producers reach the same transport
#   G  registered, indexed, tracked, documented

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b421.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

LEDGER=internal/telegram/alert_ledger.go
ALERTS=internal/telegram/alerts.go
STATE=internal/feature/admin/telegram_state.go
ACTIONS=internal/feature/admin/telegram_actions.go
DISPATCH=internal/feature/admin/telegram_handlers.go
TPL=internal/handlers/templates/admin/telegram.html
CAT=internal/i18n/catalog_telegram.go
GATE=scripts/verify_pre_deploy.sh

for f in "$LEDGER" "$ALERTS" "$STATE" "$ACTIONS" "$DISPATCH" "$TPL" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# --- A: one resolution rule ------------------------------------------------
hdr "A. one recipient rule for alerts, and it never leaks"

if grep -qF 'func resolveAlertTarget(' "$LEDGER"; then
  ok "A1: resolveAlertTarget is the single recipient rule for alerts"
else
  bad "A1: no resolveAlertTarget — the alert path and the reply path can drift apart again"
fi
if grep -qF 'db.LoadTelegramSendTarget(' "$LEDGER" && grep -qF 'db.ListTelegramBindings(' "$LEDGER"; then
  ok "A2: it consults the configured pair first, then the bindings"
else
  bad "A2: the rule does not consult both sources"
fi
if grep -qF 'if !b.IsAdmin || b.ChatID == 0 {' "$LEDGER"; then
  ok "A3: a non-admin binding is skipped by construction (alerts must never reach a member chat)"
else
  bad "A3: the binding fallback does not filter on IsAdmin — that would be a data leak, not a fix"
fi
if grep -qF 'TargetAdminBind' "$LEDGER" && grep -qF 'TargetGlobalChat' "$LEDGER"; then
  ok "A4: the resolved target's SOURCE is recorded (global chat or admin binding), so 'delivered' has a context"
else
  bad "A4: the source of the target is not recorded"
fi

# --- B: no silent drop -----------------------------------------------------
hdr "B. no alert disappears without a name"

if grep -qF 'recordAlertOutcome(' "$ALERTS" && grep -qF 'func recordAlertOutcome(' "$LEDGER"; then
  ok "B1: sendAlert records an outcome on every path"
else
  bad "B1: sendAlert does not record outcomes — the pre-B421 silent return 0 can come back"
fi
# The exact pre-fix shape must be gone: a bare early return whose only guard is !ok.
if grep -n 'if err != nil || !ok {' "$ALERTS" | grep -vq '//'; then
  bad "B2: the pre-fix silent guard (if err != nil || !ok { return 0 }) is back in alerts.go"
else
  ok "B2: the silent guard that dropped every alert is gone"
fi
for o in AlertNoToken AlertNoChat AlertBadChat AlertSendFailed AlertSuppressed; do
  if grep -qF "$o" "$LEDGER"; then
    ok "B3: outcome $o is a named state"
  else
    bad "B3: outcome $o does not exist — one class of failure would stay invisible"
  fi
done
if grep -qF 'alertLogWindow' "$LEDGER" && grep -qF 'loggedAt' "$LEDGER"; then
  ok "B4: logging is rate-limited per outcome+reason (first occurrence always logs)"
else
  bad "B4: no rate limit — a crash-looping producer would flood the journal and nobody would read it"
fi
if grep -qF 'maskChatID(' "$LEDGER"; then
  ok "B5: the chat id is masked in the ledger (it is a personal identifier)"
else
  bad "B5: the ledger stores a raw chat id"
fi

# --- C: the page tells the truth -------------------------------------------
hdr "C. the page renders the alert path's own state"

if grep -qF 'AlertDelivery telegram.AlertDeliveryStats' "$STATE"; then
  ok "C1: the page state carries the alert delivery stats"
else
  bad "C1: the page state has no alert delivery stats — the operator cannot see why nothing arrives"
fi
if grep -qF 'state.AlertDelivery = telegram.AlertDeliverySnapshot()' "$STATE"; then
  ok "C2: the loader reads the ledger UNCONDITIONALLY (before the token early-return)"
else
  bad "C2: the loader fills the stats only when a token exists — the exact case this block is about would render nothing"
fi
if grep -qF 'telegram.alert_delivery_title' "$TPL" && grep -qF 'AlertDelivery.Sent' "$TPL"; then
  ok "C3: the template shows the card with the counters"
else
  bad "C3: the template has no alert delivery card"
fi
if grep -qF 'telegram.alert_delivery_last' "$TPL" && grep -qF 'AlertDelivery.LastReason' "$TPL"; then
  ok "C4: the card shows the last outcome WITH its reason (a count alone would not explain a drop)"
else
  bad "C4: the card does not show the last outcome/reason"
fi
if grep -qF 'AlertDelivery.LastPath' "$TPL"; then
  ok "C5: the card shows the transport that carried the last delivery (direct / relay)"
else
  bad "C5: the transport path is not rendered"
fi
pairs=0
for k in alert_delivery_title alert_delivery_help alert_delivery_sent alert_delivery_suppressed \
         alert_delivery_not_configured alert_delivery_failed alert_delivery_last \
         alert_delivery_target alert_delivery_path alert_delivery_never \
         alert_test_button alert_test_help alert_test_title alert_test_fact_source alert_test_fact_path; do
  n=$(grep -c "\"telegram\.$k\"" "$CAT")
  if [ "$n" -lt 2 ]; then bad "C6: telegram.$k is missing from one language (found $n of 2)"; else pairs=$((pairs+1)); fi
done
[ "$pairs" -eq 15 ] && ok "C6: all 15 new keys exist in RU and EN"

# --- D: the test button tests the product path ----------------------------
hdr "D. the test button exercises the ALERT path"

if grep -qF 'func (s *Service) handleTelegramAlertTest(' "$ACTIONS"; then
  ok "D1: a dedicated alert-test handler exists"
else
  bad "D1: no alert-test handler"
fi
if grep -qF 'SendAlertEvent(telegram.Alert{' "$ACTIONS"; then
  ok "D2: it sends through SendAlertEvent — the same entry point real notifications use"
else
  bad "D2: the alert test does not go through SendAlertEvent"
fi
# Code-only counting: this block's own comments DISCUSS DedupKey (explaining why the test
# alert must not set one), and a contract that cannot tell code from prose would force us
# to stop documenting the decision.
code_count() { # code_count <pattern> <file...>
  local pat="$1"; shift
  awk -v pat="$pat" '
    { line=$0; t=line; sub(/^[ \t]+/, "", t) }
    t ~ /^\/\// { next }
    index(line, pat) { n++ }
    END { print n+0 }' "$@"
}

if [ "$(code_count 'DedupKey:' "$ACTIONS")" -ne 0 ]; then
  bad "D3: the alert test sets a DedupKey — a second press would be suppressed and prove nothing"
else
  ok "D3: the alert test never sets a DedupKey (a test must always attempt a real send)"
fi
if grep -qF 'AlertDeliverySnapshot()' "$ACTIONS"; then
  ok "D4: the handler reports what the ledger recorded instead of assuming success"
else
  bad "D4: the handler does not read the ledger — it would claim delivery without evidence"
fi
if grep -qF 'case "alert_test":' "$DISPATCH"; then
  ok "D5: the POST dispatcher routes action=alert_test"
else
  bad "D5: the dispatcher has no alert_test action"
fi
if grep -qF 'name="action" value="alert_test"' "$TPL"; then
  ok "D6: the page has a form that posts action=alert_test"
else
  bad "D6: no button on the page reaches the handler"
fi
if grep -qF 'alertTestFlash(' "$ACTIONS"; then
  ok "D7: the operator-facing sentence is a separate pure function (testable without HTTP)"
else
  bad "D7: the flash is composed inline — the B390 lesson is that this is where a tester learns to lie"
fi
if grep -qF 'telegram_alert_test' "$ACTIONS"; then
  ok "D8: the test is audited (telegram_alert_test) with the outcome"
else
  bad "D8: the alert test leaves no audit row"
fi

# --- E: behaviour ----------------------------------------------------------
hdr "E. behaviour, on a real database and a fake Bot API"

if OUT="$(go test ./internal/telegram/ -run B421 -count=1 2>&1)"; then
  ok "E1: the ledger records every outcome, the fallback resolves, and the real transport delivers"
else
  bad "E1: go test ./internal/telegram -run B421: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
if OUT="$(go test ./internal/feature/admin/ -run B421 -count=1 2>&1)"; then
  ok "E2: the operator-facing sentences distinguish delivered / not delivered / suppressed"
else
  bad "E2: go test ./internal/feature/admin -run B421: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
# The 'pre-fix shape still fails' anchor is what keeps the rationale honest: a member
# binding must be REFUSED, not merely undocumented.
if grep -qF 'TestB421_MemberBindingIsNeverUsedForAlerts' internal/telegram/alert_ledger_b421_test.go; then
  ok "E3: the admin-only rule is pinned by a test that plants a member binding"
else
  bad "E3: nothing proves a member binding is refused"
fi
if grep -qF 'TestB421_SendFailureIsRecordedAndRedacted' internal/telegram/alert_ledger_b421_test.go; then
  ok "E4: a 400 from the Bot API is recorded as a failure AND the token is redacted"
else
  bad "E4: no test pins the failure path (or the redaction)"
fi

# --- F: the legacy producers ----------------------------------------------
hdr "F. the long tail of triggers uses the same transport"

legacy=$(grep -rho '\.SendAlert(' --include='*.go' internal cmd 2>/dev/null | wc -l | tr -d ' ')
if [ "$legacy" -ge 10 ]; then
  ok "F1: $legacy legacy SendAlert call site(s) still exist and all go through sendAlert → the ledger"
else
  bad "F1: only $legacy legacy SendAlert call site(s) — the measurement is wrong, or they were replaced without this contract following"
fi
# A producer that bypasses the alert entry points would be invisible in the ledger and in
# /ack. Only the reply helpers may use the plain send.
bypass=$(grep -rn '\.SendTelegram(' --include='*.go' internal cmd 2>/dev/null \
         | grep -v '_test\.go' | grep -v 'internal/telegram/' | grep -v 'internal/feature/admin/telegram' | wc -l | tr -d ' ')
if [ "$bypass" -eq 0 ]; then
  ok "F2: no producer outside the telegram feature sends a plain reply where an alert belongs"
else
  bad "F2: $bypass producer(s) call SendTelegram directly — those messages never reach /ack and are invisible in the ledger"
fi

# --- G: registration -------------------------------------------------------
hdr "G. registration"

if grep -q 'run_check "B421"' "$GATE"; then
  ok "G1: B421 is registered in the catalog"
else
  bad "G1: B421 is not registered — this contract would never run"
fi
if grep -q '\*\*B421\*\*' AGENTS.md; then
  ok "G2: AGENTS.md block index carries B421"
else
  bad "G2: AGENTS.md has no B421 entry"
fi
if git ls-files --error-unmatch scripts/check_b421_alert_delivery.sh >/dev/null 2>&1; then
  ok "G3: this script is tracked by git (trap 11)"
else
  bad "G3: this script is not tracked by git"
fi
if grep -q 'B421' docs/TELEGRAM.md; then
  ok "G4: docs/TELEGRAM.md documents the alert path and the outcome vocabulary"
else
  bad "G4: no TELEGRAM.md section — the operator's own runbook must name these outcomes"
fi
if grep -q 'B421' docs/LESSONS.md; then
  ok "G5: docs/LESSONS.md carries the lesson"
else
  bad "G5: no LESSONS entry"
fi

printf '\n\033[1mB421 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
