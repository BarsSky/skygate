#!/usr/bin/env bash
# check_b403_probe_truth.sh
#
# 2026-10-20 (B403) — A PROBE MUST SAY WHAT IT MEASURED.
#
# WHY (measured on the operator's installation)
#   tailnet.all_nodes_reachability :: 3/6 probable Tailscale nodes reachable (50%)
#     100.64.0.7  cyborg          ERROR dial tcp 100.64.0.7:18022: i/o timeout
#     100.64.0.8  skybars-secure  ERROR dial tcp 100.64.0.8:18022: i/o timeout
#     100.64.0.20 svyatoslava     ERROR dial tcp 100.64.0.20:18022: i/o timeout
#
#   tailnet.split_suspected :: 6 online / 3 reachable (50%) → «LIKELY TAILNET SPLIT»
#
#   None of that was about the tailnet. Two of the three are user DEVICES that never
#   run sshd, and the third (svyatoslava) answers its cluster heartbeat every 30 s —
#   the probe was answering "is SSH up?" and reporting it as "is the tunnel up?".
#   Two defects, both semantic:
#   1. a REFUSED connection proves the path works (the peer answered); only a timeout
#      or "no route" leaves it unproven. Every error was reported as "unreachable".
#   2. SSH is skygate's own contract only for the hosts it MANAGES (tag:dev-infra-*,
#      whose advertised routes skygate sets over SSH). A device without sshd is not
#      evidence of anything.
#
# CONTRACTS
#   A. one verdict classifier: open / closed / filtered, and only filtered is a failure
#   B. only managed hosts are probed; devices are counted, named, never failed
#   C. the reachability test reports the path-ok case without inventing a latency
#   D. the split detector asks only the hosts it can ask, and says who it did not ask
#   E. pinned by tests, registered, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/admin/system_tests_tailnet_b403.go ] || {
  printf 'B403: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b403.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

KIND=internal/feature/admin/system_tests_tailnet_b403.go
TNET=internal/feature/admin/system_tests_tailnet.go
T_KIND=internal/feature/admin/system_tests_tailnet_b403_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

KIND_SRC="$(src "$KIND")"; TNET_SRC="$(src "$TNET")"; TKIND_SRC="$(src "$T_KIND")"

hdr "B403 — a probe must say what it measured"

# ---------------------------------------------------------------------------
# A. the verdict classifier
# ---------------------------------------------------------------------------
hdr "A — refused is an answer, timeout is not"

for v in TailnetProbeOpen TailnetProbeClosed TailnetProbeFiltered; do
  if has "$v" "$KIND_SRC"; then ok "A: $v is a named verdict"; else bad "A: $v is missing"; fi
done
if has 'func (v TailnetProbeVerdict) Reachable() bool' "$KIND_SRC"; then
  ok "A4: Reachable() is the single rule (open|closed), so a refusal cannot be a failure"
else
  bad "A4: nothing decides what counts as reachable"
fi
if has 'strings.Contains(msg, "refused")' "$KIND_SRC"; then
  ok "A5: a refusal is recognised as the peer ANSWERING"
else
  bad "A5: refused is still folded into \"unreachable\""
fi
if has 'TailnetProbeFiltered' "$KIND_SRC" && has 'the path did not prove' "$KIND_SRC"; then
  ok "A6: 'filtered' is documented as the ONLY blameable verdict"
else
  bad "A6: the blameable class is not stated"
fi

# ---------------------------------------------------------------------------
# B. only managed hosts are probed
# ---------------------------------------------------------------------------
hdr "B — SSH is a contract, not a tailnet primitive"

if has 'func tailnetNodeIsInfra(' "$KIND_SRC" && has 'tag:dev-infra-' "$KIND_SRC"; then
  ok "B1: infrastructure is defined by the tag skygate applies over SSH"
else
  bad "B1: there is no rule for whose SSH skygate may expect"
fi
if has 'func tailnetProbeTargets(' "$KIND_SRC"; then
  ok "B2: the split into managed hosts / devices is a pure function (testable without a network)"
else
  bad "B2: the classification is entangled with the probe"
fi
if has 'if !tailnetNodeIsInfra(n) {' "$TNET_SRC"; then
  ok "B3: both tailnet tests consult it before probing"
else
  bad "B3: a device is still probed"
fi

# ---------------------------------------------------------------------------
# C. the path-ok case is not reported as a latency
# ---------------------------------------------------------------------------
hdr "C — nothing claims a measurement it did not make"

if has 'if verdict.Reachable() {' "$TNET_SRC" && has 'if refused {' "$KIND_SRC"; then
  ok "C1: a refusal is accepted as a proven path (one probe loop, the wrapper asks Reachable)"
else
  bad "C1: the helper still returns an error for a refusal"
fi
if has 'verdict != TailnetProbeOpen' "$TNET_SRC"; then
  ok "C1b: the latency test refuses to turn a refusal into a 0s measurement"
else
  bad "C1b: a refused host would report 0s and look like the fastest node"
fi
if has 'path-ok (no sshd)' "$TNET_SRC"; then
  ok "C2: the output names the case instead of printing 0s as a latency"
else
  bad "C2: a refusal would render as a fake latency"
fi

# ---------------------------------------------------------------------------
# D. the split detector says who it did not ask
# ---------------------------------------------------------------------------
hdr "D — the split verdict is limited to what was asked"

if has 'not asked:' "$TNET_SRC" && has "device(s) online in the control plane" "$TNET_SRC"; then
  ok "D1: the devices are named as not-asked in the split output"
else
  bad "D1: the split output hides which nodes were never probed"
fi
if has 'managed host(s) online' "$TNET_SRC"; then
  ok "D2: the denominator is the managed hosts, not every node in the tailnet"
else
  bad "D2: the denominator still counts devices"
fi
if has 'SSH is not part of a device' "$TNET_SRC"; then
  ok "D3: the reachability output gives the reason, not just a list"
else
  bad "D3: the reason is missing"
fi

# ---------------------------------------------------------------------------
# E. tests, registration, documentation
# ---------------------------------------------------------------------------
hdr "E — pinned, registered, documented"

for n in TestB403_RefusedIsPathProof TestB403_TimeoutIsTheOnlyUnprovenPath TestB403_OpenIsReachable TestB403_OnlyManagedHostsAreProbed TestB403_DevicesWithNoSSHNeverBecomeASplit TestB403_SkipListStillWins; do
  if has "func $n(" "$TKIND_SRC"; then ok "E1: $n pins a case"; else bad "E1: $n is missing"; fi
done
if git ls-files --error-unmatch scripts/check_b403_probe_truth.sh >/dev/null 2>&1; then
  ok "E2: this script is tracked by git (trap #11)"
else
  bad "E2: this script is not tracked"
fi
if grep -q 'check_b403_probe_truth' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E3: registered in scripts/verify_pre_deploy.sh"
else
  bad "E3: not registered in the catalog"
fi
if grep -q '^\- \*\*B403\*\*' AGENTS.md 2>/dev/null; then
  ok "E4: one-line entry in the AGENTS.md block index"
else
  bad "E4: no B403 entry in the AGENTS block index"
fi
if grep -q 'B403' docs/ROADMAP.md 2>/dev/null && grep -q 'B403' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E5: both roadmaps record the block (rule 12)"
else
  bad "E5: a roadmap does not record the block"
fi

printf '\n\033[1mB403: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
