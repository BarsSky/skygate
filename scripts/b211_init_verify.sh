#!/usr/bin/env bash
# B211 live-verify on the agent.
# Loads .env, runs the 3 init subcommands, and verifies
# idempotency (re-run produces the same node_id + same
# standby token, NOT a new row).
set -euo pipefail
cd /home/skyadmin/skygate

# Load .env into the shell (the .env is what the running
# container uses, so we use the same values for the
# one-off CLI invocation).
# Note: we MUST unset positional params (set --) BEFORE
# `set -a` so the auto-export doesn't pick up --role= as
# a variable name. (set -a exports every variable
# assignment, including $1 if we don't clear it first.)
set --
set -a
# shellcheck disable=SC1091
. /home/skyadmin/skygate/.env
set +a

echo "=== hostname=$(hostname) SKYGATE_TS_HOSTNAME=$SKYGATE_TS_HOSTNAME ==="
echo "=== DSN=$SKYGATE_DB_DSN ==="
echo ""

echo "--- run 1: skygate init (bootstrap) ---"
/tmp/skygate_b211 init --role=skygate
echo ""

echo "--- run 2: skygate init (re-run, should be idempotent) ---"
/tmp/skygate_b211 init --role=skygate
echo ""

echo "--- run 3: skygate init status (text) ---"
/tmp/skygate_b211 init status
echo ""

echo "--- run 4: skygate init status --json ---"
/tmp/skygate_b211 init status --json
echo ""

echo "--- run 5: skygate init standby-invite (fresh token) ---"
/tmp/skygate_b211 init standby-invite --ttl-hours=1
echo ""
