#!/usr/bin/env bash
# check_b275_prefix_assignment.sh — B275: prefix→relay assignment table.
#
# A prefix is served by exactly ONE relay (headscale's primary), so the
# per-rule exit node could not be honoured for both devices. B275 moves
# the decision to skygate: an explicit, sticky, load-aware assignment
# table, a per-CIDR ACL pin that follows the OWNER (not the rule's exit
# node), and an operator-editable pin.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || { printf 'B275: cannot locate cmd/skygate/main.go\n' >&2; exit 2; }

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

PKG=internal/prefixowner/prefixowner.go
MIG=internal/db/migrations_v0_73_prefix_owner.go
ACL=internal/acl/acl.go
SYNC=internal/feature/exit_rules/sync.go

hdr "B275 — one owner per prefix, chosen by skygate"

grep -q 'func Assign(claims \[\]Claim, healthy \[\]string, existing \[\]Existing) \[\]Assignment' "$PKG" && ok "A.1 Assign() exists (pure)" || bad "A.1 Assign() missing"
grep -q 'source        TEXT NOT NULL DEFAULT .auto.' "$MIG" || grep -q "source       TEXT NOT NULL DEFAULT 'auto'" "$MIG" && ok "A.2 prefix_owner carries a source column" || bad "A.2 the table must record the assignment source"
grep -q 'migrateV073PG' internal/db/driver_postgres.go && grep -q 'migrateV073SQLite' internal/db/driver_sqlite.go && ok "A.3 the migration is registered in BOTH chains" || bad "A.3 register v0.73 in both migration chains"
grep -q 'execSQLiteDDL' "$MIG" && ok "A.4 the SQLite side goes through execSQLiteDDL" || bad "A.4 SQLite DDL must use the single chokepoint"

grep -q 'func SetManual(d \*sql.DB, prefix, exitNode string) error' "$PKG" && ok "B.1 SetManual() exists (operator pin)" || bad "B.1 SetManual() missing"
grep -q 'e.Source == "manual"' "$PKG" && ok "B.2 a manual pin is never overwritten while healthy" || bad "B.2 Assign() must preserve manual pins"
grep -q 'load\[e.ExitNode\] <= m+1' "$PKG" && ok "B.3 auto assignments are sticky (no churn without a reason)" || bad "B.3 Assign() must be sticky"

grep -q 'prefixowner.ViaForPrefix(e.TargetValue, ownerTagByPrefix)' "$ACL" && ok "C.1 the per-CIDR pin follows the OWNER" || bad "C.1 the ACL must pin a prefix to its owner"
grep -q 'ownerTagByPrefix := prefixowner.TagByPrefix(d)' "$ACL" && ok "C.2 the owner tags come from the table + node_owner_map" || bad "C.2 load the owner tags in the generator"
# CONTRACT RENEGOTIATION (2026-09-20): C.3 asserted the exact call
# `prefixowner.Reconcile(s.dbc(), nil)`, which B275.2 deliberately replaced with
# `prefixowner.Reconcile(s.dbc(), healthyExitRelays(s.dbc()))`. Passing nil means
# "no relay is healthy", so the engine falls back and every owner it had assigned
# flickers (live: the table went all-`auto` and the operators' prefixes moved);
# the nil form is therefore the regression this contract must catch, not the
# shape it should require. The check now requires the healthy-list form and still
# fails if the reconcile call disappears entirely.
#
# CONTRACT RENEGOTIATION (2026-09-23, B309): the healthy list is now
# `healthyExitRelaysForAssignment(s.dbc())` — B275.2's headscale-derived health
# MINUS the relays whose last route application failed (an online relay skygate
# cannot configure must not keep owning prefixes). The property this contract
# protects is unchanged (the engine is given a real healthy list, never nil), so
# both forms pass; only the nil form fails.
if grep -q 'prefixowner.Reconcile(s.dbc(), healthyExitRelaysForAssignment(s.dbc()))' "$SYNC"; then
  ok "C.3 the sync path reconciles with the transport-aware healthy list (B275.2 + B309)"
elif grep -q 'prefixowner.Reconcile(s.dbc(), healthyExitRelays(s.dbc()))' "$SYNC"; then
  ok "C.3 the sync path reconciles the table with the healthy-relay list (B275.2)"
elif grep -q 'prefixowner.Reconcile(s.dbc(), nil)' "$SYNC"; then
  bad "C.3 the sync path passes a nil healthy-relay list — that is the B275.2 regression (owners flicker)"
else
  bad "C.3 SyncAdvertisedRoutes must call prefixowner.Reconcile"
fi
grep -q 'prefixowner.OwnerByPrefix(s.dbc())' "$SYNC" && ok "C.4 advertising uses the persisted table" || bad "C.4 advertising must read the table"

if command -v go >/dev/null 2>&1; then
  out="$(go test ./internal/prefixowner/ ./internal/acl/ ./internal/feature/exit_rules/ 2>&1)"
  if grep -q '^ok' <<< "$out" && ! grep -q '^FAIL' <<< "$out"; then
    ok "D.1 engine + acl + exit_rules tests pass"
  else
    bad "D.1 go test failed: $(printf '%s' "$out" | tail -n 6)"
  fi
  grep -q 'TestAssign_ExplicitMajorityWins' internal/prefixowner/prefixowner_b275_test.go && \
    grep -q 'TestAssign_ManualPinSurvivesAndStaysPut' internal/prefixowner/prefixowner_b275_test.go && \
    grep -q 'TestAssign_AutoIsLoadBalancedAndSticky' internal/prefixowner/prefixowner_b275_test.go && \
    ok "D.2 the three engine contracts are present" || bad "D.2 engine regression tests missing"
else
  skip "D go toolchain not on PATH"
fi

# live: the table must exist and (after a sync pass) say who owns what
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^skygate-pg-local$'; then
  rows="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -tAc "SELECT COUNT(*) FROM prefix_owner;" 2>/dev/null | tr -d '[:space:]')"
  if [ -n "$rows" ] && [ "$rows" -ge 1 ] 2>/dev/null; then
    ok "E.1 live prefix_owner holds ${rows} assignment(s)"
    dup="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -tAc "SELECT COUNT(*) FROM (SELECT prefix FROM prefix_owner GROUP BY prefix HAVING COUNT(*)>1) d;" 2>/dev/null | tr -d '[:space:]')"
    [ "${dup:-x}" = "0" ] && ok "E.2 every prefix has exactly one owner" || bad "E.2 duplicate owners in prefix_owner: ${dup}"
  else
    skip "E live prefix_owner not populated yet (no sync pass on this host)"
  fi
else
  skip "E PostgreSQL/local docker not available"
fi

printf '\n\033[1mB275: %d PASS, %d FAIL, %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
