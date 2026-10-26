#!/usr/bin/env bash
# check_b423_threshold_alerts.sh — B423 (2026-10-25)
#
# THE TWO CHRONIC CONDITIONS THE INVESTIGATION FOUND NEVER REACHED TELEGRAM.
#
# The 2026-10-25 investigation of the reference install counted 13 310
# `cluster.discovery.error` audit rows and 1802 `login_fail` rows (plus 318
# `password_change_fail`). Both are "a persistent condition, not an event", and the product
# had no vocabulary for that:
#
#   - the discovery ticker throttles its log/audit to once an hour (B318/B354) and never
#     alerts, so a tailnet broken for weeks was invisible unless somebody read the journal;
#   - a failed login is audited per attempt and never aggregated, so 1802 failures looked
#     like nothing at all from the outside.
#
# B423 adds two guards (internal/watchalert) with PURE decisions:
#   discovery — alert on the ONSET (first failure after a success), escalate at most once per
#               window (default 6 h) while it keeps failing, and announce RECOVERY;
#   login     — count failures in a sliding window (default 10 min), alert ONCE per cooldown
#               (default 1 h) when the count crosses the threshold (default 10), carrying the
#               count and the distinct sources — never a password, never a username.
#
# CONTRACTS (20)
#   A  the discovery escalation, its onset/window/recovery shape and its wiring
#   B  the login burst, the sliding window, the cooldown and the no-secret rule
#   C  the alerts are composed (i18n title + facts), not raw strings, and go through the ledger
#   D  behaviour (the policy tables, the states, the sliding window)
#   E  registered, indexed, tracked, documented

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b423.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

WA=internal/watchalert/watchalert.go
HELPERS=cmd/skygate/main_helpers.go
AUTH=internal/feature/auth/service.go
SCHED=cmd/skygate/main_schedulers.go
CAT=internal/i18n/catalog_bot.go
GATE=scripts/verify_pre_deploy.sh
for f in "$WA" "$HELPERS" "$AUTH" "$SCHED" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. discovery: onset, escalation window, recovery"
grep -qF 'func discoveryAlertDue(' "$WA" && ok "A1: the escalation policy is a pure function" || bad "A1: no pure policy"
grep -qF 'first failure after a success' "$WA" && ok "A2: the ONSET is news (first failure after a success alerts immediately)" || bad "A2: the onset does not alert"
grep -qF 'within the escalation window' "$WA" && ok "A3: a continuing failure is rate-limited to the window (the phone is not the journal)" || bad "A3: no escalation window"
grep -qF 'alert.discovery.recovered' "$WA" && ok "A4: recovery is announced (a fault that heals silently teaches nothing)" || bad "A4: no recovery alert"
grep -qF 'watchalert.DiscoveryFailure(' "$HELPERS" && grep -qF 'watchalert.DiscoverySuccess(' "$HELPERS" && ok "A5: both are called from the discovery ticker" || bad "A5: the ticker does not use the guard"
grep -qF 'SKYGATE_DISCOVERY_ALERT_HOURS' "$SCHED" && ok "A6: the escalation window is configurable with a named env var" || bad "A6: the window is not configurable"

hdr "B. login bursts"
grep -qF 'func loginBurstDue(' "$WA" && ok "B1: the burst policy is a pure function" || bad "B1: no pure policy"
grep -qF 'watchalert.LoginFailure(' "$AUTH" && ok "B2: the login handler reports every failed attempt (same path as the audit row)" || bad "B2: failed logins are not counted"
grep -qF 'lg.hits = lg.hits[i:]' "$WA" && ok "B3: the window SLIDES (a slow trickle cannot accumulate into a fake burst)" || bad "B3: the window does not slide"
grep -qF 'strings.LastIndex(addr, ":")' "$WA" && ok "B4: the source port is stripped, so one attacker is ONE source" || bad "B4: the port would turn one source into many"
# CODE-ONLY: this file's own comment explains that a password is never reported, and a
# contract that cannot tell prose from code would forbid documenting the rule.
if awk '{ t=$0; sub(/^[ \t]+/, "", t) } t ~ /^\/\// { next } { print }' "$WA" | grep -qiE 'password|passwd|secret'; then
  bad "B5: the guard handles a credential — an alert must never carry one"
else
  ok "B5: no credential is handled or reported"
fi

hdr "C. alerts are composed and go through the ledger"
grep -qF 'SendAlertEvent(telegram.Alert{' "$WA" && ok "C1: both guards use the composed alert path (not a raw reply)" || bad "C1: the guards bypass the composer"
grep -qF 'TitleKey: "alert.discovery.title"' "$WA" && ok "C2: the discovery alert has an i18n title" || bad "C2: no i18n title for discovery"
grep -qF 'TitleKey: "alert.login_burst.title"' "$WA" && ok "C3: the login alert has an i18n title" || bad "C3: no i18n title for the login burst"
pairs=0
for k in alert.discovery.title alert.discovery.recovered alert.discovery.fact_error alert.discovery.fact_since \
         alert.discovery.fact_why alert.login_burst.title alert.login_burst.fact_count \
         alert.login_burst.fact_window alert.login_burst.fact_ips; do
  n=$(grep -c "\"$k\"" "$CAT")
  if [ "$n" -lt 2 ]; then bad "C4: $k is missing from one language ($n of 2)"; else pairs=$((pairs+1)); fi
done
[ "$pairs" -eq 9 ] && ok "C4: all 9 keys exist in RU and EN"
grep -qF 'DedupKey: "watchalert.discovery"' "$WA" && ok "C5: a dedup key bounds the discovery alert independently of the policy" || bad "C5: no dedup key"

hdr "D. behaviour"
if OUT="$(go test ./internal/watchalert/ -run B423 -count=1 2>&1)"; then
  ok "D1: onset → window → recovery, the burst threshold/cooldown and the sliding window all pass"
else
  bad "D1: watchalert B423 tests failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
grep -qF 'TestB423_TheWindowSlides' internal/watchalert/watchalert_b423_test.go && ok "D2: the sliding window is pinned by a test" || bad "D2: nothing proves the window slides"
grep -qF 'TestB423_DiscoveryOnsetThenEscalationThenRecovery' internal/watchalert/watchalert_b423_test.go && ok "D3: the whole discovery lifecycle is pinned" || bad "D3: the lifecycle is not pinned"

hdr "E. registration"
grep -q 'run_check "B423"' "$GATE" && ok "E1: registered in the catalog" || bad "E1: not registered"
grep -q '\*\*B423\*\*' AGENTS.md && ok "E2: AGENTS.md block index carries B423" || bad "E2: no AGENTS entry"
git ls-files --error-unmatch scripts/check_b423_threshold_alerts.sh >/dev/null 2>&1 && ok "E3: this script is tracked by git (trap 11)" || bad "E3: not tracked"
grep -q 'B423' docs/TELEGRAM.md && ok "E4: the runbook documents the two chronic conditions" || bad "E4: no TELEGRAM.md section"

printf '\n\033[1mB423 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
