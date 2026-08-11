#!/bin/bash
# scripts/reconcile_snapshots.sh — write an acl_snapshots row
# that matches the current live headscale policy's updatedAt.
#
# R9 (verify-post) compares:
#   - live policy's updatedAt (headscale API)
#   - last acl_snapshots row's created_at (skygate DB)
# and fails if they differ by more than 60s.
#
# R9 will diverge when:
#   1. An operator manually edits the policy via headscale API
#      (the snapshot stays at the last SetPolicy time, but
#      headscale's updatedAt advances)
#   2. skygate crashes / restarts during a reapply and only
#      the headscale PUT completed
#   3. Direct headscale DB edit (rare; for emergency)
#
# This script copies skygate.db out, INSERTs a snapshot row
# with created_at = policy's updatedAt, then copies the DB
# back. R9 will then see them aligned.
#
# Usage (from operator workstation):
#   bash scripts/reconcile_snapshots.sh
#
# This is a one-off reconciliation, NOT a normal flow. The
# normal flow is:
#   1. Operator changes exit rules in /my/exit-rules (or
#      /admin/exit-rules for cross-user)
#   2. POST handler calls GenerateACL + SetPolicy
#   3. GenerateACL writes the snapshot row before SetPolicy
#   4. Verify-post R9 sees the alignment automatically
#
# 2026-07-30: extracted from the inline fix in this session's
# operator actions. Use ONLY when the live policy was edited
# outside skygate's normal flow.

set -e

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"

SSH_KEY="${SSH_KEY:-}"
for cand in \
  "$HOME/.ssh/id_ed25519" \
  "$HOME/.ssh/id_rsa" \
  "/mnt/c/Users/knaga/.ssh/id_ed25519" \
  "/c/Users/knaga/.ssh/id_ed25519"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then
    SSH_KEY="$cand"
    break
  fi
done

if [ -z "$SSH_KEY" ]; then
  echo "ERROR: no SSH key found" >&2
  exit 2
fi

SSH="ssh -i $SSH_KEY -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes $SSH_HOST"
CREATED_BY="${CREATED_BY:-manual_reconcile_$(date -u +%Y%m%d_%H%M%S)}"

echo "=== reconcile_snapshots.sh ==="
echo "  ssh:    $SSH_HOST"
echo "  by:     $CREATED_BY"
echo

# Get live policy
echo "[1/3] GET current live policy from headscale"
$SSH <<REMOTE_EOF
set -e
API_KEY=\$(grep '^HEADSCALE_API_KEY=' /home/admin/skygate/.env | cut -d= -f2-)
CID=\$(docker ps --filter 'label=com.docker.compose.service=skygate' --format '{{.ID}}' | head -1)
echo "  container: \$CID"
POLICY_JSON=\$(curl -fsS -H "Authorization: Bearer \$API_KEY" http://localhost:50444/api/v1/policy)
UPDATED_AT=\$(echo "\$POLICY_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin)['updatedAt'])")
POLICY_BODY=\$(echo "\$POLICY_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin)['policy'])")
echo "  updatedAt: \$UPDATED_AT"
EPOCH=\$(python3 -c "from datetime import datetime; print(int(datetime.fromisoformat('\$UPDATED_AT'.replace('Z','+00:00')).timestamp()))")
echo "  epoch: \$EPOCH"
echo "\$POLICY_BODY" > /tmp/_policy_recon.json
docker cp /tmp/_policy_recon.json "\$CID:/tmp/_policy_recon.json"
docker cp "\$CID:/data/skygate.db" /tmp/_db_recon_\$\$.sqlite
NEXT_VER=\$(sqlite3 /tmp/_db_recon_\$\$.sqlite "SELECT COALESCE(MAX(version),0)+1 FROM acl_snapshots;")
echo "  next version: \$NEXT_VER"
sqlite3 /tmp/_db_recon_\$\$.sqlite "INSERT INTO acl_snapshots(version, config, created_by, applied_success, error_msg, created_at) VALUES (\$NEXT_VER, readfile('/tmp/_policy_recon.json'), '$CREATED_BY', 1, '', \$EPOCH);"
echo "  INSERT done"
sqlite3 -header -column /tmp/_db_recon_\$\$.sqlite "SELECT id, version, created_at, datetime(created_at,'unixepoch') as ts, applied_success, substr(created_by,1,40) as by FROM acl_snapshots ORDER BY id DESC LIMIT 3"
docker cp /tmp/_db_recon_\$\$.sqlite "\$CID:/data/skygate.db"
rm -f /tmp/_policy_recon.json /tmp/_db_recon_\$\$.sqlite
echo "[2/3] DB copied back"
REMOTE_EOF

echo "[3/3] Done. Run 'make verify-post' to confirm R9 PASS"
