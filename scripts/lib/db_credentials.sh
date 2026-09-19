#!/usr/bin/env bash
# scripts/lib/db_credentials.sh — resolve PostgreSQL credentials at runtime.
#
# RR-12: the default PostgreSQL password used to be hardcoded in dozens of
# live-check / live-verify scripts. Resolve it instead, in this order:
#
#   1. $SKYGATE_DB_PASSWORD
#   2. the password embedded in $SKYGATE_DB / $SKYGATE_DB_DSN
#   3. the password embedded in SKYGATE_DB / SKYGATE_DB_DSN inside the env file
#      ($SKYGATE_ENV_FILE, /etc/skygate/skygate.env, <repo>/.env,
#       /home/skyadmin/skygate/.env)
#   4. empty — psql then fails loudly ("no password supplied") instead of
#      silently using a password that lives in tracked code.
#
# Usage:
#   . "$(dirname "$0")/lib/db_credentials.sh"
#   SKYGATE_DB_PASSWORD="${SKYGATE_DB_PASSWORD:-$(skygate_db_password)}"
#   PGPASSWORD="$SKYGATE_DB_PASSWORD" psql -h ... -U admin -d skygate_staging

_skygate_pw_from_dsn() {
    printf '%s' "$1" | sed -nE 's#^[a-zA-Z][a-zA-Z0-9+.-]*://[^:/@]+:([^@]*)@.*#\1#p'
}

skygate_db_password() {
    if [ -n "${SKYGATE_DB_PASSWORD:-}" ]; then
        printf '%s' "$SKYGATE_DB_PASSWORD"
        return 0
    fi
    local v f k pw
    for v in "${SKYGATE_DB_DSN:-}" "${SKYGATE_DB:-}"; do
        [ -n "$v" ] || continue
        pw="$(_skygate_pw_from_dsn "$v")"
        if [ -n "$pw" ]; then printf '%s' "$pw"; return 0; fi
    done
    for f in "${SKYGATE_ENV_FILE:-}" /etc/skygate/skygate.env \
             "${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." 2>/dev/null && pwd)}/.env" \
             /home/skyadmin/skygate/.env; do
        if [ -n "$f" ] && [ -r "$f" ]; then
            for k in SKYGATE_DB SKYGATE_DB_DSN; do
                v="$(sed -n "s/^${k}=//p" "$f" | tail -1)"
                [ -n "$v" ] || continue
                pw="$(_skygate_pw_from_dsn "$v")"
                if [ -n "$pw" ]; then printf '%s' "$pw"; return 0; fi
            done
        fi
    done
    printf ''
}
