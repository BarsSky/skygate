#!/usr/bin/env bash
# scripts/check_b378_push_gate_and_host_artefacts.sh
#
# B378 (2026-10-12) — A PUSH IS A GATE, AND A HOST ARTEFACT IS NOT A PRODUCT DEFECT.
#
# WHY THIS EXISTS (measured, not theorised)
# ----------------------------------------
# Releasing v1.5.116, the pre-push hook was bypassed with `--no-verify`. The honest
# reason is worth writing down, because none of it was a product defect:
#
#   1. `check_b352_ownership_convergence.sh` D2 needs a WORKING python3, and asked
#      `command -v python3`. On Windows `python3` resolves to the Microsoft Store alias
#      stub, which runs nothing and exits 49 — so the parsed value was empty, the `case`
#      fell through to `*)`, and the check announced «the button's body still reaches the
#      effective-target resolver» about code that was correct. A host artefact printed as
#      a product fact: the exact class B327/B336 forbid.
#   2. `check_b_prod_health.sh` read `${USER}` bare under `set -u`; a PowerShell-launched
#      Git Bash exports PATH but not USER, so the script died with «USER: unbound
#      variable» AFTER its four HTTP contracts had PASSed — and B327's behavioural half,
#      which drives that very script, went red with it.
#   3. `check_b337_gofmt_ratchet.sh` C1 asked `gofmt` about files in a Windows working
#      tree that carried CRLF while the INDEX is LF (`.gitattributes: eol=lf`). It named
#      22 correctly formatted files as drifted. The B375 commit message had already
#      recorded this as «the KNOWN local C1 CRLF false failure» — i.e. it was tolerated
#      for a release instead of fixed.
#   4. B144 needs `LC_ALL=C.UTF-8` on MSYS (AGENTS trap #10) and the hook does not set it.
#
# Every one of those is a reason to fix the CAUSE. The rule this block pins is the
# operator's: a push is always checked, the failure is diagnosed, and it is fixed
# immediately — bypassing the gate is never the fix.
#
# CONTRACTS
#   A. the hook runs the full catalog and aborts on a RED verdict
#   B. the hook names the failing checks (scripts/push_gate_verdict.sh, behaviourally)
#   C. the hook is honest about the host it runs on (locale, MSYSTEM detection intact)
#   D. the three host-artefact defects stay fixed (behavioural, each one reproduces the
#      original false FAIL's conditions)
#   E. bookkeeping (tracked, registered, indexed)
#
# NOTE FOR FUTURE EDITORS: this check NEVER executes `.githooks/pre-push`, because that
# would run the whole catalog from inside the catalog. The hook is pinned statically and
# its verdict reader is exercised behaviourally instead.

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B378: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# Per-run scratch (AGENTS trap #13: never a FIXED /tmp path).
SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
RESTORE_FILE=""
cleanup() {
  [ -n "$RESTORE_FILE" ] && [ -f "$RESTORE_FILE" ] && git checkout -- "$RESTORE_FILE" 2>/dev/null
  rm -rf "$SKY_TMP"
}
trap cleanup EXIT

HOOK=".githooks/pre-push"
VERDICT="scripts/push_gate_verdict.sh"

hdr "B378 — a push is gated, and this host's artefacts are not product defects"

# ---------------------------------------------------------------------------
hdr "A. the hook runs the catalog and aborts on RED"
# ---------------------------------------------------------------------------
if grep -q 'bash scripts/verify_pre_deploy.sh' "$HOOK"; then
  ok "A1: the hook runs the full catalog"
else
  bad "A1: the hook no longer runs scripts/verify_pre_deploy.sh — the push is unchecked"
fi
# The catalog returns its own verdict (B335/TD-22), and the hook must branch on it.
if grep -qE 'CATALOG_RC=|VERDICT_RC=' "$HOOK" && grep -q 'exit 1' "$HOOK"; then
  ok "A2: the hook branches on the catalog's verdict and aborts the push (exit 1)"
else
  bad "A2: the hook cannot abort on a RED catalog — a red commit would be pushed"
fi
if grep -q 'PIPESTATUS\[0\]' "$HOOK"; then
  ok "A3: the catalog's status is read from PIPESTATUS, not from the tee in the pipeline"
else
  bad "A3: the hook reads \$? after a pipeline — tee's status would decide the gate"
fi
if bash -n "$HOOK" 2>/dev/null; then
  ok "A4: the hook parses (bash -n)"
else
  bad "A4: the hook does not parse — git would refuse every push"
fi

# ---------------------------------------------------------------------------
hdr "B. a RED verdict must NAME the failing checks"
# ---------------------------------------------------------------------------
if [ -f "$VERDICT" ]; then
  ok "B1: $VERDICT exists"
else
  bad "B1: $VERDICT is missing — the hook cannot say WHY the push was refused"
fi
if grep -q 'push_gate_verdict.sh' "$HOOK"; then
  ok "B2: the hook consults the verdict reader"
else
  bad "B2: the hook does not use the verdict reader (the abort would be mute)"
fi

# Behavioural: plant the three log shapes and require the right verdict + reason.
if [ -f "$VERDICT" ] && command -v bash >/dev/null 2>&1; then
  printf '  PASS    : 425\n  FAIL    : 0\n  TIMEOUT : 0\nGATE PASSED\n' > "$SKY_TMP/green.log"
  printf '  PASS    : 424\n  FAIL    : 1\n  TIMEOUT : 0\nGATE FAILED\n  failing: B337 B237.7\n' > "$SKY_TMP/red.log"
  printf 'CATALOG ERROR: run_check "B999" has no command argument\n' > "$SKY_TMP/crash.log"
  printf '  PASS    : 425\n  FAIL    : 0\n  TIMEOUT : 1\nGATE FAILED\n  failing: B261\n' > "$SKY_TMP/timeout.log"

  g_out="$(bash "$VERDICT" "$SKY_TMP/green.log" 2>&1)"; g_rc=$?
  r_out="$(bash "$VERDICT" "$SKY_TMP/red.log" 2>&1)"; r_rc=$?
  c_out="$(bash "$VERDICT" "$SKY_TMP/crash.log" 2>&1)"; c_rc=$?
  t_out="$(bash "$VERDICT" "$SKY_TMP/timeout.log" 2>&1)"; t_rc=$?

  if [ "$g_rc" -eq 0 ]; then
    ok "B3: a green log passes the verdict reader"
  else
    bad "B3: a green log was refused (rc=$g_rc): $g_out"
  fi
  if [ "$r_rc" -ne 0 ] && printf '%s' "$r_out" | grep -q 'B337'; then
    ok "B4: a RED log is refused AND the failing checks are named ($(printf '%s' "$r_out" | tr '\n' ' '))"
  else
    bad "B4: a RED log did not name the failing checks (rc=$r_rc): $r_out"
  fi
  if [ "$t_rc" -ne 0 ] && printf '%s' "$t_out" | grep -q 'B261'; then
    ok "B5: a TIMEOUT counts as a failure and is named (B335's rule, kept by the reader)"
  else
    bad "B5: a TIMEOUT did not fail the verdict (rc=$t_rc): $t_out"
  fi
  if [ "$c_rc" -ne 0 ]; then
    ok "B6: a log with NO verdict block is a FAILURE, not a pass (the pre-B335 symptom)"
  else
    bad "B6: a log with no verdict block was accepted — a crashed catalog would look green"
  fi
else
  skip "B3-B6: $VERDICT or bash unavailable"
fi

# ---------------------------------------------------------------------------
hdr "C. the hook is honest about the host it runs on"
# ---------------------------------------------------------------------------
if grep -q 'LC_ALL=C.UTF-8' "$HOOK"; then
  ok "C1: the hook sets LC_ALL=C.UTF-8 for MSYS where grep -P needs it (trap #10)"
else
  bad "C1: the hook leaves the locale to the host — B144 fails on Git Bash for a locale artefact"
fi
if grep -q 'MSYSTEM' "$HOOK"; then
  ok "C2: the Git-Bash detection B13 pins is still present"
else
  bad "C2: the MSYSTEM detection was lost (B13)"
fi
if grep -qE 'LC_ALL.*:-' "$HOOK"; then
  ok "C3: an explicit LC_ALL from the operator is never overridden"
else
  bad "C3: the hook overwrites LC_ALL unconditionally — that would defeat a deliberate locale"
fi

# ---------------------------------------------------------------------------
hdr "D. the three host-artefact defects stay fixed (each reproduces its original condition)"
# ---------------------------------------------------------------------------

# D1 — a non-functional python3 must SKIP B352's dynamic half, never invent a product FAIL.
D1_TMP="$SKY_TMP/fakepy"
mkdir -p "$D1_TMP"
printf '#!/bin/sh\nexit 49\n' > "$D1_TMP/python3"
chmod +x "$D1_TMP/python3"
d1_out="$(PATH="$D1_TMP:$PATH" bash scripts/check_b352_ownership_convergence.sh 2>&1)"; d1_rc=$?
if printf '%s' "$d1_out" | grep -q 'reaches the effective-target resolver'; then
  bad "D1: a host whose python3 runs nothing still produces B352's «reaches the effective-target resolver» FAIL — a host artefact reported as a product defect (B327/B336)"
elif [ "$d1_rc" -ne 0 ]; then
  bad "D1: B352 failed for another reason with a broken python3 (rc=$d1_rc): $(printf '%s' "$d1_out" | grep -a FAIL | head -3 | tr '\n' ' ')"
else
  ok "D1: with a python3 that runs nothing, B352 SKIPs its dynamic half with a named reason and still passes on its static pins"
fi

# D2 — check_b_prod_health.sh must survive an unset USER/LOGNAME.
if command -v curl >/dev/null 2>&1; then
  d2_out="$(env -u USER -u LOGNAME SKYGATE_PROD_URL=http://127.0.0.1:1 bash scripts/check_b_prod_health.sh 2>&1)"; d2_rc=$?
  if printf '%s' "$d2_out" | grep -q 'unbound variable'; then
    bad "D2: check_b_prod_health.sh dies with «unbound variable» when USER is unset (it did, under set -u, after its HTTP contracts had passed)"
  elif [ "$d2_rc" -ne 0 ]; then
    bad "D2: check_b_prod_health.sh exited $d2_rc against an unreachable target — a dead port must SKIP, not FAIL (B327)"
  else
    ok "D2: with USER and LOGNAME unset and an unreachable target, check_b_prod_health.sh SKIPs and exits 0"
  fi
else
  skip "D2: curl unavailable"
fi

# D3 — B337's ratchet must judge the REPOSITORY, not this checkout's line endings.
CAND=""
while IFS= read -r f; do
  [ -f "$f" ] || continue
  grep -qxF "$f" scripts/gofmt_legacy_allowlist.txt && continue
  if ! gofmt -l "$f" 2>/dev/null | grep -q .; then CAND="$f"; break; fi
done < <(git ls-files '*.go')
if [ -z "$CAND" ]; then
  skip "D3: no clean, non-frozen .go file to convert — the CRLF class cannot be exercised here"
elif ! command -v gofmt >/dev/null 2>&1 && [ ! -x "/c/Program Files/Go/bin/gofmt.exe" ]; then
  skip "D3: gofmt unavailable"
else
  RESTORE_FILE="$CAND"
  sed -i 's/$/\r/' "$CAND"
  # Prove the mutation actually happened before believing the verdict: MSYS grep reads
  # in text mode and HIDES the CR (measured 2026-10-12), so the test uses cmp against a
  # CR-stripped copy — the same binary-safe probe B337's classifier uses.
  d3_stripped="$SKY_TMP/d3_stripped.go"
  tr -d '\r' < "$CAND" > "$d3_stripped"
  if cmp -s "$CAND" "$d3_stripped"; then
    git checkout -- "$CAND" 2>/dev/null
    RESTORE_FILE=""
    skip "D3: this host's sed cannot produce CRLF — the CRLF class cannot be exercised here"
  else
    d3_out="$(bash scripts/check_b337_gofmt_ratchet.sh 2>&1)"; d3_rc=$?
    git checkout -- "$CAND" 2>/dev/null
    RESTORE_FILE=""
    if printf '%s' "$d3_out" | grep -q 'CRLF-only checkout artefact' && printf '%s' "$d3_out" | grep -q 'C1: all'; then
      ok "D3: a CRLF working copy of a gofmt-clean file is not reported as drift ($CAND) — C1 stays PASS and names the artefact"
    else
      bad "D3: a CRLF working copy still reddens B337 C1 (rc=$d3_rc): $(printf '%s' "$d3_out" | grep -a 'C1:' | head -2 | tr '\n' ' ')"
    fi
  fi
fi

# ---------------------------------------------------------------------------
hdr "E. bookkeeping"
# ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b378_push_gate_and_host_artefacts.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (AGENTS trap #11)"
else
  bad "E1: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'check_b378_push_gate_and_host_artefacts.sh' scripts/verify_pre_deploy.sh; then
  ok "E2: verify_pre_deploy.sh registers B378"
else
  bad "E2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B378\*\*' AGENTS.md; then
  ok "E3: AGENTS.md's block index carries B378"
else
  bad "E3: the block index does not know B378 (AGENTS rule 2)"
fi

printf '\n\033[1mB378 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
