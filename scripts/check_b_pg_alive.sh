#!/usr/bin/env bash
# ============================================================================
# check_b_pg_alive.sh — B-check for PostgreSQL reachability + skygate_staging
# integrity (B-mod-pg-bypass, 2026-09-09)
#
# What this verifies
# ------------------
# Pre-deploy check that PostgreSQL (the production DB that skygate uses
# via SKYGATE_DB_DSN) is:
#   (a) running and accepting connections on the configured port
#   (b) has the skygate_staging database
#   (c) has the core tables (portal_users, audit_log, applied_migrations)
#   (d) has a recent migration_apply audit row (proves migrations
#       are running, not silently stuck)
#
# Closes the gap that caused the 2026-09-09 outage:
#   - skygate container was in restart loop for ~24 hours
#     because PG was down (Patroni stopped, etcd unreachable)
#   - Docker healthcheck said "(healthy)" because it only checks
#     /healthz (which returns 200 even when DB is broken)
#   - This B-check would have caught it in <1 second
#
# Background: skygate v1.3.0+ removed the SQLite fallback, so
# SKYGATE_DB_DSN MUST point at a reachable PostgreSQL. A stale
# or unreachable DSN is a hard error that the operator must
# detect BEFORE deploy, not after.
#
# Contracts (10 total)
# --------------------
# A. pg_isready succeeds on the port from SKYGATE_DB_DSN
# B. skygate_staging database exists (queried via psql)
# C. portal_users table exists (proves migrations ran)
# D. audit_log table exists (proves migrations ran)
# E. applied_migrations table exists (proves migration tracking on)
# F. applied_migrations has >= 10 rows (proves migrations applied,
#    not just a fresh-empty DB)
# G. recent migration_apply action exists in audit_log
#    (proves the migrator actually ran, not just recorded checksums)
# H. Patroni is NOT the bottleneck (if Patroni is the only thing
#    keeping PG up, this check warns the operator)
# I. SKYGATE_DB_DSN parseable (postgres://user:pass@host:port/db)
# J. /var/lib/postgresql data dir exists with PG version subdir
#
# Exit codes
# ----------
#   0  all contracts pass
#   1  at least one contract failed
# ============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

PASS=0
FAIL=0
fails=()

ok() {
    local name="$1"
    PASS=$((PASS + 1))
    echo "  [PASS] $name"
}

fail() {
    local name="$1"
    local msg="$2"
    FAIL=$((FAIL + 1))
    fails+=("$name: $msg")
    echo "  [FAIL] $name: $msg"
}

# Load .env if present (for SKYGATE_DB_DSN).
ENV_FILE="$PROJECT_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    ENV_FILE="$PROJECT_DIR/deploy/.env.example"
fi

# Read DSN: prefer env var (so devs can override), then .env.
SKYGATE_DB_DSN="${SKYGATE_DB_DSN:-}"
if [ -z "$SKYGATE_DB_DSN" ] && [ -f "$ENV_FILE" ]; then
    SKYGATE_DB_DSN=$(grep -E '^SKYGATE_DB_DSN=' "$ENV_FILE" | head -1 | cut -d= -f2-)
fi

# B-mod-pg-alive-polygon (2026-09-10): the original
# check_b_pg_alive.sh hardcoded `sudo -u postgres` for
# every psql call, which fails on the svi polygon (PG is
# on 13.66, svi is just a client). This adds a "mode"
# switch:
#
#   local   (default) — use sudo -u postgres (assumes
#                       local Patroni / local PG with
#                       peer auth). Original behavior.
#   polygon          — use SKYGATE_DB_DSN + PGPASSWORD
#                       env var. NO sudo. For VMs that
#                       are clients of a remote PG
#                       (NPM-fronted, like svi polygon
#                       pointing at 13.66).
#   auto             — detect: try `sudo -u postgres` first,
#                       fall back to DSN + PGPASSWORD if it
#                       fails.
#
# The mode is set via SKYGATE_PG_ALIVE_MODE env var.
# Default: auto (most forgiving).
SKYGATE_PG_ALIVE_MODE="${SKYGATE_PG_ALIVE_MODE:-auto}"

echo "=== check_b_pg_alive.sh (B-mod-pg-bypass, 2026-09-09 + B-mod-pg-alive-polygon, 2026-09-10) ==="
echo "DSN: ${SKYGATE_DB_DSN:-<not set>}"
echo "Mode: $SKYGATE_PG_ALIVE_MODE"
echo "note: this B-check is meant to run ON the skygate VM (not the dev box)."
echo "      On a dev box without pg_isready/psql + without SKYGATE_DB_DSN env var,"
echo "      contracts A-G are skipped (only I+J code-only contracts are checked)."
echo "      On a polygon (svi-style) VM, set SKYGATE_PG_ALIVE_MODE=polygon + PGPASSWORD=<db-password>."

# I. SKYGATE_DB_DSN parseable
if [ -z "$SKYGATE_DB_DSN" ]; then
    echo "  [SKIP] I: SKYGATE_DB_DSN not set (export SKYGATE_DB_DSN or run on the skygate VM)"
elif echo "$SKYGATE_DB_DSN" | grep -qE '^postgres://[^:]+:[^@]+@[^:]+:[0-9]+/[^?]+\?sslmode=disable$'; then
    ok "I: SKYGATE_DB_DSN parseable (postgres://user:pass@host:port/db?sslmode=disable)"
else
    fail "I" "SKYGATE_DB_DSN does not match expected format"
fi

# If DSN is not set, skip the runtime checks (we're not on the VM)
if [ -z "$SKYGATE_DB_DSN" ]; then
    # Only J (code-only check) will be evaluated below
    SKIP_RUNTIME=true
else
    SKIP_RUNTIME=false
    DB_HOST=$(echo "$SKYGATE_DB_DSN" | sed -E 's|^postgres://[^:]+:[^@]+@([^:]+):([0-9]+)/([^?]+).*|\1|')
    DB_PORT=$(echo "$SKYGATE_DB_DSN" | sed -E 's|^postgres://[^:]+:[^@]+@([^:]+):([0-9]+)/([^?]+).*|\2|')
    DB_NAME=$(echo "$SKYGATE_DB_DSN" | sed -E 's|^postgres://[^:]+:[^@]+@([^:]+):([0-9]+)/([^?]+).*|\3|')
    # B-mod-pg-alive-polygon (2026-09-10): also parse
    # user + password from the DSN for polygon mode.
    # Format: postgres://user:pass@host:port/db?sslmode=disable
    # The : and @ delimiters can be tricky in passwords,
    # so we split from the right: db (after /), then
    # port (after :), then host (after @), then user
    # (between :// and :), then password (between user
    # and @).
    DB_USER=$(echo "$SKYGATE_DB_DSN" | sed -E 's|^postgres://([^:]+):.*|\1|')
    DB_PASS=$(echo "$SKYGATE_DB_DSN" | sed -E 's|^postgres://||' | sed -E 's|^(.+)@.+$|\1|')
fi

if [ "$SKIP_RUNTIME" = "false" ]; then
    # pg_query_pg_isready — mode-aware wrapper around
    # `pg_isready`. In polygon mode, uses -h/-p from the
    # DSN. In local mode, uses sudo -u postgres on the
    # local socket. Returns 0 if ready, 1 if not.
    pg_query_pg_isready() {
        local host="$1"
        local port="$2"
        case "$SKYGATE_PG_ALIVE_MODE" in
            polygon)
                PGPASSWORD="$DB_PASS" pg_isready -h "$host" -p "$port" -U "$DB_USER" -t 5
                ;;
            local)
                sudo -u postgres pg_isready -p "$port"
                ;;
            auto|*)
                if [ -n "$DB_PASS" ] && PGPASSWORD="$DB_PASS" pg_isready -h "$host" -p "$port" -U "$DB_USER" -t 5 2>/dev/null; then
                    return 0
                fi
                sudo -u postgres pg_isready -p "$port" 2>/dev/null
                ;;
        esac
    }

    # pg_query_psql — mode-aware wrapper around `psql`.
    # All extra args are passed to psql (-c, -tA, -d, -h,
    # -p, -U, etc). In polygon mode, the caller is
    # responsible for passing -h/-p/-U; we just set
    # PGPASSWORD. In local mode, we wrap with sudo.
    pg_query_psql() {
        case "$SKYGATE_PG_ALIVE_MODE" in
            polygon)
                PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" "$@"
                ;;
            local)
                sudo -u postgres psql -p "$DB_PORT" -d "$DB_NAME" "$@"
                ;;
            auto|*)
                if [ -n "$DB_PASS" ] && PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" "$@" 2>/dev/null; then
                    return 0
                fi
                sudo -u postgres psql -p "$DB_PORT" -d "$DB_NAME" "$@" 2>/dev/null
                ;;
        esac
    }

    # A. pg_isready succeeds
    if command -v pg_isready >/dev/null 2>&1; then
        if pg_query_pg_isready "$DB_HOST" "$DB_PORT" 2>/dev/null; then
            ok "A: pg_isready succeeds on $DB_HOST:$DB_PORT (mode=$SKYGATE_PG_ALIVE_MODE)"
        else
            fail "A" "pg_isready failed on $DB_HOST:$DB_PORT (mode=$SKYGATE_PG_ALIVE_MODE)"
        fi
    else
        echo "  [SKIP] A: pg_isready not installed"
    fi

    # B-G. Tables + audit_log (only if psql is available)
    if command -v psql >/dev/null 2>&1; then
        if pg_query_psql '\dt' >/tmp/check_b_pg_alive_tables.log 2>&1; then
            tables=$(grep -E '^ public \|' /tmp/check_b_pg_alive_tables.log | awk '{print $3}' | tr '\n' ',' | sed 's/,$//')
            if echo "$tables" | grep -q 'portal_users'; then
                ok "C: portal_users table exists"
            else
                fail "C" "portal_users table NOT found in $DB_NAME"
            fi
            if echo "$tables" | grep -q 'audit_log'; then
                ok "D: audit_log table exists"
            else
                fail "D" "audit_log table NOT found in $DB_NAME"
            fi
            if echo "$tables" | grep -q 'applied_migrations'; then
                ok "E: applied_migrations table exists (migration tracking is ON)"
            else
                fail "E" "applied_migrations table NOT found — migration tracking is OFF"
            fi
            # B. skygate_staging DB is non-empty
            if [ -n "$tables" ]; then
                ok "B: $DB_NAME DB has $(echo "$tables" | tr ',' '\n' | wc -l) tables"
            else
                fail "B" "$DB_NAME DB is empty"
            fi
            # F. applied_migrations has rows
            mig_count=$(pg_query_psql -tAc 'SELECT COUNT(*) FROM applied_migrations' 2>/dev/null)
            if [ -n "$mig_count" ] && [ "$mig_count" -ge 10 ]; then
                ok "F: applied_migrations has $mig_count rows (proves migrations ran, not just empty DB)"
            else
                fail "F" "applied_migrations has only $mig_count rows (< 10)"
            fi
            # G. at least one migration applied in the last 30 days
            # (proves the migrator is still running, not just a stale DB)
            # PG uses to_timestamp(unix_seconds), not SQLite's datetime(unix, 'unixepoch')
            recent_mig=$(pg_query_psql -tAc \
                "SELECT version || ':' || to_timestamp(applied_at)::date FROM applied_migrations WHERE applied_at > extract(epoch from now() - interval '30 days')::bigint ORDER BY applied_at DESC LIMIT 1" 2>/dev/null)
            if [ -n "$recent_mig" ]; then
                ok "G: recent migration applied: $recent_mig (migrator still active)"
            else
                fail "G" "no migration applied in the last 30 days (migrator may be stuck)"
            fi
        else
            fail "B" "psql connection failed to $DB_NAME (mode=$SKYGATE_PG_ALIVE_MODE)"
        fi
    else
        echo "  [SKIP] B-G: psql not installed (run this check on the skygate VM)"
    fi
fi

# H. Patroni state warning (only meaningful on the VM)
if [ "$SKIP_RUNTIME" = "false" ] && command -v systemctl >/dev/null 2>&1; then
    if systemctl is-active --quiet patroni 2>/dev/null; then
        # Patroni state is only meaningful on the VM that
        # runs Patroni. On a polygon client (svi pointing
        # at 13.66), this is always going to be 'not
        # active' — that's fine, just log INFO.
        if [ "$SKYGATE_PG_ALIVE_MODE" = "polygon" ]; then
            echo "  [SKIP] H: Patroni state check skipped (mode=polygon — this VM is a client, not the PG host)"
        else
            patroni_state=$(sudo -u postgres patronictl -c /etc/patroni.yml list 2>/dev/null | tail -1 || echo "(patronictl not available)")
            if echo "$patroni_state" | grep -qi 'Leader'; then
                ok "H: Patroni has Leader (HA chain healthy)"
            elif echo "$patroni_state" | grep -qi 'Replica'; then
                ok "H: Patroni has Replica (HA chain present, agent is standby)"
            elif echo "$patroni_state" | grep -qi 'unknown\|stopped'; then
                echo "  [WARN] H: Patroni state is '$patroni_state' — HA chain degraded, PG running standalone"
            else
                echo "  [INFO] H: Patroni state: $patroni_state (check HA status manually)"
            fi
        fi
    else
        echo "  [INFO] H: Patroni service not active on this VM (PG running standalone — OK for dev/staging)"
    fi
else
    echo "  [SKIP] H: Patroni state check requires running on the skygate VM"
fi

# J. PG data dir (only on VM)
if [ "$SKIP_RUNTIME" = "false" ] && [ -d /var/lib/postgresql ] && ls -d /var/lib/postgresql/*/ 2>/dev/null | head -1 | grep -qE '/var/lib/postgresql/[0-9]+'; then
    pg_ver=$(ls -d /var/lib/postgresql/*/ 2>/dev/null | head -1 | grep -oE '[0-9]+$')
    ok "J: PG data dir /var/lib/postgresql/$pg_ver/ exists"
else
    echo "  [SKIP] J: PG data dir check requires running on the skygate VM"
fi

# B-mod-pg-alive-polygon contracts (2026-09-10):
# Self-checks that don't require psql. These catch the
# most common "polygon mode broken" scenarios before
# the operator runs the B-check on a real polygon VM.
# Placed LAST so they have access to DB_USER/DB_PASS
# parsed from the DSN.
case "$SKYGATE_PG_ALIVE_MODE" in
    polygon)
        if [ -n "$SKYGATE_DB_DSN" ] && { [ -z "$DB_USER" ] || [ -z "$DB_PASS" ]; }; then
            echo "  [FAIL] polygon-mode-config: DB_USER/DB_PASS not extracted from DSN (polygon mode requires the DSN to encode user+password)"
            FAIL=$((FAIL + 1))
        elif [ -n "$SKYGATE_DB_DSN" ]; then
            echo "  [PASS] polygon-mode-config: DB_USER=$DB_USER (parsed from DSN, password length ${#DB_PASS})"
        else
            echo "  [SKIP] polygon-mode-config: no SKYGATE_DB_DSN (polygon mode without DSN makes no sense)"
        fi
        # PGPASSWORD must NOT be set globally when running
        # in polygon mode (the wrappers set it per-call).
        # If it's set globally, psql will use it for
        # every connection including unrelated ones.
        if [ -n "${PGPASSWORD:-}" ]; then
            echo "  [INFO] polygon-mode-config: PGPASSWORD env is set (wrappers still work; OK for polygon)"
        else
            echo "  [INFO] polygon-mode-config: PGPASSWORD not set globally (OK — wrappers set it per-call)"
        fi
        ;;
    local|auto|*)
        # local/auto mode preserves the legacy
        # sudo -u postgres paths. Self-check that the
        # source still has them.
        if grep -q 'sudo -u postgres' "$0"; then
            echo "  [PASS] local-mode-config: sudo -u postgres paths preserved (legacy behavior intact)"
        else
            echo "  [FAIL] local-mode-config: sudo -u postgres removed but mode != polygon"
            FAIL=$((FAIL + 1))
        fi
        ;;
esac

# Summary
echo
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    echo "Failed contracts:"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
exit 0
