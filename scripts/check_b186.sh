#!/bin/bash
# B186 — Telegram Bot API 10.1 Rich Messages adapter
#
# Operator 2026-08-25: "адаптируй сообщения бота в телеграме
# под новый формат Bot API 10.1 Telegram добавил Rich Messages
# - проанализируй новые темы оформления (как теперь можно
# оформить все списки правил и вывода информации) и подбери
# под текущий стиль". The new sendRichMessage endpoint
# accepts structured HTML/markdown/blocks: headings, lists,
# tables, <details>, <aside>, <tg-time>, etc.
#
# B186 fix: a new internal/telegram/rich.go implements the
# structured builder (Heading, Paragraph, KeyValueTable,
# Details, Aside, List, Footer, etc.). SendRich() posts via
# the new sendRichMessage API and falls back to sendMessage
# with parse_mode=HTML on any error (e.g. bot version < 10.1).
# KeyValueTable replaces the old flat "<b>label:</b>
# <code>value</code>" lines (which didn't align on mobile)
# with a real <table> block. Aside / Details / Footer
# preserve the butler-voice envelope while adding the
# structure the new endpoint can render.
#
# Limits (from the Bot API 10.1 docs, #rich-message-limits):
#   - 32768 UTF-8 chars total
#   - 500 blocks (incl. nested blocks, list items, table rows)
#   - 16 nesting levels
#   - 50 media attachments
#   - 20 columns in a table
#
# Contracts (10 sub-checks):
#  A. internal/telegram/rich.go exists with SendRich
#  B. SendRich falls back to sendMessage on error
#  C. internal/telegram/rich_test.go has 9 test functions
#  D. KeyValueTable builds a 2-col table with bold/code cells
#  E. Table enforces the 20-column limit (returns "Table too wide")
#  F. Details block carries a summary + a body slice
#  G. Aside block type is "aside"
#  H. Time inline node has type "date_time" + ISO 8601
#  I. JSON serialisation of the blocks matches the Bot API
#     spec (chat_id + blocks in one POST body)
#  J. Build goes through + all package tests pass

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

check_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "  PASS [$label] $actual"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] expected=$expected got=$actual"
    FAIL=$((FAIL+1))
  fi
}

check_ge() {
  local label="$1" min="$2" actual="$3"
  if [ "$actual" -ge "$min" ] 2>/dev/null; then
    echo "  PASS [$label] actual=$actual (>= $min)"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] actual=$actual (expected >= $min)"
    FAIL=$((FAIL+1))
  fi
}

count() {
  local n
  n=$(grep -cE -- "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

echo "=== B186 contracts ==="

# A. internal/telegram/rich.go exists with SendRich
check_ge "A-func" 1 "$(count "$REPO/internal/telegram/rich.go" 'func \(n \*RealNotifier\) SendRich')"
check_ge "A-type" 1 "$(count "$REPO/internal/telegram/rich.go" 'type RichBlock')"
check_ge "A-text" 1 "$(count "$REPO/internal/telegram/rich.go" 'type RichText')"

# B. SendRich falls back to sendMessage on error
check_ge "B-fallback" 1 "$(count "$REPO/internal/telegram/rich.go" 'fall.*back.*sendMessage')"

# C. internal/telegram/rich_test.go has 9 test functions
check_ge "C-tests" 9 "$(count "$REPO/internal/telegram/rich_test.go" '^func Test')"

# D. KeyValueTable builds a 2-col table with bold/code cells
# Pin the B186 conversion of the old "<b>label:</b>
# <code>value</code>" lines.
check_ge "D-kvtable" 1 "$(count "$REPO/internal/telegram/rich.go" 'func KeyValueTable')"
check_ge "D-kvrow" 1 "$(count "$REPO/internal/telegram/rich.go" 'type KVRow')"
check_ge "D-bold" 1 "$(grep -c 'Bold(r.Label)' "$REPO/internal/telegram/rich.go" 2>/dev/null | tr -d '\n')"

# E. Table enforces the 20-column limit
check_ge "E-toowide" 1 "$(count "$REPO/internal/telegram/rich.go" 'Table too wide')"
check_ge "E-limit" 1 "$(count "$REPO/internal/telegram/rich.go" 'maxCols > 20')"

# F. Details block carries a summary + a body slice
check_ge "F-details" 1 "$(count "$REPO/internal/telegram/rich.go" 'func Details')"
check_ge "F-body" 1 "$(count "$REPO/internal/telegram/rich.go" '\"body\":    body,')"

# G. Aside block type is "aside"
check_ge "G-aside" 1 "$(count "$REPO/internal/telegram/rich.go" '\"type\": \"aside\"')"

# H. Time inline node has type "date_time" + ISO 8601
check_ge "H-datetime" 1 "$(count "$REPO/internal/telegram/rich.go" '\"date_time\"')"
check_ge "H-iso" 1 "$(count "$REPO/internal/telegram/rich.go" '\"iso\":')"

# I. JSON serialisation shape
# Verifies the SendRich POST body uses the new sendRichMessage
# shape (chat_id + blocks at the top level).
check_ge "I-endpoint" 1 "$(count "$REPO/internal/telegram/rich.go" 'sendRichMessage')"
check_ge "I-blocks" 1 "$(count "$REPO/internal/telegram/rich.go" '\"blocks\":  blocks')"

# J. Build + tests pass
GO_BIN=""
for cand in /usr/local/go/bin/go /usr/bin/go /opt/go/bin/go "$(command -v go 2>/dev/null)"; do
  if [ -x "$cand" ]; then
    GO_BIN="$cand"
    break
  fi
done
if [ -n "$GO_BIN" ]; then
  if (cd "$REPO" && "$GO_BIN" build ./internal/telegram/... 2>&1) | grep -q '^# '; then
    check_eq "J-build" "ok" "FAIL"
  else
    check_eq "J-build" "ok" "ok"
  fi
  if (cd "$REPO" && "$GO_BIN" test -count=1 ./internal/telegram/... 2>&1) | grep -q '^ok'; then
    check_eq "J-test" "ok" "ok"
  else
    check_eq "J-test" "ok" "FAIL"
  fi
else
  echo "  SKIP [J] go not available in PATH"
fi

echo
echo "=== B186 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
