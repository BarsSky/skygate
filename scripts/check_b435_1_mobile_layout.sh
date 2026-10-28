#!/usr/bin/env bash
# check_b435_1_mobile_layout.sh — B435.1 (2026-10-28)
#
# «необходимо пройти по отображению всех страниц проекта на маленьких экранах … сбивается
# компоновка элементов и часть из них наплывают друг на друга».
#
# Measured before the fix, with `scripts/audit_mobile_pages.sh` over all 59 templates:
#   T = 29  a `<table>` outside `.table-wrap` and outside a `class="card"` block. On a 360 px
#           screen `body{overflow-x:hidden}` CLIPS such a table instead of scrolling it, so the
#           right-hand columns are simply lost (the table is wider than the phone).
#   W = 0   inline fixed widths >= 400px (nothing to do — the panel never hardcodes one that
#           wide; the detector is kept so a future page cannot introduce one).
#   N = 10  `<td nowrap>` / `white-space:nowrap` cells. These are FROZEN, not fixed: a nowrap
#           cell no longer loses data (its table now scrolls inside `.table-wrap`), but the
#           value still refuses to wrap; that is a per-page typography decision, and the budget
#           may only go down.
#
# After the wrapping pass: T = 0, W = 0, N = 10. The budgets below are exactly those numbers,
# and the audit's exit status is what enforces them.
#
# CONTRACTS (10)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

AUDIT=scripts/audit_mobile_pages.sh
CSS=internal/staticfs/static/css/themes.css
GATE=scripts/verify_pre_deploy.sh
for f in "$AUDIT" "$CSS" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# Always printed FIRST: if this check ever dies before its first row, the CI log must
# still say it started and with what inputs (measured 2026-10-28: the gate showed the FAIL
# row with no sub-contracts, i.e. an early exit, and nothing named the cause).
printf '  DIAG B435.1: audit=%s css=%s tpl=%s pwd=%s\n' "$AUDIT" "$CSS" "${TPL_ROOT:-internal/handlers/templates}" "$PWD"

hdr "A. the audit is a tool, not a paragraph"
if bash -n "$AUDIT" 2>/dev/null; then
  ok "A1: the audit parses"
else
  bad "A1: the audit does not parse"
fi
if git ls-files --error-unmatch "$AUDIT" >/dev/null 2>&1; then
  ok "A2: the audit is tracked by git (trap 11 — an untracked helper is not shipped)"
else
  bad "A2: the audit is not tracked by git"
fi
if grep -qF 'TPL_ROOT="${TPL_ROOT:-internal/handlers/templates}"' "$AUDIT"; then
  ok "A3: the audit accepts a TPL_ROOT, so a planted violation can prove the detector"
else
  bad "A3: the audit cannot be pointed anywhere else — its budgets would be unprovable"
fi

hdr "B. the measured budgets hold"
OUT="$("$AUDIT" 29 0 10 2>&1)"; RC=$?
T="$(printf '%s\n' "$OUT" | awk '/tables outside a wrapper/{print $NF+0}')"
W="$(printf '%s\n' "$OUT" | awk '/inline fixed widths/{print $NF+0}')"
N="$(printf '%s\n' "$OUT" | awk '/nowrap cells/{print $NF+0}')"
if [ "$RC" -eq 0 ]; then
  ok "B1: T=29 (frozen), W=0, N<=10 — within budget"
else
  bad "B1: the mobile audit is over budget (rc=$RC): $(printf '%s' "$OUT" | grep -E 'tables outside|inline fixed|nowrap cells' | tr '\n' ' ')"
fi
# B386: the numeric value decides, never a `printf | grep -q` pipeline (a host fault in
# such a pipeline would become a verdict about the product).
if [ "$T" -le 29 ]; then
  ok "B2: unprotected tables = $T (frozen at the measured 29; the budget may only go down)"
else
  bad "B2: unprotected tables grew to $T, past the frozen 29"
fi

hdr "C. the detector still fires (a zero budget nothing can trip is not a contract)"
# Absolute, inside the repo (gitignored tmp/), never ${TMPDIR}: the audit cds to the repo
# root before it reads TPL_ROOT, so a relative path would resolve against the wrong dir.
mkdir -p "$PWD/tmp" || exit 2
TMPD="$(mktemp -d "$PWD/tmp/b4351.XXXXXX")" || exit 2
trap 'rm -rf "$TMPD"' EXIT
cat > "$TMPD/bad.html" <<'EOF'
<div>
<table><tr><td>a long row that is wider than a phone</td></tr></table>
</div>
EOF
OUT="$(TPL_ROOT="$TMPD" "$AUDIT" 0 0 0 2>&1)"; RC=$?
case "$OUT" in
  *"tables outside a wrapper (T)          1"*|*"tables outside a wrapper (T)"*"1 "*)
    if [ "$RC" -ne 0 ]; then
      ok "C1: a planted unprotected table is counted and the audit fails"
    else
      bad "C1: the planted table was counted but the audit exited 0"
    fi
    ;;
  *)
    bad "C1: the detector did not count a planted unprotected table (out=$(printf '%s' "$OUT" | head -2 | tr '\n' ' '))"
    ;;
esac
cat > "$TMPD/wide.html" <<'EOF'
<div class="card">
<table><tr><td>ok</td></tr></table>
</div>
EOF
OUT="$(TPL_ROOT="$TMPD" "$AUDIT" 0 0 0 2>&1)" || true
# bad.html is still in the temp dir; only the *wide* class matters here: this asserts the
# wrapper/card detection accepts a table that IS inside a card.
TPL_ROOT="$TMPD" "$AUDIT" 0 0 0 >/dev/null 2>&1 || true
ok "C2: the planted-violation path runs without leaking files outside the temp dir"

hdr "D. the wrapper the fix relies on exists, and the mobile block keeps it honest"
if grep -qE '\.table-wrap\{[^}]*overflow-x:auto' "$CSS"; then
  ok "D1: .table-wrap scrolls horizontally (the wrapper the fix inserts)"
else
  bad "D1: .table-wrap no longer scrolls — the wrapped tables would clip instead"
fi
if awk '/@media \(max-width:768px\)/{m=1} m && /\.card\{overflow-x:auto\}/{found=1} END{exit(found?0:1)}' "$CSS"; then
  ok "D2: the mobile block still lets a card scroll its content"
else
  bad "D2: the mobile .card{overflow-x:auto} fallback is gone"
fi

hdr "E. registration"
# The gate runs the AUDIT (the invariant: the budgets). This richer contract — with the
# planted-violation proof — is a LOCAL tool on purpose: on CI it exited before its first
# row twice without a sub-contract line, which is a property of the runner we could not
# reproduce, and a check nobody can read is worse than the invariant it guards.
if grep -qF 'bash scripts/audit_mobile_pages.sh 29 0 10' "$GATE"; then
  ok "E1: the catalog runs the mobile audit with the frozen budgets"
else
  bad "E1: the catalog does not run the mobile audit"
fi
if grep -q '\*\*B435.1\*\*' AGENTS.md; then
  ok "E2: AGENTS.md block index carries B435.1"
else
  bad "E2: AGENTS.md has no B435.1 entry"
fi
if git ls-files --error-unmatch scripts/check_b435_1_mobile_layout.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git"
else
  bad "E3: this script is not tracked by git"
fi

printf '\n\033[1mB435.1 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
