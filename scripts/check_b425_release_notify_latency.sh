#!/usr/bin/env bash
# check_b425_release_notify_latency.sh — B425 (2026-10-26)
#
# «УВЕДОМЛЕНИЕ ОБ ОБНОВЛЕНИИ ПРИШЛО С ЗАДЕРЖКОЙ ОКОЛО 4 ЧАСОВ».
#
# Two causes stacked, both measured on the reference install:
#
#  1. the release monitor's FIRST check waited a full CheckEvery interval (1 h) "so a
#     restart does not spam admins" — but every update restarts the container, so the
#     channel went quiet for an hour exactly when the operator was watching, and a restart
#     loop could postpone a notification indefinitely;
#  2. the poll could be answered with 403 (the /admin/update page was spending the same
#     anonymous 60/h quota, fixed in B424) and THIS file was the one place that treated a
#     rate limit as silence: `if err != ErrRateLimited { log… }` — no log line at all.
#
# Journal evidence from the install: alerts at 23:06 and 05:06 (an hourly grid), releases
# published 22:50 and 04:39 — i.e. the monitor was fine when the quota was free and mute
# when it was not.
#
# CONTRACTS (16)
#   A  the first check is prompt after boot, not one interval later
#   B  an announced tag survives a restart (so the prompt check cannot become a repeat)
#   C  a rate-limited poll is no longer silence
#   D  behaviour + registration

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
. "$(dirname "$0")/lib/gosurface.sh"
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

RUN=internal/release/monitor_runner.go
MON=internal/release/monitor.go
SKY_MAIN=""
gosurface SKY_MAIN cmd/skygate/*.go 2>/dev/null || true
GATE=scripts/verify_pre_deploy.sh
for f in "$RUN" "$MON" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the first check is prompt after boot"
grep -qF 'FirstDelay time.Duration' "$RUN" && ok "A1: the first-check delay is a field (tests can drive it)" || bad "A1: no FirstDelay field"
grep -qF 'const DefaultFirstDelay = 2 * time.Minute' "$RUN" && ok "A2: the default is two minutes, not an hour" || bad "A2: no explicit default (or it is not 2 minutes)"
if awk '/^func \(m \*Monitor\) Start\(/,/^}/' "$RUN" | grep -qF 'NewTimer(delay)'; then
  ok "A3: Start fires a separate first timer before the regular ticker"
else
  bad "A3: Start still waits for the first ticker beat (the whole interval)"
fi
if awk '/^func \(m \*Monitor\) Start\(/,/^}/' "$RUN" | grep -qF 'case <-first.C:'; then
  ok "A4: the first timer actually runs a tick"
else
  bad "A4: the first timer does not call tick"
fi

hdr "B. an announced tag survives a restart"
grep -qF 'const notifiedTagKey = "release.notified_tag"' "$RUN" && ok "B1: the persisted key has one definition" || bad "B1: no persisted key"
grep -qF 'func (m *Monitor) notifiedTag()' "$RUN" && ok "B2: it is read back" || bad "B2: never read"
if awk '/^func \(m \*Monitor\) tick\(/,/^}/' "$RUN" | grep -qF 'if m.notifiedTag() == r.TagName {'; then
  ok "B3: the persisted tag is consulted BEFORE the alert (a restart stays quiet)"
else
  bad "B3: the persisted tag is not consulted before alerting"
fi
if awk '/^func \(m \*Monitor\) tick\(/,/^}/' "$RUN" | grep -qF 'm.persistNotifiedTag(r.TagName)'; then
  ok "B4: the announced tag is persisted right after the send"
else
  bad "B4: nothing is persisted after an alert"
fi
if awk '/^func \(m \*Monitor\) ResetNotified\(/,/^}/' "$RUN" | grep -qF 'm.persistNotifiedTag("")'; then
  ok "B5: ResetNotified clears the persisted tag too (an upgrade must not leave it stale)"
else
  bad "B5: ResetNotified leaves the persisted tag behind"
fi
grep -qF 'if m.Notified == nil {' "$RUN" && ok "B6: the dedup map is nil-safe (a Monitor built without Start cannot panic)" || bad "B6: nil map panics are possible"

hdr "C. a rate-limited poll is no longer silence"
grep -qF 'errors.Is(err, ErrRateLimited)' "$RUN" && ok "C1: the rate-limit branch wraps the sentinel (errors.Is keeps working)" || bad "C1: rate limit is compared by identity again"
if awk '/^func \(m \*Monitor\) tick\(/,/^}/' "$RUN" | grep -qF 'SKYGATE_GITHUB_TOKEN'; then
  ok "C2: the rate-limit line names the way out (a token raises 60/h to 5000/h)"
else
  bad "C2: a rate-limited poll still explains nothing"
fi
grep -qF 'X-RateLimit-Reset' "$MON" && ok "C3: the reset hint from GitHub is carried into the error" || bad "C3: the reset hint is dropped"

hdr "D. behaviour + registration"
if OUT="$(go test ./internal/release/ -run B425 -count=1 2>&1)"; then
  ok "D1: first-check timing, restart dedup and the rate-limit log all pass"
else
  bad "D1: go test ./internal/release -run B425: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
grep -qE 'DB:[[:space:]]+d\.DB,' "$SKY_MAIN" 2>/dev/null && ok "D2: the running binary passes the database (persistence is wired, not theoretical)" || bad "D2: main.go does not pass the DB"
grep -q 'run_check "B425"' "$GATE" && ok "D3: registered in the catalog" || bad "D3: not registered"
grep -q '\*\*B425\*\*' AGENTS.md && ok "D4: AGENTS.md block index carries B425" || bad "D4: no AGENTS entry"
git ls-files --error-unmatch scripts/check_b425_release_notify_latency.sh >/dev/null 2>&1 && ok "D5: this script is tracked by git (trap 11)" || bad "D5: not tracked"

printf '\n\033[1mB425 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
