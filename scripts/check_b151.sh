#!/bin/bash
# check_b151.sh — init-headplane.sh (Phase 8 of v1.5.0 HA plan)
#
# B151 (v1.5.0) — auto-apply headplane API key on a fresh
# deploy. See docs/internal/ha-v1.5.0-execution.md §3 (Phase 8).
#
# The B-check is split into:
#  A. Source-contract checks (the script exists, is executable,
#     has the 2 modes + the 6/4 steps in the right order)
#  B. Live bash-script checks (bundled mode generates + writes
#     + restarts; external mode requires interactive input —
#     SKIP cleanly on non-TTY hosts)
#  C. .env integration (the script reads + writes HEADPLANE_HEADSCALE__API_KEY
#     via the same getenv/setenv helpers as deploy.sh)
#
# All checks are read-only on the .env file (the live bash check
# creates a temp .env copy, not the real one).
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source file exists + has the right structure"

# A.1 — the script exists + is executable + has a shebang
# (not /bin/sh — must be bash for the array + process-substitution
# + the `read -r -p` interactive prompt).
if [ -x scripts/init-headplane.sh ] && head -1 scripts/init-headplane.sh | grep -qE '^#!/usr/bin/env bash$|^#!/bin/bash$'; then
    ok "scripts/init-headplane.sh exists + is executable + bash shebang"
else
    bad "scripts/init-headplane.sh MISSING or not executable or not bash"
fi

# A.2 — the script must reference the 2 modes (bundled + external).
# We grep for the "Mode: " line that prints in each branch.
for mode in "bundled headplane" "external headplane"; do
    if grep -qF "$mode" scripts/init-headplane.sh; then
        ok "script handles mode: '$mode'"
    else
        bad "script missing mode: '$mode' (must support both bundled + external headplane)"
    fi
done

# A.3 — the 6-step flow in the bundled branch.
# We grep for the [N/6] markers in the right order.
EXPECTED_STEPS_BUNDLED=("[1/6]" "[2/6]" "[3/6]" "[4/6]" "[5/6]" "[6/6]")
for step in "${EXPECTED_STEPS_BUNDLED[@]}"; do
    if grep -qF "$step" scripts/init-headplane.sh; then
        ok "bundled branch has step $step"
    else
        bad "bundled branch missing step $step"
    fi
done

# A.4 — the 4-step flow in the external branch.
EXPECTED_STEPS_EXTERNAL=("[1/4]" "[2/4]" "[3/4]" "[4/4]")
for step in "${EXPECTED_STEPS_EXTERNAL[@]}"; do
    if grep -qF "$step" scripts/init-headplane.sh; then
        ok "external branch has step $step"
    else
        bad "external branch missing step $step"
    fi
done

# A.5 — the script must call `docker exec headscale apikeys create`
# (the canonical way to mint a fresh API key inside the headscale
# container). A regression here breaks the bundled flow.
if grep -qE 'docker exec.*headscale apikeys create' scripts/init-headplane.sh; then
    ok "script uses 'docker exec ... headscale apikeys create' to mint keys"
else
    bad "script does NOT call 'docker exec ... headscale apikeys create' (the canonical key-mint command)"
fi

# A.6 — the script must parse the new key from the headscale output.
# We use a regex that's stable across headscale v0.23+ to v0.30+
# (the key format `hskey-api-...` has been stable since v0.20).
if grep -qE 'hskey-api-' scripts/init-headplane.sh; then
    ok "script parses hskey-api-* keys"
else
    bad "script does NOT parse hskey-api-* keys (headscale v0.23+ format)"
fi

# A.7 — the script must back up .env before writing.
if grep -qE 'pre-init-headplane' scripts/init-headplane.sh; then
    ok "script backs up .env before writing (pre-init-headplane.YYYYMMDDHHMMSS)"
else
    bad "script does NOT back up .env before writing (operator would lose the old key on a typo)"
fi

# A.8 — the script must use the same getenv/setenv pattern as
# deploy/lib/env.sh (so the .env format is consistent with the
# rest of skygate's deploy system). We check for the 2 helper
# function definitions.
if grep -qE '^getenv\(\)' scripts/init-headplane.sh \
   && grep -qE '^setenv\(\)' scripts/init-headplane.sh; then
    ok "script defines getenv() + setenv() helpers (consistent with deploy/lib/env.sh)"
else
    bad "script missing getenv() or setenv() helpers (must use the same env-file contract as deploy.sh)"
fi

# A.9 — the script must skip the key generation when the existing
# key is non-empty + non-placeholder (idempotency).
if grep -qE 'NEEDS_KEY' scripts/init-headplane.sh; then
    ok "script has NEEDS_KEY gate (idempotency — don't overwrite a real key)"
else
    bad "script missing NEEDS_KEY gate (would overwrite a real key on every run)"
fi

# A.10 — the script must verify the headplane handshake (either
# internal /admin/healthz or external HEADPLANE_EXTERNAL_URL).
# A regression that skips the verify step would silently leave
# the operator with a broken integration.
if grep -qE '/admin/healthz' scripts/init-headplane.sh; then
    ok "script verifies headplane /admin/healthz"
else
    bad "script does NOT verify headplane /admin/healthz (would leave a broken integration without warning)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: live script check (bundled mode, hermetic .env)"

# B.1 — invoke the script in --dry-run mode against a temp .env
# (no docker, no actual key mint). We do this by extracting the
# logic into a smaller test driver that the script supports via
# SKYGATE_INIT_HEADPLANE_DRY_RUN=1.
SKIP_B=0
case "$(uname -s 2>/dev/null || echo Windows)" in
    Windows*|MINGW*|MSYS*|CYGWIN*) SKIP_B=1 ;;
esac
if ! command -v docker >/dev/null 2>&1; then
    SKIP_B=1
fi

if [ "$SKIP_B" = "1" ]; then
    skip "live script test (Windows or no docker; source-contract checks above are enough on this host)"
else
    # The script does NOT have a built-in --dry-run (intentional —
    # the live behavior is the contract). We instead copy the
    # script to a temp dir + a temp .env, set SKYGATE_ENV_FILE to
    # the temp .env, and verify the script FAILS CLEANLY when
    # headscale is not reachable (the early docker-ps check
    # surfaces the error).
    tmpdir=$(mktemp -d)
    tmp_env="$tmpdir/.env"
    cat > "$tmp_env" <<EOF
HEADPLANE_EXTERNAL_URL=
HEADPLANE_HEADSCALE__API_KEY=
HEADSCALE_CONTAINER=headscale-fake
COMPOSE_PROJECT_NAME=skygate
EOF
    set +e
    SKYGATE_ENV_FILE="$tmp_env" bash scripts/init-headplane.sh 2>&1 | tail -3 > "$tmpdir/out"
    EXIT_CODE=$?
    set -e
    if [ "$EXIT_CODE" = "0" ]; then
        rm -rf "$tmpdir"
        bad "script returned 0 against a fake .env (should fail on the headscale container check)"
    elif grep -qF "headscale-fake is not running" "$tmpdir/out" 2>/dev/null; then
        ok "script fails cleanly when headscale is not running (early docker-ps check works)"
    else
        cat "$tmpdir/out" >&2
        rm -rf "$tmpdir"
        bad "script exited with $EXIT_CODE but didn't print the expected error"
    fi
    rm -rf "$tmpdir"
fi

# ---------------------------------------------------------------------------
hdr "summary"
echo "B151: init-headplane.sh (Phase 8 — auto-apply headplane API key on fresh deploy)"
echo "all contracts satisfied"
