#!/usr/bin/env bash
#===============================================================================
# Skygate Notify — Telegram delivery for backup/test/upgrade events
#
# Reads secrets from /home/skyadmin/skygate/.env (TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID,
# optional TELEGRAM_API).
# If unset → silent fallback to local log at /home/skyadmin/.skygate-notify.log.
#
# Usage:
#   notify.sh "subject" "message body"
#   notify.sh --severity=ok|warn|fail "subject" "body"
#   notify.sh --dry-run ...
#   notify.sh --test     # sends a heartbeat message
#   echo "long body" | notify.sh --stdin "subject"
#===============================================================================
set -euo pipefail

SKYGATE_DIR="${SKYGATE_DIR:-/home/skyadmin/skygate}"
ENV_FILE="${ENV_FILE:-${SKYGATE_DIR}/.env}"
# Log goes to caller's home if writable; else /tmp/skygate-notify.log fallback.
LOG_FILE="${LOG_FILE:-}"
if [[ -z "${LOG_FILE}" ]]; then
  if [[ -n "${HOME}" && -d "${HOME}" ]]; then
    LOG_FILE="${HOME}/.skygate-notify.log"
  else
    LOG_FILE="/tmp/skygate-notify.log"
  fi
fi
API_DEFAULT="https://api.telegram.org"
TIMEOUT="${NOTIFY_TIMEOUT:-10}"

SEVERITY="ok"
SUBJECT=""
BODY=""
DRY_RUN=0
TEST=0
STDIN=0

# --- arg parse (long-only, order-tolerant) ---
ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --severity=*) SEVERITY="${1#*=}" ;;
    --dry-run)    DRY_RUN=1 ;;
    --test)       TEST=1 ;;
    --stdin)      STDIN=1 ;;
    --log-file=*) LOG_FILE="${1#*=}" ;;
    --env-file=*) ENV_FILE="${1#*=}" ;;
    --help|-h)
      sed -n '2,20p' "$0"; exit 0 ;;
    *) ARGS+=("$1") ;;
  esac
  shift
done

[[ "${TEST}" -eq 1 ]] && { SUBJECT="skygate-notify test"; BODY="heartbeat from $(hostname) at $(date -u +%FT%TZ)"; }
[[ "${STDIN}" -eq 1 ]] && BODY="$(cat)" || true
SUBJECT="${ARGS[0]:-${SUBJECT}}"
BODY="${ARGS[1]:-${BODY}}"
[[ -z "${SUBJECT}" ]] && { echo "usage: notify.sh [--severity=ok|warn|fail] [--dry-run] [--test] [--stdin] 'subject' 'body'" >&2; exit 2; }

# Emoji per severity
case "${SEVERITY}" in
  ok)   ICON="✅" ;;
  warn) ICON="⚠️ " ;;
  fail) ICON="❌" ;;
  *)    ICON="•" ;;
esac

# Compose message — Telegram Markdown is fragile, send plain text with severity prefix.
TIMESTAMP="$(date -u +%FT%TZ)"
HOST="$(hostname -f 2>/dev/null || hostname)"
MSG="${ICON} [${HOST}] ${SUBJECT}
${TIMESTAMP}  severity=${SEVERITY}
${BODY}"

# --- Load .env keys (don't fail if file missing) ---
load_env_kv() {
  local key="$1"
  if [[ -r "${ENV_FILE}" ]]; then
    # shellcheck disable=SC1090
    ( set -a; source "${ENV_FILE}"; set +a; printf '%s' "$(printenv "${key}" 2>/dev/null || true)" )
  fi
}
TG_BOT_TOKEN="$(load_env_kv TELEGRAM_BOT_TOKEN)"
TG_CHAT_ID="$(load_env_kv TELEGRAM_CHAT_ID)"
TG_API="$(load_env_kv TELEGRAM_API)"
TG_API="${TG_API:-${API_DEFAULT}}"

# --- Local fallback log ---
log_local() {
  mkdir -p "$(dirname "${LOG_FILE}")" 2>/dev/null || true
  printf '%s\n---\n' "${MSG}" >> "${LOG_FILE}" 2>/dev/null || true
}

# Dry-run: print what would be sent, regardless of secret config.
if [[ "${DRY_RUN}" -eq 1 ]]; then
  if [[ -z "${TG_BOT_TOKEN}" || -z "${TG_CHAT_ID}" ]]; then
    echo "DRY-RUN: secrets missing → would fall back to log: ${LOG_FILE}"
  else
    echo "DRY-RUN would POST to ${TG_API%/}/bot${TG_BOT_TOKEN//?/<redacted>}/sendMessage (chat=${TG_CHAT_ID})"
  fi
  echo "${MSG}"
  exit 0
fi

# --- If disabled, log + return ok ---
if [[ -z "${TG_BOT_TOKEN}" || -z "${TG_CHAT_ID}" ]]; then
  log_local
  echo "notify: silent (no TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID). logged → ${LOG_FILE}" >&2
  exit 0
fi

URL="${TG_API%/}/bot${TG_BOT_TOKEN}/sendMessage"

# Truncate to ~3500 chars to fit Telegram limits comfortably
if (( ${#MSG} > 3500 )); then
  MSG="${MSG:0:3400}
… [truncated]"
fi

# --- Send ---
RESP="$(mktemp)"
HTTP_CODE="$(curl -sS --max-time "${TIMEOUT}" \
  -o "${RESP}" -w '%{http_code}' \
  -X POST "${URL}" \
  -H 'Content-Type: application/json' \
  --data-binary "$(printf '{"chat_id":"%s","text":%s,"disable_web_page_preview":true}' \
      "${TG_CHAT_ID}" \
      "$(printf '%s' "${MSG}" | jq -Rsa .)")" 2>&1 || echo "000")" || true

# Append local log regardless
log_local

if [[ "${HTTP_CODE}" =~ ^2 ]]; then
  echo "notify: ${HTTP_CODE} sent"
  rm -f "${RESP}"
  exit 0
fi

echo "notify: HTTP ${HTTP_CODE} — ${MSG}" >&2
[[ -s "${RESP}" ]] && head -c 400 "${RESP}" | sed 's/$/ /' >&2
echo >&2
rm -f "${RESP}"
exit 1
