#!/usr/bin/env bash
# check_b424_update_check_quota.sh — B424 (2026-10-25)
#
# «НЕ ОТРАБАТЫВАЕТ ОБНОВЛЕНИЕ ПО РАСПИСАНИЮ» И «ПРОПАЛ ПОСЛЕДНИЙ РЕЛИЗ, ВМЕСТО НЕГО ПРОЧЕРК» —
# ОДИН ДЕФЕКТ, А НЕ ДВА.
#
# Measured on the reference install 2026-10-25:
#
#   update_schedule_enabled=1, update_schedule_time=08:00
#   update_schedule_last_run=2026-10-13T08:00:04Z        ← 13 days old
#   release-monitor: alert sent for v1.5.169 (running v1.5.167)   ← the monitor SAW it
#   update_page_view audit rows every ~6 seconds          ← an auto-refreshing tab
#   api.github.com/repos/BarsSky/skygate/releases/latest from the container: 200 OK
#
# The page built a NEW update.Checker on EVERY render, so the checker's own caches (6 h
# success / 15 min failure backoff) never applied: one GitHub API call per render, ~600 an
# hour against the 60/hour anonymous limit. The quota died in minutes, GitHub answered 403,
# Check returned a Result with an empty Latest (a dash on the page, error field discarded)
# and the scheduler's slot hit the same failure and returned in total silence.
#
# CONTRACTS (13)
#   A  one checker per process (the caches can actually apply)
#   B  a failed check is VISIBLE: no bare dash, the reason is rendered
#   C  the scheduler names why the slot did not run
#   D  behaviour + registration

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

ADMIN=internal/feature/admin/update.go
TPL=internal/handlers/templates/admin/update.html
SCHED=internal/update/scheduler.go
CAT=internal/i18n/catalog_update.go
GATE=scripts/verify_pre_deploy.sh
for f in "$ADMIN" "$TPL" "$SCHED" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. one GitHub checker per process"
grep -qF 'var updateCheckerCache struct' "$ADMIN" && ok "A1: the checker is cached at process scope" || bad "A1: no process-scoped cache"
grep -qF 'func updateCheckerFor(' "$ADMIN" && ok "A2: one constructor for it" || bad "A2: no updateCheckerFor"
if grep -qF 'updateCheckerFor(s).Check(ctx)' "$ADMIN"; then ok "A3: the page render uses the shared instance (so the 6 h / 15 min caches apply)"; else bad "A3: the render path builds its own checker"; fi
# The file legitimately holds TWO literals: the helper's own, and the one on the APPLY route
# (an explicit operator action, which is allowed to force a fresh check). What must be gone
# is a literal on the RENDER path — hence A3 above — and an unbounded number of them.
n=$(grep -c '&update.Checker{' "$ADMIN"); [ "$n" -eq 2 ] && ok "A5: exactly two checker literals remain: the shared one and the explicit apply route (found $n)" || bad "A5: $n checker literals — expected 2 (shared + apply route)"

hdr "B. a failed check must be visible"
if grep -qF '{{if .Error}}' "$TPL" && grep -qF 'update.check_failed' "$TPL"; then
  ok "B1: the template renders the REASON instead of a bare dash"
else
  bad "B1: the page still shows a dash with no explanation"
fi
grep -qF 'update.check_never' "$TPL" && ok "B2: 'never checked' is its own state (distinguishable from a failure)" || bad "B2: no never-checked state"
pairs=0
for k in update.check_failed update.check_never; do
  c=$(grep -c "\"$k\"" "$CAT"); if [ "$c" -lt 2 ]; then bad "B3: $k missing in one language"; else pairs=$((pairs+1)); fi
done
[ "$pairs" -eq 2 ] && ok "B3: both new keys exist in RU and EN"

hdr "C. the scheduler names why the slot did not run"
grep -qF 'logScheduledNoop(' "$SCHED" && ok "C1: a named no-op log exists" || bad "C1: no no-op logging"
grep -qF 'logScheduledNoopErr(' "$SCHED" && ok "C2: a check FAILURE has its own message (including the quota hint)" || bad "C2: a failed check is still silent"
grep -qF 'no newer release (running' "$SCHED" && ok "C3: 'nothing newer' names both versions" || bad "C3: 'nothing newer' is unnamed"
grep -qF 'SKYGATE_GITHUB_TOKEN' "$SCHED" && ok "C4: the failure message names the fix (a token raises 60/h to 5000/h)" || bad "C4: the failure does not name the way out"

hdr "D. behaviour + registration"
if OUT="$(go test ./internal/feature/admin/ ./internal/update/ -count=1 2>&1)"; then
  ok "D1: the admin and update packages are green (the shared checker does not break the page or the schedule)"
else
  bad "D1: go test failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
grep -q 'run_check "B424"' "$GATE" && ok "D2: registered in the catalog" || bad "D2: not registered"
grep -q '\*\*B424\*\*' AGENTS.md && ok "D3: AGENTS.md block index carries B424" || bad "D3: no AGENTS entry"

printf '\n\033[1mB424 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
