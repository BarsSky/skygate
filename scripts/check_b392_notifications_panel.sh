#!/usr/bin/env bash
# check_b392_notifications_panel.sh
#
# 2026-10-16 (B392) — THE NOTIFICATION PANEL MUST BE READABLE, AND THE LIST MUST BE REACHABLE.
#
# OPERATOR REPORT
#   «с оповещением в панели вопросы так как там по отображению проблемы с наплывом и также не
#    удобно отслеживать если сообщений будет больше и как их все там увидеть и прочитать и
#    особенно чтобы они не перекрывались вкладкой — сейчас при нажатии всплывающее окно
#    перекрывается вкладкой».
#
# WHAT WAS MEASURED IN THE CODE (read-only audit, then these fixes)
#   1. THE DROPDOWN WAS CLIPPED, NOT MIS-STACKED. `.sidebar` was `overflow:hidden`
#      (themes.css:227) and the bell panel is an absolutely-positioned child, so its clip box
#      cut the panel at the sidebar's own width: the panel asks for up to 340px, starts ~8px
#      from the edge, and the content column begins exactly at 220px — so ~38% of it was
#      clipped, and in the 52px collapsed state only a ~44px sliver was visible. The z-index was
#      already the highest in the sidebar (200), which is why "raise the z-index" would not have
#      fixed it. Worse, `.theme-picker-sb .menu` (specificity 0,2,0) overrode `.notif-menu`
#      (0,1,0), so the declared min-width:280px never applied and the real floor was 140px.
#   2. THE LIST WAS UNREACHABLE. /my/notifications has existed since B157.1 but had NO entry in
#      the sidebar or in the bell; the dropdown showed at most 50 rows with no "more" hint, and
#      the badge counted every unread row — so a user with 300 unread saw "300" and could read 50.
#   3. THE PAGE LIED ABOUT ITS OWN SIZE. The handler loaded a capped slice into memory and sliced
#      it, while the db helpers silently rewrote any limit > 100 to 50; "N total" could therefore
#      never exceed 50, and every page past the second rendered the "you have no notifications"
#      empty state while the rows were still in the table.
#   4. THE ADMIN INBOX HAD NO BADGE. `CountOpenMonitorEventsNotLowerThan` existed with a doc
#      comment saying "the number the nav badge shows" and was never called from production code.
#   5. TIME WAS ENGLISH-ONLY. notifications.TimeAgo hardcoded "2 h ago" while the notif.time_* keys
#      sat unused in BOTH catalogues.
#   6. TWO themes.css FILES. The runtime serves internal/staticfs/static/css/themes.css (embedded);
#      every Go CSS test and 4 shell contracts (B105/B106/B107/B157) read the ROOT static/css/
#      themes.css, which had drifted 65 lines behind. A fix applied to only one of them leaves the
#      gate green and the panel broken.
#
# CONTRACTS
#   A. the panel is not clipped: no overflow:hidden on the sidebar, the scoped size rule exists,
#      the new element classes exist, and the two themes.css copies are IDENTICAL
#   B. the data layer pages and counts in SQL, with the optional-condition form, and no silent
#      limit rewrite
#   C. the UI wires it up: sidebar entry, pageFromName mapping, bell time + "more" + footer,
#      injected UnreadMore/OpenAlerts, page counters
#   D. BEHAVIOURAL: the db/notifications Go tests run — the toolchain decides
#   E. registered, tracked, documented
#
# Scratch files live under mktemp -d (AGENTS trap #13). Producers are captured before they are
# matched — never `producer | grep -q` under pipefail (AGENTS trap #9), which is also what B386's
# detector scans for in scripts/*.sh.

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B392: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b392.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

CSS_EMBEDDED=internal/staticfs/static/css/themes.css
CSS_MIRROR=static/css/themes.css
LAYOUT=internal/handlers/templates/layout.html
PAGE=internal/handlers/templates/user/notifications.html
MYNOTIF=internal/feature/my/notifications.go
HANDLERS=internal/handlers/handlers.go
DBQ=internal/db/queries.go

hdr "B392 — the notification panel reads, scales and is reachable"

CSS_SRC="$(cat "$CSS_EMBEDDED" 2>/dev/null || true)"
LAYOUT_SRC="$(cat "$LAYOUT" 2>/dev/null || true)"
PAGE_SRC="$(cat "$PAGE" 2>/dev/null || true)"
MYNOTIF_SRC="$(cat "$MYNOTIF" 2>/dev/null || true)"
HANDLERS_SRC="$(cat "$HANDLERS" 2>/dev/null || true)"
DBQ_SRC="$(cat "$DBQ" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# A. the panel is not clipped, and the CSS copies agree
# ---------------------------------------------------------------------------
hdr "A — the dropdown is not clipped, and the two themes.css copies agree"

# A1: the sidebar rule must not clip. Match the rule LINE and inspect it.
SIDEBAR_RULE="$(grep -m1 '^\.sidebar{' "$CSS_EMBEDDED" 2>/dev/null || true)"
case "$SIDEBAR_RULE" in
  *"overflow:visible"*)
    ok "A1: .sidebar does not clip its descendants (overflow:visible), so the bell panel is not cut at the sidebar edge" ;;
  *"overflow:hidden"*)
    bad "A1: .sidebar clips with overflow:hidden again — the bell panel loses ~38% of its width (that is the operator's 'перекрывается вкладкой')" ;;
  *)
    bad "A1: cannot read the .sidebar rule from $CSS_EMBEDDED (got: ${SIDEBAR_RULE:-empty})" ;;
esac

# A2: the reason must be written down, or the next reader re-adds the clip "for safety".
case "$CSS_SRC" in
  *"B392"*"overflow:visible"*|*"overflow:visible"*"B392"*)
    ok "A2: the reason the clip was removed is recorded next to the rule" ;;
  *) bad "A2: no B392 note next to the .sidebar rule — the clip will come back" ;;
esac

# A3: the scoped size rule (the one that actually wins) must exist with viewport clamps.
case "$CSS_SRC" in
  *'.theme-picker-sb .notif-menu{'*'100vw - 24px'*)
    ok "A3: the bell panel has its own rule with viewport-clamped width (it was losing a specificity war with .theme-picker-sb .menu)" ;;
  *) bad "A3: no scoped .theme-picker-sb .notif-menu rule — .theme-picker-sb .menu (min-width:140px) wins again" ;;
esac
case "$CSS_SRC" in
  *'.theme-picker-sb .notif-menu{'*'60vh'*)
    ok "A4: the panel height is clamped to the viewport (60vh), so its top edge cannot end up out of reach" ;;
  *) bad "A4: the panel height is not clamped to the viewport — the fixed 480px could exceed a short window" ;;
esac

# A5: the new element classes.
MISSING_CLS=""
for cls in notif-menu-head notif-time notif-more notif-menu-foot; do
  case "$CSS_SRC" in
    *".${cls}"*) : ;;
    *) MISSING_CLS="${MISSING_CLS}${cls} " ;;
  esac
done
if [ -z "$MISSING_CLS" ]; then
  ok "A5: the new panel elements have styles (sticky head, per-item time, 'and N more', footer link)"
else
  bad "A5: unstyled new elements: $MISSING_CLS"
fi

# A6: the two copies must be IDENTICAL. This is the trap that makes a fix invisible.
if cmp -s "$CSS_EMBEDDED" "$CSS_MIRROR"; then
  ok "A6: static/css/themes.css is byte-identical to the embedded copy the runtime serves"
else
  bad "A6: the two themes.css copies have DIVERGED — the runtime serves $CSS_EMBEDDED while the Go tests and 4 shell contracts (B105/B106/B107/B157) read $CSS_MIRROR, so a fix applied to one side leaves the gate green and the panel broken. Fix: cp $CSS_EMBEDDED $CSS_MIRROR"
  diff "$CSS_MIRROR" "$CSS_EMBEDDED" 2>/dev/null | head -8 | sed 's/^/        /' >&2 || true
fi

# A7: the classes B157's contract watches must survive (it greps the MIRROR).
B157_MISSING=""
for cls in notif-badge notif-menu notif-item notif-empty; do
  case "$(cat "$CSS_MIRROR" 2>/dev/null || true)" in
    *".${cls}"*) : ;;
    *) B157_MISSING="${B157_MISSING}${cls} " ;;
  esac
done
if [ -z "$B157_MISSING" ]; then
  ok "A7: the B157 bell classes are intact in the mirror (that contract greps the mirror, not the embedded copy)"
else
  bad "A7: B157's contract would fail — classes missing from the mirror: $B157_MISSING"
fi

# ---------------------------------------------------------------------------
# B. the data layer pages and counts in SQL
# ---------------------------------------------------------------------------
hdr "B — pagination and totals come from SQL, and nothing is silently clamped"

case "$DBQ_SRC" in
  *'qListNotificationsFiltered'*'LIMIT $4 OFFSET $5'*)
    ok "B1: the notification page query carries LIMIT/OFFSET (the page used to slice a capped read in memory)" ;;
  *) bad "B1: no LIMIT/OFFSET notification query — page 3 still renders the empty state" ;;
esac
case "$DBQ_SRC" in
  *'($2 = 0 OR read_at = 0)'*"(\$3 = '' OR severity = \$3)"*)
    ok "B2: the unread and severity filters are optional SQL conditions, so the page, the total and 'has next' cannot disagree" ;;
  *) bad "B2: the filters are not optional SQL conditions — either the total or the page will be wrong" ;;
esac
case "$DBQ_SRC" in
  *'qCountNotificationsFiltered'*) ok "B3: there is a filtered COUNT for the real total" ;;
  *) bad "B3: no filtered COUNT query — 'N total' would be the size of a page" ;;
esac

DBN_SRC="$(cat internal/db/notifications.go 2>/dev/null || true)"
case "$DBN_SRC" in
  *'limit <= 0 || limit > 100'*)
    bad "B4: the silent limit rewrite is back (limit > 100 became 50, so a caller could not tell a clamped read from a complete one)" ;;
  *) ok "B4: no silent limit rewrite — the ceiling is explicit (defaultNotificationLimit/maxNotificationLimit)" ;;
esac
case "$DBN_SRC" in
  *'clampNotificationLimit'*'maxNotificationLimit'*)
    ok "B5: the limit bounds are named constants documented in the code" ;;
  *) bad "B5: the limit bounds are not named constants" ;;
esac
case "$MYNOTIF_SRC" in
  *'notifications.CountByFilter'*'notifications.ListPageByFilter'*)
    ok "B6: the page uses the counting + paged helpers" ;;
  *) bad "B6: the page does not use the new helpers" ;;
esac
# B7: the two NEW reads must not add raw error pages. The file has pre-existing
# `http.Error(w, err.Error(), ...)` sites (older than this block, and frozen by the
# error_leak_guard ratchet), so the honest contract is that THIS block's failures are
# captured into a banner: the error is assigned to loadErr and handed to the template.
# A blanket "no http.Error in this file" would fail on code this block did not write.
N_LOADERR="$(printf '%s\n' "$MYNOTIF_SRC" | grep -c 'loadErr = err.Error()' || true)"
case "$MYNOTIF_SRC" in
  *'"LoadError":   loadErr'*|*'"LoadError": loadErr'*)
    if [ "${N_LOADERR:-0}" -ge 2 ]; then
      ok "B7: the page's own reads capture their errors into a banner (LoadError) instead of adding raw error pages"
    else
      bad "B7: LoadError is passed but only ${N_LOADERR:-0} read(s) set it (expected the count and the page read)"
    fi ;;
  *)
    bad "B7: LoadError is not passed to the template — a failed read would land in a raw error page or a silent empty list" ;;
esac

# ---------------------------------------------------------------------------
# C. the UI wires it up
# ---------------------------------------------------------------------------
hdr "C — the list is reachable and the panel says what it is not showing"

# Matched with shell builtins on a captured variable: a pipeline here would be a trap-#9/#15
# site AND a B386 A1 violation (this file is scanned by that detector).
LAYOUT_HAS_NOTIF_LINK=0
case "$LAYOUT_SRC" in
  *'href="/my/notifications"'*) LAYOUT_HAS_NOTIF_LINK=1 ;;
esac
if [ "$LAYOUT_HAS_NOTIF_LINK" = "1" ]; then
  ok "C1: the sidebar links to /my/notifications (the page existed since B157.1 and had no entry point)"
else
  bad "C1: no sidebar entry for /my/notifications — the full list is unreachable again"
fi
HANDLERS_MAPS_NOTIF=0
case "$HANDLERS_SRC" in
  *'name == "user/notifications"'*) HANDLERS_MAPS_NOTIF=1 ;;
esac
if [ "$HANDLERS_MAPS_NOTIF" = "1" ]; then
  ok "C2: pageFromName maps user/notifications to my/notifications, so the sidebar entry highlights"
else
  bad "C2: pageFromName has no mapping for user/notifications (no active highlight)"
fi
case "$LAYOUT_SRC" in
  *'notif.more_unread'*) ok "C3: the dropdown admits there are more unread rows than it shows" ;;
  *) bad "C3: the dropdown is silent about the rows it hides" ;;
esac
case "$LAYOUT_SRC" in
  *'notif.view_all'*) ok "C4: the dropdown links to the full list" ;;
  *) bad "C4: the dropdown has no way out to the full list" ;;
esac
case "$LAYOUT_SRC" in
  *'.TimeAgo'*|*'{{.TimeAgo}}'*) ok "C5: each dropdown row carries its own timestamp (the full page had one, the dropdown had none)" ;;
  *) bad "C5: no timestamp in the dropdown rows" ;;
esac
case "$HANDLERS_SRC" in
  *'data["UnreadMore"]'*) ok "C6: the handler injects the unread remainder" ;;
  *) bad "C6: no UnreadMore injection — the template cannot tell the truth about hidden rows" ;;
esac
case "$HANDLERS_SRC" in
  *'countOpenMonitorEventsNotLowerThan'*|*'CountOpenMonitorEventsNotLowerThan'*)
    ok "C7: the admin inbox badge is wired (the counter existed with a comment saying it was the nav badge, and was never called)" ;;
  *) bad "C7: the admin inbox still has no badge" ;;
esac
case "$HANDLERS_SRC" in
  *'notificationBellLimit'*) ok "C8: the bell's row cap is a named constant, not a literal" ;;
  *) bad "C8: the bell's cap is still a magic number" ;;
esac
PAGE_WINDOW_OK=1
case "$PAGE_SRC" in
  *'notif.shown_of'*) : ;;
  *) PAGE_WINDOW_OK=0 ;;
esac
case "$PAGE_SRC" in
  *'notif.page_out_of_range'*) : ;;
  *) PAGE_WINDOW_OK=0 ;;
esac
if [ "$PAGE_WINDOW_OK" = "1" ]; then
  ok "C9: the page says how many of the total it shows and warns when the requested page was out of range"
else
  bad "C9: the page does not report its own window (needs both notif.shown_of and notif.page_out_of_range)"
fi
case "$PAGE_SRC" in
  *'severity='*) ok "C10: the page has a severity filter (the colours already differed, but they could not be filtered)" ;;
  *) bad "C10: no severity filter on the page" ;;
esac
case "$LAYOUT_SRC" in
  *'OpenAlerts'*) ok "C11: the sidebar renders the admin alert badge" ;;
  *) bad "C11: the sidebar does not render the alert badge" ;;
esac

# C12: RU+EN parity for every key this block added.
CAT=internal/i18n/catalog_my.go
MISSING_KEYS=""
for k in notif.view_all notif.more_unread notif.shown_of notif.severity_label notif.severity_any \
         notif.page_out_of_range notif.empty_filtered notif.menu_label notif.time_wk_ago notif.time_mo_ago; do
  n="$(grep -cF "\"${k}\"" "$CAT" 2>/dev/null || true)"
  [ "${n:-0}" -ge 2 ] || MISSING_KEYS="${MISSING_KEYS}${k}(${n:-0}) "
done
if [ -z "$MISSING_KEYS" ]; then
  ok "C12: all 10 new i18n keys exist in RU and EN"
else
  bad "C12: keys missing the RU+EN pair: $MISSING_KEYS"
fi

case "$(cat internal/notifications/notifications.go 2>/dev/null || true)" in
  *'func TimeAgo(lang string, t time.Time, ref time.Time)'*'i18n.Tf(lang, "notif.time_'*)
    ok "C13: TimeAgo is localised (it hardcoded English while the RU keys sat unused)" ;;
  *) bad "C13: TimeAgo is still English-only" ;;
esac

# ---------------------------------------------------------------------------
# D. behavioural — the toolchain decides
# ---------------------------------------------------------------------------
hdr "D — behavioural: the pagination, filter and localisation tests run"

GO_BIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GO_BIN" ] && [ -x "/c/Program Files/Go/bin/go.exe" ]; then
  GO_BIN="/c/Program Files/Go/bin/go.exe"
fi
if [ -z "$GO_BIN" ]; then
  skip "D1: the Go toolchain is not on PATH — cannot run the behavioural contracts"
else
  TEST_TIMEOUT="${SKYGATE_CHECK_TIMEOUT:-600}"
  OUT="$(timeout "$TEST_TIMEOUT" "$GO_BIN" test ./internal/db/ -run 'B392' -count=1 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "D1: the SQL pagination/filter/total tests pass on a real (SQLite) database: page 3 of 120 returns rows, the totals are real, the filters are SQL conditions"
  else
    bad "D1: the pagination tests failed (rc=$rc)"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/        /' >&2
  fi
  OUT2="$(timeout "$TEST_TIMEOUT" "$GO_BIN" test ./internal/notifications/ -run 'TestTimeAgo|TestNotificationFilterFor' -count=1 2>&1)"
  rc2=$?
  if [ "$rc2" -eq 0 ]; then
    ok "D2: TimeAgo is pinned in BOTH languages and a bad filter value means 'all'"
  else
    bad "D2: the notification helper tests failed (rc=$rc2)"
    printf '%s\n' "$OUT2" | tail -20 | sed 's/^/        /' >&2
  fi
fi

# ---------------------------------------------------------------------------
# E. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "E — registered, tracked, documented"

if git ls-files --error-unmatch "$CSS_EMBEDDED" >/dev/null 2>&1 && git ls-files --error-unmatch "$CSS_MIRROR" >/dev/null 2>&1; then
  ok "E1: both themes.css files are tracked (the mirror is what the tests read, so it must be committed)"
else
  bad "E1: a themes.css copy is untracked"
fi
if grep -q 'check_b392_notifications_panel' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in scripts/verify_pre_deploy.sh"
fi
if grep -q '^\- \*\*B392\*\*' AGENTS.md 2>/dev/null; then
  ok "E3: one-line entry in the AGENTS.md block index"
else
  bad "E3: no B392 entry in the AGENTS.md block index"
fi
if grep -q 'v1.5.131' RELEASE-NOTES.md 2>/dev/null; then
  ok "E4: RELEASE-NOTES.md carries the release entry"
else
  bad "E4: no release entry in RELEASE-NOTES.md"
fi
if grep -q 'B392' docs/ROADMAP.md 2>/dev/null; then
  ok "E5: docs/ROADMAP.md records the block"
else
  bad "E5: docs/ROADMAP.md does not record B392"
fi

printf '\n\033[1mB392: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
