#!/usr/bin/env bash
# check_b389_rich_message_wire.sh
#
# 2026-10-16 (B389) — THE RICH MESSAGE MUST REACH THE WIRE, AND EVERY REPLY MUST SURVIVE IT.
#
# TWO DEFECTS, BOTH PROVEN AGAINST THE LIVE BOT API
# -------------------------------------------------
# The bot's own token (read from global_settings.telegram.bot_token) and a bound chat
# (telegram_bindings.chat_id) were used to POST to api.telegram.org from inside the skygate
# container — the only side with a working egress (B356.1). Results, verbatim:
#
#   1. WRONG ENVELOPE, so the rich path never delivered anything:
#        {"chat_id":N,"blocks":[…]}
#            -> 400 "Bad Request: rich message must be non-empty"
#           Unknown top-level fields are IGNORED, so the API saw an empty rich message, and
#           SendRich's own fallback re-sent the body as flat parse_mode=HTML. Every rich send
#           since B186 degraded silently; only a log line said so.
#        {"chat_id":N,"rich_message":{"blocks":[…]}}
#            -> 400 `Bad Request: can't parse InputRichBlock: type "section_heading" is unsupported`
#           i.e. the IR's type names are NOT the API's (verified accepted: paragraph, heading
#           with a numeric "size", list with OBJECT items, table with "cells", details, footer,
#           divider; rejected: section_heading, aside, inline "text", mark/sub/sup; url wants
#           the field "url", not "href").
#        {"chat_id":N,"rich_message":{"html":"<h2>x</h2>…"}}
#            -> 200 delivered. The HTML form was chosen because it needs no reverse-engineered
#           schema and reuses the renderer that already existed for the fallback.
#
#   2. FOUR COMMANDS COULD NOT BE DELIVERED AT ALL, in any form:
#        /invite (no arg)      -> 400 can't parse entities: Unsupported start tag "username"
#        /mesh join (no code)  -> 400 can't parse entities: Unsupported start tag "код"
#        (/accept (no code) and /mesh create (no name) are the same shape — the builder calls
#         markHTMLReply() at the top, so the USAGE branch is HTML too, while its catalogue
#         value carries a literal <placeholder>.)
#        Escaping the placeholders, or dropping parse_mode, made each one deliver 200.
#      The mirror-image defect: 141 catalogue values were written with `&lt;`/`&gt;` and sent
#      WITHOUT parse_mode, so the user read "&lt;username&gt;".
#
# THE FIX THIS CHECK GUARDS
# -------------------------
#   * rich.go posts {"chat_id":N,"rich_message":{"html":…}} — the IR is skygate's own, and
#     renderBlocksAsRichHTML is the single serialiser to the wire;
#   * escaping moved from the catalogue value to the SEND BOUNDARY
#     (internal/telegram/htmlsafe.go, called by sendPlain + editMessageText after trimming and
#     splitting), so a `<placeholder>` in a catalogue value can no longer drop a reply and a new
#     prefix cannot reintroduce the class;
#   * the catalogue carries logical text (no `&lt;`), verified by an i18n test.
#
# CONTRACTS
#   A. the wire shape: rich_message.html, no top-level blocks, empty render refused
#   B. the send boundary: the sanitiser is called on both HTML send paths and its allowlist is
#      the parse_mode=HTML vocabulary (not the rich one)
#   C. the catalogue: no baked-in escaping, the platform instructions are HTML, the four dead
#      commands are covered by a behavioural test
#   D. BEHAVIOURAL: the toolchain decides — the payload tests and the sanitiser tests run
#   E. registered, tracked, documented
#
# Scratch files live under mktemp -d (AGENTS trap #13). Producers are captured before they are
# matched — never `producer | grep -q` under pipefail (AGENTS trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B389: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b389.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

RICH=internal/telegram/rich.go
SAFE=internal/telegram/htmlsafe.go
NOTIFY=internal/telegram/notify.go
CAT=internal/i18n/catalog_bot.go

hdr "B389 — the rich message reaches the wire, and no reply can be dropped"

RICH_SRC="$(cat "$RICH" 2>/dev/null || true)"
# Comment-stripped views: the B389 documentation deliberately QUOTES the broken payload and the
# rich-only tag names (that is what makes the next reader understand the fix), so a source scan
# has to ignore comments or it would fail on its own explanation. Files are Go, where a full-line
# `//` comment is unambiguous.
RICH_CODE="$(grep -v '^[[:space:]]*//' "$RICH" 2>/dev/null || true)"
SAFE_SRC="$(cat "$SAFE" 2>/dev/null || true)"
NOTIFY_SRC="$(cat "$NOTIFY" 2>/dev/null || true)"
CAT_SRC="$(cat "$CAT" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# A. the wire shape
# ---------------------------------------------------------------------------
hdr "A — the sendRichMessage payload is rich_message.html, not top-level blocks"

# A1: the nested envelope is present.
case "$RICH_SRC" in
  *'"rich_message": map[string]any{'*) ok "A1: SendRich posts the message under the rich_message object" ;;
  *) bad "A1: rich.go has no nested rich_message payload (the pre-B389 shape the API answers with 'rich message must be non-empty')" ;;
esac

# A2: the body is HTML built by the rich renderer.
case "$RICH_SRC" in
  *'renderBlocksAsRichHTML(blocks)'*) ok "A2: the payload body comes from renderBlocksAsRichHTML" ;;
  *) bad "A2: rich.go does not render the IR through renderBlocksAsRichHTML" ;;
esac

# A3: the OLD wire key is gone from the CODE. This is the assertion that would have caught B186.
case "$RICH_CODE" in
  *'"blocks":'*) bad "A3: rich.go still puts a top-level \"blocks\" field on the wire (unknown top-level fields are ignored by Telegram, so the rich message is EMPTY)" ;;
  *) ok "A3: no top-level \"blocks\" field in the sendRichMessage payload" ;;
esac

# A4: a body that renders to nothing is an error, not a silent 400.
case "$RICH_CODE" in
  *'rendered to an empty body'*) ok "A4: an empty render is refused instead of posted" ;;
  *) bad "A4: SendRich does not guard against an empty rendered body" ;;
esac

# A5: the verified rich vocabulary is what the renderer emits.
MISSING_TAGS=""
for tag in '<h%d>%s</h%d>' '<p>' '<table bordered striped>' '<details><summary>' \
           '</details>' '<blockquote>' '<footer>' '<hr>' '<pre>' '<li>' '<tg-spoiler>' \
           '<tg-time unix=' 'tag = "ol"'; do
  case "$RICH_CODE" in
    *"$tag"*) : ;;
    *) MISSING_TAGS="${MISSING_TAGS}${tag} " ;;
  esac
done
if [ -z "$MISSING_TAGS" ]; then
  ok "A5: the rich renderer emits the vocabulary verified accepted by the live API (headings, table bordered striped, details/summary, blockquote, footer, hr, pre, ul/ol/li, tg-spoiler, tg-time)"
else
  bad "A5: the rich renderer lost verified tags: $MISSING_TAGS"
fi

# A6: the flat fallback must not carry rich-only tags (parse_mode=HTML would reject them).
# Extract the flat renderer body (renderBlockAsHTML .. end of function), comments stripped.
awk '/^func renderBlockAsHTML\(/{f=1} f{print} f&&/^}/{exit}' "$RICH" > "$SKY_TMP/flat.txt" 2>/dev/null || true
FLAT_SRC="$(grep -v '^[[:space:]]*//' "$SKY_TMP/flat.txt" 2>/dev/null || true)"
FLAT_ONLY=""
for tag in '<h2>' '<h3>' '<h4>' '<table' '<details' '<summary' '<footer>' '<blockquote' '<hr>' '<tg-time'; do
  case "$FLAT_SRC" in
    *"$tag"*) FLAT_ONLY="${FLAT_ONLY}${tag} " ;;
  esac
done
if [ -z "$FLAT_ONLY" ]; then
  ok "A6: the sendMessage fallback emits only the parse_mode=HTML vocabulary (no rich-only tag)"
else
  bad "A6: the flat fallback carries rich-only tags (Telegram rejects those with parse_mode=HTML): $FLAT_ONLY"
fi

# A7: reply_markup survives the fallback.
case "$RICH_SRC" in
  *'func pendingFromRichOpts'*'inline_keyboard'*)
    ok "A7: an inline keyboard passed to SendRich survives the sendMessage fallback" ;;
  *) bad "A7: the fallback rebuilds no keyboard (a rich reply with buttons would lose them)" ;;
esac

# ---------------------------------------------------------------------------
# B. the send boundary
# ---------------------------------------------------------------------------
hdr "B — escaping is a property of the send path, not of the catalogue value"

case "$SAFE_SRC" in
  *'func sanitizeTelegramHTML'*) ok "B1: internal/telegram/htmlsafe.go provides sanitizeTelegramHTML" ;;
  *) bad "B1: the send-boundary sanitiser is missing" ;;
esac

# B2: it is actually wired into both HTML send paths, and only for parse_mode=HTML.
WIRED=1
case "$NOTIFY_SRC" in
  *'pending.ParseMode == "HTML"'*'sanitizeTelegramHTML(text)'*) : ;;
  *) WIRED=0 ;;
esac
if [ "$WIRED" = "1" ]; then
  ok "B2: sendPlain and editMessageText sanitise the body when parse_mode is HTML"
else
  bad "B2: the sanitiser is not called on the HTML send path — an unbalanced <placeholder> would still drop the reply"
fi

# B3: both call sites (two occurrences expected).
N_SANITIZE="$(printf '%s\n' "$NOTIFY_SRC" | grep -c 'sanitizeTelegramHTML(text)' || true)"
if [ "${N_SANITIZE:-0}" -ge 2 ]; then
  ok "B3: both HTML send paths are covered (sendPlain + editMessageText)"
else
  bad "B3: only ${N_SANITIZE:-0} HTML send path(s) sanitise — an edited message could still be dropped"
fi

# B4: the allowlist is the parse_mode=HTML vocabulary and NOT the rich/IR names.
MISSING_TAGS_SAFE=""
for t in b i u s code pre a span blockquote tg-spoiler; do
  case "$SAFE_SRC" in
    *"\"$t\": true"*) : ;;
    *) MISSING_TAGS_SAFE="${MISSING_TAGS_SAFE}${t} " ;;
  esac
done
if [ -z "$MISSING_TAGS_SAFE" ]; then
  ok "B4: the sanitiser allowlist carries the real parse_mode=HTML vocabulary (b/i/u/s/code/pre/a/span/blockquote/tg-spoiler)"
else
  bad "B4: the allowlist is missing core parse_mode=HTML tags: $MISSING_TAGS_SAFE"
fi
case "$SAFE_SRC" in
  *'"section_heading"'*|*'"aside"'*|*'"table bordered striped"'*)
    bad "B5: the sanitiser allowlist contains rich-only names — they must stay OUT of parse_mode=HTML bodies" ;;
  *) ok "B5: no rich-only tag name leaks into the parse_mode=HTML allowlist" ;;
esac

# B6: the balanced/unbalanced rule exists (a truncated body is the realistic source).
case "$SAFE_SRC" in
  *'func escapeUnbalancedTags'*) ok "B6: an unbalanced allowed tag is escaped (Telegram rejects an unterminated tag)" ;;
  *) bad "B6: no unbalanced-tag handling" ;;
esac

# ---------------------------------------------------------------------------
# C. the catalogue
# ---------------------------------------------------------------------------
hdr "C — the catalogue carries logical text, and the dead commands are covered"

case "$CAT_SRC" in
  *'&lt;'*|*'&gt;'*|*'&amp;'*|*'&quot;'*)
    bad "C1: catalog_bot.go still contains baked-in HTML entities — the send boundary escapes now, so the value must carry the logical text"
    printf '%s\n' "$CAT_SRC" | grep -n '&lt;\|&gt;\|&amp;\|&quot;' | head -5 | sed 's/^/        /' >&2 ;;
  *) ok "C1: no &lt;/&gt;/&amp;/&quot; left in catalog_bot.go (141 values unwrapped from the pre-B389 double convention)" ;;
esac

# C2: the four commands that were silently dead are covered by a behavioural test.
HTMLSAFE_TEST=internal/telegram/htmlsafe_test.go
HT_SRC="$(cat "$HTMLSAFE_TEST" 2>/dev/null || true)"
MISSING_KEYS=""
for k in bot.invite.usage_invite bot.invite.usage_accept bot.mesh.usage_create bot.mesh.usage_join; do
  case "$HT_SRC" in
    *"$k"*) : ;;
    *) MISSING_KEYS="${MISSING_KEYS}${k} " ;;
  esac
done
if [ -z "$MISSING_KEYS" ]; then
  ok "C2: the four commands that were silently dead (/invite, /accept, /mesh create, /mesh join) are pinned by a test that runs their real catalogue values through the sanitiser"
else
  bad "C2: no regression test for the dead usage strings: $MISSING_KEYS"
fi

# C3: the platform instructions are HTML and carry no Markdown fence.
case "$CAT_SRC" in
  *'platform.linux'*'<pre>'*) ok "C3: the /add_device platform instructions are HTML with a <pre> command block" ;;
  *) bad "C3: the platform instructions are not HTML (the user sees raw markdown fences)" ;;
esac
PLAT_MD="$(printf '%s\n' "$CAT_SRC" | grep -c 'platform\..*```' || true)"
if [ "${PLAT_MD:-0}" -eq 0 ]; then
  ok "C4: no Markdown code fence left in the platform instructions"
else
  bad "C4: ${PLAT_MD} platform value(s) still use a Markdown fence"
fi

# C5: the readability contracts from this block.
if grep -q 'bot.status.label_rules' internal/telegram/commands.go 2>/dev/null; then
  ok "C5: the rich /status table uses the label keys, not the %d format strings that rendered a literal placeholder"
else
  bad "C5: the rich /status table still labels rows with a format string"
fi
case "$(cat internal/telegram/commands_mesh.go 2>/dev/null || true)" in
  *'m.Name, m.Code, env.Username, m.Code'*) ok "C6: /mesh create passes all four placeholders (it rendered %!s(MISSING))" ;;
  *) bad "C6: /mesh create still passes too few args to bot.mesh.created" ;;
esac
case "$(cat internal/telegram/personality.go 2>/dev/null || true)" in
  *'func trimForTelegram(lang, s string)'*'len([]rune(s))'*'i18n.T(lang, "bot.trim.marker")'*)
    ok "C7: trimForTelegram counts runes and uses the caller's language for the marker" ;;
  *) bad "C7: trimForTelegram is still byte-based and/or English-only" ;;
esac

# ---------------------------------------------------------------------------
# D. behavioural — the toolchain decides
# ---------------------------------------------------------------------------
hdr "D — behavioural: the payload and sanitiser tests run"

GO_BIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GO_BIN" ] && [ -x "/c/Program Files/Go/bin/go.exe" ]; then
  GO_BIN="/c/Program Files/Go/bin/go.exe"
fi
if [ -z "$GO_BIN" ]; then
  skip "D1: the Go toolchain is not on PATH — cannot run the behavioural contracts"
  skip "D2: the Go toolchain is not on PATH"
else
  TEST_TIMEOUT="${SKYGATE_CHECK_TIMEOUT:-600}"
  OUT="$(timeout "$TEST_TIMEOUT" "$GO_BIN" test ./internal/telegram/ \
        -run 'TestSendRich_|TestSanitizeTelegramHTML_|TestRichReplyMarkup_|TestTrimForTelegram_' \
        -count=1 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "D1: the wire-payload, sanitiser, keyboard and trim tests pass (the payload the notifier actually posts is captured and inspected)"
  else
    bad "D1: the wire/sanitiser tests failed (rc=$rc)"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/        /' >&2
  fi
  OUT2="$(timeout "$TEST_TIMEOUT" "$GO_BIN" test ./internal/i18n/ -count=1 2>&1)"
  rc2=$?
  if [ "$rc2" -eq 0 ]; then
    ok "D2: the catalogue policy tests pass (no baked-in escaping, no rich-only tag in a bot value)"
  else
    bad "D2: the catalogue policy tests failed (rc=$rc2)"
    printf '%s\n' "$OUT2" | tail -20 | sed 's/^/        /' >&2
  fi
fi

# ---------------------------------------------------------------------------
# E. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "E — registered, tracked, documented"

if git ls-files --error-unmatch "$SAFE" >/dev/null 2>&1; then
  ok "E1: internal/telegram/htmlsafe.go is tracked by git"
else
  bad "E1: internal/telegram/htmlsafe.go is not tracked (a file the operator depends on must be committed)"
fi
if grep -q 'check_b389_rich_message_wire' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in scripts/verify_pre_deploy.sh"
fi
if grep -q '^\- \*\*B389\*\*' AGENTS.md 2>/dev/null; then
  ok "E3: one-line entry in the AGENTS.md block index"
else
  bad "E3: no B389 entry in the AGENTS.md block index"
fi
case "$RICH_SRC" in
  *'rich message must be non-empty'*)
    ok "E4: rich.go documents the measured rejection that hid the bug" ;;
  *) bad "E4: the measured live evidence is not recorded in rich.go (the next reader will 'fix' the envelope back)" ;;
esac
if grep -q 'rich_message' docs/TELEGRAM.md 2>/dev/null; then
  ok "E5: docs/TELEGRAM.md documents the rich_message wire format"
else
  bad "E5: docs/TELEGRAM.md does not document the wire format"
fi
if git ls-files --error-unmatch .dsh/skills/skygate-telegram-message-style/SKILL.md >/dev/null 2>&1; then
  ok "E6: the message-style skill is tracked"
else
  bad "E6: .dsh/skills/skygate-telegram-message-style/SKILL.md is missing or untracked"
fi
if grep -q 'v1.5.130' docs/ROADMAP.md 2>/dev/null; then
  ok "E7: docs/ROADMAP.md carries the v1.5.130 version line and the deferred work (TD-33/TD-34)"
else
  bad "E7: docs/ROADMAP.md does not record this release"
fi
if grep -q '^**L-69' docs/LESSONS.md 2>/dev/null; then
  ok "E8: docs/LESSONS.md carries L-69 (a serialisation test that asserts its own struct certifies nothing)"
else
  bad "E8: the lesson is not written down in docs/LESSONS.md"
fi
# E9: the companion contract must not be reverted. B186's contract I ASSERTED the payload
# Telegram rejects (`"blocks":  blocks` at the top level), which is how the defect survived two
# months with a green gate. It now pins the nested envelope and the ABSENCE of the old key — the
# full log confirmations are in that script's header, and this contract makes a revert fail here.
# Comments are stripped first: that header deliberately QUOTES the old assertion as evidence.
# Matched with shell builtins on a captured variable — a pipeline here would be a trap-#9/#15 site
# AND a B386 A1 violation (this is the very class the block is about).
B186_CODE="$(grep -v '^[[:space:]]*#' scripts/check_b186.sh 2>/dev/null || true)"
B186_HAS_NEW=0
B186_HAS_OLD=0
case "$B186_CODE" in
  *'I-no-toplevel-blocks'*) B186_HAS_NEW=1 ;;
esac
case "$B186_CODE" in
  *'check_ge "I-blocks"'*) B186_HAS_OLD=1 ;;
esac
if [ "$B186_HAS_NEW" = "1" ] && [ "$B186_HAS_OLD" = "0" ]; then
  ok "E9: B186's contract I pins the nested envelope and the absence of the old top-level blocks key (renegotiated, not reverted)"
else
  bad "E9: B186's contract I still asserts the pre-B389 wire shape (has_new=$B186_HAS_NEW has_old=$B186_HAS_OLD) — the gate would again certify a broken rich path"
fi

printf '\n\033[1mB389: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
