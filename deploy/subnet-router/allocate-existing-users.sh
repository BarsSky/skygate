#!/bin/sh
# v0.24.0 — One-off: allocate per-user subnets for users that
# were created before the v0.20.0 auto-allocate feature (or
# for whom the auto-allocate path failed).
#
# Idempotent: re-running is a no-op for users that already
# have a subnet.
#
# What this does:
#   1. List all portal_users that don't have a user_subnets
#      row yet.
#   2. For each, INSERT into user_subnets with CIDR =
#      10.0.<user_id>.0/24, status='pending', router_hostname=
#      'skygate-subnet-<username>'.
#   3. UPDATE the denormalized columns on portal_users so
#      /my/devices / /admin/users pages see the new state
#      without a JOIN.
#
# Run on the skygate host:
#   ./deploy/subnet-router/allocate-existing-users.sh
#
# The script runs `docker exec skygate ...` under the hood
# because the SQLite DB lives inside the skygate container
# (docker volume skygate-data mounted at /data).

set -e

if ! command -v docker >/dev/null 2>&1; then
    echo "ERROR: docker CLI not found in PATH." >&2
    exit 1
fi
if ! docker ps --filter "name=skygate" --filter "status=running" --format '{{.Names}}' | grep -q skygate; then
    echo "ERROR: skygate container is not running." >&2
    exit 1
fi

# The skygate container is alpine and does not ship sqlite3
# in the image. We `apk add sqlite` once at the top of the
# script; the package is small (~1 MB) and the install is
# fast. The change is transient — a container restart wipes
# it, but the script finishes before that matters.
if ! docker exec skygate which sqlite3 >/dev/null 2>&1; then
    echo "Installing sqlite3 inside skygate container (one-off)..."
    docker exec skygate apk add --no-cache sqlite >/dev/null
fi

echo "Looking for users without a subnet..."
echo ""

# We use docker exec to read the DB. The DB is at /data/skygate.db
# inside the container; /data is a docker volume (skygate-data).
#
# Step 1: list candidates. We pass the SQL as a single -cmd
# argument to sqlite3; heredocs don't work cleanly through
# docker exec because of how stdin is wired.
candidates=$(docker exec skygate sqlite3 /data/skygate.db \
  "SELECT id || '|' || username FROM portal_users WHERE id NOT IN (SELECT user_id FROM user_subnets) AND id > 0 ORDER BY id;")

if [ -z "$candidates" ]; then
    echo "Nothing to do — all users have a subnet row."
    exit 0
fi

echo "Will allocate subnets for:"
echo "$candidates" | while IFS='|' read -r id username; do
    cidr="10.0.${id}.0/24"
    router_hostname="skygate-subnet-${username}"
    echo "  user_id=$id  username=$username  cidr=$cidr  router=$router_hostname"
done
echo ""

# Confirm with the operator. Non-interactive (deploy pipelines)
# can set ALLOCATE_NO_PROMPT=1 to skip.
if [ -z "$ALLOCATE_NO_PROMPT" ]; then
    printf "Proceed? [y/N] "
    read -r answer
    case "$answer" in
        y|Y|yes|YES) ;;
        *) echo "Aborted."; exit 1 ;;
    esac
fi

echo ""
echo "Allocating..."
echo "$candidates" | while IFS='|' read -r id username; do
    cidr="10.0.${id}.0/24"
    router_hostname="skygate-subnet-${username}"
    now=$(date +%s)

    # Step 2: INSERT into user_subnets. UNIQUE on user_id makes
    # this a no-op if a concurrent call got there first.
    docker exec skygate sqlite3 /data/skygate.db \
      "INSERT OR IGNORE INTO user_subnets (user_id, cidr, subnet_bits, control_plane_url, status, router_hostname, created_at, updated_at) VALUES ($id, '$cidr', 24, '', 'pending', '$router_hostname', $now, $now);"

    # Step 3: update the denormalized columns on portal_users.
    docker exec skygate sqlite3 /data/skygate.db \
      "UPDATE portal_users SET subnet_cidr = '$cidr', subnet_status = 'pending' WHERE id = $id;"

    echo "  user_id=$id  ok"
done

echo ""
echo "Done. Restart skygate so the sidecar picks up the new"
echo "rows on its next SyncOnce tick:"
echo ""
echo "  cd /home/skyadmin/skygate && docker compose up -d --force-recreate --no-deps skygate"
echo ""
echo "After restart, /admin/users/<id>/subnet will show the"
echo "new 'pending' status with a 'Issue preauth key' button."
