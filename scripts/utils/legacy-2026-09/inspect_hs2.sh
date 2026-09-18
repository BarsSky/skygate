#!/usr/bin/env bash
set +e
# Deep inspect headscale DB
ssh skyadmin@192.168.13.69 '
echo "== all tables =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite ".tables" 2>&1
echo
echo "== node 45 full row =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT * FROM nodes WHERE id=45;" 2>&1
echo
echo "== user 2147455555 (does it exist?) =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT * FROM users WHERE id=2147455555;" 2>&1
echo
echo "== user 85 (infra) =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT * FROM users WHERE id=85;" 2>&1
echo
echo "== users count =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT COUNT(*) FROM users;" 2>&1
echo
echo "== check if there is a view or trigger =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT name, type FROM sqlite_master WHERE type IN (\"view\", \"trigger\");" 2>&1
' 2>&1 | head -50
