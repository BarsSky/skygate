#!/usr/bin/env bash
# check_b446_backup_truth.sh — B446 (2026-10-30)
#
# «По прогону тестов 4 упало, приоритет с теми что по бекапам. Надо наконец разобрать что не так.»
#
# The in-app system-test run on the reference install (30 total, 25 pass, 4 fail, 1 skip) failed
# on `backup.recent` and `backup.verify`:
#
#	backup.recent — "the last recorded backup run is 216h0m0s old ... the destination may have
#	                 stopped accepting archives"
#	backup.verify — "the last successful backup is 216h0m0s old (under 48 h expected)"
#
# Both blamed the S3 DESTINATION. Measured in the same container:
#
#	backup.enabled=1  backup.in_app_enabled=1  backup.schedule="0 4 * * *"
#	backup.last_status=ok  backup.last_error=""            ← the destination never failed
#	journal: the app BOOTED at 04:02:37 UTC that morning
#
# so the process kept being recreated ACROSS the scheduled 04:00 minute, and the scheduler fired
# only INSIDE that exact minute — nine days without a run, with `last_status=ok` because nothing
# ever tried. TWO defects, both fixed here:
#
#   1. A missed minute cost a whole day: ScheduleDue now catches up a passed slot on the next tick
#      (with no double run inside one period and none while a run is in flight), and the scheduler
#      logs WHY it fired.
#   2. The diagnostic answered the wrong question: backupStaleCause names the cause from the
#      settings the scheduler reads (disabled / nothing schedules it / missed slot / the run
#      FAILED with this error), and the panel stopped crediting a system cron nobody can see.
#
# Third, unrelated but red in the same gate run: two contracts called bare `python`, which is not
# installed on that host (`python3` is), so a MISSING TOOL was reported as a PRODUCT regression.
# One resolver in scripts/lib/python_audit.sh now serves every audit and SKIPs when neither exists.
#
# CONTRACTS: 26.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# B339: read the cmd/skygate PACKAGE surface, not main.go alone.
. scripts/lib/gosurface.sh

TRUTH=internal/backup/schedule_truth_b446.go
TRUTH_T=internal/backup/schedule_truth_b446_test.go
CAUSE=internal/feature/admin/backup_stale_cause_b446.go
CAUSE_T=internal/feature/admin/backup_stale_cause_b446_test.go
SCHED=internal/backup/scheduler.go
SYST=internal/feature/admin/system_tests.go
SYST2=internal/feature/admin/system_tests_b394_new.go
MAINPKG=cmd/skygate          # the whole package: a contract must read a SURFACE (B339), not
                             # main.go alone — that is the file B372 caps and B446 shrinks
BOOT=cmd/skygate/backup_boot_b446.go
PYLIB=scripts/lib/python_audit.sh
GATE=scripts/verify_pre_deploy.sh
GOSURFACE=scripts/lib/gosurface.sh
for f in "$TRUTH" "$TRUTH_T" "$CAUSE" "$CAUSE_T" "$SCHED" "$SYST" "$SYST2" "$BOOT" "$PYLIB" "$GOSURFACE" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B446: truth=%s cause=%s python=%s\n' "$TRUTH" "$CAUSE" "$PYLIB"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b446.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. a missed minute no longer costs a day"
GOFMT_OUT="$(gofmt -l "$TRUTH" "$TRUTH_T" "$CAUSE" "$CAUSE_T" "$SCHED" "$SYST" "$SYST2" "$MAIN" "$BOOT" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$SCHED" 'ScheduleDue(sched, now, cfg.LastRun, cfg.LastStatus)'; then
  ok "A2: the scheduler asks the catch-up rule instead of comparing the minute itself"
else
  bad "A2: the scheduler still fires only inside the exact scheduled minute"
fi
if has "$TRUTH" 'catching up a missed slot'; then
  ok "A3: a passed slot is caught up on the next tick"
else
  bad "A3: no catch-up — a restart across the slot still loses the whole period"
fi
if has "$TRUTH" 'lastStatus == "running"'; then
  ok "A4: a run in flight is never duplicated"
else
  bad "A4: a long backup could be started twice"
fi
if has "$TRUTH" 'previousSlot'; then
  ok "A5: the period reference is a named function (one run per period, not per minute)"
else
  bad "A5: the period arithmetic is implicit"
fi
if has "$SCHED" 'due.Reason'; then
  ok "A6: the log says WHY it fired (scheduled minute vs catch-up)"
else
  bad "A6: a catch-up is indistinguishable from a scheduled run in the journal"
fi

hdr "B. the diagnostic answers the actual question"
if has "$CAUSE" 'enabled=0'; then
  ok "B1: a DISABLED backup is named as the cause"
else
  bad "B1: a disabled install would still be told the destination failed"
fi
if has "$CAUSE" 'in-app scheduler is OFF'; then
  ok "B2: a disabled in-app scheduler is named, and system cron is not credited blindly"
else
  bad "B2: the panel assumes a system cron it cannot see (the pre-B446 lie)"
fi
if has "$CAUSE" 'catch up the missed slot'; then
  ok "B3: a missed slot is explained as a missed slot"
else
  bad "B3: a missed slot is still blamed on something else"
fi
if has "$CAUSE" 'that error is the cause'; then
  ok "B4: a FAILED run quotes its own stored error"
else
  bad "B4: a failed run is not distinguished from one that never ran"
fi
if has "$SYST" 'backupStaleCause(getTestService(), ts)' && has "$SYST2" 'backupStaleCause(s, ts)'; then
  ok "B5: BOTH backup tests use the shared cause (no third opinion)"
else
  bad "B5: one of the two backup tests still guesses"
fi
if has "$SYST" 'the destination may have stopped accepting archives'; then
  ok "B6: the destination hypothesis survives only where it cannot be ruled out"
else
  bad "B6: the fallback sentence disappeared — an unexplained stale backup is worse"
fi

hdr "C. the journal says what the scheduler will do, and the verify banner stops lying"
if has "$BOOT" 'backup-scheduler: started'; then
  ok "C1: one startup line names enabled/schedule/last_run/next_slot"
else
  bad "C1: the scheduler still starts silently — the reason this took nine days to notice"
fi
gosurface MAIN_SURFACE "$MAINPKG"/*.go
if has "$MAIN_SURFACE" 'logBackupSchedulerStartup('; then
  ok "C2: the startup line is wired into the entrypoint"
else
  bad "C2: the helper exists but is never called"
fi
if has "$MAIN_SURFACE" 'Pre-B142 system-cron verify_backup.sh continues to run'; then
  bad "C3: the verify banner still claims a system cron «continues to run»"
else
  ok "C3: the verify banner no longer credits a cron the panel cannot see"
fi
# B372 caps main.go; the diagnostic had to move into its own file rather than grow the entrypoint.
MAIN_FILE="$(ls cmd/skygate/main*.go 2>/dev/null | head -1)"
LINES="$(wc -l < "$MAIN_FILE" 2>/dev/null | tr -d ' ')"
if [ "${LINES:-0}" -le 2400 ]; then
  ok "C4: main.go stays within the B372 cap (${LINES} lines)"
else
  bad "C4: main.go is ${LINES} lines — over the B372 cap of 2400"
fi

hdr "D. a missing interpreter is not a product regression"
if has "$PYLIB" 'skygate_python'; then
  ok "D1: one resolver chooses python3 or python"
else
  bad "D1: no shared resolver"
fi
for f in scripts/check_b438_exit_rules_table_and_filter.sh scripts/check_b439_serving_relay.sh scripts/check_b440_relay_requests.sh; do
  if has "$f" 'skygate_python'; then
    ok "D2: $f uses the shared resolver"
  else
    bad "D2: $f still calls a bare interpreter name"
  fi
done
if has scripts/check_b438_exit_rules_table_and_filter.sh 'no python interpreter (python3/python) on PATH'; then
  ok "D3: a host without any interpreter reports SKIP, not FAIL"
else
  bad "D3: a missing interpreter still fails the check"
fi

hdr "E. the rules are pinned by tests"
if ! command -v go >/dev/null 2>&1; then
  skip "E1: no go toolchain on PATH — cannot run TestB446 here (CI is authoritative)"
elif go test ./internal/backup ./internal/feature/admin -run 'TestB446' -count=1 >"$WORK/t.log" 2>&1; then
  ok "E1: TestB446 green (catch-up, no double run, four causes, healthy install)"
else
  bad "E1: TestB446 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "E2: no go toolchain on PATH — cannot run the packages here"
elif go test ./internal/backup ./internal/feature/admin -count=1 >"$WORK/p.log" 2>&1; then
  ok "E2: both touched packages are green"
else
  bad "E2: a package suite failed — $(tail -3 "$WORK/p.log" | tr '\n' ' ')"
fi

hdr "F. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b446_backup_truth.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: this script is not tracked by git"
fi
if grep -qF 'check_b446_backup_truth' "$GATE" 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B446"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "F3: the B446 catalog description contains a backtick (TD-15)" ;;
  "")    bad "F3: the B446 catalog entry was not found" ;;
  *)     ok "F3: the B446 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB446 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
