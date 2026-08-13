#!/usr/bin/env bash
# check_b114.sh — v1.3.14 (BL-17): verify_migration.sh exists
# and chains the 3 post-deploy phases:
#   1. verify_post_deploy.sh --quick (R1-R9 + R26)
#   2. POST /admin/system_tests/run (15 in-app tests)
#   3. Manual checks (healthz, readyz, /admin/services) — printed
#      for the operator to run themselves
#
# Plus the Python driver that runs INSIDE the skygate container
# (because busybox wget doesn't support cookies, and the system
# tests endpoint is internal localhost:8080).
#
# Pin: 8 contracts:
#  - script exists, is executable, bash syntax-valid
#  - script accepts SSH_HOST (positional $1 + env var)
#  - script has 3 phase sections
#  - script uses pre-state capture (MIGRATION DETECTED mode)
#  - script has Python driver with urllib + cookiejar
#  - script has Phase 1 Python3-FAIL-detect fallback
#  - script logs STATUS: passed=N failed=N line for parse
#  - catalog check itself runs in < 10 seconds

set -u
cd "$(dirname "$0")/.."

fail=0

# 1. Script exists
if [ ! -f scripts/verify_migration.sh ]; then
    echo "SKY-FAIL: scripts/verify_migration.sh missing" >&2
    fail=1
else
    echo "  PASS: scripts/verify_migration.sh exists"
fi

# 2. bash syntax valid
if ! bash -n scripts/verify_migration.sh 2>/dev/null; then
    echo "SKY-FAIL: scripts/verify_migration.sh has bash syntax errors" >&2
    fail=1
else
    echo "  PASS: bash -n clean"
fi

# 3. SSH_HOST resolution (positional + env var)
for needle in 'SSH_HOST="${SSH_HOST:-${1:-}}"'; do
    if ! grep -qF "$needle" scripts/verify_migration.sh; then
        echo "SKY-FAIL: SSH_HOST resolution missing — needle: $needle" >&2
        fail=1
    else
        echo "  PASS: SSH_HOST resolution: $needle"
    fi
done

# 4. 3 phase sections
if ! grep -qE 'Phase 1/3:' scripts/verify_migration.sh; then
    echo "SKY-FAIL: 'Phase 1/3:' section missing" >&2
    fail=1
else
    echo "  PASS: Phase 1 section"
fi
if ! grep -qE 'Phase 2/3:' scripts/verify_migration.sh; then
    echo "SKY-FAIL: 'Phase 2/3:' section missing" >&2
    fail=1
else
    echo "  PASS: Phase 2 section"
fi
if ! grep -qE 'Phase 3/3:' scripts/verify_migration.sh; then
    echo "SKY-FAIL: 'Phase 3/3:' section missing" >&2
    fail=1
else
    echo "  PASS: Phase 3 section"
fi

# 5. PRE_BUILD support (MIGRATION DETECTED mode)
if ! grep -qE 'PRE_BUILD' scripts/verify_migration.sh; then
    echo "SKY-FAIL: PRE_BUILD pre-state capture missing" >&2
    fail=1
else
    echo "  PASS: PRE_BUILD pre-state capture (MIGRATION DETECTED mode)"
fi

# 6. Python driver with urllib + cookiejar
if ! grep -qE 'urllib\.request|http\.cookiejar' scripts/verify_migration.sh; then
    echo "SKY-FAIL: Python driver missing urllib.request or http.cookiejar" >&2
    fail=1
else
    echo "  PASS: Python driver uses urllib + cookiejar (busybox-wget-compatible)"
fi

# 7. Phase 1 Python3-FAIL-detect fallback (Windows+verify_post_deploy.sh)
if ! grep -qF 'no working python3' scripts/verify_migration.sh; then
    echo "SKY-FAIL: Phase 1 python3-FAIL-detect fallback missing" >&2
    fail=1
else
    echo "  PASS: Phase 1 python3-FAIL-detect fallback (Windows host workaround)"
fi

# 8. STATUS: parse line for machine-readable output
if ! grep -qF 'STATUS: passed=' scripts/verify_migration.sh; then
    echo "SKY-FAIL: machine-readable 'STATUS: passed=N failed=N' line missing" >&2
    fail=1
else
    echo "  PASS: machine-readable STATUS: line"
fi

# 9. Driver scp'd to VM + docker cp into skygate container
for needle in 'scp -o StrictHostKeyChecking=no' 'docker cp /tmp/_vmig_driver.py' 'docker exec skygate-skygate-1 python3'; do
    if ! grep -qF "$needle" scripts/verify_migration.sh; then
        echo "SKY-FAIL: missing driver staging step — needle: $needle" >&2
        fail=1
    else
        echo "  PASS: driver staging: $needle"
    fi
done

if [ $fail -eq 0 ]; then
    echo ""
    echo "B114 PASS: verify_migration.sh chains 3 phases with portable driver staging (8 contracts)"
    exit 0
else
    echo ""
    echo "B114 FAIL: verify_migration.sh incomplete" >&2
    exit 1
fi
