#!/usr/bin/env bash
# check_b406_domain_rows.sh
#
# 2026-10-21 (B406) — A DERIVED ROW IS A PREFIX, NOT A /32.
#
# WHY (measured on the operator's installation)
#   /admin/system_tests answered:
#     exit_rules.domain_prefix :: FAIL "8 of 12 address(es) are stale or missing"
#   while the SAME tree wrote 215 derived subnet rows that day and its
#   domain_resolve_at:* keys had been refreshed minutes earlier. Both facts were
#   true because the writer and the reader disagreed about what a derived row IS:
#     - the CDN path (B183/B274/B298) writes the provider's RANGE —
#       cdn:cloudflare:registry.npmjs.org → 104.16.0.0/12, 15 rows per domain;
#     - the plain path writes the resolved address as /32.
#   The test asked for a row whose target_value is EXACTLY ip/32, which is false
#   for every range-covered domain — so healthy rules looked rotten, and the
#   operator's only remedy would have been pressing Re-sync forever. The real
#   staleness (a domain whose row set no longer contains its address at all) was
#   buried inside the same message.
#
# CONTRACTS
#   A. the question is containment, computed on parsed prefixes
#   B. a range covers an address; an exact /32 covers only itself
#   C. only enabled rows of THIS device answer the question
#   D. a read failure is reported as a fact about the check, never as staleness
#   E. pinned, registered, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/admin/system_tests_domain_rows_b406.go ] || {
  printf 'B406: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b406.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

B406=internal/feature/admin/system_tests_domain_rows_b406.go
TEST=internal/feature/admin/system_tests_domain_rows_b406_test.go
ST=internal/feature/admin/system_tests_b394_new.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

B406_SRC="$(src "$B406")"; TEST_SRC="$(src "$TEST")"; ST_SRC="$(src "$ST")"

hdr "B406 — a derived row is a prefix, not a /32"

# ---------------------------------------------------------------------------
# A. containment, on parsed prefixes
# ---------------------------------------------------------------------------
hdr "A — the question the ACL asks"

if has 'func b406Covers(' "$B406_SRC"; then
  ok "A1: one containment helper answers the question"
else
  bad "A1: no containment helper — each caller would compare strings"
fi
if has 'netip.ParsePrefix' "$B406_SRC"; then
  ok "A2: rows are parsed as prefixes (a string compare is what produced the false report)"
else
  bad "A2: rows are not parsed — '104.16.0.0/12' would never equal '104.16.5.7/32'"
fi
if has '.Masked()' "$B406_SRC"; then
  ok "A3: host bits are masked, so a sloppy legacy row cannot widen the answer"
else
  bad "A3: prefixes are used as written"
fi

# ---------------------------------------------------------------------------
# B. the two shapes
# ---------------------------------------------------------------------------
hdr "B — a range covers, a /32 covers only itself"

if has 'RangeRowCoversAddress' "$TEST_SRC"; then
  ok "B1: the range case is pinned (this is the report the block exists for)"
else
  bad "B1: no test for the range case"
fi
if has 'ExactHostRowCoversOnlyItself' "$TEST_SRC"; then
  ok "B2: the /32 case is pinned"
else
  bad "B2: no test for the /32 case"
fi
if has 'InvalidAddressIsNeverCovered' "$TEST_SRC"; then
  ok "B3: an unparsable address is never reported as covered (that would hide a real gap)"
else
  bad "B3: an invalid address could be reported covered"
fi
if has 'NonCIDRValuesAreIgnored' "$TEST_SRC"; then
  ok "B4: non-CIDR rows are ignored, so a stale domain row cannot fake coverage"
else
  bad "B4: non-CIDR values are not covered by a test"
fi

# ---------------------------------------------------------------------------
# C. only this device, only enabled rows
# ---------------------------------------------------------------------------
hdr "C — the loader asks the right rows"

if has 'device_id = $1 AND enabled = 1' "$B406_SRC"; then
  ok "C1: the SQL filters by device and by enabled"
else
  bad "C1: the loader can read another device's rows or disabled rows"
fi
if has 'LoaderReadsOnlyThisDeviceAndOnlyEnabledRows' "$TEST_SRC"; then
  ok "C2: that filter is pinned against a real database"
else
  bad "C2: the filter is not tested"
fi
if has 'b406DevicePrefixes(ctx, s.dbc(), r.deviceID)' "$ST_SRC"; then
  ok "C3: the catalogue test uses the shared loader"
else
  bad "C3: the catalogue test still reads rows of its own"
fi

# ---------------------------------------------------------------------------
# D. a read failure is not staleness
# ---------------------------------------------------------------------------
hdr "D — a failed read is a fact about the check"

if has 'if readErr != ""' "$ST_SRC"; then
  ok "D1: a read error is separated from a stale row"
else
  bad "D1: a read error would be reported as staleness (trap #15)"
fi
if has 'could not read the device' "$ST_SRC"; then
  ok "D2: the message says which of the two happened"
else
  bad "D2: the message does not distinguish them"
fi
# 2026-10-22 (B411): the sentence was reworded, the promise was not. B411 made the
# verdict about the MECHANISM, so a stale rule is now reported as
# "<domain>: N address(es) in no enabled row [examples] — <why it will not heal>"
# instead of one "X → ip is in no enabled row" line per address: the count and the
# reason are what the operator acts on, and with a rotating provider the old shape
# filled the row with addresses that were merely newer than the last resolve. What
# this contract pins is that a genuine gap still SAYS SO in those words.
if has 'in no enabled row' "$ST_SRC"; then
  ok "D3: a genuine gap says the address is in no enabled row"
else
  bad "D3: a genuine gap is still described as an exact-row mismatch"
fi

# ---------------------------------------------------------------------------
# E. pinned, registered, documented
# ---------------------------------------------------------------------------
hdr "E — pinned, registered, documented"

if go test -count=1 -run 'TestB406' ./internal/feature/admin/ >"$SKY_TMP/b406.log" 2>&1; then
  ok "E1: the B406 tests pass"
else
  bad "E1: TestB406 fails: $(tail -3 "$SKY_TMP/b406.log" | tr '\n' ' ')"
fi
if git ls-files --error-unmatch scripts/check_b406_domain_rows.sh >/dev/null 2>&1; then
  ok "E2: this script is tracked by git (trap #11)"
else
  bad "E2: this script is not tracked"
fi
if grep -q 'check_b406_domain_rows' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E3: registered in scripts/verify_pre_deploy.sh"
else
  bad "E3: not registered in the catalog"
fi
if grep -q '^\- \*\*B406\*\*' AGENTS.md 2>/dev/null; then
  ok "E4: one-line entry in the AGENTS.md block index"
else
  bad "E4: no B406 entry in the AGENTS block index"
fi
if grep -q 'B406' docs/ROADMAP.md 2>/dev/null && grep -q 'B406' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E5: both roadmaps record the block (rule 12)"
else
  bad "E5: a roadmap does not record the block"
fi

printf '\n\033[1mB406: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
