#!/bin/bash
# B183 — drop parent_domain from device_rules natural-key
# UNIQUE INDEX (autoupdater duplicate-row fix)
#
# The pre-B183 index was 6-column, which let the autoupdater
# accumulate duplicate rows when different parent_domains
# resolved to the same CIDR. Live data for emilia: 102 subnet
# rows but only 32 unique subnets — 70+ rows were duplicates
# of the same /22 with different parent_domain values
# (e.g. cdn:cloudflare:discordapp.com and
# cdn:cloudflare:discord.com both → 103.21.244.0/22). The
# visible symptom was /admin/exit-nodes "mismatch: have 34,
# want 102" (the want side counted raw rows; the have side
# counted unique routes on the Tailscale client).
#
# B183 fix: drop parent_domain from the natural key. The
# natural key is (user, device, exit, type, value) —
# parent_domain is metadata, not part of what makes a rule
# unique. The autoupdater's two ON CONFLICT clauses (sync.go)
# are also updated in B183 to use the 5-column target.
#
# Contracts (10 sub-checks):
#  A. migrateV060PG is registered in driver_postgres.go
#  B. migrateV060PG is defined in migrations_pg.go
#  C. The new unique index is on 5 columns (no parent_domain)
#  D. The dedup CTE uses ROW_NUMBER() with the cdn: prefix preference
#  E. sync.go ON CONFLICT clauses are 5-column (no parent_domain)
#  F. The dedup query is a single statement (no app-level loop)
#  G. AGENTS.md mentions B183
#  H. verify_pre_deploy.sh includes check_b183
#  I. go test ./internal/db/... passes
#  J. /admin/exit-nodes "want" count drops to the unique-routes
#     count after the migration runs (live check on VM).

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

check_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "  PASS [$label] $actual"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] expected=$expected got=$actual"
    FAIL=$((FAIL+1))
  fi
}

check_ge() {
  local label="$1" min="$2" actual="$3"
  if [ "$actual" -ge "$min" ] 2>/dev/null; then
    echo "  PASS [$label] actual=$actual (>= $min)"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] actual=$actual (expected >= $min)"
    FAIL=$((FAIL+1))
  fi
}

count() {
  local n
  n=$(grep -cE "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

echo "=== B183 contracts ==="

# A. migrateV060PG is registered in driver_postgres.go
check_ge "A" 1 "$(count "$REPO/internal/db/driver_postgres.go" 'migrateV060PG')"

# B. migrateV060PG is defined in migrations_pg.go
check_ge "B" 1 "$(count "$REPO/internal/db/migrations_pg.go" 'func migrateV060PG')"

# C. The new unique index is on 5 columns (no parent_domain)
# Look for the CREATE UNIQUE INDEX statement and verify it has
# exactly 5 column names (user_id, device_id, exit_node_id,
# target_type, target_value) without parent_domain.
C_LINE=$(grep -n 'CREATE UNIQUE INDEX IF NOT EXISTS device_rules_natural_key_uniq' "$REPO/internal/db/migrations_pg.go" | tail -1 | cut -d: -f1)
if [ -n "$C_LINE" ]; then
  NEXT_LINE=$((C_LINE + 1))
  INDEX_DEF=$(sed -n "${NEXT_LINE}p" "$REPO/internal/db/migrations_pg.go")
  if echo "$INDEX_DEF" | grep -q 'user_id.*device_id.*exit_node_id.*target_type.*target_value' \
      && ! echo "$INDEX_DEF" | grep -q 'parent_domain'; then
    check_eq "C" "5-cols-no-parent_domain" "5-cols-no-parent_domain"
  else
    check_eq "C" "5-cols-no-parent_domain" "wrong-definition: $INDEX_DEF"
  fi
else
  check_eq "C" "5-cols-no-parent_domain" "CREATE UNIQUE INDEX not found"
fi

# D. The dedup CTE uses ROW_NUMBER() with the cdn: prefix preference
# (catches "did the developer think about which row to keep?")
check_ge "D-row-number" 1 "$(count "$REPO/internal/db/migrations_pg.go" 'ROW_NUMBER\(\) OVER')"
check_ge "D-cdn-prefix" 1 "$(count "$REPO/internal/db/migrations_pg.go" "WHEN parent_domain LIKE 'cdn:%'")"
check_ge "D-order-desc" 1 "$(count "$REPO/internal/db/migrations_pg.go" 'id DESC')"

# E. sync.go ON CONFLICT clauses are 5-column (no parent_domain)
# The pre-B183 string was 6-column including parent_domain.
# After B183 it should be 5-column.
PRE_B183_6COL=$(count "$REPO/internal/feature/exit_rules/sync.go" 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value, parent_domain\)')
if [ "$PRE_B183_6COL" = "0" ]; then
  check_eq "E-pre" "0" "0"
else
  check_eq "E-pre" "0" "$PRE_B183_6COL (pre-B183 6-col ON CONFLICT still present)"
fi
POST_B183_5COL=$(count "$REPO/internal/feature/exit_rules/sync.go" 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value\) DO NOTHING')
check_ge "E-post" 1 "$POST_B183_5COL"

# F. The dedup is a single SQL statement (no app-level loop)
# Counts the number of DELETE statements in the migration —
# should be 1 (the CTE-based one). Match is fuzzy because
# the line starts with a backtick (raw string literal).
F_DEL=$(grep -cE 'DELETE FROM device_rules' "$REPO/internal/db/migrations_pg.go")
check_eq "F" "1" "$F_DEL"

# G. AGENTS.md mentions B183
if [ -f "$REPO/AGENTS.md" ]; then
  check_ge "G" 1 "$(count "$REPO/AGENTS.md" 'B183')"
else
  check_eq "G" ">=1" "0"
fi

# H. verify_pre_deploy.sh includes check_b183
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  check_ge "H" 1 "$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b183')"
else
  check_eq "H" ">=1" "0"
fi

# I. go test ./internal/db/... passes (skipped if no PG available)
GO_BIN=""
for cand in /usr/local/go/bin/go /usr/bin/go /opt/go/bin/go "$(command -v go 2>/dev/null)"; do
  if [ -x "$cand" ]; then
    GO_BIN="$cand"
    break
  fi
done
if [ -n "$GO_BIN" ]; then
  if (cd "$REPO" && "$GO_BIN" test -count=1 ./internal/db/... 2>&1) | grep -q '^ok\s'; then
    check_eq "I" "ok" "ok"
  else
    check_eq "I" "ok" "FAIL"
  fi
else
  echo "  SKIP [I] go not available in PATH"
fi

# J. (VM-only) Live check: after migration runs, the want
# count on /admin/exit-nodes should equal the unique CIDR
# count (no drift). We can't easily curl the page here, so
# instead we check the DB directly: count distinct
# (user, device, exit, type, value) tuples for exit_node=emilia
# and verify that count = number of rows.
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tA -c "
      SELECT
        (SELECT COUNT(*) FROM device_rules WHERE enabled=1 AND exit_node_id='emilia' AND target_type='subnet') AS total_rows,
        (SELECT COUNT(DISTINCT (user_id, device_id, exit_node_id, target_type, target_value)) FROM device_rules WHERE enabled=1 AND exit_node_id='emilia' AND target_type='subnet') AS distinct_rows;
    " 2>/dev/null > /tmp/b183_emilia_counts.txt
    if [ -s /tmp/b183_emilia_counts.txt ]; then
      TOTAL=$(cat /tmp/b183_emilia_counts.txt | awk -F'|' '{print $1}' | tr -d ' ')
      DISTINCT=$(cat /tmp/b183_emilia_counts.txt | awk -F'|' '{print $2}' | tr -d ' ')
      TOTAL=${TOTAL:-0}
      DISTINCT=${DISTINCT:-0}
      if [ "$TOTAL" -le 50 ] && [ "$DISTINCT" -gt 0 ] && [ "$TOTAL" -eq "$DISTINCT" ]; then
        check_eq "J" "no-dup" "no-dup"
      else
        check_eq "J" "no-dup" "still-has-dup (emilia: $TOTAL rows vs $DISTINCT distinct)"
      fi
    else
      echo "  SKIP [J] could not query emilia device_rules"
    fi
  else
    echo "  SKIP [J] psql not available"
  fi
else
  echo "  SKIP [J] not on VM"
fi

echo
echo "=== B183 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
