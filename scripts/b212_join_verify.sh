#!/usr/bin/env bash
# B212 live-verify on the agent.
# Simulates a "new node" join by using the agent's own
# skygate binary + api-url. The B201 handler will see
# the existing hostname=agent row and go through the
# "existing node" path (idempotent — the pre-B212
# behaviour), but the B212 DSN bootstrap + state file
# + DSN env file are all exercised.
set -euo pipefail
cd /home/skyadmin/skygate

# Load .env
set --
set -a
# shellcheck disable=SC1091
. /home/skyadmin/skygate/.env
set +a

# /usr/local/bin/go is 1.23.4 but go.mod wants 1.25+.
# Use /snap/go/current/bin/go (1.26.7) which works.
GO_BIN="${GO_BIN:-/snap/go/current/bin/go}"
# GOCACHE / GOMODCACHE: the default paths
# (/root/.cache/...) are not writable by skyadmin.
# Override to a skyadmin-owned dir.
export GOCACHE="/tmp/go-cache"
export GOMODCACHE="/tmp/go-modcache"
mkdir -p "$GOCACHE" "$GOMODCACHE"

# Build the B212 binary fresh
$GO_BIN build -o /tmp/skygate_b212 ./cmd/skygate

echo "=== Step 1: issue a fresh standby invite ==="
TOKEN="$(/tmp/skygate_b212 init standby-invite --ttl-hours=1 | head -1)"
echo "token: ${TOKEN:0:30}...${TOKEN: -10}"
echo ""

# The join uses the same api-url the running skygate
# uses (localhost:8080 inside the container, but we
# run from outside the container, so use the host's
# direct IP).
API_URL="http://192.168.13.69:8080"

echo "=== Step 2: run skygate join (writes state + DSN env file) ==="
/tmp/skygate_b212 join \
    --api-url="$API_URL" \
    --state-file=/tmp/b212-state.json \
    --write-dsn-to=/tmp/b212-dbs.env \
    --no-heartbeat-hint \
    "$TOKEN"
echo ""

echo "=== Step 3: inspect state file ==="
cat /tmp/b212-state.json
echo ""

echo "=== Step 4: inspect DSN env file ==="
cat /tmp/b212-dbs.env
echo ""

echo "=== Step 5: skygate join status (text) ==="
/tmp/skygate_b212 join status --state-file=/tmp/b212-state.json
echo ""

echo "=== Step 6: skygate join status --json ==="
/tmp/skygate_b212 join status --state-file=/tmp/b212-state.json --json
echo ""

echo "=== Step 7: parse + verify DSN handling ==="
# Two paths tested:
#  - "no DSN" path: cluster_database.dsn_template is empty
#    → the DSN env file should have a comment line, no SKYGATE_DB_DSN key
#  - "with DSN" path: dsn_template is set → SKYGATE_DB_DSN=<substituted>
#    → test the substitution
# In this run we exercise the "no DSN" path (the live agent's
# dsn_template is empty after the B211 init).
if grep -q '^SKYGATE_DB_DSN=' /tmp/b212-dbs.env; then
    DSN_FROM_FILE=$(grep '^SKYGATE_DB_DSN=' /tmp/b212-dbs.env | cut -d= -f2-)
    echo "DSN from file: $DSN_FROM_FILE"
    if [[ "$DSN_FROM_FILE" == *"%s"* ]]; then
        echo "FAIL: DSN still has unsubstituted %s"
        exit 1
    fi
    if [[ -z "$DSN_FROM_FILE" ]]; then
        echo "FAIL: DSN key present but value is empty"
        exit 1
    fi
    echo "OK: DSN is fully substituted"
else
    echo "DSN from file: <unset — dsn_template is empty on the primary>"
    echo "OK: no DSN key is the correct behavior when the primary has no dsn_template"
fi
echo ""

echo "=== Step 8: re-run with bad token (should fail local verify or HTTP) ==="
/tmp/skygate_b212 join --api-url="$API_URL" --no-heartbeat-hint "sgn1.bad.token" 2>&1 || echo "(expected non-zero exit)"
echo ""

echo "=== Step 9: set dsn_template on primary, re-join, verify DSN is substituted ==="
# This exercises the WITH-DSN path. Set dsn_template on the
# primary's cluster_database row, then re-issue + re-join.
DSN_TPL='postgres://admin:skygate_admin_pass@%s:5433/skygate_staging?sslmode=disable'
docker exec skygate-skygate-1 sh -c "PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5433 -U admin -d skygate_staging -c \"UPDATE cluster_database SET dsn_template='$DSN_TPL' WHERE id='skygate-staging'\" 2>&1" | head -5
# Re-build the binary so the next run uses the latest code
$GO_BIN build -o /tmp/skygate_b212 ./cmd/skygate
TOKEN2="$(/tmp/skygate_b212 init standby-invite --ttl-hours=1 | head -1)"
echo "new token: ${TOKEN2:0:30}...${TOKEN2: -10}"
/tmp/skygate_b212 join \
    --api-url="$API_URL" \
    --state-file=/tmp/b212-state2.json \
    --write-dsn-to=/tmp/b212-dbs2.env \
    --no-heartbeat-hint \
    "$TOKEN2" > /tmp/b212-join2.stdout 2> /tmp/b212-join2.stderr
echo "stdout: $(cat /tmp/b212-join2.stdout | tr '\n' '|')"
echo "stderr: $(cat /tmp/b212-join2.stderr | tr '\n' '|')"
echo "--- dbs2.env contents ---"
cat /tmp/b212-dbs2.env
DSN2=$(grep '^SKYGATE_DB_DSN=' /tmp/b212-dbs2.env | cut -d= -f2-)
if [[ -z "$DSN2" ]]; then
    echo "FAIL: DSN2 is empty (dsn_template should be set now)"
    exit 1
fi
if [[ "$DSN2" == *"%s"* ]]; then
    echo "FAIL: DSN2 still has unsubstituted %s"
    exit 1
fi
echo "OK: DSN2 is fully substituted: $DSN2"
# Cleanup: reset dsn_template to empty so we don't leave test state
docker exec skygate-skygate-1 sh -c "PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5433 -U admin -d skygate_staging -c \"UPDATE cluster_database SET dsn_template='' WHERE id='skygate-staging'\" 2>&1" | head -2
