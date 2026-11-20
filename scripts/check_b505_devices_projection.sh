#!/usr/bin/env bash
# check_b505_devices_projection.sh — B505 (2026-11-20)
#
# «Поправь мелочь, чтобы отслеживалась и соответствовала» — about the `devices` table.
#
# MEASURED on the reference deployment (2026-11-20):
#
#	SELECT count(*) FROM devices            → 0
#	headscale live nodes                   → 15
#	node_owner_map rows                    → one per node
#
# So the schema created a table, a query constant waited on it, and NOTHING ever
# wrote it. The readers that used to depend on it were moved to live headscale data
# years of blocks ago (B390 records the move), which is exactly why nobody noticed:
# the table still LOOKED authoritative to anyone opening the database.
#
# THE FIX is a projection, not a second source of truth:
#   * liveness (online, last_seen, ip_addresses, hostname) comes from headscale;
#   * attribution (which portal user owns the node) and the platform marker (os)
#     come from node_owner_map;
#   * it is refreshed on every node-discovery tick and nothing else writes it;
#   * a live node no portal user owns gets NO row and is counted as unattributed —
#     inventing an owner is the class of guess B284/B316 exist to prevent;
#   * a node that left the tailnet loses its row, so the table cannot accumulate
#     ghosts.
#
# Contracts:
#   A. the projection exists, is a projection (documented as such) and is wired
#   B. it is idempotent and heals a rename instead of duplicating the node
#   C. vanished nodes are removed; unowned nodes are counted, not invented
#   D. behaviour: the package tests run (SKIP without a Go toolchain)
#   E. the one query that names the table says what it is now
#   F. discoverable: registered, tracked, indexed, roadmap, release notes

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

IMPL=internal/db/devices_sync_b505.go
TEST=internal/db/devices_sync_b505_test.go
WIRE=internal/nodeownership/auto.go
QUERY=internal/db/queries.go
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
GATE=scripts/verify_pre_deploy.sh

for f in "$IMPL" "$TEST" "$WIRE" "$QUERY" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B505 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

IMPL_TXT="$(cat "$IMPL")"
WIRE_TXT="$(cat "$WIRE")"

# ── A. what it is, and that it runs ─────────────────────────────────────────
case "$IMPL_TXT" in
  *'func SyncDeviceProjection('*) ok "A1: the projection is a named function" ;;
  *) bad "A1: no SyncDeviceProjection in $IMPL" ;;
esac
case "$IMPL_TXT" in
  *'is a PROJECTION'*) ok "A2: the file says it is a projection, not a source of truth" ;;
  *) bad "A2: nothing states the table's role — the next reader would treat it as authoritative" ;;
esac
case "$IMPL_TXT" in
  *'node_owner_map'*) ok "A3: attribution is read from node_owner_map (one owner of that decision)" ;;
  *) bad "A3: the projection does not use node_owner_map" ;;
esac
case "$WIRE_TXT" in
  *'db.SyncDeviceProjection(conn, deviceStatesFromNodes(nodes))'*)
    ok "A4: the node-discovery tick refreshes it from the live node list" ;;
  *) bad "A4: nothing calls the projection — the table would stay empty" ;;
esac
case "$WIRE_TXT" in
  *'device-projection: written='*) ok "A5: the tick reports what it wrote (an invisible effect is how this table died)" ;;
  *) bad "A5: the projection runs silently" ;;
esac
# $N placeholders are the only form that works on BOTH backends.
case "$IMPL_TXT" in
  *'?,'*|*'= ?'*) bad "A6: a '?' placeholder appeared — pgx does not translate it (see internal/db/placeholders.go)" ;;
  *) ok "A6: no '?' placeholders (both backends accept \$N)" ;;
esac
# The headscale timestamp is RFC3339; a wrong last_seen is worse than a missing one.
case "$WIRE_TXT" in
  *'time.Parse(time.RFC3339'*) ok "A7: last_seen is parsed explicitly instead of stored raw" ;;
  *) bad "A7: last_seen is not parsed from the headscale timestamp" ;;
esac

# ── B. idempotent, rename in place ──────────────────────────────────────────
case "$IMPL_TXT" in
  *'byNodeID'*'byHost'*) ok "B1: rows are identified by node id, with a hostname fallback for legacy rows" ;;
  *) bad "B1: no node-id identity — a rename would duplicate the device" ;;
esac
case "$(cat "$TEST")" in
  *'TestB505_RenameUpdatesInPlace'*) ok "B2: a rename updating the same row is pinned by a test" ;;
  *) bad "B2: no test for the rename case" ;;
esac
case "$(cat "$TEST")" in
  *'not idempotent'*) ok "B3: idempotency is asserted behaviourally (same input twice)" ;;
  *) bad "B3: idempotency is only claimed, not tested" ;;
esac

# ── C. removals and the honest unattributed case ────────────────────────────
case "$IMPL_TXT" in
  *'DELETE FROM devices WHERE id = $1'*) ok "C1: a node that left the tailnet loses its row (no ghosts)" ;;
  *) bad "C1: vanished nodes keep their rows" ;;
esac
case "$IMPL_TXT" in
  *'Unattributed'*) ok "C2: a live node with no portal owner is counted, not attributed by guess" ;;
  *) bad "C2: the unowned case is not represented" ;;
esac
case "$(cat "$TEST")" in
  *'TestB505_UnattributedNodeGetsNoRow'*) ok "C3: the unowned case has a test" ;;
  *) bad "C3: no test for an unowned node" ;;
esac
case "$IMPL_TXT" in
  *'created_at'*) ok "C4: the file explains which columns belong to the table itself (created_at)" ;;
  *) bad "C4: the column contract is not written down" ;;
esac

# ── D. behaviour ────────────────────────────────────────────────────────────
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ -run 'B505' -count=1 2>&1)"
  if grep -qE '^(ok|PASS)' <<< "$OUT"; then
    ok "D1: the B505 tests are green (insert, rename, removal, unowned, nil DB)"
  else
    bad "D1: go test ./internal/db/ -run B505 failed:
$(printf '%s\n' "$OUT" | tail -20)"
  fi
else
  skip "D1: no Go toolchain on this host"
fi

# ── E. the one query that names the table ───────────────────────────────────
case "$(cat "$QUERY")" in
  *'B505'*) ok "E1: QSelectUserDevices documents the projection" ;;
  *) bad "E1: $QUERY still describes the table as if nothing maintained it" ;;
esac
case "$(cat "$QUERY")" in
  *'not authoritative'*) ok "E2: the query says the table is not authoritative" ;;
  *) bad "E2: a reader could still mistake the projection for the source of truth" ;;
esac

# ── F. discoverable ─────────────────────────────────────────────────────────
if grep -q 'check_b505_devices_projection.sh' "$GATE"; then
  ok "F1: registered in the pre-deploy catalog"
else
  bad "F1: not registered in $GATE"
fi
if git ls-files --error-unmatch scripts/check_b505_devices_projection.sh >/dev/null 2>&1; then
  ok "F2: git tracks this script (trap #11)"
else
  bad "F2: this script is not tracked by git (trap #11)"
fi
if grep -q 'B505' "$AGENTS"; then
  ok "F3: the AGENTS block index carries its line"
else
  bad "F3: no B505 line in $AGENTS"
fi
for rd in docs/ROADMAP.md docs/ru/ROADMAP.md; do
  if grep -q 'B505' "$rd"; then
    ok "F4: $rd records the block"
  else
    bad "F4: $rd does not mention B505"
  fi
done
if grep -q 'B505' "$NOTES"; then
  ok "F5: the release notes describe it"
else
  bad "F5: no B505 section in $NOTES"
fi

echo
echo "B505 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
