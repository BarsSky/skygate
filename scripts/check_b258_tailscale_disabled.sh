#!/usr/bin/env bash
# ============================================================================
# check_b258_tailscale_disabled.sh — B258 Tailscale "intentionally disabled" UI
# ============================================================================
# 2026-09-16: v1.5.8+ (B258) — operator reports "He удалось запустить
# Tailscale: read auth key: open /data/ts/authkey: no such file or
# directory" on /admin/tailscale. Root cause: the operator set
# SKYGATE_TS_AUTHKEY_FILE=/dev/null in docker-compose.yml to disable
# the in-container tailscaled (they manage Tailscale at the host
# level via OS packages). The entrypoint.sh skip check correctly
# skipped tailscaled on container start ("[init] TS_AUTHKEY_FILE not
# set — Tailscale skipped (non-RF mode)"), but the skygate admin
# UI was reading the LEGACY env var name `SKYGATE_TS_AUTHKEY_PATH`
# (with _PATH suffix) instead of `SKYGATE_TS_AUTHKEY_FILE` (with
# _FILE suffix, matching the entrypoint). The UI fell back to
# /data/ts/authkey (the hard-coded default), tried to read it,
# and surfaced the confusing "no such file" error.
#
# B258 fix:
#   1. Rename the env var the UI reads: SKYGATE_TS_AUTHKEY_FILE
#      (matches entrypoint), with _PATH kept as a fallback for
#      older deployments.
#   2. Add the `tailscaleAuthKeyDisabled()` helper that mirrors
#      the entrypoint's `[ -f path ]` skip check. Treats /dev/null,
#      /dev/null/*, and any non-regular-file path as "disabled".
#   3. Wire `AuthKeyDisabled` into TailscaleState so the template
#      can render a clear "Tailscale is disabled by config" banner.
#   4. Guard `handleTailscaleStart` so the click returns a
#      friendly "edit docker-compose.yml + restart" message
#      instead of the raw `os.ReadFile` error.
#
# This B-check pins: source shape + handler wiring + template
# state + i18n parity + AGENTS.md entry.
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
MAIN="cmd/skygate/main.go"
RU="internal/i18n/catalog_tailscale.go"
TEST="internal/feature/admin/tailscale_b258_test.go"

# --- A. helper + state field exist ---
echo "=== A. tailscaleAuthKeyDisabled helper ==="
if grep -qE 'func \(s \*Service\) tailscaleAuthKeyDisabled' "$SRC"; then
  ok "tailscaleAuthKeyDisabled helper defined"
else bad "tailscaleAuthKeyDisabled helper must exist"; fi
if grep -qF 'AuthKeyDisabled bool' "$SRC"; then
  ok "TailscaleState has AuthKeyDisabled bool field"
else bad "TailscaleState must have AuthKeyDisabled bool"; fi

# --- B. main.go env var rename (with fallback) ---
echo
echo "=== B. main.go env var + fallback ==="
if grep -qF 'SKYGATE_TS_AUTHKEY_FILE' "$MAIN"; then
  ok "main.go reads SKYGATE_TS_AUTHKEY_FILE (matches entrypoint)"
else bad "main.go must read SKYGATE_TS_AUTHKEY_FILE"; fi
if grep -qF 'SKYGATE_TS_AUTHKEY_PATH' "$MAIN"; then
  ok "main.go keeps SKYGATE_TS_AUTHKEY_PATH as fallback (backward-compat)"
else bad "main.go must keep SKYGATE_TS_AUTHKEY_PATH as fallback"; fi

# --- C. handler refuses Start when disabled ---
echo
echo "=== C. handleTailscaleStart refuses disabled state ==="
if grep -qF 'tailscaleAuthKeyDisabled()' "$SRC"; then
  ok "handler checks tailscaleAuthKeyDisabled() before start"
else bad "handleTailscaleStart must check tailscaleAuthKeyDisabled()"; fi

# --- D. template renders disabled banner + disables Start ---
echo
echo "=== D. template renders disabled banner ==="
for marker in 'AuthKeyDisabled' 'tailscale.disabled_title' 'tailscale.disabled_help' 'tailscale.disabled_auth_form_help' 'tailscale.disabled_start_tooltip'; do
  if grep -qF "$marker" "$TPL"; then ok "template uses $marker"
  else bad "template must reference $marker"; fi
done
# The Start button must be disabled when AuthKeyDisabled is true.
if grep -qF 'or (not .State.Available) .State.AuthKeyDisabled' "$TPL"; then
  ok "Start button disabled when AuthKeyDisabled"
else bad "Start button must be disabled when AuthKeyDisabled"; fi

# --- E. i18n keys (RU + EN) ---
echo
echo "=== E. i18n keys (RU + EN) ==="
for k in disabled_title disabled_help disabled_auth_form_help disabled_start_tooltip; do
  cnt=$(grep -cF "\"tailscale.$k\"" "$RU" 2>/dev/null || echo 0)
  if [ "$cnt" -ge 2 ]; then ok "i18n key present (RU+EN): tailscale.$k"
  else bad "i18n key $k only in $cnt/2 maps (need RU+EN)"; fi
done

# --- F. unit tests ---
echo
echo "=== F. unit tests for the disabled check ==="
for t in TestTailscaleAuthKeyDisabled_DevNull \
         TestTailscaleAuthKeyDisabled_DevNullSlash \
         TestTailscaleAuthKeyDisabled_EmptyPath \
         TestTailscaleAuthKeyDisabled_CharacterDevice \
         TestTailscaleAuthKeyDisabled_RegularFileOK \
         TestTailscaleAuthKeyDisabled_MissingFile; do
  if grep -qF "func $t" "$TEST"; then ok "$t exists"
  else bad "$t must exist in $TEST"; fi
done

# --- G. go build / vet / test pass ---
echo
echo "=== G. go build + vet + classifier tests ==="
GO="$(command -v go 2>/dev/null || true)"
if [ -z "$GO" ] && [ -x "$REPO_ROOT/scripts/find_go.sh" ]; then
  win_go="$(bash "$REPO_ROOT/scripts/find_go.sh" 2>/dev/null | tr -d '\r' | head -1)"
  if [ -n "$win_go" ]; then GO="$win_go"; fi
fi
if [ -z "$GO" ]; then
  echo "  (skipping G — go binary not found)"
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
  if GO_RUN test ./internal/feature/admin/ -run TestTailscaleAuthKeyDisabled; then
    ok "go test ./internal/feature/admin/ -run TestTailscaleAuthKeyDisabled"
  else
    bad "go test classifier failed"
  fi
fi

# --- H. AGENTS.md B258 catalog entry ---
echo
echo "=== H. AGENTS.md B258 catalog entry ==="
if grep -qF 'B258' "$REPO_ROOT/AGENTS.md"; then ok "AGENTS.md mentions B258"
else bad "AGENTS.md must mention B258 + the disabled-by-config rationale"; fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]
