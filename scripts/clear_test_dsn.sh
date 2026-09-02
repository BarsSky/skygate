#!/usr/bin/env bash
# v1.5.0+ / post-B207 — clear the B207-verify test artifact
# from cluster_database.current_dsn.
#
# Background
#
# During B207 verify (B207-verify phase of the cluster-management
# plan), the test set cluster_database.current_dsn to a deliberately
# wrong DSN ('postgres://admin:skygate_admin_pass@...') so the
# /admin/audit UNION query could exercise the
# "audit_log + cluster_audit joined by a common DSN" path. The
# literal "skygate_admin_pass" password is the test artifact —
# not a real DSN.
#
# The B203 skygate-watchdog reads cluster_database.current_dsn
# on every 5s tick. If current_dsn differs from the env DSN (which
# it does, because the password literal is different), the watchdog
# closes the old pgxpool and opens a new one against the literal
# DSN — which fails (auth error), leaving the skygate in a broken
# state until the operator manually clears the row.
#
# B208/B209/B210 all hit this bug. The fix: any verify that
# touches cluster_database.current_dsn MUST call this script as
# the final step. b208_verify.sh is the canonical example.
#
# Usage:
#   bash scripts/clear_test_dsn.sh           # uses local socket + postgres user
#   sudo -u postgres psql -d skygate_staging -c "UPDATE cluster_database SET current_dsn = ''"
#                                              # equivalent one-liner
#
# The script is idempotent (running on an already-empty current_dsn
# is a no-op) and safe to run multiple times.

set -u

PSQL="sudo -u postgres psql -d skygate_staging -t -A -v ON_ERROR_STOP=1"

# Show the current state (informational, not blocking).
echo "=== before ==="
$PSQL -c "SELECT id, length(current_dsn) AS dsn_len, substring(current_dsn, 1, 60) AS dsn_prefix FROM cluster_database;"

# The cleanup.
$PSQL -c "UPDATE cluster_database SET current_dsn = '' WHERE id = 'skygate-staging' AND current_dsn IS NOT NULL AND current_dsn <> '' RETURNING id, length(current_dsn) AS new_dsn_len;" 2>&1

echo "=== after ==="
$PSQL -c "SELECT id, length(current_dsn) AS dsn_len FROM cluster_database;"

echo
echo "watchdog will no longer detect a DSN change on the next tick."
