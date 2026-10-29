#!/usr/bin/env bash
# check_b441_route_truth.sh — B441 (2026-10-29)
#
# «надо разобраться с существующими правилами, корректность инструмента что проверяет
# конечный адрес и собирает необходимую информацию о том какие подсети и адреса
# необходимо также добавить в список на маршрутизацию».
#
# THE DEFECT THIS BLOCK CLOSES, in one sentence: the ASSIGNMENT TABLE decides which relay
# serves a prefix (B275), but the ADVERTISEMENT was still built from the RULES. A prefix the
# table had handed to a relay that no rule names — an operator pin (B277), an auto
# assignment of a relay-less rule, or an approved B440 relay request — was therefore
# announced by NOBODY while the ACL pinned clients to that relay. B397 fixed the
# non-staggered path; the STAGGERED one is what a default install runs, and it kept the
# rule-only set. The second half of the same defect: the pure union helper claimed to keep
# a rule candidate whose table row had not been written yet, but delegated to
# OwnedPrefixes, which drops it — so a fresh install advertised nothing for a pass.
#
# CONTRACTS: 16.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SYNC=internal/feature/exit_rules/sync_routes.go
TESTF=internal/feature/exit_rules/route_truth_b441_test.go
B440H=internal/feature/exit_rules/relay_request_handlers_b440.go
GATE=scripts/verify_pre_deploy.sh
for f in "$SYNC" "$TESTF" "$B440H" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B441: sync=%s tests=%s\n' "$SYNC" "$TESTF"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b441.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the advertise set is the ASSIGNMENT TABLE unioned with the relay's own rules"
GOFMT_OUT="$(gofmt -l "$SYNC" "$TESTF" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
# The union must KEEP a rule candidate the table has no opinion about (the first-pass half)
# and must ADD a prefix the table gives this relay that no rule names (the B275 half).
if has "$SYNC" 'owners[p] == "" || owners[p] == node'; then
  ok "A2: a rule candidate whose table row does not exist yet is still advertised"
else
  bad "A2: the union drops a rule candidate before the table is written — a fresh install advertises nothing"
fi
if has "$SYNC" 'for _, p := range OwnedPrefixesForRelay(node, owners)'; then
  ok "A3: the table-owned prefixes a rule does not name are added to the set"
else
  bad "A3: the union ignores the table's own assignments"
fi

hdr "B. the STAGGERED pass — the one a default install runs — uses that union"
if has "$SYNC" 'mergedOwnedPrefixes(n.name, routeList, owners)'; then
  ok "B1: the staggered loop builds its advertisement from the union"
else
  bad "B1: the staggered loop still advertises the rule-only set (OwnedPrefixes)"
fi
if has "$SYNC" 'relaysToSync(owners, nil)'; then
  ok "B2: a relay the table names but no rule does is still VISITED"
else
  bad "B2: a table-only relay is never visited, so its advertisement is never written"
fi
if has "$SYNC" 'table-owned prefix(es) that no rule of it names'; then
  ok "B3: the pass says how many table-owned prefixes it added, so the operator can see it"
else
  bad "B3: the pass applies table-owned prefixes silently"
fi
# One prefix, one advertising relay (B274). The union must not resurrect a prefix the
# table gave to somebody else.
if has "$SYNC" 'owners[p] != "" && owners[p] != n.name'; then
  ok "B4: the dropped-prefix counter still reports prefixes owned by ANOTHER relay"
else
  bad "B4: the pass no longer reports contested prefixes"
fi

hdr "C. the per-row Re-sync button obeys the table too"
if has "$SYNC" 'prefixowner.OwnerByPrefix(s.dbc())'; then
  ok "C1: SyncAdvertisedRoutesForNode reads the assignment table"
else
  bad "C1: the per-row re-sync still decides ownership from the rules alone"
fi
if has "$SYNC" 'mergedOwnedPrefixes(node, routes, owners)'; then
  ok "C2: the per-row re-sync applies the same union as the full pass"
else
  bad "C2: the per-row re-sync has its own advertise set — the two paths can disagree"
fi

hdr "D. an approved request reaches the DATA plane, not only the ACL"
if has "$B440H" 'if s.SyncRoutes != nil'; then
  ok "D1: an approval pushes a route sync"
else
  bad "D1: an approval moves the ACL pin but never the advertisement — the client is pinned to a relay that is not serving the prefix"
fi
if has "$B440H" 'go func()'; then
  ok "D2: the push does not block the HTTP handler on per-relay SSH applies"
else
  bad "D2: the route sync runs inline in the request handler"
fi

hdr "E. the invariants are pinned by tests"
if ! command -v go >/dev/null 2>&1; then
  skip "E1: no go toolchain on PATH — cannot run the TestB441 suite here (CI is authoritative)"
elif go test ./internal/feature/exit_rules -run 'TestB441' -count=1 >"$WORK/t.log" 2>&1; then
  ok "E1: TestB441 green (table-owned advertised, table-only relay visited, first pass kept)"
else
  bad "E1: TestB441 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if has "$TESTF" 'TestB441_TableOwnedPrefixIsAdvertisedWithoutARule' && has "$TESTF" 'TestB441_FirstPassStillUsesTheRuleCandidate'; then
  ok "E2: both halves of the defect are named tests"
else
  bad "E2: the two halves are not both pinned by name"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "E3: no go toolchain on PATH — cannot run the whole package suite here"
elif go test ./internal/feature/exit_rules -count=1 >"$WORK/p.log" 2>&1; then
  ok "E3: the whole exit_rules suite is green (no B274/B275/B276 regression)"
else
  bad "E3: the package suite failed — $(tail -3 "$WORK/p.log" | tr '\n' ' ')"
fi

hdr "F. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b441_route_truth.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: this script is not tracked by git"
fi
if grep -qF 'check_b441_route_truth' "$GATE" 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B441"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "F3: the B441 catalog description contains a backtick (TD-15)" ;;
  "")    bad "F3: the B441 catalog entry was not found" ;;
  *)     ok "F3: the B441 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB441 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
