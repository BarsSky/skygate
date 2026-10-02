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

# ---------------------------------------------------------------------------
# Live-database access (B336 / TD-22)
# ---------------------------------------------------------------------------
# WHY THIS EXISTS. Every live-state B-check used to do this:
#
#     DSN=$(grep '^SKYGATE_DB_DSN' .env | cut -d= -f2-)
#     if psql "$DSN" -tAc "SELECT ..." 2>/dev/null | grep -q '^1$'; then ok ...; else
#         bad "B.4 derp_health table NOT in live DB"    # ← a FABRICATED FACT
#     fi
#
# On the reference deployment that `else` branch is what always ran, because
# the DSN's host is `skygate-pg-local` — a docker DNS name, which is the
# CORRECT value (B278 removed the bridge IP from the DSN on purpose: the IP
# rotates on every container recreate) but is resolvable only INSIDE the
# compose network. The host could not resolve it, the DNS error went to
# /dev/null, and four checks announced that tables were missing, fixture rows
# were present and counts were zero when the truth was "this check never
# reached the database". Measured on the VM 2026-10-01:
#
#     psql: error: could not translate host name "skygate-pg-local" to address
#
# That is the B294 lesson (a failed read must not render as a fact) applied to
# the shell checks, and it breaks AGENTS rule 1 (a check without its dependency
# must SKIP, never FAIL).
#
# HOW IT CONNECTS. If the DSN host names a docker container, psql runs INSIDE
# that container — no host networking, no address to resolve, immune to the
# bridge-IP rotation B278 is about, and exactly what docs/operations.md §15
# tells the operator to do by hand. Otherwise (external PostgreSQL: Patroni,
# deploy/pg-ha, a plain host) the DSN is used as-is, which is the case where
# the host genuinely can resolve it.
#
# USAGE
#   . "$(dirname "$0")/lib/db_credentials.sh"
#   if ! skygate_live_db_probe; then
#       skip "live DB: $(skygate_live_db_reason)"
#   else
#       n=$(skygate_live_db_query "SELECT count(*) FROM ...")
#   fi
#
# `skygate_live_db_probe` returns non-zero when the database cannot be
# reached, so no caller can accidentally treat an unreachable database as a
# zero row count.

# skygate_db_dsn prints the first DSN it can find, in the same order
# skygate_db_password uses, and nothing when there is none.
skygate_db_dsn() {
    if [ -n "${SKYGATE_DB_DSN:-}" ]; then
        printf '%s' "$SKYGATE_DB_DSN"
        return 0
    fi
    local f v
    for f in "${SKYGATE_ENV_FILE:-}" /etc/skygate/skygate.env \
             "${SKYGATE_REPO_DIR:-${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." 2>/dev/null && pwd)}}/.env" \
             /home/skyadmin/skygate/.env; do
        if [ -n "$f" ] && [ -r "$f" ]; then
            v="$(sed -n 's/^SKYGATE_DB_DSN=//p' "$f" | tail -1)"
            if [ -n "$v" ]; then printf '%s' "$v"; return 0; fi
        fi
    done
    printf ''
}

# _skygate_dsn_field <dsn> <user|host|port|db>
_skygate_dsn_field() {
    local dsn="$1" what="$2" rest
    case "$what" in
        user) printf '%s' "$dsn" | sed -nE 's#^[a-zA-Z][a-zA-Z0-9+.-]*://([^:/@]+).*#\1#p' ;;
        host) printf '%s' "$dsn" | sed -nE 's#^[a-zA-Z][a-zA-Z0-9+.-]*://([^@]*@)?([^:/?]+).*#\2#p' ;;
        port) printf '%s' "$dsn" | sed -nE 's#^[a-zA-Z][a-zA-Z0-9+.-]*://([^@]*@)?[^:/?]+:([0-9]+).*#\2#p' ;;
        db)   rest="${dsn##*/}"; printf '%s' "${rest%%\?*}" ;;
    esac
}

# _skygate_docker runs docker, falling back to `sudo -n docker` for a host whose
# socket is root-owned. WHICH prefix to use is decided ONCE (`docker info`) and
# memoised in SKYGATE_DOCKER_CMD, because the earlier "try plain, then try sudo"
# form ran the command twice on failure — which duplicated psql's error text and
# would re-run a non-idempotent statement. stderr is preserved either way: a
# caller that wants it quiet must say so, because swallowing it is exactly how a
# failed read becomes a fact (B336).
_skygate_docker() {
    case "${SKYGATE_DOCKER_CMD:-}" in
        plain) docker "$@"; return $? ;;
        sudo)  sudo -n docker "$@"; return $? ;;
    esac
    if docker info >/dev/null 2>&1; then
        SKYGATE_DOCKER_CMD="plain"
        docker "$@"
        return $?
    fi
    if command -v sudo >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then
        SKYGATE_DOCKER_CMD="sudo"
        sudo -n docker "$@"
        return $?
    fi
    SKYGATE_DOCKER_CMD="plain"
    docker "$@"
    return $?
}

# skygate_live_db_probe — 0 when the live database answers, non-zero when it
# does not. Sets SKYGATE_LIVE_DB_ERR (one line, the real error), and on success
# SKYGATE_LIVE_DB_MODE (docker|dsn) plus the pieces the query helper needs.
skygate_live_db_probe() {
    SKYGATE_LIVE_DB_ERR=""
    SKYGATE_LIVE_DB_MODE=""
    local dsn host user port db
    dsn="$(skygate_db_dsn)"
    if [ -z "$dsn" ]; then
        SKYGATE_LIVE_DB_ERR="no SKYGATE_DB_DSN found (env, \$SKYGATE_ENV_FILE, /etc/skygate/skygate.env, <repo>/.env, /home/skyadmin/skygate/.env)"
        return 1
    fi
    host="$(_skygate_dsn_field "$dsn" host)"
    user="$(_skygate_dsn_field "$dsn" user)"
    port="$(_skygate_dsn_field "$dsn" port)"
    db="$(_skygate_dsn_field "$dsn" db)"
    [ -n "$user" ] || user="postgres"
    [ -n "$port" ] || port="5432"
    [ -n "$db" ] || db="postgres"

    # Is the DSN host a container we can reach? (The reference deployment's
    # `skygate-pg-local`, and any compose service name.)
    if command -v docker >/dev/null 2>&1 &&
       _skygate_docker inspect -f '{{.State.Running}}' "$host" 2>/dev/null | grep -q true; then
        SKYGATE_LIVE_DB_MODE="docker"
        SKYGATE_LIVE_DB_CONTAINER="$host"
        SKYGATE_LIVE_DB_USER="$user"
        SKYGATE_LIVE_DB_DB="$db"
        local err out
        err="$(_skygate_docker exec "$host" psql -U "$user" -d "$db" -tAc 'SELECT 1' 2>&1 >/dev/null | head -1)"
        out="$(_skygate_docker exec "$host" psql -U "$user" -d "$db" -tAc 'SELECT 1' 2>/dev/null)"
        if [ "$(printf '%s' "$out" | tr -d '[:space:]')" = "1" ]; then
            SKYGATE_LIVE_DB_DESC="docker exec $host psql -U $user -d $db"
            return 0
        fi
        SKYGATE_LIVE_DB_ERR="container '$host' is running but psql inside it failed: ${err:-no output}"
        return 1
    fi

    # Not a container: an external PostgreSQL the host is expected to resolve.
    SKYGATE_LIVE_DB_MODE="dsn"
    SKYGATE_LIVE_DB_DSN="$dsn"
    SKYGATE_LIVE_DB_HOST="$host"
    SKYGATE_LIVE_DB_PORT="$port"
    SKYGATE_LIVE_DB_USER="$user"
    SKYGATE_LIVE_DB_DB="$db"
    if ! command -v psql >/dev/null 2>&1; then
        SKYGATE_LIVE_DB_ERR="host '$host' is not a local container and psql is not installed on this host"
        return 1
    fi
    local perr pout
    perr="$(PGPASSWORD="$(skygate_db_password)" psql -h "$host" -p "$port" -U "$user" -d "$db" -tAc 'SELECT 1' 2>&1 >/dev/null | head -1)"
    pout="$(PGPASSWORD="$(skygate_db_password)" psql -h "$host" -p "$port" -U "$user" -d "$db" -tAc 'SELECT 1' 2>/dev/null)"
    if [ "$(printf '%s' "$pout" | tr -d '[:space:]')" = "1" ]; then
        SKYGATE_LIVE_DB_DESC="psql -h $host -p $port -U $user -d $db"
        return 0
    fi
    SKYGATE_LIVE_DB_ERR="psql -h $host -p $port -d $db failed: ${perr:-no output}"
    return 1
}

# skygate_live_db_reason — one line naming WHY the live database is unusable,
# suitable for a SKIP row. Always non-empty after a failed probe.
skygate_live_db_reason() {
    printf 'live database unreachable: %s' "${SKYGATE_LIVE_DB_ERR:-unknown error}"
}

# skygate_live_db_query <sql> [separator] — run one statement and print the rows
# (unaligned, tuples only; `separator` sets -F for multi-column results).
#
# Returns non-zero and prints nothing when the database is unreachable OR when
# the statement itself fails, and records why in SKYGATE_LIVE_DB_QUERY_ERR. That
# second case matters: a syntax error used to be swallowed by `2>/dev/null` and
# came back as an empty result, i.e. the same fabricated fact as a DNS failure —
# measured while writing B337, where a malformed `GROUP BY 1` read as "no rows"
# until the error was surfaced. Callers MUST have probed first; the probe is what
# separates "zero rows" from "no connection".
skygate_live_db_query() {
    local sql="$1" sep="${2:-}"
    local -a f=()
    [ -n "$sep" ] && f=(-F "$sep")
    local out rc errf
    errf="$(mktemp 2>/dev/null || echo /tmp/skygate-liveq.$$)"
    case "${SKYGATE_LIVE_DB_MODE:-}" in
        docker)
            out="$(_skygate_docker exec "$SKYGATE_LIVE_DB_CONTAINER" \
                psql -U "$SKYGATE_LIVE_DB_USER" -d "$SKYGATE_LIVE_DB_DB" -A -t "${f[@]}" -c "$sql" 2>"$errf")"
            rc=$?
            ;;
        dsn)
            out="$(PGPASSWORD="$(skygate_db_password)" psql -h "$SKYGATE_LIVE_DB_HOST" \
                -p "$SKYGATE_LIVE_DB_PORT" -U "$SKYGATE_LIVE_DB_USER" \
                -d "$SKYGATE_LIVE_DB_DB" -A -t "${f[@]}" -c "$sql" 2>"$errf")"
            rc=$?
            ;;
        *)
            rm -f "$errf"
            SKYGATE_LIVE_DB_QUERY_ERR="no live-database connection (call skygate_live_db_probe first)"
            return 1
            ;;
    esac
    if [ "$rc" -ne 0 ]; then
        SKYGATE_LIVE_DB_QUERY_ERR="$(tr '\n' ' ' < "$errf" 2>/dev/null | tr -s ' ' | cut -c1-400)"
        rm -f "$errf"
        return 1
    fi
    rm -f "$errf"
    SKYGATE_LIVE_DB_QUERY_ERR=""
    printf '%s' "$out"
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
