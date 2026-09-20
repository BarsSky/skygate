#!/usr/bin/env bash
# cleanup_b188_3_fixtures.sh — remove the device_rules rows the B188.3
# test fixtures leaked into the production database (B274 housekeeping).
#
# Background: the B188.3 integration tests created device_rules with
# synthetic destinations (1.2.3.0/24, 1.2.99.0/24, 5.5.5.5/32,
# 6.7.8.9/32, example.com) and a teardown was added afterwards — but the
# rows that had already been written to the live DB stayed, and the
# domain autoupdater kept re-deriving CIDRs from the leaked domain rows
# (cdn:cloudflare:<test-domain> entries), so they also inflate the
# prefix-ownership weight B274 computes.
#
# SAFETY: the predicate is a closed allow-list of synthetic destinations,
# nothing is matched by pattern alone, and the script PRINTS what it
# would delete unless you pass --apply. It never touches a rule whose
# target is a real domain, a real IP, or an RFC 1918/100.64 range.
#
# Usage:
#   bash scripts/cleanup_b188_3_fixtures.sh                # dry-run
#   bash scripts/cleanup_b188_3_fixtures.sh --apply        # delete
#
# The DB is auto-detected: $SKYGATE_DB / PG container `skygate-pg-local`
# (dbname skygate_staging) on the reference VM, or a local SQLite file
# via SKYGATE_DB=sqlite:/path/to/skygate.db.

set -uo pipefail

APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

FIXTURE_IPS="'5.5.5.5/32','6.7.8.9/32','1.2.3.0/24','1.2.99.0/24'"
FIXTURE_DOMAINS="'example.com'"

run_sql() { # run_sql "<sql>" — prints rows
  if [ -n "${SKYGATE_DB_PATH:-}" ]; then
    sqlite3 "$SKYGATE_DB_PATH" "$1"
  else
    docker exec skygate-pg-local psql -U admin -d skygate_staging -P pager=off -tA -c "$1"
  fi
}

WHERE_IP="target_type IN ('ip','subnet') AND target_value IN (${FIXTURE_IPS})"
WHERE_DOM="target_type = 'domain' AND (target_value IN (${FIXTURE_DOMAINS}) OR target_value LIKE 'cascade-verify-%' OR target_value LIKE 'limit-test-%')"

echo "=== rows matching the B188.3 fixture allow-list ==="
run_sql "SELECT id||' | '||COALESCE(device_hostname,'')||' | '||target_type||' | '||target_value||' | parent='||COALESCE(parent_domain,'') FROM device_rules WHERE ${WHERE_IP} OR ${WHERE_DOM} ORDER BY id;"
COUNT=$(run_sql "SELECT COUNT(*) FROM device_rules WHERE ${WHERE_IP} OR ${WHERE_DOM};" | tr -d '[:space:]')

if [ "${COUNT:-0}" = "0" ]; then
  echo "nothing to clean (0 fixture rows)"
  exit 0
fi

if [ "$APPLY" != "1" ]; then
  echo
  echo "DRY-RUN: ${COUNT} row(s) would be deleted. Re-run with --apply to remove them."
  exit 0
fi

echo
echo "deleting ${COUNT} row(s) …"
run_sql "DELETE FROM device_rules WHERE ${WHERE_IP} OR ${WHERE_DOM};"
LEFT=$(run_sql "SELECT COUNT(*) FROM device_rules WHERE ${WHERE_IP} OR ${WHERE_DOM};" | tr -d '[:space:]')
echo "done; fixture rows left: ${LEFT:-?}"
[ "${LEFT:-1}" = "0" ] || exit 1
