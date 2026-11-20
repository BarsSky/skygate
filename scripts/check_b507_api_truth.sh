#!/usr/bin/env bash
# check_b507_api_truth.sh — B507 (2026-11-20)
#
# THE NOTES, from a live run of the assistant against `POST /my/exit-rules/api`.
# Five of them were measured in the code and are fixed here; the numbering is the
# reviewer's own.
#
#  1. `duplicates` was always 0 AND an existing rule was counted as new:
#     `dupCount := 0` was declared and never incremented, while
#     `insertRuleUnique` returned `(true, existingID)` — so a RETRY of a batch read
#     as "added another N". The agent package promises the opposite ("re-sending is
#     safe"), so the API contradicted its own documentation.
#  2. B480 was reported as SUCCESS: when the target was already pinned to a
#     DIFFERENT relay, the writer wrote nothing and returned the OLD row's id as
#     `added`, so the assistant believed it had pinned `emilia` while the traffic
#     stayed on the previous relay.
#  3. `target_type` / `target_value` / `exit_node` were unvalidated — only
#     `device_id != 0` and a non-empty value were checked, so `subnet 1.2.3.4/99`,
#     `domain mega..nz` and a misspelled relay all became INERT rows with no error
#     anywhere.
#  4. The answer never said whether the ACL was applied: a failed `SetPolicy` still
#     produced `200 {"added":N}`, with the failure visible only in Telegram and the
#     ACL history — i.e. "the rules are in place" was sometimes untrue.
#  3b. The per-device route script answered `# No IP/subnet exit rules configured`
#     on PostgreSQL because two filters shared `$1`: `user_id = $1` and
#     `device_id = $1` while TWO arguments were passed. Measured: 368 routes
#     without `device_id`, empty with `device_id=9`.
#
# What is pinned here:
#   A. the writer reports an OUTCOME (created / existing / other-relay / failed)
#   B. the API's answer carries created/existing, per-row results and the ACL status
#   C. a failed ACL apply is not a 200
#   D. the row is validated before it can reach the database
#   E. the per-device script filter is the SECOND placeholder
#   F. the agent package no longer promises a different contract than the API keeps
#   G. behaviour: the package tests run (SKIP without a Go toolchain)

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

API=internal/feature/exit_rules/api.go
STORE=internal/feature/exit_rules/store.go
OUTCOME=internal/feature/exit_rules/rule_insert_b507.go
VALID=internal/feature/exit_rules/api_validate_b507.go
SCRIPT=internal/feature/exit_rules/routescript_data.go
BRIEF=internal/feature/exit_rules/agent_brief.go
TEST=internal/feature/exit_rules/api_b507_test.go
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
GATE=scripts/verify_pre_deploy.sh

for f in "$API" "$STORE" "$OUTCOME" "$VALID" "$SCRIPT" "$BRIEF" "$TEST" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B507 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

API_TXT="$(cat "$API")"
STORE_TXT="$(cat "$STORE")"
OUT_TXT="$(cat "$OUTCOME")"
VAL_TXT="$(cat "$VALID")"

# ── A. an outcome, not a bool ───────────────────────────────────────────────
for w in ruleInsertCreated ruleInsertExisting ruleInsertOtherRelay ruleInsertFailed; do
  case "$OUT_TXT" in
    *"$w"*) ok "A: the outcome type carries $w" ;;
    *) bad "A: $w is missing from the outcome type" ;;
  esac
done
case "$STORE_TXT" in
  *'func (s *Service) insertRuleUnique('*') ruleInsertResult'*)
    ok "A1: the writer returns ruleInsertResult (a bool cannot say which of the four happened)" ;;
  *) bad "A1: insertRuleUnique still returns a bool" ;;
esac
case "$STORE_TXT" in
  *'ruleInsertResult{Outcome: ruleInsertOtherRelay'*) ok "A2: the B480 case has its OWN outcome" ;;
  *) bad "A2: the 'target owned by another relay' case is still indistinguishable from success" ;;
esac
case "$STORE_TXT" in
  *'Outcome: ruleInsertExisting'*) ok "A3: an idempotent re-send is reported as existing" ;;
  *) bad "A3: an existing rule is still reported as written" ;;
esac
# Every caller must be adapted — a leftover `ok, id :=` would not compile, but a
# caller that silently treats other-relay as success is the bug coming back.
CALLERS="$(grep -l 'insertRuleUnique(' internal/feature/exit_rules/*.go | grep -v '_test.go' | grep -v 'store.go' | wc -l | tr -d ' ')"
if [ "${CALLERS:-0}" -ge 4 ]; then
  ok "A4: all ${CALLERS} production callers were adapted"
else
  bad "A4: only ${CALLERS} production caller(s) found - expected 4 (api, bulk_add, form_my, all_devices)"
fi

# ── B. the answer says what happened ────────────────────────────────────────
case "$API_TXT" in
  *'"created":    created'*|*'"created": created'*|*'"created":created'*)
    ok "B1: the response separates created from existing" ;;
  *) bad "B1: the response still reports one `added` number" ;;
esac
case "$API_TXT" in
  *'"results":'*) ok "B2: per-row results are returned (a partial success can be matched to the plan)" ;;
  *) bad "B2: no per-row results — parallel arrays cannot be matched back" ;;
esac
case "$API_TXT" in
  *'ruleStatusFor(ins)'*) ok "B3: the row's status comes from the writer's outcome" ;;
  *) bad "B3: the response does not consult the outcome" ;;
esac
case "$API_TXT" in
  *'"acl":'*) ok "B4: the response carries the ACL result" ;;
  *) bad "B4: the response says nothing about whether the policy was applied" ;;
esac
case "$API_TXT" in
  *'"applied": aclApplied'*) ok "B5: acl.applied is the real verdict" ;;
  *) bad "B5: acl.applied is not wired to the SetPolicy result" ;;
esac
case "$API_TXT" in
  *'func aclStatus('*) ok "B6: applied / not_needed / failed are named states" ;;
  *) bad "B6: a pure retry cannot be told apart from a failed apply" ;;
esac

# ── C. a failed apply is not a 200 ──────────────────────────────────────────
case "$API_TXT" in
  *'http.StatusBadGateway'*) ok "C1: a failed ACL apply answers 502, not 200" ;;
  *) bad "C1: a failed apply still answers 200 — the lie that started this block" ;;
esac
case "$API_TXT" in
  *'if created > 0 {'*) ok "C2: the policy is re-applied only when a row was actually written" ;;
  *) bad "C2: apply is still keyed on the total (an idempotent retry re-applies for nothing)" ;;
esac

# ── D. validation before the database ──────────────────────────────────────
case "$VAL_TXT" in
  *'func validateRuleTarget('*) ok "D1: target_type/value are validated" ;;
  *) bad "D1: no target validation" ;;
esac
case "$VAL_TXT" in
  *'func validateExitNode('*) ok "D2: exit_node is checked against the relays that exist" ;;
  *) bad "D2: a relay typo still becomes an inert rule" ;;
esac
case "$VAL_TXT" in
  *'available: %s'*) ok "D3: the relay error lists the available relays" ;;
  *) bad "D3: the relay error does not say what IS available" ;;
esac
case "$VAL_TXT" in
  *'double dot'*) ok "D4: the live `mega..nz` shape is refused by name" ;;
  *) bad "D4: an empty label in a domain is not refused" ;;
esac
case "$API_TXT" in
  *'validateRuleTarget(rl.TargetType, rl.TargetValue)'*) ok "D5: the handler runs the validation before the writer" ;;
  *) bad "D5: the validators exist but the API never calls them" ;;
esac

# ── E. the per-device script filter ────────────────────────────────────────
case "$(cat "$SCRIPT")" in
  *'db.PlaceholdersRange(2, 2)'*) ok "E1: the device filter is \$2 (userID already took \$1)" ;;
  *) bad "E1: the device filter still reuses \$1 - the per-device script is empty on PG" ;;
esac
case "$(cat "$TEST")" in
  *'TestB507_DeviceScriptIsNotEmpty'*) ok "E2: the operator's contract is pinned behaviourally (script for a device contains its CIDR)" ;;
  *) bad "E2: no behaviour test for the per-device script" ;;
esac

# ── F. the agent package agrees with the API ───────────────────────────────
# The brief is a Go string, so its JSON keys appear escaped (`\"created\"`); match
# the prose the assistant actually reads instead of the quoting.
case "$(cat "$BRIEF")" in
  *'counts rows that were WRITTEN'*) ok "F1: the brief explains created vs existing" ;;
  *) bad "F1: the brief still describes one 'added' counter" ;;
esac
case "$(cat "$BRIEF")" in
  *'A retry of the same batch answers'*) ok "F1b: the brief states what a retry looks like (the idempotency promise this block repairs)" ;;
  *) bad "F1b: the brief does not tell the assistant what a retry returns" ;;
esac
case "$(cat "$BRIEF")" in
  *'acl.applied'*) ok "F2: the brief tells the assistant that acl.applied:false means NOT in force" ;;
  *) bad "F2: the brief does not mention the ACL verdict" ;;
esac
case "$(cat "$BRIEF")" in
  *'already pinned to a DIFFERENT relay'*) ok "F3: the brief explains the B480 error instead of promising a no-op" ;;
  *) bad "F3: the brief would still let the assistant believe a refused relay pin succeeded" ;;
esac

# ── G. behaviour ────────────────────────────────────────────────────────────
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ -run 'B507' -count=1 2>&1)"
  if grep -qE '^(ok|PASS)' <<< "$OUT"; then
    ok "G1: the B507 tests are green (validators, outcome→status, per-device script)"
  else
    bad "G1: go test -run B507 failed:
$(printf '%s\n' "$OUT" | tail -20)"
  fi
else
  skip "G1: no Go toolchain on this host"
fi
if grep -q 'check_b507_api_truth.sh' "$GATE"; then
  ok "G2: registered in the pre-deploy catalog"
else
  bad "G2: not registered in $GATE"
fi
if git ls-files --error-unmatch scripts/check_b507_api_truth.sh >/dev/null 2>&1; then
  ok "G3: git tracks this script (trap #11)"
else
  bad "G3: this script is not tracked by git (trap #11)"
fi
if grep -q 'B507' "$AGENTS"; then
  ok "G4: the AGENTS block index carries its line"
else
  bad "G4: no B507 line in $AGENTS"
fi
for rd in docs/ROADMAP.md docs/ru/ROADMAP.md; do
  if grep -q 'B507' "$rd"; then
    ok "G5: $rd records the block"
  else
    bad "G5: $rd does not mention B507"
  fi
done
if grep -q 'B507' "$NOTES"; then
  ok "G6: the release notes describe it"
else
  bad "G6: no B507 section in $NOTES"
fi

echo
echo "B507 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
