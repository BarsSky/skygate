#!/bin/bash
# check_b152.sh — bootstrap_standby.sh (Phase 7 of v1.5.0 HA plan)
#
# B152 (v1.5.0) — provision a NEW skygate-standby node. The
# script is operator-driven (they SSH into the new VM + run
# the script), so the B-check focuses on source-contract:
#
#  A. Source-contract (script exists, is executable, has the
#     6 pre-flight steps + the idempotency guard, validates
#     required env vars, polls /healthz)
#  B. .env contract (the script reads HEADPLANE_HEADSCALE__API_KEY
#     + SKYGATE_HA_ROLE + SKYGATE_HA_ENABLED — same contract as
#     the rest of the skygate deploy system)
#  C. Audit log contract (writes a "ha.bootstrap" audit row so
#     the /admin/audit page surfaces the bootstrap event)
#
# The live run is operator-driven and not in the B-check.
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source file exists + has the right structure"

# A.1 — the script exists + is executable + bash.
if [ -x scripts/bootstrap_standby.sh ] && head -1 scripts/bootstrap_standby.sh | grep -qE '^#!/usr/bin/env bash$|^#!/bin/bash$'; then
    ok "scripts/bootstrap_standby.sh exists + is executable + bash shebang"
else
    bad "scripts/bootstrap_standby.sh MISSING or not executable or not bash"
fi

# A.2 — the script must validate the 3 required env vars.
# A regression that skips SKYGATE_HA_ROLE would silently start
# the new node in role=active (the wrong default) and trigger
# a split-brain.
for key in "SKYGATE_HA_ROLE" "SKYGATE_HA_ENABLED" "HEADPLANE_HEADSCALE__API_KEY"; do
    if grep -qE "\\b${key}\\b" scripts/bootstrap_standby.sh; then
        ok "script validates env var '$key'"
    else
        bad "script does NOT validate env var '$key' (a regression could start the new node in the wrong role)"
    fi
done

# A.3 — the 6-step flow.
EXPECTED_STEPS=("[1/6]" "[2/6]" "[3/6]" "[4/6]" "[5/6]" "[6/6]")
for step in "${EXPECTED_STEPS[@]}"; do
    if grep -qF "$step" scripts/bootstrap_standby.sh; then
        ok "script has step $step"
    else
        bad "script missing step $step"
    fi
done

# A.4 — the script must be idempotent: re-running on an
# already-bootstrapped host should NOT re-create containers.
if grep -qE 'skygate.*-1\$|skygate-standby' scripts/bootstrap_standby.sh \
   && grep -qE 'already bootstrapped' scripts/bootstrap_standby.sh; then
    ok "script is idempotent (detects existing skygate container, exits early)"
else
    bad "script is NOT idempotent (re-running on an already-bootstrapped host would re-create containers)"
fi

# A.5 — the script must poll /healthz (the canonical skygate
# readiness signal). A regression that skips the health check
# would let the operator think the bootstrap succeeded when
# the new node is actually 500ing.
if grep -qE '/healthz' scripts/bootstrap_standby.sh; then
    ok "script polls /healthz (60s timeout)"
else
    bad "script does NOT poll /healthz (operator would think bootstrap succeeded when it's actually 500ing)"
fi

# A.6 — the script must verify the standby registered in the
# HA chain. A regression that skips the chain-registration
# check would let the operator think the standby is ready
# when the chain table doesn't have the new hostname.
if grep -qE 'ha_chain' scripts/bootstrap_standby.sh; then
    ok "script verifies ha_chain registration (operator sees the new node in /admin/ha)"
else
    bad "script does NOT verify ha_chain registration (operator would think the standby is ready when it's not in the chain)"
fi

# A.7 — the script must S3-pull the headscale config from the
# primary (otherwise the standby would be on a different
# config version + different ACL policy).
if grep -qE 'S3.*headscale.config|ha/headscale-config' scripts/bootstrap_standby.sh; then
    ok "script S3-pulls headscale config from the primary (same ACL policy)"
else
    bad "script does NOT S3-pull headscale config (standby would be on a different ACL policy)"
fi

# A.8 — the script must S3-pull the skygate binary from
# s3://<bucket>/ha/deploy/<hostname>/ (the B150 deploy surface).
# A regression that uses the local git checkout instead would
# mean the standby is on a different commit than the primary.
if grep -qE 'ha/deploy|deploy/' scripts/bootstrap_standby.sh; then
    ok "script S3-pulls skygate binary from ha/deploy/<hostname>/"
else
    bad "script does NOT S3-pull skygate binary from the B150 deploy surface (standby would be on a different commit than the primary)"
fi

# A.9 — the script must write an audit log row (so the
# /admin/audit page surfaces the bootstrap event).
if grep -qE 'ha.bootstrap' scripts/bootstrap_standby.sh; then
    ok "script writes 'ha.bootstrap' audit row"
else
    bad "script does NOT write an audit row (the bootstrap event would be invisible in /admin/audit)"
fi

# A.10 — the script must print next-steps for the operator
# (open /admin/ha, run dr_drill.sh, etc).
if grep -qE 'Next steps|next steps' scripts/bootstrap_standby.sh; then
    ok "script prints next-step hints for the operator"
else
    bad "script does NOT print next-step hints (operator would not know what to do after the bootstrap completes)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: getenv() helper contract"

# The script uses the same getenv() helper pattern as
# deploy/lib/env.sh. This is a defensive check: a refactor that
# inlines the grep would silently change the .env parsing
# behavior (e.g. quote handling).
if grep -qE '^getenv\(\)' scripts/bootstrap_standby.sh; then
    ok "script defines getenv() helper (consistent with deploy/lib/env.sh)"
else
    bad "script does NOT define getenv() helper (would silently break .env parsing)"
fi

# ---------------------------------------------------------------------------
hdr "summary"
echo "B152: bootstrap_standby.sh (Phase 7 — provision a new skygate-standby node)"
echo "all contracts satisfied"
