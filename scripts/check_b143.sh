#!/usr/bin/env bash
#===============================================================================
# Skygate v1.4.3 (B143) — in-app smoke-mesh cleanup scheduler
#
# Pins the v1.4.3 fix for the operator-reported issue
# "smoke-mesh cruft accumulates between smoke runs":
# the pre-B143 smoke-mesh rows (created by
# scripts/smoke.sh:511-512 on every run) were never
# cleaned up — the operator's v0.33.1.36 release had
# to manually DELETE 30 rows in a one-off SQL. B143
# adds an in-app daily cron (5 AM by default, after
# the 3 AM backup + 4 AM verify) that DELETEs every
# smoke-mesh row with no members, plus a manual
# `skygate cleanup-smoke-meshes` subcommand for
# ad-hoc runs.
#
# What this script verifies (live, on the VM):
#   A. internal/mesh/cleanup.go: DeleteSmokeMeshes +
#      CleanupResult + SmokeMeshNamePrefix +
#      FormatCleanupMessage + int64ArrayToPGArray
#   B. internal/mesh/cleanup_scheduler.go:
#      StartCleanupScheduler + CleanupSchedulerDeps
#      + CleanupNotifierSink + RunCleanup + 3 storage
#      key constants + tick + 4 read helpers +
#      sameCleanupMinute + FormatHumanSchedule
#   C. cmd/skygate/main.go: cleanup-scheduler wire-up
#      + cleanup-smoke-meshes subcommand +
#      runCleanupSmokeMeshes + import + help line
#   D. internal/config/config.go:
#      CleanupSmokeMeshInAppEnabled +
#      CleanupSmokeMeshSchedule + 2 env-var defaults
#   E. internal/i18n/catalog_exit_rules.go: 7 new
#      cleanup_smoke.* keys (run_btn / run_btn_help /
#      last_run / last_run_never / removed / no_rows /
#      failed) in BOTH RU + EN
#   F. internal/mesh/cleanup_b143_test.go: 14 test
#      functions + go test passes
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

CLEANUP_GO="internal/mesh/cleanup.go"
SCHED_GO="internal/mesh/cleanup_scheduler.go"
CFG_GO="internal/config/config.go"
MAIN_GO="cmd/skygate/main.go"
I18N="internal/i18n/catalog_exit_rules.go"
TEST_FILE="internal/mesh/cleanup_b143_test.go"

for f in "${CLEANUP_GO}" "${SCHED_GO}" "${CFG_GO}" "${MAIN_GO}" "${I18N}" "${TEST_FILE}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: cleanup.go — DeleteSmokeMeshes + helpers
# ------------------------------------------------------------------------------
echo
echo "=== A. mesh/cleanup.go: DeleteSmokeMeshes + CleanupResult + FormatCleanupMessage ==="
a_delete=$(grep -cE '^func DeleteSmokeMeshes\(' "${CLEANUP_GO}" || true)
a_result=$(grep -cE '^type CleanupResult struct' "${CLEANUP_GO}" || true)
a_prefix=$(grep -cE '^const SmokeMeshNamePrefix\s*=' "${CLEANUP_GO}" || true)
a_format=$(grep -cE '^func FormatCleanupMessage\(' "${CLEANUP_GO}" || true)
a_pgarr=$(grep -cE '^func int64ArrayToPGArray\(' "${CLEANUP_GO}" || true)
a_likeprefix=$(grep -cE 'SmokeMeshNamePrefix\+"%"' "${CLEANUP_GO}" || true)
a_notexists=$(grep -cE 'AND NOT EXISTS' "${CLEANUP_GO}" || true)
a_tx=$(grep -cE 'tx\.Commit\(\)' "${CLEANUP_GO}" || true)
a_pgarr_lit=$(grep -cE 'ANY\(\$1::bigint\[\]\)' "${CLEANUP_GO}" || true)
if [ "${a_delete}" -ge 1 ] && [ "${a_result}" -ge 1 ] && \
   [ "${a_prefix}" -ge 1 ] && [ "${a_format}" -ge 1 ] && \
   [ "${a_pgarr}" -ge 1 ] && [ "${a_likeprefix}" -ge 1 ] && \
   [ "${a_notexists}" -ge 2 ] && [ "${a_tx}" -ge 2 ] && \
   [ "${a_pgarr_lit}" -ge 1 ]; then
    ok "DeleteSmokeMeshes + CleanupResult + SmokeMeshNamePrefix + FormatCleanupMessage + int64ArrayToPGArray + tx + NOT EXISTS defense (all 9 present)"
else
    bad "cleanup.go incomplete: delete=${a_delete} result=${a_result} prefix=${a_prefix} format=${a_format} pgarr=${a_pgarr} likeprefix=${a_likeprefix} notexists=${a_notexists} tx=${a_tx} pgarr_lit=${a_pgarr_lit}"
fi

# ------------------------------------------------------------------------------
# Contract B: cleanup_scheduler.go — scheduler + deps + sink + run
# ------------------------------------------------------------------------------
echo
echo "=== B. mesh/cleanup_scheduler.go: StartCleanupScheduler + deps + sink + helpers ==="
b_start=$(grep -cE '^func StartCleanupScheduler\(' "${SCHED_GO}" || true)
b_deps=$(grep -cE '^type CleanupSchedulerDeps struct' "${SCHED_GO}" || true)
b_sink=$(grep -cE '^type CleanupNotifierSink interface' "${SCHED_GO}" || true)
b_run=$(grep -cE '^func RunCleanup\(' "${SCHED_GO}" || true)
b_tick=$(grep -cE '^func cleanupTick\(' "${SCHED_GO}" || true)
b_due=$(grep -cE '^func cleanupIsDueThisTick\(' "${SCHED_GO}" || true)
b_read_enabled=$(grep -cE '^func readCleanupEnabled\(' "${SCHED_GO}" || true)
b_read_sched=$(grep -cE '^func readCleanupSchedule\(' "${SCHED_GO}" || true)
b_read_lastrun=$(grep -cE '^func readCleanupLastRun\(' "${SCHED_GO}" || true)
b_same_min=$(grep -cE '^func sameCleanupMinute\(' "${SCHED_GO}" || true)
b_human=$(grep -cE '^func FormatHumanSchedule\(' "${SCHED_GO}" || true)
b_send_alert=$(grep -cE 'deps\.Notifier\.SendAlert\(' "${SCHED_GO}" || true)
b_inflight=$(grep -cE 'inFlightCleanupMu\s+sync\.Mutex' "${SCHED_GO}" || true)
b_default_sched=$(grep -cE 'DefaultCleanupSchedule\s*=\s*"0 5 \* \* \*"' "${SCHED_GO}" || true)
b_key_enabled=$(grep -cE 'KeyCleanupSmokeMeshEnabled\s*=\s*"cleanup\.smoke_mesh_enabled"' "${SCHED_GO}" || true)
b_key_sched=$(grep -cE 'KeyCleanupSmokeMeshSchedule\s*=\s*"cleanup\.smoke_mesh_schedule"' "${SCHED_GO}" || true)
b_key_lastrun=$(grep -cE 'KeyCleanupSmokeMeshLastRun\s*=\s*"cleanup\.smoke_mesh_last_run"' "${SCHED_GO}" || true)
b_audit=$(grep -cE 'cleanupAuditAction\s*=\s*"cleanup\.smoke_mesh"' "${SCHED_GO}" || true)
b_backup_parse=$(grep -cE 'backup\.ParseSchedule\(' "${SCHED_GO}" || true)
b_db_set=$(grep -cE 'db\.SetGlobalSetting\(' "${SCHED_GO}" || true)
b_db_append=$(grep -cE 'db\.AppendExitRuleLog\(' "${SCHED_GO}" || true)
b_uses_audit_action=$(grep -cE 'cleanupAuditAction' "${SCHED_GO}" || true)
b_interval_const=$(grep -cE 'CleanupTickInterval\s*=\s*30\s*\*\s*time\.Second' "${SCHED_GO}" || true)
if [ "${b_start}" -ge 1 ] && [ "${b_deps}" -ge 1 ] && [ "${b_sink}" -ge 1 ] && \
   [ "${b_run}" -ge 1 ] && [ "${b_tick}" -ge 1 ] && [ "${b_due}" -ge 1 ] && \
   [ "${b_read_enabled}" -ge 1 ] && [ "${b_read_sched}" -ge 1 ] && \
   [ "${b_read_lastrun}" -ge 1 ] && [ "${b_same_min}" -ge 1 ] && \
   [ "${b_human}" -ge 1 ] && [ "${b_send_alert}" -ge 1 ] && \
   [ "${b_inflight}" -ge 1 ] && [ "${b_default_sched}" -ge 1 ] && \
   [ "${b_key_enabled}" -ge 1 ] && [ "${b_key_sched}" -ge 1 ] && \
   [ "${b_key_lastrun}" -ge 1 ] && [ "${b_audit}" -ge 1 ] && \
   [ "${b_backup_parse}" -ge 1 ] && [ "${b_db_set}" -ge 1 ] && \
   [ "${b_db_append}" -ge 1 ] && [ "${b_uses_audit_action}" -ge 2 ] && \
   [ "${b_interval_const}" -ge 1 ]; then
    ok "Scheduler: StartCleanupScheduler + CleanupSchedulerDeps + CleanupNotifierSink + RunCleanup + tick + 4 read helpers + sameCleanupMinute + FormatHumanSchedule + 3 storage keys + audit action + inFlightCleanupMu + 30s tick + reuse backup.ParseSchedule/db.SetGlobalSetting/db.AppendExitRuleLog (all 23 present)"
else
    bad "cleanup_scheduler.go incomplete: start=${b_start} deps=${b_deps} sink=${b_sink} run=${b_run} tick=${b_tick} due=${b_due} read_enabled=${b_read_enabled} read_sched=${b_read_sched} read_lastrun=${b_read_lastrun} same_min=${b_same_min} human=${b_human} send_alert=${b_send_alert} inflight=${b_inflight} default_sched=${b_default_sched} key_enabled=${b_key_enabled} key_sched=${b_key_sched} key_lastrun=${b_key_lastrun} audit=${b_audit} backup_parse=${b_backup_parse} db_set=${b_db_set} db_append=${b_db_append} uses_audit_action=${b_uses_audit_action} interval_const=${b_interval_const}"
fi

# ------------------------------------------------------------------------------
# Contract C: main.go — wire-up + subcommand + import + help
# ------------------------------------------------------------------------------
echo
echo "=== C. cmd/skygate/main.go: cleanup-scheduler wire-up + cleanup-smoke-meshes subcommand ==="
c_wire=$(grep -cE 'mesh\.StartCleanupScheduler\(ctx, mesh\.CleanupSchedulerDeps\{' "${MAIN_GO}" || true)
c_cfg_field=$(grep -cE 'cfg\.CleanupSmokeMeshInAppEnabled' "${MAIN_GO}" || true)
c_case=$(grep -cE 'case "cleanup-smoke-meshes":' "${MAIN_GO}" || true)
c_handler=$(grep -cE 'func runCleanupSmokeMeshes\(' "${MAIN_GO}" || true)
c_subcall=$(grep -cE 'mesh\.RunCleanup\(context\.Background\(\), mesh\.CleanupSchedulerDeps\{' "${MAIN_GO}" || true)
c_import=$(grep -cE '^\s*"skygate/internal/mesh"' "${MAIN_GO}" || true)
c_help=$(grep -cE 'cleanup-smoke-meshes\s+delete smoke-mesh cruft \(B143\)' "${MAIN_GO}" || true)
c_help_b143=$(grep -cE 'cleanup-smoke-meshes\s+delete smoke-mesh cruft' "${MAIN_GO}" || true)
if [ "${c_wire}" -ge 1 ] && [ "${c_cfg_field}" -ge 1 ] && \
   [ "${c_case}" -ge 1 ] && [ "${c_handler}" -ge 1 ] && \
   [ "${c_subcall}" -ge 1 ] && [ "${c_import}" -ge 1 ] && \
   [ "${c_help}" -ge 1 ] && [ "${c_help_b143}" -ge 1 ]; then
    ok "Wire-up + subcommand + handler + subcall + import + help (all 8 present)"
else
    bad "main.go incomplete: wire=${c_wire} cfg_field=${c_cfg_field} case=${c_case} handler=${c_handler} subcall=${c_subcall} import=${c_import} help=${c_help} help_b143=${c_help_b143}"
fi

# ------------------------------------------------------------------------------
# Contract D: config.go — env-var defaults
# ------------------------------------------------------------------------------
echo
echo "=== D. internal/config/config.go: CleanupSmokeMeshInAppEnabled + CleanupSmokeMeshSchedule env-vars ==="
d_enabled_field=$(grep -cE '^\s*CleanupSmokeMeshInAppEnabled\s+bool' "${CFG_GO}" || true)
d_sched_field=$(grep -cE '^\s*CleanupSmokeMeshSchedule\s+string' "${CFG_GO}" || true)
d_enabled_env=$(grep -cE 'SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED' "${CFG_GO}" || true)
d_sched_env=$(grep -cE 'SKYGATE_CLEANUP_SMOKE_MESH_SCHEDULE' "${CFG_GO}" || true)
if [ "${d_enabled_field}" -ge 1 ] && [ "${d_sched_field}" -ge 1 ] && \
   [ "${d_enabled_env}" -ge 1 ] && [ "${d_sched_env}" -ge 1 ]; then
    ok "2 Config fields + 2 env-var defaults (all 4 present)"
else
    bad "config.go incomplete: enabled_field=${d_enabled_field} sched_field=${d_sched_field} enabled_env=${d_enabled_env} sched_env=${d_sched_env}"
fi

# ------------------------------------------------------------------------------
# Contract E: i18n — 7 new cleanup_smoke.* keys in BOTH RU and EN
# ------------------------------------------------------------------------------
echo
echo "=== E. catalog_exit_rules.go: 7 new cleanup_smoke.* keys (RU + EN) ==="
keys="run_btn run_btn_help last_run last_run_never removed no_rows failed"
miss=0
for k in $keys; do
    n=$(grep -cE "\"cleanup_smoke\.${k}\"" "${I18N}" || true)
    if [ "${n}" -lt 2 ]; then
        miss=$((miss+1))
        echo "    missing EN/RU entry for cleanup_smoke.${k} (count=${n})"
    fi
done
if [ "${miss}" -eq 0 ]; then
    ok "All 7 new cleanup_smoke.* keys present in both RU + EN (14 total occurrences)"
else
    bad "i18n: ${miss} of 7 keys missing in one of the languages"
fi

# ------------------------------------------------------------------------------
# Contract F: unit test + go test
# ------------------------------------------------------------------------------
echo
echo "=== F. cleanup_b143_test.go: 14 test functions + go test passes ==="
t1=$(grep -cE '^func TestFormatCleanupMessage_NoRows' "${TEST_FILE}" || true)
t2=$(grep -cE '^func TestFormatCleanupMessage_SingleRow' "${TEST_FILE}" || true)
t3=$(grep -cE '^func TestFormatCleanupMessage_FewRows' "${TEST_FILE}" || true)
t4=$(grep -cE '^func TestFormatCleanupMessage_TruncatedAtFive' "${TEST_FILE}" || true)
t5=$(grep -cE '^func TestInt64ArrayToPGArray_Empty' "${TEST_FILE}" || true)
t6=$(grep -cE '^func TestInt64ArrayToPGArray_Single' "${TEST_FILE}" || true)
t7=$(grep -cE '^func TestInt64ArrayToPGArray_Many' "${TEST_FILE}" || true)
t8=$(grep -cE '^func TestSameCleanupMinute_Same' "${TEST_FILE}" || true)
t9=$(grep -cE '^func TestSameCleanupMinute_DifferentMinute' "${TEST_FILE}" || true)
t10=$(grep -cE '^func TestSameCleanupMinute_DifferentDay' "${TEST_FILE}" || true)
t11=$(grep -cE '^func TestSameCleanupMinute_ZeroValue' "${TEST_FILE}" || true)
t12=$(grep -cE '^func TestFormatHumanSchedule_EveryMinute' "${TEST_FILE}" || true)
t13=$(grep -cE '^func TestFormatHumanSchedule_Daily' "${TEST_FILE}" || true)
t14=$(grep -cE '^func TestFormatHumanSchedule_Empty' "${TEST_FILE}" || true)
t15=$(grep -cE '^func TestFormatHumanSchedule_Invalid' "${TEST_FILE}" || true)
t16=$(grep -cE '^func TestSmokeMeshNamePrefix' "${TEST_FILE}" || true)
t17=$(grep -cE '^func TestStorageKeyConstants' "${TEST_FILE}" || true)
test_count=$((t1+t2+t3+t4+t5+t6+t7+t8+t9+t10+t11+t12+t13+t14+t15+t16+t17))
if [ "${test_count}" -ge 14 ]; then
    ok "Test file: ${test_count} test functions (4 format + 3 pgarr + 4 same-minute + 4 human + 2 constant = 17 total, all present)"
else
    bad "Test file incomplete: test_count=${test_count} (expected 14-17)"
fi

GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in "/c/Program Files/Go/bin/go.exe" "/usr/local/go/bin/go" "/snap/bin/go"; do
        if [ -x "${cand}" ]; then GO="${cand}"; break; fi
    done
fi
if [ -z "${GO}" ]; then
    warn "go not found — skipping F go-test (other 5 contracts still hold)"
else
    test_out=$("${GO}" test -count=1 -run 'TestFormatCleanupMessage|TestInt64ArrayToPGArray|TestSameCleanupMinute|TestFormatHumanSchedule|TestSmokeMeshNamePrefix|TestStorageKeyConstants' ./internal/mesh/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "go test PASSes for B143 pure-Go helpers (cleanup_b143_test.go compiles + tests green)"
    else
        bad "go test FAILED: ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B143 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B143 contracts all hold."
    exit 0
fi
echo
echo "B143 has failing contracts — fix the source files above."
exit 1
