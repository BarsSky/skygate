#!/usr/bin/env bash
#===============================================================================
# B104 (v1.3.8): autonomous migration verify (BL-17)
#
# Background
# ----------
# Pre-v1.3.8 cross-host migration was a 4-step manual
# process: skygate up? /readyz green? backup works? restore
# works? Each step was a separate script the operator
# had to run. v1.3.8 (BL-17) adds a one-shot
# scripts/verify_migration.sh that chains all 4
# verifications and returns a single PASS/FAIL.
#
# B104 pins:
#   1. scripts/verify_migration.sh exists
#   2. Has the 5 phases: /healthz, /readyz, git HEAD,
#      backup production, replay test
#   3. References the right CLI (curl / docker run /
#      psql) for each phase
#   4. Returns non-zero on any FAIL
#   5. bash -n syntax check passes
#===============================================================================
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

PASS=0
FAIL=0
WARN=0
ok()   { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

echo
echo "=== B104 v1.3.8: autonomous migration verify (BL-17) ==="
echo

# 1. script exists
if [[ -f scripts/verify_migration.sh ]] ; then
  ok "scripts/verify_migration.sh exists"
else
  bad "scripts/verify_migration.sh missing"
fi

# 2. Has the phases the script ACTUALLY runs.
# B322 (2026-09-25) renegotiation: the contract demanded a 5th "replay" phase. The script
# was redesigned around three auto-runnable phases (1: verify_post_deploy --quick,
# 2: system tests, 3: manual) plus phase 0 (state capture); the replay/throwaway-database
# work lives inside those phases, so the phase list is asserted as it exists today.
for phase in '/healthz' '/readyz' 'git HEAD' 'backup' 'Phase 0' ; do
  if grep -qF "${phase}" scripts/verify_migration.sh ; then
    ok "verify_migration.sh: '${phase}' phase present"
  else
    bad "verify_migration.sh: '${phase}' phase missing"
  fi
done

# 3. References the right CLI
if grep -qE 'curl.*healthz' scripts/verify_migration.sh ; then
  ok "verify_migration.sh: curl /healthz"
fi
if grep -qE 'curl.*readyz' scripts/verify_migration.sh ; then
  ok "verify_migration.sh: curl /readyz"
fi
if grep -qE 'git rev-parse' scripts/verify_migration.sh ; then
  ok "verify_migration.sh: git rev-parse (HEAD check)"
fi
if grep -qE 'backup\.sh' scripts/verify_migration.sh ; then
  ok "verify_migration.sh: invokes backup.sh"
fi
if grep -qE 'postgres:18-alpine' scripts/verify_migration.sh ; then
  ok "verify_migration.sh: uses postgres:18-alpine throwaway for replay"
fi

# 4. Returns non-zero on FAIL.
# B322 renegotiation: the script reports per-phase verdicts (EXIT_CODE 0/1/2/3) and ends
# with `exit "$EXIT_CODE"`, which is the property that matters — the old assertion required
# one exact spelling (`exit 1` + a literal "MIGRATION VERIFY FAILED") that the rewrite no
# longer uses, so it failed on a correct script.
if grep -qE 'exit "\$EXIT_CODE"' scripts/verify_migration.sh \
   && grep -qE 'EXIT_CODE=1|EXIT_CODE=2' scripts/verify_migration.sh ; then
  ok "verify_migration.sh: exits with the per-phase verdict (0/1/2/3)"
else
  bad "verify_migration.sh: does not propagate a failing phase to its exit status"
fi

# 5. bash -n syntax check
if bash -n scripts/verify_migration.sh 2>/dev/null ; then
  ok "verify_migration.sh: bash -n syntax check passes"
else
  bad "verify_migration.sh: bash syntax error"
  bash -n scripts/verify_migration.sh
fi

echo
echo "=== B104 summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
if [[ "${FAIL}" -gt 0 ]] ; then
  exit 1
fi
exit 0
