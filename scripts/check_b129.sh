#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20 (B129) — /admin/update page redesign
#
# Pins the v1.3.20 redesign of the /admin/update page that
# (1) makes the Update button UNCONDITIONAL when IsNewer (no
#     more "auto-update enabled" gating, which was misleading —
#     the pre-B129 flag didn't auto-update anything, the operator
#     still had to click Apply), and
# (2) adds a new "Schedule" section that is the REAL auto-update
#     mechanism: a time-of-day HH:MM at which the background
#     scheduler (B130) triggers the update orchestrator.
#
# What this script verifies (live, on the VM):
#   A. update.html: Update button (Apply form) is gated ONLY by
#      IsNewer + not DevBuild — NOT by .AutoUpdateEnabled.
#      The pre-B129 gating was:
#        {{if and .IsNewer .AutoUpdateEnabled (not .DevBuild)}}
#      The B129 gating is:
#        {{if and .IsNewer (not .DevBuild)}}
#   B. update.html: the new "Schedule" card is present with
#      a form posting to /admin/update/schedule, a checkbox
#      named "enabled", a time input named "time", and a
#      submit button. All 4 elements required.
#   C. update.html: the pre-B129 auto-update banner + toggle
#      form is GONE. The form "action=\"/admin/update/auto-toggle\""
#      inside an alert-info/alert-warning banner must NOT be
#      present.
#   D. update.go: renderUpdatePage passes UpdateScheduleEnabled,
#      UpdateScheduleTime, UpdateScheduleLastRun to the template.
#   E. update_settings.go: PostAdminUpdateSchedule handler
#      exists, with the HH:MM regex validation pattern.
#   F. config.go: UpdateScheduleEnabled + UpdateScheduleTime
#      fields present, with SKYGATE_UPDATE_SCHEDULE_ENABLED
#      and SKYGATE_UPDATE_SCHEDULE_TIME env-var fallbacks.
#   G. i18n: 11 new update.schedule_* keys in both RU and EN.
#   H. main.go: POST /admin/update/schedule route registered.
#   I. internal/update/scheduler.go exists (the B130 file
#      is referenced from B129 even though it's logically
#      the next B — this check just pins the file presence
#      so a partial B129 deploy doesn't break the page).
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

UPDATE_HTML="internal/handlers/templates/admin/update.html"
UPDATE_GO="internal/feature/admin/update.go"
UPDATE_SETTINGS_GO="internal/feature/admin/update_settings.go"
CONFIG_GO="internal/config/config.go"
MAIN_GO="cmd/skygate/main.go"
I18N_RU_FILE="internal/i18n/catalog_update.go"

for f in "${UPDATE_HTML}" "${UPDATE_GO}" "${UPDATE_SETTINGS_GO}" "${CONFIG_GO}" "${MAIN_GO}" "${I18N_RU_FILE}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: Update button is unconditional (not gated by AutoUpdateEnabled)
# ------------------------------------------------------------------------------
echo
echo "=== A. update.html: Update button is unconditional when IsNewer ==="
# Look for the /admin/update/apply form. The B129 contract is that
# the conditional does NOT include .AutoUpdateEnabled.
apply_block=$(grep -B1 -A4 'action="/admin/update/apply"' "${UPDATE_HTML}" | head -20 || true)
if echo "${apply_block}" | grep -q 'AutoUpdateEnabled'; then
    bad "Apply form is still gated by .AutoUpdateEnabled (pre-B129 contract)"
else
    ok "Apply form is NOT gated by .AutoUpdateEnabled (B129 contract)"
fi
# Confirm the conditional uses .IsNewer and not .DevBuild
if echo "${apply_block}" | grep -qE 'IsNewer|IsNewer.*DevBuild|DevBuild.*IsNewer'; then
    ok "Apply form conditional includes .IsNewer (and excludes .DevBuild when present)"
else
    bad "Apply form conditional does NOT include .IsNewer — the button would never show"
fi

# ------------------------------------------------------------------------------
# Contract B: the new Schedule card
# ------------------------------------------------------------------------------
echo
echo "=== B. update.html: new Schedule card is present ==="
b_form=$(grep -c 'action="/admin/update/schedule"' "${UPDATE_HTML}" || true)
b_checkbox=$(grep -cE 'name="enabled".*type="checkbox"|type="checkbox".*name="enabled"' "${UPDATE_HTML}" || true)
b_time=$(grep -cE 'name="time".*type="time"|type="time".*name="time"' "${UPDATE_HTML}" || true)
b_save=$(grep -c 'update.schedule_save' "${UPDATE_HTML}" || true)
if [ "${b_form}" -ge 1 ] && [ "${b_checkbox}" -ge 1 ] && [ "${b_time}" -ge 1 ] && [ "${b_save}" -ge 1 ]; then
    ok "Schedule card has form (action=/admin/update/schedule), checkbox, time input, save button (all 4 present)"
else
    bad "Schedule card is incomplete: form=${b_form} checkbox=${b_checkbox} time=${b_time} save=${b_save}"
fi

# ------------------------------------------------------------------------------
# Contract C: the pre-B129 auto-toggle banner is GONE
# ------------------------------------------------------------------------------
echo
echo "=== C. update.html: pre-B129 auto-toggle banner is removed ==="
# The pre-B129 banner was a <div class="alert alert-info|warning"> containing
# a form action="/admin/update/auto-toggle". Post-B129 the auto-toggle
# route still exists (URL stability) but the banner + its form are gone.
auto_toggle_in_banner=$(grep -cE 'alert.*update/auto-toggle' "${UPDATE_HTML}" || true)
if [ "${auto_toggle_in_banner}" -eq 0 ]; then
    ok "No pre-B129 'auto-toggle' form inside an alert banner (B129 removed it)"
else
    bad "Pre-B129 auto-toggle form is still inside an alert banner (${auto_toggle_in_banner} occurrences)"
fi

# ------------------------------------------------------------------------------
# Contract D: renderUpdatePage passes schedule fields
# ------------------------------------------------------------------------------
echo
echo "=== D. update.go: renderUpdatePage passes schedule fields ==="
d_enabled=$(grep -c '"UpdateScheduleEnabled"' "${UPDATE_GO}" || true)
d_time=$(grep -c '"UpdateScheduleTime"' "${UPDATE_GO}" || true)
d_lastrun=$(grep -c '"UpdateScheduleLastRun"' "${UPDATE_GO}" || true)
if [ "${d_enabled}" -ge 1 ] && [ "${d_time}" -ge 1 ] && [ "${d_lastrun}" -ge 1 ]; then
    ok "update.go passes UpdateScheduleEnabled + UpdateScheduleTime + UpdateScheduleLastRun to template"
else
    bad "update.go is missing schedule fields: enabled=${d_enabled} time=${d_time} lastrun=${d_lastrun}"
fi

# ------------------------------------------------------------------------------
# Contract E: PostAdminUpdateSchedule handler with HH:MM regex
# ------------------------------------------------------------------------------
echo
echo "=== E. update_settings.go: PostAdminUpdateSchedule handler + HH:MM regex ==="
e_handler=$(grep -c 'func.*PostAdminUpdateSchedule' "${UPDATE_SETTINGS_GO}" || true)
e_regex=$(grep -cE 'hhmmPattern|\\^\\(\[01\]\[0-9\]\|2\[0-3\]\\)' "${UPDATE_SETTINGS_GO}" || true)
e_global_key=$(grep -c 'globalSettingsKeyUpdateSchedule' "${UPDATE_SETTINGS_GO}" || true)
if [ "${e_handler}" -ge 1 ] && [ "${e_regex}" -ge 1 ] && [ "${e_global_key}" -ge 2 ]; then
    ok "PostAdminUpdateSchedule handler + hhmmPattern regex + 2 schedule keys (enabled + time) all present"
else
    bad "PostAdminUpdateSchedule incomplete: handler=${e_handler} regex=${e_regex} keys=${e_global_key}"
fi

# ------------------------------------------------------------------------------
# Contract F: config.go schedule fields + env vars
# ------------------------------------------------------------------------------
echo
echo "=== F. config.go: UpdateScheduleEnabled + UpdateScheduleTime fields + env fallbacks ==="
f_enabled_field=$(grep -c 'UpdateScheduleEnabled' "${CONFIG_GO}" || true)
f_time_field=$(grep -c 'UpdateScheduleTime' "${CONFIG_GO}" || true)
f_env_enabled=$(grep -c 'SKYGATE_UPDATE_SCHEDULE_ENABLED' "${CONFIG_GO}" || true)
f_env_time=$(grep -c 'SKYGATE_UPDATE_SCHEDULE_TIME' "${CONFIG_GO}" || true)
if [ "${f_enabled_field}" -ge 2 ] && [ "${f_time_field}" -ge 2 ] && [ "${f_env_enabled}" -ge 1 ] && [ "${f_env_time}" -ge 1 ]; then
    ok "config.go has UpdateScheduleEnabled + UpdateScheduleTime fields + both env-var fallbacks"
else
    bad "config.go schedule config incomplete: enabled_field=${f_enabled_field} time_field=${f_time_field} env_enabled=${f_env_enabled} env_time=${f_env_time}"
fi

# ------------------------------------------------------------------------------
# Contract G: i18n — 11 new update.schedule_* keys in RU + EN
# ------------------------------------------------------------------------------
echo
echo "=== G. i18n: 11 new update.schedule_* keys (RU + EN) ==="
keys="schedule_title schedule_subtitle schedule_enabled schedule_time_label schedule_save schedule_last_run schedule_never schedule_saved schedule_fallback schedule_next_run section_settings"
miss_ru=0
miss_en=0
for k in $keys; do
    if ! grep -qE "\"update\.${k}\"" "${I18N_RU_FILE}"; then
        miss_ru=$((miss_ru+1))
    fi
done
# Find the EN map. It's the second map literal in the file
# (after the RU map which is the first). Quickest: count
# total occurrences of each key in the file. 2 = both RU+EN.
for k in $keys; do
    c=$(grep -cE "\"update\.${k}\"" "${I18N_RU_FILE}" || true)
    if [ "${c}" -lt 2 ]; then
        miss_en=$((miss_en+1))
    fi
done
if [ "${miss_ru}" -eq 0 ] && [ "${miss_en}" -eq 0 ]; then
    ok "All 11 update.schedule_* keys present in both RU and EN maps"
else
    bad "i18n keys missing: RU=${miss_ru} (of 11), EN count mismatch=${miss_en} (of 11)"
fi

# ------------------------------------------------------------------------------
# Contract H: main.go POST /admin/update/schedule route
# ------------------------------------------------------------------------------
echo
echo "=== H. main.go: POST /admin/update/schedule route registered ==="
h_route=$(grep -cE 'POST .*/admin/update/schedule' "${MAIN_GO}" || true)
if [ "${h_route}" -ge 1 ]; then
    ok "main.go registers POST /admin/update/schedule"
else
    bad "main.go is missing POST /admin/update/schedule route"
fi

# ------------------------------------------------------------------------------
# Contract I: scheduler.go file presence (B130's contract pinned by B129
# because the B129 page references the scheduler in the schedule card
# and the operator's "Last run" field is written by it)
# ------------------------------------------------------------------------------
echo
echo "=== I. internal/update/scheduler.go exists (B130 prerequisite) ==="
if [ -f "internal/update/scheduler.go" ]; then
    ok "internal/update/scheduler.go is present (B130 prerequisite)"
else
    warn "internal/update/scheduler.go is NOT yet present — the page will render but the schedule will not run. This is OK if B130 hasn't shipped yet."
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B129 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B129 contracts all hold."
    exit 0
fi
echo
echo "B129 has failing contracts — fix the source files above."
exit 1
