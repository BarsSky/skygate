#!/usr/bin/env bash
# check_b429_timezone.sh — B429 (2026-10-27)
#
# «НЕ УЧИТЫВАЕТСЯ ЛОКАЛИЗАЦИЯ ВРЕМЕНИ — РЕЛИЗ СДЕЛАН ОКОЛО 8 УТРА ПО МОСКВЕ, А В
#  УВЕДОМЛЕНИИ 5 УТРА ПО ГРИНВИЧУ — ВИДИМО ЭТО ОДНА ИЗ ПРИЧИН, ПОЧЕМУ НЕ
#  ОТРАБАТЫВАЕТ В НАЗНАЧЕННОЕ ВРЕМЯ ОБНОВЛЕНИЕ» — confirmed on the live install.
#
# Measured (2026-10-27, reference install):
#
#	host + container      /etc/localtime → Etc/UTC, SKYGATE_TZ absent
#	/admin/update         «Проверено: 2026-10-27 06:48:21 UTC»
#	update_schedule_time  08:00   ← compared against UTC, i.e. 11:00 Moscow
#	Telegram alert        «опубликована: 2026-10-27 05:12 UTC» for a release
#	                      published at 08:12 Moscow
#
# So the arithmetic was never wrong; the INSTALL had no way to say which clock
# the operator reads. Two consequences the operator named himself: the notification
# was unreadable, and the 08:00 schedule fired at 11:00 — the feature looked
# broken while behaving exactly as written.
#
# The fix is one clock for the whole panel, chosen by the admin, auto-detected by
# default, and used by every surface that renders or compares a time.
#
# CONTRACTS (31)
#   A  internal/localtime is the ONE clock: explicit > env > system, typo refused
#   B  the process installs it at boot (admin setting → SKYGATE_TIMEZONE → auto)
#   C  the update SCHEDULE is read in that clock (the operator's complaint #2)
#   D  every panel timestamp is rendered in it (the operator's complaint #1)
#   E  the admin can choose it, and the choice wins
#   F  the ratchet: no new server-zone formatting, and the machine-format
#      exceptions are named
#   G  registration / index / tracked

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
. "$(dirname "$0")/lib/gosurface.sh"
# B339: the boot sequence is a PACKAGE surface, not one file — reading
# cmd/skygate/main.go as a file is exactly what B372/B339 forbid (a boot line can
# move to main_helpers.go, as installPanelTimezone did).
gosurface SKY_MAIN cmd/skygate/*.go
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

TZ_PKG=internal/localtime/localtime.go
TZ_TEST=internal/localtime/localtime_b429_test.go
BOOT=cmd/skygate/main_helpers.go
SCHED=internal/update/scheduler.go
FUNCS=internal/handlers/templates.go
SETTINGS=internal/feature/admin/backup.go
SETTINGS_TPL=internal/handlers/templates/admin/settings.html
ALERT=internal/release/alert_b426.go
GATE=scripts/verify_pre_deploy.sh
for f in "$TZ_PKG" "$TZ_TEST" "$BOOT" "$SCHED" "$FUNCS" "$SETTINGS" "$SETTINGS_TPL" "$ALERT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. one clock, with the priority the operator asked for"
for probe in 'func Load(value string)' 'func Set(value string)' 'func Current() *time.Location' 'func Now() time.Time' 'func DetectSystem() string'; do
  grep -qF "$probe" "$TZ_PKG" && ok "A1: $probe" || bad "A1: missing $probe"
done
if grep -qF 'an unknown zone %q (use an IANA name' "$TZ_PKG" || grep -qF 'unknown time zone %q' "$TZ_PKG"; then
  ok "A2: a typo is an ERROR naming the value — never a silent UTC fallback"
else
  bad "A2: an unknown zone does not name itself (a typo could move every timestamp silently)"
fi
if grep -qF 'os.Getenv("TZ")' "$TZ_PKG" && grep -qF '/etc/localtime' "$TZ_PKG"; then
  ok "A3: the system zone is detected the way the C library does it (TZ → /etc/localtime)"
else
  bad "A3: the system zone is not detected"
fi
if OUT="$(go test ./internal/localtime/ -count=1 2>&1)"; then
  ok "A4: the behavioural tests pass (explicit zone wins and carries its name, typo refused, auto follows the system, DST is the zone's problem, unix/zero contract)"
else
  bad "A4: internal/localtime tests failed: $(printf '%s' "$OUT" | grep -E '^(---|FAIL)' | head -3 | tr '\n' ' ')"
fi

hdr "B. the process installs it at boot"
grep -qF 'func installPanelTimezone(' "$BOOT" && ok "B1: installPanelTimezone lives in the boot helper file (B372 keeps main.go under its ceiling)" || bad "B1: no installPanelTimezone"
if grep -qF 'installPanelTimezone(d, cfg)' "$SKY_MAIN"; then
  ok "B2: main() installs the clock before any renderer or scheduler asks for a time"
else
  bad "B2: main() does not install the clock"
fi
if grep -qF 'localtime.SettingKey, cfg.Timezone' "$BOOT" && grep -qF 'cfg.Timezone' "$BOOT"; then
  ok "B3: priority is admin setting (global_settings) → SKYGATE_TIMEZONE → auto-detect"
else
  bad "B3: the boot priority is not admin-setting → env → detected"
fi
if grep -qF 'SKYGATE_TIMEZONE' internal/config/config.go; then
  ok "B4: SKYGATE_TIMEZONE is the deploy-time knob"
else
  bad "B4: no SKYGATE_TIMEZONE"
fi
if grep -qF 'timezone: %s (%s)' "$BOOT"; then
  ok "B5: the boot line names the resolved zone AND where it came from (a screenshot of the journal answers this question forever)"
else
  bad "B5: the boot does not say which clock it chose"
fi

hdr "C. the update schedule is read in that clock"
if grep -qF 'now := localtime.Now()' "$SCHED"; then
  ok "C1: the scheduler's 'now' is the install's clock, so 08:00 means 08:00 to the operator"
else
  bad "C1: the scheduler still compares against the server clock"
fi
if grep -qF 'ranTodayAt(timeStr, localtime.In(lastRun), now)' "$SCHED"; then
  ok "C2: the last-run stamp is converted into the same zone before the day comparison (a mixed-zone comparison silently re-fires or never fires)"
else
  bad "C2: lastRun is compared without conversion"
fi
if grep -qF 'localtime.Name()' "$SCHED" && grep -qF 'localtime.FormatClock(now)' "$SCHED"; then
  ok "C3: the firing log line carries the zone, so 'firing at 08:00' is no longer ambiguous"
else
  bad "C3: the firing log line does not name the zone"
fi
if grep -qF 'localtime.In(now).Format("15:04")' internal/update/scheduler_b375_test.go; then
  ok "C4: the B375 tests compute the schedule minute from the SAME clock the scheduler reads (they were written against the workstation's local time)"
else
  bad "C4: the scheduler tests still assume the server's local clock"
fi

hdr "D. every panel timestamp is rendered in it"
if grep -qF '"datetimeformat": localtime.FormatUnix' "$FUNCS"; then
  ok "D1: datetimeformat (the unix formatter most pages use) renders in the installed zone, not UTC"
else
  bad "D1: datetimeformat still hardcodes a zone"
fi
if grep -qF '"dt":  localtime.Format' "$FUNCS" && grep -qF '"dts": localtime.FormatSeconds' "$FUNCS" && grep -qF '"dtd": localtime.FormatDate' "$FUNCS"; then
  ok "D2: dt / dts / dtd are registered as the ONE way a template renders a time.Time"
else
  bad "D2: the template time helpers are missing"
fi
REMAINING="$(grep -rn '\.Format ' internal/handlers/templates/ | wc -l | tr -d ' ')"
if [ "$REMAINING" = "0" ]; then
  ok "D3: NO template formats a time by hand any more (0 of the 27 pre-B429 sites remain)"
else
  bad "D3: $REMAINING template(s) still call .Format inline — they render in the server's zone"
fi
if grep -qF 'localtime.Format(ts)' "$ALERT"; then
  ok "D4: the update notification's 'released' fact renders in the installed zone (it said 05:12 UTC for a 08:12 Moscow release)"
else
  bad "D4: the alert formatter still hardcodes UTC"
fi
if grep -qF 'data-update-phase' internal/handlers/templates/admin/update.html; then
  ok "D5: the update page's clock is the same one (checked_at / log lines go through dts/dtclock)"
else
  bad "D5: the update page lost its status block"
fi

hdr "E. the admin chooses it, and the choice wins"
if grep -qF 'name="timezone"' "$SETTINGS_TPL" && grep -qF 'list="timezone-choices"' "$SETTINGS_TPL"; then
  ok "E1: /admin/settings carries the time-zone field (a datalist: curated suggestions, any IANA name accepted)"
else
  bad "E1: there is no time-zone control on the settings page"
fi
if grep -qF 'localtime.Set(value)' "$SETTINGS" && grep -qF 'localtime.SettingKey' "$SETTINGS"; then
  ok "E2: the POST installs the zone in the running process immediately (not only after a restart) and persists it"
else
  bad "E2: saving the setting does not take effect"
fi
if grep -qF 'settings.timezone' "$SETTINGS" && grep -qF 'urlSafe("time zone refused: "' "$SETTINGS"; then
  ok "E3: a refused zone is reported on the page with the reason and the zone actually in force"
else
  bad "E3: a bad zone is not reported"
fi
pairs=0
for k in settings.section_timezone settings.timezone settings.timezone_help settings.timezone_resolved settings.timezone_detected settings.timezone_now; do
  c=$(grep -c "\"$k\"" internal/i18n/catalog_admin.go); [ "$c" -ge 2 ] || bad "E4: $k missing in one language"; [ "$c" -ge 2 ] && pairs=$((pairs+1))
done
[ "$pairs" -eq 6 ] && ok "E4: all six new keys exist in RU and EN"

hdr "F. the ratchet: no new server-zone formatting"
# Machine formats are deliberately NOT localized: an audit row's RFC3339 timestamp,
# an SSE event's timestamp and a filename suffix must not move with the operator's
# clock. Every other raw layout is a regression.
ALLOWED="internal/deployrun/steps/audit_log.go internal/deployrun/sse.go internal/feature/admin/acl_import.go"
STRAY=""
while IFS= read -r line; do
  [ -z "$line" ] && continue
  file="${line%%:*}"
  case "$file" in
    internal/localtime/*) continue ;;
  esac
  allowed=no
  for a in $ALLOWED; do [ "$file" = "$a" ] && allowed=yes; done
  [ "$allowed" = "no" ] && STRAY="$STRAY $file:${line#*:}"
done <<< "$(grep -rn 'Format("2006-' internal cmd --include='*.go' | grep -v '_test.go' | grep -v '// ')"
if [ -z "$STRAY" ]; then
  ok "F1: no production file formats a time by hand outside localtime and the three machine-format exceptions"
else
  bad "F1: raw time formatting outside the allowlist:$STRAY"
fi
if grep -qF 'FormatUnix(' "$TZ_PKG" && grep -qF '2006-01-02 15:04 MST' "$TZ_PKG"; then
  ok "F2: every rendered timestamp carries the zone name (a screenshot can never be misread again)"
else
  bad "F2: the rendered format does not carry the zone"
fi

hdr "G. registration / index / tracked"
grep -q 'run_check "B429"' "$GATE" && ok "G1: registered in the catalog" || bad "G1: not registered"
grep -q '\*\*B429\*\*' AGENTS.md && ok "G2: AGENTS.md block index carries B429" || bad "G2: no AGENTS entry"
if git ls-files --error-unmatch scripts/check_b429_timezone.sh >/dev/null 2>&1; then
  ok "G3: this script is tracked by git (trap 11)"
else
  bad "G3: this script is not tracked by git yet — a check nobody can run is not a check"
fi

printf '\n\033[1mB429 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
