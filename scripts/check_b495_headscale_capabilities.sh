#!/usr/bin/env bash
# B495 (TD-5 / RR-5 preparation) — the headscale capability table and the 0.30 plan.
#
# Measured 2026-11-18: the production host runs headscale v0.29.3 and upstream's newest release is
# v0.29.7 — **0.30 does not exist yet**. "Blocked on 0.30" was carried as folklore: nothing in the tree
# said which version unlocks which capability, so the operator (or the next agent) had to re-derive it
# every time. This block puts the table in code and the plan in docs, and keeps 0.29 support explicit.
#
# Contracts:
#   A. the capability table exists, is tracked, and is pure (no probing inside it)
#   B. both capability minimums are named as constants a caller can quote
#   C. an UNPARSEABLE version claims NOTHING (never "assume the newest")
#   D. the table is pinned by tests for the real lines: 0.29.3 (this host), 0.29.7 (upstream now),
#      0.30 (the RR-5 gate), a pre-release of it, a major bump, and garbage
#   E. the boot log states the capabilities, so the journal answers the question
#   F. the docs carry the MEASURED versions, the 0.29.3→0.29.7 routine move and the 0.30 plan
#   G. the docs state that skygate drives BOTH lines from one binary (no 0.30-only branch)
#   H. the roadmap's TD-5/RR-5 row records the measurement instead of the folklore
#   I. discoverable: registered, indexed, noted

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

LIB="internal/headscale/compat_b495.go"
TEST="internal/headscale/compat_b495_test.go"
# the BOOT WIRING lives in the cmd/skygate package; read the SURFACE (B339), never one file
MAIN_GLOB="cmd/skygate/*.go"
DOC="docs/headscale-versions.md"
RM="docs/ROADMAP.md"

# ═══════════════ A. the table ═══════════════
if [ -f "$LIB" ]; then ok "A1: internal/headscale/compat_b495.go exists"; else bad "A1: the capability table is missing"; fi
if git ls-files --error-unmatch "$LIB" >/dev/null 2>&1; then
    ok "A2: git tracks it (trap #11)"
else
    bad "A2: the table is NOT tracked by git"
fi
case "$(cat "$LIB")" in
    *'func CapabilitiesFor('*) ok "A3: the answer is a pure function of the version" ;;
    *) bad "A3: no pure CapabilitiesFor" ;;
esac
# purity: the table must not do I/O — a probe belongs outside it
if grep -qE 'http\.|exec\.|os\.Open|net\.' "$LIB" 2>/dev/null; then
    bad "A4: the table performs I/O — a capability answer must be a pure function of the version"
else
    ok "A4: the table is pure (no probing inside it)"
fi

# ═══════════════ B. the minimums are named ═══════════════
case "$(cat "$LIB")" in
    *'MinVersionExtraRecords'*) ok "B1: the per-user-DNS minimum (RR-5) is a named constant" ;;
    *) bad "B1: the RR-5 minimum is not named" ;;
esac
case "$(cat "$LIB")" in
    *'MinVersionPolicyDatabase'*) ok "B2: the policy-database minimum is a named constant" ;;
    *) bad "B2: the policy-database minimum is not named" ;;
esac

# ═══════════════ C. unknown claims nothing ═══════════════
case "$(cat "$LIB")" in
    *'return Capabilities{}'*) ok "C1: an unparseable version returns the zero capability set" ;;
    *) bad "C1: the unknown-version branch does not return the zero value — it may assume capabilities" ;;
esac
case "$(cat "$LIB")" in
    *'Known'*) ok "C2: Known tells a caller whether the answer is real" ;;
    *) bad "C2: there is no Known flag" ;;
esac

# ═══════════════ D. the tests pin the real lines ═══════════════
if [ -f "$TEST" ]; then ok "D1: the tests exist"; else bad "D1: no test file"; fi
missing=""
for v in '"0.29.3"' '"0.29.7"' '"0.30.0"'; do
    grep -q "$v" "$TEST" 2>/dev/null || missing="$missing $v"
done
case "$missing" in
    '') ok "D2: the table is pinned for this host, upstream-now and the RR-5 gate" ;;
    *)  bad "D2: the tests do not pin:$missing" ;;
esac
tout="$(go test ./internal/headscale/ -run 'B495' -count=1 2>&1)"; trc=$?
if [ "$trc" -eq 0 ]; then ok "D3: the capability tests pass here"; else bad "D3: the capability tests fail: $(printf '%s' "$tout" | tail -2 | tr '\n' ' ')"; fi

# ═══════════════ E. the boot log ═══════════════
# shellcheck source=lib/gosurface.sh
. "$(dirname "$0")/lib/gosurface.sh"
gosurface BOOT_SURFACE $MAIN_GLOB
MAIN_TS="${BOOT_SURFACE:-}"
if [ -n "$MAIN_TS" ] && [ -f "$MAIN_TS" ] && grep -q 'headscale-capabilities:' "$MAIN_TS" 2>/dev/null; then
    ok "E1: the boot log states the capabilities"
else
    bad "E1: the journal cannot answer 'can this daemon do X' (searched the cmd/skygate surface)"
fi
if [ -n "$MAIN_TS" ] && [ -f "$MAIN_TS" ] && grep -q 'CapabilitiesFor(hsVer.Version)' "$MAIN_TS" 2>/dev/null; then
    ok "E2: it is computed from the DETECTED version"
else
    bad "E2: the log does not use the detected version"
fi

# ═══════════════ F/G. the docs ═══════════════
if [ -f "$DOC" ]; then ok "F1: docs/headscale-versions.md exists"; else bad "F1: no headscale-versions guide"; fi
case "$(cat "$DOC" 2>/dev/null)" in
    *'v0.29.3'*) ok "F2: it names the version measured on the host" ;;
    *) bad "F2: the doc does not record what is actually running" ;;
esac
case "$(cat "$DOC" 2>/dev/null)" in
    *'v0.29.7'*) ok "F3: it names upstream's newest release (the measurement, not a rumour)" ;;
    *) bad "F3: the doc does not record what upstream has" ;;
esac
case "$(cat "$DOC" 2>/dev/null)" in
    *'does not exist yet'*|*'does not exist'*) ok "F4: it says plainly that 0.30 is not released" ;;
    *) bad "F4: the doc does not state the availability of 0.30" ;;
esac
case "$(cat "$DOC" 2>/dev/null)" in
    *'Rollback'*) ok "F5: the upgrade path carries a rollback" ;;
    *) bad "F5: no rollback in the upgrade plan" ;;
esac
case "$(cat "$DOC" 2>/dev/null)" in
    *'keep 0.29'*|*'Keep 0.29'*|*'BOTH lines'*)
        ok "G1: the doc states that ONE binary drives both headscale lines" ;;
    *) bad "G1: the doc does not commit to keeping 0.29 support" ;;
esac

# ═══════════════ H. the roadmap row records the measurement ═══════════════
case "$(cat "$RM" 2>/dev/null)" in
    *'v0.29.7'*) ok "H1: the roadmap's blocked row carries the measured releases" ;;
    *) bad "H1: the roadmap still says only 'blocked on 0.30'" ;;
esac

# ═══════════════ I. discoverability ═══════════════
if grep -q 'check_b495_headscale_capabilities.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "I1: registered in the catalog"
else
    bad "I1: not registered in the catalog"
fi
if grep -q '^\- \*\*B495\*\*' AGENTS.md 2>/dev/null; then ok "I2: the AGENTS index carries its line"; else bad "I2: no AGENTS index line"; fi
if grep -q 'B495' "$RM" 2>/dev/null; then ok "I3: the roadmap records the block"; else bad "I3: the roadmap does not mention B495"; fi

echo
echo "  B495: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
