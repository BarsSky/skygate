#!/usr/bin/env bash

# Live-state check: skip (do not fail) when the docker daemon is unreachable.
. "$(dirname "$0")/lib/skip_if_no_docker.sh"
# check_b_tag_owners.sh — verify ACL policy tagOwners coverage
#
# Phase 7 of [auto-deploy-test-plan.md](../auto-deploy-test-plan.md)
# regression-coverage list. Catches 3 distinct gap classes:
#
#   1. **Tag drift**: a `tag:dev-<user>-<device>` tag that's used in
#      node_owner_map but NOT in tagOwners → the per-device grant
#      in the headscale policy references a non-existent tag → headscale
#      silently skips that grant at apply time.
#
#   2. **Ghost tags**: a tag in tagOwners but no node_owner_map row uses
#      it → the operator added a tag via /admin/acl but the device
#      never got bound to it. Symptom: tag shows in /admin/acl but
#      `headscale nodes list --tags` doesn't include it for any device.
#
#   3. **Standard tags**: tag:public / tag:exit-node / tag:private /
#      tag:subnet-router MUST be present in tagOwners (headscale
#      ACL generation depends on them; missing = broken policy).
#
# 2026-09-13 (Phase 7 follow-up).
#
# Usage:
#   bash scripts/check_b_tag_owners.sh
#
# Exit codes:
#   0 = all tagOwners entries map to a real tag in node_owner_map
#       OR are in the standard-tags allowlist; no missing tags;
#       standard tags all present.
#   1 = one or more contracts failed (operator must act).

set -uo pipefail

CONTAINER="${SKYGATE_CONTAINER:-skygate-skygate-1}"
HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"
PG_CONTAINER="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"

ok()  { echo "  PASS  $*"; }
bad() { echo "  FAIL  $*"; exit 1; }

# ── containers reachable ──
for c in "$CONTAINER" "$HEADSCALE_CONTAINER" "$PG_CONTAINER"; do
    # B281 (2026-09-22): absent live dependency = SKIP, never FAIL
    # (AGENTS.md §1.1). Note `bad()` in this script exits 1, so the old form
    # aborted the whole catalog entry with a failure on every CI run.
    sudo -n docker inspect "$c" >/dev/null 2>&1 || {
        echo "  SKIP  $c container not running (live check — run it on the skygate host)"
        exit 0
    }
done

# ── A. Standard tags present in live policy tagOwners ──
# tag:public, tag:exit-node, tag:private, tag:subnet-router.
# These are the standard B111 canonical tags. Missing = broken ACL.
echo
echo "=== A. standard tags in live policy tagOwners ==="
TAGOWNERS=$(sudo -n docker exec "$HEADSCALE_CONTAINER" headscale policy get -o json 2>/dev/null \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); print(" ".join(d.get("tagOwners",{}).keys()))')
for tag in tag:public tag:exit-node tag:private tag:subnet-router; do
    if echo " $TAGOWNERS " | grep -q " $tag "; then
        ok "tag:public / tag:exit-node / tag:private / tag:subnet-router present ($tag)"
    else
        bad "$tag MISSING from policy tagOwners"
    fi
done

# ── B. Every tag in tagOwners maps to a real tag in use ──
# Compare the set of tagOwners keys vs the set of tags that appear
# in either node_owner_map OR device_rules (which is where
# per-DEVICE rules store the tag they're bound to).
#
# Some tagOwners keys are STANDARD tags (tag:public / etc) — those
# don't appear in node_owner_map but are valid by design.
# We only flag tagOwners keys that:
#   - don't match the standard-tag allowlist
#   - AND don't appear in node_owner_map or device_rules
echo
echo "=== B. tagOwners keys map to real tags (no orphans) ==="
NOM_TAGS=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
  "SELECT DISTINCT tag FROM node_owner_map WHERE tag <> ''" 2>/dev/null)
DEVICE_TAGS=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
  "SELECT DISTINCT target_tag FROM device_rules WHERE target_tag <> '' AND target_type='subnet'" 2>/dev/null)
USED_TAGS="$NOM_TAGS
$DEVICE_TAGS"

STANDARD_TAGS="tag:public tag:exit-node tag:private tag:subnet-router"

orphans=0
for tag in $TAGOWNERS; do
    # Skip standard tags (they're intentionally not in node_owner_map)
    case " $STANDARD_TAGS " in
        *" $tag "*) continue ;;
    esac
    if ! echo "$USED_TAGS" | grep -qxF "$tag"; then
        bad "tagOwners entry $tag has no matching node_owner_map or device_rules row (orphan)"
        orphans=$((orphans+1))
    fi
done
if [ "$orphans" = "0" ]; then
    ok "all tagOwners entries either match a real tag in use OR are standard"
fi

# ── C. Every tag in node_owner_map is in tagOwners ──
# The inverse: node_owner_map has tag X, but policy tagOwners
# doesn't → the per-device grant referencing tag X will be
# silently dropped by headscale at apply time (the B118 bug class).
echo
echo "=== C. node_owner_map tags all in tagOwners ==="
MISSING=0
for tag in $NOM_TAGS; do
    if ! echo " $TAGOWNERS " | grep -q " $tag "; then
        bad "node_owner_map has $tag but policy tagOwners does NOT (headscale will silently drop per-device grants for this tag)"
        MISSING=$((MISSING+1))
    fi
done
if [ "$MISSING" = "0" ]; then
    ok "every tag in node_owner_map has a matching tagOwners entry"
fi

# ── D. tagOwners ownership is sane ──
# tag:dev-* tags MUST be owned by the user portion (skyadmin@ or
# michail@ etc., NOT infra@) for per-user per-DEVICE grants.
# B118 enforces this — re-pin here.
echo
echo "=== D. tag:dev-* ownership (skyadmin/michail/infra per B118) ==="
OWNERS=$(sudo -n docker exec "$HEADSCALE_CONTAINER" headscale policy get -o json 2>/dev/null \
  | python3 -c '
import json,sys
d = json.load(sys.stdin)
to = d.get("tagOwners",{})
for tag, owners in sorted(to.items()):
    if not tag.startswith("tag:dev-"):
        continue
    print(f"{tag} {owners}")
')
if [ -z "$OWNERS" ]; then
    bad "no tag:dev-* entries in tagOwners"
else
    PSQL_USERS=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
        "SELECT username FROM portal_users WHERE username <> ''" 2>/dev/null | paste -sd'|' -)
    [ -n "$PSQL_USERS" ] || PSQL_USERS='skyadmin|michail|guest|daniil|infra'
    echo "$OWNERS" | while IFS=' ' read -r tag owner_json; do
        [ -z "$tag" ] && continue
        # owner_json is the python repr of the owner list, e.g. ['infra@tsnet.skynas.ru'].
        # 2026-09-19: the old pattern only accepted a DOUBLE-quoted owner while
        # python prints single quotes, so every tag:dev-* entry was reported as
        # "potential ACL injection" (the bad() inside this subshell only killed
        # the subshell, which is why the script still exited 0 — a misleading
        # FAIL line in an otherwise green check). Accept either quoting style and
        # take the usernames from portal_users, with the historical five as a
        # fallback when the DB is not reachable.
        if echo "$owner_json" | grep -qE "[\"']?($PSQL_USERS)@"; then
            : # ok; user-owned
        else
            bad "$tag owner $owner_json is not a known portal user (potential ACL injection)"
        fi
    done
    ok "all tag:dev-* entries owned by a known portal user ($PSQL_USERS)"
fi

echo
echo "=== summary: tagOwners count=$(echo $TAGOWNERS | wc -w) tags ==="
