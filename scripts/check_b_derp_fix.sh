#!/usr/bin/env bash
# ============================================================================
# check_b_derp_fix.sh — B-block checks for the 2026-09-15 DERP/infra
# regression cluster (B-fix). Pins 5 fixes so future edits can't
# silently re-break them:
#
#   1. resolveDERPPort / resolveSTUNPort helpers exist (derp_status_resolve.go)
#      and are called by collectDerpStatus (no more hardcoded "443" / "3478")
#   2. EnsureBundledDerpRelay exists (derp_relays_auto.go) and is wired into
#      cmd/skygate/main.go's boot sequence
#   3. Telegram probe troubleshooting block uses Container.Available /
#      Container.RouteAll to surface "tailscaled not running" / "accept-routes
#      off" hints (admin/telegram.html)
#   4. SanityCheckInfraUserOwners exists (infra_owner_sanity.go) and is
#      wired into cmd/skygate/main.go after ensureInfraUser
#   5. AGENTS.md / verify_pre_deploy.sh mention the B-block id (so the
#      pre-push hook surfaces it)
#
# Usage:
#   bash scripts/check_b_derp_fix.sh
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks failed (regression risk)
# ============================================================================
set -u

PASS_COUNT=0
FAIL_COUNT=0
ok()  { PASS_COUNT=$((PASS_COUNT+1)); printf "  ok   %s\n" "$*"; }
bad() { FAIL_COUNT=$((FAIL_COUNT+1)); printf "  BAD  %s\n" "$*"; }

# Run from repo root so the relative paths match.
cd "$(dirname "$0")/.."

echo "=== A. resolveDERPPort / resolveSTUNPort helpers + call sites ==="
# A.1 the helper file exists
if [ -f internal/feature/admin/derp_status_resolve.go ]; then
  ok "A.1 derp_status_resolve.go exists"
else
  bad "A.1 internal/feature/admin/derp_status_resolve.go missing"
fi

# A.2 helpers defined
if grep -qE 'func resolveDERPPort' internal/feature/admin/derp_status_resolve.go \
   && grep -qE 'func resolveSTUNPort' internal/feature/admin/derp_status_resolve.go; then
  ok "A.2 resolveDERPPort + resolveSTUNPort defined"
else
  bad "A.2 resolveDERPPort / resolveSTUNPort not both defined"
fi

# A.3 derp.go calls them — no more hardcoded "443" / "3478" seeds
if grep -qE 'DERPPort:\s*derpPort' internal/feature/admin/derp.go \
   && grep -qE 'STUNPort:\s*stunPort' internal/feature/admin/derp.go; then
  ok "A.3 derp.go uses the helpers (derpPort / stunPort)"
else
  bad "A.3 derp.go still uses hardcoded DERPPort/STUNPort seeds"
fi

# A.4 dead hardcoded "443" seed removed
if ! grep -qE '^\s*DERPPort:\s*"443"\s*,' internal/feature/admin/derp.go; then
  ok "A.4 derp.go no longer seeds DERPPort with hardcoded \"443\""
else
  bad "A.4 derp.go still seeds DERPPort with hardcoded \"443\" (regression)"
fi

echo
echo "=== B. EnsureBundledDerpRelay + main.go wiring ==="
# B.1 helper file exists
if [ -f internal/feature/admin/derp_relays_auto.go ]; then
  ok "B.1 derp_relays_auto.go exists"
else
  bad "B.1 internal/feature/admin/derp_relays_auto.go missing"
fi

# B.2 helper defined
if grep -qE 'func EnsureBundledDerpRelay' internal/feature/admin/derp_relays_auto.go; then
  ok "B.2 EnsureBundledDerpRelay defined"
else
  bad "B.2 EnsureBundledDerpRelay not defined"
fi

# B.3 main.go wires the call
if grep -qE 'adminsvc\.EnsureBundledDerpRelay' cmd/skygate/main.go; then
  ok "B.3 cmd/skygate/main.go calls adminsvc.EnsureBundledDerpRelay"
else
  bad "B.3 cmd/skygate/main.go does NOT call EnsureBundledDerpRelay"
fi

# B.4 main.go reads DERP_HOSTNAME / SKYGATE_DERP_HOSTNAME before the call
#     (use -B30 since the call has ~25 lines of comment between hostname
#      declaration and the EnsureBundledDerpRelay invocation.)
if grep -B30 'EnsureBundledDerpRelay' cmd/skygate/main.go | grep -q 'SKYGATE_DERP_HOSTNAME'; then
  ok "B.4 main.go reads SKYGATE_DERP_HOSTNAME before the EnsureBundledDerpRelay call"
else
  bad "B.4 main.go does NOT read SKYGATE_DERP_HOSTNAME before EnsureBundledDerpRelay"
fi

echo
echo "=== C. /admin/telegram troubleshooting uses container state ==="
# C.1 telegram.html conditional hint for not-available container
if grep -qE 'probe_tip_container_off' internal/handlers/templates/admin/telegram.html \
   && grep -qE 'not .State.Container.Available' internal/handlers/templates/admin/telegram.html; then
  ok 'C.1 telegram.html fires probe_tip_container_off when Container.Available is false'
else
  bad 'C.1 telegram.html missing the Container.Available conditional hint'
fi

# C.2 telegram.html conditional hint for accept-routes off
if grep -qE 'probe_tip_container_no_accept' internal/handlers/templates/admin/telegram.html \
   && grep -qE 'not .State.Container.RouteAll' internal/handlers/templates/admin/telegram.html; then
  ok 'C.2 telegram.html fires probe_tip_container_no_accept when Container.RouteAll is false'
else
  bad 'C.2 telegram.html missing the Container.RouteAll conditional hint'
fi

# C.3 i18n keys present in both RU and EN
if grep -qE 'telegram\.probe_tip_container_off' internal/i18n/catalog_telegram.go \
   && grep -qE 'telegram\.probe_tip_container_no_accept' internal/i18n/catalog_telegram.go; then
  ok "C.3 i18n keys present in catalog_telegram.go"
else
  bad "C.3 i18n keys probe_tip_container_off / probe_tip_container_no_accept missing"
fi

echo
echo "=== D. SanityCheckInfraUserOwners + main.go wiring ==="
# D.1 helper file exists
if [ -f internal/feature/admin/infra_owner_sanity.go ]; then
  ok "D.1 infra_owner_sanity.go exists"
else
  bad "D.1 internal/feature/admin/infra_owner_sanity.go missing"
fi

# D.2 helper defined
if grep -qE 'func SanityCheckInfraUserOwners' internal/feature/admin/infra_owner_sanity.go; then
  ok "D.2 SanityCheckInfraUserOwners defined"
else
  bad "D.2 SanityCheckInfraUserOwners not defined"
fi

# D.3 main.go wires the call after ensureInfraUser
if grep -qE 'adminsvc\.SanityCheckInfraUserOwners' cmd/skygate/main.go; then
  ok "D.3 cmd/skygate/main.go calls SanityCheckInfraUserOwners"
else
  bad "D.3 cmd/skygate/main.go does NOT call SanityCheckInfraUserOwners"
fi

# D.4 main.go calls it AFTER ensureInfraUser (line ordering)
if awk '/ensureInfraUser/{found=NR} /SanityCheckInfraUserOwners/{print found; exit}' cmd/skygate/main.go | grep -qE '^[0-9]+$'; then
  ok "D.4 main.go calls SanityCheckInfraUserOwners after ensureInfraUser"
else
  bad "D.4 main.go ordering: SanityCheckInfraUserOwners must run AFTER ensureInfraUser"
fi

echo
echo "=== E. AGENTS.md / verify_pre_deploy.sh reference ==="
# E.1 AGENTS.md mentions the B-block id (B-bug-fix OR B-derp-fix)
if grep -qE 'B-bug-fix|B-derp-fix|2026-09-15 DERP/infra' AGENTS.md; then
  ok "E.1 AGENTS.md mentions the B-bug-fix entry"
else
  bad "E.1 AGENTS.md does NOT mention the B-bug-fix entry"
fi

# E.2 verify_pre_deploy.sh runs the check (best-effort — project may
#     not have a verify_pre_deploy.sh; if missing, no-op)
if [ -f scripts/verify_pre_deploy.sh ]; then
  if grep -qE 'check_b_derp_fix' scripts/verify_pre_deploy.sh; then
    ok "E.2 scripts/verify_pre_deploy.sh includes check_b_derp_fix.sh"
  else
    bad "E.2 scripts/verify_pre_deploy.sh does NOT include check_b_derp_fix.sh"
  fi
else
  echo "  skip E.2 scripts/verify_pre_deploy.sh not present (no-op)"
fi

echo
echo "=== F. Build + tests ==="
# F.1 + F.2: try multiple `go` paths because the operator's
# hybrid Windows + WSL2 host has `go` on the PowerShell
# PATH but not on the WSL2 PATH that this B-check runs in.
# Same fix as scripts/check_b171.sh (B171) — find any
# installed `go` binary, otherwise skip with a friendly note.
GO_BIN=""
for cand in "$(command -v go)" \
    "/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then
    GO_BIN="$cand"
    break
  fi
done

if [ -z "$GO_BIN" ]; then
  echo "  skip F.1 + F.2 (no go binary on PATH — pure-data inspection env)"
else
  if "$GO_BIN" build ./internal/feature/admin/... >/dev/null 2>&1; then
    ok "F.1 go build ./internal/feature/admin/..."
  else
    bad "F.1 go build ./internal/feature/admin/... FAILED"
  fi

  if "$GO_BIN" test ./internal/feature/admin/... -count=1 \
        -run "TestResolveDERPPort|TestResolveSTUNPort|TestShouldBelongToInfra" \
        >/dev/null 2>&1; then
    ok "F.2 go test ./internal/feature/admin/... (new tests pass)"
  else
    bad "F.2 go test ./internal/feature/admin/... FAILED"
  fi
fi

echo
echo "=== Summary ==="
echo "  PASS: $PASS_COUNT"
echo "  FAIL: $FAIL_COUNT"
if [ "$FAIL_COUNT" -gt 0 ]; then
  exit 1
fi
exit 0