#!/usr/bin/env bash
# check_b440_relay_requests.sh — B440 (2026-10-29)
#
# «НЕОБХОДИМО ЧТОБЫ ПОЛЬЗОВАТЕЛЬ МОГ ДЕЛАТЬ ЗАПРОС НА СМЕНУ EXIT NODE ДЛЯ ПРАВИЛА И У
# АДМИНИСТРАТОРА ИМЕЛАСЬ ВОЗМОЖНОСТЬ ВЫСТАВИТЬ ПОЛИТИКУ ПОВЕДЕНИЯ НА ТАКИЕ ЗАПРОСЫ —
# К ПРИМЕРУ ОТ ТАКОГО ТО ПОЛЬЗОВАТЕЛЯ ВСЕГДА РАЗРЕШАТЬ ЗАПРОС НА ДРУГОЙ НОД ОТ ТАКОГО
# ТО НЕТ И ОБЩАЯ ПОЛИТИКА И ОПОВЕЩАТЬ О ТАКИХ ЗАПРОСАХ АДМИНИСТРАТОРА В УВЕДОМЛЕНИЯХ И
# ТЕЛЕГРАММЕ»
#
# The model the contracts pin, because getting it wrong would ship a lie: headscale serves
# a prefix from ONE relay (B274/B275), so a request is not «route my packets elsewhere» —
# it is «make THIS relay the owner of these addresses». The user asks; the policy decides
# (global row, overridden per user, default require_approval); an approval IS the
# operator's manual pin plus the SAME ACL re-apply the exit-nodes page runs. Nothing here
# invents a second routing pipeline, and a policy of `deny` writes no row at all.
#
# CONTRACTS: 32.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

CORE=internal/feature/exit_rules/relay_request_b440.go
CORE_T=internal/feature/exit_rules/relay_request_b440_test.go
HTTP=internal/feature/exit_rules/relay_request_handlers_b440.go
MIG=internal/db/migrations_v0_82_relay_requests.go
MIGS=internal/db/driver_sqlite.go
MIGP=internal/db/driver_postgres.go
MY=internal/feature/exit_rules/form_my_rules.go
ADM=internal/feature/exit_rules/form_admin.go
TPL=internal/handlers/templates/exit_rules.html
TPLA=internal/handlers/templates/admin/exit_rules.html
ROUTES=cmd/skygate/routes.go
I18N=internal/i18n/catalog_exit_rules.go
GATE=scripts/verify_pre_deploy.sh
for f in "$CORE" "$CORE_T" "$HTTP" "$MIG" "$MIGS" "$MIGP" "$MY" "$ADM" "$TPL" "$TPLA" "$ROUTES" "$I18N" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B440: core=%s http=%s mig=%s\n' "$CORE" "$HTTP" "$MIG"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b440.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

count() { local n; n="$(grep -c -- "$2" "$1" 2>/dev/null || true)"; printf '%s' "${n:-0}"; }
has()   { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the request is a row, and the policy is a pure precedence"
GOFMT_OUT="$(gofmt -l "$CORE" "$CORE_T" "$HTTP" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the new files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
# The ORDER is the contract: a user row overrides the global row, and with neither the
# answer is `require_approval` — an install where nobody said otherwise must never apply a
# request silently.
if awk '/func ResolveRelayPolicy/,/^}/' "$CORE" | grep -q 'user'; then
  ok "A2: ResolveRelayPolicy consults the per-user row first"
else
  bad "A2: ResolveRelayPolicy ignores the per-user scope"
fi
if has "$CORE" 'return RelayPolicyRequireApproval'; then
  ok "A3: the default mode is require_approval"
else
  bad "A3: the default mode is not require_approval — a request would apply itself"
fi
if has "$CORE" 'ErrRelayRequestDenied'; then
  ok "A4: a denied policy is a named error, not a silent no-op"
else
  bad "A4: deny is not distinguishable from a successful request"
fi
# Part 1's own test suite: precedence, the queue, auto-approve pinning, deny leaving NO
# row, the three shape checks, approve/reject, a failed pin keeping the row pending, and
# an unknown mode being refused.
if ! command -v go >/dev/null 2>&1; then
  skip "A5: no go toolchain on PATH — cannot run the TestB440 suite here (CI is authoritative)"
elif go test ./internal/feature/exit_rules -run 'TestB440' -count=1 >"$WORK/t.log" 2>&1; then
  ok "A5: TestB440 suite green (precedence, queue, deny writes nothing, failed pin stays pending)"
else
  bad "A5: TestB440 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if has "$CORE_T" 'TestB440_DenyRefusesWithoutARow'; then
  ok "A6: «deny refuses WITHOUT a row» is pinned by name"
else
  bad "A6: the deny path is not pinned — a refused request could still queue up"
fi

hdr "B. the schema lands in BOTH chains (a rule of the project, not a preference)"
for f in "$MIGS" "$MIGP"; do
  n="$(count "$f" 'migrations_v0_82_relay_requests.go')"
  if [ "${n:-0}" -ge 1 ]; then
    ok "B1: $f registers the V082 migration"
  else
    bad "B1: $f does not register V082 — one backend would be missing the tables"
  fi
done
for t in exit_relay_requests exit_relay_policies; do
  n="$(count "$MIG" "$t")"
  if [ "${n:-0}" -ge 2 ]; then
    ok "B2: $t is created in both dialects (SQLite + PostgreSQL)"
  else
    bad "B2: $t appears ${n:-0} time(s) — the two chains disagree"
  fi
done
# The SQLite chain goes through the single chokepoint, and each statement is a SEPARATE
# element: a multi-statement string silently executed only its FIRST statement once
# already (the failure mode that cost a CI round in this very block).
if has "$MIG" 'execSQLiteDDL' && has "$MIG" 'relayRequestsSQLiteStmt4,'; then
  ok "B3: the SQLite side uses execSQLiteDDL with one statement per element"
else
  bad "B3: the SQLite DDL bypasses the chokepoint or packs several statements into one"
fi

hdr "C. three actions are reachable — and only from where they belong"
for r in 'POST /my/exit-rules/relay-request' 'POST /admin/exit-rules/relay-request' 'POST /admin/exit-rules/relay-policy'; do
  if has "$ROUTES" "$r"; then
    ok "C1: route registered: $r"
  else
    bad "C1: route missing: $r"
  fi
done
if has "$HTTP" 'func (s *Service) PostMyRelayRequest'; then
  ok "C2: the user handler exists"
else
  bad "C2: no user handler"
fi
# The admin handlers must re-check the role themselves (defense in depth, the same pattern
# PostAdminExitRule uses): a route-level guard alone has already been bypassed once.
n_guard="$(count "$HTTP" 'if c == nil || !c.IsAdmin')"
if [ "${n_guard:-0}" -ge 2 ]; then
  ok "C3: both admin actions re-check IsAdmin"
else
  bad "C3: an admin action trusts the route guard (found ${n_guard:-0} role checks)"
fi
if has "$HTTP" 'WHERE id = ? AND user_id = ? AND enabled = 1'; then
  ok "C4: a user can only ask about THEIR enabled rule"
else
  bad "C4: the user handler does not scope the rule lookup to the caller"
fi

hdr "D. an approval is the operator's own pin — one apply path, not a second one"
if has "$HTTP" 'prefixowner.SetManual'; then
  ok "D1: the pin goes through prefixowner.SetManual (the assignment table)"
else
  bad "D1: the approval does not pin the prefix owner"
fi
if has "$HTTP" 's.ReapplyACLIfDrifted'; then
  ok "D2: the ACL is re-applied through the shared throttled path"
else
  bad "D2: the approval does not re-apply the ACL — the pin would not reach the data plane"
fi
if has "$HTTP" 'prefixowner.TagsByHost'; then
  ok "D3: a relay without a per-node tag is refused instead of pinned blindly (B284)"
else
  bad "D3: nothing checks that the target relay carries a tag"
fi
# The pin stores a HOSTNAME (prefixowner.OwnerByPrefix/ViaForPrefix convert it); a tag
# written here would silently stop matching node_owner_map.
D_PIN="$(printf '%s' "$(grep -n 'prefixowner.SetManual' "$HTTP" 2>/dev/null)")"
case "$D_PIN" in
  *', host)'*) ok "D4: the pin stores the normalised hostname (ViaForPrefix converts it to the tag)" ;;
  *)           bad "D4: the pin does not store the hostname — the assignment table would hold a tag" ;;
esac

hdr "E. the operator is actually told — panel AND Telegram"
if has "$HTTP" 'db.InsertNotification'; then
  ok "E1: the panel notification row is written"
else
  bad "E1: nothing is written to the panel notifications"
fi
if has "$HTTP" 'SELECT id FROM portal_users WHERE is_admin = 1'; then
  ok "E2: every admin gets the row, delegated admins included (B264)"
else
  bad "E2: the notification is not addressed to the admin set"
fi
if has "$HTTP" 's.Notifier.SendAlert'; then
  ok "E3: the same ask reaches the Telegram admin channel"
else
  bad "E3: no Telegram alert — «оповещать в уведомлениях и телеграмме» is half done"
fi
if has "$HTTP" 'NotifyUser'; then
  ok "E4: the asking user is notified about the decision"
else
  bad "E4: the user is never told what happened to their request"
fi

hdr "F. both pages carry the channel, in both languages"
if has "$TPL" 'action="/my/exit-rules/relay-request"'; then
  ok "F1: /my/exit-rules has the request form"
else
  bad "F1: the user has no form to ask with"
fi
if has "$TPL" 'eq $rc.PolicyMode "deny"'; then
  ok "F2: the user form is replaced by the reason when the policy forbids asking"
else
  bad "F2: the policy mode is not reflected on the page"
fi
if has "$TPLA" 'action="/admin/exit-rules/relay-request"' && has "$TPLA" 'action="/admin/exit-rules/relay-policy"'; then
  ok "F3: the admin page has the queue AND the policy controls"
else
  bad "F3: the admin page is missing the queue or the policy form"
fi
if has "$TPLA" 'AffectedLabel'; then
  ok "F4: the queue names the OTHER users the move would affect"
else
  bad "F4: the queue hides the cross-user blast radius of an approval"
fi
for k in exit_rules.relay_request_title exit_rules.relay_request_submit \
         exit_rules.relay_request_status_pending exit_rules.relay_request_denied \
         exit_rules_admin.relay_request_title exit_rules_admin.relay_policy_title \
         exit_rules_admin.relay_policy_mode_deny exit_rules_admin.relay_policy_saved; do
  n="$(count "$I18N" "\"$k\"")"
  if [ "${n:-0}" -eq 2 ]; then
    ok "F5: $k is defined twice (RU + EN)"
  else
    bad "F5: $k is defined ${n:-0} time(s) — B325 requires a pair"
  fi
done
if ! command -v go >/dev/null 2>&1; then
  skip "F6: no go toolchain on PATH — cannot prove the cards render here (CI is authoritative)"
elif go test ./internal/handlers -run 'TestB440|TestLoadTemplates|TestExitRules' -count=1 >"$WORK/h.log" 2>&1; then
  ok "F6: both cards render (form, deny branch, queue, policy) and the templates parse"
else
  bad "F6: render tests failed — $(tail -3 "$WORK/h.log" | tr '\n' ' ')"
fi
. scripts/lib/python_audit.sh   # B446: python3 OR python, one resolver for every audit
if [ -z "$(skygate_python)" ]; then
  skip "F7: no python interpreter (python3/python) on PATH — cannot run the markup balance audit here"
elif "$(skygate_python)" scripts/audit_html_balance.py >"$WORK/b.log" 2>&1; then
  ok "F7: markup still balanced (B436)"
else
  bad "F7: markup became imbalanced — $(tail -1 "$WORK/b.log")"
fi
if bash scripts/audit_mobile_pages.sh 29 0 10 >"$WORK/m.log" 2>&1; then
  ok "F8: the new tables are inside .table-wrap and the mobile budgets hold (B435.1)"
else
  bad "F8: the mobile audit regressed — $(tail -3 "$WORK/m.log" | tr '\n' ' ')"
fi

hdr "G. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b440_relay_requests.sh >/dev/null 2>&1; then
  ok "G1: this script is tracked by git"
else
  bad "G1: this script is not tracked by git"
fi
if grep -qF 'check_b440_relay_requests' "$GATE" 2>/dev/null; then
  ok "G2: registered in scripts/verify_pre_deploy.sh"
else
  bad "G2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B440"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "G3: the B440 catalog description contains a backtick (TD-15)" ;;
  "")    bad "G3: the B440 catalog entry was not found" ;;
  *)     ok "G3: the B440 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB440 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
