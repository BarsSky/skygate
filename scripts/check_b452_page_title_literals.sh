#!/usr/bin/env bash
# check_b452_page_title_literals.sh — B452 (2026-10-30)
#
# «не вся локализация покрыта» — the second half of the page-title work. B450 stopped a handler from
# putting a CATALOG KEY in the tab; what remained (listed there as debt) are the titles written as
# ENGLISH LITERALS in fifteen handlers across six packages:
#
#	"Exit Nodes" · "Integration status" · "Service control" · "Tailscale" · "Telegram" ·
#	"Account" · "API Tokens" · "Exit Rules" · "Node Load" · "Notifications" · "Мониторинг"
#
# A Russian operator read them in English in the browser tab. Each handler reaches the language
# differently (auth.Service, exit_rules.Service, the handler struct, a bare claims value), so editing
# fifteen call sites would have been fifteen chances to get it wrong — and the NEXT page would still
# write a literal. The layout is the ONE place that renders the title, so the translation lives
# there: `{{pt .Title}}` resolves a key (B450), a known literal (B452), or leaves data alone.
#
# CONTRACTS: 12.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

RESOLVER=internal/handlers/page_title_b452.go
RESOLVER_T=internal/handlers/page_title_b452_test.go
TPL=internal/handlers/templates.go
LAYOUT=internal/handlers/templates/layout.html
I18N=internal/i18n/catalog_exit_rules.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$RESOLVER" "$RESOLVER_T" "$TPL" "$LAYOUT" "$I18N" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b452.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the title is resolved in one place"
GOFMT_OUT="$(gofmt -l "$RESOLVER" "$RESOLVER_T" "$TPL" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$LAYOUT" '{{pt .Title}} — Skygate'; then
  ok "A2: the layout renders the title THROUGH the resolver"
else
  bad "A2: the layout still prints .Title verbatim — literals stay English"
fi
if has "$LAYOUT" '<title>{{.Title}}'; then
  bad "A3: the verbatim title is back in the layout"
else
  ok "A3: no verbatim title left"
fi
if has "$TPL" '"pt":' && has "$TPL" 'resolvePageTitle,'; then
  ok "A4: the helper is registered in the real funcmap"
else
  bad "A4: the layout would fail to parse (pt not defined)"
fi
if has "$RESOLVER" 'func resolvePageTitle(value string) string'; then
  ok "A5: the resolver is a named, testable function"
else
  bad "A5: no resolver"
fi
if has "$RESOLVER" 'looksLikeCatalogKey'; then
  ok "A6: a key-shaped title is translated at render time too (the B450 shape, fixed twice)"
else
  bad "A6: a forgotten translation would still render as a key"
fi

hdr "B. the literal table is honest"
COUNT="$(grep -c 'title\.' "$RESOLVER" 2>/dev/null || true)"
if [ "${COUNT:-0}" -ge 12 ]; then
  ok "B1: the table maps at least twelve literals (${COUNT} key references)"
else
  bad "B1: only ${COUNT:-0} key reference(s) — the sweep shrank"
fi
for k in title.admin_service_control title.my_exit_rules title.node_load title.notifications; do
  if grep -qF "\"$k\"" "$I18N" 2>/dev/null; then
    ok "B2: $k exists in the catalogue file"
  else
    bad "B2: $k is missing — a literal would map to nothing"
  fi
done
N="$(grep -cF "title.notifications" "$I18N" 2>/dev/null || true)"
if [ "${N:-0}" -ge 2 ]; then
  ok "B3: new keys come in RU + EN pairs"
else
  bad "B3: title.notifications has only ${N:-0} definition(s)"
fi
if has internal/feature/exit_rules/form_my_rules.go '"Title":             "My exit rules",'; then
  ok "B4: the user page no longer claims the ADMIN title"
else
  bad "B4: the my-page still passes the admin's literal, so one key cannot serve both"
fi

hdr "C. pinned by running the tests"
if ! command -v go >/dev/null 2>&1; then
  skip "C1: no go toolchain on PATH — cannot run TestB452 here (CI is authoritative)"
else
  go test ./internal/handlers -run 'B452' -count=1 >"$WORK/t.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C1: TestB452 green (literals, keys, data left alone, the layout wiring)"
  else
    bad "C1: TestB452 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
fi

hdr "D. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b452_page_title_literals.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: this script is not tracked by git"
fi
if grep -qF 'check_b452_page_title_literals' "$GATE" 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B452"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "D3: the B452 catalog description contains a backtick (TD-15)" ;;
  "")    bad "D3: the B452 catalog entry was not found" ;;
  *)     ok "D3: the B452 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B452**' "$AGENTS" 2>/dev/null; then
  ok "D4: indexed in AGENTS.md"
else
  bad "D4: no one-line index entry in AGENTS.md"
fi

printf '\n\033[1mB452 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
