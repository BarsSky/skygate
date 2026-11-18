#!/usr/bin/env bash
# acl_inputs_snapshot.sh — capture the EXACT inputs the ACL generator consumes, as one hash.
#
# WHY THIS EXISTS (B496, safety gate for the device-metadata work)
#
# Before changing how devices are classified or how an exit-node preference is resolved, the project
# needs a way to prove that the generated ACL did not move. The generator reads a small, known set of
# tables; this script reduces them to a canonical, order-stable text and a single SHA-256, so:
#
#   before the change :  bash scripts/acl_inputs_snapshot.sh --save
#   after  the change :  bash scripts/acl_inputs_snapshot.sh --diff
#
# and `--diff` exits non-zero when anything the generator sees has changed. A non-zero diff is not
# "bad" by itself — it is the list of decisions that must be explained BEFORE an ACL is applied.
#
# It is READ-ONLY: every query runs against a read-only connection, and nothing is written into the
# panel database.
#
# WHAT IT COVERS (the generator's inputs, from internal/acl + internal/prefixowner):
#   device_rules            — the rules themselves (target, relay, owner, device_ip, parent_domain)
#   device_exit_node_prefs  — per-device exit-node choice (via / opted_out)
#   user_exit_node_prefs    — per-user default (the one that currently does NOT reach tagged devices)
#   node_owner_map          — owner attribution and the device tag used as the grant's `src`
#   prefix_owner            — which relay owns which prefix (the `via` of a per-CIDR grant)
#   portal_users            — the user set the group grants are built from
#
# Device metadata (os / device_type) is deliberately NOT included: B496's premise is that it is
# display-only, and a contract asserts no decision code reads it. If that ever changes, this list must
# change with it — visibly, in one place.
#
# USAGE
#   bash scripts/acl_inputs_snapshot.sh                 # print the hash + row counts
#   bash scripts/acl_inputs_snapshot.sh --save          # write .acl-inputs-snapshot in the CWD
#   bash scripts/acl_inputs_snapshot.sh --diff [FILE]   # compare with the saved snapshot
#   bash scripts/acl_inputs_snapshot.sh --json          # machine-readable
#   bash scripts/acl_inputs_snapshot.sh --db PATH       # operate on a copy instead of the live DB
#
# EXIT: 0 same (or saved), 1 differs, 2 no usable database.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

DB=""
MODE="print"
SNAP_FILE=".acl-inputs-snapshot"
JSON=0

while [ $# -gt 0 ]; do
    case "$1" in
        --save) MODE="save"; shift ;;
        --diff) MODE="diff"; shift; [ $# -gt 0 ] && case "${1:-}" in --*) : ;; *) SNAP_FILE="$1"; shift ;; esac ;;
        --json) JSON=1; shift ;;
        --db)   DB="${2:-}"; shift 2 ;;
        -h|--help) sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "acl_inputs_snapshot: unknown flag: $1" >&2; exit 2 ;;
    esac
done

# shellcheck source=lib/live_db.sh
. "$HERE/lib/live_db.sh"

if [ -z "$DB" ]; then
    dsn="$(live_db_dsn 2>/dev/null || true)"
    case "$dsn" in
        sqlite:*) DB="${dsn#sqlite:}" ;;
        *) : ;;
    esac
fi
if [ -z "$DB" ] || [ ! -r "$DB" ]; then
    echo "acl_inputs_snapshot: cannot read a SQLite panel database (pass --db PATH)" >&2
    exit 2
fi
command -v sqlite3 >/dev/null 2>&1 || { echo "acl_inputs_snapshot: sqlite3 is required" >&2; exit 2; }

# One canonical line per row, deterministic ORDER BY, no timestamps (they are not generator inputs).
DUMP="$(mktemp)" || exit 2
trap 'rm -f "$DUMP"' EXIT

run() { # run <label> <sql>
    echo "### $1" >> "$DUMP"
    sqlite3 -readonly -separator '|' "$DB" "$2" >> "$DUMP" 2>/dev/null || echo "(table absent)" >> "$DUMP"
}

run device_rules \
  "SELECT device_id, coalesce(user_id,''), coalesce(exit_node_id,''), coalesce(target_type,''), coalesce(target_value,''), coalesce(device_ip,''), coalesce(parent_domain,''), coalesce(user_name,''), coalesce(device_hostname,''), coalesce(enabled,1), coalesce(action,'') FROM device_rules ORDER BY device_id, target_type, target_value, exit_node_id, parent_domain, device_ip;"
run device_exit_node_prefs \
  "SELECT coalesce(user_id,''), coalesce(device_hostname,''), coalesce(exit_node_tag,''), coalesce(via_enabled,0), coalesce(opted_out,0) FROM device_exit_node_prefs ORDER BY user_id, device_hostname;"
run user_exit_node_prefs \
  "SELECT coalesce(user_id,''), coalesce(exit_node_tag,''), coalesce(via_enabled,0) FROM user_exit_node_prefs ORDER BY user_id;"
run node_owner_map \
  "SELECT node_id, coalesce(headscale_user_id,0), coalesce(username,''), coalesce(tag,''), coalesce(tagged_by_user_id,0), coalesce(hostname,'') FROM node_owner_map ORDER BY node_id;"
run prefix_owner \
  "SELECT prefix, coalesce(exit_node_id,''), coalesce(source,''), coalesce(claims,0), coalesce(devices,0) FROM prefix_owner ORDER BY prefix;"
run portal_users \
  "SELECT id, coalesce(username,''), coalesce(is_admin,0) FROM portal_users ORDER BY id;"

HASH="$(sha256sum "$DUMP" | awk '{print $1}')"
ROWS="$(grep -c '^[^#]' "$DUMP" 2>/dev/null || echo 0)"

if [ "$JSON" -eq 1 ]; then
    printf '{"db":"%s","sha256":"%s","input_rows":%s}\n' "$DB" "$HASH" "$ROWS"
else
    echo "acl inputs — database: $DB"
    echo "input rows: $ROWS"
    echo "sha256    : $HASH"
fi

case "$MODE" in
    save)
        cp "$DUMP" "$SNAP_FILE" && echo "saved: $SNAP_FILE"
        exit 0
        ;;
    diff)
        if [ ! -f "$SNAP_FILE" ]; then echo "acl_inputs_snapshot: no snapshot at $SNAP_FILE" >&2; exit 2; fi
        if diff -u "$SNAP_FILE" "$DUMP" > .acl-inputs-diff 2>&1; then
            echo "ACL INPUTS UNCHANGED"
            exit 0
        fi
        echo "ACL INPUTS CHANGED — inspect .acl-inputs-diff before applying anything" >&2
        head -40 .acl-inputs-diff >&2
        exit 1
        ;;
    *)
        exit 0
        ;;
esac
