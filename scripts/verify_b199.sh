#!/bin/sh
# verify_b199.sh — live post-deploy verification.
# Run from the agent with a valid admin session cookie.
# Note: no `set -e` because the grep checks intentionally
# use 0-counts to confirm "this text is NOT in the page"
# (e.g. no template error). Without set -e the script
# runs to completion and reports everything.

COOKIE="${SKYGATE_COOKIE:?set SKYGATE_COOKIE}"
BASE="http://127.0.0.1:8080"

echo "=== healthz ==="
curl -s -o /dev/null -w "  GET /healthz = %{http_code}\n" $BASE/healthz

echo "=== admin/cluster ==="
curl -s -b "skygate_session=$COOKIE" -o /tmp/cluster.html -w "  GET /admin/cluster = %{http_code}, %{size_download} bytes\n" $BASE/admin/cluster
echo "  Card sections:"; grep -oE 'class="card"' /tmp/cluster.html | wc -l
echo "  Section titles:"; grep -oE 'Сводка по кластеру|Ноды кластера|База данных кластера|Ожидающие инвайты|cluster-событий' /tmp/cluster.html | sort -u
echo "  Sidebar cluster link present:"; grep -c 'href="/admin/cluster"' /tmp/cluster.html
echo "  Self row marker (empty state, expected 0):"; grep -c 'tag tag-self' /tmp/cluster.html
echo "  Phase note present:"; grep -c 'Phase 2.1:' /tmp/cluster.html

echo "=== admin/database (B198.1 regression fix) ==="
curl -s -b "skygate_session=$COOKIE" -o /tmp/db.html -w "  GET /admin/database = %{http_code}, %{size_download} bytes\n" $BASE/admin/database
echo "  Card sections:"; grep -oE 'card-header' /tmp/db.html | wc -l
echo "  No 'no such template' error:"; grep -c 'no such template' /tmp/db.html
echo "  No 'render: template' error:"; grep -c 'render: template' /tmp/db.html
echo "  Section titles present:"; grep -oE 'Текущая БД|Желаемая БД|Проверить|Сохранить|Recent migrations' /tmp/db.html | sort -u

echo "=== admin/database/migrate (the recent-runs list page) ==="
curl -s -b "skygate_session=$COOKIE" -o /tmp/mig.html -w "  GET /admin/database/migrate = %{http_code}, %{size_download} bytes\n" $BASE/admin/database/migrate
echo "  No 'no such template' error:"; grep -c 'no such template' /tmp/mig.html
echo "  Has 'Recent migrations' header:"; grep -c 'Recent migrations' /tmp/mig.html

echo "=== sidebar shows all the cluster pages ==="
echo "  /admin/cluster:"; grep -c 'href="/admin/cluster"' /tmp/cluster.html
echo "  /admin/ha:"; grep -c 'href="/admin/ha"' /tmp/cluster.html
echo "  /admin/deploy:"; grep -c 'href="/admin/deploy"' /tmp/cluster.html
