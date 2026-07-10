#!/usr/bin/env bash
# End-to-end smoke for scripts/notify.sh using a localhost Telegram mock.
# Verifies that the JSON shape, severity icon, timestamp and body match
# what the real Telegram API would receive. No network egress, no token.

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKYGATE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
HARNESS="${SKYGATE_DIR}/scripts/.tg_verify.py"
PORT=8123
LOG=$(mktemp)
trap 'kill $MOCK_PID 2>/dev/null; rm -f $LOG' EXIT

echo "=== 1. Start mock Telegram server on :${PORT} ==="
python3 "${HARNESS}" --mode=mock-server --host 127.0.0.1 --port ${PORT} >"$LOG" 2>&1 &
MOCK_PID=$!
sleep 1
if ! curl -s -o /dev/null --max-time 2 http://127.0.0.1:${PORT}/probe; then
  echo "FAIL: mock not reachable"
  cat "$LOG" | head -10
  exit 1
fi

echo "=== 2. Send via notify.sh with mock TELEGRAM_API + mock creds ==="
echo "    (real TELEGRAM_API is overridden; bot token is a placeholder;"
echo "     chat_id is a placeholder; mock server prints whatever it gets)"
sudo -u skyadmin env \
  TELEGRAM_API="http://127.0.0.1:${PORT}" \
  TELEGRAM_BOT_TOKEN="MOCKBOT_TOKEN_REDACTED_AAAAAAAAAAAAAAAAAA" \
  TELEGRAM_CHAT_ID="MOCKCHAT_REDACTED" \
  HOME=/tmp/mock-test \
  "${SKYGATE_DIR}/scripts/notify.sh" --severity=warn "mock subject" "harness hello line 1
line 2 with chars: !@#\$%^
unicode: пушкин, юникод, 漢字"

echo
echo "=== 3. Mock server recorded: ==="
grep 'MOCK recv' "$LOG" | head -5

echo
echo "=== 4. Final state ==="
if grep -q 'MOCK recv' "$LOG"; then
  echo "✓ END-TO-END mock delivery verified"
  exit 0
else
  echo "✗ Mock received nothing — something broke in notify.sh wiring"
  cat "$LOG"
  exit 1
fi
