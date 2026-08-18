#!/usr/bin/env bash
# check_b112.sh — v1.3.12 (P4 catalog cleanup): verify 5 dead-code removals
# + 2 verify-pre check updates for v1.3.0+ reality.
#
# The 7 uncommitted items in working tree (post-Phase 3, 2026-08-13)
# were correctly removed:
#   1. internal/backup/s3.go: s3Client interface + realS3Client wrapper
#   2. internal/feature/admin/integrations_renderer.go: dockerCmdStdin +
#      renderHeadscaleCompose + stripHeadplaneServiceBlock + startsWithWhitespace
#   3. internal/telegram/commands_login.go: resetLoginAttempts
#   4. internal/telegram/commands_phase4.go: setKillProcess
#   5. internal/telegram/commands_user.go: hostnameMapFromHeadscale
# And the verify-pre checks for v1.3.0+ reality:
#   6. scripts/check_b93.sh: t.Skip stub acceptance (not running old SQLite test)
#   7. scripts/check_b95.sh: t.Skip stub acceptance (not checking empty if body)
# Plus:
#   8. scripts/verify_pre_deploy.sh B38: now uses migrations_pg.go + t.Skip stub
#   9. go build ./... must succeed (no broken references)

set -u
cd "$(dirname "$0")/.."

fail=0

# 1. s3Client interface removed (use `type s3Client` to skip comments)
if grep -qE '^type s3Client ' internal/backup/s3.go; then
    echo "SKY-FAIL: s3Client type definition still present" >&2
    fail=1
else
    echo "  PASS: s3Client interface removed"
fi
if grep -qE 'type realS3Client ' internal/backup/s3.go; then
    echo "SKY-FAIL: realS3Client type still present" >&2
    fail=1
else
    echo "  PASS: realS3Client wrapper removed"
fi
# Also verify the FPutObject call uses mc directly (not cl.FPutObject)
if grep -qE 'cl\.(FPutObject|BucketExists)' internal/backup/s3.go; then
    echo "SKY-FAIL: indirection cl.FPutObject/cl.BucketExists still present" >&2
    fail=1
else
    echo "  PASS: FPutObject/BucketExists call mc directly"
fi

# 2. integrations_renderer dead code removed
for fn in dockerCmdStdin renderHeadscaleCompose stripHeadplaneServiceBlock startsWithWhitespace; do
    if grep -qE "^(var|func) $fn" internal/feature/admin/integrations_renderer.go; then
        echo "SKY-FAIL: $fn definition still present in integrations_renderer.go" >&2
        fail=1
    else
        echo "  PASS: $fn removed from integrations_renderer.go"
    fi
done

# 3. resetLoginAttempts removed
if grep -qF 'func resetLoginAttempts' internal/telegram/commands_login.go; then
    echo "SKY-FAIL: resetLoginAttempts still present" >&2
    fail=1
else
    echo "  PASS: resetLoginAttempts removed"
fi

# 4. setKillProcess removed
if grep -qF 'func setKillProcess' internal/telegram/commands_phase4.go; then
    echo "SKY-FAIL: setKillProcess still present" >&2
    fail=1
else
    echo "  PASS: setKillProcess removed"
fi

# 5. hostnameMapFromHeadscale removed
if grep -qF 'func hostnameMapFromHeadscale' internal/telegram/commands_user.go; then
    echo "SKY-FAIL: hostnameMapFromHeadscale still present" >&2
    fail=1
else
    echo "  PASS: hostnameMapFromHeadscale removed"
fi

# 6. check_b93.sh accepts t.Skip stub (not running old SQLite TestBackfillInfra)
if grep -qF 'TestBackfillInfra_InfraDevTag_InsertsRow' scripts/check_b93.sh; then
    echo "SKY-FAIL: check_b93.sh still pins the old SQLite-era test fn name" >&2
    fail=1
else
    echo "  PASS: check_b93.sh updated to v1.3.0+ PG form"
fi

# 7. check_b95.sh accepts t.Skip stub (not checking empty if body)
if grep -qF 't.Errorf("cache not cleared after token change' scripts/check_b95.sh; then
    echo "SKY-FAIL: check_b95.sh still pins the v0.34.0 SA4017 grep" >&2
    fail=1
else
    echo "  PASS: check_b95.sh updated to v1.3.0+ t.Skip stub form"
fi

# 8. B38 grep uses migrations_pg.go + t.Skip stub (only the B38 run_check
#    block itself should not reference migrations_v0.50.go; comments above
#    can mention it for context. So we narrow the range to just the
#    `run_check "B38"` line and the following bash block.)
if sed -n '941,950p' scripts/verify_pre_deploy.sh | grep -qF 'migrations_v0.50.go'; then
    echo "SKY-FAIL: B38 run_check still references migrations_v0.50.go" >&2
    fail=1
else
    echo "  PASS: B38 run_check uses migrations_pg.go"
fi
if sed -n '941,950p' scripts/verify_pre_deploy.sh | grep -qF 'migrations_pg.go'; then
    echo "  PASS: B38 run_check references migrations_pg.go"
else
    echo "SKY-FAIL: B38 run_check should reference migrations_pg.go" >&2
    fail=1
fi
if sed -n '941,950p' scripts/verify_pre_deploy.sh | grep -qF 't.Skip'; then
    echo "  PASS: B38 run_check accepts t.Skip stub"
else
    echo "SKY-FAIL: B38 run_check should accept t.Skip stub" >&2
    fail=1
fi

# 9. go build passes (resolves go binary like verify_pre_deploy.sh does)
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in \
        "/c/Program Files/Go/bin/go.exe" \
        "/c/Program Files/Go/bin/go" \
        "/mnt/c/Program Files/Go/bin/go.exe" \
        "/mnt/c/Program Files/Go/bin/go" \
        "/usr/local/go/bin/go" \
        "/usr/lib/go/bin/go" \
        "/opt/go/bin/go" \
        "/snap/bin/go"; do
        if [ -x "$cand" ]; then
            GO="$cand"
            break
        fi
    done
fi
if [ -z "$GO" ]; then
    echo "SKY-FAIL: go binary not found" >&2
    fail=1
elif ! "$GO" build ./... >/dev/null 2>&1; then
    echo "SKY-FAIL: go build ./... failed (GO=$GO)" >&2
    fail=1
else
    echo "  PASS: go build ./... clean (GO=$GO)"
fi

if [ $fail -eq 0 ]; then
    echo ""
    echo "B112 PASS: v1.3.12 P4 catalog cleanup is in effect (5 dead-code removals + 3 check updates + 1 build check)"
    exit 0
else
    echo ""
    echo "B112 FAIL: v1.3.12 P4 catalog cleanup incomplete" >&2
    exit 1
fi
