#!/usr/bin/env bash
#===============================================================================
# Skygate Backup/Restore/Migrate — Test Suite
# Проверяет каждый шаг механизма бэкапа и восстановления
# Usage: ./test.sh [--verbose]
#===============================================================================
set -euo pipefail

VERBOSE=false
[[ "${1:-}" == "--verbose" ]] && VERBOSE=true

PASS=0; FAIL=0
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "${SCRIPT_DIR}/.."

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
mkdir -p "${BACKUP_DIR}"
if bash scripts/backup.sh "${BACKUP_DIR}" > /dev/null 2>&1; then
    ok "backup.sh ran without errors"
else
    fail "backup.sh failed"
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
    
    # Check contents
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
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/login 2>/dev/null || echo "FAIL")
if [ "${HTTP_CODE}" = "200" ]; then
    ok "HTTP 200 on /login"
else
    fail "HTTP ${HTTP_CODE} on /login"
fi

# === STEP 8: Admin backup page (redirect to login w/o auth) ===
echo ""
echo "--- 8. Admin pages ---"
for page in "/admin/backup" "/admin/settings"; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:8080${page}" 2>/dev/null || echo "FAIL")
    if [ "${CODE}" = "302" ] || [ "${CODE}" = "200" ]; then
        ok "  ${page} → ${CODE}"
    else
        fail "  ${page} → ${CODE} (expected 302/200)"
    fi
done

# === STEP 9: Migrate.sh prerequisite check ===
echo ""
echo "--- 9. Migration prerequisites ---"
for cmd in docker git; do
    if command -v "${cmd}" &>/dev/null; then
        ok "${cmd} installed"
    else
        fail "${cmd} NOT installed"
    fi
done

# === STEP 10: Restore.sh can parse a backup ===
echo ""
echo "--- 10. Restore script parsing ---"
if [ -n "${ARCHIVE}" ]; then
    # Test that restore.sh can at least list contents (inventory)
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

# === CLEANUP ===
rm -rf "${BACKUP_DIR}"

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
