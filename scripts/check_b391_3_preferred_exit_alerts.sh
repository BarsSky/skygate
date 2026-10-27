#!/usr/bin/env bash
# check_b391_3_preferred_exit_alerts.sh — B391.3 (2026-10-27)
#
# THE LAST EIGHT ENGLISH-ONLY ALERTS, AND WHY THEY NEEDED A SHAPE.
#
# B391 froze the alert-localisation debt at 34; B391.1 took it to 26, B391.2 to 8. The
# eight that remained were the seven preferred-exit reconciler notices (CREATE / UPDATE /
# CLEAR / MIGRATE / AMBIGUOUS / ORPHAN + the stale-preference warning) and the Tailscale
# auto-discovery notice. They are the only alerts built from state that RECURS: the
# reconciler runs every tick and an ORPHAN or AMBIGUOUS preference persists until a human
# resolves it, so the same message can be emitted again and again. An in-memory
# `shouldAlert` window already capped that at once an hour per device+reason, but the
# window dies with the process and says nothing about the transport.
#
# Translating them verbatim would have produced a repeating multi-line Russian wall with an
# SQL snippet buried in the middle. So each one is now a COMPOSED alert (the B391 composer):
#   title   — three families: changed / stale / needs-a-manual-decision;
#   facts   — the five fields every one of them carries (action, device, user, pin, reason)
#             plus the variant's own fact (relay state, candidates, rule count);
#   details — the actionable part (rollback or cleanup SQL, where to fix it);
#   dedup   — `preferred_exit.<action>.<device>[.<pin>]`, one hour, so a persistent state
#             is one incident per window even across a restart.
#
# The builders are PURE, which is what lets the tests assert what the operator will read
# without a database or a ticker — and the ratchet that stood at 34 is now 0, i.e. a new
# raw `SendAlert(fmt.Sprintf(…))` is no longer debt, it is a failure.
#
# CONTRACTS (14)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

BUILDER=internal/feature/exit_rules/preferred_exit_alert_b3913.go
BUILDER_TEST=internal/feature/exit_rules/preferred_exit_alert_b3913_test.go
REC=internal/feature/exit_rules/reconciler.go
RENAME=internal/feature/exit_rules/reconciler_rename.go
DISCOVERY=cmd/skygate/main_helpers.go
CAT=internal/i18n/catalog_bot.go
RATCHET_SH=scripts/check_b391_alert_composer.sh
GATE=scripts/verify_pre_deploy.sh
for f in "$BUILDER" "$BUILDER_TEST" "$REC" "$RENAME" "$DISCOVERY" "$CAT" "$RATCHET_SH" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the builders are pure and named"
if grep -qF 'func BuildPreferredExitAlert(in PreferredExitAlertInput) telegram.Alert' "$BUILDER"; then
  ok "A1: BuildPreferredExitAlert exists and takes the reconciler's own facts"
else
  bad "A1: BuildPreferredExitAlert is missing — the seven notices have no single shape"
fi
if grep -qF 'func BuildDiscoveryAlert(discovered int, tagFilter string) telegram.Alert' "$BUILDER"; then
  ok "A2: BuildDiscoveryAlert exists (the eighth and last site)"
else
  bad "A2: BuildDiscoveryAlert is missing — the discovery notice is still a literal"
fi
if ! grep -q 'time\.Now(' "$BUILDER" && ! grep -q 'db\.' "$BUILDER" && ! grep -q 'func (s \*Service)' "$BUILDER"; then
  ok "A3: the builders touch no clock, no database and no service (pure)"
else
  bad "A3: the builder file reaches for a clock, a database or the service — the message can no longer be tested by reading it"
fi
if grep -qF 'DedupTTL: preferredExitDedupTTL' "$BUILDER" && grep -qF 'DedupKey: preferredExitDedupKey' "$BUILDER"; then
  ok "A4: every preferred-exit alert carries a dedup key and a TTL"
else
  bad "A4: the alerts are not deduped — a persistent ORPHAN/AMBIGUOUS state pages every pass"
fi

hdr "B. the English literals are gone and the call sites use the builders"
for lit in '♻️ preferred-exit reconciled' 'Tailscale auto-discovery found'; do
  if grep -qF "$lit" "$REC" "$RENAME" "$DISCOVERY" 2>/dev/null; then
    bad "B1: the literal '$lit' is still at a call site"
  else
    ok "B1: no call site still builds '$lit'"
  fi
done
n=$(grep -c 'sendPreferredExitAlert(n, PreferredExitAlertInput{' "$REC" "$RENAME" 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
if [ "$n" = "7" ]; then
  ok "B2: all seven reconciler notices go through sendPreferredExitAlert ($n call sites)"
else
  bad "B2: expected 7 sendPreferredExitAlert call sites in the reconciler files, found $n"
fi
if grep -qF 'exit_rules.BuildDiscoveryAlert(discovered, tagFilter)' "$DISCOVERY"; then
  ok "B3: the discovery ticker sends the composed alert (and keeps a plain fallback sink)"
else
  bad "B3: the discovery ticker does not use BuildDiscoveryAlert"
fi

hdr "C. the strings exist in both catalogues"
missing=""
for k in alert.preferred_exit.title alert.preferred_exit.title_stale alert.preferred_exit.title_review \
         alert.preferred_exit.fact_action alert.preferred_exit.fact_device alert.preferred_exit.fact_user \
         alert.preferred_exit.fact_pin alert.preferred_exit.fact_reason alert.preferred_exit.detail_orphan \
         alert.preferred_exit.plain alert.discovery_new_nodes.title; do
  c=$(grep -cF "\"$k\":" "$CAT" 2>/dev/null || true)
  [ "$c" = "2" ] || missing="$missing $k($c)"
done
if [ -z "$missing" ]; then
  ok "C1: the sampled keys are defined in RU and EN"
else
  bad "C1: key(s) missing from one catalogue:$missing"
fi
total=$(grep -cF '"alert.preferred_exit.' "$CAT" 2>/dev/null || true)
if [ $((total % 2)) -eq 0 ] && [ "$total" -ge 40 ]; then
  ok "C2: the alert.preferred_exit.* set is balanced across the two maps ($total lines)"
else
  bad "C2: the alert.preferred_exit.* set looks unbalanced ($total lines) — a key exists in only one language"
fi
# An RU value that is byte-identical to its EN twin is not a translation (the B325.1 hole).
dup=$(grep -F '"alert.preferred_exit.' "$CAT" | sed -E 's/^[^\"]*"([^"]*)": *"(.*)",$/\1\t\2/' | sort | awk -F'\t' '{c[$2]++} END {n=0; for (v in c) if (c[v]>1) n++; print n}')
if [ "${dup:-1}" = "0" ]; then
  ok "C3: no two alert.preferred_exit values are identical (EN did not get copied into RU)"
else
  bad "C3: $dup value(s) are shared between the RU and EN maps — that is not a translation"
fi

hdr "D. the behaviour is tested"
for fn in TestB3913_EveryVariantCarriesTheFiveCoreFacts TestB3913_StaleNamesTheRelayAndTheHuman \
          TestB3913_OrphanAndAmbiguousAreActionable TestB3913_MigrateShowsBothHostnames \
          TestB3913_ComposedTextIsRussianAndEscaped TestB3913_PlainFallbackKeepsTheSameIncident \
          TestB3913_DiscoveryAlertIsComposedAndDeduped; do
  if grep -qF "func $fn(" "$BUILDER_TEST"; then
    ok "D1: $fn"
  else
    bad "D1: $fn is missing from $BUILDER_TEST"
  fi
done
if OUT="$(go test ./internal/feature/exit_rules/ -run B3913 -count=1 2>&1)"; then
  ok "D2: go test -run B3913 passes"
else
  bad "D2: go test -run B3913 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi

hdr "E. the ratchet is paid to zero"
if grep -qE '^RATCHET=0$' "$RATCHET_SH"; then
  ok "E1: RATCHET=0 — the localisation debt is closed, and a new raw alert is a failure"
else
  bad "E1: the B391 ratchet is not 0 — the debt is still counted somewhere"
fi
if grep -q 'run_check "B391.3"' "$GATE"; then
  ok "E2: B391.3 is registered in the catalog"
else
  bad "E2: B391.3 is not registered — this contract would never run"
fi
if grep -q '\*\*B391.3\*\*' AGENTS.md; then
  ok "E3: AGENTS.md block index carries B391.3"
else
  bad "E3: AGENTS.md has no B391.3 entry"
fi
if git ls-files --error-unmatch scripts/check_b391_3_preferred_exit_alerts.sh >/dev/null 2>&1; then
  ok "E4: this script is tracked by git (trap 11)"
else
  bad "E4: this script is not tracked by git"
fi

printf '\n\033[1mB391.3 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
