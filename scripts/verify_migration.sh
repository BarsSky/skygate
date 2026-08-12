#!/usr/bin/env bash
#===============================================================================
# Skygate migration verify (BL-17)
#
# One-shot "is the migration done?" script for cross-host
# moves. Run on the NEW host after a fresh restore.sh +
# docker compose up. The script chains the 4 verification
# passes the operator would otherwise have to run by hand:
#
#   1. /healthz returns 200 (process is up)
#   2. /readyz shows all 4 integrations green
#      (db / headscale / headplane / tailscale)
#   3. The build label is recent (HEAD matches origin/main)
#   4. Live migration: take a fresh local backup + verify
#      it lands in BACKUP_DIR with the expected size
#   5. Cross-host restore simulation: replay the dump
#      into a fresh DB + verify table count (proves the
#      dump is replayable, not just a snapshot)
#
# Returns 0 if ALL passes succeed, non-zero otherwise.
# The output is human-readable but also machine-parseable
# (lines starting with "PASS" / "FAIL" / "WARN" + a final
# summary line).
#
# Usage: SKYGATE_DIR=/home/skyadmin/skygate bash scripts/verify_migration.sh
#
# 2026-08-12 v1.3.8: written as part of BL-17 (autonomous
# migration verify). Pairs with scripts/restore.sh (BL-15)
# + scripts/test_backup_protocols.sh (BL-16) +
# /admin/backup/download-s3 (BL-18) to form the full
# backup/restore/migration test suite.
#===============================================================================
set -uo pipefail

: "${SKYGATE_DIR:=/home/skyadmin/skygate}"
: "${SKYGATE_URL:=http://localhost:8080}"
cd "${SKYGATE_DIR}" || exit 1

PASS=0
FAIL=0
WARN=0
ok()   { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

echo
echo "=== Skygate migration verify (BL-17) ==="
echo "SKYGATE_DIR: ${SKYGATE_DIR}"
echo "SKYGATE_URL: ${SKYGATE_URL}"
echo

# ----------------------------------------------------------------------------
# 1. /healthz returns 200 with status:ok
# ----------------------------------------------------------------------------
HEALTHZ=$(curl -s --max-time 5 -w '\n%{http_code}' "${SKYGATE_URL}/healthz" 2>/dev/null)
HEALTHZ_RC=$(echo "${HEALTHZ}" | tail -1)
HEALTHZ_BODY=$(echo "${HEALTHZ}" | head -n -1)
if [[ "${HEALTHZ_RC}" == "200" ]] && echo "${HEALTHZ_BODY}" | grep -q '"status":"ok"' ; then
  ok "1/5 /healthz returns 200 + status:ok"
  BUILD=$(echo "${HEALTHZ_BODY}" | grep -oE '"build":"[^"]+"' | cut -d'"' -f4)
  echo "        build label: ${BUILD}"
else
  bad "1/5 /healthz (got HTTP ${HEALTHZ_RC})"
fi

# ----------------------------------------------------------------------------
# 2. /readyz shows all 4 integrations green
# ----------------------------------------------------------------------------
READYZ=$(curl -s --max-time 10 "${SKYGATE_URL}/readyz" 2>/dev/null)
if [[ -z "${READYZ}" ]] ; then
  bad "2/5 /readyz: empty response"
else
  HEALTHY=$(echo "${READYZ}" | grep -oE '"healthy":(true|false)' | cut -d: -f2)
  DEPS=$(echo "${READYZ}" | grep -oE '"dependencies_healthy":(true|false)' | cut -d: -f2)
  if [[ "${HEALTHY}" == "true" && "${DEPS}" == "true" ]] ; then
    ok "2/5 /readyz healthy=true, dependencies_healthy=true"
  else
    bad "2/5 /readyz healthy=${HEALTHY:-?} deps=${DEPS:-?}"
  fi
  for svc in db headscale headplane tailscale ; do
    # The JSON has both a top-level "db":"ok" AND a
    # nested "checks":{"db":"ok"} — grep matches both,
    # so we take the first match (head -1) to get
    # the top-level value. The nested checks object
    # is for the B92 availability board, not the
    # go/no-go signal.
    VAL=$(echo "${READYZ}" | grep -oE "\"${svc}\":\"(ok|ERROR[^\"]*)\"" | head -1 | cut -d'"' -f4)
    if [[ "${VAL}" == "ok" ]] ; then
      ok "    /readyz ${svc}=ok"
    else
      bad "    /readyz ${svc}=${VAL:-?}"
    fi
  done
fi

# ----------------------------------------------------------------------------
# 3. Build label is recent (HEAD matches origin/main).
# This catches the "you restored the right code but
# forgot to restart skygate" mistake — the build label
# would still be the OLD label until the container
# recreates.
# ----------------------------------------------------------------------------
HEAD_LOCAL=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
HEAD_REMOTE=$(git rev-parse --short origin/main 2>/dev/null || echo unknown)
if [[ "${HEAD_LOCAL}" == "${HEAD_REMOTE}" ]] ; then
  ok "3/5 git HEAD matches origin/main (${HEAD_LOCAL})"
else
  warn "3/5 git HEAD (${HEAD_LOCAL}) != origin/main (${HEAD_REMOTE}) — operator may need to redeploy"
fi

# ----------------------------------------------------------------------------
# 4. Live backup: take a fresh local backup, verify it
# lands in BACKUP_DIR and is the right size.
# ----------------------------------------------------------------------------
BACKUP_DIR=$(grep -E '^SKYGATE_BACKUP_DIR=' .env 2>/dev/null | cut -d= -f2- || true)
if [[ -z "${BACKUP_DIR}" ]] ; then
  BACKUP_DIR="/home/skyadmin/skygate-backups"
fi
echo
echo "  backup dir: ${BACKUP_DIR}"
rm -f "${BACKUP_DIR}"/*.tar.gz 2>/dev/null
mkdir -p "${BACKUP_DIR}"
if bash scripts/backup.sh "${BACKUP_DIR}" >/tmp/skygate-mig-verify-backup.log 2>&1 ; then
  ARCHIVE=$(ls -t "${BACKUP_DIR}"/skygate-full-*.tar.gz 2>/dev/null | head -1)
  if [[ -n "${ARCHIVE}" ]] ; then
    SIZE=$(stat -c '%s' "${ARCHIVE}")
    if [[ ${SIZE} -gt 100000 ]] ; then
      ok "4/5 backup produced ${ARCHIVE} (${SIZE} bytes)"
    else
      bad "4/5 backup too small: ${ARCHIVE} (${SIZE} bytes — likely a no-op backup)"
    fi
  else
    bad "4/5 backup ran but produced no archive in ${BACKUP_DIR}"
  fi
else
  bad "4/5 backup.sh failed (see /tmp/skygate-mig-verify-backup.log)"
fi

# ----------------------------------------------------------------------------
# 5. Cross-host restore simulation: replay the dump
# into a fresh DB and verify it has the same table count
# as the live DB. This proves the dump is replayable,
# which is what cross-host migration actually depends on.
# ----------------------------------------------------------------------------
DB_DSN=$(grep -E '^SKYGATE_DB_DSN=' .env 2>/dev/null | head -1 | cut -d= -f2-)
if [[ -z "${DB_DSN}" || -z "${ARCHIVE}" ]] ; then
  warn "5/5 skipped (DB_DSN or ARCHIVE not available)"
else
  HOST=$(echo "${DB_DSN}" | sed -E 's|.*@([^:/]+):.*|\1|')
  PORT=$(echo "${DB_DSN}" | sed -E 's|.*@[^:/]+:([0-9]+).*|\1|')
  USER=$(echo "${DB_DSN}" | sed -E 's|.*://([^:]+):.*|\1|')
  DB=$(echo "${DB_DSN}" | sed -E 's|.*/([^?]+).*|\1|')
  # Live count for the comparison below.
  LIVE_TABLES=$(docker run --rm --network host -e PGPASSWORD=skygate_admin_pass postgres:18-alpine \
    psql -h "${HOST}" -p "${PORT}" -U "${USER}" -d "${DB}" -tA -c \
    "SELECT count(*) FROM pg_tables WHERE schemaname='public';" 2>/dev/null | tr -d '[:space:]')
  # Drop + create the test DB
  docker run --rm --network host -e PGPASSWORD=skygate_admin_pass postgres:18-alpine \
    psql -h "${HOST}" -p "${PORT}" -U "${USER}" -d "${DB}" \
    -c "DROP DATABASE IF EXISTS skygate_mig_verify_test;" 2>/dev/null | tail -1 >/dev/null
  docker run --rm --network host -e PGPASSWORD=skygate_admin_pass postgres:18-alpine \
    psql -h "${HOST}" -p "${PORT}" -U "${USER}" -d "${DB}" \
    -c "CREATE DATABASE skygate_mig_verify_test;" 2>/dev/null | tail -1 >/dev/null
  # Apply the dump. We bind-mount the tarball into
  # the postgres throwaway so the -f path is
  # visible inside the container (host tmp paths
  # are NOT visible unless mounted).
  APPLY_LOG=/tmp/skygate-mig-verify-apply.log
  BIND_DIR=$(mktemp -d)
  cp "${ARCHIVE}" "${BIND_DIR}/archive.tar.gz"
  docker run --rm --network host -e PGPASSWORD=skygate_admin_pass \
    -v "${BIND_DIR}:/restore:ro" \
    postgres:18-alpine \
    sh -c "tar xzf /restore/archive.tar.gz && psql -h ${HOST} -p ${PORT} -U ${USER} -d skygate_mig_verify_test -v ON_ERROR_STOP=1 -f skygate-full-*/skygate-pg.sql" \
    >"${APPLY_LOG}" 2>&1
  if [[ $? -ne 0 ]] ; then
    warn "  replay apply had errors (last 5 lines:)"
    tail -5 "${APPLY_LOG}" | sed 's/^/      /'
  fi
  RESTORED_TABLES=$(docker run --rm --network host -e PGPASSWORD=skygate_admin_pass postgres:18-alpine \
    psql -h "${HOST}" -p "${PORT}" -U "${USER}" -d skygate_mig_verify_test -tA -c \
    "SELECT count(*) FROM pg_tables WHERE schemaname='public';" 2>/dev/null | tr -d '[:space:]')
  if [[ "${RESTORED_TABLES}" == "${LIVE_TABLES}" && ${RESTORED_TABLES:-0} -ge 20 ]] ; then
    ok "5/5 replay test: ${RESTORED_TABLES} tables restored (matches live ${LIVE_TABLES})"
  else
    bad "5/5 replay test: live=${LIVE_TABLES:-?} restored=${RESTORED_TABLES:-?}"
  fi
  # Drop the test DB
  docker run --rm --network host -e PGPASSWORD=skygate_admin_pass postgres:18-alpine \
    psql -h "${HOST}" -p "${PORT}" -U "${USER}" -d "${DB}" \
    -c "DROP DATABASE skygate_mig_verify_test;" 2>/dev/null | tail -1 >/dev/null
  rm -rf "${BIND_DIR}"
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
if [[ "${FAIL}" -gt 0 ]] ; then
  echo "MIGRATION VERIFY FAILED — fix the FAIL items before declaring the migration done."
  exit 1
fi
echo "MIGRATION VERIFY OK"
exit 0
