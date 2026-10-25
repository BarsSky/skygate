#!/bin/bash
# scripts/monitor_disk.sh — disk space DIAGNOSTIC (B391: the alert moved in-app).
#
# WHAT CHANGED AND WHY (2026-10-25, TD-34)
# ----------------------------------------
# This script used to be the around-the-clock disk alert, installed as a host cron
# (`/usr/local/bin/skygate-monitor-disk`, every 6 h). Measured on the reference host, it
# could not do the one thing it existed for:
#
#   * it dispatched Telegram only when SKYGATE_TELEGRAM_BOT_TOKEN and
#     SKYGATE_TELEGRAM_CHAT_ID were in the CRON environment, and they are NOT in `.env`
#     (the bot token lives in the database), so every run wrote to a log and told
#     nobody;
#   * even with the env set it could not deliver: the HOST cannot reach
#     api.telegram.org (curl → 000, relay names do not resolve), while the CONTAINER
#     sees the very same filesystem AND has the relay tunnel the notifier already uses.
#
# So the alert now belongs to the application: `internal/diskmon` checks the filesystem
# that holds the database every 10 minutes, classifies it with the SAME thresholds
# (85 % warn / 95 % critical), announces transitions (and a bounded repeat while a level
# holds), and sends them through the same notifier as every other alert — with an i18n
# title, a severity, escaping and a length policy. State is persisted in
# `global_settings` (monitor.disk.level / monitor.disk.last_alert_at), so a restart does
# not re-announce a level the operator already knows.
#
# THIS SCRIPT IS NOW A DIAGNOSTIC. It prints the current reading and the level, and it
# does NOT send anything unless you explicitly opt in for a host that cannot run skygate.
#
# Usage:
#   bash scripts/monitor_disk.sh                 # print the reading, no alert
#   SKYGATE_LEGACY_DISK_ALERT=1 bash scripts/monitor_disk.sh   # old behaviour (opt-in)
#
# ACTION FOR OPERATORS: remove the old cron line if you added one —
#   crontab -l | grep -v skygate-monitor-disk | crontab -
#
# 2026-07-30: v0.32.5 — added after the disk-full → DB corruption incident.
# 2026-10-25 (B391): reduced to a diagnostic; the alert moved into the application.

set -e

THRESHOLD_WARN=85
THRESHOLD_CRIT=95
LOG_PREFIX="disk-monitor"
HOSTNAME=$(hostname)

DF_OUTPUT=$(df -P / | tail -1)
# Parse the percentage by SHAPE, not by column: `df -P` is portable but its columns are
# not (MSYS/Git-Bash on Windows reorders them and reports the whole drive), and a
# diagnostic that prints "39385708%" is worse than one that admits it could not tell.
DF_PCT=$(echo "$DF_OUTPUT" | grep -o '[0-9][0-9]*%' | head -1 | tr -d '%')
DF_AVAIL=$(echo "$DF_OUTPUT" | awk '{print $4}')
if [ -z "$DF_PCT" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $LOG_PREFIX [unknown] could not parse df: $DF_OUTPUT"
    echo "  the application's own check (internal/diskmon) uses statfs and is unaffected."
    exit 0
fi

LEVEL="ok"
if [ "$DF_PCT" -ge "$THRESHOLD_CRIT" ]; then
    LEVEL="critical"
elif [ "$DF_PCT" -ge "$THRESHOLD_WARN" ]; then
    LEVEL="warn"
fi

echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $LOG_PREFIX [$LEVEL] disk=${DF_PCT}% avail=${DF_AVAIL}K"
echo "  the 24/7 alert is owned by the application (internal/diskmon, every 10 min)."
echo "  remove an old cron line with: crontab -l | grep -v skygate-monitor-disk | crontab -"

if [ "$LEVEL" = "ok" ]; then
    exit 0
fi

echo "  top consumers:"
sudo du -sh /var/* /home/* 2>/dev/null | sort -hr | head -5 || true
echo "  action: clear old backups/logs/images (docker system prune -a -f),"
echo "          then verify the database via /admin/system_tests (R30)."

# Opt-in only: for a host that runs skygate nowhere else. Requires the token and the
# chat_id in THIS environment (they are normally in the database, not in .env).
if [ "${SKYGATE_LEGACY_DISK_ALERT:-0}" = "1" ] &&
   [ -n "$SKYGATE_TELEGRAM_BOT_TOKEN" ] && [ -n "$SKYGATE_TELEGRAM_CHAT_ID" ]; then
    curl -fsS -X POST "https://api.telegram.org/bot${SKYGATE_TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d "chat_id=${SKYGATE_TELEGRAM_CHAT_ID}" \
        -d "text=disk ${DF_PCT}% on ${HOSTNAME} (${DF_AVAIL}K free) — thresholds ${THRESHOLD_WARN}%/${THRESHOLD_CRIT}%" \
        -d "parse_mode=HTML" >/dev/null 2>&1 \
        && echo "  legacy telegram alert sent" \
        || echo "  legacy telegram alert failed (continuing)"
fi

# Critical: exit 1 so external monitoring (uptime checks, k8s liveness) can detect it.
if [ "$LEVEL" = "critical" ]; then
    exit 1
fi
exit 0
