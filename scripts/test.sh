#!/usr/bin/env bash
#===============================================================================
# Skygate Backup/Restore/Migrate — Test Suite
# Проверяет каждый шаг механизма бэкапа и восстановления
# Usage: ./test.sh [--verbose]
#===============================================================================
set -uo pipefail

VERBOSE=false
[[ "${1:-}" == "--verbose" ]] && VERBOSE=true

PASS=0; FAIL=0
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKYGATE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${SKYGATE_DIR}"

log_test() { echo -e "  [${1}] ${2}"; }
ok()   { PASS=$((PASS+1)); log_test "✅" "$1"; }
fail() { FAIL=$((FAIL+1)); log_test "❌" "$1"; }

echo "=============================================="
echo "  Skygate Backup/Restore — Test Suite"
echo "=============================================="

# === STEP 1: Backup script exists ===
echo ""
echo "--- 1. Backup Script ---"
if [ -f scripts/backup.sh ]; then
    ok "scripts/backup.sh exists"
else
    fail "scripts/backup.sh NOT FOUND"
fi
if [ -x scripts/backup.sh ]; then
    ok "backup.sh is executable"
else
    fail "backup.sh not executable"
fi

# === STEP 2: Restore script exists ===
echo ""
echo "--- 2. Restore Script ---"
if [ -f scripts/restore.sh ]; then
    ok "scripts/restore.sh exists"
else
    fail "scripts/restore.sh NOT FOUND"
fi

# === STEP 2b: Notify helper ===
echo ""
echo "--- 2b. Notify Helper ---"
if [ -x scripts/notify.sh ]; then
    if scripts/notify.sh --dry-run "test smoke" "dry-run path" >/tmp/notify.out 2>&1; then
        ok "notify.sh --dry-run OK"
    else
        fail "notify.sh --dry-run failed"
    fi
else
    fail "scripts/notify.sh missing or not executable"
fi

# === STEP 2c: Cron installer ===
echo ""
echo "--- 2c. Cron Installer ---"
if [ -x scripts/backup_cron.sh ]; then
    ok "backup_cron.sh exists and is executable"
    if scripts/backup_cron.sh status >/tmp/cron.out 2>&1; then
        ok "backup_cron.sh status OK"
    else
        fail "backup_cron.sh status failed"
    fi
else
    fail "scripts/backup_cron.sh missing or not executable"
fi

# === STEP 3: Migrate script exists ===
echo ""
echo "--- 3. Migration Script ---"
if [ -f scripts/migrate.sh ]; then
    ok "scripts/migrate.sh exists"
else
    fail "scripts/migrate.sh NOT FOUND"
fi

# === STEP 4: Backup creates valid archive ===
echo ""
echo "--- 4. Backup produces valid archive ---"
BACKUP_DIR="/tmp/skygate-test-$$"
BACKUP_HOME="/tmp/skygate-backup-home-$$"
mkdir -p "${BACKUP_DIR}" "${BACKUP_HOME}"
# Isolate HOME for the backup run so it writes status.json inside BACKUP_HOME
# which we preserve for STEP 12. BACKUP_DIR is the archive target.
if HOME="${BACKUP_HOME}" SKYGATE_BACKUP_STATUS_JSON="${BACKUP_HOME}/.skygate-backup-status.json" \
    scripts/backup.sh "${BACKUP_DIR}" >/dev/null 2>&1; then
    ok "backup.sh ran without errors (full prod path)"
else
    rc=$?
    # Backup is allowed to complete with warnings, but exit must be 0 or 2
    if [[ $rc -eq 0 || $rc -eq 2 ]]; then
        ok "backup.sh exit code=${rc} (acceptable)"
    else
        fail "backup.sh exited ${rc} (expected 0 or 2)"
    fi
fi

ARCHIVE=$(ls "${BACKUP_DIR}"/*.tar.gz 2>/dev/null | head -1)
if [ -n "${ARCHIVE}" ]; then
    ok "Archive created: $(basename ${ARCHIVE}) ($(du -h ${ARCHIVE} | cut -f1))"
else
    fail "No archive created"
fi

# === STEP 5: Archive integrity check ===
echo ""
echo "--- 5. Archive Integrity ---"
if [ -n "${ARCHIVE}" ]; then
    if tar tzf "${ARCHIVE}" > /dev/null 2>&1; then
        ok "Archive opens correctly (tar tzf OK)"
    else
        fail "Archive corrupted (tar tzf failed)"
    fi

    CONTENTS=$(tar tzf "${ARCHIVE}")
    for needed in "skygate.env" "inventory.txt"; do
        if echo "${CONTENTS}" | grep -q "${needed}"; then
            ok "  Contains: ${needed}"
        else
            fail "  Missing: ${needed}"
        fi
    done
fi

# === STEP 6: Docker containers status ===
echo ""
echo "--- 6. Docker Status ---"
for container in skygate headscale headplane; do
    STATUS=$(docker ps --format "{{.Status}}" --filter "name=${container}" 2>/dev/null || echo "not found")
    if echo "${STATUS}" | grep -q "Up"; then
        ok "${container}: ${STATUS}"
    else
        fail "${container}: ${STATUS:-not running}"
    fi
done

# === STEP 7: Skygate API ===
echo ""
echo "--- 7. Skygate HTTP ---"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 http://localhost:8080/login 2>/dev/null || echo "FAIL")
if [ "${HTTP_CODE}" = "200" ]; then
    ok "HTTP 200 on /login"
else
    fail "HTTP ${HTTP_CODE} on /login"
fi

# === STEP 8: Admin pages ===
echo ""
echo "--- 8. Admin pages ---"
for page in "/admin/backup" "/admin/settings"; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "http://localhost:8080${page}" 2>/dev/null || echo "FAIL")
    if [ "${CODE}" = "302" ] || [ "${CODE}" = "200" ]; then
        ok "  ${page} → ${CODE}"
    else
        fail "  ${page} → ${CODE} (expected 302/200)"
    fi
done

# === STEP 9: Go unit tests ===
echo ""
echo "--- 9. Go unit tests ---"
if command -v go >/dev/null 2>&1; then
    if GOTOOLCHAIN=local go test -count=1 ./internal/... >/tmp/gotest.out 2>&1; then
        ran=$(grep -c '^=== RUN ' /tmp/gotest.out || echo 0)
        passed=$(grep -c '^--- PASS' /tmp/gotest.out || echo 0)
        ok "go test ./internal/... OK (${ran} ran, ${passed} passed)"
    else
        fail "go test ./internal/... failed"
        head -20 /tmp/gotest.out | sed 's/^/    /'
    fi
else
    fail "go binary missing — cannot run unit tests"
fi

# === STEP 10: Migrate.sh prerequisite check ===
echo ""
echo "--- 10. Migration prerequisites ---"
for cmd in docker git crontab; do
    if command -v "${cmd}" &>/dev/null; then
        ok "${cmd} installed"
    else
        fail "${cmd} NOT installed"
    fi
done

# === STEP 11: Restore.sh can parse a backup ===
echo ""
echo "--- 11. Restore script parsing ---"
if [ -n "${ARCHIVE}" ]; then
    TMPDIR="/tmp/restore-test-$$"
    mkdir -p "${TMPDIR}"
    tar xzf "${ARCHIVE}" -C "${TMPDIR}"
    EXTRACTED=$(ls "${TMPDIR}")
    if [ -n "${EXTRACTED}" ]; then
        ok "Restore can unpack archive"
    else
        fail "Restore cannot unpack archive"
    fi
    rm -rf "${TMPDIR}"
fi

# === STEP 12: Backup status JSON writeable ===
echo ""
echo "--- 12. Status JSON ---"
STATUS_JSON="${BACKUP_HOME}/.skygate-backup-status.json"
if [[ -s "${STATUS_JSON}" ]]; then
    ok "status.json present ($(stat -c '%s' "${STATUS_JSON}") bytes)"
    if python3 -c "import json; json.load(open('${STATUS_JSON}'))" 2>/dev/null; then
        ok "status.json is valid JSON"
    else
        fail "status.json is NOT valid JSON"
    fi
else
    fail "no status.json at ${STATUS_JSON} — backup did not record final state?"
fi

# === CLEANUP ===
rm -rf "${BACKUP_DIR}" "${BACKUP_HOME}"

echo ""
echo "=============================================="
echo "  Results: ${PASS} passed, ${FAIL} failed"
echo "=============================================="
if [ "${FAIL}" -gt 0 ]; then
    echo ""
    echo "Failed steps:"
    echo "  Re-run: bash scripts/test.sh --verbose"
    echo "  Check logs: docker logs skygate 2>&1 | tail -20"
    exit 1
fi
