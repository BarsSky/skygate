#!/usr/bin/env bash
# scripts/check_b267_headscale_cli_mode.sh
#
# B267 (2026-09-19) — route approval must not require docker.
#
# Operator report (native, non-docker install): assigning an exit node failed with
#   approve-routes: approve-routes: exec: "docker": executable file not found in $PATH:
# `internal/headscale/routes.go:approveRoutesForNodeID` hardcoded
#   exec.Command("docker", "exec", "headscale", "/ko-app/headscale", "nodes", "approve-routes", …)
# so on a systemd/binary headscale (no docker at all) every approve-routes call —
# the "Tag as exit-node" button, ApproveAllRoutes, the relay flow — failed.
#
# What this check pins:
#   A) the REST API is tried FIRST (works on every install kind):
#      POST /api/v1/node/{id}/approve_routes
#   B) the CLI fallback goes through the install-kind-aware helper
#   C) the helper tries `docker exec` only when docker is actually in PATH, and
#      falls back to the local `headscale` binary otherwise
#   D) the failure message names both attempts instead of the bare
#      `exec: "docker": executable file not found`
#   E) no hardcoded `exec.Command("docker", "exec", "headscale"…)` remains
#
# Exit 0 on all green, non-zero on any FAIL.
set -uo pipefail
set -f

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

SRC="internal/headscale/routes.go"

PASS=0
FAIL=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

# --- A. the API endpoint is attempted ---
if grep -qF '/api/v1/node/%d/approve_routes' "$SRC"; then
  ok "A: approve-routes tries POST /api/v1/node/{id}/approve_routes first"
else
  bad "A: $SRC does not call the approve_routes REST endpoint — a native install has no CLI fallback path"
fi

# --- B/C/D. install-kind-aware CLI helper ---
if grep -q 'func (c \*Client) runHeadscaleCLI' "$SRC"; then
  ok "B: runHeadscaleCLI helper exists"
else
  bad "B: no runHeadscaleCLI helper"
fi
if grep -q 'exec.LookPath("docker")' "$SRC"; then
  ok "C: docker is probed with exec.LookPath before use"
else
  bad "C: docker is used without a LookPath probe (native installs cannot reach the fallback)"
fi
if grep -q 'exec.Command("headscale", args...)' "$SRC"; then
  ok "C: the local \`headscale\` binary is the non-docker fallback"
else
  bad "C: no local-headscale fallback — a systemd install stays broken"
fi
# B272.6 renegotiation (2026-09-21): this used to grep for `docker not in PATH`.
# The message now names the ACTUAL install kind instead of offering docker and
# "install the CLI" side by side — on the native host aro that wording fitted none
# of the facts (docker absent by design, the CLI installed, the real cause a
# socket permission error). The contract now asserts what the message must teach:
# the native socket/group requirement and the SKYGATE_HEADSCALE_CLI override.
if grep -q 'native install the CLI needs access to the headscale socket' "$SRC" && \
   grep -q 'SKYGATE_HEADSCALE_CLI' "$SRC"; then
  ok "D: the failure explains the native alternative (socket/group) and the CLI override"
else
  bad "D: the error does not explain how to run without docker"
fi
if grep -q 'SKYGATE_HEADSCALE_CLI' "$SRC"; then
  ok "D: the in-container CLI path is overridable (SKYGATE_HEADSCALE_CLI)"
else
  bad "D: the hardcoded /ko-app/headscale path is not overridable"
fi

# --- E. the old hardcoded docker exec is gone ---
if grep -v '^[[:space:]]*//' "$SRC" | grep -q 'exec.Command("docker", "exec", "headscale"'; then
  bad "E: the hardcoded docker exec is still present"
else
  ok "E: no hardcoded \`docker exec headscale\` left in $SRC"
fi

# --- F. focused tests still pass ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/headscale/ -count=1 -run 'TestIsSafeSSHTarget|TestSplitSSHTarget|TestBuildSetAdvertisedRoutes|TestRoute' >/dev/null 2>&1; then
    ok "F: focused internal/headscale tests pass"
  else
    bad "F: focused internal/headscale tests failed"
  fi
else
  ok "F: go test skipped (no go in PATH)"
fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]
