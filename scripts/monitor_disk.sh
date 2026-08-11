#!/bin/bash
# scripts/monitor_disk.sh — disk space monitor for the skygate VM.
#
# Why: R30 (DB integrity check) FAILs in the verify catalog when
# the disk hits 100% full. SQLite's WAL writes fail silently
# at the syscall level when the disk has no free space, leaving
# btree pages in an inconsistent state. R31 (verify-post disk
# check) is the deploy-time signal; this cron is the around-the-
# clock signal.
#
# Usage (cron, run every 6h):
#   0 */6 * * * /usr/local/bin/monitor_disk.sh >> /var/log/skygate-disk-monitor.log 2>&1
#
# Or run interactively to see the current state:
#   bash scripts/monitor_disk.sh
#
# Thresholds:
#   75% — INFO log only, no alert
#   85% — WARN log + Telegram alert (matches R31 threshold)
#   95% — CRITICAL log + Telegram alert
#
# Telegram dispatch: this script uses the curl-friendly
# `SKYGATE_TELEGRAM_BOT_TOKEN` + `SKYGATE_TELEGRAM_CHAT_ID` env
# vars (same as `internal/telegram/notify.go` uses). If unset,
# the alerts are written to the log only.
#
# 2026-07-30: v0.32.5 — added after the disk-full → DB corruption
# incident. See docs/BACKLOG.md Priority 8.

set -e

THRESHOLD_WARN=85
THRESHOLD_CRIT=95
LOG_PREFIX="disk-monitor"
HOSTNAME=$(hostname)

# Get the disk usage percentage (whole number, no % sign)
DF_OUTPUT=$(df -P / | tail -1)
DF_PCT=$(echo "$DF_OUTPUT" | awk '{print $5}' | tr -d '%')
DF_AVAIL=$(echo "$DF_OUTPUT" | awk '{print $4}')

# Classify
LEVEL="ok"
if [ "$DF_PCT" -ge "$THRESHOLD_CRIT" ]; then
    LEVEL="critical"
elif [ "$DF_PCT" -ge "$THRESHOLD_WARN" ]; then
    LEVEL="warn"
fi

# Log
log_line() {
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $LOG_PREFIX [$LEVEL] disk=${DF_PCT}% avail=${DF_AVAIL}K $1"
}

if [ "$LEVEL" = "ok" ]; then
    log_line "ok (below ${THRESHOLD_WARN}% threshold)"
    exit 0
fi

# Build the alert message
SUBJECT="[$LEVEL] disk ${DF_PCT}% on ${HOSTNAME}"
BODY="Disk usage: ${DF_PCT}% (${DF_AVAIL}K available)
Threshold: ${THRESHOLD_WARN}% warn / ${THRESHOLD_CRIT}% critical
Top consumers:
$(sudo du -sh /var/* /home/* 2>/dev/null | sort -hr | head -5)
Action: run \`sudo docker system prune -a -f\` and
\`sudo rm -rf /var/backups/skygate/PRE_*\` to reclaim space.
If R30 fails after this, run \`bash scripts/recover_db_corruption.sh\`."

log_line "$BODY"

# Telegram alert (if env vars are set)
if [ -n "$SKYGATE_TELEGRAM_BOT_TOKEN" ] && [ -n "$SKYGATE_TELEGRAM_CHAT_ID" ]; then
    curl -fsS -X POST "https://api.telegram.org/bot${SKYGATE_TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d "chat_id=${SKYGATE_TELEGRAM_CHAT_ID}" \
        -d "text=${SUBJECT}
${BODY}" \
        -d "parse_mode=HTML" >/dev/null 2>&1 \
        && log_line "telegram alert sent" \
        || log_line "telegram alert failed (continuing)"
else
    log_line "telegram env not set, alert written to log only"
fi

# Critical: exit 1 so external monitoring (uptime checks, k8s liveness)
# can detect it. Warn: exit 0 (it's not yet an outage).
if [ "$LEVEL" = "critical" ]; then
    exit 1
fi
exit 0
