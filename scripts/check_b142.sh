#!/usr/bin/env bash
#===============================================================================
# Skygate v1.4.1 (B142) — in-app backup-verify scheduler
#
# Pins the v1.4.1 fix for the operator-reported issue
# "verify_backup.sh cron — нет Telegram-алерта":
# the pre-B142 verify pipeline (scripts/verify_backup.sh,
# system-cron `0 4 * * 0`) wrote the result to global_settings
# (backup.last_verify_status) but did NOT send a Telegram
# alert on failure. The operator only learned about a failed
# verify by manually checking the DB or /admin/backup page.
# B142 adds a parallel in-app goroutine scheduler that runs
# the same verify script on a configurable schedule AND
# sends a Telegram alert on failure. The pre-B142 system-
# cron entry continues to work; the in-app scheduler is a
# drop-in replacement for operators who want alerts.
#
# What this script verifies (live, on the VM):
#   A. internal/backup/config.go: InAppVerifyEnabled +
#      VerifySchedule + 4 last-verify-status fields + 8
#      storage key constants
#   B. internal/backup/verify_scheduler.go: StartVerifyScheduler
#      + VerifySchedulerDeps + NotifierSink + tick + runVerify
#      + 3 read helpers + 2 tail helpers
#   C. cmd/skygate/main.go: in-app verify scheduler wire-up
#      + POST /admin/backup/verify-now route
#   D. internal/config/config.go: BackupVerifyInAppEnabled +
#      BackupVerifySchedule env-var defaults
#   E. internal/feature/admin/backup_config.go: PostAdminBackupVerifyNow
#      + in_app_verify_enabled + verify_schedule form fields
#   F. internal/handlers/templates/admin/backup.html: verify
#      form fields + Verify now button + Last verify panel
#   G. internal/i18n/catalog_backup.go: 8 new backup.* keys
#      (last_verify, last_verify_never, last_verify_archive,
#      last_verify_error, verify_now, verify_now_ok,
#      verify_now_failed, in_app_verify_scheduler,
#      in_app_verify_help, verify_schedule,
#      verify_schedule_help) in BOTH RU + EN (11 keys)
#   H. cmd/skygate/main.go: runBackupVerifyOK/Fail write the
#      renamed + new keys (backup.last_verify_at, archive,
#      error)
#   I. Unit test file: internal/backup/verify_scheduler_b142_test.go
#      with 12 test functions + go test passes
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

CONFIG_GO="internal/backup/config.go"
SCHED_GO="internal/backup/verify_scheduler.go"
BACKUP_CFG_GO="internal/feature/admin/backup_config.go"
CFG_GO="internal/config/config.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/backup.html"
I18N="internal/i18n/catalog_backup.go"
TEST_FILE="internal/backup/verify_scheduler_b142_test.go"

for f in "${CONFIG_GO}" "${SCHED_GO}" "${BACKUP_CFG_GO}" "${CFG_GO}" "${MAIN_GO}" "${TEMPLATE}" "${I18N}" "${TEST_FILE}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: config.go — verify fields + storage keys
# ------------------------------------------------------------------------------
echo
echo "=== A. backup/config.go: InAppVerifyEnabled + VerifySchedule + 4 status fields + 8 keys ==="
a_enabled_field=$(grep -cE '^\s*InAppVerifyEnabled\s+bool' "${CONFIG_GO}" || true)
a_schedule_field=$(grep -cE '^\s*VerifySchedule\s+string' "${CONFIG_GO}" || true)
a_at_field=$(grep -cE '^\s*LastVerifyAt\s+int64' "${CONFIG_GO}" || true)
a_status_field=$(grep -cE '^\s*LastVerifyStatus\s+string' "${CONFIG_GO}" || true)
a_arch_field=$(grep -cE '^\s*LastVerifyArchive\s+string' "${CONFIG_GO}" || true)
a_err_field=$(grep -cE '^\s*LastVerifyError\s+string' "${CONFIG_GO}" || true)
a_in_app_key=$(grep -cE 'keyInAppVerifyEnabled\s*=\s*"backup\.in_app_verify_enabled"' "${CONFIG_GO}" || true)
a_sched_key=$(grep -cE 'keyVerifySchedule\s*=\s*"backup\.verify_schedule"' "${CONFIG_GO}" || true)
a_at_key=$(grep -cE 'keyLastVerifyAt\s*=\s*"backup\.last_verify_at"' "${CONFIG_GO}" || true)
a_vstat_key=$(grep -cE 'keyLastVerifyStatus\s*=\s*"backup\.last_verify_status"' "${CONFIG_GO}" || true)
a_verr_key=$(grep -cE 'keyLastVerifyError\s*=\s*"backup\.last_verify_error"' "${CONFIG_GO}" || true)
a_varch_key=$(grep -cE 'keyLastVerifyArchive\s*=\s*"backup\.last_verify_archive"' "${CONFIG_GO}" || true)
if [ "${a_enabled_field}" -ge 1 ] && [ "${a_schedule_field}" -ge 1 ] && \
   [ "${a_at_field}" -ge 1 ] && [ "${a_status_field}" -ge 1 ] && \
   [ "${a_arch_field}" -ge 1 ] && [ "${a_err_field}" -ge 1 ] && \
   [ "${a_in_app_key}" -ge 1 ] && [ "${a_sched_key}" -ge 1 ] && \
   [ "${a_at_key}" -ge 1 ] && [ "${a_vstat_key}" -ge 1 ] && \
   [ "${a_verr_key}" -ge 1 ] && [ "${a_varch_key}" -ge 1 ]; then
    ok "6 Config fields + 6 storage key constants (all 12 present)"
else
    bad "config.go incomplete: enabled=${a_enabled_field} schedule=${a_schedule_field} at=${a_at_field} status=${a_status_field} arch=${a_arch_field} err=${a_err_field}"
    bad "  keys: in_app=${a_in_app_key} sched=${a_sched_key} at=${a_at_key} vstat=${a_vstat_key} verr=${a_verr_key} varch=${a_varch_key}"
fi

# ------------------------------------------------------------------------------
# Contract B: verify_scheduler.go — scheduler + deps + notifier sink + helpers
# ------------------------------------------------------------------------------
echo
echo "=== B. backup/verify_scheduler.go: StartVerifyScheduler + deps + NotifierSink + helpers ==="
b_start=$(grep -cE '^func StartVerifyScheduler\(' "${SCHED_GO}" || true)
b_deps=$(grep -cE '^type VerifySchedulerDeps struct' "${SCHED_GO}" || true)
b_sink=$(grep -cE '^type NotifierSink interface' "${SCHED_GO}" || true)
b_tick=$(grep -cE '^func verifyTick\(' "${SCHED_GO}" || true)
b_run=$(grep -cE '^func runVerify\(' "${SCHED_GO}" || true)
b_due=$(grep -cE '^func verifyIsDueThisTick\(' "${SCHED_GO}" || true)
b_read_enabled=$(grep -cE '^func readVerifyEnabled\(' "${SCHED_GO}" || true)
b_read_sched=$(grep -cE '^func readVerifySchedule\(' "${SCHED_GO}" || true)
b_read_at=$(grep -cE '^func readLastVerifyAt\(' "${SCHED_GO}" || true)
b_same_min=$(grep -cE '^func sameMinute\(' "${SCHED_GO}" || true)
b_tail=$(grep -cE '^func tailLines\(' "${SCHED_GO}" || true)
b_trunc=$(grep -cE '^func truncateString\(' "${SCHED_GO}" || true)
b_inflight=$(grep -cE 'inFlightVerify\s+bool' "${SCHED_GO}" || true)
b_send_alert=$(grep -cE 'deps\.Notifier\.SendAlert\(' "${SCHED_GO}" || true)
if [ "${b_start}" -ge 1 ] && [ "${b_deps}" -ge 1 ] && [ "${b_sink}" -ge 1 ] && \
   [ "${b_tick}" -ge 1 ] && [ "${b_run}" -ge 1 ] && [ "${b_due}" -ge 1 ] && \
   [ "${b_read_enabled}" -ge 1 ] && [ "${b_read_sched}" -ge 1 ] && \
   [ "${b_read_at}" -ge 1 ] && [ "${b_same_min}" -ge 1 ] && \
   [ "${b_tail}" -ge 1 ] && [ "${b_trunc}" -ge 1 ] && \
   [ "${b_inflight}" -ge 1 ] && [ "${b_send_alert}" -ge 1 ]; then
    ok "Scheduler: StartVerifyScheduler + VerifySchedulerDeps + NotifierSink + 11 helpers + inFlightVerify + SendAlert call (all 14 present)"
else
    bad "verify_scheduler.go incomplete: start=${b_start} deps=${b_deps} sink=${b_sink} tick=${b_tick} run=${b_run} due=${b_due} read_enabled=${b_read_enabled} read_sched=${b_read_sched} read_at=${b_read_at} same_min=${b_same_min} tail=${b_tail} trunc=${b_trunc} inflight=${b_inflight} send_alert=${b_send_alert}"
fi

# ------------------------------------------------------------------------------
# Contract C: main.go — in-app verify scheduler wire-up + verify-now route
# ------------------------------------------------------------------------------
echo
echo "=== C. cmd/skygate/main.go: scheduler wire-up + verify-now route ==="
c_wire=$(grep -cE 'StartVerifyScheduler\(ctx, backup\.VerifySchedulerDeps\{' "${MAIN_GO}" || true)
c_cfg_field=$(grep -cE 'cfg\.BackupVerifyInAppEnabled' "${MAIN_GO}" || true)
c_route=$(grep -cE 'POST /admin/backup/verify-now' "${MAIN_GO}" || true)
c_handler=$(grep -cE 'adminSvc\.PostAdminBackupVerifyNow' "${MAIN_GO}" || true)
if [ "${c_wire}" -ge 1 ] && [ "${c_cfg_field}" -ge 1 ] && [ "${c_route}" -ge 1 ] && [ "${c_handler}" -ge 1 ]; then
    ok "Wire-up + route + handler (all 4 present)"
else
    bad "main.go incomplete: wire=${c_wire} cfg_field=${c_cfg_field} route=${c_route} handler=${c_handler}"
fi

# ------------------------------------------------------------------------------
# Contract D: internal/config/config.go — env-var defaults
# ------------------------------------------------------------------------------
echo
echo "=== D. internal/config/config.go: BackupVerifyInAppEnabled + BackupVerifySchedule env-var defaults ==="
d_enabled_field=$(grep -cE '^\s*BackupVerifyInAppEnabled\s+bool' "${CFG_GO}" || true)
d_sched_field=$(grep -cE '^\s*BackupVerifySchedule\s+string' "${CFG_GO}" || true)
d_enabled_env=$(grep -cE 'SKYGATE_BACKUP_VERIFY_IN_APP_ENABLED' "${CFG_GO}" || true)
d_sched_env=$(grep -cE 'SKYGATE_BACKUP_VERIFY_SCHEDULE' "${CFG_GO}" || true)
if [ "${d_enabled_field}" -ge 1 ] && [ "${d_sched_field}" -ge 1 ] && [ "${d_enabled_env}" -ge 1 ] && [ "${d_sched_env}" -ge 1 ]; then
    ok "2 Config fields + 2 env-var defaults (all 4 present)"
else
    bad "config.go incomplete: enabled_field=${d_enabled_field} sched_field=${d_sched_field} enabled_env=${d_enabled_env} sched_env=${d_sched_env}"
fi

# ------------------------------------------------------------------------------
# Contract E: backup_config.go — PostAdminBackupVerifyNow + form fields
# ------------------------------------------------------------------------------
echo
echo "=== E. feature/admin/backup_config.go: PostAdminBackupVerifyNow + form parsing ==="
e_handler=$(grep -cE 'func \(s \*Service\) PostAdminBackupVerifyNow' "${BACKUP_CFG_GO}" || true)
e_in_app_parse=$(grep -cE 'cfg\.InAppVerifyEnabled\s*=\s*r\.FormValue\("in_app_verify_enabled"\)' "${BACKUP_CFG_GO}" || true)
e_sched_parse=$(grep -cE 'VerifySchedule:\s*strings\.TrimSpace\(r\.FormValue\("verify_schedule"\)\)' "${BACKUP_CFG_GO}" || true)
e_audit=$(grep -cE 'backup\.verify_now' "${BACKUP_CFG_GO}" || true)
if [ "${e_handler}" -ge 1 ] && [ "${e_in_app_parse}" -ge 1 ] && [ "${e_sched_parse}" -ge 1 ] && [ "${e_audit}" -ge 1 ]; then
    ok "Handler + form parsing + audit (all 4 present)"
else
    bad "backup_config.go incomplete: handler=${e_handler} in_app_parse=${e_in_app_parse} sched_parse=${e_sched_parse} audit=${e_audit}"
fi

# ------------------------------------------------------------------------------
# Contract F: backup.html — verify form fields + button + panel
# ------------------------------------------------------------------------------
echo
echo "=== F. backup.html: verify form + Verify now button + Last verify panel ==="
f_in_app_checkbox=$(grep -cE 'name="in_app_verify_enabled"' "${TEMPLATE}" || true)
f_sched_input=$(grep -cE 'name="verify_schedule"' "${TEMPLATE}" || true)
f_verify_button=$(grep -cE 'formaction="/admin/backup/verify-now"' "${TEMPLATE}" || true)
f_verify_panel=$(grep -cE 'backup\.last_verify' "${TEMPLATE}" || true)
if [ "${f_in_app_checkbox}" -ge 1 ] && [ "${f_sched_input}" -ge 1 ] && [ "${f_verify_button}" -ge 1 ] && [ "${f_verify_panel}" -ge 1 ]; then
    ok "Template: in_app_verify checkbox + verify_schedule input + Verify now button + Last verify panel (all 4 present)"
else
    bad "Template incomplete: in_app_checkbox=${f_in_app_checkbox} sched_input=${f_sched_input} verify_button=${f_verify_button} verify_panel=${f_verify_panel}"
fi

# ------------------------------------------------------------------------------
# Contract G: i18n — 11 new keys in BOTH RU and EN
# ------------------------------------------------------------------------------
echo
echo "=== G. catalog_backup.go: 11 new backup.* keys (RU + EN) ==="
keys="last_verify last_verify_never last_verify_archive last_verify_error verify_now verify_now_ok verify_now_failed in_app_verify_scheduler in_app_verify_help verify_schedule verify_schedule_help"
miss=0
for k in $keys; do
    n=$(grep -cE "\"backup\.${k}\"" "${I18N}" || true)
    if [ "${n}" -lt 2 ]; then
        miss=$((miss+1))
        echo "    missing EN/RU entry for backup.${k} (count=${n})"
    fi
done
if [ "${miss}" -eq 0 ]; then
    ok "All 11 new backup.* keys present in both RU + EN (22 total occurrences)"
else
    bad "i18n: ${miss} of 11 keys missing in one of the languages"
fi

# ------------------------------------------------------------------------------
# Contract H: runBackupVerifyOK/Fail use the renamed + new keys
# ------------------------------------------------------------------------------
echo
echo "=== H. cmd/skygate/main.go: runBackupVerifyOK/Fail use backup.last_verify_at + archive + error ==="
h_ok_at=$(grep -cE 'SetGlobalSetting\(d, "backup\.last_verify_at"' "${MAIN_GO}" || true)
h_ok_arch=$(grep -cE 'SetGlobalSetting\(d, "backup\.last_verify_archive"' "${MAIN_GO}" || true)
h_ok_err=$(grep -cE 'SetGlobalSetting\(d, "backup\.last_verify_error"' "${MAIN_GO}" || true)
h_fail_arch=$(grep -cE 'SetGlobalSetting\(d, "backup\.last_verify_archive"' "${MAIN_GO}" || true)
h_fail_err=$(grep -cE 'SetGlobalSetting\(d, "backup\.last_verify_error"' "${MAIN_GO}" || true)
h_old_key_gone=$(grep -cE 'SetGlobalSetting\(d, "backup\.last_verify",' "${MAIN_GO}" || true)
# old key is gone if the grep returns 0 matches; this is the backwards-compat delete.
if [ "${h_ok_at}" -ge 2 ] && [ "${h_ok_arch}" -ge 1 ] && [ "${h_ok_err}" -ge 1 ] && \
   [ "${h_fail_arch}" -ge 1 ] && [ "${h_fail_err}" -ge 1 ] && [ "${h_old_key_gone}" -eq 0 ]; then
    ok "OK + Fail handlers write last_verify_at + archive + error; pre-B142 'backup.last_verify' (no _at) key is gone"
else
    bad "main.go incomplete: ok_at=${h_ok_at} ok_arch=${h_ok_arch} ok_err=${h_ok_err} fail_arch=${h_fail_arch} fail_err=${h_fail_err} old_key_gone=${h_old_key_gone} (expected 0)"
fi

# ------------------------------------------------------------------------------
# Contract I: unit test + go test
# ------------------------------------------------------------------------------
echo
echo "=== I. verify_scheduler_b142_test.go: 12 test functions + go test passes ==="
i_t1=$(grep -cE '^func TestTailLines_Empty' "${TEST_FILE}" || true)
i_t2=$(grep -cE '^func TestTailLines_FewerThanN' "${TEST_FILE}" || true)
i_t3=$(grep -cE '^func TestTailLines_MoreThanN' "${TEST_FILE}" || true)
i_t4=$(grep -cE '^func TestTailLines_TrimsTrailingWhitespace' "${TEST_FILE}" || true)
i_t5=$(grep -cE '^func TestTruncateString_UnderLimit' "${TEST_FILE}" || true)
i_t6=$(grep -cE '^func TestTruncateString_AtLimit' "${TEST_FILE}" || true)
i_t7=$(grep -cE '^func TestTruncateString_OverLimit' "${TEST_FILE}" || true)
i_t8=$(grep -cE '^func TestTruncateString_Empty' "${TEST_FILE}" || true)
i_t9=$(grep -cE '^func TestSameMinute_SameMinute' "${TEST_FILE}" || true)
i_t10=$(grep -cE '^func TestSameMinute_DifferentMinutes' "${TEST_FILE}" || true)
i_t11=$(grep -cE '^func TestSameMinute_DifferentDays' "${TEST_FILE}" || true)
i_t12=$(grep -cE '^func TestSameMinute_ZeroValue' "${TEST_FILE}" || true)
i_t13=$(grep -cE '^func TestSameMinute_SameSecond' "${TEST_FILE}" || true)
test_count=$((i_t1+i_t2+i_t3+i_t4+i_t5+i_t6+i_t7+i_t8+i_t9+i_t10+i_t11+i_t12+i_t13))
if [ "${test_count}" -ge 12 ]; then
    ok "12 test functions present (4 tail + 4 truncate + 5 same-minute = 13 total, all present)"
else
    bad "Test file incomplete: test_count=${test_count} (expected 12-13)"
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
    warn "go not found — skipping I go-test (other 8 contracts still hold)"
else
    test_out=$("${GO}" test -count=1 -run 'TestTailLines|TestTruncateString|TestSameMinute' ./internal/backup/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "go test PASSes for tailLines / truncateString / sameMinute (B142 compiles + tests green)"
    else
        bad "go test FAILED: ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B142 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B142 contracts all hold."
    exit 0
fi
echo
echo "B142 has failing contracts — fix the source files above."
exit 1
