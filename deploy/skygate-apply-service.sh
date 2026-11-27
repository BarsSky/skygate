#!/bin/sh
# ============================================================================
# skygate-apply-service.sh — B533 (2026-11-27)
#
# PRIVILEGED half of the "restart headscale from the panel" action.
#
# WHY THIS EXISTS
#
# The native skygate unit runs as an unprivileged service user under
# ProtectSystem=strict, so `systemctl restart headscale` is not a right it has.
# Until B533 the operator's only routes were SSH or the console — and on
# 2026-11-27 the panel itself was unreachable precisely because the control
# plane was down (the provider moved the VM to a new IPv4 while the zone kept the
# old one), so "restart headscale from the panel" had to exist for the next time.
#
# The design is the one the policy (B272.3) and local-routes (B293) appliers
# already use: the unprivileged service drops a DATA-ONLY request file into its
# own data directory, a root-owned path unit turns its appearance into this
# script, and the verdict is written back as data for skygate to read.
#
# SECURITY MODEL (read before changing anything here)
#
#   * request.props is written by an UNPRIVILEGED process. It is PARSED AS DATA,
#     never sourced, and every value must match a strict character class.
#   * ACTION and SERVICE are checked against ALLOW-LISTS. This script cannot be
#     asked to stop sshd, restart an arbitrary unit, or pass an argument through:
#     a compromised skygate gets "restart headscale|skygate|caddy|derper|
#     tailscaled" and nothing else.
#   * No value from the request ever reaches a shell — the service name is one
#     argv element after the allow-list check.
#
# REQUEST (<update_dir>/service.request.props)
#   ACTION=restart            # restart | status
#   SERVICE=headscale         # allow-listed name
#   REQUESTED_AT=2026-11-27T13:00:00Z
#   REQUESTED_BY=skygate
#
# STATUS (<update_dir>/service.apply.status, written atomically)
#   RESULT=ok|failed
#   TS=<utc>
#   STATE=<the manager's state AFTER the action>
#   COMMAND=<what ran>
#   REASON=<failure text, empty on success>
#
# CONFIG (/etc/skygate/service-helper.conf, root-owned, optional)
#   SKYGATE_UPDATE_DIR="/var/lib/skygate/update"
#   SKYGATE_ALLOWED_SERVICES="headscale skygate caddy derper tailscaled"
# ============================================================================
set -uo pipefail

CONF="${SKYGATE_SERVICE_HELPER_CONF:-/etc/skygate/service-helper.conf}"
# shellcheck disable=SC1090
[ -r "$CONF" ] && . "$CONF"

UPDATE_DIR="${SKYGATE_UPDATE_DIR:-/var/lib/skygate/update}"
REQ="${SKYGATE_SERVICE_REQUEST_PATH:-$UPDATE_DIR/service.request.props}"
STATUS="$UPDATE_DIR/service.apply.status"
LOG="$UPDATE_DIR/service-apply.log"
ALLOWED="${SKYGATE_ALLOWED_SERVICES:-headscale skygate caddy derper tailscaled}"

log() {
    printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG" 2>/dev/null || true
    printf '%s\n' "$*"
}

# finish <result> <state> <command> <reason> — the single exit point. Always
# writes the verdict, always removes the request so the path unit re-arms.
finish() {
    _result="$1"; _state="$2"; _command="$3"; _reason="${4:-}"
    _tmp="$STATUS.tmp.$$"
    {
        printf 'RESULT=%s\n' "$_result"
        printf 'TS=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        printf 'STATE=%s\n' "$_state"
        printf 'COMMAND=%s\n' "$_command"
        printf 'REASON=%s\n' "$_reason"
    } > "$_tmp" 2>/dev/null && chmod 0644 "$_tmp" 2>/dev/null && mv -f "$_tmp" "$STATUS" 2>/dev/null
    rm -f "$REQ" 2>/dev/null || true
    log "verdict: $_result (state=$_state command=$_command${_reason:+ reason=$_reason})"
    exit 0
}

# read_prop <key> — first matching line's value, unquoted. DATA ONLY.
read_prop() {
    _key="$1"
    [ -r "$REQ" ] || return 0
    while IFS= read -r _line; do
        case "$_line" in
            "$_key"=*) printf '%s' "${_line#*=}" ;;
        esac
    done < "$REQ" | head -n 1
}

# valid_word <value> — letters, digits, dash, underscore, dot. Nothing else.
valid_word() {
    case "$1" in
        ""|*[!A-Za-z0-9._-]*) return 1 ;;
        *) return 0 ;;
    esac
}

if [ ! -r "$REQ" ]; then
    log "no request at $REQ — nothing to do"
    exit 3
fi

ACTION="$(read_prop ACTION)"
SERVICE="$(read_prop SERVICE)"

if ! valid_word "$ACTION" || ! valid_word "$SERVICE"; then
    finish failed "" "" "request contains characters outside [A-Za-z0-9._-]: action='$ACTION' service='$SERVICE'"
fi

case " $ALLOWED " in
    *" $SERVICE "*) ;;
    *) finish failed "" "" "service '$SERVICE' is not in the allow-list ($ALLOWED)" ;;
esac

case "$ACTION" in
    restart|status) ;;
    *) finish failed "" "" "action '$ACTION' is not in the allow-list (restart|status)" ;;
esac

# --- who manages the service ------------------------------------------------
MANAGER=""
if command -v systemctl > /dev/null 2>&1; then
    if systemctl list-unit-files --no-legend --no-pager "$SERVICE.service" 2>/dev/null | grep -q "$SERVICE.service"; then
        MANAGER=systemd
    fi
fi
if [ -z "$MANAGER" ] && command -v rc-service > /dev/null 2>&1 && [ -e "/etc/init.d/$SERVICE" ]; then
    MANAGER=openrc
fi
if [ -z "$MANAGER" ] && command -v docker > /dev/null 2>&1; then
    if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "$SERVICE"; then
        MANAGER=docker
    fi
fi

if [ -z "$MANAGER" ]; then
    finish failed "absent" "" "no systemd unit, OpenRC service or docker container named '$SERVICE' on this host"
fi

state_of() {
    case "$MANAGER" in
        systemd)
            _a="$(systemctl show "$SERVICE" --property=ActiveState --value 2>/dev/null)"
            _s="$(systemctl show "$SERVICE" --property=SubState --value 2>/dev/null)"
            printf '%s/%s' "${_a:-unknown}" "${_s:-unknown}"
            ;;
        openrc)
            rc-service "$SERVICE" status 2>&1 | head -n 1 | tr -d ' \t'
            ;;
        docker)
            docker inspect -f '{{.State.Status}}' "$SERVICE" 2>/dev/null || printf 'unknown'
            ;;
    esac
}

if [ "$ACTION" = "status" ]; then
    finish ok "$(state_of)" "status $SERVICE" ""
fi

# --- restart ----------------------------------------------------------------
case "$MANAGER" in
    systemd)
        # AGENTS trap #6: a crashed unit refuses to restart until it is reset.
        systemctl is-failed --quiet "$SERVICE" 2>/dev/null && systemctl reset-failed "$SERVICE" >> "$LOG" 2>&1
        cmd="systemctl restart $SERVICE"
        if out="$(systemctl restart "$SERVICE" 2>&1)"; then
            sleep 1
            finish ok "$(state_of)" "$cmd" ""
        fi
        finish failed "$(state_of)" "$cmd" "$out"
        ;;
    openrc)
        cmd="rc-service $SERVICE restart"
        if out="$(rc-service "$SERVICE" restart 2>&1)"; then
            sleep 1
            finish ok "$(state_of)" "$cmd" ""
        fi
        finish failed "$(state_of)" "$cmd" "$out"
        ;;
    docker)
        cmd="docker restart $SERVICE"
        if out="$(docker restart "$SERVICE" 2>&1)"; then
            sleep 1
            finish ok "$(state_of)" "$cmd" ""
        fi
        finish failed "$(state_of)" "$cmd" "$out"
        ;;
esac
