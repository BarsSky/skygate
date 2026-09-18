#!/usr/bin/env bash
# ============================================================================
# check_b255_telegram_async.sh — B255 Telegram background polling + nearest egress
# ============================================================================
# 2026-09-16: v1.5.8+ (B255) — /admin/telegram page was blocking the
# request thread on TWO synchronous operations on every render:
#   1. cachedTelegramProbe (api.telegram.org HTTP, 5s timeout, 30s cache)
#   2. readContainerTailscaleState (docker exec tailscale status --json, 8s timeout)
# On cold cache + slow tailscaled, the page took ~5-8s to render.
# Operator UX: "page just hangs when I open it".
#
# B255 fix:
#  1. Page renders immediately with a CSS spinner slot.
#  2. Two new background GET handlers fire the slow calls asynchronously:
#     - GET /admin/telegram/probe-bg      → AdminTelegramProbeBg
#     - GET /admin/telegram/container-bg  → AdminTelegramContainerBg
#  3. The HTML returned by the bg handlers is dropped into the slot
#     via `el.outerHTML = ...` by the JS in admin/telegram.html.
#  4. JS re-polls on a cadence (probe 30s, container 15s) so the
#     operator sees the effect of a "Re-apply accept-routes" click
#     within ~15s without reloading the page.
#  5. New "Pin nearest exit node" button on /admin/telegram. The
#     handler (handleTelegramSetNearestEgress) measures latency from
#     skygate-host's tailscaled via `tailscale status --json`
#     PeerLatency, picks the lowest-latency enabled relay, and reuses
#     the existing set_egress SSH + advertise-routes path.
#
# This B-check pins: source shape + handler wiring + template slot +
# JS poll + route registration + i18n parity + AGENTS.md entry.
# ============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Discover the `go` binary. Linux/macOS have it on PATH; on Windows
# (Git Bash, MSYS) we need to defer to find_go.sh because the
# system PATH may not include /c/Program Files/Go/bin/ — the bash
# tool from Mavis on Windows runs through Git Bash which has a
# stripped PATH. find_go.sh delegates to PowerShell via cmd.exe to
# find the binary and returns its Windows path.
GO="$(command -v go 2>/dev/null || true)"
if [ -z "$GO" ] && [ -x "$REPO_ROOT/scripts/find_go.sh" ]; then
  win_go="$(bash "$REPO_ROOT/scripts/find_go.sh" 2>/dev/null | tr -d '\r' | head -1)"
  if [ -n "$win_go" ]; then GO="$win_go"; fi
fi
if [ -z "$GO" ]; then
  echo "ERROR: could not find 'go' binary on PATH or in common Windows locations"
  exit 2
fi

PASS=0
FAIL=0
log()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

TG="internal/feature/admin/telegram.go"
MAIN="cmd/skygate/main.go"
TPL="internal/handlers/templates/admin/telegram.html"
RU="internal/i18n/catalog_telegram.go"
AGENTS="AGENTS.md"

# --- A. background probe + container handlers exist ---
echo "=== A. AdminTelegramProbeBg + AdminTelegramContainerBg handlers ==="
for fn in AdminTelegramProbeBg AdminTelegramContainerBg; do
  if grep -qF "func (s *Service) $fn" "$TG" 2>/dev/null; then
    ok "$fn defined"
  else bad "$fn handler must be defined"; fi
done

# --- B. AdminTelegram + loadTelegramUIState no longer block on slow ops ---
echo
echo "=== B. AdminTelegram page no longer blocks on probe/container ==="
# The pre-B255 path ran cachedTelegramProbe + readContainerTailscaleState
# inline. Now they must be inside the bg handlers only. We extract the
# body of loadTelegramUIState + AdminTelegram + AdminTelegramPost and
# grep for the slow helpers — but strip `//` lines first because the
# pre-B255 calls survive as a "this used to run" comment in the
# AdminTelegram body (informative for future maintainers).
slow_in_page=$(awk '
  /^func / { f=0; lc=0 }
  /^func \(s \*Service\) loadTelegramUIState\(\) telegramUIState/      {f=1}
  /^func \(s \*Service\) AdminTelegram\(w http\.ResponseWriter, r \*http\.Request\) \{/ {f=1}
  /^func \(s \*Service\) AdminTelegramPost\(w http\.ResponseWriter, r \*http\.Request\) \{/ {f=1}
  f {print; lc++; if (lc > 80) {f=0}}
' "$TG" | grep -v '^\s*//' | grep -v '^\s*$')
if echo "$slow_in_page" | grep -qF 'cachedTelegramProbe'; then
  bad "AdminTelegram/loadTelegramUIState must NOT call cachedTelegramProbe synchronously"
else ok "AdminTelegram + loadTelegramUIState no longer call cachedTelegramProbe sync"; fi
if echo "$slow_in_page" | grep -qF 'readContainerTailscaleState'; then
  bad "AdminTelegram/loadTelegramUIState must NOT call readContainerTailscaleState synchronously"
else ok "AdminTelegram + loadTelegramUIState no longer call readContainerTailscaleState sync"; fi

# --- C. renderProbeHTML + renderContainerHTML helpers ---
echo
echo "=== C. renderProbeHTML + renderContainerHTML helpers ==="
for fn in renderProbeHTML renderContainerHTML; do
  if grep -qF "func $fn(" "$TG" 2>/dev/null; then
    ok "$fn helper defined"
  else bad "$fn helper must be defined"; fi
done

# --- D. template has slot divs + JS poll ---
echo
echo "=== D. template slot divs + JS poll ==="
for slot in 'telegram-probe-slot' 'telegram-container-slot'; do
  cnt=$(grep -cF "$slot" "$TPL" 2>/dev/null || echo 0)
  if [ "$cnt" -ge 2 ]; then
    ok "$slot referenced in template (slot div + render output)"
  else bad "$slot referenced only $cnt/2 times (need slot div + helper output)"; fi
done
for js in "fetchAndSwap" "pollProbe()" "pollContainer()" "setInterval(pollProbe" "setInterval(pollContainer"; do
  if grep -qF "$js" "$TPL" 2>/dev/null; then
    ok "JS contains $js"
  else bad "JS must contain $js"; fi
done

# --- E. route registration in main.go ---
echo
echo "=== E. route registration in main.go ==="
for route in 'GET /admin/telegram/probe-bg' 'GET /admin/telegram/container-bg'; do
  if grep -qF "$route" "$MAIN" 2>/dev/null; then
    ok "route registered: $route"
  else bad "main.go must register route $route"; fi
done
for handler in AdminTelegramProbeBg AdminTelegramContainerBg; do
  if grep -qF "$handler" "$MAIN" 2>/dev/null; then
    ok "$handler wired into main.go"
  else bad "main.go must reference $handler"; fi
done

# --- F. "Pin nearest exit node" button + handler ---
echo
echo "=== F. Pin nearest exit node button + handler ==="
if grep -qF 'func (s *Service) handleTelegramSetNearestEgress' "$TG" 2>/dev/null; then
  ok "handleTelegramSetNearestEgress defined"
else bad "handleTelegramSetNearestEgress must be defined"; fi
if grep -qF 'set_nearest_egress' "$TG" 2>/dev/null; then
  ok "set_nearest_egress action wired in dispatcher"
else bad "AdminTelegramPost must handle set_nearest_egress action"; fi
if grep -qF 'set_nearest_egress' "$TPL" 2>/dev/null; then
  ok "template form posts to set_nearest_egress"
else bad "template must contain form posting to set_nearest_egress"; fi
if grep -qF 'egress_nearest_apply' "$TPL" 2>/dev/null; then
  ok "template uses egress_nearest_apply i18n key"
else bad "template must reference egress_nearest_apply i18n key"; fi

# --- G. tailscalePeerLatencies helper ---
echo
echo "=== G. tailscalePeerLatencies helper (PeerLatency from 'tailscale status --json') ==="
if grep -qF 'func tailscalePeerLatencies' "$TG" 2>/dev/null; then
  ok "tailscalePeerLatencies defined"
else bad "tailscalePeerLatencies must be defined"; fi
if grep -qF 'PeerLatency' "$TG" 2>/dev/null; then
  ok "helper parses PeerLatency from status JSON"
else bad "helper must parse PeerLatency field"; fi
if grep -qF '"ms"' "$TG" 2>/dev/null; then
  ok "helper accepts the modern {ms: N} PeerLatency object shape"
else bad "helper must accept modern {ms: N} shape"; fi

# --- H. i18n keys (RU + EN) ---
echo
echo "=== H. i18n keys (RU + EN) ==="
for k in egress_nearest_apply egress_nearest_apply_help egress_nearest_apply_confirm egress_nearest_no_latency; do
  cnt=$(grep -cF "\"telegram.$k\"" "$RU" 2>/dev/null || echo 0)
  if [ "$cnt" -ge 2 ]; then ok "i18n key present (RU+EN): telegram.$k"
  else bad "i18n key $k only in $cnt/2 maps (need RU+EN)"; fi
done

# --- I. test file present + key tests exist ---
echo
echo "=== I. test file + coverage ==="
TEST="internal/feature/admin/telegram_b255_test.go"
if [ -f "$TEST" ]; then ok "$TEST exists"
else bad "$TEST must exist"; fi
for t in TestRenderProbeHTML_OkDirect TestRenderProbeHTML_Unreachable_ContainerOff TestRenderProbeHTML_OkRelay TestRenderContainerHTML_AvailableRouteAllOff TestTailscalePeerLatencies_ParseObjectShape TestTailscalePeerLatencies_ParseLegacyBareNumber; do
  if grep -qF "func $t" "$TEST" 2>/dev/null; then ok "$t exists"
  else bad "$t must exist in $TEST"; fi
done

# --- J. build + vet + test pass ---
echo
echo "=== J. build + vet + admin tests ==="
# Run go via cmd.exe if the discovered path is a Windows path
# (Git Bash can't exec .exe files directly when found via
# PowerShell — only cmd.exe can).
if echo "$GO" | grep -qE '^[A-Z]:'; then
  GO_RUN() { cmd.exe //c "\"$GO\" $*" >/dev/null 2>&1; }
  GO_VERBOSE() { cmd.exe //c "\"$GO\" $*"; }
else
  GO_RUN() { "$GO" "$@" >/dev/null 2>&1; }
  GO_VERBOSE() { "$GO" "$@"; }
fi
if GO_RUN build ./...; then ok "go build ./..."
else bad "go build ./... failed"; fi
if GO_RUN vet ./internal/feature/admin/...; then ok "go vet ./internal/feature/admin/..."
else bad "go vet ./internal/feature/admin/... failed"; fi
if GO_RUN test ./internal/feature/admin/ -run 'TestRender|TestTailscalePeerLatencies'; then
  ok "go test ./internal/feature/admin/ -run TestRender|TestTailscalePeerLatencies"
else bad "go test ./internal/feature/admin/ -run TestRender|TestTailscalePeerLatencies failed"; fi

# --- K. AGENTS.md B255 catalog entry ---
echo
echo "=== K. AGENTS.md B255 catalog entry ==="
if grep -qF 'B255' "$AGENTS" 2>/dev/null && grep -qE 'telegram background poll|Telegram background poll|nearest exit node|Pin nearest' "$AGENTS" 2>/dev/null; then
  ok "AGENTS.md mentions B255 + the bg-poll / nearest rationale"
else bad "AGENTS.md must mention B255 + the bg-poll / nearest rationale"; fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]
