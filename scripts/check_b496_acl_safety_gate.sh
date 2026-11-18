#!/usr/bin/env bash
# B496 (safety gate for the device-metadata work) — PROVE that a change cannot move the ACL before
# changing anything.
#
# The operator's condition (2026-11-18): «проверить, что не сломают текущую работу остальных устройств,
# и только тогда применять». That sentence is implementable, and this block is the implementation:
#
#   1. device metadata (os / device_type) must be DISPLAY-ONLY. Measured 2026-11-18: the only readers
#      are internal/db/node_owner_map.go (storage) and the devices template; internal/acl,
#      internal/feature/exit_rules, internal/prefixowner and internal/headscale never mention them.
#      A contract keeps it that way, because the planned B496 work adds new type values and a new
#      detection heuristic — and a heuristic that leaks into the generator would silently re-pin
#      devices that work today.
#
#   2. the generator's INPUTS must be capturable as one hash, so "did the ACL move?" is answered by a
#      command instead of an opinion: scripts/acl_inputs_snapshot.sh --save before, --diff after.
#      A non-empty diff is the list of decisions to explain before anything is applied.
#
# Contracts:
#   A. the snapshot tool exists, parses, is LF-only and git-TRACKED (trap #11)
#   B. it is READ-ONLY and covers exactly the generator's inputs (each table named)
#   C. it does NOT include device metadata — the premise of the block, stated in one place
#   D. nothing in the decision packages reads os / device_type
#   E. it refuses cleanly without a database (never a green line with nothing inspected)
#   F. B340: scratch files under mktemp, no fixed /tmp path
#   G. discoverable: registered, indexed, recorded

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

SNAP="scripts/acl_inputs_snapshot.sh"
SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-check.XXXXXX")" || { echo "  FAIL  cannot create a scratch dir"; exit 1; }
trap 'rm -rf "$SKY_TMP"' EXIT

# ═══════════════ A. the tool ═══════════════
if [ -f "$SNAP" ]; then ok "A1: scripts/acl_inputs_snapshot.sh exists"; else bad "A1: the snapshot tool is missing"; fi
if bash -n "$SNAP" 2>/dev/null; then ok "A2: it parses"; else bad "A2: it does not parse"; fi
if git ls-files --error-unmatch "$SNAP" >/dev/null 2>&1; then ok "A3: git tracks it (trap #11)"; else bad "A3: it is NOT tracked by git"; fi
cr="$(awk 'index($0, "\r") { n++ } END { print n + 0 }' "$SNAP" 2>/dev/null)"
[ "${cr:-1}" = "0" ] && ok "A4: LF-only" || bad "A4: it carries CR line endings"

# ═══════════════ B. read-only + the exact input set ═══════════════
case "$(cat "$SNAP")" in
    *'-readonly'*) ok "B1: every query runs read-only" ;;
    *) bad "B1: the tool does not open the database read-only" ;;
esac
missing=""
for t in device_rules device_exit_node_prefs user_exit_node_prefs node_owner_map prefix_owner portal_users; do
    grep -q "$t" "$SNAP" 2>/dev/null || missing="$missing $t"
done
case "$missing" in
    '') ok "B2: all six generator input tables are captured" ;;
    *)  bad "B2: the snapshot misses:$missing" ;;
esac
case "$(cat "$SNAP")" in
    *'ORDER BY'*) ok "B3: the dump is order-stable (a diff is meaningful)" ;;
    *) bad "B3: no ORDER BY — two runs of the same data would differ" ;;
esac

# ═══════════════ C. metadata excluded, on purpose ═══════════════
# The dump must not SELECT os / device_type. The words may appear in the header comment (they explain
# the exclusion) — only the SQL is checked.
if awk '/^run node_owner_map/,/^$/' "$SNAP" | grep -q 'device_type'; then
    bad "C1: the snapshot includes device_type — the premise of B496 is that it is display-only"
else
    ok "C1: the snapshot excludes device_type (display-only by contract)"
fi
if awk '/^run node_owner_map/,/^$/' "$SNAP" | grep -qE 'coalesce\(os|,os(,| )'; then
    bad "C2: the snapshot includes os"
else
    ok "C2: the snapshot excludes os"
fi

# ═══════════════ D. no decision code reads the metadata ═══════════════
leak=""
for pkg in internal/acl internal/feature/exit_rules internal/prefixowner internal/headscale; do
    [ -d "$pkg" ] || continue
    # FIELD/column use only: `.OS` (the NodeOwnerMap field) and `device_type`/`DeviceType`.
    # The bare word "os" is Go's package name (os/exec, os.Open); matching it made the first
    # version of this contract fail on IMPORT lines — the assertion was about the wrong thing.
    hits="$(grep -rnE 'device_type|DeviceType|\.OS\b' --include='*.go' "$pkg" 2>/dev/null \
        | grep -v '_test.go')"
    [ -n "$hits" ] && leak="$leak
$hits"
done
case "$leak" in
    '') ok "D1: no decision package reads os / device_type (metadata is display-only)" ;;
    *)  bad "D1: a decision package mentions device metadata:$leak" ;;
esac

# ═══════════════ E. clean refusal ═══════════════
out="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/no-such.env" bash "$SNAP" --db "$SKY_TMP/absent.db" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then ok "E1: without a database it exits 2 (no false green)"; else bad "E1: it exited $rc without a database, expected 2"; fi
case "$out" in
    *'cannot read'*) ok "E2: and it says why" ;;
    *) bad "E2: the refusal does not explain itself" ;;
esac

# ═══════════════ F. B340 ═══════════════
case "$(cat "$SNAP")" in
    *'mktemp'*) ok "F1: scratch output goes through mktemp" ;;
    *) bad "F1: no mktemp (trap #13: a fixed /tmp path is not writable for the next user)" ;;
esac
if grep -qE '/tmp/(acl|skygate-acl)' "$SNAP" 2>/dev/null; then bad "F2: it names a FIXED /tmp path"; else ok "F2: no fixed /tmp path"; fi

# ═══════════════ G. discoverability ═══════════════
if grep -q 'check_b496_acl_safety_gate.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then ok "G1: registered in the catalog"; else bad "G1: not registered"; fi
if grep -q '^\- \*\*B496\*\*' AGENTS.md 2>/dev/null; then ok "G2: the AGENTS index carries its line"; else bad "G2: no AGENTS index line"; fi
if grep -q 'B496' docs/ROADMAP.md 2>/dev/null; then ok "G3: the roadmap records it"; else bad "G3: the roadmap does not mention B496"; fi

echo
echo "  B496: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
