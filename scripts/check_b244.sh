#!/usr/bin/env bash
# scripts/check_b244.sh — documentation/code consistency pin
# (B244, v1.5.2+, 2026-09-14)
#
# Background
#
# The 2026-09-14 doc audit
# (docs/LESSONS.md) surfaced
# 11 critical drift items that the operator was
# finding by hand:
#
#   D-1. PROJECT.md says Go 1.23 + SQLite, real is
#        go 1.25 + PG-only (post-v1.3.0 cutover).
#   D-2/D-3/D-7. README.md / docs/ru/README.md status
#        block claims "66/66 verify-pre checks",
#        "27 packages", "v0.33.1.17" — all stale
#        (real: 223 checks, 40+ packages, v1.5.2-alpha1).
#   D-5. README.md has a doubled "docs/"
#        link target.
#   D-6. README.md references deploy/install-docker.sh
#        that doesn't exist (real: deploy/install.sh).
#   L-1. catalog_bot.go has 6 RU keys ending in "&gt;>."
#        (extra `>` before period) instead of "&gt;.".
#   L-2. catalog_bot.go has 4 RU keys with raw "<ключ>"
#        that Telegram HTML mode rejects.
#
# Each of these drifts in silence for months because
# nothing compares docs against code. B244 turns the
# doc audit into a permanent CI gate so future drift
# fails the same verify-pre pipeline that catches the
# code regressions.
#
# Contracts (run from project root):
#
#   1. README.md:6 Go version matches go.mod
#      (allow "1.25+" / "1.25.0" / "1.25.x" / etc.)
#   2. PROJECT.md no longer says "Go 1.23" or "(SQLite)"
#   3. README.md / docs/ru/README.md / AGENTS.md have no
#      "66/66 verify-pre" literal
#   4. README.md / docs/ru/README.md have no "27 packages"
#      literal (the count is no longer accurate)
#   5. README.md / docs/ru/README.md have no "v0.33.1.17"
#      literal (status block, except in `CHANGELOG.md`
#      release history)
#   6. README.md has no "install-docker.sh" reference
#      (real file is deploy/install.sh)
#   7. README.md has no doubled "docs/"
#      path
#   8. internal/i18n/catalog_bot.go has no "&gt;>."
#      literal in any RU key
#   9. internal/i18n/catalog_bot.go has no raw "<ключ>"
#      in any RU key (must be "&lt;ключ&gt;")
#  10. verify_pre_deploy.sh registers B244
#      (this script — wires the gate into the gate)
#
# Auto-prevention rationale: each contract is a literal
# grep — if anyone reintroduces one of the drifted
# patterns, the B244 check fails on next verify-pre run
# and surfaces the file:line in the standard summary
# table.

set -u

# Resolve project root (this script lives in scripts/, so root is parent).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; NC=$'\033[0m'
PASS=0; FAIL=0
FAILS=()

# Pretty-print a single check.
check() {
  local label="$1" ok="$2" detail="${3:-}"
  if [ "$ok" = "1" ]; then
    echo "  ${GRN}PASS${NC}  $label"
    PASS=$((PASS+1))
  else
    echo "  ${RED}FAIL${NC}  $label"
    [ -n "$detail" ] && echo "        $detail"
    FAIL=$((FAIL+1))
    FAILS+=("$label")
  fi
}

# 1. README.md:6 Go version vs go.mod
GOMOD_VER=$(grep -E '^go ' go.mod 2>/dev/null | awk '{print $2}' | head -1)
README_GO=$(grep -oE 'go[- ]1\.[0-9]+(\.[0-9]+)?\+?' README.md 2>/dev/null | head -1)
GOMOD_MAJOR=$(echo "$GOMOD_VER" | cut -d. -f1,2)
README_MAJOR=$(echo "$README_GO" | grep -oE '1\.[0-9]+')
if [ -n "$GOMOD_MAJOR" ] && [ "$GOMOD_MAJOR" = "$README_MAJOR" ]; then
  check "C1 README.md Go version matches go.mod ($GOMOD_MAJOR)" 1
else
  check "C1 README.md Go version matches go.mod" 0 "go.mod=$GOMOD_MAJOR, README.md=$README_MAJOR"
fi

# 2. PROJECT.md no longer says "Go 1.23" or "(SQLite)"
PROJECT_GO_HIT=$(grep -nE 'Go 1\.23' PROJECT.md 2>/dev/null || true)
PROJECT_SQLITE_HIT=$(grep -nE '\(SQLite\)' PROJECT.md 2>/dev/null || true)
if [ -z "$PROJECT_GO_HIT" ] && [ -z "$PROJECT_SQLITE_HIT" ]; then
  check "C2 PROJECT.md no longer says Go 1.23 or (SQLite)" 1
else
  DETAIL=""
  [ -n "$PROJECT_GO_HIT" ] && DETAIL="$DETAIL Go-1.23-hit=$PROJECT_GO_HIT;"
  [ -n "$PROJECT_SQLITE_HIT" ] && DETAIL="$DETAIL SQLite-hit=$PROJECT_SQLITE_HIT;"
  check "C2 PROJECT.md no longer says Go 1.23 or (SQLite)" 0 "$DETAIL"
fi

# 3. No "66/66 verify-pre" literal in README files
STALE_66=$(grep -nE '66/66[^0-9]' README.md docs/ru/README.md AGENTS.md 2>/dev/null || true)
if [ -z "$STALE_66" ]; then
  check "C3 No '66/66' literal in README.md / docs/ru/README.md / AGENTS.md" 1
else
  check "C3 No '66/66' literal in README files" 0 "hits: $(echo "$STALE_66" | head -3 | tr '\n' '|')"
fi

# 4. No "27 packages" literal in README files
STALE_27=$(grep -nE '\b27 packages\b' README.md docs/ru/README.md AGENTS.md 2>/dev/null || true)
if [ -z "$STALE_27" ]; then
  check "C4 No '27 packages' literal in README files" 1
else
  check "C4 No '27 packages' literal in README files" 0 "hits: $(echo "$STALE_27" | head -3 | tr '\n' '|')"
fi

# 5. No "Status (v0.33.1.17)" literal in README files
#    (Historical feature mentions like "v0.33.1.17+" are
#    legitimate — this check pins only the status block.)
STALE_VER=$(grep -nE 'Status \(v0\.33\.1\.17\)' README.md docs/ru/README.md 2>/dev/null || true)
if [ -z "$STALE_VER" ]; then
  check "C5 No 'Status (v0.33.1.17)' literal in README files" 1
else
  check "C5 No 'Status (v0.33.1.17)' literal in README files" 0 "hits: $(echo "$STALE_VER" | head -3 | tr '\n' '|')"
fi

# 6. README.md has no "install-docker.sh" reference
STALE_INSTALL=$(grep -nE 'install-docker\.sh' README.md docs/ru/README.md 2>/dev/null || true)
if [ -z "$STALE_INSTALL" ]; then
  check "C6 No 'install-docker.sh' reference in README files" 1
else
  check "C6 No 'install-docker.sh' reference in README files" 0 "hits: $(echo "$STALE_INSTALL" | head -3 | tr '\n' '|')"
fi

# 7. README.md has no doubled "docs/" path (i.e. no
#    docs/docs/ or docs/internal/internal/ segment). A plain
#    `grep 'docs/'` would match every legitimate documentation
#    link, so pin the DOUBLED forms only.
DOUBLED=$(grep -nE 'docs/docs/|docs/internal/internal/|/docs/docs/' README.md docs/ru/README.md AGENTS.md 2>/dev/null || true)
if [ -z "$DOUBLED" ]; then
  check "C7 No doubled 'docs/' path in README files" 1
else
  check "C7 No doubled 'docs/' path" 0 "hits: $(echo "$DOUBLED" | head -3 | tr '\n' '|')"
fi

# 8. catalog_bot.go has no "&gt;>." literal (the L-1 typo)
GT_TYPO=$(grep -nE '&gt;>\.' internal/i18n/catalog_bot.go 2>/dev/null || true)
if [ -z "$GT_TYPO" ]; then
  check "C8 catalog_bot.go has no '&gt;>.' (L-1 typo fixed)" 1
else
  # Count distinct keys affected.
  KEY_COUNT=$(echo "$GT_TYPO" | wc -l)
  check "C8 catalog_bot.go has no '&gt;>.'" 0 "$KEY_COUNT lines affected (first: $(echo "$GT_TYPO" | head -1))"
fi

# 9. catalog_bot.go has no raw "<ключ>" outside &lt;...&gt; form (L-2)
#    Exclude lines where "<ключ" is inside backticks (markdown code — Telegram
#    HTML mode does not parse content inside <code>...</code>).
#    Examples that ARE flagged: "/login <ключ>" (raw, not in backticks).
#    Examples that are NOT flagged: "`/login <ключ>`" (markdown code).
RAW_KLUCH=$(grep -nE '<ключ[^a-zA-Z0-9;]|ключ>' internal/i18n/catalog_bot.go 2>/dev/null \
  | grep -v '&lt;ключ&gt;' \
  | grep -vE '`<|>`' || true)
if [ -z "$RAW_KLUCH" ]; then
  check "C9 catalog_bot.go has no raw '<ключ>' (L-2 typo fixed)" 1
else
  KEY_COUNT=$(echo "$RAW_KLUCH" | wc -l)
  check "C9 catalog_bot.go has no raw '<ключ>'" 0 "$KEY_COUNT lines affected (first: $(echo "$RAW_KLUCH" | head -1))"
fi

# 10. verify_pre_deploy.sh wires B244 into the gate.
if grep -qE 'check_b244\.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  check "C10 verify_pre_deploy.sh registers B244" 1
else
  check "C10 verify_pre_deploy.sh registers B244" 0 "missing 'check_b244.sh' reference"
fi

echo
echo "  B244 summary: ${GRN}${PASS} pass${NC} / ${RED}${FAIL} fail${NC}"
if [ "$FAIL" -gt 0 ]; then
  echo "  failures:"
  for f in "${FAILS[@]}"; do
    echo "    - $f"
  done
  exit 1
fi
exit 0
