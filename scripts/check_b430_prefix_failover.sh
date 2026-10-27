#!/usr/bin/env bash
# check_b430_prefix_failover.sh — B430 (2026-10-27)
#
# «КОГДА УЗЕЛ ПАДАЕТ, ПРЕФИКСЫ ПЕРЕЕЗЖАЮТ МОЛЧА».
#
# The engine handles a dead relay correctly: every prefix that relay owned is
# reassigned to a healthy one and the per-CIDR `via=` pins are rewritten, so devices
# keep reaching those destinations — through another relay. What the operator saw was
# devices that suddenly could not reach those destinations, with no alert, no audit
# row, and /admin/exit-nodes showing the new owner as if it had always been that way.
# The only trace was one log line, which is not a signal anybody reads.
#
# B430 turns that pass into ONE short composed notification through the same alert
# path as every other operator signal: what moved, from which relay, to which relay,
# and how many prefixes are affected — deduplicated on the ROUTE (a flapping relay
# moves the same prefixes back and forth and must not page once per prefix).
#
# What must NOT page: an engine rebalance between two HEALTHY relays (bookkeeping),
# a first assignment of a prefix (a rebuilt table / a restore), and a pass where the
# healthy set is EMPTY (headscale unreachable is a missing measurement, not an
# outage — the engine then falls back to the relays the rules named).
#
# CONTRACTS (15)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
. "$(dirname "$0")/lib/gosurface.sh"
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

PURE=internal/prefixowner/prefixowner.go
PURE_TEST=internal/prefixowner/failover_b430_test.go
ALERT=internal/feature/exit_rules/prefix_failover_b430.go
ALERT_TEST=internal/feature/exit_rules/prefix_failover_b430_test.go
SYNC=internal/feature/exit_rules/sync_acl.go
CAT=internal/i18n/catalog_bot.go
GATE=scripts/verify_pre_deploy.sh
for f in "$PURE" "$PURE_TEST" "$ALERT" "$ALERT_TEST" "$SYNC" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the decision is pure and names its evidence"
if grep -qF 'func DescribeFailover(prev, next map[string]string, healthy []string) []PrefixMove' "$PURE"; then
  ok "A1: DescribeFailover takes previous owners, new owners and the healthy set — no clock, no IO"
else
  bad "A1: DescribeFailover does not have the pure signature"
fi
if grep -qF 'if len(healthySet) == 0 {' "$PURE" && grep -qF 'not evidence of a failure' "$PURE"; then
  ok "A2: an EMPTY healthy set is treated as 'headscale could not tell us', never as 'everything is down'"
else
  bad "A2: an empty healthy set would be reported as an outage"
fi
if grep -qF 'if healthySet[from] {' "$PURE"; then
  ok "A3: a move between two HEALTHY relays is not a failover (bookkeeping must not page anyone)"
else
  bad "A3: a healthy rebalance would be reported as a failover"
fi

hdr "B. behaviour: what is news and what is not"
if OUT="$(go test ./internal/prefixowner/ -run 'TestB430' -count=1 2>&1)"; then
  ok "B1: only a prefix that LEFT an unhealthy relay is reported; an empty healthy set and a first assignment are silent"
else
  bad "B1: prefixowner B430 tests failed: $(printf '%s' "$OUT" | grep -E '^(---|FAIL)' | head -3 | tr '\n' ' ')"
fi

hdr "C. the operator gets ONE readable message"
if grep -qF 'const PrefixFailoverTitleKey = "alert.prefix_failover.title"' "$ALERT"; then
  ok "C1: the title comes from the catalogue (RU+EN), not a hardcoded English sentence"
else
  bad "C1: no catalogue title key"
fi
if grep -qF 'DedupKey: key' "$ALERT" && grep -qF '"prefix.failover"' "$ALERT" && grep -qF 'DedupTTL: 30 * time.Minute' "$ALERT"; then
  ok "C2: the dedup identity is the ROUTE (from>to) with a 30-minute window — a flapping relay pages once per direction, not per prefix"
else
  bad "C2: the alert is not deduplicated on the route"
fi
if grep -qF 'maxFailoverPrefixesListed' "$ALERT" && grep -qF '… +%d' "$ALERT"; then
  ok "C3: the excerpt is bounded AND says how many prefixes it dropped (a dead relay owns dozens)"
else
  bad "C3: the excerpt is unbounded or hides what it dropped"
fi
if grep -qF 'SendAlertEvent(alert)' "$ALERT" && grep -qF 's.Notifier.SendAlert(' "$ALERT"; then
  ok "C4: the composed path is preferred and a SendAlert-only sink still gets a sentence (nothing is lost)"
else
  bad "C4: the alert has no fallback for a plain sink"
fi
if grep -qF 'best-effort' "$ALERT" || grep -qF 'Best-effort' "$ALERT"; then
  ok "C5: delivery is best-effort — the reconcile pass must never fail because a message could not be sent"
else
  bad "C5: the notification is not documented as best-effort"
fi
pairs=0
for k in alert.prefix_failover.title alert.prefix_failover.fact_moved; do
  c=$(grep -c "\"$k\"" "$CAT"); [ "$c" -ge 2 ] || bad "C6: $k missing in one language"; [ "$c" -ge 2 ] && pairs=$((pairs+1))
done
[ "$pairs" -eq 2 ] && ok "C6: both new keys exist in RU and EN"

hdr "D. the reconcile pass produces it"
if grep -qF 'before := prefixowner.OwnerByPrefix(s.dbc())' "$SYNC" && grep -qF 'prefixowner.DescribeFailover(before, prefixowner.OwnerByPrefix(s.dbc()), healthyExitRelaysForAssignment(s.dbc()))' "$SYNC"; then
  ok "D1: the table is snapshotted BEFORE the pass and compared after it (that comparison is the only place the reason exists)"
else
  bad "D1: the reconcile pass does not compare before/after owners"
fi
if grep -qF 's.notifyPrefixFailover(moves)' "$SYNC"; then
  ok "D2: a forced move reaches the operator — the pass is no longer silent"
else
  bad "D2: the pass still swallows the move"
fi
if OUT="$(go test ./internal/feature/exit_rules/ -run 'TestB430' -count=1 2>&1)"; then
  ok "D3: the composed shape is pinned behaviourally (translated label, warn severity, bounded excerpt, route dedup, per-route facts)"
else
  bad "D3: exit_rules B430 tests failed: $(printf '%s' "$OUT" | grep -E '^(---|FAIL)' | head -3 | tr '\n' ' ')"
fi

hdr "E. registration / index / tracked"
grep -q 'run_check "B430"' "$GATE" && ok "E1: registered in the catalog" || bad "E1: not registered"
grep -q '\*\*B430\*\*' AGENTS.md && ok "E2: AGENTS.md block index carries B430" || bad "E2: no AGENTS entry"
if git ls-files --error-unmatch scripts/check_b430_prefix_failover.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap 11)"
else
  bad "E3: this script is not tracked by git yet — a check nobody can run is not a check"
fi

printf '\n\033[1mB430 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
