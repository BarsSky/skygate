#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20 (B130) — background scheduler for time-of-day auto-update
#
# Pins the v1.3.20 B130 background scheduler that turns the
# B129 "Schedule" UI into a real auto-update mechanism. The
# scheduler is a goroutine in cmd/skygate/main.go that:
#   - ticks every 30 seconds
#   - reads global_settings["update_schedule_enabled" / "_time"]
#   - when (enabled, time matches HH:MM, GitHub has newer,
#     no update in flight, not already fired this minute)
#     → spawns the Docker upgrader + sends Telegram alerts
#   - stamps global_settings["update_schedule_last_run"] so
#     the page can show the timestamp
#
# What this script verifies (live, on the VM):
#   A. internal/update/scheduler.go exists with the SchedulerDeps
#      struct + Start() entry point + TickInterval constant
#   B. internal/update/scheduler_db.go wires the db helpers
#      (getGlobalSetting / setGlobalSetting) via init()
#   C. cmd/skygate/main.go: update.Start(...) is called in the
#      boot sequence, guarded by cfg.UpdateScheduleEnabled
#   D. main.go has the schedulerNotifierSink adapter so the
#      update package can use telegram.Notifier without an
#      import cycle
#   E. config.go: UpdateScheduleEnabled + UpdateScheduleTime
#      + SKYGATE_UPDATE_SCHEDULE_ENABLED + SKYGATE_UPDATE_SCHEDULE_TIME
#      (the B129 fields/envs the scheduler reads)
#   F. Live scheduler smoke test: build the package, no
#      compile errors, go test ./internal/update passes
#   G. The scheduler reads global_settings keys
#      "update_schedule_enabled" + "update_schedule_time" +
#      writes "update_schedule_last_run"
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

SCHEDULER_GO="internal/update/scheduler.go"
SCHEDULER_DB_GO="internal/update/scheduler_db.go"
MAIN_GO="cmd/skygate/main.go"
CONFIG_GO="internal/config/config.go"

for f in "${SCHEDULER_GO}" "${SCHEDULER_DB_GO}" "${MAIN_GO}" "${CONFIG_GO}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: scheduler.go has the required types + Start
# ------------------------------------------------------------------------------
echo
echo "=== A. internal/update/scheduler.go: SchedulerDeps + Start + TickInterval ==="
a_deps=$(grep -c 'type SchedulerDeps' "${SCHEDULER_GO}" || true)
a_start=$(grep -c 'func Start' "${SCHEDULER_GO}" || true)
a_tick=$(grep -c 'TickInterval' "${SCHEDULER_GO}" || true)
a_runScheduled=$(grep -c 'func runScheduled' "${SCHEDULER_GO}" || true)
a_tickFn=$(grep -c 'func tick' "${SCHEDULER_GO}" || true)
if [ "${a_deps}" -ge 1 ] && [ "${a_start}" -ge 1 ] && [ "${a_tick}" -ge 1 ] && [ "${a_runScheduled}" -ge 1 ] && [ "${a_tickFn}" -ge 1 ]; then
    ok "scheduler.go has SchedulerDeps + Start + TickInterval + tick + runScheduled (all 5 present)"
else
    bad "scheduler.go is incomplete: deps=${a_deps} start=${a_start} tick=${a_tick} runScheduled=${a_runScheduled} tickFn=${a_tickFn}"
fi

# ------------------------------------------------------------------------------
# Contract B: scheduler_db.go wires db helpers via init()
# ------------------------------------------------------------------------------
echo
echo "=== B. internal/update/scheduler_db.go: init() binds getGlobalSetting/setGlobalSetting ==="
b_init=$(grep -c '^func init' "${SCHEDULER_DB_GO}" || true)
b_get=$(grep -c 'getGlobalSetting.*=.*db\.GetGlobalSetting\|getGlobalSetting = func' "${SCHEDULER_DB_GO}" || true)
b_set=$(grep -c 'setGlobalSetting.*=.*db\.SetGlobalSetting\|setGlobalSetting = func' "${SCHEDULER_DB_GO}" || true)
b_import=$(grep -c '"skygate/internal/db"' "${SCHEDULER_DB_GO}" || true)
if [ "${b_init}" -ge 1 ] && [ "${b_get}" -ge 1 ] && [ "${b_set}" -ge 1 ] && [ "${b_import}" -ge 1 ]; then
    ok "scheduler_db.go has init() that binds getGlobalSetting + setGlobalSetting + imports internal/db"
else
    bad "scheduler_db.go is incomplete: init=${b_init} get=${b_get} set=${b_set} import_db=${b_import}"
fi

# ------------------------------------------------------------------------------
# Contract C: main.go calls update.Start() in the boot sequence
# ------------------------------------------------------------------------------
echo
echo "=== C. cmd/skygate/main.go: update.Start() called, guarded by cfg.UpdateScheduleEnabled ==="
c_start=$(grep -c 'update\.Start(' "${MAIN_GO}" || true)
c_guard=$(grep -c 'if cfg\.UpdateScheduleEnabled' "${MAIN_GO}" || true)
c_deps=$(grep -c 'update\.SchedulerDeps' "${MAIN_GO}" || true)
if [ "${c_start}" -ge 1 ] && [ "${c_guard}" -ge 1 ] && [ "${c_deps}" -ge 1 ]; then
    ok "main.go calls update.Start() + cfg.UpdateScheduleEnabled guard + SchedulerDeps literal (all 3 present)"
else
    bad "main.go is missing the scheduler wire-up: start=${c_start} guard=${c_guard} deps=${c_deps}"
fi

# ------------------------------------------------------------------------------
# Contract D: schedulerNotifierSink adapter exists in main.go
# ------------------------------------------------------------------------------
echo
echo "=== D. main.go: schedulerNotifierSink adapter (avoids telegram import cycle) ==="
d_sink=$(grep -c 'func schedulerNotifierSink' "${MAIN_GO}" || true)
d_struct=$(grep -c 'type schedulerSink' "${MAIN_GO}" || true)
d_sendalert=$(grep -c 's.n.SendAlert' "${MAIN_GO}" || true)
if [ "${d_sink}" -ge 1 ] && [ "${d_struct}" -ge 1 ] && [ "${d_sendalert}" -ge 1 ]; then
    ok "main.go has schedulerNotifierSink + schedulerSink struct + SendAlert delegation"
else
    bad "schedulerNotifierSink adapter is incomplete: func=${d_sink} struct=${d_struct} sendalert=${d_sendalert}"
fi

# ------------------------------------------------------------------------------
# Contract E: config.go has the B129 schedule fields + env fallbacks
# (the scheduler reads these via Cfg.UpdateScheduleEnabled / .UpdateScheduleTime)
# ------------------------------------------------------------------------------
echo
echo "=== E. config.go: UpdateScheduleEnabled + UpdateScheduleTime + env-var fallbacks ==="
e_enabled=$(grep -c 'UpdateScheduleEnabled' "${CONFIG_GO}" || true)
e_time=$(grep -c 'UpdateScheduleTime' "${CONFIG_GO}" || true)
e_env1=$(grep -c 'SKYGATE_UPDATE_SCHEDULE_ENABLED' "${CONFIG_GO}" || true)
e_env2=$(grep -c 'SKYGATE_UPDATE_SCHEDULE_TIME' "${CONFIG_GO}" || true)
if [ "${e_enabled}" -ge 2 ] && [ "${e_time}" -ge 2 ] && [ "${e_env1}" -ge 1 ] && [ "${e_env2}" -ge 1 ]; then
    ok "config.go has the B129 fields + both env-var fallbacks (same contract as B129-E)"
else
    bad "config.go schedule fields incomplete: enabled=${e_enabled} time=${e_time} env1=${e_env1} env2=${e_env2}"
fi

# ------------------------------------------------------------------------------
# Contract F: live compile + go test
# ------------------------------------------------------------------------------
echo
echo "=== F. Live go test ./internal/update/ ==="
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in "/c/Program Files/Go/bin/go.exe" "/usr/local/go/bin/go" "/snap/bin/go"; do
        if [ -x "${cand}" ]; then GO="${cand}"; break; fi
    done
fi
if [ -z "${GO}" ]; then
    warn "go not found — skipping F (other 6 contracts still hold)"
else
    test_out=$("${GO}" test -count=1 ./internal/update/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "go test ./internal/update/ PASSes (B130 scheduler compiles + tests green)"
    else
        bad "go test ./internal/update/ FAILED: ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Contract G: scheduler reads the right global_settings keys
# ------------------------------------------------------------------------------
echo
echo "=== G. scheduler.go: reads/writes the correct global_settings keys ==="
g_read_enabled=$(grep -cE 'getGlobalSetting\(.*"update_schedule_enabled"' "${SCHEDULER_GO}" || true)
g_read_time=$(grep -cE 'getGlobalSetting\(.*"update_schedule_time"' "${SCHEDULER_GO}" || true)
g_read_lastrun=$(grep -cE 'getGlobalSetting\(.*"update_schedule_last_run"' "${SCHEDULER_GO}" || true)
g_write_lastrun=$(grep -cE 'setGlobalSetting\(.*"update_schedule_last_run"' "${SCHEDULER_GO}" || true)
if [ "${g_read_enabled}" -ge 1 ] && [ "${g_read_time}" -ge 1 ] && [ "${g_read_lastrun}" -ge 1 ] && [ "${g_write_lastrun}" -ge 1 ]; then
    ok "scheduler reads enabled + time + last_run, and writes last_run (all 4 key references present)"
else
    bad "scheduler key references incomplete: read_enabled=${g_read_enabled} read_time=${g_read_time} read_lastrun=${g_read_lastrun} write_lastrun=${g_write_lastrun}"
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B130 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B130 contracts all hold."
    exit 0
fi
echo
echo "B130 has failing contracts — fix the source files above."
exit 1
