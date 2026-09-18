#!/usr/bin/env bash
# ============================================================================
# check_b253_telegram_async.sh — B253 Telegram probe async refresh
# ============================================================================
# 2026-09-15: v1.5.6+ (B253) — /admin/telegram page was blocking
# the request thread on the 5-second Telegram API timeout on every
# cache miss. Operator UX: page loads as slow as Telegram times out.
#
# B253 fix:
#  1. cachedTelegramProbe is now stale-while-revalidate: returns
#     the last-known result instantly and kicks off a background
#     goroutine to refresh. Page render NEVER blocks.
#  2. Separate TTLs: 30s for success (operator wants fresh data on
#     reload), 5 min for failure (single Telegram outage doesn't
#     keep every page load slow).
#  3. New POST /admin/telegram/probe/now handler bypasses the
#     cache for explicit "Probe now" clicks. The button is
#     intentionally slow on Telegram timeout (up to 5s) — the
#     operator asked for it.
#  4. TelegramProbeResult gets Stale + StaleAt fields so the
#     template can show "обновится после HH:MM:SS" hint.
#
# This B-check pins the source shape + handler wiring + i18n.
# ============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
log()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

# --- A. async refresh in telegram.go ---
echo "=== A. async refresh in cachedTelegramProbe ==="
TG="internal/feature/admin/telegram.go"
if grep -qF 'refreshProbeAsync' "$TG" 2>/dev/null; then
  ok "refreshProbeAsync called from cachedTelegramProbe"
else bad "cachedTelegramProbe must call refreshProbeAsync on cache miss/stale"; fi
if grep -qE 'go s\.refreshProbeAsync\(' "$TG" 2>/dev/null; then
  ok "refreshProbeAsync spawned as a goroutine (non-blocking)"
else bad "refreshProbeAsync must be spawned via 'go s.refreshProbeAsync(...)' to avoid blocking the page"; fi

# --- B. separate TTLs (success vs error) ---
echo
echo "=== B. separate TTLs ==="
if grep -qF 'telegramProbeTTLSuccess' "$TG" 2>/dev/null; then
  ok "telegramProbeTTLSuccess constant present"
else bad "telegramProbeTTLSuccess must be defined (replaces the old 30s-only TTL)"; fi
if grep -qF 'telegramProbeTTLError' "$TG" 2>/dev/null; then
  ok "telegramProbeTTLError constant present (5min failure TTL)"
else bad "telegramProbeTTLError must be defined"; fi
if grep -qE 'telegramProbeTTLError\s*=\s*5\s*\*\s*time\.Minute' "$TG" 2>/dev/null; then
  ok "telegramProbeTTLError = 5 min"
else bad "telegramProbeTTLError should be 5 minutes"; fi
if grep -qE 'telegramProbeTTLSuccess\s*=\s*30\s*\*\s*time\.Second' "$TG" 2>/dev/null; then
  ok "telegramProbeTTLSuccess = 30s"
else bad "telegramProbeTTLSuccess should be 30 seconds"; fi

# --- C. probeNowSync function exists ---
echo
echo "=== C. probeNowSync bypass-cache path ==="
if grep -qF 'func (s *Service) probeNowSync' "$TG" 2>/dev/null; then
  ok "probeNowSync function defined"
else bad "probeNowSync (synchronous bypass) must be defined"; fi
if grep -qF 'PostAdminTelegramProbeNow' "$TG" 2>/dev/null; then
  ok "PostAdminTelegramProbeNow handler defined"
else bad "PostAdminTelegramProbeNow handler must be defined"; fi

# --- D. TelegramProbeResult gets Stale + StaleAt fields ---
echo
echo "=== D. TelegramProbeResult.Stale + StaleAt fields ==="
PROBE="internal/feature/admin/telegram_probe.go"
if grep -qF 'Stale   bool' "$PROBE" 2>/dev/null; then
  ok "TelegramProbeResult.Stale bool field present"
else bad "TelegramProbeResult.Stale bool must be present (B253)"; fi
if grep -qF 'StaleAt string' "$PROBE" 2>/dev/null; then
  ok "TelegramProbeResult.StaleAt string field present"
else bad "TelegramProbeResult.StaleAt string must be present"; fi

# --- E. main.go wires the POST route ---
echo
echo "=== E. main.go POST /admin/telegram/probe/now route ==="
MAIN="cmd/skygate/main.go"
if grep -qF 'POST /admin/telegram/probe/now' "$MAIN" 2>/dev/null; then
  ok "POST /admin/telegram/probe/now registered"
else bad "main.go must register POST /admin/telegram/probe/now"; fi
if grep -qF 'PostAdminTelegramProbeNow' "$MAIN" 2>/dev/null; then
  ok "PostAdminTelegramProbeNow wired into main.go"
else bad "PostAdminTelegramProbeNow handler must be referenced in main.go"; fi

# --- F. /admin/telegram template has Probe now button ---
echo
echo "=== F. /admin/telegram template UI ==="
TPL="internal/handlers/templates/admin/telegram.html"
if grep -qF '/admin/telegram/probe/now' "$TPL" 2>/dev/null; then
  ok "template form posts to /admin/telegram/probe/now"
else bad "template must contain a form posting to /admin/telegram/probe/now"; fi
if grep -qF 'probe_now' "$TPL" 2>/dev/null; then
  ok "template references probe_now i18n key"
else bad "template must reference the probe_now i18n key"; fi
if grep -qF 'probe_stale' "$TPL" 2>/dev/null; then
  ok "template references probe_stale i18n key (stale indicator)"
else bad "template must reference probe_stale i18n key for the stale indicator"; fi

# --- G. i18n keys (RU + EN) ---
echo
echo "=== G. i18n keys (RU + EN) ==="
RU="internal/i18n/catalog_telegram.go"
for k in probe_now probe_stale probe_stale_until; do
  cnt=$(grep -cF "\"telegram.$k\"" "$RU" 2>/dev/null || echo 0)
  if [ "$cnt" -ge 2 ]; then ok "i18n key present (RU+EN): telegram.$k"
  else bad "i18n key $k only in $cnt/2 maps (need RU+EN)"; fi
done

# --- H. AGENTS.md B253 entry ---
echo
echo "=== H. AGENTS.md B253 catalog entry ==="
AGENTS="AGENTS.md"
if grep -qF 'B253' "$AGENTS" 2>/dev/null && grep -qE 'telegram probe async|Telegram probe async' "$AGENTS" 2>/dev/null; then
  ok "AGENTS.md mentions B253 + Telegram probe async"
else bad "AGENTS.md must mention B253 + the async-refresh rationale"; fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]