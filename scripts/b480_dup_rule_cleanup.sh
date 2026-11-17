#!/usr/bin/env bash
# ============================================================================
# b480_dup_rule_cleanup.sh — B480, retuned by B484 (2026-11-17)
#
# ONE RULE PER TARGET — and the two classes that only LOOK like duplicates.
#
# Measured on the live installation 2026-11-17 (461 rows, 58 groups the old version called
# "duplicated"), the grouping the first version used was wrong in a way that mattered:
#
#   1. SAME RELAY, DIFFERENT parent_domain — 38 groups, 0 true duplicates. The same address is
#      published by several domains (`discord.com` and `discord.gg`, `harness.io` /
#      `www.harness.io` / `pnpm.io` / `minimax.io` for one IP). The six-column unique index
#      exists PRECISELY to keep one row per domain, and B298 showed what deleting them costs:
#      the domain loses its marker, re-resolves on the next tick, re-inserts, the collapse
#      deletes it again — on a `policy.mode: file` host that is a headscale restart every five
#      minutes. These are NOT duplicates and this script no longer calls them that.
#
#   2. DIFFERENT RELAY, SAME (target, parent_domain) — the real duplicate: one domain, one
#      target, two exit nodes. That is what B480's writer now refuses and what this script
#      removes, keeping the row whose relay is the prefix's CURRENT owner.
#
# A third, separate finding is reported but NOT deleted: a prefix whose recorded owner is a
# relay that no rule names (`140.82.113.22/32` owned by `sharlotta` while the rules name
# karolina and emilia). Deleting the rules would not fix it — the owner is what is wrong, and
# B484 fixes the engine that chose it; until the engine re-decides, the script keeps the oldest
# row and names the mismatch.
#
# Usage:
#   bash scripts/b480_dup_rule_cleanup.sh                  # report only
#   bash scripts/b480_dup_rule_cleanup.sh --apply          # delete the losing cross-relay rows
#   bash scripts/b480_dup_rule_cleanup.sh --apply --force   # also delete rows of an owner that
#                                                          # no rule names (keeps the oldest)
#   SKYGATE_DB=/path/to/skygate.db bash scripts/b480_dup_rule_cleanup.sh
# ============================================================================
set -uo pipefail

DB="${SKYGATE_DB:-/var/lib/skygate/skygate.db}"
APPLY=0
FORCE=0
for a in "$@"; do
  case "$a" in
    --apply) APPLY=1 ;;
    --force) FORCE=1 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done

[ -f "$DB" ] || { echo "FAIL: database not found: $DB (set SKYGATE_DB)" >&2; exit 1; }
command -v sqlite3 >/dev/null 2>&1 || { echo "SKIP: sqlite3 not installed — nothing inspected"; exit 0; }

echo "=== database: $DB"

# --- class A: the same (user, device, type, target, parent_domain) on more than one relay ----
GROUPS_A="$(sqlite3 "$DB" "select count(*) from (
    select 1 from device_rules
     where enabled=1 and target_type in ('ip','subnet')
     group by user_id, device_id, target_type, target_value, coalesce(parent_domain,'')
    having count(distinct exit_node_id) > 1);")"
ROWS_A="$(sqlite3 "$DB" "select coalesce(sum(n),0) from (
    select count(*) n from device_rules
     where enabled=1 and target_type in ('ip','subnet')
     group by user_id, device_id, target_type, target_value, coalesce(parent_domain,'')
    having count(distinct exit_node_id) > 1);")"

echo "=== class A — one target+domain on SEVERAL relays (the real duplicate): ${GROUPS_A:-0} group(s), ${ROWS_A:-0} row(s)"
sqlite3 -header "$DB" "
  select d.user_id, d.device_id, d.target_value, coalesce(d.parent_domain,'') as domain,
         group_concat(d.exit_node_id) as relays,
         coalesce((select exit_node_id from prefix_owner p where p.prefix = d.target_value),'<no owner>') as owner
    from device_rules d
   where d.enabled=1 and d.target_type in ('ip','subnet')
     and (d.user_id, d.device_id, d.target_type, d.target_value, coalesce(d.parent_domain,'')) in (
       select user_id, device_id, target_type, target_value, coalesce(parent_domain,'')
         from device_rules where enabled=1 and target_type in ('ip','subnet')
        group by user_id, device_id, target_type, target_value, coalesce(parent_domain,'')
       having count(distinct exit_node_id) > 1)
   group by d.user_id, d.device_id, d.target_value, coalesce(d.parent_domain,'')
   order by d.device_id, d.target_value limit 25;" | sed 's/^/  /'

# --- class B: shared addresses, one row per domain — kept BY DESIGN (B298) -------------------
GROUPS_B="$(sqlite3 "$DB" "select count(*) from (
    select 1 from device_rules where enabled=1 and target_type in ('ip','subnet')
     group by user_id, device_id, target_type, target_value
    having count(*)>1 and count(distinct exit_node_id)=1 and count(distinct coalesce(parent_domain,''))>1);")"
echo
echo "=== class B — one address published by several domains, one row per domain: ${GROUPS_B:-0} group(s)"
echo "    kept by design: the per-domain row is what the CDN short-circuit and the domain status read (B298)."

# --- class C: the owner is a relay no rule names ---------------------------------------------
# Only devices that ACTUALLY USE an exit node can be broken by a foreign owner: a device with no
# preference (`opted_out`, or an empty tag) routes directly, so `via` never applies to it and the
# owner's relay is irrelevant. Measuring on 2026-11-17 without this filter reported 157 "broken"
# prefixes, and every one of them belonged to the opted-out `skyworker` — a report that cries
# wolf on 157 harmless rows is how a real one gets ignored.
VOTING_DEVICE="exists (
    select 1 from device_exit_node_prefs pref
      join node_owner_map m on lower(m.hostname) = lower(pref.device_hostname)
     where m.node_id = cast(d.device_id as text)
       and pref.opted_out = 0 and coalesce(pref.exit_node_tag,'') <> '')"
GROUPS_C="$(sqlite3 "$DB" "select count(*) from (
    select d.user_id, d.device_id, d.target_value
      from device_rules d join prefix_owner p on p.prefix = d.target_value
     where d.enabled=1 and d.target_type in ('ip','subnet') and $VOTING_DEVICE
     group by d.user_id, d.device_id, d.target_value, p.exit_node_id
    having sum(case when coalesce(d.exit_node_id,'') = p.exit_node_id then 1 else 0 end) = 0);")"
echo
echo "=== class C — a prefix whose OWNER no rule names: ${GROUPS_C:-0} group(s)"
if [ "${GROUPS_C:-0}" -gt 0 ]; then
  sqlite3 -header "$DB" "
    select d.device_id, d.target_value, group_concat(distinct coalesce(d.exit_node_id,'<empty>')) as rules_name,
           p.exit_node_id as owner, p.source
      from device_rules d join prefix_owner p on p.prefix = d.target_value
     where d.enabled=1 and d.target_type in ('ip','subnet') and exists (
       select 1 from device_exit_node_prefs pref
         join node_owner_map m on lower(m.hostname) = lower(pref.device_hostname)
        where m.node_id = cast(d.device_id as text)
          and pref.opted_out = 0 and coalesce(pref.exit_node_tag,'') <> '')
     group by d.user_id, d.device_id, d.target_value, p.exit_node_id
    having sum(case when coalesce(d.exit_node_id,'') = p.exit_node_id then 1 else 0 end) = 0
     limit 20;" | sed 's/^/  /'
  echo "    the owner is wrong, not the rules: B484 makes the engine decide among the relays the rules name."
  echo "    until it re-decides, pin it from the panel (skygate prefix-owner) or re-run with --force to collapse to the oldest row."
fi

if [ "${GROUPS_A:-0}" -eq 0 ]; then
  echo
  echo "nothing to clean up: no target+domain is served by two relays"
  exit 0
fi

# --- the rows to delete ---------------------------------------------------------------------
# A row loses when its partition has another row that is a BETTER keeper:
#   the owner's row (always the keeper when the owner is one of the rows),
#   else the oldest row (--force only, and only for class C).
LOSE_SQL_KEEP_OWNER="select r.id from device_rules r
   join prefix_owner p on p.prefix = r.target_value
  where r.enabled = 1 and r.target_type in ('ip','subnet')
    and coalesce(r.exit_node_id,'') <> coalesce(p.exit_node_id,'')
    and exists (
      select 1 from device_rules keep
       where keep.user_id = r.user_id and keep.device_id = r.device_id
         and keep.target_type = r.target_type and keep.target_value = r.target_value
         and coalesce(keep.parent_domain,'') = coalesce(r.parent_domain,'')
         and coalesce(keep.exit_node_id,'') = coalesce(p.exit_node_id,''))"

LOSE_SQL_KEEP_OLDEST="select r.id from device_rules r
  where r.enabled = 1 and r.target_type in ('ip','subnet')
    and not exists (select 1 from prefix_owner p where p.prefix = r.target_value
                     and coalesce(p.exit_node_id,'') = coalesce(r.exit_node_id,''))
    and exists (
      select 1 from device_rules keep
       where keep.user_id = r.user_id and keep.device_id = r.device_id
         and keep.target_type = r.target_type and keep.target_value = r.target_value
         and coalesce(keep.parent_domain,'') = coalesce(r.parent_domain,'')
         and keep.id < r.id)"

TOTAL=0
C="$(sqlite3 "$DB" "select count(*) from ($LOSE_SQL_KEEP_OWNER);")"
TOTAL=$((TOTAL + ${C:-0}))
echo
echo "rows removed for the owner's row to win: ${C:-0}"
LEFT="$(sqlite3 "$DB" "select count(*) from (
    select 1 from device_rules where enabled=1 and target_type in ('ip','subnet')
     group by user_id, device_id, target_type, target_value, coalesce(parent_domain,'')
    having count(distinct exit_node_id) > 1)
   g join (select 1) x;")"
if [ "$FORCE" -eq 1 ]; then
  C2="$(sqlite3 "$DB" "select count(*) from ($LOSE_SQL_KEEP_OLDEST);")"
  TOTAL=$((TOTAL + ${C2:-0}))
  echo "rows removed by --force (owner names no row; the oldest wins): ${C2:-0}"
else
  echo "groups left for --force (owner names no row): ${GROUPS_C:-0}"
fi
echo "total rows that would be removed: ${TOTAL}"

if [ "$APPLY" -eq 0 ]; then
  echo "dry run: nothing written (add --apply to delete the rows above)"
  exit 0
fi

BACKUP="${DB}.pre-b484-$(date -u +%Y%m%d%H%M%S)"
cp -a "$DB" "$BACKUP" 2>/dev/null && echo "backup: $BACKUP" || echo "WARN: could not back up the database (continuing)"
sqlite3 "$DB" "delete from device_rules where id in ($LOSE_SQL_KEEP_OWNER);"
if [ "$FORCE" -eq 1 ]; then
  sqlite3 "$DB" "delete from device_rules where id in ($LOSE_SQL_KEEP_OLDEST);"
fi
AFTER_A="$(sqlite3 "$DB" "select count(*) from (
    select 1 from device_rules where enabled=1 and target_type in ('ip','subnet')
     group by user_id, device_id, target_type, target_value, coalesce(parent_domain,'')
    having count(distinct exit_node_id) > 1);")"
echo "APPLIED. class-A groups remaining: ${AFTER_A:-0} (class B is by design and class C is an owner problem, not a duplicate)"
