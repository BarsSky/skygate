#!/usr/bin/env bash
# check_b330_docs_consistency.sh
#
# 2026-10-01 (B330) — documentation consistency, measured rather than assumed.
#
# WHY THIS EXISTS
# ---------------
# The 2026-10-01 audit of the documentation found four defects that no contract
# could have caught, because no contract looked at the docs at all:
#
#   1. docs/ROADMAP.md still described the **v1.5.9** cycle ("Last updated
#      2026-09-18", "Version line v1.5.9") while the tree was at v1.5.94 — 85
#      releases of drift. Every reader was told the project stood a year behind
#      where it was, and four TD rows (TD-4, TD-19, RR-8, RR-9) claimed OPEN for
#      work that had shipped.
#   2. docs/ru/ROADMAP.md had diverged from its English original: its TD table
#      stopped at TD-20 (no TD-21/TD-22 rows and neither detail section), RR-10
#      asserted the opposite of the English text, and it carried operator SQL the
#      English one did not.
#   3. Three relative links were broken (`docs/ru/README.md` → `LICENSE` twice,
#      `docs/ru/ROADMAP.md` → `operations.md`), because `docs/ru/` is one
#      directory deeper than the files it points at.
#   4. `docs/README.md` — the catalogue that calls itself "Catalogue of every
#      document in this directory" — omitted `i18n-audit.md` and
#      `responsive-audit.md`, one of which a B-check cites as its evidence.
#
# CONTRACTS
#   A. no broken relative links anywhere under docs/
#   B. the RU roadmap mirrors the EN roadmap's section structure
#   C. both roadmaps name the newest release (the drift detector)
#   D. docs/README.md names every docs/*.md
#   E. the bilingual set the catalogue claims actually exists
#   F. tracked by git (AGENTS trap #11) and registered in the catalog

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B330: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

hdr "B330 — the documentation stays in agreement with the tree"

# --- A: no broken relative links --------------------------------------------
# Anchors (#section), absolute URLs and mailto are not filesystem paths.
BROKEN=""
while IFS= read -r md; do
  dir=$(dirname "$md")
  # Capture-then-match: never `producer | grep -q` under pipefail (AGENTS trap #9).
  LINKS="$(grep -oE '\]\([^)]*\)' "$md" 2>/dev/null || true)"
  while IFS= read -r raw; do
    [ -n "$raw" ] || continue
    link="${raw#](}"
    link="${link%)}"
    link="${link%%#*}"
    case "$link" in
      ""|"#"*|http://*|https://*|mailto:*) continue ;;
    esac
    if [ ! -e "$dir/$link" ]; then
      BROKEN="${BROKEN}${md} -> ${link}"$'\n'
    fi
  done <<< "$LINKS"
done < <(find docs -name '*.md' -type f | sort)

if [ -z "$BROKEN" ]; then
  ok "A1: every relative link under docs/ resolves to a real file"
else
  bad "A1: broken relative link(s) under docs/ — a reader following them gets a 404:"
  printf '%s' "$BROKEN" | sed 's/^/       /' >&2
fi

# --- B: the RU roadmap mirrors the EN one -----------------------------------
section_numbers() {
  grep -E '^## [0-9]+\.' "$1" 2>/dev/null | sed -E 's/^## ([0-9]+)\..*/\1/' | tr '\n' ' '
}
EN_SECTIONS="$(section_numbers docs/ROADMAP.md)"
RU_SECTIONS="$(section_numbers docs/ru/ROADMAP.md)"
if [ -n "$EN_SECTIONS" ] && [ "$EN_SECTIONS" = "$RU_SECTIONS" ]; then
  ok "B1: docs/ru/ROADMAP.md mirrors the EN section numbering ($EN_SECTIONS)"
else
  bad "B1: the RU roadmap does not mirror the EN structure (EN: '$EN_SECTIONS' RU: '$RU_SECTIONS')"
fi
EN_SUB="$(grep -cE '^### [0-9]+\.[0-9]+' docs/ROADMAP.md 2>/dev/null || echo 0)"
RU_SUB="$(grep -cE '^### [0-9]+\.[0-9]+' docs/ru/ROADMAP.md 2>/dev/null || echo 0)"
if [ "$EN_SUB" = "$RU_SUB" ]; then
  ok "B2: both roadmaps carry the same number of numbered subsections ($EN_SUB)"
else
  bad "B2: subsection count differs (EN=$EN_SUB RU=$RU_SUB) — one side was edited alone"
fi

# --- C: the drift detector ---------------------------------------------------
# The newest release heading in RELEASE-NOTES.md must appear in both roadmaps.
NEWEST="$(grep -E '^## v[0-9]+\.[0-9]+\.[0-9]+' RELEASE-NOTES.md 2>/dev/null | head -1 | sed -E 's/^## (v[0-9]+\.[0-9]+\.[0-9]+).*/\1/')"
if [ -z "$NEWEST" ]; then
  bad "C1: could not read the newest release heading from RELEASE-NOTES.md"
else
  ok "C1: newest release in RELEASE-NOTES.md is $NEWEST"
  if grep -qF "$NEWEST" docs/ROADMAP.md; then
    ok "C2: docs/ROADMAP.md names $NEWEST (it is not describing an older cycle)"
  else
    bad "C2: docs/ROADMAP.md does not mention $NEWEST — it has drifted behind the tree again"
  fi
  if grep -qF "$NEWEST" docs/ru/ROADMAP.md; then
    ok "C3: docs/ru/ROADMAP.md names $NEWEST"
  else
    bad "C3: docs/ru/ROADMAP.md does not mention $NEWEST — the RU mirror was left behind"
  fi
fi
if grep -qE '^\*\*Last updated:\*\*' docs/ROADMAP.md; then
  ok "C4: docs/ROADMAP.md carries a 'Last updated' line"
else
  bad "C4: docs/ROADMAP.md lost its 'Last updated' line — nothing dates the file"
fi

# --- D: the catalogue names every document ----------------------------------
UNCATALOGUED=""
for f in docs/*.md; do
  base="$(basename "$f")"
  [ "$base" = "README.md" ] && continue
  if ! grep -qF "$base" docs/README.md; then
    UNCATALOGUED="${UNCATALOGUED}${base} "
  fi
done
if [ -z "$UNCATALOGUED" ]; then
  ok "D1: docs/README.md names every docs/*.md (it is a catalogue, not a sample)"
else
  bad "D1: present in docs/ but absent from the catalogue: $UNCATALOGUED"
fi

# --- E: the bilingual set the catalogue claims ------------------------------
for pair in "docs/INSTALL.md docs/ru/INSTALL.md" \
            "docs/UPDATE.md docs/ru/UPDATE.md" \
            "docs/ROADMAP.md docs/ru/ROADMAP.md" \
            "README.md docs/ru/README.md"; do
  set -- $pair
  if [ -f "$1" ] && [ -f "$2" ]; then
    ok "E1: bilingual pair present: $1 <-> $2"
  else
    bad "E1: the catalogue claims $1 <-> $2 are bilingual, but a side is missing"
  fi
done

# --- F: tracked by git (trap #11) and registered -----------------------------
if git ls-files --error-unmatch scripts/check_b330_docs_consistency.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'check_b330_docs_consistency.sh' scripts/verify_pre_deploy.sh; then
  ok "F2: verify_pre_deploy.sh registers B330"
else
  bad "F2: verify_pre_deploy.sh does not register B330 — the contract would never run"
fi
if grep -q 'B330' AGENTS.md; then
  ok "F3: AGENTS.md's block index carries B330"
else
  bad "F3: AGENTS.md has no B330 entry (AGENTS rule 2)"
fi

printf '\n\033[1mB330 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
