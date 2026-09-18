#!/bin/bash
# B208 verify — exercise /admin/ha + the other admin
# pages AFTER the B203 watchdog's first hot-reload.
# Pre-B208, these would return 500 (sql: database
# is closed). Post-B208, they return 200.
cd ~/skygate
SECRET=$(grep '^SKYGATE_JWT_SECRET' .env | cut -d= -f2)
echo "secret length: ${#SECRET}"
go run scripts/ha_jwt_token.go "$SECRET" 2>&1

# v1.5.0+ / post-B207 — clear the test artifact from
# cluster_database.current_dsn so the B203 watchdog doesn't
# keep swapping on every 5s tick after the verify. See
# scripts/clear_test_dsn.sh for the full rationale.
bash scripts/clear_test_dsn.sh
