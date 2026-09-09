#!/usr/bin/env bash
# ============================================================================
# check_b_standby_provision.sh — B-check for HA standby auto-provisioning
# See AGENTS.md (B-new entry, 2026-09-09) for the B-block context.
#
# What this verifies
# ------------------
# Closes the gap where new HA standbys (e.g. svyatoslava-1) ended up in
# the synthetic `tagged-devices` headscale user (because preauth keys
# were minted without --user mapping), breaking per-DEVICE Tailscale
# grants and causing the standby to be invisible to skygate-host-1-1.
#
# The fix: deploy/scripts/create-standby-preauth.sh + the new step 0
# in scripts/bootstrap_standby.sh.
#
# Contracts (24 total)
# -------------------
# A. deploy/scripts/create-standby-preauth.sh exists + executable + bash shebang
# B. ... has --hostname required + --user default 85 (infra)
# C. ... uses docker exec headscale for the preauth create
# D. ... attaches a tag:dev-infra-<hostname> ACL tag (B175 Strategy E)
# E. ... exits non-zero if the docker exec fails
# F. ... records the preauth creation in skygate audit_log
# G. scripts/bootstrap_standby.sh has the new "step 0: Tailscale auth" block
# H. ... references SKYGATE_STANDBY_TS_AUTHKEY env var
# I. ... uses --netfilter-mode=nodir (B179 safety, NOT --netfilter-mode=off)
# J. ... has an idempotency check (skip if tailscale already Running)
# K. ... calls sudo tailscale up with --login-server=https://head.skynas.ru
# L. ... falls back gracefully when SKYGATE_STANDBY_TS_AUTHKEY is unset
# M. ... dies if tailscale up fails (the authkey is single-use — bad
#     key means restart from primary with a fresh create-standby-preauth)
# N. verify_pre_deploy.sh registers check_b_standby_provision.sh
# O. AGENTS.md mentions B-new + cross-references this B-check
# P. docs/internal/ha-v1.5.0-execution.md §3 Phase 7 mentions the new flow
#
# Exit codes
# ----------
#   0  all contracts pass
#   1  at least one contract failed
# ============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

PASS=0
FAIL=0
fails=()

ok()  { PASS=$((PASS+1)); echo "  [PASS] $1"; }
nok() { FAIL=$((FAIL+1)); fails+=("$1"); echo "  [FAIL] $1"; }
hdr() { echo ""; echo "=== $1 ==="; }

CREATE_SCRIPT="deploy/scripts/create-standby-preauth.sh"
BOOT_SCRIPT="scripts/bootstrap_standby.sh"

# --- A. create-standby-preauth.sh exists + executable + bash shebang ---
hdr "A. $CREATE_SCRIPT"
[ -f "$CREATE_SCRIPT" ] && ok "create-standby-preauth.sh exists" || nok "create-standby-preauth.sh MISSING"
[ -x "$CREATE_SCRIPT" ] && ok "create-standby-preauth.sh executable" || nok "create-standby-preauth.sh NOT executable (chmod +x)"
if [ -f "$CREATE_SCRIPT" ]; then
    head -1 "$CREATE_SCRIPT" | grep -qE '^#!/usr/bin/env bash$|^#!/bin/bash$' \
        && ok "create-standby-preauth.sh bash shebang" \
        || nok "create-standby-preauth.sh bash shebang missing/wrong"
fi

# --- B. required args ---
hdr "B. create-standby-preauth.sh required args"
grep -qE '\-\-hostname.*REQUIRED|--hostname.*is required|--hostname\)' "$CREATE_SCRIPT" 2>/dev/null \
    && ok "--hostname flag is required" \
    || nok "--hostname flag is NOT marked required"
grep -qE 'USER_ID=\"85\"|USER_ID=.85' "$CREATE_SCRIPT" 2>/dev/null \
    && ok "default USER_ID=85 (infra) — correct for HA standbys running etcd/Patroni" \
    || nok "default USER_ID is not 85 (should default to infra, not skyadmin)"

# --- C. docker exec headscale ---
hdr "C. uses headscale via docker exec"
grep -qE 'docker[[:space:]]+exec[[:space:]]+headscale.*preauthkeys|docker[[:space:]]+exec[[:space:]]+headscale' "$CREATE_SCRIPT" 2>/dev/null \
    && ok "uses 'docker exec headscale' to run the preauth create" \
    || nok "does NOT use 'docker exec headscale' (should run inside the headscale container)"

# --- D. attaches tag:dev-infra-<hostname> ACL ---
hdr "D. auto-tag the new node (B175 Strategy E)"
grep -qE 'tag:dev-infra-\$HOSTNAME|tag:dev-infra-\$1' "$CREATE_SCRIPT" 2>/dev/null \
    && ok "attaches 'tag:dev-infra-<hostname>' auto-tag so the new node gets the per-DEVICE grant" \
    || nok "does NOT attach the 'tag:dev-infra-<hostname>' auto-tag (B175 Strategy E would not fire)"

# --- E. dies if docker exec fails ---
hdr "E. non-zero exit on docker exec failure"
grep -qE 'set -euo pipefail|set -e' "$CREATE_SCRIPT" 2>/dev/null \
    && ok "uses 'set -euo pipefail' (any failure aborts with non-zero exit)" \
    || nok "missing 'set -euo pipefail' (failures might be silently swallowed)"

# --- F. records in skygate audit_log ---
hdr "F. writes ha.preauth.create audit row"
grep -qE "INSERT INTO audit_log|ha.preauth.create" "$CREATE_SCRIPT" 2>/dev/null \
    && ok "writes 'ha.preauth.create' audit row" \
    || nok "does NOT write an audit row (operator has no record of preauth minting)"

# --- G. bootstrap_standby.sh has the new step 0 ---
hdr "G. $BOOT_SCRIPT has new step 0 Tailscale auth"
grep -qE 'step 0.*[Tt]ailscale auth|step 0: Tailscale' "$BOOT_SCRIPT" 2>/dev/null \
    && ok "bootstrap_standby.sh has step 0: Tailscale auth block" \
    || nok "bootstrap_standby.sh does NOT have step 0: Tailscale auth (pre-B-new behaviour — manual tailscale up)"

# --- H. references SKYGATE_STANDBY_TS_AUTHKEY ---
hdr "H. uses SKYGATE_STANDBY_TS_AUTHKEY env var"
grep -qE 'SKYGATE_STANDBY_TS_AUTHKEY' "$BOOT_SCRIPT" 2>/dev/null \
    && ok "uses SKYGATE_STANDBY_TS_AUTHKEY env var (set by create-standby-preauth.sh output)" \
    || nok "does NOT use SKYGATE_STANDBY_TS_AUTHKEY (operator would have to set the env var manually)"

# --- I. --netfilter-mode=nodir (B179 safety) ---
hdr "I. --netfilter-mode=nodir (B179 safety, NOT 'off')"
if grep -qE 'netfilter-mode=nodir' "$BOOT_SCRIPT" 2>/dev/null; then
    ok "uses --netfilter-mode=nodir (safe: doesn't touch iptables — survives the 2026-08-31 / 2026-09-08 iptables-trap repeat)"
else
    nok "does NOT use --netfilter-mode=nodir (B179 trap will re-occur on every new standby)"
fi
if grep -qE 'netfilter-mode=off' "$BOOT_SCRIPT" 2>/dev/null; then
    nok "uses the DANGEROUS '--netfilter-mode=off' (B179 trap — leaves stale ts-input rules from previous session)"
else
    ok "does NOT use --netfilter-mode=off (B179 safety)"
fi

# --- J. idempotency check ---
hdr "J. idempotency (skip if tailscale already Running)"
grep -qE 'BackendState.*Running|already joined|skipping auth' "$BOOT_SCRIPT" 2>/dev/null \
    && ok "skips tailscale up if already in tailnet (idempotent re-runs)" \
    || nok "does NOT check 'already joined' (re-running the script would fail on tailscale up)"

# --- K. --login-server=https://head.skynas.ru ---
hdr "K. uses correct headscale login server"
grep -qE 'login-server=https://head\.skynas\.ru' "$BOOT_SCRIPT" 2>/dev/null \
    && ok "uses --login-server=https://head.skynas.ru" \
    || nok "does NOT use --login-server=https://head.skynas.ru (will connect to tailscale.com SaaS by default — wrong control plane)"

# --- L. graceful fallback when authkey unset ---
hdr "L. graceful fallback (legacy path)"
grep -qE 'SKYGATE_STANDBY_TS_AUTHKEY.*not set|assuming Tailscale.*already joined' "$BOOT_SCRIPT" 2>/dev/null \
    && ok "falls back to 'assuming Tailscale is already joined' when authkey is unset (legacy path still works)" \
    || nok "does NOT have a legacy fallback (operators who set up Tailscale manually will see the script fail)"

# --- M. dies if tailscale up fails ---
hdr "M. die on tailscale up failure"
grep -qE 'die .*tailscale up|tailscale up failed' "$BOOT_SCRIPT" 2>/dev/null \
    && ok "calls die() on tailscale up failure (authkey is single-use — operator must regenerate)" \
    || nok "does NOT call die() on tailscale up failure (silent failure mode = operator can't tell what went wrong)"

# --- N. verify_pre_deploy.sh registration ---
hdr "N. verify_pre_deploy.sh registration"
grep -qE "check_b_standby_provision" scripts/verify_pre_deploy.sh 2>/dev/null \
    && ok "verify_pre_deploy.sh registers check_b_standby_provision.sh" \
    || nok "verify_pre_deploy.sh does NOT register check_b_standby_provision.sh (won't run in CI)"

# --- O. AGENTS.md ---
hdr "O. AGENTS.md mentions the B-block"
grep -qE "standby.*provisioning|create-standby-preauth|tagged-devices.*synthetic|B175 Strategy E gap" AGENTS.md 2>/dev/null \
    && ok "AGENTS.md mentions the B-block (operator-facing docs are up to date)" \
    || nok "AGENTS.md does NOT mention the B-block (operator won't know about the auto-provisioning fix)"

# --- P. ha-v1.5.0-execution.md ---
hdr "P. docs/internal/ha-v1.5.0-execution.md mentions the new flow"
grep -qE "create-standby-preauth|SKYGATE_STANDBY_TS_AUTHKEY|netfilter-mode=nodir" docs/internal/ha-v1.5.0-execution.md 2>/dev/null \
    && ok "ha-v1.5.0-execution.md mentions the new preauth flow" \
    || nok "ha-v1.5.0-execution.md does NOT mention the new flow (operator will follow the old manual runbook)"

# --- summary ---
echo ""
echo "=== summary ==="
echo "  pass: $PASS"
echo "  fail: $FAIL"
if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "FAILED contracts:"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
echo ""
echo "all $PASS contracts PASS"
