#!/bin/bash
# check_b249.sh — B249 image-pull update contract check.
#
# B249 (2026-09-15): the image-pull update path is a fast (~5-30s)
# alternative to the git+build update path (~60-120s). The
# /admin/update page exposes both buttons; this script pins
# the contracts that make the image-pull path work end-to-end.
#
# Live-verified against the test integration environment at
# C:\skygate-test on 192.168.13.20 (2026-09-15).
#
# Usage:
#   bash scripts/check_b249.sh
#
# Exit codes:
#   0 = all contracts pass
#   1 = at least one contract failed (printed inline)

set -u
PASS=0
FAIL=0

# ANSI colors (silenced if not a TTY).
if [ -t 1 ]; then
  C_GREEN="\033[32m"; C_RED="\033[31m"; C_RESET="\033[0m"
else
  C_GREEN=""; C_RED=""; C_RESET=""
fi

ok() {
  echo -e "  ${C_GREEN}PASS${C_RESET}  $1"
  PASS=$((PASS + 1))
}

bad() {
  echo -e "  ${C_RED}FAIL${C_RESET}  $1"
  FAIL=$((FAIL + 1))
}

check() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    ok "$desc"
  else
    bad "$desc"
  fi
}

# -----------------------------------------------------------------
# A. Source-code contracts
# -----------------------------------------------------------------
echo "=== A. Source-code contracts (internal/update/image.go) ==="

# A1. The ImagePullStrategy struct + Run method exist.
check "internal/update/image.go has NewImagePullStrategy" \
  grep -q 'func NewImagePullStrategy' internal/update/image.go
check "internal/update/image.go has ImagePullStrategy.Run method" \
  grep -q 'func (s \*ImagePullStrategy) Run' internal/update/image.go
check "internal/update/image.go has imageIsFromRegistry gate" \
  grep -q 'imageIsFromRegistry' internal/update/image.go
check "internal/update/image.go has pollHealthz post-restart check" \
  grep -q 'pollHealthz' internal/update/image.go
check "internal/update/image.go has backup-tag rename" \
  grep -q 'skygate-pre-update-' internal/update/image.go
check "internal/update/image.go prefers docker-compose.ghcr.yml" \
  grep -q 'docker-compose.ghcr.yml' internal/update/image.go

# A2. shellExec var override exists (for tests).
check "internal/update/shell.go has shellExec var" \
  grep -q 'var shellExec' internal/update/shell.go
check "internal/update/shell.go has realShellExec" \
  grep -q 'func realShellExec' internal/update/shell.go

# A3. Handler wiring.
check "internal/feature/admin/update.go has PostAdminUpdatePullImage" \
  grep -q 'PostAdminUpdatePullImage' internal/feature/admin/update.go

# A4. Route registration in main.go.
check "cmd/skygate/main.go registers POST /admin/update/pull-image" \
  grep -q 'POST /admin/update/pull-image' cmd/skygate/main.go

# A5. UI button in template.
check "internal/handlers/templates/admin/update.html has pull-image form" \
  grep -q 'admin/update/pull-image' internal/handlers/templates/admin/update.html

# A6. i18n keys (RU + EN).
check "catalog_update.go has update.image_pull (RU)" \
  grep -q '"update.image_pull"' internal/i18n/catalog_update.go
check "catalog_update.go has update.image_pull_help (RU)" \
  grep -q '"update.image_pull_help"' internal/i18n/catalog_update.go
check "catalog_update.go has update.image_pull_confirm (RU)" \
  grep -q '"update.image_pull_confirm"' internal/i18n/catalog_update.go

# -----------------------------------------------------------------
# B. Test contracts
# -----------------------------------------------------------------
echo ""
echo "=== B. Test contracts (internal/update/image_b249_test.go) ==="

check "image_b249_test.go exists" \
  test -f internal/update/image_b249_test.go

check "TestImageIsFromRegistry covers registry vs local" \
  grep -q 'func TestImageIsFromRegistry' internal/update/image_b249_test.go

check "TestImageTag covers tag extraction edge cases" \
  grep -q 'func TestImageTag' internal/update/image_b249_test.go

check "TestRun_PreFlightRejectsLocallyBuiltImage pins the regression" \
  grep -q 'func TestRun_PreFlightRejectsLocallyBuiltImage' internal/update/image_b249_test.go

check "TestRun_AlreadyOnTarget_IsNoOp pins no-op branch" \
  grep -q 'func TestRun_AlreadyOnTarget_IsNoOp' internal/update/image_b249_test.go

check "TestRun_SuccessPath pins happy-path sequence" \
  grep -q 'func TestRun_SuccessPath' internal/update/image_b249_test.go

check "TestRun_ComposeUpFailure_RollsBack pins rollback" \
  grep -q 'func TestRun_ComposeUpFailure_RollsBack' internal/update/image_b249_test.go

# -----------------------------------------------------------------
# Summary
# -----------------------------------------------------------------
echo ""
echo "=== B249 summary: $PASS pass / $FAIL fail ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
