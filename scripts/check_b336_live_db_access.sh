#!/usr/bin/env bash
# check_b336_live_db_access.sh
#
# 2026-10-01 (B336) — a shell check must not report a READ FAILURE as a FACT
# about the database.
#
# THE MEASUREMENT
# ---------------
# The 2026-10-01 baseline gate run on the reference VM ended with 10 FAILs. Six
# of them came from four live-state contracts that had never reached the
# database:
#
#   FAIL  B118  live policy:  references to svyatoslava-legacy   ← empty count
#   FAIL  B118  nom:  rows for tag:dev-infra-…
#   FAIL  B118  tagOwners:  entries for tag:dev-infra-…
#   FAIL  B189  B.4 derp_health table NOT in live DB
#   FAIL  B190  A.1 found  b188_* users in portal_users (expected 0)
#
# Note the EMPTY counts: `${cnt}` was never assigned. The cause is a design
# decision the project made on purpose — B278 removed the docker bridge IP from
# SKYGATE_DB_DSN and made the DSN host a docker DNS name (`skygate-pg-local`),
# because the bridge IP rotates on every container recreate. That name resolves
# only INSIDE the compose network, so a host-side `psql "$DSN"` answers:
#
#   psql: error: could not translate host name "skygate-pg-local" to address
#
# and every one of those checks sent stderr to /dev/null and then treated the
# empty result as data. Measured through the new helper on the same deployment:
#
#   derp_health            = derp_health      (the table IS there)
#   svyatoslava refs       = 0
#   tag:dev-infra-* owners = 4
#   node_owner_map infra   = 4
#   b188_* debris          = 0
#
# i.e. every one of those FAILs was fabricated; the contracts all hold. That is
# the B294 lesson (a failed read must not render as a fact) applied to the shell
# checks, and it violated AGENTS rule 1: a check whose dependency is unavailable
# must SKIP, never FAIL.
#
# THE FIX
# -------
# `scripts/lib/db_credentials.sh` now carries a live-DB access layer:
# `skygate_live_db_probe` runs psql INSIDE the container when the DSN host names
# one (no host networking, immune to the rotation B278 is about, and exactly
# what docs/operations.md §15 tells the operator to do by hand), falls back to a
# plain DSN for external PostgreSQL (Patroni, deploy/pg-ha), and on failure
# records the REAL error in `SKYGATE_LIVE_DB_ERR`. `skygate_live_db_query` is the
# only way those checks read the database, and it refuses to run without a
# successful probe, so "zero rows" can no longer mean "no connection".
#
# Two VACUOUS contracts surfaced in the same pass and are fixed here:
#   * B119's predicate ended with `AND exit_node_tag NOT LIKE '%'` — true for
#     every value, so its count was always 0 and the contract could not fail;
#   * B118's contract G searched the SQL for the literal placeholder
#     `<polygon-vm-hostname>` (an earlier redaction put it inside the SQL
#     strings), a string that cannot exist in any database. It now asserts that
#     every `tag:dev-infra-*` is one of the four sanctioned relays, which CAN
#     fail and catches the retired HA mirror coming back.
#
# B188.2 contract X additionally pointed at a hardcoded, long-dead
# `172.17.0.1:5000`; it now goes through the same helper (measured: rules=129,
# via=129, diff=0).
#
# CONTRACTS
#   A. the shared helper exists, parses both DSN forms, and refuses to query
#      without a probe
#   B. BEHAVIOURAL: an unreachable database makes the probe fail with the real
#      error, and the query helper then returns nothing (never a zero)
#   C. every live-state contract probes before it queries
#   D. the vacuous predicates are gone
#   E. the stale hardcoded address is gone
#   F. tracked by git (AGENTS trap #11) and registered in the catalog

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B336: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LIB=scripts/lib/db_credentials.sh
LIVE_CHECKS=(scripts/check_b118.sh scripts/check_b119.sh scripts/check_b189.sh scripts/check_b190.sh scripts/check_b188_2.sh)

hdr "B336 — a live-state check must not report a read failure as a fact"

# ---------------------------------------------------------------------------
hdr "A. the shared live-DB access layer"
# ---------------------------------------------------------------------------
for fn in skygate_db_dsn skygate_live_db_probe skygate_live_db_reason skygate_live_db_query; do
  if grep -qE "^${fn}\(\)" "$LIB"; then
    ok "A1: $LIB defines $fn"
  else
    bad "A1: $LIB does not define $fn — the live-state checks have no honest way to read the database"
  fi
done

# Both DSN shapes must parse: a docker service name and an external host.
PARSE="$(bash -c '
  . ./'"$LIB"' 2>/dev/null || exit 9
  d1="postgres://admin:pw@skygate-pg-local:5432/skygate_staging"
  d2="postgres://skygate:skygate@127.0.0.1:5432/skygate_test?sslmode=disable"
  printf "%s|%s|%s|%s\n" "$(_skygate_dsn_field "$d1" host)" "$(_skygate_dsn_field "$d1" db)" "$(_skygate_dsn_field "$d2" host)" "$(_skygate_dsn_field "$d2" db)"
' 2>/dev/null)"
if [ "$PARSE" = "skygate-pg-local|skygate_staging|127.0.0.1|skygate_test" ]; then
  ok "A2: both DSN forms parse (docker service name without and with a query string)"
else
  bad "A2: DSN parsing is wrong: got '$PARSE', want 'skygate-pg-local|skygate_staging|127.0.0.1|skygate_test'"
fi

# The query helper must REFUSE to run without a successful probe. This is the
# contract that makes "zero rows" impossible to confuse with "no connection".
NORUN="$(bash -c '. ./'"$LIB"' 2>/dev/null; skygate_live_db_query "SELECT 1"; printf "rc=%s" "$?"' 2>/dev/null)"
if printf '%s' "$NORUN" | grep -q 'rc=[1-9]' && ! printf '%s' "$NORUN" | grep -qE '^[0-9]+rc='; then
  ok "A3: the query helper refuses to run before a successful probe (no mode set)"
else
  bad "A3: the query helper ran without a probe: '$NORUN' — that is how an unreachable database becomes a zero row count"
fi

# A connection failure is not the only kind of read failure: a SQL error used to
# be swallowed by `2>/dev/null` too and came back as an empty result — the same
# fabricated fact. Measured while renegotiating B188.2's contract U: a malformed
# `GROUP BY 1` read as "no rows" until the error was surfaced.
# Scoped to the QUERY helper's body: the probe deliberately discards the query
# output and captures stderr separately, but a query must never discard stderr.
QFN="$(awk '/^skygate_live_db_query\(\)/{f=1} f{print} f&&/^}/{exit}' "$LIB" 2>/dev/null)"
if [ -z "$QFN" ]; then
  bad "A4: could not extract skygate_live_db_query from $LIB"
elif printf '%s' "$QFN" | grep -qE 'psql[^|]*2>/dev/null'; then
  bad "A4: skygate_live_db_query still sends psql's stderr to /dev/null — a SQL error would read as an empty result"
else
  ok "A4: skygate_live_db_query keeps psql's stderr, so a failed statement is not an empty one"
fi
if grep -q 'SKYGATE_LIVE_DB_QUERY_ERR' "$LIB"; then
  ok "A4b: the query helper records why a statement failed in SKYGATE_LIVE_DB_QUERY_ERR"
else
  bad "A4b: the query helper has no error channel, so a failed statement is indistinguishable from zero rows"
fi
A4C="$(bash -c '. ./'"$LIB"' 2>/dev/null; skygate_live_db_query "SELECT 1" >/dev/null 2>&1; printf "rc=%s err=%s" "$?" "${SKYGATE_LIVE_DB_QUERY_ERR:-EMPTY}"' 2>/dev/null)"
if printf '%s' "$A4C" | grep -q 'rc=[1-9]' && ! printf '%s' "$A4C" | grep -q 'err=EMPTY$'; then
  ok "A4c: a failed statement returns non-zero AND names the reason"
else
  bad "A4c: a failed statement did not report both a non-zero status and a reason: $A4C"
fi

# ---------------------------------------------------------------------------
hdr "B. behavioural — an unreachable database is named, not guessed"
# ---------------------------------------------------------------------------
# Force a DSN whose host is neither a container nor resolvable, so the probe has
# to fail for a real reason on ANY host (including CI, which has no .env).
PROBE_OUT="$(SKYGATE_DB_DSN='postgres://nobody:nothing@skygate-pg-no-such-host.invalid:5432/nodb' \
  bash -c '. ./'"$LIB"' 2>/dev/null
    if skygate_live_db_probe; then echo "PROBE-OK"; else echo "PROBE-FAIL"; skygate_live_db_reason; fi
    echo "query=[$(skygate_live_db_query "SELECT 1")]"' 2>&1)"
if printf '%s' "$PROBE_OUT" | grep -q 'PROBE-FAIL'; then
  ok "B1: an unreachable live database makes the probe fail"
else
  bad "B1: the probe reported success for a host that cannot resolve: $PROBE_OUT"
fi
if printf '%s' "$PROBE_OUT" | grep -qivE '^(PROBE-FAIL|query=\[\])$' && printf '%s' "$PROBE_OUT" | grep -qiE 'not installed|no such host|not resolve|not known|name or service|failed|could not|unreachable|no SKYGATE_DB_DSN'; then
  ok "B2: the failure NAMES the real error instead of inventing a fact"
else
  bad "B2: the probe failed without naming a reason: $PROBE_OUT"
fi
if printf '%s' "$PROBE_OUT" | grep -q 'query=\[\]'; then
  ok "B3: with the probe failed, the query helper returned NOTHING (not a zero)"
else
  bad "B3: the query helper returned data for an unreachable database: $PROBE_OUT"
fi

# ---------------------------------------------------------------------------
hdr "C. every live-state contract probes before it queries"
# ---------------------------------------------------------------------------
for f in "${LIVE_CHECKS[@]}"; do
  if [ ! -f "$f" ]; then
    bad "C1: $f is missing"
    continue
  fi
  if ! grep -q 'skygate_live_db_probe' "$f"; then
    bad "C1: $f queries the live DB without probing — a read failure would be reported as a fact"
    continue
  fi
  if grep -qE 'psql .*-h "\$\{?host' "$f"; then
    bad "C1: $f still builds a host-side psql call from the DSN host (the B278 docker DNS name does not resolve from the host)"
    continue
  fi
  ok "C1: $f probes first and reads through the helper"
done

# B188.2's contract U was RENEGOTIATED against the assignment table (B275):
# the old form asserted "skyworker must never carry via=emilia" — the pre-B275
# design — while the data plane (prefix_owner, source=explicit) and the control
# plane (the live policy) agree exactly: 77 emilia / 123 karolina on BOTH sides.
if grep -q 'prefix_owner' scripts/check_b188_2.sh; then
  ok "C3: check_b188_2.sh's U contract compares the live pins against prefix_owner (the B275 assignment table) instead of the rule's own exit_node_id"
else
  bad "C3: check_b188_2.sh does not consult prefix_owner — its U contract still encodes the pre-B275 premise"
fi
if grep -qE 'U_RULES=.*skygate_live_db_query' scripts/check_b188_2.sh && \
   grep -B4 'U_RULES=' scripts/check_b188_2.sh | grep -q 'skygate_live_db_probe'; then
  ok "C4: U probes before it queries (a query before the probe returns empty and would SKIP for the wrong reason — measured)"
else
  bad "C4: U queries before probing — the first version of this fix SKIPped with rules='' for that exact reason"
fi

# The probe must precede the guard, not be decorative: every guarded query sits
# in a file that also fails over to a SKIP.
for f in "${LIVE_CHECKS[@]}"; do
  [ -f "$f" ] || continue
  if grep -qE 'skygate_live_db_reason' "$f"; then
    ok "C2: $f reports the probe's reason on the SKIP path"
  else
    bad "C2: $f never prints the failure reason — the operator would see a silent skip"
  fi
done

# ---------------------------------------------------------------------------
hdr "D. the vacuous predicates are gone"
# ---------------------------------------------------------------------------
if grep -v '^[[:space:]]*#' scripts/check_b119.sh | grep -q "exit_node_tag NOT LIKE '%'"; then
  bad "D1: check_b119.sh still ends its predicate with NOT LIKE '%' — true for every value, so the contract can never fail"
else
  ok "D1: check_b119.sh no longer carries the always-true NOT LIKE '%' predicate"
fi
if grep -v '^[[:space:]]*#' scripts/check_b118.sh | grep -qE "ILIKE '%<[a-z-]+>'|tag:dev-infra-<"; then
  bad "D2: check_b118.sh still searches the SQL for a literal <placeholder> hostname — that string cannot exist in any database, so those contracts are vacuous"
else
  ok "D2: check_b118.sh no longer searches for a placeholder hostname in SQL"
fi
if grep -q 'SANCTIONED=' scripts/check_b118.sh; then
  ok "D3: check_b118.sh asserts against the SANCTIONED relay set instead, which can actually fail"
else
  bad "D3: check_b118.sh has no sanctioned-set contract to replace the vacuous placeholder search"
fi

# A check must not build to a FIXED path under /tmp. Measured 2026-10-01: three
# checks (B3, B18, B90) wrote their build output to fixed names, so after a gate
# run under a DIFFERENT user the file was root-owned and the next run failed with
# "open /tmp/x: permission denied" — a red row that says nothing about what the
# check tests. mktemp removes the shared name and therefore the whole class.
FIXED_TMP="$(grep -oE '\-o /tmp/[A-Za-z0-9_.-]+' scripts/verify_pre_deploy.sh | sort -u | tr '\n' ' ')"
if [ -z "$FIXED_TMP" ]; then
  ok "D4: no check in the catalog builds to a fixed /tmp path (mktemp instead — the path cannot be pre-owned by another user's run)"
else
  bad "D4: the catalog still builds to fixed /tmp paths: $FIXED_TMP — a stale file owned by another user makes the check fail for a reason unrelated to what it tests"
fi

# ---------------------------------------------------------------------------
hdr "E. the stale hardcoded database address is gone"
# ---------------------------------------------------------------------------
if grep -v '^[[:space:]]*#' scripts/check_b188_2.sh | grep -q '172\.17\.0\.1'; then
  bad "E1: check_b188_2.sh still points at the hardcoded 172.17.0.1 — an address no install uses, so the query silently returned nothing"
else
  ok "E1: check_b188_2.sh no longer reaches the live DB at a hardcoded address"
fi

# ---------------------------------------------------------------------------
hdr "F. tracked by git and registered in the catalog"
# ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b336_live_db_access.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: scripts/check_b336_live_db_access.sh is NOT tracked by git (AGENTS trap #11: .gitignore can eat a new script)"
fi
if grep -q 'run_check "B336"' scripts/verify_pre_deploy.sh; then
  ok "F2: scripts/verify_pre_deploy.sh registers B336"
else
  bad "F2: scripts/verify_pre_deploy.sh does not register B336 — the contract would never run"
fi
if grep -q '\*\*B336\*\*' AGENTS.md; then
  ok "F3: AGENTS.md's block index carries B336"
else
  bad "F3: AGENTS.md has no B336 entry (AGENTS rule 2)"
fi

printf '\n\033[1mB336 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
