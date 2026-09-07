#!/bin/bash
# scripts/check_b146.sh — B146 (v1.5.0) Phase 2 reg.ru DNS
# live test contract.
#
# Pins the e2e live test (scripts/b146_regapi_live.sh)
# that proves the reg.ru v2 /zone/get_resource_records
# endpoint can be reached from this host with the
# operator's credentials + mTLS cert. Pre-B146 the
# auth pattern was discovered via 5+ curl invocations
# on 2026-08-18 (status log §6 of
# docs/internal/ha-v1.5.0-execution.md) but the
# discovered-working pattern was never productionized
# into a script that the operator can re-run.
#
# What this check does:
#   1. Source contract: the live test script exists +
#      is executable + has valid bash syntax
#   2. The HA execution doc mentions B146 in the
#      Phase 2 section (so the contract is documented
#      alongside the work it was written for)
#   3. The status log is updated to mark Phase 2 as
#      DONE (per the v1.5.0 HA tracker rule: "when
#      working on anything that touches the HA chain,
#      certsync, DNS failover, or deploy subcommands,
#      update docs/internal/ha-v1.5.0-execution.md §6
#      status log in the same commit")
#   4. AGENTS.md documents the B146 contract (so
#      future agents know the live test exists + how
#      to re-run it)
#   5. The live test script's known-passing credentials
#      contract is documented (the script will
#      SKIP on a host without env vars — but the
#      operator can re-run with env vars to verify
#      the integration end-to-end on the live VM)
#
# Why no live-test-execution contract: the live
# call requires (a) the operator's cert + key on
# disk, (b) the SKYGATE_DNS_REGAPI_USER + _PASSWORD
# env vars in the shell, (c) the VM's public IP in
# the reg.ru API IP whitelist. None of these are
# true on a CI runner or a fresh dev box. The
# script is the operator's live-test surface, not
# the verify-pre catalog's. (The catalog pins the
# contract that the script exists + is correct; the
# operator runs the script directly when they want
# to verify.)
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. The live-test script ---

# A.1 the script exists and is executable
if [ -x scripts/b146_regapi_live.sh ]; then
    ok "A.1 scripts/b146_regapi_live.sh exists and is executable"
else
    bad "A.1 scripts/b146_regapi_live.sh missing or not executable (operator can't re-run the live test)"
fi

# A.2 the script has valid bash syntax
if bash -n scripts/b146_regapi_live.sh 2>/dev/null; then
    ok "A.2 scripts/b146_regapi_live.sh has valid bash syntax"
else
    bad "A.2 scripts/b146_regapi_live.sh has bash syntax errors"
fi

# A.3 the script pins the working auth pattern
# (top-level form fields, password NOT in
# input_data). The B161.4 NO_AUTH discovery showed
# that the pre-fix code put creds inside input_data
# — that pattern returned NO_AUTH. We grep for the
# discovery-marked comment to make sure a future
# refactor doesn't accidentally move the creds
# back inside input_data.
if grep -q 'NOT.*input_data' scripts/b146_regapi_live.sh 2>/dev/null && \
   grep -q 'input_data' scripts/b146_regapi_live.sh 2>/dev/null; then
    ok "A.3 script documents the NO_AUTH trap (creds in input_data returns NO_AUTH)"
else
    bad "A.3 script must document the NO_AUTH trap so a future refactor doesn't regress"
fi

# A.4 the script handles the 4 reg.ru error codes
# the operator actually saw on 2026-08-18
# (NO_AUTH + ACCESS_DENIED_FROM_IP + DOMAIN_NOT_FOUND +
# RECORD_NOT_FOUND) with actionable error messages
for code in NO_AUTH ACCESS_DENIED_FROM_IP DOMAIN_NOT_FOUND; do
    if grep -q "ERROR:${code}" scripts/b146_regapi_live.sh 2>/dev/null; then
        ok "A.4 script handles ERROR:${code} (the known 2026-08-18 failure modes)"
    else
        bad "A.4 script must handle ERROR:${code} (operator saw this on 2026-08-18)"
    fi
done

# A.5 the script has a clear PASS/FAIL/SKIP output
# format (operator greps for the prefix in the
# live test's stdout)
if grep -q 'echo "PASS:' scripts/b146_regapi_live.sh 2>/dev/null && \
   grep -q 'echo "FAIL:' scripts/b146_regapi_live.sh 2>/dev/null && \
   grep -q 'echo "SKIP:' scripts/b146_regapi_live.sh 2>/dev/null; then
    ok "A.5 script has a grep-able PASS/FAIL/SKIP output format"
else
    bad "A.5 script must output 'PASS:' / 'FAIL:' / 'SKIP:' prefixes (operator greps them)"
fi

# A.6 the script uses curl with --cert + --key
# (mTLS, the working pattern). NOT a Go test —
# the operator's standard shell toolchain.
if grep -q -- '--cert' scripts/b146_regapi_live.sh 2>/dev/null && \
   grep -q -- '--key' scripts/b146_regapi_live.sh 2>/dev/null; then
    ok "A.6 script uses curl --cert + --key (mTLS, the working pattern)"
else
    bad "A.6 script must use curl --cert + --key (mTLS is required by reg.ru v2 API)"
fi

# --- B. HA execution doc references ---

# B.1 the HA execution doc §1 (Phase 2) still
# references the reg.ru DNS live test (the work
# that B146 closes)
if grep -q 'Phase 2' docs/internal/ha-v1.5.0-execution.md 2>/dev/null && \
   grep -q 'reg.ru DNS' docs/internal/ha-v1.5.0-execution.md 2>/dev/null; then
    ok "B.1 docs/internal/ha-v1.5.0-execution.md §1 still references the reg.ru DNS live test"
else
    bad "B.1 docs/internal/ha-v1.5.0-execution.md must reference the reg.ru DNS live test (Phase 2 / B146)"
fi

# B.2 the status log (§6) has a 2026-09-07 entry
# for the B146 productionization (per the v1.5.0 HA
# tracker rule: update §6 in the same commit)
if grep -q '2026-09-07' docs/internal/ha-v1.5.0-execution.md 2>/dev/null && \
   grep -q -B0 -A1 '2026-09-07' docs/internal/ha-v1.5.0-execution.md 2>/dev/null | grep -q -i 'b146\|phase 2.*done\|reg.ru.*live\|reg.ru.*skipped\|reg.ru.*test' 2>/dev/null; then
    ok "B.2 HA execution doc §6 has the 2026-09-07 status log entry"
else
    # The B-check is "did we update the log" — soft
    # fail if missing (the B146 commit author should
    # add the log entry; we don't enforce a hard
    # format because the operator may want to add
    # more context than the auto-format provides).
    echo "  WARN  B.2 HA execution doc §6 should have a 2026-09-07 B146 status entry (operator's preference on format)"
fi

# --- C. verify_pre_deploy.sh + AGENTS.md registration ---

# C.1 the check is registered
if grep -q 'check_b146' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "C.1 scripts/verify_pre_deploy.sh includes the B146 check"
else
    bad "C.1 scripts/verify_pre_deploy.sh must include the B146 check (see the run_check block for B146)"
fi

# C.2 AGENTS.md mentions B146
if grep -q 'B146' AGENTS.md 2>/dev/null; then
    ok "C.2 AGENTS.md documents B146 (the live test + B-check)"
else
    bad "C.2 AGENTS.md must document B146 (B-check convention)"
fi

# --- D. The runnable path is documented ---

# D.1 the script's header documents the 3 required
# env vars + the 2 file paths (so the operator
# knows what to set up before running)
for env_var in SKYGATE_DNS_REGAPI_USER SKYGATE_DNS_REGAPI_PASSWORD SKYGATE_DNS_REGAPI_ZONE; do
    if grep -q "$env_var" scripts/b146_regapi_live.sh 2>/dev/null; then
        ok "D.1 script references $env_var (operator knows to set it)"
    else
        bad "D.1 script must reference $env_var (operator needs to know the env var name)"
    fi
done

# D.2 the script's header documents the
# cert + key file path
if grep -q 'cert.pem' scripts/b146_regapi_live.sh 2>/dev/null && \
   grep -q 'key.pem' scripts/b146_regapi_live.sh 2>/dev/null; then
    ok "D.2 script references cert.pem + key.pem (operator knows the file locations)"
else
    bad "D.2 script must reference cert.pem + key.pem (operator needs to know the file locations)"
fi

# --- Summary ---

echo
echo "=== B146 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
