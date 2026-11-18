#!/usr/bin/env bash
# B493 (TD-43) — the schedule is expressed in the PANEL's timezone, and its clock boundary is the
# panel day.
#
# The operator's report (2026-11-18): «я ставил расписание на 8 утра по МСК и также вводил учёт по
# таймзоне, где расположен хост». Measured on the production host: the SYSTEM clock is Etc/UTC while
# the PANEL's `global_settings.timezone` is Europe/Moscow, and the journal's 05:08 UTC skip lines were
# 08:08 MSK — the slot WAS being evaluated in Moscow time. That is a fact worth pinning: "the schedule
# is an hour off" is invisible until an update lands at the wrong hour, and the two plausible
# implementations (host zone vs panel zone) differ by three hours on this very host.
#
# Contracts:
#   A. the scheduler takes its "now" and its last-run comparison from the localtime package, neve
#      from time.Now/time.Local
#   B. a test proves 08:00 MSK == 05:00 UTC is DUE and 08:00 UTC == 11:00 MSK is NOT
#   C. a test proves the "already ran today" guard is answered in the PANEL day (the case where the
#      panel day and the UTC day differ)
#   D. both tests pass here
#   E. the docs say which clock the slot uses, and that moving /etc/localtime does not change it
#   F. the operator's LAN PostgreSQL test box is documented (their 2026-11-18 decision)
#   G. discoverable: registered, indexed, recorded

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

SCHED="internal/update/scheduler.go"
TEST="internal/update/scheduler_tz_b493_test.go"
DOC="docs/UPDATE.md"
OPS="docs/operations.md"

# ═══════════════ A. the scheduler's clock source ═══════════════
case "$(cat "$SCHED")" in
    *'now := localtime.Now()'*) ok "A1: the tick takes its 'now' from the panel timezone" ;;
    *) bad "A1: the tick does not use localtime.Now()" ;;
esac
case "$(cat "$SCHED")" in
    *'ranTodayAt(timeStr, localtime.In(lastRun), now)'*)
        ok "A2: the last-run comparison is converted into the panel timezone" ;;
    *) bad "A2: the last-run timestamp is compared without converting it — the dedup would use the stored (UTC) day" ;;
esac
# NOT a plain substring: `localtime.Now()` CONTAINS "time.Now()" and matched this check on the first
# run. A host-clock call is `time.Now()` with a non-identifier character (or the line start) in front.
n_time_now="$(grep -cE '(^|[^[:alnum:]_])time\.Now\(\)' "$SCHED" 2>/dev/null)"
case "$n_time_now" in
    0) ok "A3: no time.Now() left in the scheduler (one clock, not two)" ;;
    *) bad "A3: the scheduler still calls time.Now() ${n_time_now} time(s) — that is the host clock, not the panel's" ;;
esac
case "$(cat "$SCHED")" in
    *'localtime.FormatClock'*) ok "A4: the trigger log renders the slot in the panel timezone" ;;
    *) bad "A4: the trigger log does not name the panel timezone" ;;
esac

# ═══════════════ B/C. the two measured cases are pinned by tests ═══════════════
if [ -f "$TEST" ]; then ok "B1: the timezone test file exists"; else bad "B1: no timezone test file"; fi
for t in TestB493_ScheduleIsEvaluatedInThePanelTimezone TestB493_LastRunDedupUsesThePanelTimezone; do
    if grep -q "func $t" "$TEST" 2>/dev/null; then
        ok "B/C: $t is present"
    else
        bad "B/C: $t is missing"
    fi
done
case "$(cat "$TEST" 2>/dev/null)" in
    *'Europe/Moscow'*) ok "B2: the test uses a zone that is NOT the host's (UTC +03:00)" ;;
    *) bad "B2: the test does not pin a non-UTC zone, so it would pass on a UTC-only implementation" ;;
esac
case "$(cat "$TEST" 2>/dev/null)" in
    *'scheduledRunDue'*) ok "B3: it drives the real due-window predicate" ;;
    *) bad "B3: it does not exercise the due-window predicate" ;;
esac

tout="$(go test ./internal/update/ -run 'B493' -count=1 2>&1)"; trc=$?
if [ "$trc" -eq 0 ]; then
    ok "D1: both timezone tests pass here"
else
    bad "D1: the timezone tests fail: $(printf '%s' "$tout" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════ E. the docs say which clock ═══════════════
case "$(cat "$DOC" 2>/dev/null)" in
    *'Which clock does'*) ok "E1: docs/UPDATE.md explains which clock the slot uses" ;;
    *) bad "E1: the update guide does not say whose clock 08:00 is" ;;
esac
case "$(cat "$DOC" 2>/dev/null)" in
    *'global_settings.timezone'*) ok "E2: it names the actual setting" ;;
    *) bad "E2: it does not name the setting that decides the clock" ;;
esac
case "$(cat "$DOC" 2>/dev/null)" in
    *'/etc/localtime'*) ok "E3: it states that the host zone does not move the slot" ;;
    *) bad "E3: it leaves the host-zone question open — the trap that produced this block" ;;
esac

# ═══════════════ F. the LAN PG test box ═══════════════
case "$(cat "$OPS" 2>/dev/null)" in
    *'A dedicated PostgreSQL test box on the LAN'*)
        ok "F1: the operator's decision (a LAN PG box for testing) is recorded with a recipe" ;;
    *) bad "F1: the LAN PG test box is not documented" ;;
esac
case "$(cat "$OPS" 2>/dev/null)" in
    *'SKYGATE_TEST_PG_DSN'*) ok "F2: the recipe uses the supported DSN knob (no new code path)" ;;
    *) bad "F2: the recipe does not use the supported knob" ;;
esac
if grep -q 'LAN_HOST' "$OPS" 2>/dev/null; then
    ok "F3: the recipe uses a placeholder host, not an address (rule 6)"
else
    bad "F3: the recipe does not use a placeholder host"
fi

# ═══════════════ G. discoverability ═══════════════
if grep -q 'check_b493_schedule_timezone.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "G1: registered in scripts/verify_pre_deploy.sh"
else
    bad "G1: not registered in the catalog"
fi
if grep -q '^\- \*\*B493\*\*' AGENTS.md 2>/dev/null; then
    ok "G2: the AGENTS index carries its line"
else
    bad "G2: no AGENTS index line"
fi
if grep -q 'B493' docs/ROADMAP.md 2>/dev/null; then
    ok "G3: the roadmap records it"
else
    bad "G3: the roadmap does not mention B493"
fi

echo
echo "  B493: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
