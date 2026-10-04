#!/usr/bin/env bash
# check_b337_gofmt_ratchet.sh
#
# 2026-10-01 (B337) — TD-21: make `gofmt` enforceable as a RATCHET instead of a
# rule nobody can satisfy.
#
# THE PROBLEM
# -----------
# AGENTS rule 3 has said "every file you TOUCH must be gofmt-clean" for a while,
# and NOTHING enforced it: `grep -rln gofmt scripts/ .github/ Makefile` found
# only two comments. Measured on the reference VM 2026-10-01 with go1.25.4:
#
#   847 tracked .go files, 276 not gofmt-clean (32.6%) — 275 after the
#   2026-10-01 telegram/commands_user.go split paid one entry down
#
# Renormalising all of them at once is a ~30k-line whitespace-only commit: it
# would bury any real change in the same push and poison `git blame` for every
# line of those files. So the drift is frozen instead — exactly like the B325
# i18n budgets, where the counts are pinned and may only fall.
#
# THE SHAPE
# ---------
# `scripts/gofmt_legacy_allowlist.txt` lists the frozen set (with the measurement
# and the rules in its header). This contract then enforces four things:
#
#   1. every tracked .go file NOT in the list is gofmt-clean — i.e. anything new
#      or anything you touch is held to the rule;
#   2. every file IN the list that has become clean must be REMOVED — the list
#      may only shrink, which is what makes it a ratchet rather than a permanent
#      exemption, and the fix direction is always available to the author;
#   3. the list may never grow, and its size is asserted against the frozen
#      count;
#   4. the list is sorted and unique, so a change to it reviews as one line per
#      file rather than as a reshuffle.
#
# It also carries the file-level half of rule 3 that a whole-tree ratchet cannot
# express: the files changed in THIS tree (staged vs HEAD) must be clean even if
# they are allow-listed, so "I only edited it a little" is not an exemption.
#
# CONTRACTS
#   A. the allow-list exists and is well-formed (sorted, unique, no duplicates)
#   B. the frozen count matches, and the list does not grow
#   C. every non-listed tracked .go file is gofmt-clean
#   D. every listed file that is now clean has been removed (ratchet teeth)
#   E. the .go files changed in this tree are clean, allow-listed or not
#   F. tracked by git (AGENTS trap #11), registered, and in the index

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B337: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LIST=scripts/gofmt_legacy_allowlist.txt
# The frozen count. LOWER IT when you pay entries down; the contract refuses to
# let it rise.
#
# 2026-10-01: 276 → 275. Refactor Phase D split
# internal/telegram/commands_user.go (1983 lines) into nine files; the 752-line
# remainder is gofmt-clean, so contract D1 demanded its line leave the list.
#
# 2026-10-01: 275 → 274. The same phase `gofmt -w`-ed
# internal/feature/admin/system_tests.go (80 diff lines of struct-tag alignment)
# while splitting it into four files — a split cannot produce clean output from a
# drifted source, and contract D1 then demands the removal.
#
# 2026-10-01: 274 → 272. B341 touched two more allow-listed files
# (internal/feature/exit_rules/reconciler_b229_test.go and
# reconciler_b237_7_test.go) when it renegotiated their "silent no-op" contract
# into a named skip; rule 3 (and contract E1) require every file you TOUCH to be
# gofmt-clean, so both were formatted and left the list.
#
# 2026-10-02: 272 → 271. B342 touched internal/feature/admin/cluster.go (two new
# page-data fields for the onboarding artifact) and formatted it in the same
# batch; contract D1 caught that the file was now clean and still listed.
#
# 2026-10-02: 271 → 270. B343 added two i18n keys to
# internal/i18n/catalog_exit_rules.go; the key that is longer than any existing
# one re-aligned the whole map block, and gofmt then reported the file clean —
# contract D1 again (a touched file must leave the list). The 230-line alignment
# diff is the cost of keeping a touched file gofmt-clean inside an aligned map.
#
# 2026-10-02: 270 → 268. The same block wired the marker into BOTH rule pages and
# formatted internal/feature/exit_rules/form_admin.go and form_my.go in the same
# batch; D1 caught both.
#
# 2026-10-04: 268 → 267. B346 added the pinned-release keys to
# internal/i18n/catalog_update.go; the map's alignment was reformatted in the same
# change, so the file is gofmt-clean and left the list (D1 is the contract that
# makes "I touched it, I formatted it" non-optional).
#
# 2026-10-04: 267 → 266. B349 added a column to a query in
# internal/db/queries.go and gofmt then reported the file clean (it had been frozen
# as drifted), so it left the list too. D1 caught it in CI on the B349 commit —
# the ratchet working as designed, not a regression.
FROZEN=266

# Resolve gofmt the way verify_pre_deploy.sh resolves go: the Windows install
# lives under a path with a space, so `command -v` is not enough.
GOFMT=""
if command -v gofmt >/dev/null 2>&1; then
  GOFMT="gofmt"
elif [ -n "${GO:-}" ] && [ -x "$(dirname "$GO")/gofmt" ]; then
  GOFMT="$(dirname "$GO")/gofmt"
else
  for cand in "/c/Program Files/Go/bin/gofmt.exe" "/usr/local/go/bin/gofmt" "/usr/lib/go/bin/gofmt"; do
    [ -x "$cand" ] && GOFMT="$cand" && break
  done
fi

hdr "B337 — the gofmt ratchet (TD-21)"

# ---------------------------------------------------------------------------
hdr "A. the allow-list is well-formed"
# ---------------------------------------------------------------------------
if [ -f "$LIST" ]; then
  ok "A1: $LIST exists"
else
  bad "A1: $LIST is missing — without the frozen set there is no ratchet, only an unenforceable rule"
fi

ENTRIES="$(grep -v '^#' "$LIST" 2>/dev/null | grep -v '^[[:space:]]*$')"
N="$(printf '%s\n' "$ENTRIES" | grep -c .)"
if [ "$N" -gt 0 ]; then
  ok "A2: the list carries $N frozen files"
else
  bad "A2: the list has no entries — either the drift is gone (then say so and drop the ratchet) or the file was emptied by accident"
fi
if [ "$(printf '%s\n' "$ENTRIES" | LC_ALL=C sort)" = "$(printf '%s\n' "$ENTRIES")" ]; then
  ok "A3: the list is sorted under LC_ALL=C (a change to it reviews as one line per file)"
else
  bad "A3: the list is not sorted under LC_ALL=C — re-sort it (LC_ALL=C sort) so a diff shows what actually changed, and so the order does not depend on the host locale"
fi
DUPS="$(printf '%s\n' "$ENTRIES" | LC_ALL=C sort | uniq -d)"
if [ -z "$DUPS" ]; then
  ok "A4: the list has no duplicate entries"
else
  bad "A4: the list repeats $(printf '%s' "$DUPS" | tr '\n' ' ')"
fi
STALE="$(printf '%s\n' "$ENTRIES" | while read -r f; do [ -n "$f" ] && [ ! -f "$f" ] && echo "$f"; done)"
if [ -z "$STALE" ]; then
  ok "A5: every listed file still exists"
else
  bad "A5: the list names files that no longer exist: $(printf '%s' "$STALE" | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------
hdr "B. the frozen count is honest"
# ---------------------------------------------------------------------------
if [ "$N" -le "$FROZEN" ]; then
  ok "B1: the frozen set is $N (budget $FROZEN — it may only fall)"
else
  bad "B1: the frozen set GREW to $N (budget $FROZEN). Adding a file to the allow-list is how a ratchet dies: fix the file, or if it is genuinely pre-existing drift, say so in the commit and raise the budget deliberately"
fi
if grep -q "MEASURED 2026-10-01" "$LIST"; then
  ok "B2: the list records the measurement it came from (date, Go version, file count)"
else
  bad "B2: the list does not record its measurement — the next reader cannot tell what the budget was for"
fi

# ---------------------------------------------------------------------------
hdr "C. everything NOT frozen is clean"
# ---------------------------------------------------------------------------
if [ -z "$GOFMT" ]; then
  skip "C: gofmt is not on this host — run this check on the VM"
else
  ALL="$(git ls-files '*.go' 2>/dev/null)"
  ALLN="$(printf '%s\n' "$ALL" | grep -c .)"
  DIRTY="$(printf '%s\n' "$ALL" | xargs "$GOFMT" -l 2>/dev/null | sort)"
  DIRTYN="$(printf '%s\n' "$DIRTY" | grep -c .)"
  NEWDIRTY="$(printf '%s\n' "$DIRTY" | while read -r f; do
    [ -z "$f" ] && continue
    grep -qxF "$f" <<< "$ENTRIES" || echo "$f"
  done)"
  NEWN="$(printf '%s\n' "$NEWDIRTY" | grep -c .)"
  if [ "$NEWN" -eq 0 ]; then
    ok "C1: all $((ALLN - DIRTYN)) non-frozen tracked .go files are gofmt-clean (of $ALLN)"
  else
    bad "C1: $NEWN tracked .go file(s) are not gofmt-clean and are NOT in the allowlist:"
    printf '%s\n' "$NEWDIRTY" | head -10 | sed 's/^/        /' >&2
    echo "        Fix them with 'gofmt -w <file>' (AGENTS rule 3), or — only for genuine pre-existing drift — add them to $LIST and raise the budget in this script deliberately." >&2
  fi

  # ---------------------------------------------------------------------------
  hdr "D. the ratchet's teeth: a fixed file must leave the list"
  # ---------------------------------------------------------------------------
  FIXED="$(printf '%s\n' "$ENTRIES" | while read -r f; do
    [ -z "$f" ] && continue
    [ -f "$f" ] || continue
    if "$GOFMT" -l "$f" 2>/dev/null | grep -q .; then :; else echo "$f"; fi
  done)"
  FIXEDN="$(printf '%s\n' "$FIXED" | grep -c .)"
  if [ "$FIXEDN" -eq 0 ]; then
    ok "D1: every allow-listed file is still genuinely drifted (nothing is frozen that no longer needs to be)"
  else
    bad "D1: $FIXEDN allow-listed file(s) are now gofmt-CLEAN and must be removed from $LIST (then lower the budget):"
    printf '%s\n' "$FIXED" | head -10 | sed 's/^/        /' >&2
    echo "        A list that keeps files after they are fixed is an exemption, not a ratchet." >&2
  fi

  # ---------------------------------------------------------------------------
  hdr "E. the files changed in THIS tree are clean, allow-listed or not"
  # ---------------------------------------------------------------------------
  CHANGED="$( { git diff --cached --name-only --diff-filter=ACM 2>/dev/null; git diff --name-only --diff-filter=ACM 2>/dev/null; } | sort -u | grep '\.go$' || true)"
  CHANGEDN="$(printf '%s\n' "$CHANGED" | grep -c .)"
  if [ "$CHANGEDN" -eq 0 ]; then
    skip "E: no .go file is changed in this tree"
  else
    BADCHANGED="$(printf '%s\n' "$CHANGED" | while read -r f; do
      [ -z "$f" ] && continue
      [ -f "$f" ] || continue
      if "$GOFMT" -l "$f" 2>/dev/null | grep -q .; then echo "$f"; fi
    done)"
    if [ -z "$BADCHANGED" ]; then
      ok "E1: all $CHANGEDN .go file(s) changed in this tree are gofmt-clean (the file-level half of rule 3)"
    else
      bad "E1: a file changed in this tree is not gofmt-clean: $(printf '%s' "$BADCHANGED" | tr '\n' ' ') — run 'gofmt -w' on it; pre-existing drift is not a licence to add more"
    fi
  fi
fi

# ---------------------------------------------------------------------------
hdr "F. tracked by git, registered, and in the index"
# ---------------------------------------------------------------------------
if git ls-files --error-unmatch "$LIST" >/dev/null 2>&1; then
  ok "F1: $LIST is tracked by git"
else
  bad "F1: $LIST is NOT tracked by git (AGENTS trap #11: .gitignore can eat a file, and an untracked allowlist silently disables the ratchet)"
fi
if git ls-files --error-unmatch scripts/check_b337_gofmt_ratchet.sh >/dev/null 2>&1; then
  ok "F2: this script is tracked by git"
else
  bad "F2: scripts/check_b337_gofmt_ratchet.sh is NOT tracked by git"
fi
if grep -q 'run_check "B337"' scripts/verify_pre_deploy.sh; then
  ok "F3: scripts/verify_pre_deploy.sh registers B337"
else
  bad "F3: scripts/verify_pre_deploy.sh does not register B337 — the contract would never run"
fi
if grep -q '\*\*B337\*\*' AGENTS.md; then
  ok "F4: AGENTS.md's block index carries B337"
else
  bad "F4: AGENTS.md has no B337 entry (AGENTS rule 2)"
fi
if grep -q 'B337' AGENTS.md; then
  ok "F5: rule 3's region points at the ratchet"
else
  bad "F5: rule 3 does not mention B337 — the rule still reads as unenforceable"
fi

printf '\n\033[1mB337 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
