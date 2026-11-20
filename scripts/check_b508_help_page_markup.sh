#!/usr/bin/env bash
# check_b508_help_page_markup.sh — B508 (2026-11-20)
#
# Operator report: «на странице помощи много не правильно сделанных ссылок на страницы в
# открытую висит html синтаксис».
#
# MEASURED. Two separate defects were hiding behind that sentence.
#
# (1) RAW MARKUP. The help page teaches with formatting — <b>, <code>, <a href> — and
#     those tags live INSIDE the i18n catalogue strings. `t`/`tf` return a plain `string`,
#     which html/template escapes, so the tags reached the browser as text. Measured:
#     144 such usages on the help page rendered escaped and only 3 piped through
#     `safeHTML` — which is why the defect looked random (a <b> in one card worked, the
#     <a href> in the next did not). Thirteen OTHER templates carried another 44 of the
#     same. `{{tf … .BaseURL | safeHTML}}` was worse than ugly: `safeHTML` over a
#     formatted argument is an XSS hole, because BaseURL is built from the request's
#     Host header.
#
# (2) LINKS TO PAGES THAT DO NOT EXIST. `/docs/windows-client.md` was linked from BOTH
#     the help page and /my/exit-rules, and no route has ever served /docs/ — the button
#     promised a "full Windows guide" and opened a 404. The sidebar «Справка» and the
#     footer «docs» pointed at `/help`, which is not registered either.
#
# THE FIX: `th` (trusted-markup translate — the catalogue's own markup is trusted, every
# ARGUMENT is escaped), used for every HTML-bearing catalogue string in every template;
# the dead links now point at pages that exist (/my/exit-rules/help, the generated route
# script); and five templates that carried a UTF-8 BOM had it stripped.
#
# Contracts:
#   A. the helper exists, escapes its arguments, and `t`/`tf` still do not trust markup
#   B. the measurement is ZERO: no template renders an HTML-bearing catalogue key escaped
#   C. the measurement is ZERO: every internal href (template or help catalogue) is a
#      registered route, static asset or templated route
#   D. the specific dead links are gone (and cannot come back as text)
#   E. the page renders markup as markup (behaviour, via the Go render test)
#   F. templates carry no UTF-8 BOM
#   G. discoverable: registered, tracked, indexed, roadmap, release notes

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

# B446's resolver: a missing interpreter is an environment fact, never a regression.
. scripts/lib/python_audit.sh
PY="$(skygate_python)"

TPL=internal/handlers/templates
HELP=$TPL/exit_rules_help.html
FUNCS=internal/handlers/templates.go
AUDIT=scripts/audit_help_markup_b508.py
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
GATE=scripts/verify_pre_deploy.sh

for f in "$HELP" "$FUNCS" "$AUDIT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B508 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# ── A. the helper ───────────────────────────────────────────────────────────
case "$(cat "$FUNCS")" in
  *'"th": func(key string, args ...any) template.HTML {'*)
    ok "A1: the th helper exists and returns trusted markup" ;;
  *) bad "A1: no th helper in the funcmap" ;;
esac
case "$(cat "$FUNCS")" in
  *'htmlpkg.EscapeString(fmt.Sprint(a))'*)
    ok "A2: th ESCAPES its arguments (BaseURL comes from the request Host header)" ;;
  *) bad "A2: th interpolates arguments raw — that is the XSS hole it was written to close" ;;
esac
case "$(cat "$FUNCS")" in
  *'"t": func(key string) string {'*) ok "A3: t still returns a plain string (plain-text pages stay escaped)" ;;
  *) bad "A3: t no longer returns a string — the default became trusting" ;;
esac
case "$(cat "$FUNCS")" in
  *'"safeJSON"'*|*'"safeHTML": func(s string) template.HTML { return template.HTML(s) }'*)
    ok "A4: the pre-existing helpers are untouched" ;;
  *) bad "A4: an existing helper disappeared" ;;
esac
# The help page must not go back to the escaping helper for a catalogue string.
case "$(cat "$HELP")" in
  *'{{t "help.exit_rules_help.'*) bad "B1: the help page renders a catalogue string through t again (its tags would show as text)" ;;
  *'{{tf "help.exit_rules_help.'*) bad "B1: the help page renders a catalogue string through tf again" ;;
  *) ok "B1: every help string goes through th" ;;
esac
case "$(cat "$HELP")" in
  *'| safeHTML'*) bad "B2: the page still pipes a catalogue string through safeHTML" ;;
  *) ok "B2: no safeHTML pipes left on the page (th already trusts the catalogue)" ;;
esac

# ── B/C. the two measurements ──────────────────────────────────────────────
if [ -z "$PY" ]; then
  skip "B3: no python interpreter on this host — the escaped-markup measurement cannot run"
  skip "C1: no python interpreter on this host — the link measurement cannot run"
else
  OUT="$("$PY" "$AUDIT" 2>&1)"
  RC=$?
  case "$OUT" in
    *"escaped markup: 0"*) ok "B3: no template renders an HTML-bearing catalogue key escaped" ;;
    *) bad "B3: raw markup still reaches the browser:
$(printf '%s\n' "$OUT" | head -20)" ;;
  esac
  case "$OUT" in
    *"dead links: 0"*) ok "C1: every internal href names a registered route (or a static asset)" ;;
    *) bad "C1: the pages contain links nobody serves:
$(printf '%s\n' "$OUT" | sed -n '/DEAD LINKS/,$p' | head -20)" ;;
  esac
  [ "$RC" -eq 0 ] || true
fi

# ── D. the specific offenders ──────────────────────────────────────────────
case "$(cat internal/handlers/templates/exit_rules.html)" in
  *'docs/windows-client.md'*) bad "D1: /my/exit-rules still links /docs/windows-client.md (no route serves /docs/)" ;;
  *) ok "D1: the Windows button points at the generated route script, not a missing file" ;;
esac
case "$(cat "$TPL/layout.html")" in
  *'href="/help"'*) bad "D2: the sidebar/footer still link /help, which is not registered" ;;
  *) ok "D2: the Help link points at /my/exit-rules/help" ;;
esac
grep -q 'href="/my/exit-rules/help"' "$TPL/layout.html" \
  && ok "D3: the sidebar links the real help page" \
  || bad "D3: the sidebar no longer links the help page at all"

# ── E. the page renders ────────────────────────────────────────────────────
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/handlers/ -run 'B508' -count=1 2>&1)"
  if grep -qE '^(ok|PASS)' <<< "$OUT"; then
    ok "E1: the B508 render tests are green (no escaped tags, real links, admin-only card, arguments escaped)"
  else
    bad "E1: go test -run B508 failed:
$(printf '%s\n' "$OUT" | tail -20)"
  fi
  OUT2="$(go test ./internal/handlers/ ./internal/feature/admin/ -count=1 2>&1)"
  if grep -qE '^(FAIL|--- FAIL)' <<< "$OUT2"; then
    bad "E2: the template change broke a page render elsewhere:
$(printf '%s\n' "$OUT2" | grep -E '^(FAIL|--- FAIL)' | head -10)"
  else
    ok "E2: every other page still renders (the funcmaps learned th)"
  fi
else
  skip "E1: no Go toolchain on this host"
  skip "E2: no Go toolchain on this host"
fi

# ── F. no BOM in a template ────────────────────────────────────────────────
if [ -z "$PY" ]; then
  skip "F1: no python interpreter — BOM scan skipped"
else
  BOM="$("$PY" - "$TPL" <<'PY'
import glob, os, sys
root = sys.argv[1]
bad = []
for path in glob.glob(os.path.join(root, "**", "*.html"), recursive=True):
    if open(path, "rb").read(3) == b"\xef\xbb\xbf":
        bad.append(path.replace("\\", "/"))
print(" ".join(bad))
PY
)"
  if [ -z "$BOM" ]; then
    ok "F1: no template carries a UTF-8 BOM (five did; measured and stripped)"
  else
    bad "F1: templates carry a UTF-8 BOM: $BOM"
  fi
fi

# ── G. discoverable ────────────────────────────────────────────────────────
if grep -q 'check_b508_help_page_markup.sh' "$GATE"; then
  ok "G1: registered in the pre-deploy catalog"
else
  bad "G1: not registered in $GATE"
fi
if git ls-files --error-unmatch scripts/check_b508_help_page_markup.sh >/dev/null 2>&1; then
  ok "G2: git tracks this script (trap #11)"
else
  bad "G2: this script is not tracked by git (trap #11)"
fi
if git ls-files --error-unmatch "$AUDIT" >/dev/null 2>&1; then
  ok "G3: git tracks the audit"
else
  bad "G3: the audit script is not tracked by git"
fi
if grep -q 'B508' "$AGENTS"; then
  ok "G4: the AGENTS block index carries its line"
else
  bad "G4: no B508 line in $AGENTS"
fi
for rd in docs/ROADMAP.md docs/ru/ROADMAP.md; do
  if grep -q 'B508' "$rd"; then
    ok "G5: $rd records the block"
  else
    bad "G5: $rd does not mention B508"
  fi
done
if grep -q 'B508' "$NOTES"; then
  ok "G6: the release notes describe it"
else
  bad "G6: no B508 section in $NOTES"
fi

echo
echo "B508 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
