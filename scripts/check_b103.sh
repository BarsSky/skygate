#!/usr/bin/env bash
#===============================================================================
# B103 (v1.3.8): in-app S3 download (BL-18)
#
# Background
# ----------
# Pre-v1.3.8 the /admin/backup page had a "Download" link
# that streamed the archive from the local BACKUP_DIR.
# For S3 backups (the v1.3.8 addition), the file lives
# in the bucket — not on the local filesystem — so the
# download link was a dead button. The operator had to
# `aws s3 cp` or `mc cp` to fetch the file, then re-upload
# to /admin/backup/restore. v1.3.8 (BL-18) closes that
# loop with a new "Download from S3" button + handler.
#
# B103 pins:
#   1. The handler GetAdminBackupDownloadS3 is defined
#      in the admin service.
#   2. The route GET /admin/backup/download-s3 is
#      registered in main.go.
#   3. The template renders a "Download from S3" button
#      when LastArchive starts with "s3://".
#   4. The new template func "hasPrefix" is registered
#      in templates.go.
#   5. The exported NewS3ClientForConfig helper is
#      available in internal/backup.
#   6. i18n key "backup.download_from_s3" exists in
#      both ru + en (B4 parity).
#   7. The audit log row for s3 download is wired.
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
echo "=== B103 v1.3.8: in-app S3 download (BL-18) ==="
echo

# 1. Handler function present
if grep -qE 'func \(s \*Service\) GetAdminBackupDownloadS3' internal/feature/admin/backup.go ; then
  ok "backup.go: GetAdminBackupDownloadS3 handler defined"
else
  bad "backup.go: GetAdminBackupDownloadS3 handler missing"
fi

# 2. Route registered in main.go
if grep -qE '"GET /admin/backup/download-s3"' cmd/skygate/main.go ; then
  ok "main.go: GET /admin/backup/download-s3 route registered"
else
  bad "main.go: download-s3 route missing"
fi

# 3. Template has the button
if grep -qF 'Download from S3' internal/handlers/templates/admin/backup.html \
   || grep -qF 'download_from_s3' internal/handlers/templates/admin/backup.html ; then
  ok "backup.html: 'Download from S3' button rendered for s3:// archives"
else
  bad "backup.html: button missing"
fi

# 4. hasPrefix template func registered
if grep -qE '"hasPrefix"' internal/handlers/templates.go ; then
  ok "templates.go: hasPrefix func registered"
else
  bad "templates.go: hasPrefix func missing"
fi

# 5. hasPrefix is used in the template
if grep -qF 'hasPrefix .Config.LastArchive "s3://"' internal/handlers/templates/admin/backup.html ; then
  ok "backup.html: hasPrefix guard for s3:// prefix"
else
  bad "backup.html: hasPrefix guard missing"
fi

# 6. Exported NewS3ClientForConfig in s3.go
if grep -qE '^func NewS3ClientForConfig' internal/backup/s3.go ; then
  ok "s3.go: NewS3ClientForConfig exported"
else
  bad "s3.go: NewS3ClientForConfig not exported"
fi

# 7. i18n keys present in both catalogs (B4 parity)
for lang_dir in '' ; do
  for key in backup.download_from_s3 backup.download_s3_failed ; do
    if grep -qF "\"${key}\"" internal/i18n/catalog_backup.go ; then
      ok "i18n: key ${key} present (ru + en parity covered by B4)"
    else
      bad "i18n: key ${key} missing"
    fi
  done
done

# 8. Audit log row written
if grep -qF 'backup.download_s3' internal/feature/admin/backup.go ; then
  ok "backup.go: audit log row wired"
else
  bad "backup.go: audit log row missing"
fi

# 9. The handler uses StatObject + GetObject (so the
# client gets correct Content-Length / Content-Type)
if grep -qE 'StatObject\(' internal/feature/admin/backup.go \
   && grep -qE 'GetObject\(' internal/feature/admin/backup.go ; then
  ok "backup.go: StatObject + GetObject streaming"
else
  bad "backup.go: StatObject or GetObject missing"
fi

# 10. Content-Disposition header (so the browser
# saves the file with the right name)
if grep -qE 'Content-Disposition' internal/feature/admin/backup.go ; then
  ok "backup.go: Content-Disposition header set"
else
  bad "backup.go: Content-Disposition header missing"
fi

# 11. minio import in backup.go (so the handler can
# actually call StatObject / GetObject)
if grep -qF 'github.com/minio/minio-go/v7' internal/feature/admin/backup.go ; then
  ok "backup.go: imports minio-go/v7"
else
  bad "backup.go: minio-go import missing"
fi

# 12. Build check (the new code compiles cleanly)
GO_BIN="${GO_BIN:-$(command -v go 2>/dev/null || true)}"
if [[ -z "${GO_BIN}" ]] ; then
  for try in '/c/Program Files/Go/bin/go' '/c/Go/bin/go' '/usr/local/go/bin/go' ; do
    [[ -x "${try}" ]] && GO_BIN="${try}" && break
  done
fi
if [[ -n "${GO_BIN}" ]] ; then
  if "${GO_BIN}" build ./cmd/skygate 2>&1 | grep -qE 'error' ; then
    bad "go build ./cmd/skygate: compile errors"
  else
    ok "go build ./cmd/skygate: clean"
  fi
else
  warn "go binary not found in PATH; skipping live compile check (covered by CI)"
fi

echo
echo "=== B103 summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
if [[ "${FAIL}" -gt 0 ]] ; then
  exit 1
fi
exit 0
