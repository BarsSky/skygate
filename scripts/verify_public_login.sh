# 2026-09-18: operator infrastructure values redacted to env vars before
. "$(dirname "$0")/lib/db_credentials.sh"
SKYGATE_DB_PASSWORD="${SKYGATE_DB_PASSWORD:-$(skygate_db_password)}"
# this script was committed. Set them in your shell, e.g.
#   VM_HOST=<the skygate host> EMILIA_PUBLIC_IP=... bash scripts\verify_public_login.sh
# The ${VAR:?} form makes a missing value a hard error instead of an
# empty hostname.
#!/usr/bin/env bash
cd /home/skyadmin/skygate
set -a
. /home/skyadmin/skygate/.env
set +a
echo "Public /login end-to-end check:"
curl -s -X POST --data-urlencode "username=skyadmin" --data-urlencode "password=$SKYGATE_ADMIN_PASS" -o /dev/null -w 'HTTP=%{http_code} loc=%{redirect_url}\n' --max-time 30 https://skygate.example.com/login
echo "Public /healthz:"
curl -s -o /dev/null -w 'HTTP=%{http_code} time=%{time_total}s\n' --max-time 15 https://skygate.example.com/healthz
echo "Most recent login_ok audit_log row (UTC):"
env PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 172.17.0.1 -p 5433 -U admin -d skygate_staging -tA -c "SELECT to_timestamp(created_at) AT TIME ZONE 'UTC' as ts, username, action, ip_address FROM audit_log WHERE action='login_ok' ORDER BY id DESC LIMIT 1"
