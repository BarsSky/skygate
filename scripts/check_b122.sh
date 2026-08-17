#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.2 follow-up (B122) — restore.sh PG restore (BL-15 e2e)
#
# Pins the v1.3.19.2 follow-up that makes `scripts/restore.sh`
# end-to-end functional for the v1.3.0+ PG-era archives.
# Specifically:
#   1. The `do_pg_restore()` function (added v1.3.8 / B101) is
#      wired to read SKYGATE_DB_DSN from the archive's
#      skygate.env, replay skygate-pg.sql via throwaway
#      postgres:18-alpine on the docker bridge, and report
#      success/failure per the script's log conventions.
#   2. The `do_skygate_db()` dispatcher picks the PG path when
#      skygate-pg.sql is present (v1.3.0+ archives) and the
#      legacy SQLite path when skygate.db is present (v0.32.x
#      archives). This is the bugfix the original BL-15 ticket
#      was opened for ("the in-app Restore button does nothing
#      for the DB step on PG-era archives" — the pre-v1.3.0
#      restore.sh copied skygate.db from the named volume,
#      which doesn't exist on PG-only deploys).
#   3. `do_headscale_config` / `do_headscale_db` /
#      `do_headplane` use `sudo` so the in-app restore (which
#      runs from the skygate container as root) and the
#      interactive restore (which runs as skyadmin via the
#      operator's shell) both work. Without sudo, the
#      interactive path gets "Permission denied" on the
#      root-owned /home/admin/headscale/config dir.
#   4. `do_headscale_db` uses a shell-glob loop instead of
#      `ls ... | head` — the latter trips `set -euo pipefail`
#      when no headscale*.db file is in the archive (a valid
#      scenario for v1.3.8+ archives).
#   5. The in-app `PostAdminBackupRestore` handler feeds "8"
#      on stdin (the "ALL" path) and lets restore.sh walk
#      through all 7 sub-restore steps.
#
# What this script verifies (live, on the VM):
#   A. scripts/restore.sh contains do_pg_restore with
#      postgres:18-alpine + psql -f /restore/skygate-pg.sql
#   B. The dispatcher do_skygate_db checks for
#      skygate-pg.sql (the v1.3.0+ format)
#   C. do_pg_restore parses SKYGATE_DB_DSN from
#      skygate.env (not from the host environment)
#   D. do_headscale_config uses `sudo` (for the in-app +
#      interactive cross-user case)
#   E. do_headscale_db uses shell-glob (not `ls ... | head`)
#      so the no-file case is handled by the if check
#   F. Live DB: a v1.3.0+ backup archive exists with
#      skygate-pg.sql + skygate.env (skygate_full_<TS>*.tar.gz
#      in skygate-backups/ with the right files inside)
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

# Allow override so this script works from /tmp on the VM
: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

RESTORE_SH="scripts/restore.sh"
[ -f "${RESTORE_SH}" ] || { bad "source file not found: ${RESTORE_SH}"; exit 1; }

# ------------------------------------------------------------------------------
# Contract A: restore.sh has do_pg_restore (postgres:18-alpine + psql)
# ------------------------------------------------------------------------------
echo
echo "=== A. restore.sh: do_pg_restore uses postgres:18-alpine + psql -f ==="
if grep -q 'do_pg_restore' "${RESTORE_SH}"; then
    ok "do_pg_restore() function exists in restore.sh"
else
    bad "do_pg_restore() function missing — PG archives would not be replayed"
fi
if grep -q 'postgres:18-alpine' "${RESTORE_SH}"; then
    ok "restore.sh uses postgres:18-alpine throwaway (no host psql dependency)"
else
    bad "restore.sh does NOT use postgres:18-alpine (operator would need postgresql-client on host)"
fi
if tr '\n' ' ' < "${RESTORE_SH}" | grep -qE 'psql[^|]*-f[[:space:]]+/restore/skygate-pg\.sql'; then
    ok "restore.sh runs psql -f /restore/skygate-pg.sql (replays the text dump)"
else
    bad "restore.sh missing 'psql -f /restore/skygate-pg.sql' — PG dump is never replayed"
fi
if grep -q 'ON_ERROR_STOP=1' "${RESTORE_SH}"; then
    ok "restore.sh uses psql ON_ERROR_STOP=1 (abort on first error)"
else
    warn "restore.sh psql is missing ON_ERROR_STOP=1 — silent failures on partial restores"
fi

# ------------------------------------------------------------------------------
# Contract B: dispatcher picks skygate-pg.sql path for v1.3.0+ archives
# ------------------------------------------------------------------------------
echo
echo "=== B. restore.sh: dispatcher picks skygate-pg.sql ==="
# The dispatcher in do_skygate_db should check for
# skygate-pg.sql first and call do_pg_restore.
if grep -A3 'do_skygate_db' "${RESTORE_SH}" | grep -q 'skygate-pg.sql' &&
   grep -A3 'do_skygate_db' "${RESTORE_SH}" | grep -q 'do_pg_restore'; then
    ok "do_skygate_db checks skygate-pg.sql and calls do_pg_restore"
else
    bad "do_skygate_db dispatcher doesn't wire skygate-pg.sql to do_pg_restore"
fi

# ------------------------------------------------------------------------------
# Contract C: DSN parsed from skygate.env (not from host env)
# ------------------------------------------------------------------------------
echo
echo "=== C. restore.sh: DSN parsed from skygate.env in archive ==="
# load_dsn_from_env() must read from skygate.env (in the
# extracted archive dir), not from $SKYGATE_DB_DSN in the
# host environment. The latter would silently target the
# wrong DB on a cross-host migration.
if grep -q "load_dsn_from_env" "${RESTORE_SH}" &&
   grep -q "SKYGATE_DB_DSN" "${RESTORE_SH}" &&
   grep -B1 -A10 "do_pg_restore()" "${RESTORE_SH}" | grep -q "skygate.env"; then
    ok "do_pg_restore calls load_dsn_from_env() against skygate.env (not host env)"
else
    bad "do_pg_restore does not properly read SKYGATE_DB_DSN from skygate.env"
fi

# ------------------------------------------------------------------------------
# Contract D: do_headscale_config uses sudo
# ------------------------------------------------------------------------------
echo
echo "=== D. restore.sh: do_headscale_config uses sudo (root-owned dir) ==="
# The /home/admin/headscale/config dir is owned by root
# (deployed by deploy.sh as root). The interactive restore
# path runs as skyadmin (no write access). Sudo bridges the
# two — it's a no-op when skygate container runs as root.
if awk '/^do_headscale_config\(\)/,/^}/' "${RESTORE_SH}" | grep -q 'sudo cp' &&
   awk '/^do_headscale_config\(\)/,/^}/' "${RESTORE_SH}" | grep -q 'sudo mkdir'; then
    ok "do_headscale_config uses sudo cp + sudo mkdir (works for both interactive + in-app)"
else
    bad "do_headscale_config missing sudo prefix — interactive restore would fail with Permission denied"
fi

# ------------------------------------------------------------------------------
# Contract E: do_headscale_db uses shell-glob (not ls | head)
# ------------------------------------------------------------------------------
echo
echo "=== E. restore.sh: do_headscale_db uses shell-glob (no set -e trip) ==="
# Pre-fix used `DB_FILE=$(ls headscale*.db | head -1)` which
# trips set -euo pipefail when no headscale*.db is in the
# archive (a valid scenario for v1.3.8+ archives). The fix
# uses a for-loop with glob matching + [ -f ] check.
if awk '/^do_headscale_db/,/^}/' "${RESTORE_SH}" | grep -q 'for f in headscale\*.db'; then
    ok "do_headscale_db uses for-loop with shell-glob (handles missing-file case)"
else
    bad "do_headscale_db uses ls | head (trips set -e when archive has no headscale*.db)"
fi

# ------------------------------------------------------------------------------
# Contract F: live DB has a v1.3.0+ backup archive
# ------------------------------------------------------------------------------
echo
echo "=== F. live: a v1.3.0+ archive exists in skygate-backups ==="
if [ -d /home/skyadmin/skygate-backups ]; then
    # Find any archive with skygate-pg.sql inside
    archive=""
    for f in /home/skyadmin/skygate-backups/skygate-full-*.tar.gz; do
        if [ -f "${f}" ]; then
            tar tzf "${f}" 2>/dev/null | grep -q '^[^/]*/skygate-pg\.sql$' && archive="${f}" && break
        fi
    done
    if [ -n "${archive}" ]; then
        ok "found v1.3.0+ archive: ${archive##*/}"
        # Extract inventory to verify skygate.env also present
        tmp_dir=$(mktemp -d)
        cd "${tmp_dir}"
        tar xzf "${archive}" --wildcards '*/inventory.txt' '*/skygate.env' 2>/dev/null
        inv=$(find . -name 'inventory.txt' | head -1)
        envf=$(find . -name 'skygate.env' | head -1)
        if [ -n "${inv}" ] && grep -q 'skygate-pg.sql' "${inv}" 2>/dev/null; then
            ok "inventory.txt references skygate-pg.sql (v1.3.0+ format)"
        else
            bad "inventory.txt missing skygate-pg.sql reference"
        fi
        if [ -n "${envf}" ] && grep -q 'SKYGATE_DB_DSN' "${envf}" 2>/dev/null; then
            ok "skygate.env has SKYGATE_DB_DSN (do_pg_restore can parse it)"
        else
            bad "skygate.env missing SKYGATE_DB_DSN (do_pg_restore would fail)"
        fi
        cd /tmp; rm -rf "${tmp_dir}"
    else
        warn "no v1.3.0+ archive found in /home/skyadmin/skygate-backups (operator should run a new backup first)"
    fi
else
    warn "/home/skyadmin/skygate-backups/ does not exist — live contract F cannot be verified"
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
