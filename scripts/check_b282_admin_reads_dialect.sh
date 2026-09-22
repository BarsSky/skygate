#!/usr/bin/env bash
# check_b282_admin_reads_dialect.sh
#
# 2026-09-22 (B282) — "an operator page must read the database the
# operator actually runs".
#
# LIVE CASE (native host `aro`, SQLite at /var/lib/skygate/skygate.db).
# Three operator surfaces were dead on a SQLite install because the SQL
# was written for PostgreSQL and never branched:
#
#   /admin/audit
#     SQL logic error: unrecognized token: ":" (1)
#     — the UNION over audit_log + cluster_audit used 'audit_log'::text,
#       to_timestamp(created_at), ''::text and detail::text inline. The
#       page that records every silent rewrite (for example
#       `my_exit_rules_apply_preferred preferred=node updated=21`) could
#       not be read at all.
#
#   exit_rules.preferred_mismatch (a /admin/system_tests row)
#     fail: query rules: SQL logic error: unrecognized token: ":" (1)
#     — the join `node_owner_map.node_id = r.device_id::text` needs the
#       cast on PG (text = integer is SQLSTATE 42883 without it) and
#       must not use it on SQLite, where "::" is not a token.
#
#   /admin/headscale/acl
#     list acl: unmarshal policy: json: cannot unmarshal string into Go
#     value of type admin.ACLView
#     — headscale answered GET /api/v1/policy with a STRINGIFIED policy
#       ({"policy":"{…}"}); GetACL cached the quoted literal, so every
#       consumer's json.Unmarshal died. The same trap sat in the
#       exit_rules.all_in_headscale_acl system test.
#
# CONTRACTS
#   A. one dialect text-cast helper exists (db.DialectKind.CastText)
#   B. one tolerant timestamp decoder exists (db.ParseDBTime)
#   C. /admin/audit builds its statement through a dialect-aware builder
#   D. the preferred-mismatch query asks the dialect for its cast
#   E. the policy is normalised in ONE place (headscale.PolicyJSON)
#   F. the SQLite forms carry no PostgreSQL-only token
#   G. the B282 Go regression tests exist and pass
#   H. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B282: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DIALECT=internal/db/dialect.go
DBTIME=internal/db/db_time.go
PAGES=internal/feature/admin/admin_pages.go
SYSTESTS=internal/feature/admin/system_tests.go
HACL=internal/feature/admin/headscale_acl.go
ACL=internal/headscale/acl.go
POLJSON=internal/headscale/policy_json.go
POLEQ=internal/headscale/policy_equivalent_b276.go
AUDITTEST=internal/feature/admin/b282_sqlite_reads_test.go
DBTEST=internal/db/dialect_b282_test.go
HSTEST=internal/headscale/policy_b282_test.go

hdr "B282 — admin reads on both dialects (SQLite + PostgreSQL)"

# --- A: one dialect text-cast helper ----------------------------------------
if [ -f "$DIALECT" ] && grep -q 'func (k DialectKind) CastText(' "$DIALECT"; then
  ok "A1: db.DialectKind.CastText exists (one cast, two dialects)"
else
  bad "A1: CastText is missing from $DIALECT — the next caller will type the PG shorthand again"
fi
if grep -q 'CAST(%s AS TEXT)' "$DIALECT"; then
  ok "A2: the SQLite form is CAST(<expr> AS TEXT)"
else
  bad "A2: the SQLite cast form is missing"
fi

# --- B: one tolerant timestamp decoder --------------------------------------
if [ -f "$DBTIME" ] && grep -q '^func ParseDBTime(' "$DBTIME"; then
  ok "B1: db.ParseDBTime exists (a time.Time-only scan cannot read SQLite timestamps)"
else
  bad "B1: $DBTIME / ParseDBTime is missing — SQLite INTEGER and TEXT timestamps stay unreadable"
fi
for shape in 'time.Time' 'int64' 'float64' '\[\]byte'; do
  if grep -q "$shape" "$DBTIME" 2>/dev/null; then
    ok "B2: ParseDBTime handles $shape"
  else
    bad "B2: ParseDBTime does not handle $shape"
  fi
done

# --- C: /admin/audit builds through the dialect-aware builder ---------------
if grep -q '^func buildUnifiedAuditQuery(' "$PAGES"; then
  ok "C1: buildUnifiedAuditQuery exists in admin_pages.go"
else
  bad "C1: buildUnifiedAuditQuery is missing — /admin/audit is back to inline PG-only SQL"
fi
if grep -q 'buildUnifiedAuditQuery(db.ActiveDialect()' "$PAGES"; then
  ok "C2: the handler passes the live dialect into the builder"
else
  bad "C2: the handler does not pass db.ActiveDialect() — the builder would guess"
fi
if grep -q 'case db.DialectSQLite:' "$PAGES"; then
  ok "C3: the builder branches on the SQLite dialect"
else
  bad "C3: the builder has no SQLite branch"
fi
if grep -q 'db.ParseDBTime(tsRaw)' "$PAGES"; then
  ok "C4: the row scan decodes the timestamp through db.ParseDBTime"
else
  bad "C4: the audit row scan does not use db.ParseDBTime (a time.Time scan fails on SQLite)"
fi
# The old inline PG-only shape must be gone. Comments may still quote it,
# so only non-comment lines count.
for token in "'audit_log'::text AS source" "to_timestamp(created_at) AS ts" "detail::text AS detail"; do
  hits=$(grep -nF "$token" "$PAGES" | grep -v ':[[:space:]]*//' || true)
  if [ -z "$hits" ]; then
    ok "C5: the inline PostgreSQL form [$token] is gone from admin_pages.go"
  else
    bad "C5: admin_pages.go still contains [$token] outside a comment: $hits"
  fi
done

# --- D: the preferred-mismatch query asks the dialect ------------------------
if grep -q '^func preferredMismatchRulesQuery(' "$SYSTESTS"; then
  ok "D1: preferredMismatchRulesQuery exists (the cast is dialect-native)"
else
  bad "D1: preferredMismatchRulesQuery is missing — the live failure was this join"
fi
hits=$(grep -nF 'r.device_id::text' "$SYSTESTS" | grep -v ':[[:space:]]*//' || true)
if [ -z "$hits" ]; then
  ok "D2: no hardcoded r.device_id::text left in system_tests.go code"
else
  bad "D2: system_tests.go still hardcodes the PG cast outside a comment: $hits"
fi
if grep -q 'db.ActiveDialect()' "$SYSTESTS"; then
  ok "D3: the system test resolves the dialect at runtime"
else
  bad "D3: the system test does not consult db.ActiveDialect()"
fi

# --- E: the policy is normalised in exactly one place -----------------------
if [ -f "$POLJSON" ] && grep -q '^func PolicyJSON(' "$POLJSON"; then
  ok "E1: headscale.PolicyJSON exists (unquote + hujson.Standardize)"
else
  bad "E1: headscale.PolicyJSON is missing"
fi
if grep -q 'unquotePolicyIfStringified(raw)' "$ACL"; then
  ok "E2: GetACL unquotes a stringified policy field (the live ACL-page 500)"
else
  bad "E2: GetACL caches the QUOTED policy — consumers fail with cannot unmarshal string"
fi
if grep -q 'headscale.PolicyJSON(rawPolicy)' "$HACL"; then
  ok "E3: /admin/headscale/acl normalises the policy before unmarshalling"
else
  bad "E3: ListACL still json.Unmarshals the raw policy field"
fi
hits=$(grep -nF 'json.Unmarshal([]byte(rawPolicy), view)' "$HACL" | grep -v ':[[:space:]]*//' || true)
if [ -z "$hits" ]; then
  ok "E4: the pre-B282 unmarshal of the raw policy is gone"
else
  bad "E4: the raw-policy unmarshal is back: $hits"
fi
if grep -q 'headscale.PolicyJSON(policyJSON)' "$SYSTESTS"; then
  ok "E5: exit_rules.all_in_headscale_acl parses the policy through PolicyJSON"
else
  bad "E5: the ACL system test parses the raw policy field (same trap as the page)"
fi
if grep -q 'PolicyJSON(policy)' "$POLEQ"; then
  ok "E6: PolicyEquivalent shares the same normaliser (no second copy)"
else
  bad "E6: PolicyEquivalent has its own normaliser again"
fi

# --- F: the new helpers stay dialect-clean ----------------------------------
if [ -f "$DBTIME" ] && ! grep -q '::' "$DBTIME"; then
  ok "F1: $DBTIME contains no PG cast shorthand"
else
  bad "F1: $DBTIME contains a :: cast"
fi
if grep -q 'CAST(' "$DIALECT" && ! grep -q 'db.ActiveDialect()' "$DIALECT"; then
  ok "F2: the cast helper itself is dialect-parameterised (no global lookup inside it)"
else
  bad "F2: the cast helper reads the process-wide dialect instead of its receiver"
fi

# --- G: the regression tests exist and pass ---------------------------------
for tf in "$DBTEST" "$HSTEST" "$AUDITTEST"; do
  if [ -f "$tf" ]; then
    ok "G1: $tf exists"
  else
    bad "G1: $tf is missing"
  fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ -run 'B282' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "G2: the B282 db tests pass"
  else
    bad "G2: the B282 db tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/headscale/ -run 'B282' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "G3: the B282 headscale tests pass"
  else
    bad "G3: the B282 headscale tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/feature/admin/ -run 'B282' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "G4: the B282 admin tests pass (real SQLite: the audit UNION and the preferred-mismatch join run)"
  else
    bad "G4: the B282 admin tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "G2-G4: go not on PATH — run the B282 tests on the VM"
fi

# --- H: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b282_admin_reads_dialect.sh >/dev/null 2>&1; then
  ok "H1: scripts/check_b282_admin_reads_dialect.sh is tracked by git"
else
  bad "H1: scripts/check_b282_admin_reads_dialect.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB282 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
