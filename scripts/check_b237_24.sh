#!/bin/bash
# scripts/check_b237_24.sh — B237.24 (v1.5.2+, 2026-09-08)
# Guard against the `ghcr.io/BarsSky/skygate` mixed-case bug
# that broke the release.yml docker-build for v1.5.0 + v1.5.2.
#
# Background:
#   The v1.5.0 release workflow ran on 2026-09-04. The
#   `docker/build-push-action@v6` step failed with:
#     ERROR: failed to build: invalid tag
#       "ghcr.io/BarsSky/skygate:v1.5.0":
#       repository name must be lowercase
#   The release.yml used `${{ github.repository_owner }}`
#   directly in the tag path, which evaluates to `BarsSky`
#   (the operator's GitHub account, mixed-case). Docker
#   registry requires lowercase. The v1.5.0 release was
#   published as a draft (CI red → workflow didn't auto-publish)
#   and the operator manually edited the notes + clicked
#   "Publish" without the docker image ever being pushed.
#
#   The v1.5.2 release had the same bug. The operator
#   force-moved the tag to a new commit (218a02ac) and asked
#   me to investigate; I discovered the release.yml bug
#   via `gh run list` + `gh run view --log-failed`. The fix
#   is to lowercase the path:
#     `${{ github.repository_owner }}` → `${{ github.repository_owner | lower }}`
#   (GitHub Actions supports the `| lower` filter).
#
# This B-check pins the lowercase contract so the bug doesn't
# come back. If anyone adds a new tag in release.yml that
# forgets the `| lower` filter, this check catches it.
#
# Why this matters: the v1.5.0 + v1.5.2 docker images were
# NEVER pushed to ghcr.io. The release notes were published
# (manually) but the actual image the operator uses is
# whatever the CI built earlier (v1.5.0-alpha1 for v1.5.0,
# B237.21 for v1.5.2). Anyone who `docker pull
# ghcr.io/barssky/skygate:v1.5.2` gets a 404 because the
# release workflow never pushed it.
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

# (No global PATH fixup needed: the find_gh/find_go helpers below
# probe a few absolute Windows locations explicitly. WSL2's bash
# PATH lookup doesn't scan .exe files reliably, but explicit
# paths work.)

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP  $1"; }

# --- A. release.yml has the lowercase filter on every docker tag ---

# A.1 Count tag lines that use the pre-computed `steps.meta.outputs.lower_owner`.
# B237.24 design: the meta step pre-computes `lower_owner` from
# `GITHUB_REPOSITORY_OWNER` (lowercased via `tr '[:upper:]' '[:lower:]'`),
# and the 4 tag lines (version + latest + vX.Y + vX) all use
# `${{ steps.meta.outputs.lower_owner }}`. This is the SAFE pattern because
# `${{ ... | lower }}` works in plain expressions but NOT inside
# `format('...', arg)` calls — the format() function rejects pipes in
# its arg list. Pre-computing avoids the parse error.
A1=$(grep -E 'steps\.meta\.outputs\.lower_owner' .github/workflows/release.yml 2>/dev/null | wc -l)
# A.2 Count tag lines that still use raw `github.repository_owner` (the
# legacy bug pattern that builds `ghcr.io/BarsSky/skygate:...` and fails).
A2=$(grep -E 'ghcr\.io/.*github\.repository_owner[^.]' .github/workflows/release.yml 2>/dev/null | wc -l)
# Expectation: A1 >= 4 (at least 4 tag lines use lower_owner), A2 == 0
# (no tag line uses raw github.repository_owner).
if [ "$A1" -ge 4 ] && [ "$A2" = "0" ]; then
  ok "A.1 release.yml: all ghcr.io tag lines use steps.meta.outputs.lower_owner (A1=$A1 hits, A2=$A2 raw — 0 raw is the safety property)"
else
  bad "A.1 release.yml: A1=$A1 hits with lower_owner (expect >=4), A2=$A2 raw github.repository_owner (expect 0 raw)"
fi

# A.3 The meta step actually computes lower_owner (GITHUB_REPOSITORY_OWNER
# piped through tr '[:upper:]' '[:lower:]'). If this is missing, the
# `${{ steps.meta.outputs.lower_owner }}` references in tag lines will
# produce empty strings and the docker push will fail.
# Use grep -F (fixed string) — the literal `tr '[:upper:]' '[:lower:]'`
# has square brackets that ERE would interpret as a character class.
A3=$(grep -cF "tr '[:upper:]' '[:lower:]'" .github/workflows/release.yml 2>/dev/null)
A3=${A3:-0}
if [ "$A3" -ge 1 ]; then
  ok "A.3 release.yml: meta step computes lower_owner via tr (A3=$A3 hit(s))"
else
  bad "A.3 release.yml: meta step does NOT compute lower_owner — tag lines will be empty"
fi

# A.4 Comment in release.yml documents the lowercase fix (so future
# contributors know why this matters).
A4=$(grep -E 'lowercase' .github/workflows/release.yml 2>/dev/null | wc -l)
if [ "$A4" -ge 1 ]; then
  ok "A.4 release.yml: 'lowercase' documented in comments (A4=$A4 hit(s))"
else
  bad "A.4 release.yml: 'lowercase' not documented in comments (operator needs context to remember)"
fi

# --- B. AGENTS.md mentions B237.24 ---

B1=$(grep -E 'B237\.24' AGENTS.md 2>/dev/null | wc -l)
if [ "$B1" -ge 1 ]; then
  ok "B.1 AGENTS.md mentions B237.24 ($B1 hits)"
else
  bad "B.1 AGENTS.md must mention B237.24"
fi

# --- C. docs/PLANS.md mentions B237.24 ---

C1=$(grep -E 'B237\.24' docs/PLANS.md 2>/dev/null | wc -l)
if [ "$C1" -ge 1 ]; then
  ok "C.1 docs/PLANS.md mentions B237.24 ($C1 hits)"
else
  bad "C.1 docs/PLANS.md must mention B237.24"
fi

# --- D. Live: no remote v1.5.2 tag exists (it was deleted per operator's rule) ---

if command -v git >/dev/null 2>&1; then
  if git ls-remote origin 'refs/tags/v1.5.2' 2>/dev/null | grep -q '.'; then
    bad "D.1 remote v1.5.2 tag EXISTS — was the release workflow re-run successfully? (per operator's rule: no v1.5.2 tag if CI failed)"
  else
    ok "D.1 remote v1.5.2 tag is absent (deleted after CI failure)"
  fi
else
  skip "D.1 git not available in PATH"
fi

# --- E. Live: no v1.5.2 release published (or it's a draft without images) ---
# WSL2 + Git Bash note: `command -v gh` is unreliable because bash's
# PATH lookup doesn't scan .exe files in WSL2. We probe a few absolute
# locations instead. On native Linux CI (GitHub Actions ubuntu-24.04)
# the standard PATH works, so command -v gh also works.

find_gh() {
  if command -v gh >/dev/null 2>&1; then
    command -v gh
    return 0
  fi
  for cand in "/mnt/c/Program Files/GitHub CLI/gh.exe" \
              "/c/Program Files/GitHub CLI/gh.exe" \
              "/usr/bin/gh" "/usr/local/bin/gh"; do
    if [ -x "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done
  return 1
}

GH_BIN="$(find_gh || true)"
if [ -n "$GH_BIN" ]; then
  EOUT=$("$GH_BIN" release view v1.5.2 --json isDraft,databaseId,tagName 2>/dev/null || true)
  if echo "$EOUT" | grep -q '"isDraft":true'; then
    bad "E.1 v1.5.2 release exists as DRAFT (CI failed; clean it up)"
  elif echo "$EOUT" | grep -q '"tagName":"v1.5.2"'; then
    bad "E.1 v1.5.2 release is PUBLISHED but tag is missing — broken state"
  else
    ok "E.1 v1.5.2 release is absent (clean state)"
  fi
else
  skip "E.1 gh CLI not available in PATH"
fi

# --- F. Build / vet are clean (regression catch) ---

find_go() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return 0
  fi
  for cand in "/mnt/c/Program Files/Go/bin/go.exe" \
              "/c/Program Files/Go/bin/go.exe" \
              "/usr/local/go/bin/go" "/usr/bin/go"; do
    if [ -x "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done
  return 1
}

GO_BIN="$(find_go || true)"
if [ -n "$GO_BIN" ]; then
  if "$GO_BIN" build ./... 2>&1 | grep -q .; then
    bad "F.1 go build ./... not clean"
  else
    ok "F.1 go build ./... clean"
  fi
  if "$GO_BIN" vet ./... 2>&1 | grep -q .; then
    bad "F.2 go vet ./... not clean"
  else
    ok "F.2 go vet ./... clean"
  fi
else
  skip "F.1-F.2 go not available in PATH"
fi

echo ""
echo "  B237.24 (ghcr.io lowercase path guard): $PASS PASS, $FAIL FAIL"
exit $FAIL
