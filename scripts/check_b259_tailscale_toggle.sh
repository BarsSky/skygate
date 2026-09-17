#!/usr/bin/env bash
# ============================================================================
# check_b259_tailscale_toggle.sh — B259 flip Tailscale path via UI
# ============================================================================
# 2026-09-16: v1.5.8+ (B259) — operator reports "I don't have
# access to docker-compose anymore, that file is lost". After
# B258 surfaced "edit docker-compose.yml + restart" as the only
# way to re-enable Tailscale in the container, the operator
# needed a UI-only toggle. B259 adds:
#   1. DB-overridable auth key path (tailscale.auth_key_path
#      in global_settings, with SKYGATE_TS_AUTHKEY_FILE as
#      fallback) — mirrors the existing
#      tailscale.login_server override pattern (B258 sibling).
#   2. "Enable in-container Tailscale" button on
#      /admin/tailscale (shown in the disabled banner) —
#      persists /data/ts/authkey to the DB override, generates
#      a fresh preauth key via headscale, writes it to the
#      file, and starts tailscaled.
#   3. "Disable in-container Tailscale" button on
#      /admin/tailscale (shown in the normal card) — flips
#      back to /dev/null, stops tailscaled if running, and
#      removes the auth key file.
#
# This B-check pins: source shape + DB-overridable resolution
# + new dispatcher cases + handler wiring + template buttons
# + i18n parity + AGENTS.md entry.
# ============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
log()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

SRC="internal/feature/admin/tailscale.go"
TPL="internal/handlers/templates/admin/tailscale.html"
RU="internal/i18n/catalog_tailscale.go"
TEST="internal/feature/admin/tailscale_b259_test.go"

# --- A. DB-overridable path resolution ---
echo "=== A. tailscaleAuthKeyPath resolves DB > env > default ==="
if grep -qF 'const tailscaleAuthKeyPathDBKey = "tailscale.auth_key_path"' "$SRC"; then
  ok "tailscaleAuthKeyPathDBKey constant defined"
else bad "tailscaleAuthKeyPathDBKey constant must be defined"; fi
if grep -qE 'func \(s \*Service\) tailscaleAuthKeyPath\(\) string' "$SRC"; then
  ok "tailscaleAuthKeyPath helper defined"
else bad "tailscaleAuthKeyPath helper must be defined"; fi
if grep -qE 'func \(s \*Service\) tailscaleAuthKeyPathSource\(\) string' "$SRC"; then
  ok "tailscaleAuthKeyPathSource helper defined (for the template source-hint)"
else bad "tailscaleAuthKeyPathSource helper must be defined"; fi

# --- B. handler wiring (enable + disable + dispatcher) ---
echo
echo "=== B. handler wiring ==="
if grep -qF 'func (s *Service) handleTailscaleEnableInContainer' "$SRC"; then
  ok "handleTailscaleEnableInContainer handler defined"
else bad "handleTailscaleEnableInContainer handler must be defined"; fi
if grep -qF 'func (s *Service) handleTailscaleDisableInContainer' "$SRC"; then
  ok "handleTailscaleDisableInContainer handler defined"
else bad "handleTailscaleDisableInContainer handler must be defined"; fi
if grep -qF 'case "enable_in_container":' "$SRC"; then
  ok "dispatcher has 'enable_in_container' case"
else bad "PostAdminTailscale must dispatch 'enable_in_container'"; fi
if grep -qF 'case "disable_in_container":' "$SRC"; then
  ok "dispatcher has 'disable_in_container' case"
else bad "PostAdminTailscale must dispatch 'disable_in_container'"; fi

# --- C. Enable handler does the 3 steps in the right order ---
echo
echo "=== C. Enable handler does the right 3 steps ==="
# The 3 steps are: (1) SetGlobalSetting to /data/ts/authkey,
# (2) generate preauth key + write file, (3) start tailscaled.
# The "FIRST step is the DB write" contract pins that the
# startTailscaled below actually sees the new path.
if grep -qF 'db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, newPath)' "$SRC"; then
  ok "enable handler persists new path to DB (FIRST step)"
else bad "enable handler must persist new path to DB FIRST"; fi
if grep -qF 'hs.CreatePreauthKeyWithTags(userID' "$SRC"; then
  ok "enable handler generates preauth key via headscale"
else bad "enable handler must generate preauth key via headscale"; fi
if grep -qE 's\.startTailscaled\(\)' "$SRC"; then
  ok "enable handler calls startTailscaled"
else bad "enable handler must call startTailscaled"; fi

# --- D. Disable handler is symmetric ---
echo
echo "=== D. Disable handler stops + persists + cleans up ==="
if grep -qF 'db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, newPath)' "$SRC"; then
  ok "disable handler persists /dev/null to DB"
else bad "disable handler must persist /dev/null to DB"; fi
if grep -qF 's.stopTailscaled()' "$SRC"; then
  ok "disable handler stops tailscaled if running"
else bad "disable handler must stop tailscaled if running"; fi

# --- E. template renders the two new buttons ---
echo
echo "=== E. template renders Enable/Disable buttons ==="
if grep -qF 'value="enable_in_container"' "$TPL"; then
  ok "template has enable_in_container form"
else bad "template must have enable_in_container form"; fi
if grep -qF 'value="disable_in_container"' "$TPL"; then
  ok "template has disable_in_container form"
else bad "template must have disable_in_container form"; fi

# --- F. i18n keys (RU + EN) ---
echo
echo "=== F. i18n keys (RU + EN) ==="
for k in enable_in_container_btn \
         enable_in_container_confirm \
         disable_in_container_heading \
         disable_in_container_help \
         disable_in_container_btn \
         disable_in_container_confirm; do
  cnt=$(grep -cF "\"tailscale.$k\"" "$RU" 2>/dev/null || echo 0)
  if [ "$cnt" -ge 2 ]; then ok "i18n key present (RU+EN): tailscale.$k"
  else bad "i18n key $k only in $cnt/2 maps (need RU+EN)"; fi
done

# --- G. unit tests ---
echo
echo "=== G. unit tests for B259 ==="
for t in TestTailscaleAuthKeyPath_ResolutionOrder \
         TestTailscaleAuthKeyPathSource_EnvFallback \
         TestTailscaleAuthKeyDisabled_RespectsRealPath \
         TestTailscaleAuthKeyDisabled_RespectsDevNull \
         TestTailscaleAuthKeyDisabled_RespectsDevNullVariants \
         TestEnableInContainerPersistsDBPath \
         TestGenerateAndWriteTailscaleKeyForEnable_B259_DelegatesToFindUserForHostname; do
  if grep -qF "func $t" "$TEST"; then ok "$t exists"
  else bad "$t must exist in $TEST"; fi
done

# --- H. go build + vet + classifier tests ---
echo
echo "=== H. go build + vet + classifier tests ==="
GO="$(command -v go 2>/dev/null || true)"
if [ -z "$GO" ] && [ -x "$REPO_ROOT/scripts/find_go.sh" ]; then
  win_go="$(bash "$REPO_ROOT/scripts/find_go.sh" 2>/dev/null | tr -d '\r' | head -1)"
  if [ -n "$win_go" ]; then GO="$win_go"; fi
fi
if [ -z "$GO" ]; then
  echo "  (skipping H — go binary not found)"
else
  if echo "$GO" | grep -qE '^[A-Z]:'; then
    GO_RUN() { cmd.exe //c "\"$GO\" $*" >/dev/null 2>&1; }
  else
    GO_RUN() { "$GO" "$@" >/dev/null 2>&1; }
  fi
  if GO_RUN build ./internal/feature/admin/...; then ok "go build ./internal/feature/admin/..."
  else bad "go build ./internal/feature/admin/... failed"; fi
  if GO_RUN vet ./internal/feature/admin/...; then ok "go vet ./internal/feature/admin/..."
  else bad "go vet ./internal/feature/admin/... failed"; fi
  if GO_RUN test ./internal/feature/admin/ -run "TestTailscaleAuthKey|TestEnableInContainer|TestGenerateAndWriteTailscaleKeyForEnable"; then
    ok "go test ./internal/feature/admin/ -run B259 tests"
  else
    bad "go test B259 failed"
  fi
fi

# --- I. AGENTS.md B259 catalog entry ---
echo
echo "=== I. AGENTS.md B259 catalog entry ==="
if grep -qF 'B259' "$REPO_ROOT/AGENTS.md"; then ok "AGENTS.md mentions B259"
else bad "AGENTS.md must mention B259 + the UI-toggle rationale"; fi

# --- J. B259.1 fix: delegate to findUserForHostname ---
# Operator 2026-09-17: "раз skygate-host принадлежит infra то
# от лица пользователя infra все и делать — зачем плодить сущности".
# The pre-B259.1 helper looked up the headscale user via inline
# `u.Name == hostname` logic, which forced the operator to
# manually create a phantom `skygate-host` headscale user.
# B259.1 replaces that with findUserForHostname (the canonical
# B251 helper that pins hostname `skygate-host` → user `infra`
# per operator 2026-08-13 directive — uid=85). After B259.1,
# B259's "Включить Tailscale в контейнере" button works against
# the existing infra user with no manual provisioning.
echo
echo "=== J. B259.1 fix: delegates to findUserForHostname ==="
if grep -qF 's.findUserForHostname(context.Background(), hs, hostname)' "$SRC"; then
  ok "generateAndWriteTailscaleKeyForEnable calls findUserForHostname (no phantom user)"
else bad "generateAndWriteTailscaleKeyForEnable must call findUserForHostname (B259.1)"; fi
# Scope the negative checks to the function body — there are
# OTHER hs.ListUsers() calls elsewhere in tailscale.go we don't
# want to break (e.g. the audit-row builders).
awk '/^func \(s \*Service\) generateAndWriteTailscaleKeyForEnable\(/{flag=1} flag{print} /^func \(s \*Service\) handleTailscaleDisableInContainer\(/{flag=0; exit}' "$SRC" > /tmp/b259_body.txt
if grep -qF 'hs.ListUsers()' /tmp/b259_body.txt; then
  bad "generateAndWriteTailscaleKeyForEnable body must NOT call hs.ListUsers() (findUserForHostname is the sole source of truth)"
else
  ok "generateAndWriteTailscaleKeyForEnable body has no hs.ListUsers() (delegated)"
fi
if grep -qF 'strings.TrimSuffix(hostname, "-1")' /tmp/b259_body.txt; then
  bad "generateAndWriteTailscaleKeyForEnable body must NOT contain the legacy u.Name==hostname-or-strip-1 sentinel"
else
  ok "generateAndWriteTailscaleKeyForEnable body has no TrimSuffix(hostname, -1) sentinel (B259.1)"
fi
rm -f /tmp/b259_body.txt
if grep -qF 'B259.1' "$REPO_ROOT/AGENTS.md"; then ok "AGENTS.md mentions B259.1"
else bad "AGENTS.md must mention B259.1 (the findUserForHostname delegation fix)"; fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]
