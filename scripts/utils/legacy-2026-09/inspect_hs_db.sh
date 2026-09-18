#!/usr/bin/env bash
set +e
# Inspect headscale SQLite DB to find the right tables/columns
# for moving node 45 to user 'infra'
ssh skyadmin@192.168.13.69 '
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite ".tables" 2>&1
echo
echo "== users =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT id, name, display_name FROM users;" 2>&1
echo
echo "== node 45 =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT id, given_name, user_id, last_seen FROM nodes WHERE id=45;" 2>&1
echo
echo "== schema for nodes =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite ".schema nodes" 2>&1
' 2>&1
