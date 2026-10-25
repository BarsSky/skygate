#!/usr/bin/env bash
# diagnose_alerts.sh — READ-ONLY: why did (or didn't) a Telegram notification arrive?
#
# B422 (2026-10-25). Answers four questions without changing anything:
#   1. WHICH skygate is running here (docker container / systemd unit / nothing) and which
#      build it is.
#   2. WHO alerts would be delivered to — token present? telegram.chat_id set? an admin
#      binding? — printed so that no secret reaches the terminal (only the SHAPE and
#      lengths are shown).
#   3. WHAT was actually delivered (`telegram_alerts`) and what the log says about the
#      alert path (the v1.5.167+ boot line and every "ALERT NOT DELIVERED" line).
#   4. WHICH alert-worthy events HAPPENED (audit_log), which is what separates "no events"
#      from "events whose notifications were dropped".
#
# The distinction matters because the 2026-10-25 investigation found the second case: the
# reference install had a working bot, a bound ADMIN chat, and an EMPTY telegram.chat_id —
# and alerts (unlike replies and the "Send test" button) required chat_id, so every one of
# them was dropped with no log line for months.
#
# Safe to run repeatedly: every statement is a SELECT, every file is read. Nothing is
# started, stopped, restarted or written. No token is printed.
#
# Usage:  bash scripts/diagnose_alerts.sh [--since=30d]
set -uo pipefail

SINCE="30d"
for a in "$@"; do
  case "$a" in
    --since=*) SINCE="${a#--since=}" ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done

sec() { printf '\n\033[1m%s\033[0m\n' "$*"; }
row() { printf '  %-26s %s\n' "$1" "$2"; }
mask() { sed -E 's#(://[^:/]+:)[^@]*@#\1***@#g'; }
dk() { sudo -n docker "$@" 2>/dev/null; }

sec "1. what is installed and running"

CONT=""; IMG=""; UNIT=""
CONT="$(dk ps --format '{{.Names}}|{{.Image}}|{{.Status}}' | grep -i skygate | head -1)"
if [ -n "$CONT" ]; then
  row "container" "$(printf '%s' "$CONT" | tr '|' ' ')"
  CONT_NAME="${CONT%%|*}"
else
  for u in skygate skygate.service; do
    if systemctl list-unit-files --no-pager --no-legend 2>/dev/null | grep -q "^${u%.service}.service"; then
      UNIT="${u%.service}"; break
    fi
  done
  if [ -n "$UNIT" ]; then
    row "systemd unit" "$UNIT ($(systemctl is-active "$UNIT" 2>/dev/null), $(systemctl is-enabled "$UNIT" 2>/dev/null))"
  else
    row "runtime" "neither a skygate container nor a skygate unit was found"
  fi
fi
HEALTH=""
for url in http://127.0.0.1:8080/healthz http://127.0.0.1:8082/healthz; do
  b="$(curl -s -m 4 "$url" 2>/dev/null)"
  case "$b" in *build*) HEALTH="$url -> $(printf '%s' "$b" | head -c 220)"; break ;; esac
done
row "healthz" "${HEALTH:-unreachable on 8080/8082}"

sec "2. who alerts are delivered to (no secrets printed)"

DSN=""; PG_CONT=""
if [ -n "${CONT_NAME:-}" ]; then
  DSN="$(dk exec "$CONT_NAME" printenv SKYGATE_DB)"
fi
if [ -z "$DSN" ] && [ -r /etc/skygate/skygate.env ]; then
  DSN="$(grep -m1 -E '^SKYGATE_DB=' /etc/skygate/skygate.env 2>/dev/null | cut -d= -f2-)"
fi
case "$DSN" in
  postgres*|postgresql*) row "database" "postgres: $(printf '%s' "$DSN" | mask | head -c 90)" ;;
  sqlite*)               row "database" "sqlite: ${DSN#sqlite:}" ;;
  "")                    row "database" "not discoverable (env file unreadable and not in a container)" ;;
  *)                     row "database" "unknown DSN scheme" ;;
esac

# One query path per backend. For PostgreSQL we prefer a psql INSIDE the database
# container (it is the same server the app uses and needs no client on the host).
psql_like() { # psql_like <statement>
  if [ -n "$PG_CONT" ]; then
    local u p d
    u="$(printf '%s' "$DSN" | sed -E 's#^[a-z]+://([^:/@]+).*#\1#')"
    p="$(printf '%s' "$DSN" | sed -E 's#^[a-z]+://[^:]+:([^@]*)@.*#\1#')"
    d="$(printf '%s' "$DSN" | sed -E 's#^[a-z]+://[^/]+/([^?]+).*#\1#')"
    sudo -n docker exec -e "PGPASSWORD=$p" "$PG_CONT" psql -U "$u" -d "$d" -At -F'|' -c "$1" 2>/dev/null
  elif [ -n "${SQLITE:-}" ] && [ -r "${SQLITE#sqlite:}" ]; then
    local f="${SQLITE#sqlite:}"; f="${f%%\?*}"
    command -v sqlite3 >/dev/null 2>&1 && sqlite3 "$f" "$1" 2>/dev/null
  fi
}

case "$DSN" in
  postgres*|postgresql*)
    PG_CONT="$(dk ps --format '{{.Names}}' | grep -iE 'postgres|pg-' | head -1)"
    [ -n "$PG_CONT" ] || row "postgres container" "not found — set PG_CONT or run psql by hand"
    ;;
  sqlite*) SQLITE="$DSN" ;;
esac

q() { # q <statement> <label> [default label]
  local out
  out="$(psql_like "$1")"
  if [ -z "$out" ]; then
    row "$2" "${3:-unavailable (no client / DB not readable as $USER)}"
  else
    row "$2" "$(printf '%s' "$out" | tr '\n' ' ')"
  fi
}

q "SELECT CASE WHEN COALESCE((SELECT value FROM global_settings WHERE key='telegram.bot_token'),'')='' THEN 'ABSENT' ELSE 'present' END" "bot token"
q "SELECT COALESCE((SELECT value FROM global_settings WHERE key='telegram.chat_id'),'<<ABSENT>>')" "chat_id"
q "SELECT COUNT(*) FROM telegram_bindings" "bound chats"
q "SELECT COUNT(*) FROM telegram_bindings WHERE is_admin=1" "of them admin"
q "SELECT chat_id || ' user=' || portal_user_id || ' admin=' || is_admin FROM telegram_bindings WHERE is_admin=1 ORDER BY bound_at DESC LIMIT 3" "admin binding(s)"
q "SELECT COUNT(*) FROM telegram_alerts" "telegram_alerts rows"
q "SELECT id || ' @' || to_timestamp(sent_at) || ' ' || LEFT(replace(body, chr(10), ' '), 60) FROM telegram_alerts ORDER BY id DESC LIMIT 3" "last delivered" "unavailable"
q "SELECT COALESCE((SELECT value FROM global_settings WHERE key='monitor.disk.level'),'<<unset>>')" "disk level"
q "SELECT COALESCE((SELECT value FROM global_settings WHERE key='telegram.egress_node_id'),'<<unset>>')" "telegram egress relay id"

sec "3. what the log says about the alert path (last $SINCE)"

LOG=""
if [ -n "${CONT_NAME:-}" ]; then
  # --tail rather than --since: a container that was recreated (an update does exactly
  # that) has no older lines, and a huge --since window on a busy host returns megabytes
  # for a count nobody reads. The label says what the window really is.
  LOG="$(sudo -n docker logs --tail 5000 "$CONT_NAME" 2>&1)"
elif [ -n "$UNIT" ]; then
  LOG="$(journalctl -u "$UNIT" -n 5000 --no-pager 2>/dev/null)"
fi
SINCE="last 5000 lines"
if [ -z "$LOG" ]; then
  row "log" "nothing read (no container/unit access, or an empty window)"
else
  cnt() { printf '%s\n' "$LOG" | grep -c -- "$1"; }
  row "lines in window" "$(printf '%s\n' "$LOG" | wc -l)"
  row "alerts delivered" "$(cnt 'alert #')"
  row "ALERT NOT DELIVERED" "$(cnt 'ALERT NOT DELIVERED')"
  row "pre-1.5.167 drop path" "$(cnt 'no token and chat_id')"
  row "telegram errors" "$(printf '%s\n' "$LOG" | grep -ciE 'telegram:.*(error|failed|timeout)')"
  row "alert target at boot" "$(printf '%s\n' "$LOG" | grep -m1 'telegram: alerts' | cut -c1-120)"
  printf '\n  --- last 10 telegram lines ---\n'
  printf '%s\n' "$LOG" | grep -i 'telegram' | tail -10 | sed 's/^/    /'
fi

sec "4. did any alert-worthy event happen at all"

for a in tag.autoupdate_failed exit_node_provision exit_node_base_route_autorepair backup. certsync. update. ha. mesh. invite subnet_share; do
  q "SELECT COUNT(*) FROM audit_log WHERE action LIKE '${a}%'" "$a*"
done
q "SELECT COUNT(*) FROM audit_log WHERE action = 'cluster.discovery.error'" "cluster.discovery.error"
printf '\n  --- last 10 audit rows ---\n'
q "SELECT to_timestamp(created_at)::date || ' ' || action FROM audit_log ORDER BY id DESC LIMIT 10" "recent actions" "unavailable"

sec "verdict"
TOKEN="$(psql_like "SELECT COALESCE((SELECT value FROM global_settings WHERE key='telegram.bot_token'),'')")"
CHAT="$(psql_like "SELECT COALESCE((SELECT value FROM global_settings WHERE key='telegram.chat_id'),'')")"
ADMINB="$(psql_like "SELECT COUNT(*) FROM telegram_bindings WHERE is_admin=1")"
DELIVERED="$(psql_like "SELECT COUNT(*) FROM telegram_alerts")"
if [ -z "${TOKEN:-}" ] && [ -z "${CHAT:-}" ]; then
  echo "  The database could not be read (no client / no permission), so no verdict is possible."
  echo "  Re-run as a user who can reach the DB, or run the SQL from section 2 by hand."
elif [ -z "$TOKEN" ]; then
  echo "  No bot token in the database: there is no bot, so no alert could ever be sent."
elif [ -z "$CHAT" ] && [ "${ADMINB:-0}" = "0" ]; then
  echo "  Token present, NO recipient at all: telegram.chat_id is empty and no ADMIN chat is"
  echo "  bound. Pre-v1.5.167 every alert was dropped silently in this state."
  echo "  Fix: /admin/telegram -> save «ID чата», or /start + [Bind] from an admin account."
elif [ -z "$CHAT" ]; then
  echo "  Token present, telegram.chat_id EMPTY, but ${ADMINB} admin chat(s) are bound."
  echo "  Pre-v1.5.167 alerts required chat_id and were dropped silently while the manual"
  echo "  test still arrived through the bound chat — exactly the reported symptom."
  echo "  v1.5.167+ uses the admin binding as the recipient; section 3 should show"
  echo "  'telegram: alerts -> target=admin_binding ...'. Persisting chat_id is still better."
else
  echo "  Token and chat_id are both configured, so alerts have a recipient."
  echo "  If nothing arrived: section 3 names the failure ('ALERT NOT DELIVERED' = network,"
  echo "  HTTP 4xx or parse), and ${DELIVERED:-?} row(s) in telegram_alerts show what was sent."
fi
