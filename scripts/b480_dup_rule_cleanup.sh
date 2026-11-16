#!/usr/bin/env bash
# ============================================================================
# b480_dup_rule_cleanup.sh — B480 (2026-11-16)
#
# ONE RULE PER TARGET. Before B480 the dedup key included the relay, so the same target
# could accumulate one row per relay (`basic` and `skyworker` had both emilia and
# karolina rows for the same Discord /32s). This reports those rows and, with --apply,
# keeps the one that names the prefix's CURRENT owner (or the oldest row when the prefix
# has no owner row) and deletes the rest.
#
# Dry run by default. It never touches a prefix whose owner is unknown, and it never
# deletes the last row for a (user, device, target) triple — a target must stay covered.
#
# Usage:
#   bash scripts/b480_dup_rule_cleanup.sh                 # report only
#   bash scripts/b480_dup_rule_cleanup.sh --apply         # delete the losing duplicates
#   SKYGATE_DB=/path/to/skygate.db bash scripts/b480_dup_rule_cleanup.sh
# ============================================================================
set -uo pipefail

DB="${SKYGATE_DB:-/var/lib/skygate/skygate.db}"
APPLY=0
for a in "$@"; do
  case "$a" in
    --apply) APPLY=1 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done

[ -f "$DB" ] || { echo "FAIL: database not found: $DB (set SKYGATE_DB)" >&2; exit 1; }
command -v sqlite3 >/dev/null 2>&1 || { echo "SKIP: sqlite3 not installed — nothing inspected"; exit 0; }

echo "=== duplicated (user, device, target) rows in device_rules — database: $DB"
sqlite3 -header "$DB" <<'SQL'
select user_id, device_id, target_type, target_value,
       count(*) as rows, group_concat(exit_node_id) as relays
  from device_rules
 where enabled = 1 and target_type in ('ip','subnet')
 group by user_id, device_id, target_type, target_value
having count(*) > 1
 order by rows desc
 limit 40;
SQL

N="$(sqlite3 "$DB" "select count(*) from (select 1 from device_rules where enabled=1 and target_type in ('ip','subnet') group by user_id, device_id, target_type, target_value having count(*) > 1);")"
echo
echo "targets with more than one row: ${N:-0}"

if [ "${N:-0}" -eq 0 ]; then
  echo "nothing to clean up"
  exit 0
fi

# The rows to delete: every duplicate whose relay is NOT the prefix's current owner.
# When the prefix has no owner row, the OLDEST row survives and the newer duplicates go.
LOSE_SQL=(
"select r.id from device_rules r
   join prefix_owner p on p.prefix = r.target_value
  where r.enabled = 1 and r.target_type in ('ip','subnet')
    and coalesce(r.exit_node_id,'') <> coalesce(p.exit_node_id,'')
    and exists (
      select 1 from device_rules keep
       where keep.user_id = r.user_id and keep.device_id = r.device_id
         and keep.target_type = r.target_type and keep.target_value = r.target_value
         and coalesce(keep.exit_node_id,'') = coalesce(p.exit_node_id,''))"
"select r.id from device_rules r
  where r.enabled = 1 and r.target_type in ('ip','subnet')
    and not exists (select 1 from prefix_owner p where p.prefix = r.target_value)
    and exists (
      select 1 from device_rules keep
       where keep.user_id = r.user_id and keep.device_id = r.device_id
         and keep.target_type = r.target_type and keep.target_value = r.target_value
         and keep.id < r.id)"
)

TOTAL=0
for q in "${LOSE_SQL[@]}"; do
  c="$(sqlite3 "$DB" "select count(*) from ($q);")"
  TOTAL=$((TOTAL + ${c:-0}))
done
echo "rows that would be removed: ${TOTAL}"

if [ "$APPLY" -eq 0 ]; then
  echo "dry run: nothing written (add --apply to delete the rows above)"
  exit 0
fi

BACKUP="${DB}.pre-b480-$(date -u +%Y%m%d%H%M%S)"
cp -a "$DB" "$BACKUP" 2>/dev/null && echo "backup: $BACKUP" || echo "WARN: could not back up the database (continuing)"
for q in "${LOSE_SQL[@]}"; do
  sqlite3 "$DB" "delete from device_rules where id in ($q);"
done
LEFT="$(sqlite3 "$DB" "select count(*) from (select 1 from device_rules where enabled=1 and target_type in ('ip','subnet') group by user_id, device_id, target_type, target_value having count(*) > 1);")"
echo "APPLIED. targets still duplicated: ${LEFT:-0}"
[ "${LEFT:-0}" -eq 0 ] || echo "NOTE: the remaining duplicates have no owner row on either side — pin the prefix first (skygate prefix-owner), then re-run"
