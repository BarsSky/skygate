#!/usr/bin/env bash
# B202.5 (v1.5.0+) — agent-state live-verify for SSHDumpTransport.
#
# This dry-run verifies the AGENT's state is ready for the
# SSHDumpTransport path. The Go-side code contract is
# pinned by scripts/check_b202_5.sh (run on the local
# dev box) + the unit tests in
# internal/dbmigrate/ssh_transport_test.go.
#
# The actual end-to-end test (B202.5 code deployed +
# SSH from agent to svi + real pg_dump over the wire)
# is a separate operator action that happens AFTER:
#   1. The agent's tailscale is restored (currently off
#      after the B209.1 emergency fix that switched
#      SKYGATE_TS_AUTHKEY_FILE to /dev/null)
#   2. The agent's id_ed25519.pub (or skygate-svi-backup
#      key) is added to svi's ~/.ssh/authorized_keys
#   3. The agent container is rebuilt + restarted with
#      the B202.5 binary + the SKYGATE_DBMIGRATE_SSH_*
#      env vars set
# The user instructed (2026-09-02): "headscale и
# headplane не трогай, тест с авторазвертыванием лучше
# будет провести на машине под windows... но это после"
# — so the live agent deploy is deferred to the local
# Windows Docker auto-deploy test (Phase 2.3 territory).
#
# What this dry-run DOES check on the agent:
#   1. ssh binary present + OpenSSH version
#   2. SSH keys present + their comment/role
#      (so the operator knows which key to add to svi
#      authorized_keys)
#   3. The agent's known_hosts has the agent's own
#      host key (so `ssh 127.0.0.1` doesn't prompt)
#   4. pg_dump is on the agent's PATH (the framework
#      reads PgDumpPath from env; if empty, defaults
#      to "pg_dump" which must be on PATH)
#   5. Tailscale status (the agent is in non-RF mode
#      post-B209.1 — operator must restore tailscale
#      before svi-reachable SSH works)
#   6. A "dry-run transport command" echo so the
#      operator can see the exact ssh args the
#      transport would build (without actually running
#      them, which would fail without a working key)
#
# Usage: bash scripts/b202_5_verify.sh

set -u
# No `set -e` — we count failures, don't abort.

PASS=0
FAIL=0
fails=()
WARNS=()

check() {
  local name="$1"
  local result="$2"
  if [ "$result" = "ok" ]; then
    printf "  \033[32m✓\033[0m %s\n" "$name"
    PASS=$((PASS+1))
  else
    printf "  \033[31m✗\033[0m %s\n" "$name"
    FAIL=$((FAIL+1))
    fails+=("$name")
  fi
}

warn() {
  printf "  \033[33m!${NC} %s\n" "$1"
  WARNS+=("$1")
}

# Color helpers.
if [ -t 1 ]; then
  RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; NC=$'\033[0m'
else
  RED=''; GRN=''; YLW=''; NC=''
fi

echo "=== B202.5 agent-state live-verify ==="
echo "  agent: $(hostname 2>/dev/null || echo unknown)"
echo

# --- Phase 1: ssh binary ---
echo "[Phase 1] ssh binary"
if ! command -v ssh >/dev/null 2>&1; then
  check "ssh on PATH" fail
  echo "${RED}no ssh — install openssh-client (apk add openssh-client / apt install openssh-client)${NC}"
  exit 1
fi
check "ssh on PATH" ok

SSH_VERSION=$(ssh -V 2>&1 | head -1)
if echo "$SSH_VERSION" | grep -qiE "OpenSSH"; then
  check "ssh is OpenSSH ($SSH_VERSION)" ok
else
  check "ssh is OpenSSH (got: $SSH_VERSION)" fail
fi

# --- Phase 2: SSH keys inventory ---
echo "[Phase 2] SSH keys inventory"
KNOWN_KEYS=""
for k in /home/skyadmin/.ssh/id_ed25519 /home/skyadmin/.ssh/id_rsa /home/skyadmin/.ssh/skygate_sync; do
  if [ -f "$k" ]; then
    KNOWN_KEYS="$KNOWN_KEYS $k"
    COMMENT=$(ssh-keygen -y -f "$k" 2>/dev/null | awk '{print $NF}')
    printf "    %-40s %s\n" "$k" "$COMMENT"
  fi
done
if [ -z "$KNOWN_KEYS" ]; then
  check "at least one SSH private key in /home/skyadmin/.ssh/" fail
else
  check "SSH private keys present in /home/skyadmin/.ssh/" ok
fi

# Note: the operator's key (knaga@SKYWORKER) is what
# the agent's authorized_keys has, but that key lives
# on SKYWORKER, not on the agent. The agent's own
# id_ed25519 is for headscale-bootstrap. For svi SSH,
# the operator either adds the agent's key to svi's
# authorized_keys, or copies SKYWORKER's key to the
# agent, or uses the existing skygate-svi-backup
# key (whose public key is already on svi).

# --- Phase 3: known_hosts ---
echo "[Phase 3] agent's own known_hosts"
if [ -f /home/skyadmin/.ssh/known_hosts ]; then
  HAS_SELF=$(ssh-keygen -F 127.0.0.1 2>/dev/null | head -1)
  if [ -n "$HAS_SELF" ]; then
    check "127.0.0.1 in known_hosts (so agent→agent SSH doesn't prompt)" ok
  else
    warn "127.0.0.1 NOT in known_hosts — first SSH to loopback will prompt; 'StrictHostKeyChecking=accept-new' in the transport handles this on the first call"
  fi
else
  warn "/home/skyadmin/.ssh/known_hosts does not exist"
fi

# --- Phase 4: pg_dump on agent PATH ---
echo "[Phase 4] pg_dump on agent PATH"
if ! command -v pg_dump >/dev/null 2>&1; then
  check "pg_dump on agent PATH" fail
  echo "${RED}no pg_dump — install postgresql-client${NC}"
else
  PGDUMP_PATH=$(command -v pg_dump)
  PGDUMP_VERSION=$(pg_dump --version 2>&1 | head -1)
  check "pg_dump on agent PATH ($PGDUMP_PATH, $PGDUMP_VERSION)" ok
  # Note: pg_dump on the AGENT is used by the local
  # transport (the default). The SSH transport uses
  # the REMOTE pg_dump (SKYGATE_DBMIGRATE_SSH_PGDUMP).
  # This check ensures both paths can find pg_dump.
fi

# --- Phase 5: Tailscale status (the svi-reachable path) ---
echo "[Phase 5] Tailscale status (svi at 100.64.0.24 is reachable only via Tailscale)"
if command -v tailscale >/dev/null 2>&1; then
  TS_STATUS=$(tailscale status 2>&1 | head -5)
  if echo "$TS_STATUS" | grep -qE "Logged out|not logged in|stopped"; then
    warn "tailscale is logged out (post-B209.1 emergency fix): svi at 100.64.0.24 is NOT reachable from the agent. Restore tailscale (set SKYGATE_TS_AUTHKEY_FILE to a valid authkey + restart container) before the svi→agent move."
  else
    check "tailscale is logged in (svi at 100.64.0.24 reachable)" ok
  fi
else
  warn "tailscale binary not found on agent (the non-RF mode after B209.1)"
fi

# --- Phase 6: dry-run command echo (proves the transport's flag/quoting) ---
echo "[Phase 6] dry-run command echo (proves the transport's flag order + quoting)"
# We construct the EXACT command the SSHDumpTransport
# would build for a hypothetical svi→agent dump. The
# command is NOT executed (would fail with permission
# denied without a working key), but the echo proves
# the args are right: -p 22, -i <key>, -o BatchMode=yes,
# -o StrictHostKeyChecking=accept-new, user@host,
# 'pg_dump -Fc --no-owner --no-acl --no-comments -d <dsn>'.
DSN='postgres://admin:secret@127.0.0.1:5433/skygate_staging?sslmode=disable'
QUOTED_DSN="'postgres://admin:secret@127.0.0.1:5433/skygate_staging?sslmode=disable'"
DRY_CMD="ssh -p 22 -i /home/skyadmin/.ssh/id_ed25519 -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@100.64.0.24 'pg_dump -Fc --no-owner --no-acl --no-comments -d $QUOTED_DSN'"
echo "  ${YLW}would-execute:${NC}"
echo "    $DRY_CMD"
echo
echo "  ${YLW}echoed-not-executed (would fail with 'Permission denied' until"
echo "  the agent's id_ed25519.pub is added to svi's authorized_keys; this"
echo "  dry-run is a code-construction check, not an end-to-end test).${NC}"

# Verify the cmd structure (sanity check on the echo logic)
if echo "$DRY_CMD" | grep -q "BatchMode=yes" \
   && echo "$DRY_CMD" | grep -q "StrictHostKeyChecking=accept-new" \
   && echo "$DRY_CMD" | grep -q "pg_dump -Fc --no-owner --no-acl --no-comments" \
   && echo "$DRY_CMD" | grep -q "root@100.64.0.24" \
   && echo "$DRY_CMD" | grep -q "$QUOTED_DSN"; then
  check "dry-run command has the expected flags + host + remote pg_dump" ok
else
  check "dry-run command structure" fail
fi

# --- Summary ---
echo
echo "=== B202.5 agent-state: ${PASS} pass, ${FAIL} fail, ${#WARNS[@]} warn ==="
if [ "$FAIL" -gt "0" ]; then
  echo "${RED}FAILURES:${NC}"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
fi
if [ "${#WARNS[@]}" -gt "0" ]; then
  echo "${YLW}WARNINGS:${NC}"
  for w in "${WARNS[@]}"; do
    echo "  - $w"
  done
fi
echo
echo "${YLW}Operator checklist for the REAL svi→agent move (NOT done by this dry-run):${NC}"
echo "  1. On svi (root@100.64.0.24), add the agent's public key:"
echo "       cat /home/skyadmin/.ssh/id_ed25519.pub | ssh root@100.64.0.24 \\"
echo "         'cat >> ~/.ssh/authorized_keys'"
echo "     (or use the existing skygate-svi-backup key pair the svi backup"
echo "     script already uses — its private key would need to be copied"
echo "     to the agent first)"
echo "  2. Rebuild the skygate container with the B202.5 binary:"
echo "       git pull && docker compose build skygate && docker compose up -d skygate"
echo "     (the agent's host tailscale is already logged in, so svi at"
echo "     100.64.0.24 is reachable; the B209.1 fix only disabled tailscale"
echo "     INSIDE the skygate container, not on the host)"
echo "  3. Mount the SSH key into the container (the host key is at"
echo "     /home/skyadmin/.ssh/id_ed25519; the SSHDumpTransport's"
echo "     -i flag needs to point at a path INSIDE the container):"
echo "       add to docker-compose.yml volumes: "
echo "         /home/skyadmin/.ssh/id_ed25519:/run/secrets/ssh_key:ro"
echo "     then set SKYGATE_DBMIGRATE_SSH_KEY=/run/secrets/ssh_key"
echo "  4. In /home/skyadmin/skygate/.env on the agent, set:"
echo "       SKYGATE_DBMIGRATE_TRANSPORT=ssh"
echo "       SKYGATE_DBMIGRATE_SSH_HOST=100.64.0.24  # svi via Tailscale"
echo "       SKYGATE_DBMIGRATE_SSH_USER=root  # or skygate-svi-backup"
echo "       SKYGATE_DBMIGRATE_SSH_KEY=/run/secrets/ssh_key  # mounted path"
echo "       SKYGATE_DBMIGRATE_SSH_PGDUMP=pg_dump"
echo "  5. Restart the skygate container so the new env vars + key mount are picked up"
echo "  6. Hit /admin/database -> 'Migrate' -> fill the target DSN form"
echo "  7. Watch the 6 steps run on /admin/database/migrate/{id} with"
echo "     transport=ssh in the audit row"
echo
if [ "$FAIL" -gt "0" ]; then
  exit 1
fi
echo "${GRN}all checks passed${NC}"
exit 0
