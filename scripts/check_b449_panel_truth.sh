#!/usr/bin/env bash
# check_b449_panel_truth.sh — B449 (2026-10-30)
#
# Four panel-truth reports from one round on the reference install:
#
#	«Почему проблемы на двух релеях — что по сути exit node? публичные при этом через них
#	 работает нормально доступ»
#	«На странице правил администратора не хватает у Все правила возможности свернуть этот блок»
#	«плашка появилась после применить подстановку в правила что указывают на не preferred exit
#	 node … предупреждение от прошлых решений что осталось не убранным»
#	«что за проблемные статусы 117 штук»  (answered by the analysis, fixed in the next block)
#
# A. THE RELAY SSH PROBE BUILT AN UNRESOLVABLE HOST.
#    `exit_servers` stores `ssh_target` PLUS a port column, and karolina's row carries the port
#    in both: `root@100.64.0.2:18022` with `ssh_port=18022`. The probe took everything after the
#    `@` as the HOST, and the ssh word builder bracketed anything containing a colon (the IPv6
#    rule), so it asked for `root@[100.64.0.2:18022]`:
#
#	нет SSH к релею: ssh root@[100.64.0.2:18022] … Could not resolve hostname
#
#    aisha's row has an EMPTY ssh_target and got the same unexplained sentence. Both rows are
#    working exit nodes — the probe is skygate's management SSH, not egress — and the panel said
#    «проблема» without saying which of the two it was.
#
# B. THE RELAY-REQUEST RULE PICKER WAS ALWAYS EMPTY. `relayRequestRules` used the SQLite
#    placeholder `?` against PostgreSQL:
#
#	[relay-request] list the user's rules: ERROR: syntax error at or near "AND" (SQLSTATE 42601)
#
#    on every page load, in the journal only. The select listed nothing.
#
# C. «ПРИМЕНИТЬ ПОДСТАНОВКУ» ABORTED ON THE FIRST CONFLICT AND BLAMED THE DATABASE. Moving a rule
#    to the preferred relay can collide with a rule the user already has for the same
#    (device, destination, parent) — routine after a CDN expansion. The loop returned on the
#    FIRST error, so the rules before it had moved, the rest had not, and the operator saw
#    «Не удалось добавить правило: Ошибка базы данных. Попробуйте позже» — the wrong action and a
#    cause that waiting cannot fix.
#
# D. «ВСЕ ПРАВИЛА» HAD NO FOLD. The tree collapses group by group, but the CARD had no fold, so a
#    291-rule page is a wall between the operator and the cards below it.
#
# CONTRACTS: 20.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

TARGET=internal/exitnode/target_b449.go
TARGET_T=internal/exitnode/target_b449_test.go
REQ=internal/feature/exit_rules/relay_request_handlers_b440.go
MY=internal/feature/exit_rules/form_my.go
MY_T=internal/feature/exit_rules/form_my_b449_test.go
MY_TPL=internal/handlers/templates/exit_rules.html
ADMIN_TPL=internal/handlers/templates/admin/exit_rules.html
RENDER_T=internal/handlers/exit_rules_render_b449_test.go
I18N=internal/i18n/catalog_exit_rules.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$TARGET" "$TARGET_T" "$REQ" "$MY" "$MY_T" "$MY_TPL" "$ADMIN_TPL" "$RENDER_T" "$I18N" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B449: target=%s my=%s\n' "$TARGET" "$MY"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b449.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the relay SSH target is parsed once, and only IPv6 is bracketed"
GOFMT_OUT="$(gofmt -l "$TARGET" "$TARGET_T" "$MY" "$MY_T" "$RENDER_T" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$TARGET" 'func ParseSSHTarget(raw string) (SSHTargetSpec, string, bool)'; then
  ok "A2: one parser for [user@]host[:port], and it returns the REASON when it refuses"
else
  bad "A2: no shared parser — the probe and the apply can read the same column differently"
fi
if has internal/feature/admin/exit_nodes_health_b417_1.go 'exitnode.ParseSSHTarget(target)'; then
  ok "A3: the health probe uses it (no private re-parse)"
else
  bad "A3: the probe still splits the string itself"
fi
if has "$TARGET" 'netip.ParseAddr(h)'; then
  ok "A4: only a real IPv6 literal is bracketed — the `host:port` shape is no longer mangled"
else
  bad "A4: the bracket rule is still 'contains a colon' → the 100.64.0.2:18022 bug"
fi
if has internal/exitnode/provision_b412.go 'return SSHHostWord(r.User, r.Host)'; then
  ok "A5: the provisioning/apply path builds the ssh word with the SAME rule"
else
  bad "A5: two bracket rules again"
fi
if has internal/feature/admin/exit_nodes_health_b417_1.go 'доступ через релей от неё не зависит'; then
  ok "A6: the message says the SSH probe is a setting, not the relay's egress"
else
  bad "A6: a management-SSH failure still reads like a broken exit node"
fi

hdr "B. the relay-request picker speaks PostgreSQL"
if has "$REQ" 'WHERE user_id = $1 AND enabled = 1'; then
  ok "B1: relayRequestRules uses \$1"
else
  bad "B1: relayRequestRules still uses ? — syntax error at or near AND"
fi
if has "$REQ" 'WHERE user_id = ?'; then
  bad "B2: a SQLite placeholder survived in this file"
else
  ok "B2: no SQLite placeholder left in the relay-request handlers"
fi

hdr "C. a substitution reports what it did, and one conflict does not abort it"
if has "$MY" 'skipped++'; then
  ok "C1: a conflicting rule is COUNTED instead of aborting the batch"
else
  bad "C1: the loop still returns on the first conflict (partial apply, no report)"
fi
if has "$MY" 'skipped=%d'; then
  ok "C2: the audit records both outcomes (updated/skipped)"
else
  bad "C2: the audit cannot tell a complete move from a partial one"
fi
if has "$MY" '"/my/exit-rules?pref="'; then
  ok "C3: the outcome travels as its own flash parameter, not as the «add rule» error"
else
  bad "C3: the partial result is still sent as err= (wrong action in the banner)"
fi
if has "$MY" 'func preferredSkipReason(' && has "$MY" 'уже есть правило'; then
  ok "C4: the duplicate-key case is explained with the fix, not with «try later»"
else
  bad "C4: the skip reason does not name the real conflict"
fi
RU_KEY="$(printf '%s' "$(cat "$I18N" 2>/dev/null)" | grep -c 'exit_rules.pref_apply_result' || true)"
if [ "${RU_KEY:-0}" -ge 2 ]; then
  ok "C5: exit_rules.pref_apply_result exists in BOTH catalogues (RU + EN)"
else
  bad "C5: only ${RU_KEY:-0} definition(s) of the new banner key"
fi
if has "$MY_TPL" 'id="pref-apply-banner"' && has "$MY_TPL" 'exit_rules.pref_apply_result'; then
  ok "C6: the page renders it in its own (warning) banner"
else
  bad "C6: the outcome has no banner, so a partial substitution still looks silent"
fi

hdr "D. «Все правила» folds as a whole, and remembers it"
if has "$ADMIN_TPL" 'id="admin-all-rules-fold"'; then
  ok "D1: the card is a <details> — the block can be folded"
else
  bad "D1: the card still cannot be folded; a 291-rule page stays a wall"
fi
if has "$ADMIN_TPL" 'skygate.admin.exit_rules.all_open'; then
  ok "D2: the fold choice is remembered across reloads"
else
  bad "D2: the fold re-opens on every page load"
fi
if has "$ADMIN_TPL" 'exit_rules_admin.fold_hint'; then
  ok "D3: the summary carries a hint, so the fold is discoverable"
else
  bad "D3: the fold is invisible"
fi
if has "$RENDER_T" 'TestAdminAllRulesCardFolds_B449' && has "$RENDER_T" 'TestMyExitRulesPrefBanner_B449'; then
  ok "D4: both are pinned by executing the real templates"
else
  bad "D4: no render test for the fold / the banner"
fi

hdr "E. pinned by running the tests"
if ! command -v go >/dev/null 2>&1; then
  skip "E1: no go toolchain on PATH — cannot run the tests here (CI is authoritative)"
else
  go test ./internal/exitnode ./internal/feature/exit_rules ./internal/handlers -run 'B449|PreferredSkip|RuleLabel' -count=1 >"$WORK/t.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "E1: the B449 tests are green (parser, skip reason, both render paths)"
  else
    bad "E1: B449 tests failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
fi

hdr "F. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b449_panel_truth.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: this script is not tracked by git"
fi
if grep -qF 'check_b449_panel_truth' "$GATE" 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B449"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "F3: the B449 catalog description contains a backtick (TD-15)" ;;
  "")    bad "F3: the B449 catalog entry was not found" ;;
  *)     ok "F3: the B449 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B449**' "$AGENTS" 2>/dev/null; then
  ok "F4: indexed in AGENTS.md"
else
  bad "F4: no one-line index entry in AGENTS.md"
fi

printf '\n\033[1mB449 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
