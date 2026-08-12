#!/usr/bin/env bash
#===============================================================================
# B102 (v1.3.8): backup Dockerfile includes mount helpers (BL-16)
#
# Background: the v1.3.8 BL-16 per-protocol e2e test
# showed that the in-app backup (which runs inside the
# skygate container via `bash scripts/backup.sh`) could
# NOT mount SMB / NFS / SFTP shares because the
# Dockerfile's apk add list did not include
# cifs-utils / nfs-utils / sshfs. The mount commands
# in internal/backup/mount.go (mountSMB, mountNFS,
# mountSFTP) would have failed with "executable file
# not found in $PATH" for any non-local / non-S3
# protocol. v1.3.8's Dockerfile fix adds the 3
# packages so the code paths in mount.go are actually
# exercisable.
#
# B102 pins:
#   - cifs-utils in Dockerfile (enables mount.cifs for SMB)
#   - nfs-utils in Dockerfile (enables mount.nfs for NFS)
#   - sshfs in Dockerfile (enables sshfs for SFTP)
#   - scripts/test_backup_protocols.sh exists and runs
#     without errors on the current code
#   - scripts/test_backup_protocols.sh mentions all 5
#     protocols (local, s3, smb, nfs, sftp)
#
# Defense in depth: if a future apk-cleanup pass removes
# one of these packages, B102 fails before the change
# can ship and break the SMB / NFS / SFTP backup paths.
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
echo "=== B102 v1.3.8: backup Dockerfile includes mount helpers (BL-16) ==="
echo

# 1. cifs-utils in Dockerfile apk add
if grep -qE '^\s+cifs-utils\s*\\?\s*$' Dockerfile ; then
  ok "Dockerfile: cifs-utils in apk add list (enables mount.cifs for SMB)"
else
  bad "Dockerfile: cifs-utils missing — SMB backup would fail at mount"
fi

# 2. nfs-utils in Dockerfile apk add
if grep -qE '^\s+nfs-utils\s*\\?\s*$' Dockerfile ; then
  ok "Dockerfile: nfs-utils in apk add list (enables mount.nfs for NFS)"
else
  bad "Dockerfile: nfs-utils missing — NFS backup would fail at mount"
fi

# 3. sshfs in Dockerfile apk add
if grep -qE '^\s+sshfs\s*\\?\s*$' Dockerfile ; then
  ok "Dockerfile: sshfs in apk add list (enables sshfs for SFTP)"
else
  bad "Dockerfile: sshfs missing — SFTP backup would fail at mount"
fi

# 4. test_backup_protocols.sh exists
if [[ -f scripts/test_backup_protocols.sh ]] ; then
  ok "scripts/test_backup_protocols.sh exists"
else
  bad "scripts/test_backup_protocols.sh missing"
fi

# 5. test script mentions all 5 protocols. Use a
# simple fixed-string grep — earlier regex attempt
# hit PowerShell's paren-escape issues.
for proto in local s3 smb nfs sftp ; do
  if grep -qF "${proto}" scripts/test_backup_protocols.sh ; then
    ok "test_backup_protocols.sh: ${proto} mentioned"
  else
    bad "test_backup_protocols.sh: ${proto} not mentioned"
  fi
done

# 6. test script has the function for each protocol
for fn in test_local test_s3 test_mount_protocol_config ; do
  if grep -qE "^${fn}\(\)" scripts/test_backup_protocols.sh ; then
    ok "test_backup_protocols.sh: ${fn}() function present"
  else
    bad "test_backup_protocols.sh: ${fn}() function missing"
  fi
done

# 7. test script has a case statement dispatching on protocol
if grep -qE 'case "\$\{1:-all\}"' scripts/test_backup_protocols.sh ; then
  ok "test_backup_protocols.sh: case dispatcher present"
else
  bad "test_backup_protocols.sh: case dispatcher missing"
fi

# 8. bash syntax check on the test script
if bash -n scripts/test_backup_protocols.sh 2>/dev/null ; then
  ok "test_backup_protocols.sh: bash -n syntax check passes"
else
  bad "test_backup_protocols.sh: bash syntax error"
  bash -n scripts/test_backup_protocols.sh
fi

# 9. The mount-based protocols are in AllProtocols
for proto in smb nfs sftp ; do
  if grep -qE "Protocol${proto^^}" internal/backup/config.go ; then
    ok "config.go: Protocol${proto^^} in AllProtocols"
  else
    bad "config.go: Protocol${proto^^} not in AllProtocols"
  fi
done

# 10. The mount-based protocols are in mount.go's switch
for proto in smb nfs sftp ; do
  if grep -qE "case Protocol${proto^^}:" internal/backup/mount.go ; then
    ok "mount.go: case Protocol${proto^^} in Mount() switch"
  else
    bad "mount.go: case Protocol${proto^^} missing from Mount()"
  fi
done

# 11. Mount() has an early return for local (so S3's no-op
# is the only mount-skip case besides local)
if grep -qE "if c\.Protocol == ProtocolLocal " internal/backup/mount.go ; then
  ok "mount.go: ProtocolLocal early-return in Mount()"
else
  bad "mount.go: ProtocolLocal early-return missing"
fi

echo
echo "=== B102 summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
if [[ "${FAIL}" -gt 0 ]] ; then
  exit 1
fi
exit 0
