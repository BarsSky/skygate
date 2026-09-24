#!/usr/bin/env bash
# ============================================================================
# check_b252_derp_cert_sync.sh — B252 DERP cert auto-renewal
# ============================================================================
# 2026-09-15: v1.5.6+ (B252) — bundled derper TLS cert is a static
# file on disk; pre-B252 the renewal flow was a manual SSH ritual
# (copy cert from NPM API + systemctl reload derper). B252 embeds
# the renewal flow as a skygate-internal feature with three modes
# (npm / letsencrypt / manual) and a daily cron.
#
# This B-check pins the source shape + driver registration so a
# future refactor can't silently break the auto-renewal.
#
# Pass criteria — every contract must hold:
#   A. migrations_v0_71_derp_cert_sync.go exists with required
#      columns (hostname, mode, npm_base_url, npm_cert_id,
#      cert_dir, derper_pid_file, derper_systemd_unit,
#      check_interval_min, enabled, last_checked_at,
#      last_synced_at, last_cert_sha256, last_error,
#      expiry_warn_at, notes).
#   B. migration is idempotent (CREATE TABLE IF NOT EXISTS +
#      CREATE INDEX IF NOT EXISTS).
#   C. v0.71 is registered in driver_postgres.go with the
#      B252 label + migrations_v0_71_derp_cert_sync.go filename.
#   D. derp_cert_sync.go exports StartCertSyncCron +
#      DerpCertSyncInterval + DerpCertSyncConfig + SyncOne +
#      npmLogin + npmDownloadCert + reloadDerper + expiryFromCert
#      so the cron + the manual /admin/derp/cert-sync/run
#      handler can both reach the sync logic.
#   E. main.go calls admin.StartCertSyncCron at startup
#      (alongside derphealth.StartCron).
#   F. POST /admin/derp/cert-sync/run route is wired to
#      adminSvc.PostAdminDerpCertSyncRun.
#   G. /admin/derp template includes the "Cert auto-renewal"
#      section with a Sync now button + the i18n hint that
#      mentions both /etc/hosts (systemd) and extra_hosts
#      (docker-compose) paths.
#   H. i18n keys present in both ruDerp + enDerp maps for the
#      Cert auto-renewal block (cert_sync_title through
#      cert_sync_hosts_hint).
#   I. AGENTS.md mentions B252 in the B-# catalog.
# ============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
log()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

# --- A. migration source shape ---
echo "=== A. migration source columns ==="
MIG="internal/db/migrations_v0_71_derp_cert_sync.go"
if [ ! -f "$MIG" ]; then bad "missing $MIG"; else
  # Token-level checks (column name + key constraint fragment).
  # The migration body is inside a Go raw-string literal with
  # embedded tabs that grep can't reproduce byte-for-byte, so we
  # anchor on the column name + a near-by constraint fragment.
  # A future rename / type change / default drop will break at
  # least one of the pairs.
  for pair in 'hostname:UNIQUE' \
               'mode:letsencrypt' \
               'npm_base_url:DEFAULT' \
               'npm_cert_id:INTEGER' \
               'cert_dir:derper/certs' \
               'derper_pid_file:derper.pid' \
               'derper_systemd_unit:derper.service' \
               'check_interval_min:1440' \
               'enabled:DEFAULT 1' \
               'last_checked_at:BIGINT' \
               'last_synced_at:BIGINT' \
               'last_cert_sha256:TEXT' \
               'last_error:TEXT' \
               'expiry_warn_at:BIGINT' \
               'notes:TEXT'; do
    col="${pair%%:*}"
    cn="${pair#*:}"
    # Find the column name in the CREATE TABLE block only (not
    # in doc comments). The CREATE TABLE block is the substring
    # between "CREATE TABLE IF NOT EXISTS derp_cert_sync (" and
    # the next 4000 chars (covers even a padded source).
    block="$(awk '/CREATE TABLE IF NOT EXISTS derp_cert_sync \(/{flag=1} flag{print; if (/^\t*\)`,\?$/||/^\),?$/){flag=0}}' "$MIG")"
    # 2026-09-24 (B317 verification) — TRUNCATE IN THE SHELL, NEVER WITH `head`.
    # This used to be `... | head -c 4000`, i.e. a producer feeding a reader that
    # exits at its limit: awk then dies with SIGPIPE (141), `pipefail` (set at the
    # top of this file) turns the pipeline into a failure, and because this is a
    # COMMAND SUBSTITUTION in an assignment, `set -e` aborts the whole script —
    # section A stopped mid-loop and the gate reported a column that is present as
    # missing. It is the same class as AGENTS trap #9, and it is why a green check
    # can fail on a loaded machine (this file passed 45/0 standalone and reported a
    # false ✗ in a full gate run).
    block="${block:0:4000}"
    if echo "$block" | grep -qE "^[[:space:]]+${col}[[:space:]]" && \
       echo "$block" | grep -qE "^[[:space:]]+${col}[[:space:]].*${cn}"; then
      ok "column present: $col  (constraint fragment: $cn)"
    else
      bad "missing column $col or constraint $cn in CREATE TABLE block"
    fi
  done
fi

# --- B. idempotency ---
echo
echo "=== B. migration idempotency ==="
if grep -qF 'CREATE TABLE IF NOT EXISTS derp_cert_sync' "$MIG" 2>/dev/null; then
  ok "CREATE TABLE IF NOT EXISTS"
else bad "CREATE TABLE must use IF NOT EXISTS"; fi
if grep -qF 'CREATE INDEX IF NOT EXISTS' "$MIG" 2>/dev/null; then
  ok "indexes use CREATE INDEX IF NOT EXISTS"
else bad "indexes must use CREATE INDEX IF NOT EXISTS"; fi

# --- C. driver registration ---
echo
echo "=== C. driver_postgres.go registration ==="
DRV="internal/db/driver_postgres.go"
if grep -qF 'migrateV071PG' "$DRV" 2>/dev/null; then
  ok "migrateV071PG referenced in $DRV"
else bad "migrateV071PG must be registered in $DRV"; fi
if grep -qE 'v0\.71.*B252.*derp_cert_sync' "$DRV" 2>/dev/null; then
  ok "v0.71 label mentions B252 + derp_cert_sync"
else bad "v0.71 entry must say B252 + derp_cert_sync"; fi

# --- D. exports from derp_cert_sync.go ---
echo
echo "=== D. derp_cert_sync.go exports ==="
PKG="internal/feature/admin/derp_cert_sync.go"
for sym in StartCertSyncCron DerpCertSyncInterval DerpCertSyncConfig SyncOne \
          'func npmLogin' 'func npmDownloadCert' 'func reloadDerper' 'func expiryFromCert'; do
  if grep -qF "$sym" "$PKG" 2>/dev/null; then ok "export present: $sym"
  else bad "missing export: $sym in $PKG"; fi
done

# --- E. main.go wires the cron ---
echo
echo "=== E. main.go cron wiring ==="
MAIN="cmd/skygate/main.go"
# main.go imports the admin package as 'adminsvc' (alias) so
# the call site is adminsvc.StartCertSyncCron(...). We accept
# either bare 'admin.' or 'adminsvc.' in source — both compile
# to the same package.
if grep -qE '(admin|adminsvc)\.StartCertSyncCron' "$MAIN" 2>/dev/null; then
  ok "admin/adminsvc.StartCertSyncCron called in main.go"
else bad "main.go must call admin.StartCertSyncCron at startup"; fi

# --- F. POST route registration ---
echo
echo "=== F. /admin/derp/cert-sync/run route ==="
if grep -qF 'POST /admin/derp/cert-sync/run' "$MAIN" 2>/dev/null; then
  ok "POST /admin/derp/cert-sync/run registered in main.go"
else bad "main.go must register POST /admin/derp/cert-sync/run"; fi
if grep -qF 'PostAdminDerpCertSyncRun' "$MAIN" 2>/dev/null; then
  ok "PostAdminDerpCertSyncRun handler wired"
else bad "PostAdminDerpCertSyncRun must be wired in main.go"; fi

# --- G. /admin/derp template section ---
echo
echo "=== G. /admin/derp template UI ==="
TPL="internal/handlers/templates/admin/derp.html"
if grep -qF 'cert_sync_title' "$TPL" 2>/dev/null; then
  ok "cert_sync_title block present in $TPL"
else bad "$TPL must render Cert auto-renewal section"; fi
if grep -qF '/admin/derp/cert-sync/run' "$TPL" 2>/dev/null; then
  ok "Sync now form posts to /admin/derp/cert-sync/run"
else bad "Sync now form action missing"; fi

# --- H. i18n keys ---
echo
echo "=== H. i18n keys (RU + EN) ==="
RU="internal/i18n/catalog_derp.go"
for k in cert_sync_title cert_sync_hostname cert_sync_mode \
         cert_sync_last_synced cert_sync_expiry cert_sync_status \
         cert_sync_ok cert_sync_err cert_sync_pending \
         cert_sync_run_now cert_sync_run_help cert_sync_hosts_hint; do
  if grep -qF "\"derp.$k\"" "$RU" 2>/dev/null; then
    # verify the key appears in BOTH ruDerp and enDerp blocks
    # (the file has 2 maps). Approximate: count >= 2.
    cnt=$(grep -cF "\"derp.$k\"" "$RU" 2>/dev/null || echo 0)
    if [ "$cnt" -ge 2 ]; then ok "i18n key present (RU+EN): derp.$k"
    else bad "i18n key $k only in $cnt/2 maps (need RU+EN)"; fi
  else bad "i18n key missing: derp.$k"; fi
done

# --- I. AGENTS.md B252 entry ---
echo
echo "=== I. AGENTS.md B252 catalog entry ==="
AGENTS="AGENTS.md"
if grep -qF 'B252' "$AGENTS" 2>/dev/null && grep -qF 'derp_cert_sync' "$AGENTS" 2>/dev/null; then
  ok "AGENTS.md mentions both B252 and derp_cert_sync"
else bad "AGENTS.md must mention B252 + derp_cert_sync in the B-# catalog"; fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]