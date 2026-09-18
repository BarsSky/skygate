#!/usr/bin/env bash
# B213 live-verify on the agent.
#  1. `skygate migrate status` — should show applied=0
#     pending=N (where N=number of migrations in the
#     binary), extra=0 (the bookkeeping table was
#     empty pre-B213).
#  2. `skygate migrate up` — should back-fill all
#     migrations (write to applied_migrations).
#  3. `skygate migrate status` (re-run) — should show
#     applied=N pending=0.
#  4. `skygate migrate up` (re-run) — should be
#     idempotent (no error, applied=0 delta).
#  5. `skygate migrate down --target=20` — should
#     return a clear "not implemented" error.
set -euo pipefail
cd /home/skyadmin/skygate

# Load .env
set --
set -a
# shellcheck disable=SC1091
. /home/skyadmin/skygate/.env
set +a

# Build with the B213 code
GO_BIN="${GO_BIN:-/snap/go/current/bin/go}"
export GOCACHE="/tmp/go-cache"
export GOMODCACHE="/tmp/go-modcache"
mkdir -p "$GOCACHE" "$GOMODCACHE"
$GO_BIN build -o /tmp/skygate_b213 ./cmd/skygate

echo "=== Step 1: skygate migrate status (pre-backfill) ==="
/tmp/skygate_b213 migrate status 2>&1 | tail -5
echo ""

echo "=== Step 2: skygate migrate up (back-fill applied_migrations) ==="
/tmp/skygate_b213 migrate up 2>&1
echo ""

echo "=== Step 3: skygate migrate status (post-backfill) ==="
/tmp/skygate_b213 migrate status 2>&1 | tail -8
echo ""

echo "=== Step 4: skygate migrate up (re-run, should be idempotent) ==="
/tmp/skygate_b213 migrate up 2>&1
echo ""

echo "=== Step 5: skygate migrate down (stub) ==="
/tmp/skygate_b213 migrate down --target=20 2>&1 || echo "(expected non-zero exit)"
echo ""

echo "=== Step 6: skygate migrate status --json (machine-readable) ==="
/tmp/skygate_b213 migrate status --json 2>&1 | head -30
echo ""
