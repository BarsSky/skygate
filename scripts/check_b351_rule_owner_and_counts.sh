#!/usr/bin/env bash
# check_b351_rule_owner_and_counts.sh
#
# 2026-10-05 (B351) — the admin page measured the PAGE, and the auto-updater wrote
# rows without their owner.
#
# Operator report (screenshot of /admin/exit-rules):
#
#   «Проверь доступность правил для пользователя michail от basic почему то правил
#    7 страниц у админа, и при этом на каждой по разному, посмотри что не так с
#    отображением и нет ли обновлятора сервиса что переписывает в неправильном ключе»
#
# MEASURED on the live PostgreSQL deployment (349 enabled rows, 2 users with rules):
#
#   1. THE COUNTERS WERE PAGE-SCOPED. The page is `ORDER BY r.id LIMIT 50 OFFSET n`
#      and the CDN auto-updater inserts new rows for every device on every tick, so
#      one (user, device) group is scattered over many pages — while the page built
#      its groups AND their counters from that slice. The same user read
#      «19 правил / 3% (19/500)» on page 6 and «49 правил / 9% (49/500)» on page 7,
#      and the heading said «ВСЕ ПРАВИЛА (50)» next to a pagination line saying
#      «всего 349 правил».
#
#   2. THE DENORMALISED OWNER WAS EMPTY FOR MOST ROWS. `device_rules.user_name` was
#      filled by exactly one thing — the one-time V0.44 migration — while the
#      runtime backfill for `device_hostname` has no twin and the auto-updater's two
#      raw INSERTs listed neither column. Live: 220 of 349 rows empty, including ALL
#      68 rows of `basic`; and `DeviceRuleCountsForAdmin` grouped by that column, so
#      the device index listed one device twice (once under its owner, once under "").
#
#      The device's ACCESS was fine — all 68 rules named `karolina`, the
#      prefix_owner, which advertised and had all 188 of its routes approved — the
#      bookkeeping was what lied. That is the "wrong key" the operator suspected.
#
# CONTRACTS
#   A. the writer: every INSERT names the owner, and the runtime healer exists
#   B. the consumer: the admin inventory resolves the owner, never the blank column
#   C. the display: the counters come from the database, and the page says what it shows
#   D. bookkeeping + a live-state contract that SKIPs when the DB is unreachable

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B351: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SYNC=internal/feature/exit_rules/sync.go
OWNER=internal/feature/exit_rules/rule_owner_b351.go
IDX=internal/feature/exit_rules/device_index_b348.go
FORM=internal/feature/exit_rules/form_admin.go
TPL=internal/handlers/templates/admin/exit_rules.html
COUNTS=internal/db/rule_counts_b351.go
QUERIES=internal/db/queries.go

hdr "B351 — the owner must be written, and the counters must come from the database"

# --- A: the writer ------------------------------------------------------------
# A1 is a CLASS guard, not a spot check: the bug was that two INSERTs quietly
# listed fewer columns than the canonical statement, so the rule has to be about
# every INSERT in the tree rather than about the two sites known today. Test files
# are excluded (a fixture may legitimately omit a denormalised column), and a line
# that only MENTIONS the statement in a comment is excluded by requiring the
# column list to be on the same line as the INSERT keyword.
A1_MISS="$(grep -rn 'INSERT INTO device_rules (' --include='*.go' internal cmd 2>/dev/null \
  | grep -v '_test\.go:' \
  | grep -vE 'user_name.*device_hostname|device_hostname.*user_name' || true)"
# The multi-line raw INSERTs in sync.go put the column list on the INSERT line, so
# the grep above sees it; a statement written as `INSERT INTO device_rules (` on
# its own line would be invisible to it — assert there is none of those either.
A1_MULTILINE="$(grep -rn 'INSERT INTO device_rules ($' --include='*.go' internal cmd 2>/dev/null | grep -v '_test\.go:' || true)"
if [ -z "$A1_MISS" ] && [ -z "$A1_MULTILINE" ]; then
  ok "A1: every non-test INSERT INTO device_rules lists user_name AND device_hostname"
else
  bad "A1: an INSERT INTO device_rules omits the owner columns (this is the B351 bug — the row lands with an empty user_name and the admin index splits the device in two):"
  printf '%s\n%s\n' "$A1_MISS" "$A1_MULTILINE" | grep -v '^$' | head -10 | sed 's/^/       /' >&2
fi

A2=$(grep -c 'user_name, device_hostname' "$SYNC" 2>/dev/null || echo 0)
if [ "${A2:-0}" -ge 2 ]; then
  ok "A2: both raw auto-updater INSERTs (CDN expansion + /32 fallback) carry the owner pair"
else
  bad "A2: only ${A2:-0} of the two auto-updater INSERTs carries user_name/device_hostname"
fi

A3_MISS=""
grep -q 'func BackfillDeviceRuleUserNames' internal/db/device_rules.go internal/db/rule_counts_b351.go 2>/dev/null \
  || A3_MISS="${A3_MISS}db.BackfillDeviceRuleUserNames does not exist"$'\n'
grep -q 'db.BackfillDeviceRuleUserNames(s.dbc())' "$SYNC" \
  || A3_MISS="${A3_MISS}the maintenance tick never calls the healer — the 220 existing rows stay empty forever"$'\n'
grep -q 'qBackfillDeviceRuleUserNames' "$QUERIES" \
  || A3_MISS="${A3_MISS}the healer statement is not in queries.go"$'\n'
if [ -z "$A3_MISS" ]; then
  ok "A3: the runtime healer exists and runs in the auto-updater tick (the V0.44 migration cannot run again)"
else
  bad "A3: the hidden half of the fix is missing:"
  printf '%s' "$A3_MISS" | sed 's/^/       /' >&2
fi

A4_MISS=""
grep -q 'newRuleOwnerLookup' "$SYNC" \
  || A4_MISS="${A4_MISS}the INSERTs resolve the owner through no lookup"$'\n'
grep -q 'func newRuleOwnerLookup' "$OWNER" \
  || A4_MISS="${A4_MISS}rule_owner_b351.go has no lookup constructor"$'\n'
grep -q 'portal_users' "$OWNER" \
  || A4_MISS="${A4_MISS}the lookup does not read portal_users"$'\n'
grep -q 'node_owner_map' "$OWNER" \
  || A4_MISS="${A4_MISS}the lookup does not read node_owner_map"$'\n'
if [ -z "$A4_MISS" ]; then
  ok "A4: the owner pair is resolved from portal_users + node_owner_map (never from the column being repaired)"
else
  bad "A4: the lookup is incomplete:"
  printf '%s' "$A4_MISS" | sed 's/^/       /' >&2
fi

# --- B: the consumer ----------------------------------------------------------
B_MISS=""
grep -q 'LEFT JOIN portal_users p ON p.id = r.user_id' "$IDX" \
  || B_MISS="${B_MISS}DeviceRuleCountsForAdmin does not join portal_users"$'\n'
grep -q 'GROUP BY COALESCE(p.username' "$IDX" \
  || B_MISS="${B_MISS}DeviceRuleCountsForAdmin does not group by the RESOLVED username"$'\n'
if grep -q 'GROUP BY COALESCE(r.user_name' "$IDX"; then
  B_MISS="${B_MISS}DeviceRuleCountsForAdmin still groups by the denormalised column (the B351 split-device bug)"$'\n'
fi
# The doc comment claimed the blank meant "predates the backfill" — it did not.
if grep -q 'it is, for rows written since v0.28' "$IDX"; then
  B_MISS="${B_MISS}the false claim about the blank column is still in the comment"$'\n'
fi
if [ -z "$B_MISS" ]; then
  ok "B1: the admin device inventory resolves the owner by user_id and no longer trusts the denormalised column"
else
  bad "B1: the inventory still depends on the column the auto-updater left empty:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

if grep -q 'TestDeviceRuleCountsForAdmin_MergesBlankOwner_B351' internal/feature/exit_rules/rule_owner_b351_test.go 2>/dev/null; then
  ok "B2: a behavioural test pins that a blank owner must not split one device into two inventory lines"
else
  bad "B2: no test pins the blank-owner merge (a text assertion cannot prove the GROUP BY is gone)"
fi

# --- C: the display -----------------------------------------------------------
C_MISS=""
grep -q 'db.AdminRuleCounters(s.dbc())' "$FORM" \
  || C_MISS="${C_MISS}the handler does not load the real counters"$'\n'
grep -q 'realCounts.UserFacingByUser\[uid\]' "$FORM" \
  || C_MISS="${C_MISS}the per-user badge does not use the quota numerator the insert guard enforces"$'\n'
grep -q 'realCounts.EnabledByUser\[uid\]' "$FORM" \
  || C_MISS="${C_MISS}the per-user rule total is not the database's"$'\n'
grep -q 'realCounts.EnabledByUserDevice\[db.UserDeviceKey' "$FORM" \
  || C_MISS="${C_MISS}the per-device total is not the database's"$'\n'
grep -q 'realCounts.EnabledByUserDeviceExit\[db.UserDeviceExitKey' "$FORM" \
  || C_MISS="${C_MISS}the per-relay total is not the database's"$'\n'
grep -q '"TotalRules":    realTotal' "$FORM" \
  || C_MISS="${C_MISS}the heading total is not the database total"$'\n'
grep -q 'totalPct = realTotal \* 100 / maxTotal' "$FORM" \
  || C_MISS="${C_MISS}the system-load badge is still page-scoped"$'\n'
grep -q '"ShownRules": len(rr)' "$FORM" \
  || C_MISS="${C_MISS}the page does not expose what it is showing"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: every /admin/exit-rules counter (heading, system load, per-user, per-device, per-relay) is the unpaginated database number"
else
  bad "C1: a counter is still derived from the 50-row page slice:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

C2_MISS=""
grep -q 'exit_rules_admin.shown_of_total' "$TPL" \
  || C2_MISS="${C2_MISS}the page never says «показано N из M»"$'\n'
grep -q 'exit_rules_admin.quota_badge_title' "$TPL" \
  || C2_MISS="${C2_MISS}the quota badge has no explanation of what it counts"$'\n'
for cat in internal/i18n/catalog_exit_rules.go; do
  for key in shown_of_total quota_badge_title; do
    n=$(grep -c "\"exit_rules_admin\.$key\"" "$cat" 2>/dev/null || echo 0)
    [ "${n:-0}" -ge 2 ] || C2_MISS="${C2_MISS}exit_rules_admin.$key is not defined in both languages ($n)"$'\n'
  done
done
if [ -z "$C2_MISS" ]; then
  ok "C2: the page distinguishes what it SHOWS from what the table HOLDS, in RU and EN"
else
  bad "C2: the page can still pass a window off as a total:"
  printf '%s' "$C2_MISS" | sed 's/^/       /' >&2
fi

# One user's rows must be contiguous, so a group cannot be interleaved across the
# whole table by the auto-updater's insert order (it was `ORDER BY r.id`).
if grep -q 'ORDER BY r.user_id, r.device_id, r.id LIMIT \$1 OFFSET \$2' "$QUERIES"; then
  ok "C3: the paged admin query orders by the group key, so a device's rows stay contiguous"
else
  bad "C3: the paged admin query still orders by r.id alone — one device's rows are scattered across pages and every page tells a different story"
fi

if grep -q 'TestAdminRuleCounters_B351' internal/db/rule_counts_b351_test.go 2>/dev/null; then
  ok "C4: a behavioural test pins the four counters against a real database (incl. the quota unit)"
else
  bad "C4: no test pins the counters — a wrong map key would ship silently"
fi

# --- D: bookkeeping + the live half -------------------------------------------
D_MISS=""
git ls-files --error-unmatch scripts/check_b351_rule_owner_and_counts.sh >/dev/null 2>&1 \
  || D_MISS="${D_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
grep -q 'check_b351_rule_owner_and_counts.sh' scripts/verify_pre_deploy.sh \
  || D_MISS="${D_MISS}verify_pre_deploy.sh does not register B351 — the contract would never run"$'\n'
grep -q 'B351' AGENTS.md \
  || D_MISS="${D_MISS}AGENTS.md's block index has no B351 entry (AGENTS rule 2)"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: tracked, registered in the catalog, indexed in AGENTS.md"
else
  bad "D1: the block bookkeeping is incomplete:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# The live half: after the healer has run, no row may still carry an empty owner.
#
# 2026-10-05 — SCOPED AS INFORMATIONAL, on purpose. The first run of this contract
# made the pre-deploy gate RED with a correct measurement (the live table still had
# 220 empty rows because the RUNNING build predates the healer), which is the B327
# class exactly: a pre-deploy catalog must not assert a POST-deploy fact, and a check
# that reports a state it cannot fix must SKIP. The assertion now lives where it
# belongs — scripts/verify_post_deploy.sh (R-B351) — and this row only reports the
# number, so an operator reading a pre-deploy run still sees it.
if command -v docker >/dev/null 2>&1 && sudo -n docker ps >/dev/null 2>&1; then
  EMPTY=$(timeout 20 sudo -n docker exec skygate-pg-local psql -U admin -d skygate_staging -tAc \
    "SELECT COUNT(*) FROM device_rules WHERE COALESCE(user_name,'') = ''" 2>/dev/null | tr -d '[:space:]')
  if [ -z "$EMPTY" ]; then
    skip "D2: live device_rules not reachable from here (run this script on the reference VM)"
  else
    skip "D2: live device_rules currently has $EMPTY row(s) with an empty user_name — informational; the assertion is scripts/verify_post_deploy.sh R-B351 (the healer runs in the auto-updater tick, i.e. after the deploy)"
  fi
else
  skip "D2: docker unavailable or passwordless sudo not configured — cannot inspect the live rule table"
fi

printf '\n\033[1mB351 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
