#!/usr/bin/env bash
# check_b536_duplicate_report_is_not_a_failure.sh
#
# B536 (2026-11-28) — a REPORT must not wear the failure's costume.
#
# The operator's screen (Telegram, 08:05, right under «⏰ Плановое обновление …»):
#
#   ❌ skygate tag autoupdate FAILED
#   node: 198 (skybars-1)
#   failed tag: tag:dev-skyadmin-skybars-1
#   reason: unknown
#   error: duplicate name: "skybars-1" … shares the base name of "skybars" …
#
# Every label in that message was wrong for what happened: no tag was written, nothing
# failed, and the "reason" is not `unknown` — it is B534's detector REPORTING one device
# behind two headscale nodes. The operator read the pair as "the update failed".
#
# WHAT THIS CONTRACT PINS
#   A. the report has its own channel: `ReportDuplicate`, audit `node.duplicate`, and the
#      pair still leaves the process through audit + metric + Telegram;
#   B. the tag-failure metric is NOT touched by a report — a report that increments
#      skygate_tag_autoupdate_failures_total makes that series lie, which is exactly what
#      happened live (reason="unknown" for events where no tag write was attempted);
#   C. the message says what it is: a duplicate, the pair, the evidence kind and the
#      action — and contains none of the failure's words;
#   D. the report is rate-limited per node AND per kind, so the 5-minute detector cannot
#      flood the chat while a new node or a new kind still gets through;
#   E. the B534 contract was renegotiated in the same commit (its row no longer asserts the
#      failure call), and the files are tracked/registered/indexed.
#
# WHERE IT SKIPS: nowhere — source contract + unit tests.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B536: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
NL=$'\n'

SINK=internal/nodeownership/auto_alert_b536.go
ALERT=internal/nodeownership/auto_alert.go
TICK=internal/nodeownership/auto.go
METRIC=internal/metrics/collector.go
TEST=internal/nodeownership/auto_alert_b536_test.go

hdr "B536 — a duplicate-node report is a report, never a failed tag write"

# --- A: its own channel -------------------------------------------------------
A_MISS=""
grep -q 'func (s \*TagAlertSink) ReportDuplicate(d NodeDuplicate)' "$SINK" \
  || A_MISS="${A_MISS}ReportDuplicate is missing${NL}"
grep -q 'duplicateAlertAction = "node.duplicate"' "$SINK" \
  || A_MISS="${A_MISS}the audit action is not its own noun (node.duplicate)${NL}"
grep -q 'alertSink.ReportDuplicate(dup)' "$TICK" \
  || A_MISS="${A_MISS}the maintenance tick does not use the duplicate channel${NL}"
if [ -z "$A_MISS" ]; then
  ok "A: the pair leaves through its own channel (audit node.duplicate + metric + Telegram)"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "A: $l"; done <<< "$A_MISS"
fi

# --- B: the failure metric stays untouched -----------------------------------
B_MISS=""
if sed -n '/func (s \*TagAlertSink) ReportDuplicate/,/^}/p' "$SINK" | grep -q 'TagAutoupdateFailuresCounter'; then
  B_MISS="${B_MISS}ReportDuplicate increments the TAG-FAILURE counter — that series would report reports as failures${NL}"
fi
grep -q 'var DuplicateNodesCounter = NewCounterVec(' "$METRIC" \
  || B_MISS="${B_MISS}skygate_duplicate_nodes_total is not declared${NL}"
grep -q '"skygate_duplicate_nodes_total"' "$METRIC" \
  || B_MISS="${B_MISS}the duplicate counter has no metric name${NL}"
grep -q 'metrics.DuplicateNodesCounter' "$SINK" \
  || B_MISS="${B_MISS}the report does not increment its own counter${NL}"
if [ -z "$B_MISS" ]; then
  ok "B: the report bumps skygate_duplicate_nodes_total and leaves the tag-failure metric alone"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"
fi

# --- C: the words describe a report ------------------------------------------
# The forbidden strings are checked in the TEXT BUILDER only: the file's header quotes the
# live message verbatim (that is the evidence), so a whole-file grep would fail on its own
# documentation — the same trap B536 is about, one level down.
TEXT_FN="$(sed -n '/func buildDuplicateText(/,/^}/p' "$SINK")"
C_MISS=""
if [ -z "$TEXT_FN" ]; then
  C_MISS="${C_MISS}cannot find buildDuplicateText in ${SINK}${NL}"
fi
for forbidden in 'tag autoupdate FAILED' 'failed tag' 'reason: unknown'; do
  case "$TEXT_FN" in
    *"$forbidden"*) C_MISS="${C_MISS}the duplicate text still contains '${forbidden}'${NL}" ;;
  esac
done
case "$TEXT_FN" in
  *"Дубликат узла"*) ;;
  *) C_MISS="${C_MISS}the title does not say what the message is${NL}" ;;
esac
case "$TEXT_FN" in
  *"НЕ ошибка тегирования"*) ;;
  *) C_MISS="${C_MISS}the message does not say that it is NOT a tag failure — the reader's first conclusion${NL}" ;;
esac
case "$TEXT_FN" in
  *"что делать:"*) ;;
  *) C_MISS="${C_MISS}the message states no action${NL}" ;;
esac
if [ -z "$C_MISS" ]; then
  ok "C: the message names the pair, the evidence, the action and explicitly denies a tag failure"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "C: $l"; done <<< "$C_MISS"
fi

# --- D: rate limit per node AND per kind -------------------------------------
D_MISS=""
grep -q 'd.NewID + "\\x00duplicate\\x00" + kind' "$SINK" \
  || D_MISS="${D_MISS}the rate-limit key is not (node, kind) — five ticks a minute would flood, a new kind would be silently dropped${NL}"
grep -q 'rateLimitWindow' "$SINK" \
  || D_MISS="${D_MISS}the report is not rate-limited at all${NL}"
if [ -z "$D_MISS" ]; then ok "D: the report is rate-limited per node and per kind"
else while IFS= read -r l; do [ -n "$l" ] && bad "D: $l"; done <<< "$D_MISS"; fi

# --- E: the old contract moved with it, and bookkeeping ----------------------
E_MISS=""
# The regex dot, not the literal: check_b534 carries this detector line itself, so a
# literal pattern would match its own source and report a defect that is not there.
if grep -q "ReportFailure(dup\." scripts/check_b534_duplicate_nodes.sh; then
  E_MISS="${E_MISS}check_b534 still asserts the FAILURE call — the contract would pin the defect${NL}"
fi
grep -q 'ReportDuplicate(dup)' scripts/check_b534_duplicate_nodes.sh \
  || E_MISS="${E_MISS}check_b534 does not assert the duplicate channel${NL}"
git ls-files --error-unmatch "$SINK" >/dev/null 2>&1 || E_MISS="${E_MISS}${SINK} is NOT tracked by git${NL}"
git ls-files --error-unmatch "$TEST" >/dev/null 2>&1 || E_MISS="${E_MISS}${TEST} is NOT tracked by git${NL}"
git ls-files --error-unmatch scripts/check_b536_duplicate_report_is_not_a_failure.sh >/dev/null 2>&1 \
  || E_MISS="${E_MISS}this script is NOT tracked by git (trap 11)${NL}"
grep -q '"B536"' scripts/verify_pre_deploy.sh || E_MISS="${E_MISS}verify_pre_deploy.sh does not register B536${NL}"
grep -q '^- \*\*B536\*\*' AGENTS.md || E_MISS="${E_MISS}AGENTS.md's block index carries no B536 line${NL}"
if [ -z "$E_MISS" ]; then ok "E: B534 renegotiated, files tracked, block registered and indexed"
else while IFS= read -r l; do [ -n "$l" ] && bad "E: $l"; done <<< "$E_MISS"; fi

# --- F: the unit tests run ---------------------------------------------------
OUT="$(go test ./internal/nodeownership/ -count=1 -run 'TestB536' 2>&1)"; rc=$?
if [ "$rc" -eq 0 ]; then
  ok "F: the B536 unit tests pass ($(printf '%s' "$OUT" | tail -1))"
else
  bad "F: TestB536* failed: $(printf '%s' "$OUT" | tail -3)"
fi

printf '\nB536: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
