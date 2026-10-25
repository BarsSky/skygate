#!/usr/bin/env bash
# check_b422_alert_coverage.sh — B422 (2026-10-25)
#
# «ТИШИНА ПО ДЕЛУ» И «ТИШИНА ИЗ-ЗА ПОЛОМКИ» ДОЛЖНЫ БЫТЬ РАЗЛИЧИМЫ — И ПОЛУЧАТЕЛЬ НЕ ДОЛЖЕН
# ЗАВИСЕТЬ ОТ ТОГО, ПОМНИТ ЛИ ОПЕРАТОР ВТОРОЕ ПОЛЕ.
#
# The 2026-10-25 investigation of the operator's own install, run with
# scripts/diagnose_alerts.sh, produced a verdict that no surface had shown for months:
#
#	bot token  present (46 chars)      telegram.chat_id  <<ABSENT>>
#	bound chats 1, of them admin 1 (chat 328946535, user=1)
#	audit_log: tag.autoupdate_failed 3082, exit_node_provision failures 5, …
#
# Alerts required `telegram.chat_id`; the bot had been configured by BINDING a chat; the
# manual test used the binding (so it arrived) and alerts did not. 3082 tag failures and
# several failed provisioning runs produced alerts that were dropped with no log line — the
# pre-B421 code simply returned 0. The app even said so at boot:
#
#	🤖 Telegram bot token set (no chat_id yet — receive-only); starting getUpdates loop.
#
# B421 fixed the delivery (admin binding is an acceptable recipient; every outcome is
# named). B422 closes the two remaining gaps the investigation exposed:
#
#  1. THE CONFIGURATION MUST NOT DEPEND ON MEMORY. The page offers ONE CLICK to persist the
#     admin chat that alerts are already using as `telegram.chat_id` — guarded so it never
#     proposes a member's chat and never overwrites a recipient the operator chose.
#  2. A CHANNEL THAT NEVER SPEAKS LOOKS BROKEN. A weekly canary (configurable, persisted,
#     first message delayed after boot so a restart is not a canary) sends ONE short message
#     through the SAME alert path: delivered / failed / recipient.
#  3. The boot line that says which recipient alerts will use now runs as the FIRST
#     statement of the scheduler wiring, so it cannot depend on anything below it (the
#     previous placement was reported missing from a live container's journal).
#
# CONTRACTS (24)
#   A  adoption: one click, admin-only, never over an operator's choice
#   B  the canary: pure decision, persisted, delayed first message, same alert path, off switch
#   C  the boot line is the first statement and names both outcomes
#   D  behaviour: the policy table, the real tick, the guards
#   E  the diagnostic script that produced the verdict exists and is documented
#   F  registered, indexed, tracked, docs

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b422.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

LEDGER=internal/telegram/alert_ledger.go
ACTIONS=internal/feature/admin/telegram_actions.go
DISPATCH=internal/feature/admin/telegram_handlers.go
STATE=internal/feature/admin/telegram_state.go
TPL=internal/handlers/templates/admin/telegram.html
CAT=internal/i18n/catalog_telegram.go
CANARY=internal/alertcanary/canary.go
SCHED=cmd/skygate/main_schedulers.go
DIAG=scripts/diagnose_alerts.sh
GATE=scripts/verify_pre_deploy.sh

for f in "$LEDGER" "$ACTIONS" "$DISPATCH" "$STATE" "$TPL" "$CAT" "$CANARY" "$SCHED" "$DIAG" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# --- A: adoption -----------------------------------------------------------
hdr "A. the recipient must not depend on remembering a second field"

if grep -qF 'func AdoptableAdminChat(' "$LEDGER"; then
  ok "A1: AdoptableAdminChat exists (the one-click offer's source of truth)"
else
  bad "A1: no AdoptableAdminChat — the page could only show an empty chat_id"
fi
if awk '/func AdoptableAdminChat\(/,/^}/' "$LEDGER" | grep -qF 'TargetAdminBind'; then
  ok "A2: only an ADMIN binding is ever proposed (a member chat must never become the alert recipient)"
else
  bad "A2: the adoption does not restrict itself to an admin binding"
fi
if awk '/func AdoptableAdminChat\(/,/^}/' "$LEDGER" | grep -qF 'LoadTelegramSendTarget'; then
  ok "A3: it refuses to propose anything when chat_id is already set (never overwrite the operator's choice)"
else
  bad "A3: the adoption can overwrite a configured recipient"
fi
if grep -qF 'func (s *Service) handleTelegramAdoptChat(' "$ACTIONS"; then
  ok "A4: the adopt handler exists"
else
  bad "A4: no adopt handler"
fi
if grep -qF 'telegram_chat_id_adopted' "$ACTIONS"; then
  ok "A5: the adoption is audited with its source"
else
  bad "A5: the adoption leaves no audit row"
fi
if grep -qF 'case "adopt_chat":' "$DISPATCH"; then
  ok "A6: the dispatcher routes action=adopt_chat"
else
  bad "A6: no adopt_chat action"
fi
if grep -qF 'name="action" value="adopt_chat"' "$TPL" && grep -qF 'AlertAdoptableMasked' "$TPL"; then
  ok "A7: the page shows WHICH chat (masked) and posts the adoption"
else
  bad "A7: the page has no one-click adoption button"
fi
if grep -qF 'AdoptableAdminChat(s.dbc())' "$STATE"; then
  ok "A8: the page state is filled from the same helper (no second rule)"
else
  bad "A8: the page computes adoptability some other way"
fi
pairs=0
for k in alert_adopt_hint alert_adopt_button; do
  n=$(grep -c "\"telegram\.$k\"" "$CAT")
  if [ "$n" -lt 2 ]; then bad "A9: telegram.$k missing from one language ($n of 2)"; else pairs=$((pairs+1)); fi
done
[ "$pairs" -eq 2 ] && ok "A9: the adoption text exists in RU and EN"

# --- B: the canary ---------------------------------------------------------
hdr "B. a channel that never speaks looks broken: the weekly canary"

if grep -qF 'func nextCanary(' "$CANARY"; then
  ok "B1: the policy is a pure function (nextCanary)"
else
  bad "B1: no pure decision — the policy would only be testable through timers"
fi
for branch in 'no alert recipient configured' 'firstDelay' 'interval elapsed' 'disabled'; do
  if grep -qF "$branch" "$CANARY"; then
    ok "B2: the policy names the branch '$branch'"
  else
    bad "B2: the policy has no branch for '$branch'"
  fi
done
if grep -qF 'lastSentKey' "$CANARY" && grep -qF 'db.SetGlobalSetting' "$CANARY"; then
  ok "B3: the last send is persisted (a restart cannot re-announce)"
else
  bad "B3: nothing is persisted — every restart would re-send or skip wrongly"
fi
if grep -qF 'SendAlertEvent(' "$CANARY"; then
  ok "B4: the canary goes through the SAME alert entry point (so it measures the real path)"
else
  bad "B4: the canary bypasses the alert path"
fi
if grep -qF 'if id == 0 {' "$CANARY" && grep -qF 'will retry' "$CANARY"; then
  ok "B5: a failed canary is retried, not counted as sent"
else
  bad "B5: a failed canary could silence the signal for a whole interval"
fi
if grep -qF 'DefaultInterval' "$CANARY" && grep -qF '7 * 24 * time.Hour' "$CANARY"; then
  ok "B6: the default interval is a week"
else
  bad "B6: the default interval is not documented as a week"
fi
if grep -qF 'alertcanary.New(alertcanary.Config{' "$SCHED"; then
  ok "B7: it is wired in the scheduler block of the boot path"
else
  bad "B7: the canary is never started"
fi
if grep -qF 'SKYGATE_ALERT_CANARY_HOURS' "$SCHED"; then
  ok "B8: there is a named off switch (SKYGATE_ALERT_CANARY_HOURS=0)"
else
  bad "B8: no documented way to switch the canary off"
fi

# --- C: the boot line ------------------------------------------------------
hdr "C. the journal says which recipient alerts use"

first_stmt="$(awk '/^func wireOptionalSchedulers\(/,/^$/' "$SCHED" | grep -v '^\s*//' | grep -v '^\s*$' | sed -n '2p')"
case "$first_stmt" in
  *LogAlertTargetAtBoot*) ok "C1: it is the FIRST statement of the scheduler wiring (nothing below can prevent it)" ;;
  *) bad "C1: the first statement is not the alert-target log: $first_stmt" ;;
esac
if grep -qF 'telegram: alerts' "$LEDGER" && grep -qF 'ALERTS NOT DELIVERABLE' "$LEDGER"; then
  ok "C2: it names BOTH outcomes (resolved recipient / named reason there is none)"
else
  bad "C2: the boot log does not cover both outcomes"
fi

# --- D: behaviour ----------------------------------------------------------
hdr "D. behaviour"

if OUT="$(go test ./internal/alertcanary/ -run B422 -count=1 2>&1)"; then
  ok "D1: the policy table and the real tick (send-once, persist, retry after failure) pass"
else
  bad "D1: alertcanary B422 tests failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
if OUT="$(go test ./internal/telegram/ -run B422 -count=1 2>&1)"; then
  ok "D2: adoption guards pass (admin only, never over a configured chat) and the recipient description stays masked"
else
  bad "D2: telegram B422 tests failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
if grep -qF 'TestB422_AdoptableChatRefusesAMember' internal/telegram/adopt_b422_test.go; then
  ok "D3: a member binding being refused is pinned by a test, not by a comment"
else
  bad "D3: nothing proves a member chat is refused"
fi

# --- E: the diagnostic -----------------------------------------------------
hdr "E. the investigation is repeatable in one command"

if [ -x "$DIAG" ] || [ -f "$DIAG" ]; then
  ok "E1: scripts/diagnose_alerts.sh exists"
else
  bad "E1: no diagnostic script"
fi
if bash -n "$DIAG" 2>/dev/null; then
  ok "E2: it is syntactically valid"
else
  bad "E2: the diagnostic script has a syntax error"
fi
for probe in 'telegram.bot_token' 'telegram.chat_id' 'telegram_bindings' 'telegram_alerts' 'tag.autoupdate_failed'; do
  if grep -qF "$probe" "$DIAG"; then
    ok "E3: it inspects $probe"
  else
    bad "E3: it does not inspect $probe — the 2026-10-25 verdict needed exactly that"
  fi
done
if grep -qF 'READ-ONLY' "$DIAG"; then
  ok "E4: it declares itself read-only"
else
  bad "E4: the script does not state that it changes nothing"
fi
if grep -qF 'diagnose_alerts.sh' docs/TELEGRAM.md; then
  ok "E5: the runbook points at it"
else
  bad "E5: TELEGRAM.md does not mention the diagnostic"
fi

# --- F: registration -------------------------------------------------------
hdr "F. registration"

if grep -q 'run_check "B422"' "$GATE"; then
  ok "F1: B422 is registered in the catalog"
else
  bad "F1: B422 is not registered"
fi
if grep -q '\*\*B422\*\*' AGENTS.md; then
  ok "F2: AGENTS.md block index carries B422"
else
  bad "F2: AGENTS.md has no B422 entry"
fi
if git ls-files --error-unmatch scripts/check_b422_alert_coverage.sh >/dev/null 2>&1; then
  ok "F3: this script is tracked by git (trap 11)"
else
  bad "F3: this script is not tracked by git"
fi
if grep -q 'B422' docs/LESSONS.md; then
  ok "F4: docs/LESSONS.md carries the lesson"
else
  bad "F4: no LESSONS entry"
fi
if grep -q 'B422' docs/ROADMAP.md && grep -q 'B422' docs/ru/ROADMAP.md; then
  ok "F5: the roadmap records it in both languages"
else
  bad "F5: the roadmap does not record B422 in both languages"
fi

printf '\n\033[1mB422 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
