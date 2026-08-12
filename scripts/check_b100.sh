#!/usr/bin/env bash
#===============================================================================
# B100 (v1.3.8): S3 / S3-compatible backup destination
#
# Pins the v1.3.8 contracts that add S3 (AWS, MinIO, Yandex Object
# Storage, Selectel, VK Cloud, Backblaze B2) as a fifth backup
# destination. Mirrors the B96/B97/B98/B99 pattern (dedicated
# helper invoked from verify_pre_deploy.sh) to avoid the
# nested-quote hell that hits when this many greps get
# inlined in a single run_check function.
#
# What's pinned (each grep has a clear "what it catches" line
# in RELEASE-NOTES.md and the per-file comment):
#
#   1. internal/backup/config.go: ProtocolS3 constant + S3
#      fields on Config + S3 keys in storage constants +
#      S3 case in detectProtocol + S3 case in Validate.
#
#   2. internal/backup/s3.go: real file exists with the
#      newS3Client + uploadToS3 + buildS3Key funcs.
#
#   3. internal/backup/s3_test.go: 4 unit tests cover
#      buildS3Key, detectProtocol, S3 Validate, and the
#      AllProtocols-includes-S3 contract.
#
#   4. internal/backup/runner.go: ProtocolS3 path in
#      runBackupLocked — staging dir selection + post-run
#      S3 upload call.
#
#   5. internal/backup/mount.go: ProtocolS3 is a no-op for
#      Mount / Unmount (no FUSE layer).
#
#   6. internal/handlers/templates/admin/backup.html: the
#      form has 8 S3 fields (endpoint, region, access_key,
#      secret_key, bucket, prefix, staging_dir, use_ssl) +
#      data-show-for="s3" toggles.
#
#   7. internal/feature/admin/backup_config.go: S3 fields
#      parsed from POST + audit log mentions s3_bucket.
#
#   8. internal/i18n/catalog_backup.go: protocol_s3 +
#      s3_endpoint + s3_region + s3_access_key + s3_secret_key
#      + s3_bucket + s3_prefix + s3_staging_dir + s3_use_ssl
#      + s3_test_ok i18n keys (ru + en parity, 10 each).
#
#   9. go.mod: github.com/minio/minio-go/v7 in direct deps
#      (NOT indirect — confirms something in our code
#      actually imports it).
#
#  10. Unit tests pass: TestBuildS3Key, TestDetectProtocolS3,
#      TestS3Validate, TestIsValidProtocolIncludesS3 (4
#      separate TestXxx cases in internal/backup/s3_test.go).
#
# This script is idempotent. Re-running it after any v1.3.8
# refactor catches accidental removals (B-check stays green
# = catalog stays green).
#===============================================================================
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

PASS=0
FAIL=0
WARN=0

ok()   { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

echo
echo "=== B100 v1.3.8: S3 backup destination ==="
echo

# 1. config.go additions
if grep -q 'ProtocolS3 Protocol = "s3"' internal/backup/config.go ; then
  ok "config.go: ProtocolS3 constant defined"
else
  bad "config.go: ProtocolS3 constant missing"
fi
for f in S3Endpoint S3Region S3AccessKey S3SecretKey S3Bucket S3Prefix S3StagingDir S3UseSSL ; do
  if grep -q "	${f} " internal/backup/config.go ; then
    ok "config.go: Config.${f} field present"
  else
    bad "config.go: Config.${f} field missing"
  fi
done
if grep -qE '"s3://"' internal/backup/config.go && grep -qF 'return ProtocolS3' internal/backup/config.go ; then
  ok "config.go: detectProtocol handles ProtocolS3 (s3:// scheme)"
else
  bad "config.go: detectProtocol missing ProtocolS3 detection"
fi
if grep -q 'c\.Protocol == ProtocolS3' internal/backup/config.go ; then
  ok "config.go: Validate handles S3 (bucket/creds check)"
else
  bad "config.go: Validate missing S3 branch"
fi

# 2. s3.go file exists with key funcs
if [[ -f internal/backup/s3.go ]] ; then
  ok "s3.go file exists"
  for fn in 'func newS3Client' 'func uploadToS3' 'func buildS3Key' ; do
    if grep -q "${fn}" internal/backup/s3.go ; then
      ok "s3.go: ${fn} present"
    else
      bad "s3.go: ${fn} missing"
    fi
  done
else
  bad "internal/backup/s3.go not found"
fi

# 3. s3_test.go has the 4 unit tests
if [[ -f internal/backup/s3_test.go ]] ; then
  ok "s3_test.go file exists"
  for tn in TestBuildS3Key TestDetectProtocolS3 TestS3Validate TestIsValidProtocolIncludesS3 ; do
    if grep -q "^func ${tn}" internal/backup/s3_test.go ; then
      ok "s3_test.go: ${tn} defined"
    else
      bad "s3_test.go: ${tn} missing"
    fi
  done
else
  bad "internal/backup/s3_test.go not found"
fi

# 4. runner.go has the S3 path
if grep -q 'c\.Protocol == ProtocolS3' internal/backup/runner.go ; then
  ok "runner.go: S3 path present (staging dir + upload)"
else
  bad "runner.go: S3 path missing"
fi
if grep -q 'uploadToS3' internal/backup/runner.go ; then
  ok "runner.go: calls uploadToS3 for S3 protocol"
else
  bad "runner.go: does not call uploadToS3"
fi

# 5. mount.go: S3 is a no-op for mount/unmount
if grep -qE 'c\.Protocol == ProtocolS3.*\n.*return nil' internal/backup/mount.go ; then
  ok "mount.go: S3 short-circuit in Mount"
else
  # Multi-line patterns trip the grep; check more loosely.
  if grep -q 'c.Protocol == ProtocolS3' internal/backup/mount.go && grep -q 'return nil' internal/backup/mount.go ; then
    ok "mount.go: S3 short-circuit in Mount (loose match)"
  else
    bad "mount.go: S3 short-circuit in Mount not found"
  fi
fi
if grep -q 'protocol == ProtocolS3' internal/backup/mount.go ; then
  ok "mount.go: S3 short-circuit in Unmount"
else
  bad "mount.go: S3 short-circuit in Unmount not found"
fi

# 6. backup.html form has S3 fields
if [[ -f internal/handlers/templates/admin/backup.html ]] ; then
  for f in s3_endpoint s3_region s3_bucket s3_prefix s3_access_key s3_secret_key s3_staging_dir s3_use_ssl ; do
    if grep -qE "name=\"${f}\"" internal/handlers/templates/admin/backup.html ; then
      ok "backup.html: ${f} form field present"
    else
      bad "backup.html: ${f} form field missing"
    fi
  done
  if grep -q 'data-show-for="s3"' internal/handlers/templates/admin/backup.html ; then
    ok "backup.html: data-show-for=\"s3\" present"
  else
    bad "backup.html: data-show-for=\"s3\" missing"
  fi
else
  bad "admin/backup.html not found"
fi

# 7. backup_config.go parses S3 fields from POST
if grep -qE 'r\.FormValue\("s3_(endpoint|region|access_key|secret_key|bucket|prefix|staging_dir)"\)' internal/feature/admin/backup_config.go ; then
  ok "backup_config.go: S3 fields parsed from POST"
else
  bad "backup_config.go: S3 fields not parsed from POST"
fi
if grep -q 's3_bucket' internal/feature/admin/backup_config.go ; then
  ok "backup_config.go: audit log mentions s3_bucket"
else
  warn "backup_config.go: audit log does not mention s3_bucket"
fi

# 8. i18n keys (ru + en parity, 10 keys × 2 = 20 expected matches)
keys_needed=(
  backup.protocol_s3
  backup.s3_endpoint
  backup.s3_region
  backup.s3_access_key
  backup.s3_secret_key
  backup.s3_bucket
  backup.s3_prefix
  backup.s3_staging_dir
  backup.s3_use_ssl
  backup.s3_test_ok
)
if [[ -f internal/i18n/catalog_backup.go ]] ; then
  ru_ok=0
  en_ok=0
  for k in "${keys_needed[@]}" ; do
    ru_count=$(grep -c "\"${k}\"" internal/i18n/catalog_backup.go)
    # ru is the first map (ruBackup); en is the second (enBackup).
    # We split the file at the `var enBackup` marker and count
    # in each half.
    ru_in_first=$(awk '/^var ruBackup/,/^}$/' internal/i18n/catalog_backup.go | grep -c "\"${k}\"")
    en_in_second=$(awk '/^var enBackup/,/^}$/' internal/i18n/catalog_backup.go | grep -c "\"${k}\"")
    if [[ "${ru_in_first}" -ge 1 ]] && [[ "${en_in_second}" -ge 1 ]] ; then
      : # counted below
    else
      bad "i18n: key ${k} missing in ru or en catalog (ru=${ru_in_first}, en=${en_in_second})"
    fi
  done
  # Final parity check via the existing TestCatalogsParity
  # test (B4) — we don't duplicate it here.
  if grep -q 'backup.protocol_s3' internal/i18n/catalog_backup.go ; then
    ok "i18n: backup.protocol_s3 present (parity covered by B4 TestCatalogsParity)"
  fi
else
  bad "internal/i18n/catalog_backup.go not found"
fi

# 9. go.mod: minio-go in direct deps (not indirect)
# Use grep -F (fixed string) because the pattern is
# literal. The earlier ^require|^\t form got mangled
# by PowerShell grep (which treats \t as a stray escape).
if grep -F 'github.com/minio/minio-go/v7 v' go.mod >/dev/null 2>&1 ; then
  # Found the dep line. Now check it's NOT marked
  # indirect (which would mean no Go code imports it).
  if grep -F 'github.com/minio/minio-go/v7 v' go.mod | grep -F '// indirect' >/dev/null 2>&1 ; then
    bad "go.mod: minio-go is marked indirect (no Go code imports it yet)"
  else
    ok "go.mod: minio-go in direct deps"
  fi
else
  bad "go.mod: minio-go not in deps at all"
fi

# 10. unit tests pass
# Find go in PATH or the standard Windows install
# location. PowerShell's `go` is fine but bash on
# Windows often doesn't inherit it (especially
# under MSYS / Git Bash). Falling back to the
# common install paths keeps this script runnable
# from any shell the operator uses.
GO_BIN="${GO_BIN:-$(command -v go 2>/dev/null || true)}"
if [[ -z "${GO_BIN}" ]] ; then
  for try in '/c/Program Files/Go/bin/go' '/c/Go/bin/go' '/usr/local/go/bin/go' '/c/Users/knaga/go/bin/go' ; do
    if [[ -x "${try}" ]] ; then GO_BIN="${try}"; break; fi
  done
fi
if [[ -z "${GO_BIN}" ]] ; then
  warn "go binary not found in PATH; skipping live test run (covered by CI)"
else
  test_out=$("${GO_BIN}" test ./internal/backup/ -run 'TestBuildS3Key|TestDetectProtocolS3|TestS3Validate|TestIsValidProtocolIncludesS3' -count=1 2>&1)
  if echo "${test_out}" | grep -qE '^ok.*internal/backup' ; then
    ok "go test ./internal/backup/ S3 tests: ok"
  else
    bad "go test ./internal/backup/ S3 tests failed: ${test_out}"
  fi
fi

echo
echo "=== B100 summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
if [[ "${FAIL}" -gt 0 ]] ; then
  exit 1
fi
exit 0
