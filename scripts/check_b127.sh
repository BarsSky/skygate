#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.4 (B127) — verify_post_deploy.sh false-positive cleanup
#
# Pins the v1.3.19.4 fixes that turn several long-standing
# verify_post_deploy.sh FAILs into PASSes on WSL bash (where
# the operator actually runs the script):
#
#   1. R11, R12, R13, R14, R15+R16, R28, R29: refactored from
#      `echo $X | python3 -c '...'` (which printed "Python was
#      not found" on WSL because the Microsoft Store alias
#      blocks real python3 invocation) to `json_field` (which
#      runs python3 on the VM where it's always installed).
#      Pre-B127, every refactored check returned empty/0
#      silently, FAILing the R-check.
#   2. R17, R18: same refactor + use HOST= env var (the loop
#      variable for which exit-node to check).
#   3. R34 (--quick mode): pre-init REMOTE_CK="" so the R34
#      block (which runs even in --quick mode) can safely
#      check [ -z "$REMOTE_CK" ] without `set -u` aborting.
#   4. SKYGATE_ADMIN_USER / SKYGATE_ADMIN_PASSWORD fallbacks:
#      the verify_login.sh subshell needs both. If neither is
#      set in the operator's env, the script now reads them
#      from the VM's .env. Pre-B127, R31/R32/R34 FAILed for
#      any operator who didn't have the right env vars set.
#
# What this script verifies (live, on the VM):
#   A. NO remaining `python3 -c` or `python3 <<EOF` calls in
#      verify_post_deploy.sh (the buggy direct-host python3
#      pattern). All JSON parsing is now via json_field.
#   B. R34 pre-init REMOTE_CK="" is present before the R31
#      block (not in the middle of it)
#   C. The SKYGATE_ADMIN_USER fallback block is present and
#      defaults to "skyadmin"
#   D. The SKYGATE_ADMIN_PASSWORD fallback block reads from
#      the VM's SKYGATE_ADMIN_PASS env var
#   E. R11-R16, R28, R29, R17-R18, R34 all use json_field
#   F. R15+R16 use the file-based DB_JSON_FILE pattern (not
#      the value-substitution pattern that broke on `"` in JSON)
#   G. json_field is still defined in verify_post_deploy.sh
#      (we depend on it)
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

# Allow override so this script works from /tmp on the VM
: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

VERIFY_POST="scripts/verify_post_deploy.sh"

[ -f "${VERIFY_POST}" ] || { bad "source file not found: ${VERIFY_POST}"; exit 1; }

# ------------------------------------------------------------------------------
# Contract A: NO `python3 -c` or `python3 <<EOF` calls in verify_post_deploy.sh
# ------------------------------------------------------------------------------
echo
echo "=== A. NO direct python3 -c / heredoc in verify_post_deploy.sh ==="
# Count remaining buggy patterns. Allow them ONLY in comments.
# Exclude lines starting with '#' (comments). Use single quotes
# around the patterns to avoid bash interpreting the regex specials.
py_c_count=$(grep -vE '^[[:space:]]*#' "${VERIFY_POST}" 2>/dev/null | grep -cE 'python3 -c' || true)
py_c_count=${py_c_count:-0}
heredoc_count=$(grep -vE '^[[:space:]]*#' "${VERIFY_POST}" 2>/dev/null | grep -cF 'python3 <<' || true)
heredoc_count=${heredoc_count:-0}
if [ "${py_c_count}" -eq 0 ] && [ "${heredoc_count}" -eq 0 ]; then
    ok "verify_post_deploy.sh has no direct python3 -c or python3 heredoc calls in non-comment lines"
else
    bad "verify_post_deploy.sh still has ${py_c_count} python3 -c + ${heredoc_count} python3 heredoc calls in non-comment lines"
fi

# ------------------------------------------------------------------------------
# Contract B: REMOTE_CK="" pre-init for R34
# ------------------------------------------------------------------------------
echo
echo "=== B. REMOTE_CK pre-init before R31 block (R34 --quick mode fix) ==="
# The init must be BEFORE the R31 block (which is the first
# `if [ "$QUICK" = 0 ]` block after the R11-R16 phase).
# Look for "REMOTE_CK=\"\"" with the B127 marker comment.
if grep -qE 'REMOTE_CK=""' "${VERIFY_POST}"; then
    ok "REMOTE_CK=\"\" init is present in verify_post_deploy.sh"
else
    bad "REMOTE_CK=\"\" init is missing — R34 in --quick mode will fail with unbound variable"
fi
# Verify the init is NOT inside the non-quick R31 block (would
# not help --quick mode). The init should be at the top of
# the script, near RESULTS_PASS/RESULTS_FAIL.
init_line=$(grep -nE 'REMOTE_CK=""' "${VERIFY_POST}" | head -1 | cut -d: -f1)
# R31 echo is `echo "[R31] /admin/headscale/acl renders..."` — not
# at line start, so grep for the echo pattern instead.
r31_start=$(grep -nE 'echo "\[R31\]' "${VERIFY_POST}" | head -1 | cut -d: -f1)
if [ -n "$init_line" ] && [ -n "$r31_start" ] && [ "$init_line" -lt "$r31_start" ]; then
    ok "REMOTE_CK init (line ${init_line}) is BEFORE the R31 block (line ${r31_start})"
else
    bad "REMOTE_CK init is at line ${init_line}, R31 starts at line ${r31_start} — init must be before R31"
fi

# ------------------------------------------------------------------------------
# Contract C: SKYGATE_ADMIN_USER fallback to "skyadmin"
# ------------------------------------------------------------------------------
echo
echo "=== C. SKYGATE_ADMIN_USER fallback block ==="
# Look for the B127 marker + the default "skyadmin" + read from VM .env
if grep -qE 'SKYGATE_ADMIN_USER="skyadmin"' "${VERIFY_POST}"; then
    ok "SKYGATE_ADMIN_USER falls back to 'skyadmin' when env is unset"
else
    bad "SKYGATE_ADMIN_USER does NOT default to 'skyadmin' — verify_login.sh will reject the missing var"
fi
# Verify the fallback reads from VM .env (graceful operator-experience)
if grep -qF "SKYGATE_ADMIN_USER=" "${VERIFY_POST}" && grep -qF "grep '^SKYGATE_ADMIN_USER=" "${VERIFY_POST}"; then
    ok "SKYGATE_ADMIN_USER fallback reads from VM .env (operator doesn't have to set it manually)"
else
    warn "SKYGATE_ADMIN_USER fallback does NOT read from VM .env (operator MUST export it manually)"
fi

# ------------------------------------------------------------------------------
# Contract D: SKYGATE_ADMIN_PASSWORD fallback reads from VM's SKYGATE_ADMIN_PASS
# ------------------------------------------------------------------------------
echo
echo "=== D. SKYGATE_ADMIN_PASSWORD fallback block ==="
# Look for the B127 marker + the `docker exec ... echo \$SKYGATE_ADMIN_PASS` pattern
if grep -qE 'docker exec.*echo.*\\\$SKYGATE_ADMIN_PASS' "${VERIFY_POST}"; then
    ok "SKYGATE_ADMIN_PASSWORD fallback reads from VM's SKYGATE_ADMIN_PASS env var"
else
    bad "SKYGATE_ADMIN_PASSWORD fallback does NOT read from VM — operator must export it manually"
fi

# ------------------------------------------------------------------------------
# Contract E: R-checks use json_field
# ------------------------------------------------------------------------------
echo
echo "=== E. R-checks use json_field (not direct python3) ==="
# Spot-check each R-check: find the R-block comment marker, then
# look at the next 60 lines for json_field. Use a simple regex
# that matches "# R11:" / "# R15 + R16:" / etc.
check_uses_json_field() {
    local r="$1"
    local rline
    # Look for the R-block by its echo marker, not just a comment
    # mention. R-checks all print "echo "[Rxx] ..."` before doing
    # anything. The block starts there.
    rline=$(grep -nE "echo \"\\\[${r}(\\\]|-R[0-9]+\\\])" "${VERIFY_POST}" 2>/dev/null | head -1 | cut -d: -f1)
    if [ -z "$rline" ]; then
        # Fallback: look for the Phase marker like "Phase 4: exit-nodes (R17-R18)"
        rline=$(grep -nE "Phase .* \\(${r}(-R[0-9]+)?\\) —" "${VERIFY_POST}" 2>/dev/null | head -1 | cut -d: -f1)
    fi
    if [ -z "$rline" ]; then
        rline=$(grep -nE "^ {0,3}# ${r}[: ]" "${VERIFY_POST}" 2>/dev/null | head -1 | cut -d: -f1)
    fi
    if [ -z "$rline" ]; then
        bad "${r} comment marker not found"
        return
    fi
    if sed -n "${rline},$((rline + 60))p" "${VERIFY_POST}" 2>/dev/null | grep -qE 'json_field'; then
        ok "${r} uses json_field"
    else
        bad "${r} does NOT use json_field (still buggy direct python3 pattern)"
    fi
}
for r in R11 R12 R13 R14 R15 R17 R18 R28 R29; do
    check_uses_json_field "$r"
done

# ------------------------------------------------------------------------------
# Contract F: R15+R16 use file-based DB_JSON_FILE pattern
# ------------------------------------------------------------------------------
echo
echo "=== F. R15+R16 use file-based DB_JSON_FILE pattern (not broken value-substitution) ==="
# The pre-B127 attempt to pass DB_JSON as a json_field arg failed
# because json_field's `$*` is unquoted and the JSON value contains
# `"` which broke the shell. The B127 fix is to write DB_JSON to a
# file on the VM and pass the file PATH (no `"` in the value).
if grep -qE 'DB_JSON_FILE=' "${VERIFY_POST}"; then
    ok "R15+R16 use the DB_JSON_FILE pattern (file-based, survives json_field unquoted \$*)"
else
    bad "R15+R16 do NOT use DB_JSON_FILE — value-substitution still breaks on JSON quotes"
fi
# The python should read from os.environ['DB_JSON_FILE'] not DB_JSON
if grep -A 60 'R15 + R16' "${VERIFY_POST}" | grep -qE "os.environ.get.'DB_JSON_FILE."; then
    ok "R15+R16 python reads from DB_JSON_FILE (not the broken DB_JSON env var)"
else
    bad "R15+R16 python does NOT read from DB_JSON_FILE"
fi

# ------------------------------------------------------------------------------
# Contract G: json_field function is still defined
# ------------------------------------------------------------------------------
echo
echo "=== G. json_field function is still defined ==="
if grep -qE '^json_field\(\)' "${VERIFY_POST}"; then
    ok "json_field() function is still defined (we depend on it for all refactored R-checks)"
else
    bad "json_field() function is missing — refactored R-checks will fail"
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "================================================"
echo "B127 contracts: ${PASS} PASS / ${FAIL} FAIL / ${WARN} WARN"
echo "================================================"
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
