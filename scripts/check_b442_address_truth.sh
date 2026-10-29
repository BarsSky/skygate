#!/usr/bin/env bash
# check_b442_address_truth.sh — B442 (2026-10-29)
#
# «корректность инструмента что проверяет конечный адрес и собирает необходимую информацию о
# том какие подсети и адреса необходимо также добавить в список на маршрутизацию».
#
# Three things the resolver said that were not true:
#
#   1. An AAAA answer was DISCARDED by a `strings.Contains(addr, ":")` continue. A domain
#      that answers only over IPv6 therefore resolved to an EMPTY set, every row of it was
#      deleted, and nothing said why. It is now SPLIT OUT and reported.
#   2. One DNS answer was treated as the whole truth, so a resolver returning a PARTIAL set
#      deleted live /32 rows — and with them the ACL grant and the advertised route, until
#      the next tick re-added them. An address is dropped only when a SECOND lookup also
#      does not answer it, and a FAILED second lookup removes nothing (no data is not a
#      negative answer).
#   3. A `deny` rule claimed a prefix exactly like an accept rule, so it could decide WHO
#      SERVES that prefix — while grants[] cannot express deny at all (B265). A refused
#      destination could move a prefix onto a relay and steal it from an accept rule.
#
# CONTRACTS: 14.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

HELP=internal/feature/exit_rules/address_truth_b442.go
HELPT=internal/feature/exit_rules/address_truth_b442_test.go
SYNC=internal/feature/exit_rules/sync_domain.go
ROUTES=internal/feature/exit_rules/sync_routes.go
POWN=internal/prefixowner/prefixowner.go
GATE=scripts/verify_pre_deploy.sh
for f in "$HELP" "$HELPT" "$SYNC" "$ROUTES" "$POWN" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B442: helper=%s resolver=%s\n' "$HELP" "$SYNC"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b442.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. IPv6 answers are SPLIT OUT, never silently swallowed"
GOFMT_OUT="$(gofmt -l "$HELP" "$HELPT" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the new files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$SYNC" 'SplitAnswers(addrs)'; then
  ok "A2: the resolver separates A from AAAA instead of skipping them"
else
  bad "A2: the resolver still drops AAAA answers (an IPv6-only domain resolves to nothing, and rows are deleted)"
fi
# The bare `continue // skip IPv6` is the defect itself; it must be gone.
if has "$SYNC" 'skip IPv6'; then
  bad "A3: the skip-IPv6 branch is still present"
else
  ok "A3: no silent skip-IPv6 branch remains"
fi
if has "$SYNC" 'exit routing for IPv6 is not implemented'; then
  ok "A4: an IPv6-only destination is REPORTED (log + audit row) instead of resolving to nothing"
else
  bad "A4: nothing tells the operator that a destination is IPv6-only"
fi

hdr "B. one partial answer does not delete a live row"
if has "$SYNC" 'ConfirmDeletions(missing, currentIPs, second, secondAnswered)'; then
  ok "B1: pruning is decided by two answers, not one"
else
  bad "B1: the resolver still deletes rows from a single answer"
fi
if has "$HELP" 'secondAnswered bool'; then
  ok "B2: a FAILED second lookup is its own case (no data is not a negative answer)"
else
  bad "B2: the helper cannot distinguish a failed lookup from an empty answer"
fi
if has "$SYNC" 'net.LookupHost(d.domain); lerr == nil'; then
  ok "B3: the second lookup is taken from the same name, right before the prune"
else
  bad "B3: no confirming lookup is performed"
fi

hdr "C. a deny rule does not vote on who serves a prefix"
for f in "$ROUTES" "$POWN"; do
  if has "$f" "COALESCE(action,'accept') <> 'deny'"; then
    ok "C1: $f excludes deny rules from the claim set"
  else
    bad "C1: $f still lets a deny rule claim a prefix"
  fi
done
if has "$POWN" "COALESCE(r.action,'accept') <> 'deny'"; then
  ok "C2: the device-preference claim query excludes deny too"
else
  bad "C2: the B398 preference query still counts deny rows"
fi
if has "$HELP" 'DenyClaimsPrefix'; then
  ok "C3: the rule is a named predicate, not an inline string compare to maintain in five places"
else
  bad "C3: no named predicate for «may this rule claim its prefix»"
fi

hdr "D. the rules are pinned by tests"
if ! command -v go >/dev/null 2>&1; then
  skip "D1: no go toolchain on PATH — cannot run TestB442 here (CI is authoritative)"
elif go test ./internal/feature/exit_rules -run 'TestB442' -count=1 >"$WORK/t.log" 2>&1; then
  ok "D1: TestB442 green (split, partial answer, failed lookup, deny claims)"
else
  bad "D1: TestB442 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "D2: no go toolchain on PATH — cannot run the package suites here"
elif go test ./internal/feature/exit_rules ./internal/prefixowner -count=1 >"$WORK/p.log" 2>&1; then
  ok "D2: both touched packages are green (owner assignment unaffected)"
else
  bad "D2: a package suite failed — $(tail -3 "$WORK/p.log" | tr '\n' ' ')"
fi

hdr "E. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b442_address_truth.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: this script is not tracked by git"
fi
if grep -qF 'check_b442_address_truth' "$GATE" 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B442"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "E3: the B442 catalog description contains a backtick (TD-15)" ;;
  "")    bad "E3: the B442 catalog entry was not found" ;;
  *)     ok "E3: the B442 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB442 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
