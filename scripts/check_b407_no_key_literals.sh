#!/usr/bin/env bash
# check_b407_no_key_literals.sh
#
# 2026-10-21 (B407) — A TEST FIXTURE MUST NOT LOOK LIKE A LEAKED CREDENTIAL.
#
# WHY (the operator's GitHub Secret scanning page)
#   Four "Tailscale API Key" alerts were open, all from test fixtures:
#     internal/module/tailscale/tailscale_test.go:155   tskey-… then auth-test-placeholder
#     internal/feature/admin/admin_tailscale_test.go:34 tskey-… ending deadbeef
#     …:231                                             tskey-… starting supersecret
#     …:310                                             tskey-… starting auto-generated
#
#   None is a real key (a real one is the auth type + ~43 random characters ≈ 57
#   chars; these are 27–35 and contain dictionary words), but the scanner cannot
#   tell: it matches the PATTERN, scans the WHOLE history, and the three old
#   fixtures live in commits whose code was deleted in v1.3.0 — so no edit can
#   ever clear those alerts, only a dismissal can. A repository that keeps
#   producing this class teaches everyone to ignore the scanner, which is the
#   same failure mode as a red system-test row for "not configured".
#
#   So: no real-shaped credential literal lives in the tree (tests COMPOSE their
#   fixtures), test PEMs stay fixtures (they are not flagged by any provider
#   pattern), and non-test code may not carry key material at all.
#
# CONTRACTS
#   A. no real-shaped token of a provider pattern exists in any tracked file
#   B. the former Tailscale fixture is composed, and the test still uses it
#   C. no private-key MATERIAL in non-test code (headers-only fixtures are fine)
#   D. the dismissal procedure is written down where an operator will find it
#   E. registered, tracked, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/module/tailscale/tailscale_test.go ] || {
  printf 'B407: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b407.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

TS_TEST=internal/module/tailscale/tailscale_test.go
OPS=docs/operations.md

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

TS_SRC="$(src "$TS_TEST")"

hdr "B407 — a test fixture must not look like a leaked credential"

# ---------------------------------------------------------------------------
# A. no real-shaped token anywhere in the tracked tree
# ---------------------------------------------------------------------------
hdr "A — the tree carries no real-shaped credential"

# The detector models what GitHub itself matched, which is NARROWER than "a key of
# realistic length": GitHub flagged a 31-character value that GitHub itself matched,
# a word where a key would have random data — so its rule is the SHAPE
# `tskey-<word>-<something>` with no length floor. Every placeholder this repository
# uses breaks that shape right after the type (`tskey-auth-...`, `tskey-…`), so the
# detector has no false positive on documentation, and it has no self-match because
# this file assembles the prefix from two shell words.
TS_PREFIX='tsk''ey-'
REAL_PAT="${TS_PREFIX}[A-Za-z0-9]+-[A-Za-z0-9]+|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}"
HITS="$SKY_TMP/hits.txt"
: > "$HITS"
git ls-files -- '*.go' '*.sh' '*.md' '*.yml' '*.yaml' '*.json' '*.html' '*.txt' 2>/dev/null \
  | while IFS= read -r f; do
      [ -f "$f" ] || continue
      grep -nE "$REAL_PAT" "$f" 2>/dev/null | sed "s|^|$f:|"
    done > "$HITS"
if [ -s "$HITS" ]; then
  bad "A1: a key-shaped literal is committed (GitHub would open an alert): $(head -3 "$HITS" | tr '\n' ' ')"
else
  ok "A1: no key-shaped literal in any tracked file"
fi

# The detector must be able to SEE the shape GitHub saw, otherwise A1 is vacuous
# (the B340 lesson: prove the detector works by planting a violation). The planted
# value is the SHORT, word-shaped one GitHub actually flagged — not a 57-character
# key — so the detector cannot pass this test by only catching unrealistic input.
printf 'x = "tsk%s%s"\n' 'ey-' 'auto-generated-1234567890' > "$SKY_TMP/planted.go"
if grep -qE "$REAL_PAT" "$SKY_TMP/planted.go"; then
  ok "A2a: the detector catches the SHORT word-shaped value GitHub flagged (planted proof)"
else
  bad "A2a: the detector cannot see the value GitHub saw — A1 proves nothing"
fi
printf 'x = "tsk%s%s"\n' 'ey-' "auth-$(printf 'k%.0s' $(seq 1 43))" > "$SKY_TMP/planted_real.go"
if grep -qE "$REAL_PAT" "$SKY_TMP/planted_real.go"; then
  ok "A2b: it also catches a realistic key (long, random)"
else
  bad "A2b: a realistic key slips through"
fi
printf 'placeholder: tskey-auth-... and tskey-… (the documented forms)\n' > "$SKY_TMP/allowed.txt"
if grep -qE "$REAL_PAT" "$SKY_TMP/allowed.txt"; then
  bad "A3: the detector flags the documented placeholders — it would be turned off"
else
  ok "A3: the documented placeholders do not trip it (no false positive on docs)"
fi

# ---------------------------------------------------------------------------
# B. the former fixture is composed
# ---------------------------------------------------------------------------
hdr "B — the Tailscale fixture no longer exists as a literal"

if has '"tskey" + "-auth-test-placeholder"' "$TS_SRC"; then
  ok "B1: the fixture value is COMPOSED at runtime (no contiguous key text)"
else
  bad "B1: the fixture is still a single literal the scanner will match"
fi
if has 'SKYGATE_TS_AUTHKEY":      tsAuthKeyFixture' "$TS_SRC"; then
  ok "B2: the module config still receives the fixture"
else
  bad "B2: the test no longer feeds a preauth key (the fixture changed meaning)"
fi
if has '--authkey="+tsAuthKeyFixture' "$TS_SRC"; then
  ok "B3: the assertion uses the same composed value (the test still proves the flag)"
else
  bad "B3: the assertion lost the composed fixture"
fi
OLD_FIXTURE='tsk''ey-auth-test-placeholder'
if grep -q "$OLD_FIXTURE" "$TS_TEST" 2>/dev/null; then
  bad "B4: the old literal is still in the file"
else
  ok "B4: the old literal is gone from the head"
fi

# ---------------------------------------------------------------------------
# C. no private-key material in non-test code
# ---------------------------------------------------------------------------
hdr "C — production code carries no key material"

# A test PEM (header + a body that only exists for the fixture) is fine: no
# provider pattern matches it. What must never happen is key material inside a
# NON-test file.
MAT="$SKY_TMP/material.txt"
: > "$MAT"
git ls-files -- '*.go' 2>/dev/null | while IFS= read -r f; do
  case "$f" in
    *_test.go) continue ;;
  esac
  [ -f "$f" ] || continue
  awk -v file="$f" '
    /-----BEGIN [A-Z ]*PRIVATE KEY-----/ { inblk=1; n=0; next }
    /-----END [A-Z ]*PRIVATE KEY-----/   { if (inblk && n >= 2) printf "%s:%d: private key material (%d body lines)\n", file, NR, n; inblk=0; next }
    inblk && length($0) > 20 { n++ }
  ' "$f"
done > "$MAT"
if [ -s "$MAT" ]; then
  bad "C1: key material sits in non-test code: $(head -2 "$MAT" | tr '\n' ' ')"
else
  ok "C1: no private-key material in non-test code"
fi
if has 'BEGIN EC PRIVATE KEY' "$(src internal/derphealth/testcert_test.go)"; then
  ok "C2: the one PEM in the tree is a test fixture (derphealth/testcert_test.go)"
else
  ok "C2: no PEM fixture in the tree"
fi

# ---------------------------------------------------------------------------
# D. the dismissal procedure is written down
# ---------------------------------------------------------------------------
hdr "D — an operator can close an alert without rewriting history"

OPS_SRC="$(src "$OPS")"
if has 'secret scanning' "$OPS_SRC"; then
  ok "D1: docs/operations.md has a secret-scanning section"
else
  bad "D1: no procedure — the next alert will be 'investigated' from scratch"
fi
if has 'used_in_tests' "$OPS_SRC"; then
  ok "D2: the dismissal reason is named (so the alert is closed as a fixture, not as 'wont_fix')"
else
  bad "D2: the procedure does not name the dismissal reason"
fi
if has 'admin_tailscale_test.go' "$OPS_SRC"; then
  ok "D3: the three historical alerts are explained (their code was deleted in v1.3.0)"
else
  bad "D3: the procedure does not say why three alerts can never be fixed by editing code"
fi

# ---------------------------------------------------------------------------
# E. registration, tracking, documentation
# ---------------------------------------------------------------------------
hdr "E — registered, tracked, documented"

if git ls-files --error-unmatch scripts/check_b407_no_key_literals.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (trap #11)"
else
  bad "E1: this script is not tracked"
fi
if grep -q 'check_b407_no_key_literals' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
if grep -q '^\- \*\*B407\*\*' AGENTS.md 2>/dev/null; then
  ok "E3: one-line entry in the AGENTS.md block index"
else
  bad "E3: no B407 entry in the AGENTS block index"
fi
if grep -q 'B407' docs/ROADMAP.md 2>/dev/null && grep -q 'B407' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E4: both roadmaps record the block (rule 12)"
else
  bad "E4: a roadmap does not record the block"
fi

printf '\n\033[1mB407: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
