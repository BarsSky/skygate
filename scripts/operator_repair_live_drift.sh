#!/usr/bin/env bash
# operator_repair_live_drift.sh — repair the two live-data drifts that keep four
# pre-deploy contracts red on the reference host, with backups and a rollback
# recipe. DRY RUN by default; nothing is mutated without `--apply`.
#
# The two drifts (evidence: docs/ROADMAP.md §5.1):
#
#   A. node_owner_map (B243) — four rows point at headscale users that no longer
#      exist: node_id 6/29/31 carry headscale_user_id=6 while the portal user
#      `michail` now lives at headscale ID 8, and node_id 45 carries the
#      int-overflow sentinel 2147455555 (the synthetic `tagged-devices` owner).
#      -> relink the three michail rows to 8, delete the sentinel row.
#
#   B. ACL drifted from the DB (B188.2 + B188.3 + B-mod-tag-owners-coverage).
#      B188.2/B188.3 were a stale contract (fixed in code 2026-09-18). The
#      remaining part is THREE orphan tagOwners entries, produced by stale
#      per-device rows rather than by the policy:
#        tag:dev-skyadmin-svyatoslava-1  <- node_owner_map row whose tag does not
#                                           follow tag:dev-<user>-<hostname>
#                                           (it collided with skyworker's node)
#        tag:dev-skyadmin-emilia         <- device_exit_node_prefs rows for
#        tag:dev-skyadmin-skygate-host-1    devices that belong to infra / no
#                                           longer exist (the app's own
#                                           reconciler logs them as ORPHAN)
#      -> normalise the tags, drop the orphan prefs, then regenerate + re-apply
#         the ACL from the DB.
#
# Usage:
#   bash scripts/operator_repair_live_drift.sh            # dry run (default)
#   bash scripts/operator_repair_live_drift.sh --apply    # do it
#
# Environment overrides: SKYGATE_REPO, SKYGATE_PG_CONTAINER, SKYGATE_PG_DB,
# SKYGATE_PG_USER, SKYGATE_HS_CONTAINER, SKYGATE_APP_CONTAINER.
#
# Exit codes: 0 = ok (dry run or applied cleanly), 1 = precondition failed,
#             2 = a mutation failed (backups are printed so you can roll back).

set -uo pipefail

APPLY=0
case "${1:-}" in
  --apply) APPLY=1 ;;
  ""|--dry-run) APPLY=0 ;;
  *) echo "usage: $0 [--dry-run|--apply]" >&2; exit 1 ;;
esac

REPO="${SKYGATE_REPO:-/home/skyadmin/skygate}"
PG="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"
DB="${SKYGATE_PG_DB:-skygate_staging}"
PGUSER="${SKYGATE_PG_USER:-admin}"
HS="${SKYGATE_HS_CONTAINER:-headscale}"
APP="${SKYGATE_APP_CONTAINER:-skygate-skygate-1}"

MODE=$([ "$APPLY" = 1 ] && echo APPLY || echo "DRY RUN")
TS="$(date -u +%Y%m%dT%H%M%SZ)"
BK="/tmp/live-drift-$TS"

say()  { printf '%s\n' "$*"; }
hdr()  { printf '\n=== %s ===\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker not available — run this on the reference host"

psql_ro() { docker exec "$PG" psql -U "$PGUSER" -d "$DB" -tA -F'|' -c "$1"; }

hdr "0. preconditions"
for c in "$PG" "$HS" "$APP"; do
  docker inspect -f '{{.State.Running}}' "$c" >/dev/null 2>&1 \
    || die "container $c not found"
done
say "containers present: $PG / $HS / $APP"
[ -d "$REPO" ] || die "repo not found at $REPO (set SKYGATE_REPO)"

hdr "1. current node_owner_map rows"
ROWS="$(psql_ro 'SELECT node_id, headscale_user_id, username, tag FROM node_owner_map ORDER BY node_id')" \
  || die "could not read node_owner_map"
printf '%s\n' "$ROWS"
STALE_RELINK="$(printf '%s\n' "$ROWS" | awk -F'|' '$2 == 6 && $3 == "michail" {print $1}' | paste -sd, -)"
STALE_SENTINEL="$(printf '%s\n' "$ROWS" | awk -F'|' '$2 == 2147455555 {print $1}' | paste -sd, -)"

hdr "2. live headscale users"
USERS="$(docker exec "$HS" headscale users list 2>/dev/null | sed 's/\x1b\[[0-9;]*m//g')" \
  || die "could not list headscale users"
printf '%s\n' "$USERS"
MICHAIL_ID="$(printf '%s\n' "$USERS" | awk -F'|' '$3 ~ /michail/ {gsub(/ /,"",$1); print $1; exit}')"

hdr "3. plan"
say "A. node_owner_map"
say "   relink rows [$STALE_RELINK] from headscale_user_id=6 to ${MICHAIL_ID:-<michail-not-found>}"
say "   delete sentinel rows [$STALE_SENTINEL] (headscale_user_id=2147455555)"
say "B. ACL"
say "   regenerate + re-apply the policy from the DB:"
say "     docker exec $APP /app/skygate acl-apply"
say "   (this is the app's own generation path; the DB is the source of truth —"
say "    back up the live policy first, which step 4 does)"

hdr "4. backups -> $BK"
mkdir -p "$BK" || die "could not create $BK"
docker exec "$HS" headscale policy get > "$BK/policy.before.json" 2>/dev/null \
  || die "could not back up the live policy"
psql_ro 'SELECT node_id, headscale_user_id, username, tag FROM node_owner_map ORDER BY node_id' \
  > "$BK/node_owner_map.before.csv" || die "could not back up node_owner_map"
psql_ro 'SELECT user_id, device_hostname, exit_node_tag, via_enabled FROM device_exit_node_prefs ORDER BY user_id, device_hostname' \
  > "$BK/device_exit_node_prefs.before.csv" || die "could not back up device_exit_node_prefs"
cp -p "$REPO/go.mod" "$BK/go.mod" 2>/dev/null || true
say "policy.before.json               $(wc -c < "$BK/policy.before.json") bytes"
say "node_owner_map.before.csv        $(wc -l < "$BK/node_owner_map.before.csv") rows"
say "device_exit_node_prefs.before.csv $(wc -l < "$BK/device_exit_node_prefs.before.csv") rows"
say "rollback:"
say "  docker exec -i $HS headscale policy set -f - < $BK/policy.before.json"
say "  psql: UPDATE node_owner_map SET headscale_user_id=<old> WHERE node_id=<id>;  (see the CSV)"
say "  psql: re-INSERT the deleted device_exit_node_prefs rows from the CSV if needed"

if [ "$APPLY" != 1 ]; then
  hdr "DRY RUN — nothing changed"
  say "re-run with --apply to perform steps 3A + 3B, then the four contracts are re-checked."
  exit 0
fi

[ -n "$MICHAIL_ID" ] || echo "note: headscale user 'michail' not found — skipping the relink step"
if [ -z "$STALE_RELINK$STALE_SENTINEL" ]; then
  say "note: no stale node_owner_map rows — step 5A is already clean"
fi

hdr "5A. apply node_owner_map repair"
if [ -n "$STALE_RELINK" ]; then
  psql_ro "UPDATE node_owner_map SET headscale_user_id=$MICHAIL_ID WHERE headscale_user_id=6 AND username='michail'" \
    || die "UPDATE failed (backup: $BK)"
fi
if [ -n "$STALE_SENTINEL" ]; then
  psql_ro "DELETE FROM node_owner_map WHERE headscale_user_id=2147455555" \
    || die "DELETE failed (backup: $BK)"
fi
psql_ro 'SELECT node_id, headscale_user_id, username, hostname, tag FROM node_owner_map ORDER BY node_id::int'

# 5B. The orphan tagOwners entries come from stale PER-DEVICE rows, not from the
# policy itself:
#   tag:dev-skyadmin-svyatoslava-1   <- node_owner_map row whose tag does not
#                                       follow the tag:dev-<user>-<hostname>
#                                       convention (its tag collided with
#                                       skyworker's node)
#   tag:dev-skyadmin-emilia          <- device_exit_node_prefs row for a device
#   tag:dev-skyadmin-skygate-host-1     that belongs to another user (infra) or
#                                       no longer exists — the app's own
#                                       preferred-exit reconciler logs exactly
#                                       these as ORPHAN candidates
hdr "5B. clean the stale per-device rows behind the orphan tagOwners entries"
psql_ro "SELECT node_id, username, hostname AS device, tag AS old_tag,
                'tag:dev-' || username || '-' || hostname AS correct_tag
           FROM node_owner_map
          WHERE hostname <> '' AND tag <> 'tag:dev-' || username || '-' || hostname
            AND NOT EXISTS (SELECT 1 FROM node_owner_map o
                             WHERE o.tag = 'tag:dev-' || node_owner_map.username || '-' || node_owner_map.hostname
                               AND o.node_id <> node_owner_map.node_id)"
psql_ro "SELECT p.user_id, u.username, p.device_hostname, p.exit_node_tag
           FROM device_exit_node_prefs p JOIN portal_users u ON u.id = p.user_id
          WHERE NOT EXISTS (SELECT 1 FROM node_owner_map n
                             WHERE n.username = u.username AND n.hostname = p.device_hostname)"
if [ "$APPLY" = 1 ]; then
  psql_ro "UPDATE node_owner_map n
              SET tag = 'tag:dev-' || n.username || '-' || n.hostname
            WHERE n.hostname <> '' AND n.tag <> 'tag:dev-' || n.username || '-' || n.hostname
              AND NOT EXISTS (SELECT 1 FROM node_owner_map o
                               WHERE o.tag = 'tag:dev-' || n.username || '-' || n.hostname
                                 AND o.node_id <> n.node_id)" \
    || die "tag-normalisation UPDATE failed (backup: $BK)"
  psql_ro "DELETE FROM device_exit_node_prefs p
             USING portal_users u
            WHERE u.id = p.user_id
              AND NOT EXISTS (SELECT 1 FROM node_owner_map n
                               WHERE n.username = u.username AND n.hostname = p.device_hostname)" \
    || die "orphan-pref DELETE failed (backup: $BK)"
  say "remaining per-device rows:"
  psql_ro 'SELECT user_id, device_hostname, exit_node_tag FROM device_exit_node_prefs ORDER BY user_id, device_hostname'
fi

hdr "5C. re-apply the ACL from the DB"
docker exec "$APP" /app/skygate acl-apply || die "acl-apply failed (policy backup: $BK/policy.before.json)"
docker exec "$HS" headscale policy get > "$BK/policy.after.json" 2>/dev/null || true
if [ -s "$BK/policy.after.json" ]; then
  say "policy diff (before -> after):"
  diff -u "$BK/policy.before.json" "$BK/policy.after.json" | head -40 || true
fi

hdr "6. re-run the four contracts"
cd "$REPO" || die "cd $REPO failed"
for c in check_b_node_owner_map_orphans.sh check_b_tag_owners.sh check_b188_2.sh check_b188_3.sh; do
  if [ -f "scripts/$c" ]; then
    printf '\n--- %s ---\n' "$c"
    bash "scripts/$c" 2>&1 | tail -12
  fi
done
say ""
say "backups kept at $BK"
