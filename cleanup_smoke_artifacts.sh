#!/bin/bash
# 2026-07-20: cleanup leftover smoke test artifacts on the VM.
#
# After multiple smoke runs, skygate accumulates:
#   - test users (smoke_mesh_<pid>) in portal_users
#   - test meshes (smoke-mesh-<pid>) in meshes
#
# The smoke.sh step 13.8 cleanup runs
# /admin/users/{id}/delete which cascades to FK tables
# (user_subnets, shares, mesh_members) AND to the
# headscale user. But early smoke runs (before the
# id-extraction fix in bb61459) left users behind.
# This script cleans those up retroactively.
#
# What it does:
#   1. For each smoke_mesh_* user in skygate:
#      - extract the numeric id from /admin/users HTML
#      - POST /admin/users/{id}/delete
#      - verify the user is gone from both skygate and
#        headscale
#   2. For each smoke-mesh-* mesh:
#      - set status='dissolved' (the v0.22.0 audit design:
#        dissolved meshes are KEPT for audit history)
#   3. Final state check: count leftover rows.
set -uo pipefail

cd /home/skyadmin/skygate

HS_PASS=$(grep -E "^SKYGATE_ADMIN_PASS=" .env | cut -d= -f2)
CK=/tmp/cleanup_ck
rm -f "$CK"
docker exec skygate apk add sqlite >/dev/null 2>&1

echo "=== 0. Pre-cleanup state ==="
echo "  skygate portal_users matching 'smoke%':"
docker exec skygate sqlite3 -header -column /data/skygate.db \
  "SELECT id, username, headscale_user_id FROM portal_users WHERE username LIKE 'smoke%';"
echo "  skygate meshes matching 'smoke%':"
docker exec skygate sqlite3 -header -column /data/skygate.db \
  "SELECT id, code, name, status FROM meshes WHERE name LIKE 'smoke%';"
echo "  headscale users matching 'smoke%':"
docker exec headscale headscale users list 2>/dev/null | grep "smoke" || echo "  (none)"
echo ""

echo "=== 1. Login as admin ==="
curl -sS -c "$CK" -b "$CK" -X POST \
  --data-urlencode "username=skyadmin" --data-urlencode "password=${HS_PASS}" \
  http://localhost:8080/login -o /dev/null

echo "=== 2. Delete leftover smoke_mesh_* users (skygate + headscale cascade) ==="
ADMIN_USERS_HTML=$(curl -sS -b "$CK" "http://localhost:8080/admin/users")
# For each smoke_mesh_* row in skygate, extract the id and
# call /admin/users/{id}/delete. The /admin/users HTML
# renders one row per user with the action URLs
# /admin/users/{id}/subnet, /admin/users/{id}/delete, etc.
SKY_USERS=$(docker exec skygate sqlite3 /data/skygate.db \
  "SELECT id, username, headscale_user_id FROM portal_users WHERE username LIKE 'smoke%';")
DELETED=0
FAILED=0
while IFS='|' read -r uid uname hsid; do
  [ -z "$uid" ] && continue
  # Extract the delete URL for this user from the admin
  # /admin/users HTML. The row's structure is:
  #   <form action="/admin/users/{id}/delete" ...>
  USER_HTML=$(echo "$ADMIN_USERS_HTML" | grep -A 50 "$uname" | head -50)
  DEL_URL=$(echo "$USER_HTML" | grep -oE "/admin/users/${uid}/delete" | head -1)
  if [ -z "$DEL_URL" ]; then
    # The user might not be in the admin HTML anymore (e.g.
    # already deleted by a previous cleanup pass). Skip.
    echo "  [skip] $uname: no delete URL in /admin/users HTML (id=$uid)"
    continue
  fi
  HTTP=$(curl -sS -b "$CK" -X POST -o /dev/null -w "%{http_code}" \
    "http://localhost:8080${DEL_URL}")
  if [ "$HTTP" = "302" ]; then
    echo "  [ok]   $uname (skygate id=$uid, headscale id=$hsid): HTTP $HTTP"
    DELETED=$((DELETED + 1))
  else
    echo "  [fail] $uname: HTTP $HTTP"
    FAILED=$((FAILED + 1))
  fi
done <<< "$SKY_USERS"
echo ""
echo "  deleted: $DELETED, failed: $FAILED"

echo ""
echo "=== 3. Dissolve leftover smoke-mesh-* meshes (v0.22.0 design: keep for audit) ==="
# Mark them dissolved so the next ACL re-render drops
# them from the per-user dst list (the meshes.status='active'
# filter excludes dissolved meshes). We don't DELETE the
# rows because the v0.22.0 design is "Status filter, not
# DELETE" — dissolved rows stay for audit.
MESH_IDS=$(docker exec skygate sqlite3 /data/skygate.db \
  "SELECT id FROM meshes WHERE name LIKE 'smoke-mesh-%';")
DISSOLVED=0
for mid in $MESH_IDS; do
  if [ -n "$mid" ]; then
    docker exec skygate sqlite3 /data/skygate.db \
      "UPDATE meshes SET status='dissolved', dissolved_at=$(date +%s) WHERE id=$mid;" >/dev/null
    DISSOLVED=$((DISSOLVED + 1))
  fi
done
echo "  dissolved: $DISSOLVED"

echo ""
echo "=== 4. Belt-and-braces: clear mesh_members for the dissolved meshes ==="
# mesh_members rows are FK CASCADE-deleted when the mesh
# is DELETEd, but UPDATE-only (dissolve) doesn't trigger
# CASCADE. Most are already empty (smoke users left), but
# be explicit.
docker exec skygate sqlite3 /data/skygate.db \
  "DELETE FROM mesh_members WHERE mesh_id IN (SELECT id FROM meshes WHERE name LIKE 'smoke-mesh-%');"
echo "  mesh_members for smoke-mesh-* cleared"

echo ""
echo "=== 5. Post-cleanup state ==="
echo "  skygate portal_users matching 'smoke%':"
docker exec skygate sqlite3 -header -column /data/skygate.db \
  "SELECT id, username, headscale_user_id FROM portal_users WHERE username LIKE 'smoke%';"
echo "  skygate meshes matching 'smoke%':"
docker exec skygate sqlite3 -header -column /data/skygate.db \
  "SELECT id, code, name, status FROM meshes WHERE name LIKE 'smoke%';"
echo "  headscale users matching 'smoke%':"
docker exec headscale headscale users list 2>/dev/null | grep "smoke" || echo "  (none)"

echo ""
echo "=== 6. Sanity: re-apply ACL so headscale sees the dissolved meshes are gone ==="
curl -sS -b "$CK" -X POST -d "" \
  http://localhost:8080/admin/exit-rules/reapply -o /dev/null
sleep 2
echo "  re-apply: done"

echo ""
echo "=== 7. Final smoke test (should still be 83/83) ==="
make test 2>&1 | grep -E "SUMMARY|smoke done" | head -5
