#!/usr/bin/env bash
# check_b457_derp_health_per_relay.sh — B457 (2026-10-31)
#
# «два релея почему то одинаковых висят но один рабочий второй с ошибкой по таблице ниже
# не понять какой» — the operator's own report, and the table could not answer it because
# of its PRIMARY KEY.
#
# derp_health was created in v0.62 with `region_id INTEGER PRIMARY KEY`: ONE verdict per
# region, and a region is not a relay. The reference host ran two enabled rows for region
# 900 (`derp.skynas.ru:443` and a stale `:8443`), so the two relays fought over one storage
# slot and the LAST probe won. B317 mitigated it by probing every row and persisting only
# the BEST one per region — a correct recommendation and a dashboard that cannot represent
# the second relay at all. The mitigation is what this block removes.
#
# WHAT MUST STAY TRUE:
#   * the key is (region_id, url) — the URL, not the hostname, because the live pair
#     differed by PORT on one machine, and keying on the host would have reproduced the
#     bug exactly for the case that caused it;
#   * BOTH migration chains move the key and BOTH are guarded, so a re-run is a no-op
#     (a rebuild that runs twice on a migrated table would drop live verdicts);
#   * the prober stores EVERY relay's verdict, and a relay that cannot answer keeps ITS
#     error, which is what lets the page name the broken one;
#   * a relay missing from the map is pruned by PAIR — a region-level delete takes the
#     working neighbour too;
#   * the recommendation is still one per region (that decision did not change), and the
#     page highlights the recommended (region, url) and shows the endpoint whenever it
#     says something the hostname does not;
#   * the banner text no longer claims one row per region.
#
# CONTRACTS: 14.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

MIG=internal/db/migrations_v0_83_derp_health_per_relay.go
MIGT=internal/db/migrations_v0_83_test.go
PGREG=internal/db/driver_postgres.go
SQREG=internal/db/driver_sqlite.go
TYPES=internal/derphealth/types.go
PROBE=internal/derphealth/probe.go
PRUNE=internal/derphealth/prune.go
EP=internal/derphealth/endpoints_b457.go
TEST=internal/derphealth/per_relay_b457_test.go
DASH=internal/feature/admin/derp_dashboard.go
TPL=internal/handlers/templates/admin/derp_dashboard.html
I18N=internal/i18n/catalog_admin.go
CLI=cmd/skygate/derp_probe.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$MIG" "$MIGT" "$PGREG" "$SQREG" "$TYPES" "$PROBE" "$PRUNE" "$EP" "$TEST" "$DASH" "$TPL" "$I18N" "$CLI" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b457.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. a relay is (region, url) — in BOTH chains, guarded"
GOFMT_OUT="$(gofmt -l "$MIG" "$MIGT" "$TYPES" "$PROBE" "$PRUNE" "$EP" "$TEST" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$PGREG" 'migrateV083PG' && has "$SQREG" 'migrateV083SQLite'; then
  ok "A2: the migration is registered in the PostgreSQL AND the SQLite chain (rule 9)"
else
  bad "A2: a migration missing from one chain is a database that half-migrates"
fi
if has "$MIG" 'ADD PRIMARY KEY (region_id, url)' && has "$MIG" 'PRIMARY KEY (region_id, url)'; then
  ok "A3: both chains key the table by (region_id, url)"
else
  bad "A3: the relay key is not (region_id, url) in both chains"
fi
# The rebuild is a THREE-PHASE COPY (copy aside → swap → copy back) because SQLite
# cannot alter a primary key and B11 does not allow a RENAME in a migration. What makes
# it safe is that it is resumable: a run that dies between two statements leaves its
# staging table behind, and the next boot recognises which phase it is in and FINISHES
# instead of either starting over or answering "already migrated" while the rows sit in
# the staging table. Then it verifies its own effect.
if has "$MIG" 'const derpHealthSideTable = "derp_health_v83"' \
   && has "$MIG" 'sqliteTableExists(d, derpHealthSideTable)' \
   && has "$MIG" 'if live && scoped && !side {' \
   && has "$MIG" 'if !stillScoped {'; then
  ok "A4: the SQLite rebuild is a resumable three-phase copy that verifies its own effect"
else
  bad "A4: the rebuild cannot tell a half-finished run from a finished one (or never checks its result)"
fi
if has "$MIG" "c.conkey @> ARRAY[" && has "$MIG" "'url'"; then
  ok "A5: the PostgreSQL migration detects the old key before touching it (idempotent)"
else
  bad "A5: the PostgreSQL migration cannot tell whether it already ran"
fi
if has "$TYPES" 'ON CONFLICT (region_id, url) DO UPDATE SET'; then
  ok "A6: the upsert writes one row per relay, not one per region"
else
  bad "A6: the upsert still collides two relays of one region"
fi

hdr "B. every measurement stored, and the page can name the broken relay"
if has "$PROBE" 'for _, r := range results {' && ! has "$PROBE" 'for _, best := range BestPerRegion(results) {'; then
  ok "B1: ProbeAll persists EVERY relay (the pre-B457 collapse is gone from the write path)"
else
  bad "B1: the write path still keeps only one verdict per region"
fi
if has "$PROBE" 'func BestPerRegion(' || has "$PROBE" 'BestPerRegion('; then
  ok "B2: BestPerRegion survives for the one place that needs a single answer (the recommendation)"
else
  bad "B2: the recommendation rule was deleted with the workaround"
fi
if has "$PRUNE" 'lower(url) = lower(' && has "$PRUNE" 'if d == nil || len(derps) == 0 {'; then
  ok "B3: the prune keys on (region_id, url) and an empty list stays a no-op"
else
  bad "B3: a region-level prune deletes the relay it did not see, and its neighbour"
fi
if has "$DASH" '"RecommendedURL":      recommendedURL,' && has "$DASH" 'derphealth.EndpointFromURL(r.URL)'; then
  ok "B4: the page highlights the recommended RELAY and computes its endpoint"
else
  bad "B4: the recommendation is still region-wide, so both relays light up"
fi
if has "$TPL" '(eq .URL $.RecommendedURL)' && has "$TPL" '(ne .Endpoint .Host)'; then
  ok "B5: the table highlights (region, url) and shows host:port when it says more than the host"
else
  bad "B5: two relays of one hostname render as two identical lines"
fi
if has "$I18N" 'derp_dashboard.endpoint_help' && [ "$(grep -c '"derp_dashboard.endpoint_help":' "$I18N")" -ge 2 ]; then
  ok "B6: the endpoint tooltip is in BOTH catalogues (rule 10)"
else
  bad "B6: the endpoint tooltip is missing from a catalogue"
fi
if has "$I18N" 'ровно то, что настроено' && ! has "$I18N" 'хранится по одной строке на регион'; then
  ok "B7: the multi-relay banner no longer claims the table keeps one row per region"
else
  bad "B7: the banner still describes the pre-B457 store, so the page contradicts itself"
fi
if has "$CLI" 'each keeps its own verdict'; then
  ok "B8: derp-probe's note matches what the dashboard now shows"
else
  bad "B8: the CLI still says the dashboard keeps only the best row"
fi

hdr "C. pinned by running the tests, and registered"
if ! command -v go >/dev/null 2>&1; then
  skip "C1: no go toolchain on PATH — CI is authoritative"
  skip "C2: no go toolchain on PATH — CI is authoritative"
else
  go test ./internal/derphealth -run 'B457' -count=1 >"$WORK/health.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C1: the relay-scoped store is green (two rows coexist, a neighbour is never overwritten, endpoints)"
  else
    bad "C1: derphealth B457 tests failed — $(tail -3 "$WORK/health.log" | tr '\n' ' ')"
  fi
  go test ./internal/db -run 'B457' -count=1 >"$WORK/db.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C2: the migration is green (old schema → composite key, verdict kept, second run a no-op)"
  else
    bad "C2: migration tests failed — $(tail -3 "$WORK/db.log" | tr '\n' ' ')"
  fi
fi
if git ls-files --error-unmatch scripts/check_b457_derp_health_per_relay.sh >/dev/null 2>&1; then
  ok "C3: this script is tracked by git"
else
  bad "C3: this script is not tracked by git"
fi
if grep -qF 'check_b457_derp_health_per_relay' "$GATE" 2>/dev/null; then
  ok "C4: registered in scripts/verify_pre_deploy.sh"
else
  bad "C4: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B457"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "C5: the B457 catalog description contains a backtick (TD-15)" ;;
  "")    bad "C5: the B457 catalog entry was not found" ;;
  *)     ok "C5: the B457 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B457**' "$AGENTS" 2>/dev/null; then
  ok "C6: AGENTS.md's block index carries B457"
else
  bad "C6: AGENTS.md's block index has no B457 line"
fi

printf '\n\033[1mB457 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
