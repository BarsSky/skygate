#!/usr/bin/env bash
# ============================================================================
# check_b257_adopt_devices.sh — B257 device adoption for pre-existing tailnets
# ============================================================================
# 2026-09-16: v1.5.8+ (B257) — when skygate is installed on top of an
# EXISTING headscale (sidecar install path), the B77 backfill
# can't auto-claim nodes that:
#   - have no preauth key match (they were registered via direct
#     headscale preauth or OIDC, not /my/preauth)
#   - have no existing tag:dev-* tag (that's what we're trying to add)
#   - have PreAuthKeyID set (Strategy E OIDC guard rejects)
# The operator had to SSH + `headscale nodes tag --force` manually
# for each one. B257 closes the gap with a "Devices awaiting
# adoption" card on /admin/devices — the per-row "Assign to
# <user>" button inserts into node_owner_map, calls EnsureTagOwner,
# and AddTag's the dev-tag in one click.
#
# This B-check pins: source shape + handler wiring + template
# slot + JS + route registration + i18n parity + AGENTS.md entry.
# ============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
log()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

SRC="internal/feature/admin/adopt_devices.go"
TPL="internal/handlers/templates/admin/devices.html"
MAIN="cmd/skygate/main.go"
RU="internal/i18n/catalog_my.go"
AGENTS="AGENTS.md"
TEST="internal/feature/admin/adopt_devices_b257_test.go"

# --- A. classification helper exists ---
echo "=== A. classifyNodeForAdoption helper ==="
if grep -qF 'func classifyNodeForAdoption' "$SRC"; then
  ok "classifyNodeForAdoption helper defined"
else bad "classifyNodeForAdoption helper must exist (used by findAdoptionCandidates)"; fi
if grep -qE 'func \(s \*Service\) findAdoptionCandidates' "$SRC"; then
  ok "findAdoptionCandidates method on *Service"
else bad "findAdoptionCandidates must be defined on *Service"; fi

# --- B. handler exists ---
echo
echo "=== B. PostAdminDeviceAdopt handler ==="
if grep -qE 'func \(s \*Service\) PostAdminDeviceAdopt' "$SRC"; then
  ok "PostAdminDeviceAdopt handler defined"
else bad "PostAdminDeviceAdopt handler must be defined"; fi
if grep -qF 'EnsureTagOwner' "$SRC"; then
  ok "handler calls EnsureTagOwner (B245 idempotent tagOwners)"
else bad "handler must call EnsureTagOwner before AddTag"; fi
if grep -qF 'db.UpsertNodeOwner' "$SRC"; then
  ok "handler inserts into node_owner_map"
else bad "handler must call db.UpsertNodeOwner"; fi
if grep -qF 'hs.AddTag(' "$SRC"; then
  ok "handler calls headscale AddTag (with b177 fallback logging)"
else bad "handler must call hs.AddTag for the new dev-tag"; fi

# --- C. template renders the adoption card ---
echo
echo "=== C. template renders Devices-awaiting-adoption card ==="
if grep -qF 'devices.adoption_card_title' "$TPL"; then
  ok "template uses devices.adoption_card_title i18n key"
else bad "template must render the adoption card title via devices.adoption_card_title"; fi
if grep -qF '/admin/devices/adopt' "$TPL"; then
  ok "template form posts to /admin/devices/adopt"
else bad "template must post to /admin/devices/adopt"; fi
if grep -qF 'AdoptionCandidates' "$TPL"; then
  ok "template reads AdoptionCandidates from context"
else bad "template must loop over AdoptionCandidates"; fi

# --- D. route registration in main.go ---
echo
echo "=== D. main.go POST /admin/devices/adopt route ==="
if grep -qF 'POST /admin/devices/adopt' "$MAIN"; then
  ok "POST /admin/devices/adopt registered"
else bad "main.go must register POST /admin/devices/adopt"; fi
if grep -qF 'PostAdminDeviceAdopt' "$MAIN"; then
  ok "PostAdminDeviceAdopt wired into main.go"
else bad "main.go must reference PostAdminDeviceAdopt"; fi
# Also check AdoptionCandidates is passed to the template
if grep -qF '"AdoptionCandidates": s.findAdoptionCandidates' internal/feature/admin/devices.go; then
  ok "GetAdminDevices populates AdoptionCandidates"
else bad "GetAdminDevices must populate AdoptionCandidates in template context"; fi

# --- E. i18n keys (RU + EN) ---
echo
echo "=== E. i18n keys (RU + EN) ==="
for k in adoption_card_title adoption_card_help adoption_col_node adoption_col_ip adoption_col_os adoption_col_role adoption_col_assign adoption_button adoption_confirm; do
  cnt=$(grep -cF "\"devices.$k\"" "$RU" 2>/dev/null || echo 0)
  if [ "$cnt" -ge 2 ]; then ok "i18n key present (RU+EN): devices.$k"
  else bad "i18n key $k only in $cnt/2 maps (need RU+EN)"; fi
done

# --- F. unit tests (pure function) ---
echo
echo "=== F. unit tests for the classifier ==="
for t in TestClassifyNodeForAdoption_HappyPath \
         TestClassifyNodeForAdoption_RejectEmptyNodeID \
         TestClassifyNodeForAdoption_RejectAlreadyOwned \
         TestClassifyNodeForAdoption_RejectEmptyUserName \
         TestClassifyNodeForAdoption_RejectOrphanHeadscaleUser \
         TestClassifyNodeForAdoption_RejectZeroPortalID \
         TestClassifyNodeForAdoption_PopulatesOSAndRole; do
  if grep -qF "func $t" "$TEST"; then ok "$t exists"
  else bad "$t must exist in $TEST"; fi
done

# --- G. go build / vet / test pass ---
echo
echo "=== G. go build + vet + classifier tests ==="
# Find go binary — bash on Windows often doesn't have it on PATH.
GO="$(command -v go 2>/dev/null || true)"
if [ -z "$GO" ] && [ -x "$REPO_ROOT/scripts/find_go.sh" ]; then
  win_go="$(bash "$REPO_ROOT/scripts/find_go.sh" 2>/dev/null | tr -d '\r' | head -1)"
  if [ -n "$win_go" ]; then GO="$win_go"; fi
fi

if [ -z "$GO" ]; then
  echo "  (skipping G — go binary not found)"
else
  # If GO is a Windows-style path (C:\...), invoke through cmd.exe
  # because bash can't exec it directly under mixed-mode cygwin/MSYS.
  if echo "$GO" | grep -qE '^[A-Z]:'; then
    GO_RUN() { cmd.exe //c "\"$GO\" $*" >/dev/null 2>&1; }
  else
    GO_RUN() { "$GO" "$@" >/dev/null 2>&1; }
  fi
  if GO_RUN build ./internal/feature/admin/...; then ok "go build ./internal/feature/admin/..."
  else bad "go build ./internal/feature/admin/... failed"; fi
  if GO_RUN vet ./internal/feature/admin/...; then ok "go vet ./internal/feature/admin/..."
  else bad "go vet ./internal/feature/admin/... failed"; fi
  if GO_RUN test ./internal/feature/admin/ -run TestClassifyNodeForAdoption; then
    ok "go test ./internal/feature/admin/ -run TestClassifyNodeForAdoption"
  else
    bad "go test classifier failed"
  fi
fi

# --- H. AGENTS.md B257 catalog entry ---
echo
echo "=== H. AGENTS.md B257 catalog entry ==="
if grep -qF 'B257' "$AGENTS"; then ok "AGENTS.md mentions B257"
else bad "AGENTS.md must mention B257 + the adoption rationale"; fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]
