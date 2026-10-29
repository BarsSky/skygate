#!/usr/bin/env bash
# check_b444_diagnostics.sh — B444 (2026-10-29)
#
# «собирает необходимую информацию о том какие подсети и адреса необходимо также добавить в
# список на маршрутизацию» — the part that was missing was the ANSWER itself: the panel could
# not say which prefix nobody serves.
#
# Three holes:
#
# 1. NO DIAGNOSTIC AT ALL. The assignment table (B275) decides who serves a prefix and the
#    relays advertise what the table gives them (B441); when the two disagree the destination
#    is a black hole for every client pinned to it, and the only signals were a log line and a
#    system test that sampled THREE domain rules. New `exit_rules.prefix_serving` compares the
#    table with headscale's APPROVED routes per relay and NAMES every unserved prefix, its
#    owner and the reason (relay missing from headscale vs relay not advertising vs no owner).
#    The base exit routes 0.0.0.0/0 and ::/0 are excluded — they "contain" every prefix and
#    counting them would make the diagnostic vacuous.
# 2. NO METRICS. Two gauges are now published at the end of every sync pass
#    (skygate_exit_rules_prefixes_owned / _not_applied), so «сколько адресов не попало в
#    маршрутизацию» is answered continuously instead of on demand.
# 3. THE SAMPLE WAS SILENT (and tiny). The domain diagnostic read `LIMIT 3` and said PASS,
#    so a broken rule outside those three was invisible. It now reads all of them, checks up to
#    a bounded sample before the per-test deadline, and the verdict SAYS «Sampled N of M».
#
# CONTRACTS: 19.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SERV=internal/feature/exit_rules/prefix_serving_b444.go
SERV_T=internal/feature/exit_rules/prefix_serving_b444_test.go
MET=internal/feature/exit_rules/prefix_serving_metrics_b444.go
SYST=internal/feature/admin/system_tests_prefix_serving_b444.go
DOM=internal/feature/admin/system_tests_b394_new.go
SYNC=internal/feature/exit_rules/sync_routes.go
GATE=scripts/verify_pre_deploy.sh
for f in "$SERV" "$SERV_T" "$MET" "$SYST" "$DOM" "$SYNC" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B444: report=%s systest=%s metrics=%s\n' "$SERV" "$SYST" "$MET"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b444.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the live diagnostic names what nobody serves"
GOFMT_OUT="$(gofmt -l "$SERV" "$SERV_T" "$MET" "$SYST" "$DOM" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$SYST" 'exit_rules.prefix_serving'; then
  ok "A2: the system test exists under the exit_rules category"
else
  bad "A2: no prefix-serving diagnostic — the panel still cannot name an unserved prefix"
fi
# The live truth is APPROVED routes; the advertised-but-unapproved set would report prefixes as
# served that no client can reach (the false positive B273 removed from the health ladder).
if has "$SYST" 'n.ApprovedRoutes'; then
  ok "A3: the diagnostic compares the table with APPROVED routes, not with what a relay offered"
else
  bad "A3: the diagnostic would count advertised-but-unapproved prefixes as served"
fi
if has "$SYST" 'ReportServing(owners, advertised)'; then
  ok "A4: both halves come from the shared pure report, so the page and the metric cannot disagree about the rule"
else
  bad "A4: the diagnostic has its own logic instead of the shared report"
fi
# The vacuity guard: 0.0.0.0/0 "contains" everything.
if has "$SERV" 'a.Bits() == 0'; then
  ok "A5: the base exit routes are excluded from covering (otherwise every prefix would look served)"
else
  bad "A5: a 0.0.0.0/0 advertisement would count as serving EVERY prefix — the diagnostic would be vacuous"
fi
for reason in 'not in headscale' 'does not advertise this prefix' 'no owner for this prefix'; do
  if has "$SERV" "$reason"; then
    ok "A6: the report distinguishes the reason «$reason»"
  else
    bad "A6: the report cannot tell the operator why a prefix is unserved («$reason» missing)"
  fi
done

hdr "B. the metric is published every pass"
if has "$SYNC" 'publishPrefixServingMetrics(owners, result)'; then
  ok "B1: the sync pass publishes the numbers"
else
  bad "B1: the gauges are declared but never set — the dashboard would show zeros forever"
fi
if has "$MET" 'skygate_exit_rules_prefixes_owned' && has "$MET" 'skygate_exit_rules_prefixes_not_applied'; then
  ok "B2: both gauge names are the documented ones"
else
  bad "B2: the gauge names are missing or renamed"
fi
# The metric must describe THIS pass, not the live approved set — those are different questions
# and a gauge that pretends to be the system test is how a dashboard starts lying.
if has "$MET" 'NOT applied by their relay in the last sync pass'; then
  ok "B3: the gauge help says which question it answers"
else
  bad "B3: the gauge help does not say it is about the last pass"
fi

hdr "C. the sample is bounded AND stated"
if has "$DOM" 'b444DomainSampleCap'; then
  ok "C1: the domain diagnostic uses a named sample cap"
else
  bad "C1: the domain diagnostic still has an implicit LIMIT"
fi
if has "$DOM" 'b444SampleNote(sampled, totalRules)'; then
  ok "C2: the verdict says how many of how many rules were checked"
else
  bad "C2: a PASS can still silently describe three of forty rules"
fi
# The old LIMIT must be gone from the query: a bounded loop over all rows is what makes
# "sampled N of M" possible at all.
if has "$DOM" 'GROUP BY device_id, target_value ORDER BY device_id LIMIT 3'; then
  bad "C3: the query still asks the database for three rows"
else
  ok "C3: the query returns every domain rule; the LOOP bounds the work"
fi

hdr "D. the rules are pinned by tests"
if ! command -v go >/dev/null 2>&1; then
  skip "D1: no go toolchain on PATH — cannot run TestB444 here (CI is authoritative)"
elif go test ./internal/feature/exit_rules -run 'TestB444' -count=1 >"$WORK/t.log" 2>&1; then
  ok "D1: TestB444 green (covering, unserved, missing relay, unowned, metric)"
else
  bad "D1: TestB444 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "D2: no go toolchain on PATH — cannot run the package suites here"
elif go test ./internal/feature/exit_rules ./internal/feature/admin ./internal/metrics -count=1 >"$WORK/p.log" 2>&1; then
  ok "D2: the three touched packages are green"
else
  bad "D2: a package suite failed — $(tail -3 "$WORK/p.log" | tr '\n' ' ')"
fi

hdr "E. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b444_diagnostics.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: this script is not tracked by git"
fi
if grep -qF 'check_b444_diagnostics' "$GATE" 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B444"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "E3: the B444 catalog description contains a backtick (TD-15)" ;;
  "")    bad "E3: the B444 catalog entry was not found" ;;
  *)     ok "E3: the B444 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB444 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
