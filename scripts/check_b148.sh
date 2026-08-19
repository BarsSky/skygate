#!/usr/bin/env bash
# check_b148.sh — v1.5.0 / B148 contracts.
#
# This is the B-check that pins the /admin/certificates page
# (TLS cert management: show current cert, upload new PEM
# pair, LE DNS-01 toggle). It verifies the B148 surface
# per docs/internal/ha-v1.5.0-execution.md §5.1 / Phase 4:
#
#   A. internal/feature/admin/certificates.go exists with
#      the 3 documented handlers (GetAdminCertificates,
#      PostAdminCertificateUpload, PostAdminCertificateToggleDNS01)
#      + the validateCertKeyPair re-use.
#   B. internal/handlers/templates/admin/certificates.html
#      exists and renders the 4 page sections (current cert,
#      upload form, DNS-01 toggle, recent events).
#   C. The i18n catalog has the 25 cert.* keys in BOTH
#      ruAdmin and enAdmin maps (parity is checked by
#      TestCatalogsParity in the i18n package).
#   D. cmd/skygate/main.go wires the 3 admin routes +
#      layout.html has the /admin/certificates sidebar link
#      + sectionPageSet includes admin/certificates.
#   E. The unit-test file internal/feature/admin/certificates_test.go
#      has 6+ test functions + go test passes.
#
# The script is intentionally read-only — it does not
# touch the database or run the live VM. The unit tests
# (`go test ./internal/feature/admin/`) cover the runtime
# contract; this script is the "is the code even there?"
# check.

set -euo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$REPO_ROOT"

# When the script is run from git-bash on Windows, the
# `go` binary is at C:\Program Files\Go\bin\go.exe and may
# not be on the bash PATH inherited from PowerShell. Add
# common locations.
if ! command -v go >/dev/null 2>&1; then
    for cand in \
        "/c/Program Files/Go/bin/go.exe" \
        "/c/Program Files (x86)/Go/bin/go.exe" \
        "/c/Go/bin/go.exe" \
        "/usr/local/go/bin/go" \
        "/opt/go/bin/go" \
        "/usr/bin/go"; do
        if [ -x "$cand" ]; then
            export PATH="$(dirname "$cand"):$PATH"
            break
        fi
    done
fi
# Last resort: use the Windows-style full path via cmd.exe
# (handles WSL where `/c/...` paths don't exist).
if ! command -v go >/dev/null 2>&1; then
    if command -v cmd.exe >/dev/null 2>&1; then
        go() { cmd.exe //c "go $*"; }
        export -f go 2>/dev/null || true
    fi
fi
if ! command -v go >/dev/null 2>&1; then
    echo "FATAL: go binary not found in PATH (looked in /c/Program Files/Go/bin and friends)"
    echo "Current PATH: $PATH"
    exit 1
fi

PASS=0
FAIL=0

ok()   { echo "PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "FAIL: $1"; FAIL=$((FAIL+1)); }

# --- contract A: internal/feature/admin/certificates.go ---------------
echo
echo "=== contract A: internal/feature/admin/certificates.go ==="
if [ -f internal/feature/admin/certificates.go ]; then
    ok "internal/feature/admin/certificates.go exists"
else
    bad "internal/feature/admin/certificates.go missing"
fi
# 3 handlers (the 3 documented POST/GET endpoints)
for sig in \
    "func (s *Service) GetAdminCertificates" \
    "func (s *Service) PostAdminCertificateUpload" \
    "func (s *Service) PostAdminCertificateToggleDNS01"; do
    if grep -q -F "$sig" internal/feature/admin/certificates.go; then
        ok "certificates.go has handler: ${sig##* }"
    else
        bad "certificates.go missing handler: $sig"
    fi
done
# Validation re-uses the certsync package (B147 owns
# the rules; B148 re-uses the exported wrapper).
if grep -q 'certsync.ValidateCertKeyPair' internal/feature/admin/certificates.go; then
    ok "certificates.go uses certsync.ValidateCertKeyPair (rules live in B147)"
else
    bad "certificates.go does not call certsync.ValidateCertKeyPair (validation should reuse the certsync rules)"
fi
# The Service struct must carry the CertUploadToS3 callback
# (wired by main.go at boot). The handler depends on it
# being settable from outside the package.
if grep -q 'CertUploadToS3' internal/feature/admin/service.go; then
    ok "service.go has CertUploadToS3 field (callback for the S3 upload)"
else
    bad "service.go missing CertUploadToS3 field (no way to wire the S3 upload from main.go)"
fi
# The certsync package must expose ValidateCertKeyPair
# (B147 added it; B148 depends on it).
if grep -q '^func ValidateCertKeyPair' internal/certsync/certsync.go; then
    ok "certsync.ValidateCertKeyPair is exported (B148 re-uses it)"
else
    bad "certsync.ValidateCertKeyPair is not exported (B148 cannot validate uploaded cert+key)"
fi

# --- contract B: certificates.html template renders 4 sections -------
echo
echo "=== contract B: internal/handlers/templates/admin/certificates.html ==="
if [ -f internal/handlers/templates/admin/certificates.html ]; then
    ok "internal/handlers/templates/admin/certificates.html exists"
else
    bad "internal/handlers/templates/admin/certificates.html missing"
fi
# The 4 page sections per the B148 plan.
for marker in \
    "body-admin-certificates" \
    "cert.title" \
    "cert_current" \
    "cert_upload_title" \
    "cert_dns01_title" \
    "cert_recent_events"; do
    if grep -q "$marker" internal/handlers/templates/admin/certificates.html; then
        ok "certificates.html has section marker: $marker"
    else
        bad "certificates.html missing section marker: $marker"
    fi
done
# Form fields — the upload form has cert_pem_file +
# cert_pem_text + key_pem_file + key_pem_text + dns01_enabled.
for field in \
    'name="cert_pem_file"' \
    'name="cert_pem_text"' \
    'name="key_pem_file"' \
    'name="key_pem_text"' \
    'name="dns01_enabled"'; do
    if grep -qF "$field" internal/handlers/templates/admin/certificates.html; then
        ok "certificates.html has form field: $field"
    else
        bad "certificates.html missing form field: $field"
    fi
done

# --- contract C: i18n catalog parity for cert.* keys -----------------
echo
echo "=== contract C: i18n catalog parity for cert.* keys ==="
if go test -count=1 -run "TestCatalogsParity" ./internal/i18n/ 2>&1 | tee /tmp/b148_parity.log >/dev/null; then
    ok "TestCatalogsParity PASS (RU+EN cert.* keys in lock-step)"
else
    bad "TestCatalogsParity FAIL (RU+EN cert.* keys mismatch) — see /tmp/b148_parity.log"
    head -10 /tmp/b148_parity.log
fi
# Spot-check the 25 cert.* keys exist in BOTH ruAdmin
# and enAdmin. We check the dot-prefixed ones (2) — the
# underscore ones (23) are still in lock-step via
# TestCatalogsParity, but we don't enumerate all 25 here
# to keep the script fast. The grep uses the actual
# padded format from the catalog (closing quote is at
# column 14 for `cert.title` and column 17 for
# `cert.subtitle`, with trailing spaces before the
# colon).
for key in \
    "cert.title" \
    "cert.subtitle"; do
    if grep -q "\"$key[[:space:]]*\":" internal/i18n/catalog_admin.go; then
        count=$(grep -c "\"$key[[:space:]]*\":" internal/i18n/catalog_admin.go)
        if [ "$count" -ge 2 ]; then
            ok "cert key present in both maps: $key (count=$count)"
        else
            bad "cert key only in one map: $key (count=$count, need 2)"
        fi
    else
        bad "cert key missing entirely: $key"
    fi
done
# nav.certificates in catalog_common.go.
if grep -q '"nav.certificates"' internal/i18n/catalog_common.go; then
    count=$(grep -c '"nav.certificates"' internal/i18n/catalog_common.go)
    if [ "$count" -ge 2 ]; then
        ok "nav.certificates present in both maps (count=$count)"
    else
        bad "nav.certificates only in one map (count=$count, need 2)"
    fi
else
    bad "nav.certificates missing entirely from catalog_common.go"
fi

# --- contract D: main.go routes + layout + sectionPageSet -----------
echo
echo "=== contract D: cmd/skygate/main.go routes + layout ==="
# 3 admin routes
for route in \
    "GET /admin/certificates" \
    "POST /admin/certificates/upload" \
    "POST /admin/certificates/toggle-dns01"; do
    if grep -q "mux.Handle(\"$route\"" cmd/skygate/main.go; then
        ok "main.go registers route: $route"
    else
        bad "main.go missing route: $route"
    fi
done
# Layout has the /admin/certificates menu link.
if grep -q 'href="/admin/certificates"' internal/handlers/templates/layout.html; then
    ok "layout.html has /admin/certificates menu link"
else
    bad "layout.html missing /admin/certificates menu link"
fi
# sectionPageSet includes admin/certificates.
if grep -q '"admin/certificates"' internal/handlers/handlers.go; then
    ok "handlers.go sectionPageSet includes admin/certificates"
else
    bad "handlers.go sectionPageSet missing admin/certificates"
fi

# --- contract E: unit-test file + go test passes --------------------
echo
echo "=== contract E: certificates_test.go + go test ==="
TEST_FILE="internal/feature/admin/certificates_test.go"
if [ -f "$TEST_FILE" ]; then
    ok "$TEST_FILE exists"
else
    bad "$TEST_FILE missing"
fi
# At least 6 test functions.
if [ -f "$TEST_FILE" ]; then
    n=$(grep -c "^func Test" "$TEST_FILE" || true)
    if [ "$n" -ge 6 ]; then
        ok "$TEST_FILE has $n test functions (>= 6 required)"
    else
        bad "$TEST_FILE has only $n test functions (need >= 6)"
    fi
fi
# The 6 must-include tests (per the B148 plan).
for t in \
    "TestReadLocalCertInfo_ParsesValidCert" \
    "TestReadLocalCertInfo_MalformedCert" \
    "TestReadCertInput_PrefersFile" \
    "TestReadCertInput_FallsBackToText" \
    "TestCertRedirect_EncodesFlash" \
    "TestCertChainStrings_ReturnsIssuer"; do
    if [ -f "$TEST_FILE" ] && grep -q "^func $t" "$TEST_FILE"; then
        ok "test function present: $t"
    else
        bad "test function missing: $t"
    fi
done
# go test passes.
if go test -count=1 -short -run "TestReadLocalCertInfo|TestReadCertInput|TestCertRedirect|TestCertSyncCertPath|TestCertChainStrings" ./internal/feature/admin/ 2>&1 | tee /tmp/b148_tests.log >/dev/null; then
    ok "go test PASSes for B148 pure-Go helpers (certificates_test.go compiles + tests green)"
else
    bad "go test FAILs for B148 pure-Go helpers — see /tmp/b148_tests.log"
    head -20 /tmp/b148_tests.log
fi

# --- summary ----------------------------------------------------------
echo
echo "=== summary ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
if [ "$FAIL" -eq 0 ]; then
    echo "all B148 contracts satisfied"
    exit 0
else
    echo "B148 contracts NOT satisfied"
    exit 1
fi
