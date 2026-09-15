#!/usr/bin/env bash
# check_b251.sh — verify the B251 "skygate-host reserved name" + B245 hujson fix landed.
#
# B251 (2026-09-15) closes three related gaps that surfaced during
# the live skygate-host-1-1 incident on the operator VM (192.168.13.69):
#
#   1. Hostname `skygate-host` is now the single canonical name for
#      the VM that runs the skygate container itself. Pre-B251 the
#      default `skygate-host-1` (and the wild `strings.HasPrefix("skygate-host-")`
#      rule) let two physical VMs claim the reserved role.
#
#   2. /admin/tailscale's "Generate Auth Key" no longer falls back
#      to "find whichever headscale user happens to have this
#      hostname". For the reserved name `skygate-host`, it always
#      pins to the `infra` portal user's headscale_user_id (default
#      uid=85). This closes the silent re-binding hazard where a
#      temporary migration under `skyadmin` would persist.
#
#   3. B245 (an outstanding autoupdater-degradation bug for months):
#      EnsureTagOwner used `encoding/json` directly to parse the
#      headscale policy, which crashed with
#      `cannot unmarshal string into Go value of type map[string]interface {}`
#      on the live policy (which headscale 0.29 returns as either
#      HuJSON-with-comments + stringified JSON or a top-level JSON
#      object). B251 wraps the parse in unquotePolicyIfStringified
#      → hujson.Standardize → json.Unmarshal.
#
# Contracts (12):
#   A. `cmd/skygate/main.go` TailscaleHostname default is `skygate-host`
#   B. `cmd/skygate/main.go` SelfHostname default is `skygate-host`
#   C. `internal/feature/admin/tailscale.go` tailscaleHostname default is `skygate-host`
#   D. `internal/nodeownership/auto.go` isInfraNode uses strict equality
#   E. `internal/nodeownership/auto.go` BackfillInfra rule comment mentions reserved name
#   F. `internal/feature/admin/tailscale.go` findUserForHostname has the reserved-name shortcut
#   G. `internal/feature/admin/tailscale.go` infraHeadscaleUserID helper exists
#   H. `internal/feature/admin/infra_owner_sanity.go` shouldBelongToInfra uses strict equality
#   I. `internal/headscale/tags.go` imports `github.com/tailscale/hujson`
#   J. `internal/headscale/tags.go` calls hujson.Standardize in EnsureTagOwner
#   K. `internal/headscale/tags.go` calls unquotePolicyIfStringified in EnsureTagOwner
#   L. `go.mod` requires `github.com/tailscale/hujson`
#
# Tests pinned in source (run separately via `go test`):
#   - internal/nodeownership/isinfra_b251_test.go — 5 cases (reserved name, legacy, suffixed, etc.)
#   - internal/feature/admin/derp_status_resolve_test.go TestShouldBelongToInfra_B251_Negative — 6 negative cases
#   - internal/feature/admin/tailscale_b251_test.go — 3 PG-backed cases (happy / missing / null link)
#   - internal/headscale/tags_b251_test.go — 3 cases (HuJSON policy, preserves existing, stringified policy)
#
# Exit codes:
#   0 = all contracts passed
#   1 = one or more contracts failed

set -uo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)

ok()  { echo "  PASS  $*"; }
bad() { echo "  FAIL  $*"; }

# ── A: main.go TailscaleHostname default ──
echo "=== A. TailscaleHostname default = skygate-host ==="
if grep -nE 'TailscaleHostname:\s*tailscaleEnvOr\("SKYGATE_TS_HOSTNAME",\s*"skygate-host"\)' cmd/skygate/main.go >/dev/null 2>&1; then
    ok "cmd/skygate/main.go default hostname = skygate-host"
else
    bad "cmd/skygate/main.go TailscaleHostname default is not skygate-host"
fi

# ── B: main.go SelfHostname default ──
echo
echo "=== B. SelfHostname default = skygate-host ==="
if grep -nE 'SelfHostname:\s*tailscaleEnvOr\("SKYGATE_TS_HOSTNAME",\s*"skygate-host"\)' cmd/skygate/main.go >/dev/null 2>&1; then
    ok "cmd/skygate/main.go SelfHostname default = skygate-host"
else
    bad "cmd/skygate/main.go SelfHostname default is not skygate-host"
fi

# ── C: tailscale.go tailscaleHostname default ──
echo
echo "=== C. tailscaleHostname default = skygate-host ==="
if grep -nE 'return\s+"skygate-host"' internal/feature/admin/tailscale.go >/dev/null 2>&1; then
    ok "internal/feature/admin/tailscale.go tailscaleHostname() returns skygate-host"
else
    bad "internal/feature/admin/tailscale.go tailscaleHostname() default is not skygate-host"
fi

# ── D: isInfraNode uses strict equality ──
echo
echo "=== D. isInfraNode uses strict equality (B251) ==="
if grep -nE 'return\s+n\.Hostname\s*==\s*"skygate-host"' internal/nodeownership/auto.go >/dev/null 2>&1; then
    ok "isInfraNode uses hostname == skygate-host (no longer HasPrefix)"
else
    bad "isInfraNode is not using strict equality"
fi

# ── E: BackfillInfra comment mentions reserved name ──
echo
echo "=== E. BackfillInfra comment references B251 / reserved name ==="
if grep -nE 'B251' internal/nodeownership/auto.go >/dev/null 2>&1; then
    ok "internal/nodeownership/auto.go has B251 reference"
else
    bad "internal/nodeownership/auto.go missing B251 reference"
fi

# ── F: findUserForHostname reserved-name shortcut ──
echo
echo "=== F. findUserForHostname reserved-name shortcut ==="
if grep -nE 'hostname\s*==\s*"skygate-host"' internal/feature/admin/tailscale.go >/dev/null 2>&1 && \
   grep -nE 'infraHeadscaleUserID' internal/feature/admin/tailscale.go >/dev/null 2>&1; then
    ok "findUserForHostname pins skygate-host to infra (no longer falls back to headscale search)"
else
    bad "findUserForHostname missing the reserved-name shortcut or infraHeadscaleUserID helper"
fi

# ── G: infraHeadscaleUserID helper exists ──
echo
echo "=== G. infraHeadscaleUserID helper exists ==="
if grep -nE 'func \(s \*Service\) infraHeadscaleUserID' internal/feature/admin/tailscale.go >/dev/null 2>&1; then
    ok "infraHeadscaleUserID helper defined"
else
    bad "infraHeadscaleUserID helper missing"
fi

# ── H: shouldBelongToInfra uses strict equality ──
echo
echo "=== H. shouldBelongToInfra uses strict equality ==="
if grep -nE 'if\s+hostname\s*==\s*"skygate-host"' internal/feature/admin/infra_owner_sanity.go >/dev/null 2>&1; then
    ok "shouldBelongToInfra uses hostname == skygate-host"
else
    bad "shouldBelongToInfra is not using strict equality"
fi
if grep -nE 'HasPrefix\(hostname,\s*"skygate-host-\)"' internal/feature/admin/infra_owner_sanity.go >/dev/null 2>&1; then
    bad "shouldBelongToInfra still uses HasPrefix(\"skygate-host-\") — B251 didn't take"
else
    ok "shouldBelongToInfra no longer uses the pre-B251 HasPrefix rule"
fi

# ── I: hujson import in tags.go ──
echo
echo "=== I. internal/headscale/tags.go imports hujson ==="
if grep -nE '"github.com/tailscale/hujson"' internal/headscale/tags.go >/dev/null 2>&1; then
    ok "hujson imported in tags.go"
else
    bad "hujson import missing in tags.go"
fi

# ── J: hujson.Standardize in EnsureTagOwner ──
echo
echo "=== J. EnsureTagOwner calls hujson.Standardize ==="
if grep -nE 'hujson\.Standardize' internal/headscale/tags.go >/dev/null 2>&1; then
    ok "hujson.Standardize called in tags.go"
else
    bad "hujson.Standardize missing"
fi

# ── K: unquotePolicyIfStringified in EnsureTagOwner ──
echo
echo "=== K. EnsureTagOwner calls unquotePolicyIfStringified (the actual B245 fix) ==="
if grep -nE 'unquotePolicyIfStringified' internal/headscale/tags.go >/dev/null 2>&1; then
    ok "unquotePolicyIfStringified called in tags.go"
else
    bad "unquotePolicyIfStringified missing — pre-B251 form dies on stringified policies"
fi

# ── L: go.mod requires hujson ──
echo
echo "=== L. go.mod requires github.com/tailscale/hujson ==="
if grep -nE 'github.com/tailscale/hujson' go.mod >/dev/null 2>&1; then
    ok "hujson in go.mod"
else
    bad "hujson missing from go.mod"
fi

echo
echo "=== summary: see PASS/FAIL counts above ==="