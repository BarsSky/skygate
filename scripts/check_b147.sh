#!/usr/bin/env bash
# check_b147.sh — v1.5.0 / B147 contracts.
#
# This is the B-check that pins the in-app certsync
# scheduler (Phase 3 of the v1.5.0 BL-2 plan). It verifies
# five things:
#
#   A. internal/certsync/{certsync,certsync_crypto,
#      s3adapter}.go exist with the 5 documented functions
#      (Start, tick, validateCertKeyPair, writeLocalCerts,
#      checkExpiry) + the S3Client interface.
#   B. internal/certsync/certsync_test.go exists with 4
#      pure-Go unit tests (no S3, no network): NoVersionIsNoOp,
#      VersionBumpTriggersPull, SHAMismatchTriggersPull,
#      InvalidCertFails.
#   C. cmd/skygate/main.go wires the certsync scheduler
#      via Start() (gated on cfg.CertSyncEnabled), the
#      buildBackupConfigForCertSync helper builds a
#      minimal backup.Config from env vars, and the
#      S3 adapter wraps the production minio client.
#   D. internal/config/config.go has the 4 new env-driven
#      config fields (CertSyncEnabled, CertSyncBucket,
#      CertSyncLocalDir, CertSyncInterval) with the
#      documented defaults (enabled, skygate-backups,
#      /var/lib/skygate/certs, 30s).
#   E. main.go imports "skygate/internal/certsync" +
#      the "skygate/internal/backup" S3 client builder
#      is reachable + the startup log line announces
#      whether certsync is enabled or disabled.
#
# The script is intentionally read-only — it does not
# touch the database or run the live VM. The unit tests
# (`go test ./internal/certsync/`) cover the runtime
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
if ! command -v go >/dev/null 2>&1; then
    if command -v cmd.exe >/dev/null 2>&1; then
        # Define a shim function that runs go via cmd
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

# --- contract A: internal/certsync/* with 5 functions -----------------
echo
echo "=== contract A: internal/certsync/{certsync,certsync_crypto,s3adapter}.go ==="
for f in certsync.go certsync_crypto.go s3adapter.go; do
    if [ -f "internal/certsync/$f" ]; then
        ok "internal/certsync/$f exists"
    else
        bad "internal/certsync/$f missing"
    fi
done
for sig in \
    "func Start(ctx context.Context, deps CertSyncDeps) (*CertSync, error)" \
    "func (c *CertSync) tick(ctx context.Context, deps CertSyncDeps)" \
    "func validateCertKeyPair(cert, key []byte) error" \
    "func (c *CertSync) writeLocalCerts(dir string, cert, key []byte) error" \
    "func (c *CertSync) checkExpiry(ctx context.Context, deps CertSyncDeps)"; do
    name=$(echo "$sig" | sed -E 's/^func ([A-Za-z][A-Za-z0-9_]*).*/\1/')
    if grep -q -F "$sig" internal/certsync/certsync.go; then
        ok "certsync has function: $name"
    else
        bad "certsync missing function: $name"
    fi
done
# S3Client interface must be declared.
if grep -q "^type S3Client interface" internal/certsync/certsync.go; then
    ok "S3Client interface declared in certsync.go"
else
    bad "S3Client interface missing from certsync.go"
fi
# S3 adapter must be wrapped from minio.Client.
if grep -q "github.com/minio/minio-go/v7" internal/certsync/s3adapter.go; then
    ok "s3adapter.go imports minio-go"
else
    bad "s3adapter.go does not import minio-go"
fi
if grep -q "type MinioS3Client struct" internal/certsync/s3adapter.go; then
    ok "MinioS3Client adapter struct declared"
else
    bad "MinioS3Client adapter struct missing"
fi

# --- contract B: certsync_test.go with 4 unit tests -----------------
echo
echo "=== contract B: internal/certsync/certsync_test.go ==="
if [ -f internal/certsync/certsync_test.go ]; then
    ok "internal/certsync/certsync_test.go exists"
else
    bad "internal/certsync/certsync_test.go missing"
fi
for t in \
    "TestNoVersionIsNoOp" \
    "TestVersionBumpTriggersPull" \
    "TestSHAMismatchTriggersPull" \
    "TestInvalidCertFails"; do
    if grep -q -E "^func $t" internal/certsync/certsync_test.go; then
        ok "certsync_test.go has test: $t"
    else
        bad "certsync_test.go missing test: $t"
    fi
done
# The tests must pass under the standard `go test` invocation.
if go test -count=1 -short -run "TestNoVersionIsNoOp|TestVersionBumpTriggersPull|TestSHAMismatchTriggersPull|TestInvalidCertFails" ./internal/certsync/ 2>&1 | tee /tmp/b147_tests.log >/dev/null; then
    ok "certsync unit tests PASS"
else
    bad "certsync unit tests FAIL — see /tmp/b147_tests.log"
    head -20 /tmp/b147_tests.log
fi

# --- contract C: main.go wires the certsync scheduler --------------
echo
echo "=== contract C: cmd/skygate/main.go certsync wiring ==="
if grep -q 'certsync\.Start(' cmd/skygate/main.go; then
    ok "main.go calls certsync.Start"
else
    bad "main.go missing certsync.Start call"
fi
if grep -q 'buildBackupConfigForCertSync' cmd/skygate/main.go; then
    ok "main.go has buildBackupConfigForCertSync helper"
else
    bad "main.go missing buildBackupConfigForCertSync helper"
fi
if grep -q "cfg.CertSyncEnabled" cmd/skygate/main.go; then
    ok "main.go gates certsync on cfg.CertSyncEnabled"
else
    bad "main.go does not gate certsync on cfg.CertSyncEnabled"
fi
if grep -q 'certsync.NewMinioS3Client' cmd/skygate/main.go; then
    ok "main.go wires the S3 adapter (NewMinioS3Client)"
else
    bad "main.go missing NewMinioS3Client call"
fi
if grep -q 'backup.NewS3ClientForConfig' cmd/skygate/main.go; then
    ok "main.go uses backup.NewS3ClientForConfig (production S3 client)"
else
    bad "main.go does not use backup.NewS3ClientForConfig"
fi
# main.go imports the certsync package.
if grep -q '"skygate/internal/certsync"' cmd/skygate/main.go; then
    ok "main.go imports skygate/internal/certsync"
else
    bad "main.go missing skygate/internal/certsync import"
fi
# Startup log line announces enabled / disabled.
if grep -q 'certsync: enabled' cmd/skygate/main.go; then
    ok "main.go logs 'certsync: enabled' line on boot"
else
    bad "main.go missing 'certsync: enabled' log line"
fi
if grep -q 'certsync: disabled' cmd/skygate/main.go; then
    ok "main.go logs 'certsync: disabled' line on boot"
else
    bad "main.go missing 'certsync: disabled' log line"
fi

# --- contract D: config.go has 4 new CertSync* fields --------------
echo
echo "=== contract D: internal/config/config.go CertSync* fields ==="
for field in \
    "CertSyncEnabled" \
    "CertSyncBucket" \
    "CertSyncLocalDir" \
    "CertSyncInterval"; do
    if grep -q "$field " internal/config/config.go; then
        ok "config.go has field: $field"
    else
        bad "config.go missing field: $field"
    fi
done
# Env-var defaults.
for envvar in \
    "SKYGATE_CERTSYNC_ENABLED" \
    "SKYGATE_CERTSYNC_S3_BUCKET" \
    "SKYGATE_CERTSYNC_LOCAL_DIR" \
    "SKYGATE_CERTSYNC_INTERVAL"; do
    if grep -q "$envvar" internal/config/config.go; then
        ok "config.go reads env var: $envvar"
    else
        bad "config.go missing env var: $envvar"
    fi
done
# Default values match the plan (bucket=skygate-backups,
# local=/var/lib/skygate/certs, interval=30s).
if grep -q 'CertSyncBucket:.*"skygate-backups"' internal/config/config.go; then
    ok "CertSyncBucket default = 'skygate-backups' (per plan)"
else
    bad "CertSyncBucket default does not match plan ('skygate-backups')"
fi
if grep -q 'CertSyncLocalDir:.*"/var/lib/skygate/certs"' internal/config/config.go; then
    ok "CertSyncLocalDir default = '/var/lib/skygate/certs' (per plan)"
else
    bad "CertSyncLocalDir default does not match plan"
fi
if grep -q '30\*time.Second' internal/config/config.go; then
    ok "CertSyncInterval default = 30s (per plan)"
else
    bad "CertSyncInterval default does not match plan (30s)"
fi

# --- contract E: S3 layout constants are pinned ------------------------
echo
echo "=== contract E: S3 layout constants + key paths ==="
# The S3 key layout is the contract between the in-app
# scheduler and the operator-side cert-renew script. If
# the keys change, the renewal script's uploads won't be
# seen by the scheduler — silent failure. Pin the keys.
for key in \
    '"certs/.version"' \
    '"certs/cert.pem"' \
    '"certs/key.pem"'; do
    if grep -q "$key" internal/certsync/certsync.go; then
        ok "S3 key constant pinned: $key"
    else
        bad "S3 key constant missing: $key"
    fi
done
# Local dir layout.
for path in 'LocalCert = "cert.pem"' 'LocalKey  = "key.pem"' 'LocalVersionCache = ".certsync-version"'; do
    if grep -q "$path" internal/certsync/certsync.go; then
        ok "local path constant pinned: $path"
    else
        bad "local path constant missing: $path"
    fi
done

# --- summary ----------------------------------------------------------
echo
echo "=== summary ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
if [ "$FAIL" -eq 0 ]; then
    echo "all B147 contracts satisfied"
    exit 0
else
    echo "B147 contracts NOT satisfied"
    exit 1
fi
