#!/usr/bin/env bash
# check_b291_cluster_sqlite.sh
#
# 2026-09-23 (B291) — «в кластере на native-хосте ничего не работает: каждые
# 5 минут ошибка, и страницы /admin/cluster + /admin/ha пустые».
#
# LIVE REPORT (operator, native host `aro`, SQLite /var/lib/skygate/skygate.db):
#
#   cluster.discovery.error  cluster_node:workpc  error="insert discovered node:
#                            SQL logic error: near \"['skygate-standby']\": syntax
#                            error"                       ... EVERY 5 MINUTES (137 rows)
#   /admin/cluster           no nodes, no invites, no events
#   /admin/ha                no nodes, no events
#
# The whole cluster/HA tree was written for PostgreSQL and was therefore DEAD on
# SQLite. Every write path was PostgreSQL-shaped:
#
#   ARRAY['skygate-standby']::text[]      discovery.go       → near "[…]": syntax error
#   '[]'::jsonb                           cluster.go
#   'node-' || substr(md5(random()::text), 1, 12)            → no such function: md5
#   NOW()                                 node.go / join.go  → no such function: NOW
#   SELECT … FOR UPDATE                   node.go (×4)       → near "FOR": syntax error
#   'skygate' = ANY (roles)               failover/drill     → no such function: ANY
#   roles || ARRAY['skygate']::text[]     failover/drill
#   array_remove(roles, 'skygate')        failover/drill/CLI
#   array_to_string(roles, ',')           node.go            → no such function
#   $N::jsonb                             cluster_audit/elector
#   detail->>'reason'                     ha.go              → unrecognised token
#   created_at > NOW() - INTERVAL '5 min' elector
#   extract(epoch FROM last_seen_at)::bigint                 → unrecognised token
#
# The READ side had the same class of leak, which is why the pages looked
# "empty" instead of "broken": cluster_invite `expires_at > NOW()`,
# cluster_audit `extract(epoch …)::bigint` + `detail::text`, and
# joined_at/last_seen_at scanned straight into sql.NullTime (SQLite stores a
# bound time.Time in Go's String() form, which NullTime refuses — the row was
# dropped silently).
#
# Plus one bug that was broken on BOTH backends: the /admin/ha event union read
# `unix_timestamp` and `actor` from audit_log — neither column exists (the table
# has created_at and username), so the whole pre-B195 half of that history was
# silently missing everywhere, PostgreSQL included.
#
# CONTRACTS
#   A. the dialect helpers the port is built on exist and are used
#   B. no PostgreSQL-only SQL is left in the cluster/HA tree (comments excluded)
#   C. timestamps are decoded tolerantly on read and bound via the dialect on write
#   D. the /admin/ha event query names real audit_log columns
#   E. the `skygate cluster failover` CLI goes through the same helpers
#   F. the regression tests exist and pass on a real SQLite database
#   G. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B291: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

HELPERS=internal/db/cluster_sql_b291.go
DISCOVERY=internal/cluster/discovery.go
NODE=internal/cluster/node.go
JOIN=internal/cluster/join.go
INVITE=internal/cluster/invite.go
CLUSTER_CLI=cmd/skygate/cluster.go
ADMIN_CLUSTER=internal/feature/admin/cluster.go
ADMIN_HA=internal/feature/admin/ha.go
ELECTOR=internal/elector/elector.go
FAILOVER=internal/db/cluster_failover.go
DRILL=internal/db/cluster_drill.go

# The cluster/HA tree, minus the dialect-helper file itself (whose whole point
# is to CONTAIN the PostgreSQL spellings behind a switch).
TREE="
internal/cluster/cluster.go
internal/cluster/discovery.go
internal/cluster/invite.go
internal/cluster/join.go
internal/cluster/node.go
internal/cluster/upgrade.go
internal/db/cluster.go
internal/db/cluster_audit.go
internal/db/cluster_drill.go
internal/db/cluster_failover.go
internal/db/cluster_patroni.go
internal/elector/elector.go
internal/feature/admin/cluster.go
internal/feature/admin/ha.go
cmd/skygate/cluster.go
"

# pg_only_hits <file> — PostgreSQL-only SQL in a Go file, IGNORING `//` comment
# lines. The port's own documentation quotes the old statements on purpose, so a
# naive grep would always "find" them (the same trap B288 hit).
pg_only_hits() {
  grep -vE '^[[:space:]]*//' "$1" 2>/dev/null \
    | grep -nE '::text|::jsonb|::bigint|NOW\(\)|extract\(|to_timestamp|ARRAY\[|array_to_string|array_remove|ANY[[:space:]]*\(roles|FOR UPDATE|substr\(md5\(|INTERVAL' \
    || true
}

hdr "B291 — the cluster/HA tree must work on SQLite"

# --- A: the dialect helpers ---------------------------------------------------
if [ -f "$HELPERS" ]; then
  ok "A1: $HELPERS exists"
else
  bad "A1: $HELPERS is missing"
fi
for fn in NowExpr NowMinusExpr CastJSON JSONField CastTextArray RandomHexExpr ForUpdateExpr TimeValue; do
  if grep -q "func (k DialectKind) $fn(" "$HELPERS"; then
    ok "A2.$fn: DialectKind.$fn exists"
  else
    bad "A2.$fn: DialectKind.$fn is missing"
  fi
done
for fn in TextArrayLiteral RolesContain RolesAdd RolesRemove FindClusterPrimary NodeRoles SetNodeRoles; do
  if grep -q "^func $fn(" "$HELPERS"; then
    ok "A3.$fn: $fn exists"
  else
    bad "A3.$fn: $fn is missing"
  fi
done
if grep -q 'if k == DialectPostgres {' "$HELPERS" && grep -q 'return ""' "$HELPERS"; then
  ok "A4: ForUpdateExpr renders the lock only for PostgreSQL (SQLite has no FOR UPDATE)"
else
  bad "A4: ForUpdateExpr has no dialect switch"
fi
if grep -q 'func (k DialectKind) TimeValue(' "$HELPERS" && grep -q 't.UTC().Format(time.RFC3339Nano)' "$HELPERS"; then
  ok "A5: TimeValue writes a shape both readers understand (not Go's String() form)"
else
  bad "A5: TimeValue does not canonicalise the SQLite representation"
fi
if grep -q 'FindClusterPrimary(tx)' "$FAILOVER" && grep -q 'FindClusterPrimary(tx)' "$DRILL"; then
  ok "A6: the failover and the drill both resolve the primary through FindClusterPrimary"
else
  bad "A6: failover/drill still resolve the primary with a PostgreSQL predicate"
fi
if grep -q 'SetNodeRoles(tx, targetID' "$FAILOVER" && grep -q 'RolesRemove(fromRoles, "skygate")' "$FAILOVER" && \
   grep -q 'RolesContain(' "$HELPERS"; then
  ok "A7: the promote/demote role surgery happens in Go (exact match, not LIKE)"
else
  bad "A7: the role surgery is not the shared Go implementation"
fi

# --- B: no PostgreSQL-only SQL left in the tree -------------------------------
leftover=""
for f in $TREE; do
  [ -f "$f" ] || continue
  hits="$(pg_only_hits "$f")"
  if [ -n "$hits" ]; then
    leftover="${leftover}${f}:
${hits}
"
  fi
done
if [ -z "$leftover" ]; then
  ok "B1: no PostgreSQL-only SQL remains outside comments in the cluster/HA tree"
else
  bad "B1: PostgreSQL-only SQL is still live in the cluster/HA tree:"
  printf '%s' "$leftover" | sed 's/^/       /' >&2
fi
if grep -q "CastTextArray(\"\$5\")" "$DISCOVERY" && grep -q 'TextArrayLiteral(\[\]string{"skygate-standby"})' "$DISCOVERY"; then
  ok "B2: the discovery INSERT binds a dialect-native roles literal (the live error)"
else
  bad "B2: discovery.go still writes ARRAY['skygate-standby']::text[]"
fi
if grep -q 'db.ActiveDialect().TimeValue(' "$NODE" && grep -q 'db.ActiveDialect().RandomHexExpr(12)' "$NODE"; then
  ok "B3: the node writers bind timestamps and generate ids dialect-natively"
else
  bad "B3: node.go still uses NOW() / md5(random()::text)"
fi
if grep -q 'ForUpdateExpr()' "$NODE"; then
  ok "B4: the four SELECT … FOR UPDATE sites go through ForUpdateExpr"
else
  bad "B4: node.go still hardcodes FOR UPDATE (a syntax error on SQLite)"
fi
if grep -q 'RandomHexExpr(12)' "$NODE" && ! grep -q 'random()::text' "$NODE"; then
  ok "B5: the generated node id no longer depends on md5(random())"
else
  bad "B5: the generated node id is still PostgreSQL-only"
fi

# --- C: timestamp decoding ----------------------------------------------------
for spec in "$NODE:joinedRaw, lastSeenRaw:scanNode" "$INVITE:issuedRaw, expiresRaw, usedAtRaw:LookupInvite" \
            "$JOIN:lastSeenRaw:heartbeat" "$ELECTOR:lastSeenRaw, joinedRaw:evaluate" \
            "$ADMIN_CLUSTER:joinedRaw, lastSeenRaw:node list" "$ADMIN_HA:lastSeenRaw:cluster node list"; do
  f="${spec%%:*}"; rest="${spec#*:}"; vars="${rest%%:*}"; label="${rest#*:}"
  if grep -q "$vars" "$f" && grep -q 'ParseDBTime' "$f"; then
    ok "C.$label: $(basename "$f") scans raw values and decodes them with ParseDBTime"
  else
    bad "C.$label: $(basename "$f") scans a timestamp straight into time.Time/NullTime"
  fi
done
if grep -q 'created_at, *$' "$ADMIN_HA" || grep -q 'created_at,' "$ADMIN_HA"; then
  ok "C.ha-events: the cluster_audit branch reads created_at and decodes it"
else
  bad "C.ha-events: the cluster_audit branch does not read created_at"
fi
if grep -q "CastText(\"detail\")" "$ADMIN_HA" && grep -q 'JSONField("detail", "reason")' "$ADMIN_HA"; then
  ok "C.ha-detail: the events query uses the dialect JSON helpers"
else
  bad "C.ha-detail: the events query still uses detail::text / detail->>'reason'"
fi
if grep -q 'CastText("detail")' "$ADMIN_CLUSTER"; then
  ok "C.cluster-detail: /admin/cluster reads cluster_audit.detail dialect-natively"
else
  bad "C.cluster-detail: /admin/cluster still uses detail::text"
fi
if grep -q 'now := time.Now()' "$ADMIN_CLUSTER" && grep -q 'expiresAt.After(now)' "$ADMIN_CLUSTER"; then
  ok "C.invites: the pending-invite expiry filter runs in Go (SQLite has no NOW())"
else
  bad "C.invites: the invite expiry filter is still a SQL NOW() comparison"
fi
if grep -q 'ParseDBTime(createdRaw)' "$ELECTOR" && grep -q 'now.Sub(ts) > 5\*time.Minute' "$ELECTOR"; then
  ok "C.elector-dedup: the 5-minute dedup window is computed in Go"
else
  bad "C.elector-dedup: the dedup still uses detail->>… + INTERVAL"
fi

# --- D: the /admin/ha event union uses real columns ---------------------------
if grep -q 'SELECT id, created_at, username, action, detail' "$ADMIN_HA"; then
  ok "D1: the audit_log branch selects created_at + username (the real columns)"
else
  bad "D1: the audit_log branch does not select created_at + username"
fi
if grep -vE '^[[:space:]]*//' "$ADMIN_HA" | grep -q 'unix_timestamp'; then
  bad "D2: the audit_log branch still selects unix_timestamp — that column exists in NO schema"
else
  ok "D2: the non-existent unix_timestamp column is gone"
fi
if grep -q '^	id             INTEGER PRIMARY KEY AUTOINCREMENT' internal/db/migrations_sqlite.go || \
   grep -q 'CREATE TABLE IF NOT EXISTS audit_log (' internal/db/migrations_sqlite.go; then
  ok "D3: the audit_log DDL is present to cross-check the column names"
else
  bad "D3: audit_log DDL not found in the SQLite migration chain"
fi

# --- E: the CLI failover ------------------------------------------------------
if grep -q 'db.NodeRoles(tx, targetID)' "$CLUSTER_CLI" && grep -q 'db.SetNodeRoles(tx, targetID' "$CLUSTER_CLI" && \
   grep -q 'db.RolesAdd(rolesForTarget, "skygate-standby")' "$CLUSTER_CLI"; then
  ok "E1: the CLI promotes through the shared role helpers (other roles survive)"
else
  bad "E1: the CLI still rewrites roles with a PostgreSQL ARRAY(...) expression"
fi
if grep -q "db.ActiveDialect().CastJSON(\"\$3\")" "$CLUSTER_CLI"; then
  ok "E2: the CLI audit INSERT casts JSON per dialect"
else
  bad "E2: the CLI audit INSERT still uses \$3::jsonb"
fi
if grep -q 'db.StringArray' "$CLUSTER_CLI" && grep -q 'db.RolesContain(\[\]string(roles), "skygate")' "$CLUSTER_CLI"; then
  ok "E3: the CLI finds the failed primary with an exact-match Go predicate"
else
  bad "E3: the CLI still looks for the failed primary with ANY(roles)"
fi

# --- F: the regression tests --------------------------------------------------
TESTS="internal/cluster/cluster_sqlite_b291_test.go internal/elector/elector_sqlite_b291_test.go internal/feature/admin/cluster_ha_sqlite_b291_test.go internal/db/cluster_sql_b291_test.go"
for t in $TESTS; do
  if [ -f "$t" ]; then
    ok "F1: $t exists"
  else
    bad "F1: $t is missing"
  fi
done
if grep -q 'TestB291_DiscoveryInsertWorksOnSQLite' internal/cluster/cluster_sqlite_b291_test.go 2>/dev/null; then
  ok "F2: the discovery regression is pinned (this is the live error seen every 5 minutes)"
else
  bad "F2: the discovery regression test is not pinned"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/cluster/ ./internal/elector/ ./internal/feature/admin/ ./internal/db/ -run 'B291' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F3: the B291 tests pass on a real SQLite database"
  else
    bad "F3: the B291 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "F3: go not on PATH — run the B291 tests on the VM"
fi
if grep -q 'newB291SQLite' internal/cluster/cluster_sqlite_b291_test.go 2>/dev/null && \
   grep -q 'ApplyMigrations' internal/cluster/cluster_sqlite_b291_test.go 2>/dev/null; then
  ok "F4: the cluster tests run against a MIGRATED database, not a hand-made table"
else
  bad "F4: the cluster tests do not apply the real migrations"
fi

# --- G: tracked by git (trap #11) --------------------------------------------
if git ls-files --error-unmatch scripts/check_b291_cluster_sqlite.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b291_cluster_sqlite.sh is tracked by git"
else
  bad "G1: scripts/check_b291_cluster_sqlite.sh is NOT tracked"
fi

printf '\n\033[1mB291 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
