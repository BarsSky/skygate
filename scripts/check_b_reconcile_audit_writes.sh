#!/usr/bin/env bash

# Live-state check: skip (do not fail) when the docker daemon is unreachable.
. "$(dirname "$0")/lib/skip_if_no_docker.sh"
# check_b_reconcile_audit_writes.sh — verify the reconcile cron writes
# audit_log rows that survive a subsequent SELECT (regression test for
# the B243 SQLSTATE 42601 bug).
#
# 2026-09-12 (B243): pre-B243, writeAudit + writeAuditRaw used
# literal "(?, ?, ?, ?, ?)" placeholders. PostgreSQL (pgx stdlib)
# REQUIRES "$1,$2,..." — the literal ? is rejected with SQLSTATE 42601
# "syntax error at or near ','". The portal-side UPDATE succeeded,
# but the audit_log INSERT silently failed. skygate logs were full of
# "reconcile: write audit row (...): ERROR: ... (SQLSTATE 42601)" on
# every cycle. The operator had NO record of any reconcile event.
#
# B243 fix: writeAudit + writeAuditRaw now use db.PlaceholdersList(5)
# (which renders as "$1,$2,$3,$4,$5" on PG and "?,,," on SQLite via
# the existing dialect helper).
#
# This B-check catches the regression by:
#   1. recording the current headscale_user_reconcile row count
#   2. triggering a reconcile cycle (via the cron at startup + waiting
#      OR by restarting skygate; the cron's startup run is enough)
#   3. querying audit_log again
#   4. FAIL if no new rows appeared OR if the count went BACKWARD
#      (e.g. someone deleted rows from audit_log — that's a separate
#      signal that needs operator review)
#
# Does NOT cover:
#   - The headscale-side duplicate detection (see
#     check_b_duplicate_users.sh).
#   - The per-row outcome correctness (the cron writes relinked/linked/
#     orphan/duplicate_name rows; see check_b_admin_user_sync.sh).
#
# Usage:
#   bash scripts/check_b_reconcile_audit_writes.sh
#
# Exit codes:
#   0 = at least 1 new reconcile audit row since the recorded baseline
#   1 = no new rows (cycle didn't fire, OR SQL still broken, OR audit
#       write silently failed)
#   2 = DB unreachable / table missing

set -uo pipefail

CONTAINER="${SKYGATE_CONTAINER:-skygate-skygate-1}"
PG_CONTAINER="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"

# B281 (2026-09-22): no container = absent live dependency = SKIP, never FAIL
# (AGENTS.md §1.1). This check RESTARTS skygate to fire the reconcile cron, so
# it can only ever run on the operator's own host — on CI it must SKIP.
if ! sudo -n docker inspect "$CONTAINER" >/dev/null 2>&1; then
    echo "  SKIP  $CONTAINER container not running (live check — restart-based, run it on the skygate host)"
    exit 0
fi
if ! sudo -n docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
    echo "  SKIP  $PG_CONTAINER container not running (live check — run it on the skygate host)"
    exit 0
fi

# ── detect backend ──
LIVE_DB=$(sudo -n docker exec "$CONTAINER" sh -c 'env | grep "^SKYGATE_DB=" | head -1 | cut -d= -f2-' 2>/dev/null)
case "$LIVE_DB" in
    postgres://*|postgresql://*) BACKEND="pg" ;;
    sqlite:*|file:*|"")         BACKEND="sqlite" ;;
    *)                          BACKEND="unknown" ;;
esac
[ "$BACKEND" != "unknown" ] || { echo "  FAIL  backend unknown (SKYGATE_DB=$LIVE_DB)"; exit 2; }
echo "backend=$BACKEND"

# ── baseline count + most recent row timestamp ──
BEFORE_COUNT=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
  "SELECT COUNT(*) FROM audit_log WHERE action = 'headscale_user_reconcile'" 2>/dev/null)
BEFORE_TS=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
  "SELECT COALESCE(EXTRACT(epoch FROM MAX(to_timestamp(created_at))), 0)::int FROM audit_log WHERE action = 'headscale_user_reconcile'" 2>/dev/null)
echo "baseline: count=$BEFORE_COUNT most_recent_ts=$BEFORE_TS"

# ── trigger reconcile via docker restart (cron runs at startup) ──
echo
echo "Triggering reconcile cycle via docker restart (cron runs at startup)..."
PRE_RESTART_TS=$(date +%s)
sudo -n docker restart "$CONTAINER" >/dev/null 2>&1

# ── wait for healthy (max 60s — the entrypoint's pre-flight wait can
#    take 30-60s on a cold start) ──
echo -n "  waiting for healthy"
HEALTHY=""
for i in $(seq 1 90); do
    sleep 2
    STATE=$(sudo -n docker inspect "$CONTAINER" --format '{{.State.Status}}' 2>/dev/null)
    if [ "$STATE" != "running" ]; then
        echo -n "."
        continue
    fi
    HEALTH=$(sudo -n docker inspect "$CONTAINER" --format '{{.State.Health.Status}}' 2>/dev/null)
    if [ "$HEALTH" = "healthy" ]; then
        echo " healthy after ${i}x2s"
        HEALTHY=1
        break
    fi
    echo -n "."
done
if [ -z "$HEALTHY" ]; then
    echo "  FAIL  $CONTAINER not healthy after 180s (timeout)"
    exit 1
fi

# ── the cron runs immediately at startup; give it 10s to write ──
sleep 10

# ── count again ──
AFTER_COUNT=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
  "SELECT COUNT(*) FROM audit_log WHERE action = 'headscale_user_reconcile'" 2>/dev/null)
AFTER_TS=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
  "SELECT COALESCE(EXTRACT(epoch FROM MAX(to_timestamp(created_at))), 0)::int FROM audit_log WHERE action = 'headscale_user_reconcile'" 2>/dev/null)
echo "after:    count=$AFTER_COUNT most_recent_ts=$AFTER_TS"

# ── pass/fail ──
NEW=$((AFTER_COUNT - BEFORE_COUNT))
if [ "$NEW" -gt 0 ] && [ "$AFTER_TS" -gt "$BEFORE_TS" ]; then
    echo "  PASS  A: reconcile cron wrote $NEW new audit_log rows (ts advanced $((AFTER_TS - BEFORE_TS))s)"

    # Also check the new rows have the expected outcomes (the per-cycle
    # summary JSON should contain either "rows" or "duplicate_names").
    HAS_VALID_DETAIL=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
      "SELECT COUNT(*) FROM audit_log WHERE action = 'headscale_user_reconcile' AND created_at > $BEFORE_TS AND (detail LIKE '%\"rows\"%' OR detail LIKE '%\"outcome\"%' OR detail LIKE '%\"duplicate_names%')" 2>/dev/null)
    if [ "$HAS_VALID_DETAIL" -gt 0 ]; then
        echo "  PASS  B: $HAS_VALID_DETAIL new rows have valid detail JSON (rows/outcome/duplicate_names)"
    else
        echo "  WARN  B: new rows missing expected detail JSON shape (rows / outcome / duplicate_names)"
    fi

    # Check the logs for SQLSTATE 42601 — should be 0 NEW occurrences.
    SQLSTATE_COUNT=$(sudo -n docker logs "$CONTAINER" --since "${PRE_RESTART_TS}s" 2>&1 | grep -c "SQLSTATE 42601" || true)
    if [ "$SQLSTATE_COUNT" = "0" ]; then
        echo "  PASS  C: 0 SQLSTATE 42601 errors in this cycle (B243 SQL fix verified)"
    else
        echo "  FAIL  C: $SQLSTATE_COUNT SQLSTATE 42601 errors in skygate logs since restart (B243 regression!)"
        sudo -n docker logs "$CONTAINER" --since "${PRE_RESTART_TS}s" 2>&1 | grep "SQLSTATE 42601" | head -3
        exit 1
    fi
    exit 0
fi

# No new rows — could be: cycle didn't fire (unhealthy), or SQL write
# still broken (B243 regression), or operator deleted audit_log rows
# (suspicious).
if [ "$NEW" = "0" ] && [ "$AFTER_TS" = "$BEFORE_TS" ]; then
    echo "  FAIL  A: no new audit_log rows after restart (cycle didn't write)"
    echo "          This is the exact B243 bug pattern: cron ran but"
    echo "          audit_log INSERT silently failed (SQLSTATE 42601?)."
    echo "          skygate logs around this restart:"
    sudo -n docker logs "$CONTAINER" --since "${PRE_RESTART_TS}s" 2>&1 | grep -iE "reconcile|sqlstate|42601" | head -10
    exit 1
fi

if [ "$AFTER_COUNT" -lt "$BEFORE_COUNT" ]; then
    echo "  FAIL  A: audit_log rows went BACKWARD ($BEFORE_COUNT → $AFTER_COUNT)"
    echo "          someone deleted audit_log rows since the baseline"
    exit 1
fi

echo "  FAIL  A: unexpected state (before=$BEFORE_COUNT after=$AFTER_COUNT ts_delta=$((AFTER_TS-BEFORE_TS)))"
exit 1
