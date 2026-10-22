#!/usr/bin/env bash
# check_b409_cdn_gap_coverage.sh
#
# 2026-10-22 (B409) — A CDN MARKER MUST NOT END THE CONVERSATION.
#
# WHY (live run on v1.5.153, as the operator)
#   exit_rules.domain_prefix :: 5 of 12 address(es) are stale or missing:
#     agent.minimax.io         → 62.115.253.59  is in no enabled row
#     cdn-registry-1.docker.io → 54.157.162.28  is in no enabled row
#     cdn-registry-1.docker.io → 100.60.245.70  is in no enabled row
#
#   The rows were fresh (5 Akamai ranges, 6 rows for the other domain) and the
#   resolver had run that day. Root cause, from sync_domain.go:
#
#       existingMarker := … parent_domain LIKE 'cdn:%:<domain>' …
#       if isCDNMarker(existingMarker) { continue }   // ← BEFORE any DNS lookup
#
#   Once a domain had a CDN marker it was never resolved again: the marker was
#   treated as the whole answer, while the domain's answers moved to edges outside
#   the frozen range set. Enabled rule, rows in the database, no traffic routed.
#
# CONTRACTS
#   A. a covered answer stays covered, an uncovered one becomes a gap
#   B. the marker no longer short-circuits DNS
#   C. gap rows live under the SAME marker (so B298 dedup keeps them)
#   D. pinned, registered, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/exit_rules/sync_domain_cdn_gap_b409.go ] || {
  printf 'B409: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b409.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

GAP=internal/feature/exit_rules/sync_domain_cdn_gap_b409.go
SYNC=internal/feature/exit_rules/sync_domain.go
TEST=internal/feature/exit_rules/sync_domain_cdn_gap_b409_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

GAP_SRC="$(src "$GAP")"; SYNC_SRC="$(src "$SYNC")"; TEST_SRC="$(src "$TEST")"

hdr "B409 — a CDN marker must not end the conversation"

# ---------------------------------------------------------------------------
# A. containment
# ---------------------------------------------------------------------------
hdr "A — covered stays covered, uncovered becomes a gap"

if has 'func b409Uncovered(' "$GAP_SRC"; then
  ok "A1: one pure containment helper decides coverage"
else
  bad "A1: no containment helper"
fi
if has 'netip.ParsePrefix' "$GAP_SRC" && has 'p.Contains(addr)' "$GAP_SRC"; then
  ok "A2: coverage is computed from parsed prefixes, not strings"
else
  bad "A2: coverage is not computed from prefixes"
fi
if has '!addr.Is4()' "$GAP_SRC"; then
  ok "A3: IPv6 and unparsable answers never become gap rows"
else
  bad "A3: an IPv6 answer could become a bogus /32"
fi
if has 'sort.Strings(out)' "$GAP_SRC"; then
  ok "A4: the gap list is deterministic (stable logs, stable tests)"
else
  bad "A4: the gap order depends on map iteration"
fi

# ---------------------------------------------------------------------------
# B. the marker no longer short-circuits DNS
# ---------------------------------------------------------------------------
hdr "B — the resolver keeps asking"

# The old shape was: marker check → bare `continue`. The new shape calls the gap
# reconciler first. Detect the regression by looking for the marker branch WITHOUT it.
if has 's.b409ReconcileGaps(' "$SYNC_SRC"; then
  ok "B1: the marker branch resolves and reconciles gaps"
else
  bad "B1: the marker branch still short-circuits before DNS — the measured bug"
fi
if has 'func b409ResolveIPv4(' "$GAP_SRC" && has 'net.LookupHost' "$GAP_SRC"; then
  ok "B2: it resolves the domain even when a marker exists"
else
  bad "B2: no resolution on the marked path"
fi
if has 'return 0, 0' "$GAP_SRC"; then
  ok "B3: an unreachable resolver is not a verdict about coverage (retried next tick)"
else
  bad "B3: a lookup failure could be read as 'covered'"
fi
if has 's.markDomainResolved(d.domain, nowUnix)' "$SYNC_SRC"; then
  ok "B4: the interval gate still applies (no per-tick DNS storm)"
else
  bad "B4: the domain would be resolved on every pass"
fi

# ---------------------------------------------------------------------------
# C. gap rows stay dedup-visible
# ---------------------------------------------------------------------------
hdr "C — gap rows live under the same marker"

if has 'd.userID, d.deviceID, d.exitNode, ip+"/32", d.action, d.deviceIP, marker,' "$GAP_SRC"; then
  ok "C1: a gap /32 is written under the CDN marker (B298 partition keeps it)"
else
  bad "C1: the gap row has another parent — dedup would delete it next tick"
fi
if has "target_value LIKE '%/32'" "$GAP_SRC"; then
  ok "C2: only /32 gap rows are read, so the CDN RANGE rows are never touched"
else
  bad "C2: the range rows could be mistaken for gaps"
fi
if has 'DELETE FROM device_rules WHERE id=$1' "$GAP_SRC"; then
  ok "C3: a moved answer's stale gap row is removed"
else
  bad "C3: a stale gap row would keep granting an address nobody answers on"
fi

# ---------------------------------------------------------------------------
# D. pinned, registered, documented
# ---------------------------------------------------------------------------
hdr "D — pinned, registered, documented"

for n in TestB409_AnswerInsideARangeIsCovered TestB409_AnswerOutsideEveryRangeIsAGap \
         TestB409_NoStoredRangesMeansEveryAnswerIsAGap TestB409_IPv6AndGarbageAreIgnored \
         TestB409_GapListIsStableSorted; do
  if has "func $n(" "$TEST_SRC"; then ok "D1: $n pins a case"; else bad "D1: $n is missing"; fi
done
if go test -count=1 -run 'TestB409' ./internal/feature/exit_rules/ >"$SKY_TMP/b409.log" 2>&1; then
  ok "D2: the B409 tests pass"
else
  bad "D2: TestB409 fails: $(tail -3 "$SKY_TMP/b409.log" | tr '\n' ' ')"
fi
if git ls-files --error-unmatch scripts/check_b409_cdn_gap_coverage.sh >/dev/null 2>&1; then
  ok "D3: this script is tracked by git (trap #11)"
else
  bad "D3: this script is not tracked"
fi
if grep -q 'check_b409_cdn_gap_coverage' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D4: registered in scripts/verify_pre_deploy.sh"
else
  bad "D4: not registered in the catalog"
fi
if grep -q '^\- \*\*B409\*\*' AGENTS.md 2>/dev/null; then
  ok "D5: one-line entry in the AGENTS.md block index"
else
  bad "D5: no B409 entry in the AGENTS block index"
fi
if grep -q 'B409' docs/ROADMAP.md 2>/dev/null && grep -q 'B409' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "D6: both roadmaps record the block (rule 12)"
else
  bad "D6: a roadmap does not record the block"
fi

printf '\n\033[1mB409: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
