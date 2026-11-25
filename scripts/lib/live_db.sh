#!/usr/bin/env bash
# scripts/lib/live_db.sh — ONE WAY FOR A LIVE-STATE CHECK TO READ THE INSTALLATION (B488 / TD-37).
#
# THE DEBT THIS CLOSES
#
# Six live-state contracts (B243-duplicate-users, B243-reconcile-audit, B243-node-owner-map-orphans,
# B-mod-tag-owners-coverage, B188.2, B188.3) were DOCKER-SHAPED: they ran
# `docker exec headscale headscale …` and `docker exec skygate-pg-local psql …` directly. On a NATIVE
# (systemd) install — the production shape since the 2026-11-15 migration — those commands cannot
# answer anything, so the six checks SKIPped and nobody verified duplicate portal users, orphaned
# node_owner_map rows or tagOwners coverage on the host that actually matters.
#
# WHAT IT PROVIDES
#
#   live_kind                    → "docker" | "native" | "none"
#   live_headscale <args…>       → runs the headscale CLI on whichever shape this host is
#   live_db_sql [-F<sep>] <SQL>  → runs one SQL statement against the PANEL database, printing rows
#                                  the way `psql -At` / `sqlite3` do (one row per line, fields separated
#                                  by the -F separator, empty result = empty output)
#   live_db_dsn                  → the DSN the panel runs with, whatever the shape
#
# The caller keeps its own contract logic; only the transport moves here. Every function returns
# non-zero when the shape cannot answer, and the checks keep their existing SKIP semantics.
#
# HOW THE SHAPE IS DECIDED
#
#   docker — `skygate-skygate-1` AND `headscale` containers are RUNNING (the compose install);
#   native — the env file exists (the systemd install): the headscale CLI is the local binary and the
#            panel DB is the one named by SKYGATE_DB in that file
#            (`sqlite:/var/lib/skygate/skygate.db` or a postgres:// DSN);
#   none   — neither: every function fails, and the check reports SKIP.
#
# Deliberately NOT detected over HTTP: a reachable /healthz says nothing about whether the DB and the
# headscale CLI on THIS host can answer (the retired host forwarded both through a tunnel while
# having no containers — the measurement B479 recorded).
#
# USAGE (after the B479 guard):
#
#   . "$(dirname "$0")/lib/live_db.sh"
#   live_headscale users list -o json
#   live_db_sql "SELECT count(*) FROM portal_users;"
#   live_db_sql -F'|' "SELECT node_id, username FROM node_owner_map;"
#
# SKYGATE_LIVE_ENV_FILE overrides the native env-file path (default /etc/skygate/skygate.env). It
# exists so the B488 contract can prove the native transport against a throwaway file instead of
# writing to /etc on a developer workstation.

_live_db_kind=""

# live_env_file — the env file that describes the native install on THIS host.
live_env_file() {
    printf '%s\n' "${SKYGATE_LIVE_ENV_FILE:-/etc/skygate/skygate.env}"
}

live_kind() {
    if [ -n "$_live_db_kind" ]; then printf '%s\n' "$_live_db_kind"; return 0; fi
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 \
       && [ "$(docker inspect -f '{{.State.Running}}' skygate-skygate-1 2>/dev/null)" = "true" ] \
       && [ "$(docker inspect -f '{{.State.Running}}' headscale 2>/dev/null)" = "true" ]; then
        _live_db_kind=docker
    elif [ -f "$(live_env_file)" ]; then
        _live_db_kind=native
    else
        _live_db_kind=none
    fi
    printf '%s\n' "$_live_db_kind"
}

# _live_docker <args…> — docker for THIS shape. root talks to the daemon directly, a docker-group
# member does not need sudo, and only where neither works is one passwordless sudo attempted (the
# pre-B488 checks ran a bare `docker exec`, so that plain form stays the fallback).
_live_docker() {
    if [ "$(id -u)" -eq 0 ]; then docker "$@"
    elif docker info >/dev/null 2>&1; then docker "$@"
    else sudo -n docker "$@"; fi
}

# _live_env_value KEY — reads one KEY=value from the native env file (no shell evaluation).
_live_env_value() {
    sed -n "s/^$1=//p" "$(live_env_file)" 2>/dev/null | head -1
}

# live_headscale <args…> — the headscale CLI for THIS shape.
live_headscale() {
    case "$(live_kind)" in
        docker)
            _live_docker exec headscale headscale "$@" 2>/dev/null
            ;;
        native)
            local bin
            bin="$(command -v headscale 2>/dev/null || true)"
            [ -n "$bin" ] || bin=/usr/local/bin/headscale
            [ -x "$bin" ] || return 1
            # /etc/headscale/config.yaml is root-only on a native install; sudo -n is passwordless for
            # the operator user on the reference host (AGENTS trap #13 says to reach for root only when
            # the check genuinely needs it — reading another service's config is such a case).
            if [ "$(id -u)" -eq 0 ]; then "$bin" "$@"; else sudo -n "$bin" "$@"; fi 2>/dev/null
            ;;
        *)
            return 1
            ;;
    esac
}

# live_db_sql [-F<sep>] <SQL> — one statement against the PANEL database.
live_db_sql() {
    local sep='|' sql
    case "${1:-}" in
        -F*) sep="${1#-F}"; shift ;;
    esac
    sql="${1:-}"
    [ -n "$sql" ] || return 2

    case "$(live_kind)" in
        docker)
            # The compose install keeps the panel on SQLite under /data inside the container; when the
            # deployment uses PostgreSQL the PG container carries the database named in SKYGATE_DB.
            local dsn inner dbpart db dbname path
            dsn="$(_live_docker exec skygate-skygate-1 sh -c 'env | grep "^SKYGATE_DB=" | head -1 | cut -d= -f2-' 2>/dev/null)"
            case "$dsn" in
                postgres*|postgresql*)
                    inner="${dsn#*://}"; dbpart="${inner##*/}"; db="${dbpart%%\?*}"
                    _live_docker exec skygate-pg-local psql -U admin -d "${db:-skygate_staging}" -At -F"$sep" -c "$sql" 2>/dev/null
                    ;;
                *)
                    path="${dsn#sqlite:}"
                    [ -n "$path" ] || path=/data/skygate.db
                    _live_docker exec skygate-skygate-1 sqlite3 "$path" "$sql" 2>/dev/null
                    ;;
            esac
            ;;
        native)
            local dsn path out rc
            dsn="$(_live_env_value SKYGATE_DB)"
            [ -n "$dsn" ] || dsn="sqlite:/var/lib/skygate/skygate.db"
            case "$dsn" in
                postgres*|postgresql*)
                    command -v psql >/dev/null 2>&1 || return 1
                    # Same rule as the SQLite branch below (B522): try the caller's own
                    # credentials first, and treat sudo as the reserve, not the default.
                    if [ "$(id -u)" -eq 0 ]; then psql "$dsn" -At -F"$sep" -c "$sql" 2>/dev/null
                    else
                        out="$(psql "$dsn" -At -F"$sep" -c "$sql" 2>/dev/null)"; rc=$?
                        if [ "$rc" -eq 0 ]; then printf '%s\n' "$out"
                        else sudo -n psql "$dsn" -At -F"$sep" -c "$sql" 2>/dev/null; fi
                    fi
                    ;;
                *)
                    path="${dsn#sqlite:}"
                    command -v sqlite3 >/dev/null 2>&1 || return 1
                    # B522 (measured 2026-11-25): a NON-ROOT caller was always routed through
                    # `sudo -n`, even when the database file was readable to it directly. On the
                    # reference host the gate runs as a user without passwordless sudo
                    # ("User gate is not allowed to run sudo on svyatoslava"), so every read
                    # returned an EMPTY string with a non-zero status — and a caller that
                    # compared the output (check_b488 F6) reported the product as broken.
                    # Read it ourselves first and reach for sudo only when that is refused.
                    # The decision is made on the EXIT STATUS, not on the output: a SELECT that
                    # legitimately matches no rows returns 0 with an empty answer, and treating
                    # that as "no access" would silently turn an empty result into a retry.
                    if [ "$(id -u)" -eq 0 ]; then sqlite3 -readonly "$path" "$sql" 2>/dev/null
                    else
                        out="$(sqlite3 -readonly "$path" "$sql" 2>/dev/null)"; rc=$?
                        if [ "$rc" -eq 0 ]; then printf '%s\n' "$out"
                        else sudo -n sqlite3 -readonly "$path" "$sql" 2>/dev/null; fi
                    fi
                    ;;
            esac
            ;;
        *)
            return 1
            ;;
    esac
}

# live_db_dsn — the panel DSN for THIS shape (what SKYGATE_DB says), so a check can print or branch on
# it without knowing whether it runs in a container or on systemd.
live_db_dsn() {
    case "$(live_kind)" in
        docker)
            _live_docker exec skygate-skygate-1 sh -c 'env | grep "^SKYGATE_DB=" | head -1 | cut -d= -f2-' 2>/dev/null
            ;;
        native)
            local dsn
            dsn="$(_live_env_value SKYGATE_DB)"
            [ -n "$dsn" ] && printf '%s\n' "$dsn" || printf '%s\n' 'sqlite:/var/lib/skygate/skygate.db'
            ;;
        *)
            return 1
            ;;
    esac
}
