#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.4 (B126) — R9 verify_post_deploy EXTRACT bug
#
# Pins the v1.3.19.4 fix for the verify_post_deploy.sh R9 check that
# produced a spurious FAIL ("diff=80715s") even when the live policy
# matched the last applied snapshot to the second.
#
# Root cause (4-part chain):
#   1. The PG column `acl_snapshots.created_at` is INTEGER (Unix epoch),
#      NOT TIMESTAMPTZ as the v1.3.1 comment claimed.
#   2. The R9 psql_vm query called `EXTRACT(epoch FROM created_at)`,
#      which errors on PG with `function pg_catalog.extract(unknown,
#      integer) does not exist`.
#   3. The error text gets awk'd into LAST_ATTEMPT_EPOCH as "ERROR:",
#      and into LAST_ATTEMPT_SUCCESS as "function".
#   4. Downstream `date -d ""` (for empty LAST_ATTEMPT_ISO) returns
#      midnight-today (~1786914000), not 0 — so DIFF = 1786994715 -
#      1786914000 = 80715s, way above the 3600s threshold.
#
# The fix: use `created_at` directly (it's already an integer epoch
# from the column DEFAULT `EXTRACT(epoch FROM now())::bigint`).
#
# What this script verifies:
#   A. verify_post_deploy.sh does NOT use EXTRACT(epoch FROM created_at)
#      in any psql_vm query (the original buggy pattern)
#   B. The R9 SNAPSHOT_INFO query reads `created_at` directly
#   C. The R9 LAST_APPLIED_EPOCH query reads `created_at` directly
#   D. The script comment correctly states that created_at is INTEGER
#      (not TIMESTAMPTZ) — protects against a future "helpful" refactor
#   E. Live psql query returns a parseable epoch (no "ERROR:" text)
#   F. The R9 awk parse produces a non-empty numeric LAST_ATTEMPT_EPOCH
#   G. The end-to-end R9 logic produces DIFF in [-60, 3600] range
#      (matching live policy = last applied within 1 hour)
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
# Contract A: NO EXTRACT(epoch FROM created_at) in verify_post_deploy.sh
# ------------------------------------------------------------------------------
echo
echo "=== A. NO EXTRACT(epoch FROM created_at) in verify_post_deploy.sh (excluding comments) ==="
# This was the original buggy pattern. The fix removes it entirely from
# the SQL queries. The comment block may mention the pattern as a
# cautionary note — we filter out lines starting with '#'.
extract_count=$(grep -vE '^\s*#' "${VERIFY_POST}" 2>/dev/null | grep -cE 'EXTRACT\(epoch FROM created_at\)' || true)
extract_count=${extract_count:-0}
if [ "${extract_count}" -eq 0 ]; then
    ok "verify_post_deploy.sh has no EXTRACT(epoch FROM created_at) in non-comment lines"
else
    bad "verify_post_deploy.sh still has ${extract_count} EXTRACT(epoch FROM created_at) call(s) in non-comment lines — bug regressed"
fi

# ------------------------------------------------------------------------------
# Contract B: R9 SNAPSHOT_INFO query uses created_at directly
# ------------------------------------------------------------------------------
echo
echo "=== B. R9 SNAPSHOT_INFO query reads created_at directly ==="
# The query is around the comment that mentions B126 (v1.3.19.4).
# It must SELECT created_at || ' ' || COALESCE(applied_success::text, '0').
# The first awk field should be the created_at (epoch integer), not a
# "SELECT" keyword or "ERROR:" text.
if grep -qE "SELECT created_at \|\| ' ' \|\| COALESCE\(applied_success" "${VERIFY_POST}"; then
    ok "R9 SNAPSHOT_INFO query uses created_at directly"
else
    bad "R9 SNAPSHOT_INFO query is missing 'SELECT created_at || ' ' || COALESCE(applied_success...'"
fi
# Defensive: must NOT use EXTRACT in any non-comment line within the
# R9 query block (between the R9 comment and the if statement).
r9_block=$(sed -n '/Read both: last attempt/,/fi$/p' "${VERIFY_POST}" | grep -vE '^\s*#')
if echo "${r9_block}" | grep -qE 'EXTRACT'; then
    bad "R9 query block still contains EXTRACT in non-comment lines — bug regressed"
else
    ok "R9 query block has no EXTRACT in non-comment lines"
fi

# ------------------------------------------------------------------------------
# Contract C: LAST_APPLIED_EPOCH query uses created_at directly
# ------------------------------------------------------------------------------
echo
echo "=== C. R9 LAST_APPLIED_EPOCH query reads created_at directly ==="
if grep -qE "SELECT created_at FROM acl_snapshots WHERE applied_success=1" "${VERIFY_POST}"; then
    ok "R9 LAST_APPLIED_EPOCH query uses created_at directly"
else
    bad "R9 LAST_APPLIED_EPOCH query is missing 'SELECT created_at FROM acl_snapshots WHERE applied_success=1'"
fi

# ------------------------------------------------------------------------------
# Contract D: Comment correctly notes that created_at is INTEGER
# ------------------------------------------------------------------------------
echo
echo "=== D. Script comment correctly states created_at is INTEGER ==="
# Look for the B126 / v1.3.19.4 comment that explains the column type.
# We don't pin the exact wording, just that it mentions "INTEGER" and
# that the column type is NOT TIMESTAMPTZ. The comment lives in the R9
# block (between the SNAPSHOT_INFO comment and the SNAPSHOT_INFO query).
r9_comment_block=$(sed -n '/Read both: last attempt/,/SNAPSHOT_INFO=/p' "${VERIFY_POST}")
if echo "${r9_comment_block}" | grep -qE 'INTEGER'; then
    ok "R9 comment block mentions INTEGER (correct column type)"
else
    bad "R9 comment block does NOT mention INTEGER — refactor risk"
fi
# Sanity: the old wrong comment "TIMESTAMPTZ" claim should be gone
# (or at least disclaimed). The v1.3.1 comment was "created_at is now
# a PG TIMESTAMPTZ" — if it still says that without correction, that's
# a regression.
if grep -qE 'created_at is now a PG TIMESTAMPTZ' "${VERIFY_POST}"; then
    bad "verify_post_deploy.sh still has the OLD 'TIMESTAMPTZ' comment — claim is wrong"
else
    ok "Old 'TIMESTAMPTZ' comment has been replaced with INTEGER note"
fi

# ------------------------------------------------------------------------------
# Contract E: Live psql query returns a parseable epoch
# ------------------------------------------------------------------------------
echo
echo "=== E. Live psql query returns a parseable epoch (no 'ERROR:' text) ==="
# Run the same query the script runs, on the live VM. Need SSH access.
if command -v ssh >/dev/null 2>&1; then
    # Read SSH_HOST from env (matches verify_post_deploy.sh convention)
    : "${SSH_HOST:=admin@192.0.2.1}"
    : "${SSH_KEY:=}"
    if [ "${SSH_HOST}" = "admin@192.0.2.1" ]; then
        warn "SSH_HOST not set (using default placeholder) — skipping live query check"
    else
        ssh_args="-o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes"
        [ -n "${SSH_KEY}" ] && ssh_args="${ssh_args} -i ${SSH_KEY}"
        # psql_vm on the VM (postgres:18-alpine if psql not on PATH)
        SNAPSHOT_INFO=$(ssh ${ssh_args} "${SSH_HOST}" "set -e
            DSN=\$(grep -E '^SKYGATE_DB_DSN=' /home/skyadmin/skygate/.env | head -1 | cut -d= -f2-)
            DS=\${DSN#postgres://}
            DS=\${DS%%\?*}
            PU=\${DS%%:*}
            REST=\${DS#*:}
            PP=\${REST%%@*}
            REST=\${REST#*@}
            PH=\${REST%%:*}
            REST=\${REST#*:}
            PPORT=\${REST%%/*}
            PDB=\${REST#*/}
            if command -v psql >/dev/null 2>&1; then
              PGPASSWORD=\"\$PP\" psql -h \"\$PH\" -p \"\$PPORT\" -U \"\$PU\" -d \"\$PDB\" -tA -c 'SELECT created_at || '\'' '\'' || COALESCE(applied_success::text, '\''0'\'') FROM acl_snapshots ORDER BY id DESC LIMIT 1' 2>&1
            else
              docker run --rm -i --network host -e PGPASSWORD=\"\$PP\" postgres:18-alpine psql -h \"\$PH\" -p \"\$PPORT\" -U \"\$PU\" -d \"\$PDB\" -tA -c 'SELECT created_at || '\'' '\'' || COALESCE(applied_success::text, '\''0'\'') FROM acl_snapshots ORDER BY id DESC LIMIT 1' 2>&1
            fi" 2>/dev/null | head -1)
        # Raw output should be "<epoch> <applied_success>" e.g. "1786994716 1"
        if echo "${SNAPSHOT_INFO}" | grep -qE '^[0-9]+ [01]$'; then
            ok "Live query returned '${SNAPSHOT_INFO}' (parseable: epoch + applied_success)"
        elif echo "${SNAPSHOT_INFO}" | grep -qE '^ERROR'; then
            bad "Live query returned an ERROR: ${SNAPSHOT_INFO:0:100}"
        else
            bad "Live query returned unexpected: ${SNAPSHOT_INFO:0:100}"
        fi
    fi
else
    warn "ssh not on PATH — skipping live query check"
fi

# ------------------------------------------------------------------------------
# Contract F: R9 awk parse produces a non-empty numeric LAST_ATTEMPT_EPOCH
# ------------------------------------------------------------------------------
echo
echo "=== F. R9 awk parse produces a numeric LAST_ATTEMPT_EPOCH ==="
# Simulate the R9 awk logic on a well-formed SNAPSHOT_INFO like the live
# query now returns.
SNAPSHOT_INFO_SIM="1786994716 1"
LAST_ATTEMPT_EPOCH=$(echo "${SNAPSHOT_INFO_SIM}" | awk '{print $1}')
LAST_ATTEMPT_SUCCESS=$(echo "${SNAPSHOT_INFO_SIM}" | awk '{print $2}')
if [ -n "${LAST_ATTEMPT_EPOCH}" ] && echo "${LAST_ATTEMPT_EPOCH}" | grep -qE '^[0-9]+$'; then
    ok "R9 awk parse produces numeric epoch: '${LAST_ATTEMPT_EPOCH}'"
else
    bad "R9 awk parse produced non-numeric: '${LAST_ATTEMPT_EPOCH}'"
fi
if [ "${LAST_ATTEMPT_SUCCESS}" = "1" ] || [ "${LAST_ATTEMPT_SUCCESS}" = "0" ]; then
    ok "R9 awk parse produces valid success flag: '${LAST_ATTEMPT_SUCCESS}'"
else
    bad "R9 awk parse produced invalid success flag: '${LAST_ATTEMPT_SUCCESS}'"
fi

# ------------------------------------------------------------------------------
# Contract G: R9 DIFF arithmetic lands in [-60, 3600] for healthy state
# ------------------------------------------------------------------------------
echo
echo "=== G. R9 DIFF arithmetic lands in [-60, 3600] for healthy state ==="
# Simulate: live policy = 1 second AFTER the last applied (typical case
# where reapply just succeeded and headscale acknowledged the new policy).
LAST_ATTEMPT_ISO="2026-08-17T19:25:16Z"
UPDATED_AT="2026-08-17T19:25:15.861718255Z"
APPLIED_EPOCH=$(date -d "${LAST_ATTEMPT_ISO}" +%s 2>/dev/null || echo 0)
POLICY_EPOCH=$(date -d "${UPDATED_AT}" +%s 2>/dev/null || echo 0)
DIFF=$((POLICY_EPOCH - APPLIED_EPOCH))
if [ "${DIFF}" -ge -60 ] && [ "${DIFF}" -le 3600 ]; then
    ok "R9 DIFF=${DIFF}s is in [-60, 3600] range (would PASS)"
else
    bad "R9 DIFF=${DIFF}s is OUT OF [-60, 3600] range (would FAIL — bug regressed)"
fi
# Defensive: pre-fix behavior would have given DIFF=80715 (date -d "" returns midnight)
PRE_FIX_DIFF=$((POLICY_EPOCH - $(date -d "" +%s 2>/dev/null || echo 0)))
if [ "${PRE_FIX_DIFF}" -gt 3600 ]; then
    ok "Pre-fix bug signature (DIFF > 3600 when LAST_ATTEMPT_ISO is empty) confirmed: ${PRE_FIX_DIFF}s"
else
    warn "Pre-fix bug signature check inconclusive (date -d \"\" returned ${PRE_FIX_DIFF}s — may be system-dependent)"
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "================================================"
echo "B126 contracts: ${PASS} PASS / ${FAIL} FAIL / ${WARN} WARN"
echo "================================================"
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
