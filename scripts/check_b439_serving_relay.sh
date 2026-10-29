#!/usr/bin/env bash
# check_b439_serving_relay.sh — B439 (2026-10-29)
#
# «НЕСМОТРЯ НА ПОЛИТИКУ АВТО-ВЫБОРА ДОСТУПНОГО EXIT NODE, В ТАБЛИЦЕ ВИСЯТ ПОМЕТКИ О
# РАСХОЖДЕНИИ В ПРЕДПОЧТЕНИИ УСТРОЙСТВА».
#
# Why they hung: since B275 the relay that CARRIES a prefix is decided by the
# `prefix_owner` assignment table, while `device_rules.exit_node_id` became the
# operator's wish. Four answers were still computed from the wish — the per-rule status
# badge, the «N rules reference an exit-node the device does not use» banner, the admin
# page's Applicable/ApprovedInHeadscale pair, and the system test that counts the same
# thing. So a rule whose prefix had been assigned to another relay reported "wrong" /
# "not approved" although its traffic flowed: measured live 2026-10-29, a domain's 25
# ranges owned by karolina while the rule named emilia.
#
# The fix is one pure rule (serving_relay_b439.go) plus its two consequences: the
# CARRIER's approved routes decide "approved", and a mismatch is counted only when
# nothing owns the prefix (so the rule's own relay really is the live intent).
#
# CONTRACTS: 15.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

HELP=internal/feature/exit_rules/serving_relay_b439.go
HELPT=internal/feature/exit_rules/serving_relay_b439_test.go
MY=internal/feature/exit_rules/form_my_rules.go
ADM=internal/feature/exit_rules/form_admin.go
TPL=internal/handlers/templates/exit_rules.html
I18N=internal/i18n/catalog_exit_rules.go
GATE=scripts/verify_pre_deploy.sh
for f in "$HELP" "$HELPT" "$MY" "$ADM" "$TPL" "$I18N" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B439: helper=%s my=%s admin=%s tpl=%s\n' "$HELP" "$MY" "$ADM" "$TPL"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b439.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

count() { local n; n="$(grep -c -- "$2" "$1" 2>/dev/null || true)"; printf '%s' "${n:-0}"; }
has()   { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. one pure answer to «who carries this rule»"
if gofmt -l "$HELP" 2>/dev/null | grep -q .; then
  bad "A1: $HELP is not gofmt-clean"
else
  ok "A1: the helper is gofmt-clean"
fi
# The ORDER is the contract: an owner always wins, because the data plane follows the
# assignment table — reporting the rule's wish as the carrier is the false alarm itself.
if awk '/func ServingRelayFor/,/^}/' "$HELP" | grep -q 'ServingByOwner'; then
  ok "A2: ServingRelayFor returns the OWNER before the rule's own relay"
else
  bad "A2: ServingRelayFor does not prefer the owner — the wish would be reported as the carrier again"
fi
if has "$HELP" 'if NormalizeRelay(owner) != ""'; then
  ok "A3: IsRelayMismatch short-circuits to false whenever a prefix has an owner"
else
  bad "A3: IsRelayMismatch still counts an owner-served rule as a mismatch"
fi
if has "$HELP" 'TrimPrefix(s, "tag:dev-infra-")'; then
  ok "A4: tag and hostname spellings of one relay are comparable"
else
  bad "A4: the helper cannot compare a tag with a hostname"
fi

hdr "B. the behaviour is pinned by tests, and it is the operator's live case"
if go test ./internal/feature/exit_rules -run 'TestB439' -count=1 >"$WORK/t.log" 2>&1; then
  ok "B1: TestB439 suite green (owner wins, real mismatch kept, domain owner needs all ranges)"
else
  bad "B1: TestB439 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if has "$HELPT" 'TestB439_OwnerServedRuleIsNotAMismatch_B439'; then
  ok "B2: the live case (rule=emilia, owner=karolina, pref=karolina) is a named test"
else
  bad "B2: the live case is not pinned"
fi

hdr "C. the user page judges by the carrier, not by the wish"
if has "$MY" 'ownerByPrefix := prefixowner.OwnerByPrefix'; then
  ok "C1: /my/exit-rules loads the assignment table"
else
  bad "C1: /my/exit-rules never reads prefix_owner — the status is still built from the wish"
fi
if has "$MY" 'approvedByExitNode[serving.Host]'; then
  ok "C2: «approved» is decided by the SERVING relay's approved routes"
else
  bad "C2: the approved check still indexes the rule's own relay"
fi
if has "$MY" 'IsRelayMismatch(r.ExitNodeID, pref, owner)'; then
  ok "C3: the «wrong» status and the banner count use the real-mismatch predicate"
else
  bad "C3: the status path still calls IsRuleApplicable — an owner-served rule would be «wrong»"
fi
if has "$MY" '"ServedByRuleID":'; then
  ok "C4: the carrier is exposed to the template"
else
  bad "C4: the template has no way to say which relay carries the rule"
fi

hdr "D. the admin page makes the same judgement"
if has "$ADM" 'ownerByPrefix map[string]string'; then
  ok "D1: annotateRulesWithPrefs takes the assignment table"
else
  bad "D1: the admin annotator has no owner data"
fi
if has "$ADM" 'ruleApprovedInHeadscale(rr[i], serving.Host'; then
  ok "D2: the admin approval check uses the serving host"
else
  bad "D2: the admin approval check still uses the rule's relay"
fi
# Comments are allowed to NAME the old lookup (the prose explains the fix); the
# assertion is about live code, so the comment lines are stripped first.
sed 's://.*::' "$ADM" > "$WORK/adm_nocomment.go"
if ! has "$WORK/adm_nocomment.go" 'approvedByExitNode[rule.ExitNode]'; then
  ok "D3: no admin path indexes approved routes by the rule's wish any more"
else
  bad "D3: an admin path still indexes approved routes by the rule's own relay (live code, not a comment)"
fi

hdr "E. the row explains itself instead of showing a red triangle"
if has "$TPL" 'eq $status "served_by_owner"'; then
  ok "E1: the page renders the «served by X» status"
else
  bad "E1: no served_by_owner branch — a correctly served rule still looks unapproved"
fi
if has "$TPL" 'index $.ServedByRuleID .ID'; then
  ok "E2: the branch names the relay"
else
  bad "E2: the branch does not name the carrier"
fi
for k in exit_rules.served_by_owner exit_rules.served_by_owner_title; do
  n="$(count "$I18N" "\"$k\"")"
  if [ "${n:-0}" -eq 2 ]; then
    ok "E3: $k is defined twice (RU + EN)"
  else
    bad "E3: $k is defined ${n:-0} time(s) — B325 requires a pair"
  fi
done
if go test ./internal/handlers -run 'TestLoadTemplates|TestExitRules' -count=1 >"$WORK/h.log" 2>&1; then
  ok "E4: the templates still parse and render"
else
  bad "E4: handler render tests failed — $(tail -3 "$WORK/h.log" | tr '\n' ' ')"
fi
if python scripts/audit_html_balance.py >"$WORK/b.log" 2>&1; then
  ok "E5: markup still balanced (B436)"
else
  bad "E5: markup became imbalanced — $(tail -1 "$WORK/b.log")"
fi

hdr "F. registered, indexed, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b439_serving_relay.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: this script is not tracked by git"
fi
if grep -qF 'check_b439_serving_relay' "$GATE" 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B439"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "F3: the B439 catalog description contains a backtick (TD-15)" ;;
  "")    bad "F3: the B439 catalog entry was not found" ;;
  *)     ok "F3: the B439 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB439 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
