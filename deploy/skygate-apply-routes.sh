#!/usr/bin/env bash
# deploy/skygate-apply-routes.sh — B293 (2026-09-23)
#
# PRIVILEGED half of the LOCAL exit-node route apply.
#
# WHY THIS EXISTS
#
# When the exit node IS the machine that runs skygate (live on the operator's
# `aro`: headscale + skygate + the exit node in one place, the local tailscaled
# being `exit-node-vps` / 100.64.0.1), the advertised routes must be applied with
# `tailscale set --advertise-exit-node --advertise-routes=…` against the LOCAL
# daemon. Doing it over SSH meant an SSH session from the host to itself, through
# the very tailnet it configures: pointless (a key + authorized_keys on the same
# box) and fragile (it fails exactly when the local tailscaled needs repair).
#
# `tailscale set` needs root (or the daemon's `--operator` user). skygate tries,
# in order: `tailscale set` directly, then `sudo -n tailscale set`, then THIS
# script — the same privilege split the headscale policy write uses (B272.3): the
# unprivileged service drops a DATA-ONLY request file and a root-owned path unit
# runs this applier. It works on installs where the service has no root, no sudo
# and no operator grant at all.
#
# REQUEST (written by skygate, mode 0644, inside its own data dir):
#   ROUTES=0.0.0.0/0,::/0,10.0.0.0/8
#   ACCEPT_ROUTES=0            # -1 | 0 | 1
#   REQUESTED_AT=2026-09-23T05:00:00Z
#   REQUESTED_BY=skygate
#
# Every value is re-validated here and passed as ONE argv element — the request is
# data, never code.
#
# STATUS (read by skygate so it can report what actually happened):
#   RESULT=ok|failed
#   TS=<utc>
#   ROUTES=<applied list>
#   COMMAND=<the command that ran>
#   REASON=<failure text, empty on success>
#
# CONFIG (/etc/skygate/routes-helper.conf, root-owned, optional):
#   SKYGATE_UPDATE_DIR="/var/lib/skygate/update"
#   SKYGATE_TAILSCALE_CLI="/usr/bin/tailscale"

set -uo pipefail

CONF="${SKYGATE_ROUTES_HELPER_CONF:-/etc/skygate/routes-helper.conf}"
# shellcheck disable=SC1090
[ -r "$CONF" ] && . "$CONF"

UPDATE_DIR="${SKYGATE_UPDATE_DIR:-/var/lib/skygate/update}"
REQ="${SKYGATE_ROUTES_REQUEST_PATH:-$UPDATE_DIR/routes.request.props}"
STATUS="$UPDATE_DIR/routes-apply.status"
LOG="$UPDATE_DIR/routes-apply.log"
TS_BIN="${SKYGATE_TAILSCALE_CLI:-tailscale}"

ROUTES=""
ACCEPT_ROUTES="0"
COMMAND=""

status_write() {
    _res="$1"; _reason="${2:-}"
    printf 'RESULT=%s\nTS=%s\nROUTES=%s\nCOMMAND=%s\nREASON=%s\n' \
        "$_res" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$ROUTES" "$COMMAND" "$_reason" \
        > "$STATUS.tmp" 2>/dev/null && mv -f "$STATUS.tmp" "$STATUS" 2>/dev/null || true
    chmod 0644 "$STATUS" 2>/dev/null || true
}

log() {
    printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG" 2>/dev/null || true
    printf '%s\n' "$*"
}

fail() {
    log "ERROR: $*"
    status_write failed "$*"
    rm -f "$REQ" 2>/dev/null || true
    exit 1
}

# The path unit fires on appearance; a spurious run (file already consumed, or a
# unit restart) must be a silent no-op.
[ -f "$REQ" ] || exit 0

# ---------- 1. parse the request (data only) --------------------------------
while IFS= read -r line; do
    case "$line" in
        ROUTES=*)        ROUTES="${line#ROUTES=}" ;;
        ACCEPT_ROUTES=*) ACCEPT_ROUTES="${line#ACCEPT_ROUTES=}" ;;
    esac
done < "$REQ"

[ -n "$ROUTES" ] || fail "request has no ROUTES"

# ---------- 2. validate (defence in depth; skygate already checked) ----------
case "$ACCEPT_ROUTES" in
    -1|0|1) : ;;
    *) fail "ACCEPT_ROUTES must be -1, 0 or 1 (got '$ACCEPT_ROUTES')" ;;
esac

# Each route must be a bare CIDR. Nothing else is ever handed to the CLI, and the
# whole comma list travels as a single argv element below.
ROUTES_TRIMMED=""
IFS=',' read -r -a _routes <<< "$ROUTES"
for r in "${_routes[@]}"; do
    r="$(printf '%s' "$r" | tr -d '[:space:]')"
    [ -n "$r" ] || continue
    if ! printf '%s' "$r" | grep -Eq '^[0-9A-Fa-f:.]+/[0-9]{1,3}$'; then
        fail "refusing route '$r' — not a bare CIDR"
    fi
    # A CIDR prefix longer than 128 is nonsense; catch the obvious typo instead of
    # letting tailscale reject the whole set.
    _pfx="${r##*/}"
    if [ "$_pfx" -gt 128 ] 2>/dev/null; then
        fail "refusing route '$r' — prefix /$_pfx is out of range"
    fi
    [ -z "$ROUTES_TRIMMED" ] && ROUTES_TRIMMED="$r" || ROUTES_TRIMMED="$ROUTES_TRIMMED,$r"
done
ROUTES="$ROUTES_TRIMMED"
[ -n "$ROUTES" ] || fail "no valid route survived validation"

# ---------- 3. apply ---------------------------------------------------------
command -v "$TS_BIN" >/dev/null 2>&1 || fail "tailscale CLI '$TS_BIN' not found (set SKYGATE_TAILSCALE_CLI in $CONF)"

ARGS=(set --advertise-exit-node "--advertise-routes=$ROUTES")
case "$ACCEPT_ROUTES" in
    -1) ARGS+=(--accept-routes=false) ;;
     1) ARGS+=(--accept-routes=true) ;;
esac
COMMAND="$TS_BIN ${ARGS[*]}"

OUT="$("$TS_BIN" "${ARGS[@]}" 2>&1)"
RC=$?
if [ "$RC" -ne 0 ]; then
    fail "tailscale set failed (rc=$RC): $(printf '%s' "$OUT" | tr '\n' ' ' | cut -c1-400)"
fi

log "applied: $COMMAND"
[ -n "$OUT" ] && log "output: $(printf '%s' "$OUT" | tr '\n' ' ' | cut -c1-400)"
status_write ok ""
rm -f "$REQ" 2>/dev/null || true
exit 0
