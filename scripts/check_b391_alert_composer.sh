#!/usr/bin/env bash
# check_b391_alert_composer.sh — B391 (2026-10-25, TD-34)
#
# ONE i18n-AWARE, RICH-CAPABLE ALERT COMPOSER — AND THE DISK SIGNAL IN THE PROCESS
# THAT CAN DELIVER IT.
#
# Measured before this block (TD-34): all 61 outgoing triggers built their text with
# `fmt.Sprintf` at the call site, in English only, and sent it through the REPLY
# transport, which sets no parse_mode and wraps the body in a ``` fence — so an alert
# that quoted a command arrived with the backticks showing, and a body over Telegram's
# 4096-character limit was refused outright and LOST. Separately, the around-the-clock
# disk check was a host cron that could not deliver at all: it needed the bot token in
# the cron environment (the token lives in the DB) and the host cannot reach
# api.telegram.org, while the container sees the same filesystem and has the relay
# tunnel.
#
# CONTRACTS (24)
#   A  the composer exists, owns the severity vocabulary, escapes, and bounds the body
#   B  the alert transport sets parse_mode=HTML and does NOT use the ``` fence
#   C  the Notifier interface carries the composed path (interface, Real, Noop)
#   D  i18n: the envelope + disk keys exist in BOTH catalogues
#   E  the disk monitor: pure decision half, unix+windows usage, persisted state
#   F  it is wired into the schedulers with named knobs
#   G  the retired cron: no delivery unless the operator opts in, and the script is a
#      diagnostic that never fabricates a percentage
#   H  the translation RATCHET: raw-English alert sites may only decrease
#   I  the behavioural tests pass
#   J  registered, indexed, tracked

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b391.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

COMPOSER=internal/telegram/alert_compose.go
ALERTS=internal/telegram/alerts.go
NOTIFY=internal/telegram/notify.go
DISK=internal/diskmon/disk_monitor.go
DISK_UNIX=internal/diskmon/disk_usage_unix.go
DISK_WIN=internal/diskmon/disk_usage_windows.go
SCHED=cmd/skygate/main_schedulers.go
CRON=scripts/monitor_disk.sh
CATALOG=internal/i18n/catalog_bot.go
TELEGRAM_DOC=docs/TELEGRAM.md
GATE=scripts/verify_pre_deploy.sh

for f in "$COMPOSER" "$ALERTS" "$NOTIFY" "$DISK" "$DISK_UNIX" "$DISK_WIN" \
         "$SCHED" "$CRON" "$CATALOG" "$TELEGRAM_DOC" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# strip_go_comments removes // and /* */ so an assertion never matches the banner that
# explains it (the B418/B390 lesson).
strip_go_comments() {
  awk 'BEGIN{d=0}
       { s=$0; out=""
         while (1) {
           if (d) { p=index(s,"*/"); if (p==0) { s=""; break } s=substr(s,p+2); d=0 }
           else   { p=index(s,"/*"); if (p==0) { out=out s; break }
                    out=out substr(s,1,p-1); s=substr(s,p+2); d=1 }
         }
         sub(/\/\/.*$/, "", out)
         print out }' "$1"
}

# --- A: the composer --------------------------------------------------------
hdr "A. one composer owns the alert vocabulary"

strip_go_comments "$COMPOSER" > "$SKY_TMP/composer.go"
for sev in 'SeverityInfo' 'SeverityWarn' 'SeverityCritical' 'SeverityRecovery'; do
  if ! grep -qF "$sev" "$SKY_TMP/composer.go"; then
    bad "A1: the severity vocabulary lost \`$sev\` — an alert that cannot say HOW BAD cannot be triaged"
  fi
done
grep -qF 'SeverityInfo' "$SKY_TMP/composer.go" &&
  ok "A1: the severity vocabulary exists (info / warn / critical / recovery)"
if grep -qF 'func ComposeAlert(' "$SKY_TMP/composer.go" &&
   grep -qF 'func ComposeAlertPlain(' "$SKY_TMP/composer.go"; then
  ok "A2: the composer renders both the HTML body and the plain fallback (/ack, logs)"
else
  bad "A2: the composer is missing one of ComposeAlert / ComposeAlertPlain"
fi
if grep -qF 'html.EscapeString(' "$SKY_TMP/composer.go"; then
  ok "A3: caller-supplied strings are escaped (a hostname with '<' cannot be rejected by the API)"
else
  bad "A3: the composer interpolates raw values — Telegram answers 400 and the alert is lost"
fi
if grep -qF 'maxAlertBody' "$SKY_TMP/composer.go" &&
   grep -qF 'alert.truncated' "$SKY_TMP/composer.go"; then
  ok "A4: the body is bounded and a truncated alert SAYS it was truncated"
else
  bad "A4: no length policy — a long alert is refused by the API (the pre-B391 loss)"
fi

# --- B: the transport -------------------------------------------------------
hdr "B. alerts go out as HTML, never as a fenced block"

strip_go_comments "$ALERTS" > "$SKY_TMP/alerts.go"
strip_go_comments "$NOTIFY" > "$SKY_TMP/notify.go"
if grep -qF 'sendHTMLToChat(' "$SKY_TMP/alerts.go"; then
  ok "B1: the alert path uses its own HTML transport"
else
  bad "B1: the alert path does not use an HTML transport"
fi
if grep -qF 'sendHTMLToChat(' "$SKY_TMP/notify.go" &&
   grep -qF 'postMessageErr(token, chatID, htmlBody, "HTML")' "$SKY_TMP/notify.go"; then
  ok "B2: that transport sets parse_mode=HTML (the pre-B391 path set no parse mode at all)"
else
  bad "B2: the alert transport does not set parse_mode=HTML"
fi
if grep -q 'n\.SendTelegram(' "$SKY_TMP/alerts.go"; then
  bad "B3: an alert is still sent through the reply transport (triple-backtick fence, no parse_mode)"
else
  ok "B3: no alert goes through the fenced reply transport"
fi
if grep -qF 'sanitizeTelegramHTML(' "$SKY_TMP/notify.go"; then
  ok "B4: the HTML body is sanitised at the wire (B389's helper), so a stray tag cannot drop the message"
else
  bad "B4: the wire no longer sanitises HTML bodies"
fi

# --- C: the interface -------------------------------------------------------
hdr "C. the composed path is part of the Notifier contract"

if grep -qF 'SendAlertHTML(htmlBody, plain string) int64' "$SKY_TMP/notify.go" &&
   grep -qF 'SendAlertEvent(a Alert) int64' "$SKY_TMP/notify.go"; then
  ok "C1: the Notifier interface carries SendAlertHTML + SendAlertEvent"
else
  bad "C1: the interface does not expose the composed alert path"
fi
if grep -qF 'func (NoopNotifier) SendAlertHTML(' "$SKY_TMP/alerts.go" &&
   grep -qF 'func (NoopNotifier) SendAlertEvent(' "$SKY_TMP/alerts.go"; then
  ok "C2: the no-op notifier implements them (a deployment without a bot still compiles)"
else
  bad "C2: NoopNotifier does not implement the composed path"
fi
if grep -qF 'shouldSuppressAlert(' "$SKY_TMP/alerts.go"; then
  ok "C3: a duplicate alert inside its window is suppressed (a flapping relay cannot flood the chat)"
else
  bad "C3: no dedup — every tick of a flapping component sends again"
fi

# --- D: i18n ---------------------------------------------------------------
hdr "D. the envelope and the disk alert speak both languages"

d_missing=0
for key in alert.untitled alert.truncated alert.disk.title_warn alert.disk.title_critical \
           alert.disk.title_recovered alert.disk.fact_path alert.disk.fact_usage \
           alert.disk.fact_free alert.disk.fact_thresholds alert.disk.action \
           alert.disk.read_failed alert.disk.repeat; do
  n=$(grep -c "\"$key\":" "$CATALOG")
  [ "$n" -ge 2 ] || { bad "D1: $key is not defined in BOTH catalogues (found $n)"; d_missing=1; }
done
[ "$d_missing" -eq 0 ] && ok "D1: every alert key exists in RU and EN"

# --- E: the disk monitor ----------------------------------------------------
hdr "E. the disk check is a decision, not a script"

strip_go_comments "$DISK" > "$SKY_TMP/disk.go"
if grep -qF 'func ClassifyDisk(' "$SKY_TMP/disk.go" &&
   grep -qF 'func nextDiskAction(' "$SKY_TMP/disk.go"; then
  ok "E1: the decision half is pure and testable (classify + what to say)"
else
  bad "E1: the disk decision is not a pure function — it cannot be tested without a full disk"
fi
if grep -qF 'DiskLevelWarn' "$SKY_TMP/disk.go" && grep -qF 'DiskLevelCritical' "$SKY_TMP/disk.go"; then
  ok "E2: the two levels the retired cron used (85 % / 95 %) are the vocabulary"
else
  bad "E2: the level vocabulary is gone"
fi
if grep -qF 'monitor.disk.level' "$SKY_TMP/disk.go" &&
   grep -qF 'monitor.disk.last_alert_at' "$SKY_TMP/disk.go"; then
  ok "E3: the level and the last alert are persisted (a restart does not re-announce)"
else
  bad "E3: the state is not persisted — every restart would repeat the alert"
fi
if grep -qF 'Bavail' "$DISK_UNIX"; then
  ok "E4: the unix collector reads Bavail (the root reserve excluded), not Bfree"
else
  bad "E4: the unix collector does not read Bavail — the number would not predict the next failed write"
fi
if grep -qF 'ErrDiskUsageUnsupported' "$DISK_WIN"; then
  ok "E5: the windows stub reports a NAMED limitation instead of a fabricated percentage"
else
  bad "E5: the windows stub invents a number"
fi

# --- F: the wiring ---------------------------------------------------------
hdr "F. the app owns the schedule"

strip_go_comments "$SCHED" > "$SKY_TMP/sched.go"
if grep -qF 'diskmon.NewDiskMonitor(' "$SKY_TMP/sched.go" &&
   grep -qF '.Start(ctx)' "$SKY_TMP/sched.go"; then
  ok "F1: the disk monitor is started by the binary"
else
  bad "F1: nothing starts the disk monitor — the signal would be dead code"
fi
for knob in SKYGATE_DISK_WARN_PCT SKYGATE_DISK_CRIT_PCT SKYGATE_DISK_MONITOR_INTERVAL_SEC; do
  if ! grep -qF "$knob" "$SKY_TMP/sched.go"; then
    bad "F2: the knob $knob is not read — the operator cannot tune the thresholds"
  fi
done
grep -qF 'SKYGATE_DISK_WARN_PCT' "$SKY_TMP/sched.go" &&
  ok "F2: the thresholds and the interval are operator-tunable env knobs"

# --- G: the retired cron ---------------------------------------------------
hdr "G. the host script is a diagnostic, not a second alert path"

if grep -qF 'SKYGATE_LEGACY_DISK_ALERT' "$CRON"; then
  ok "G1: the legacy Telegram dispatch is opt-in (two alert paths must not race)"
else
  bad "G1: the host script still dispatches unconditionally — a second, undeliverable alert path"
fi
if grep -q 'grep -o .\[0-9\]\[0-9\]\*%.' "$CRON"; then
  ok "G2: the diagnostic parses df by SHAPE (a wrong column printed a nonsense percentage)"
else
  bad "G2: the diagnostic trusts a df column index — it printed 39385708% on MSYS"
fi
if grep -q 'internal/diskmon' "$CRON" && grep -q 'internal/diskmon' "$TELEGRAM_DOC"; then
  ok "G3: the script and the docs both point at the in-app monitor"
else
  bad "G3: the operator is not told where the check moved"
fi

# --- H: the translation ratchet --------------------------------------------
hdr "H. the English-only alert sites may only decrease"

# B391 froze the debt at the number measured when the composer landed. The composer
# exists so that number can fall without touching 34 handlers in one release; the
# ratchet is what makes "we will do it later" visible instead of forgotten.
RATCHET=34
sites=$(grep -rho 'SendAlert(fmt\.Sprint[a-z]*(' --include='*.go' internal cmd 2>/dev/null \
        | wc -l | tr -d ' ')
if [ "$sites" -le "$RATCHET" ]; then
  ok "H1: raw-English alert sites = $sites (ratchet $RATCHET; may only go down)"
else
  bad "H1: raw-English alert sites = $sites, above the ratchet $RATCHET — a new alert bypassed the composer"
fi

# --- I: the behavioural tests ----------------------------------------------
hdr "I. the behavioural tests"

if OUT="$(go test ./internal/telegram/ ./internal/diskmon/ -run B391 -count=1 2>&1)"; then
  ok "I1: composer + transport + dedup + disk transitions pass"
else
  bad "I1: go test -run B391 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi

# --- J: registration -------------------------------------------------------
hdr "J. registration"

if grep -q 'run_check "B391"' "$GATE"; then
  ok "J1: B391 is registered in the catalog"
else
  bad "J1: B391 is not registered — this contract would never run"
fi
if grep -q '\*\*B391\*\*' AGENTS.md; then
  ok "J2: AGENTS.md block index carries B391"
else
  bad "J2: AGENTS.md has no B391 entry"
fi
if git ls-files --error-unmatch scripts/check_b391_alert_composer.sh >/dev/null 2>&1; then
  ok "J3: this script is tracked by git (trap 11)"
else
  bad "J3: this script is not tracked by git"
fi

printf '\n\033[1mB391 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
