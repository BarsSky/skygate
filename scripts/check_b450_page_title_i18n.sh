#!/usr/bin/env bash
# check_b450_page_title_i18n.sh — B450 (2026-10-30)
#
# «не вся локализация покрыта в строке состояния страницы висит переменная а не значение»
# (screenshot of the browser tab reading «title.», on /admin/update).
#
# THE PAGE TITLE WAS A CATALOG KEY. internal/feature/admin/update.go passed
#
#	"Title": "title.admin_update",
#
# into the page data, and layout.html renders `<title>{{.Title}} — Skygate</title>` verbatim, so
# the tab showed the key (truncated by the tab width to «title.»). Nothing could see it:
#
#   - TestCatalogsParity compares KEY SETS — the key exists and is translated in RU and EN;
#   - the B325 ratchet hunts hardcoded ENGLISH in templates — this was a key, not English;
#   - every render test supplies its own Title, so the real handler's value was never rendered.
#
# The fix translates the slot, and the guard is GENERAL: any `"Title": "<literal>"` in the tree
# whose literal is an existing catalogue key is a slot that forgot to translate — whatever page
# it lives on. The test proves itself (reverting the one-line fix fails it).
#
# CONTRACTS: 12.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

UPDATE=internal/feature/admin/update.go
GUARD=internal/i18n/title_keys_b450_test.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$UPDATE" "$GUARD" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b450.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the page title is a translation, not a key"
GOFMT_OUT="$(gofmt -l "$UPDATE" "$GUARD" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$UPDATE" 's.I18nT(c, "title.admin_update")'; then
  ok "A2: /admin/update translates its title"
else
  bad "A2: the update page still puts the raw key in the tab"
fi
if has "$UPDATE" '"Title":        "title.admin_update",'; then
  bad "A3: the raw-key assignment is back"
else
  ok "A3: the raw-key assignment is gone"
fi
if has "$GUARD" 'TestNoPageTitleIsARawKey_B450'; then
  ok "A4: a tree-wide guard exists (any Title slot whose literal is a catalogue key)"
else
  bad "A4: nothing would catch the next page whose title forgets to translate"
fi
if has "$GUARD" 'keysOf(t)'; then
  ok "A5: the guard reads the REAL catalogues, so a new key cannot slip past it"
else
  bad "A5: the guard does not consult the catalogues"
fi
if has "$GUARD" 'TestPageTitleMapKeysExist_B450'; then
  ok "A6: the pageTitle map is checked too (a missing key would render as the key)"
else
  bad "A6: the generic renderer's title map is unguarded"
fi

hdr "B. pinned by running the guard"
if ! command -v go >/dev/null 2>&1; then
  skip "B1: no go toolchain on PATH — cannot run TestB450 here (CI is authoritative)"
else
  go test ./internal/i18n -run 'B450' -count=1 >"$WORK/t.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "B1: both B450 tests are green"
  else
    bad "B1: B450 tests failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
  # The detector must prove itself: reintroduce the exact defect in a COPY and expect a failure.
  cp "$UPDATE" "$WORK/update.go.bak"
  sed -i 's|"Title":        s.I18nT(c, "title.admin_update"),|"Title":        "title.admin_update",|' "$UPDATE" 2>/dev/null
  go test ./internal/i18n -run 'TestNoPageTitleIsARawKey_B450' -count=1 >"$WORK/neg.log" 2>&1
  neg_rc=$?
  cp "$WORK/update.go.bak" "$UPDATE"
  if [ "$neg_rc" -ne 0 ]; then
    ok "B2: the guard FAILS on the reintroduced defect (it is not a vacuous pass)"
  else
    bad "B2: the guard passed on the broken tree — it proves nothing"
  fi
fi

hdr "C. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b450_page_title_i18n.sh >/dev/null 2>&1; then
  ok "C1: this script is tracked by git"
else
  bad "C1: this script is not tracked by git"
fi
if grep -qF 'check_b450_page_title_i18n' "$GATE" 2>/dev/null; then
  ok "C2: registered in scripts/verify_pre_deploy.sh"
else
  bad "C2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B450"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "C3: the B450 catalog description contains a backtick (TD-15)" ;;
  "")    bad "C3: the B450 catalog entry was not found" ;;
  *)     ok "C3: the B450 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B450**' "$AGENTS" 2>/dev/null; then
  ok "C4: indexed in AGENTS.md"
else
  bad "C4: no one-line index entry in AGENTS.md"
fi

printf '\n\033[1mB450 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
