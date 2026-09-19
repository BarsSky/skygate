#!/bin/sh
# skygate-apply-update.sh — privileged half of the native (systemd /
# bare-binary) self-update.
#
# WHY THIS FILE EXISTS
#
# A native skygate install runs as an unprivileged service user under
# ProtectSystem=strict (see deploy/install-common.sh:write_systemd_unit),
# so the process itself cannot replace /usr/local/bin/skygate and
# cannot `systemctl restart skygate`. This script is the root-owned
# half that can. It is triggered by the root-owned path unit
# skygate-update.path the moment the unprivileged service drops
# <update_dir>/request.props, and it is deliberately the ONLY thing
# allowed to touch the binary or the unit.
#
# SECURITY MODEL (read before changing anything here)
#
#   * request.props is written by an UNPRIVILEGED process. It is
#     therefore PARSED AS DATA, never sourced. `read_prop` accepts a
#     single non-empty line per key with a strict character class and
#     rejects everything else. Do not "simplify" this into
#     `. "$REQ"` — that would hand any skygate-level compromise a root
#     shell.
#   * /etc/skygate/skygate.env is owned by the SERVICE USER (the
#     installer creates /etc/skygate as skygate:skygate 0750), so it is
#     parsed as data too and handed to `env` as KEY=VALUE arguments —
#     never sourced. Same reasoning, same escalation.
#   * Every PATH (binary, service name, run user, env file, health
#     URL, update dir, allowed owner/repo, asset arch) comes from the
#     root-owned $CONF_FILE, written by the installer — NOT from
#     request.props. A compromised service can therefore ask for
#     "install official release tag X" and nothing else: it cannot
#     point the helper at its own binary or at an attacker URL.
#   * The only values from request.props that reach a command line are
#     TARGET (validated: [A-Za-z0-9][A-Za-z0-9._+-]{0,63}, no
#     "-g<hex>" git-describe suffix), JOB_ID (hex, logging) and
#     RUNTIME_PID (digits, bare mode only, and only after checking
#     /proc/<pid>/comm is skygate).
#   * The download URL is built from $OWNER/$REPO/$TARGET only, and the
#     tarball's SHA256 is verified before anything is executed —
#     against the release's SHA256SUMS asset when it exists, otherwise
#     against GitHub's per-asset `digest` from the Releases API for the
#     SAME owner/repo/tag (both are the same trust root as the
#     download itself). Nothing is installed unverified.
#
# WHAT IT DOES (in order, aborting safely at every step)
#
#   1. parse + validate the request
#   2. back up the CURRENT binary to <update_dir>/skygate.prev
#   3. download skygate-$TARGET-$ASSET_ARCH.tar.gz, then verify its
#      SHA256 against (a) the release's SHA256SUMS asset when the
#      release publishes one, or (b) GitHub's own per-asset
#      `digest: sha256:<hex>` from the Releases API — the reference
#      repo's published releases carry NO SHA256SUMS asset at all
#      (v1.5.6 … v1.5.8), so a SHA256SUMS-only gate made the whole
#      feature unusable while still looking "secure"
#   4. extract, run `<new binary> migrate-only` as $RUN_USER (BEFORE
#      the swap: a failed migration leaves the old binary in place and
#      needs no rollback at all). The SUBCOMMAND form is the real one —
#      `--migrate-only` is not a flag (it prints
#      'unknown command "--migrate-only"'), even though the manual
#      steps used to suggest it; see internal/update/manual.go.
#   5. swap the binary ATOMICALLY via a rename (writing directly over a
#      running executable fails with ETXTBSY)
#   6. restart ($MODE=systemd: systemctl restart $SERVICE;
#      $MODE=openrc: rc-service $SERVICE restart;
#      $MODE=bare: TERM the recorded pid + start $BARE_START as $RUN_USER)
#   7. poll $HEALTH_URL until /healthz reports 200 + status:ok AND a
#      build string belonging to $TARGET — not just "something is
#      listening", which is how the pre-fix Docker path could call a
#      stale process a success
#   8. on failure AFTER the swap: restore <update_dir>/skygate.prev,
#      restart, verify the service is serving again, report rolled_back
#   9. write <update_dir>/result.status + result.build + result.error
#      and append the whole story to <update_dir>/apply.log, which
#      skygate folds into the /admin/update state on its next render
#
# ENV (all optional; the installed config file sets them)
#
#   SKYGATE_HELPER_CONF          config path (default /etc/skygate/update-helper.conf)
#   SKYGATE_UPDATE_MODE          systemd | openrc | bare (set in the conf)
#   SKYGATE_UPDATE_DRY_RUN=1     stop after the migration step, before
#                                the swap (used by the B261 check)
#   SKYGATE_UPDATE_HEALTH_TIMEOUT seconds to wait for /healthz (default 90)
#
# Exit code: 0 whenever a verdict was written (done / rolled_back /
# failed all exit 0 — the verdict lives in result.status; a non-zero
# exit would only produce "unit failed" noise), 3 when there was no
# request to act on.

set -u

CONF_FILE="${SKYGATE_HELPER_CONF:-/etc/skygate/update-helper.conf}"

# -------- config -----------------------------------------------------
#
# $CONF_FILE is root-owned and IS sourced (trusted root input).
# Defaults are the standard install layout, so the script still works
# on a host whose installer predates this file.

UPDATE_DIR_DEFAULT=/var/lib/skygate/update
MODE_DEFAULT=systemd
SERVICE_DEFAULT=skygate
BINARY_PATH_DEFAULT=/usr/local/bin/skygate
RUN_USER_DEFAULT=skygate
ENV_FILE_DEFAULT=/etc/skygate/skygate.env
HEALTH_URL_DEFAULT=http://127.0.0.1:8080/healthz
OWNER_DEFAULT=BarsSky
REPO_DEFAULT=skygate
ASSET_ARCH_DEFAULT=linux-amd64

SKYGATE_UPDATE_DIR="$UPDATE_DIR_DEFAULT"
SKYGATE_UPDATE_MODE="$MODE_DEFAULT"
SKYGATE_UPDATE_SERVICE="$SERVICE_DEFAULT"
SKYGATE_UPDATE_BINARY="$BINARY_PATH_DEFAULT"
SKYGATE_UPDATE_RUN_USER="$RUN_USER_DEFAULT"
SKYGATE_UPDATE_ENV_FILE="$ENV_FILE_DEFAULT"
SKYGATE_UPDATE_HEALTH_URL="$HEALTH_URL_DEFAULT"
SKYGATE_UPDATE_OWNER="$OWNER_DEFAULT"
SKYGATE_UPDATE_REPO="$REPO_DEFAULT"
SKYGATE_UPDATE_ASSET_ARCH="$ASSET_ARCH_DEFAULT"
SKYGATE_UPDATE_BARE_START=""

if [ -r "$CONF_FILE" ]; then
    # shellcheck disable=SC1090
    . "$CONF_FILE"
fi

UPDATE_DIR="$SKYGATE_UPDATE_DIR"
REQ="$UPDATE_DIR/request.props"
RESULT_STATUS="$UPDATE_DIR/result.status"
RESULT_BUILD="$UPDATE_DIR/result.build"
RESULT_ERROR="$UPDATE_DIR/result.error"
LOG="$UPDATE_DIR/apply.log"
PREV_BINARY="$UPDATE_DIR/skygate.prev"
MODE="$SKYGATE_UPDATE_MODE"
SERVICE="$SKYGATE_UPDATE_SERVICE"
BINARY_PATH="$SKYGATE_UPDATE_BINARY"
RUN_USER="$SKYGATE_UPDATE_RUN_USER"
ENV_FILE="$SKYGATE_UPDATE_ENV_FILE"
HEALTH_URL="$SKYGATE_UPDATE_HEALTH_URL"
OWNER="$SKYGATE_UPDATE_OWNER"
REPO="$SKYGATE_UPDATE_REPO"
ASSET_ARCH="$SKYGATE_UPDATE_ASSET_ARCH"
BARE_START="$SKYGATE_UPDATE_BARE_START"
DRY_RUN="${SKYGATE_UPDATE_DRY_RUN:-0}"
HEALTH_TIMEOUT="${SKYGATE_UPDATE_HEALTH_TIMEOUT:-90}"
HEALTH_POLL_INTERVAL="${SKYGATE_UPDATE_HEALTH_POLL:-1}"

# -------- helpers ----------------------------------------------------

log() {
    printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG" 2>/dev/null || true
    printf '%s\n' "$*"
}

# finish <status> [error message] — the single exit point. Always
# writes the verdict the Go side reads, then removes the request so the
# path unit re-arms.
finish() {
    _status="$1"
    _err="${2:-}"
    if [ -n "$_err" ]; then
        printf '%s\n' "$_err" > "$RESULT_ERROR" 2>/dev/null || true
    else
        rm -f "$RESULT_ERROR" 2>/dev/null || true
    fi
    printf '%s\n' "$_status" > "$RESULT_STATUS" 2>/dev/null || true
    # skygate (the service user) reads these and deletes them after
    # folding the verdict into the update state, so hand them over.
    # apply.log and skygate.prev stay root-owned on purpose: the log is
    # read-only for the service user, and the rollback source must not
    # be writable by the process being rolled back.
    for _f in "$RESULT_STATUS" "$RESULT_BUILD" "$RESULT_ERROR"; do
        [ -e "$_f" ] && chown "$RUN_USER:$RUN_USER" "$_f" 2>/dev/null
    done
    chmod 0640 "$RESULT_STATUS" 2>/dev/null || true
    chmod 0644 "$LOG" 2>/dev/null || true
    rm -f "$REQ"
    log "verdict: $_status${_err:+ ($_err)}"
    exit 0
}

# read_prop <key> — first matching line's value, unquoted. DATA ONLY.
read_prop() {
    _key="$1"
    _raw="$(sed -n "s/^${_key}=//p" "$REQ" 2>/dev/null | head -n 1)"
    printf '%s' "$(printf '%s' "$_raw" | tr -d "'\"")"
}

# valid_target mirrors native.go's nativeTagPattern + the
# git-describe rejection: the value ends up in a URL and inside a
# `case`, so the character class is the boundary.
valid_target() {
    case "$1" in
        '') return 1 ;;
        [A-Za-z0-9]*) ;;
        *) return 1 ;;
    esac
    case "$1" in
        *[!A-Za-z0-9._+-]*) return 1 ;;
    esac
    case "$1" in
        *-g[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) return 1 ;;
    esac
    [ ${#1} -le 64 ] || return 1
    return 0
}

valid_hex_id() {
    case "$1" in
        ''|*[!0-9a-f]*) return 1 ;;
    esac
    [ ${#1} -le 32 ] || return 1
    return 0
}

valid_pid() {
    case "$1" in
        ''|*[!0-9]*) return 1 ;;
    esac
    return 0
}

# is_our_process <pid> — guards the bare-mode kill against a recycled
# pid: only TERM a process whose comm is the skygate binary.
is_our_process() {
    [ -r "/proc/$1/comm" ] || return 1
    case "$(cat "/proc/$1/comm" 2>/dev/null)" in
        skygate*) return 0 ;;
    esac
    return 1
}

health_body() {
    if command -v curl > /dev/null 2>&1; then
        curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null
    elif command -v wget > /dev/null 2>&1; then
        wget -q -O - --timeout=5 "$HEALTH_URL" 2>/dev/null
    else
        return 1
    fi
}

# build_matches <body> <expected tag-or-prefix>
# Requires a healthy body AND a build string that belongs to the
# expected release (exact tag, or "<tag>+<commit>" as the release
# pipeline's ldflags produce). A bare HTTP 200 is NOT enough.
#
# B269: since the socket is now bound at the very top of main() (so a
# port conflict fails immediately and the process always has an HTTP
# face), /healthz starts answering with the NEW build string while the
# DB and the services are still being constructed. The body therefore
# carries an explicit boot marker — `"ready":false` — until the real mux
# takes over. A body carrying it is a live process that has NOT finished
# starting: useful to log, never enough to call a swap successful.
build_matches() {
    _body="$1"
    _want="$2"
    case "$_body" in
        *'"status":"ok"'*) ;;
        *) return 1 ;;
    esac
    case "$_body" in
        *'"ready":false'*) return 1 ;;
    esac
    _build="$(printf '%s' "$_body" | sed -n 's/.*"build":"\([^"]*\)".*/\1/p')"
    [ -n "$_build" ] || return 1
    LAST_BUILD="$_build"
    [ -n "$_want" ] || return 0
    case "$_build" in
        "$_want"|"$_want"+*) return 0 ;;
    esac
    return 1
}

# boot_phase <body> — the startup phase reported by the provisional
# /healthz, or "" once the real handler owns the port. Logged so a
# stalled boot names its blocker in the applier's own log.
boot_phase() {
    printf '%s' "$1" | sed -n 's/.*"phase":"\([^"]*\)".*/\1/p'
}

# wait_for_build <expected tag>
wait_for_build() {
    _want="$1"
    _i=0
    _started=0
    while [ "$_i" -lt "$HEALTH_TIMEOUT" ]; do
        _i=$((_i + 1))
        _body="$(health_body || true)"
        if [ -n "$_body" ] && build_matches "$_body" "$_want"; then
            printf '%s\n' "$LAST_BUILD" > "$RESULT_BUILD"
            log "healthz reports build '$LAST_BUILD' after ${_i}s"
            return 0
        fi
        # B269: a body with "ready":false means the new binary holds the
        # port and is still building its services — say so (naming the
        # phase) instead of the misleading "reports build ... waiting".
        if [ -n "$_body" ]; then
            _phase="$(boot_phase "$_body")"
            if [ -n "$_phase" ]; then
                if [ "$_started" -eq 0 ]; then
                    log "healthz is answering (build '${LAST_BUILD:-none}') but the service is still starting: phase='$_phase' (deploy/skygate-apply-update.sh waits for 'ready' to become true)"
                    _started=1
                elif [ $((_i % 10)) -eq 0 ]; then
                    log "still starting after ${_i}s: phase='$_phase'"
                fi
            elif [ "$_i" -eq 1 ]; then
                log "healthz is up but reports build '${LAST_BUILD:-none}', waiting for '$_want'"
            fi
        fi
        sleep "$HEALTH_POLL_INTERVAL"
    done
    return 1
}

# wait_for_any_health — used after a rollback: the service must be
# serving again. The build string is logged, not required to match
# (the previous binary may report a describe label).
wait_for_any_health() {
    _i=0
    while [ "$_i" -lt "$HEALTH_TIMEOUT" ]; do
        _i=$((_i + 1))
        _body="$(health_body || true)"
        if [ -n "$_body" ] && build_matches "$_body" ""; then
            return 0
        fi
        sleep "$HEALTH_POLL_INTERVAL"
    done
    return 1
}

# health_port — the TCP port behind $HEALTH_URL, for the diagnostics
# below. Pure string work so it needs no tools.
health_port() {
    _hp="${HEALTH_URL#*://}"
    _hp="${_hp%%/*}"
    case "$_hp" in
        *:*) printf '%s' "${_hp##*:}" ;;
        *)   printf '80' ;;
    esac
}

# service_state — one line describing what systemd/OpenRC thinks of the
# unit, or "unknown". Never fails.
service_state() {
    if [ "$MODE" = "systemd" ] && command -v systemctl > /dev/null 2>&1; then
        systemctl is-active "$SERVICE" 2>/dev/null || true
        return 0
    fi
    if [ "$MODE" = "openrc" ] && command -v rc-service > /dev/null 2>&1; then
        rc-service "$SERVICE" status 2>/dev/null | head -1 || true
        return 0
    fi
    if [ "$MODE" = "bare" ] && valid_pid "${RUNTIME_PID:-}" && is_our_process "$RUNTIME_PID"; then
        printf 'pid %s alive\n' "$RUNTIME_PID"
        return 0
    fi
    printf 'unknown'
}

# port_owner — who is listening on the health port right now.
port_owner() {
    _port="$(health_port)"
    if command -v ss > /dev/null 2>&1; then
        ss -ltnp 2>/dev/null | grep -F ":${_port} " | head -3
        return 0
    fi
    if command -v netstat > /dev/null 2>&1; then
        netstat -ltnp 2>/dev/null | grep -F ":${_port} " | head -3
        return 0
    fi
    printf 'ss/netstat unavailable'
}

# service_diagnostics — the reason the pre-B268 applier was useless when
# a swap went wrong: on failure it only said "healthz did not report
# build X", which is equally true for "the unit is masked", "the new
# binary panics on startup", "something else already owns the port" and
# "the service is up but on another port". Everything below is
# best-effort and must never abort the applier.
#
# B268 (2026-09-19) — added after a real native/systemd failure where the
# operator could see nothing but the timeout.
service_diagnostics() {
    log "DIAG: unit state: $(service_state)"
    _owner="$(port_owner)"
    log "DIAG: listener on port $(health_port): ${_owner:-nothing is listening}"
    log "DIAG: binary on disk: $(ls -l "$BINARY_PATH" 2>&1 | head -1)"
    if [ "$MODE" = "systemd" ] && command -v journalctl > /dev/null 2>&1; then
        _j="$(journalctl -u "$SERVICE" -n 15 --no-pager 2>/dev/null | tail -15)"
        if [ -n "$_j" ]; then
            log "DIAG: last journal lines for $SERVICE:"
            printf '%s\n' "$_j" | while IFS= read -r _l; do log "DIAG:   $_l"; done
        else
            log "DIAG: journalctl returned nothing for $SERVICE (unit may never have started)"
        fi
    fi
    if [ -r "$LOG" ]; then
        log "DIAG: service log tail ($LOG):"
        tail -10 "$LOG" 2>/dev/null | while IFS= read -r _l; do log "DIAG:   $_l"; done
    fi
}

# binary_smoke_test <path> — run the freshly extracted binary in a mode
# that cannot touch the network, the DB or the port, and require it to
# exit 0. This catches the "release asset is not runnable on this host"
# class (wrong arch, missing loader, truncated download) BEFORE the swap,
# so the running service is never replaced by a binary that cannot start.
binary_smoke_test() {
    _bin="$1"
    if command -v runuser > /dev/null 2>&1; then
        _out="$(runuser -u "$RUN_USER" -- timeout 20 "$_bin" --version 2>&1)"
    else
        _out="$(timeout 20 "$_bin" --version 2>&1)"
    fi
    _rc=$?
    if [ "$_rc" -eq 0 ]; then
        log "smoke test OK: the new binary runs and reports '$(printf '%s' "$_out" | head -1)'"
        return 0
    fi
    log "ERROR: the new binary failed the pre-swap smoke test (rc=$_rc): $(printf '%s' "$_out" | head -3 | tr '\n' ' ')"
    return 1
}

restart_service() {
    if [ "$MODE" = "systemd" ]; then
        if ! command -v systemctl > /dev/null 2>&1; then
            log "ERROR: systemctl not found (MODE=systemd)"
            return 1
        fi
        systemctl restart "$SERVICE" >> "$LOG" 2>&1
        return $?
    fi
    # OpenRC (Alpine et al). B262: the kind was missing entirely, so an
    # Alpine install could not be updated at all; the service is managed by
    # rc-service and its env comes from /etc/conf.d/skygate, which sources
    # /etc/skygate/skygate.env. `restart` on a stopped service can return
    # non-zero, so fall back to `start` before declaring failure.
    if [ "$MODE" = "openrc" ]; then
        if ! command -v rc-service > /dev/null 2>&1; then
            log "ERROR: rc-service not found (MODE=openrc)"
            return 1
        fi
        if ! rc-service "$SERVICE" restart >> "$LOG" 2>&1; then
            log "rc-service $SERVICE restart returned non-zero; trying start"
            rc-service "$SERVICE" start >> "$LOG" 2>&1
            return $?
        fi
        return 0
    fi
    # bare: TERM the recorded pid, then start the baked command as the
    # service user (never as root — the whole point of RUN_USER).
    if valid_pid "${RUNTIME_PID:-}" && is_our_process "$RUNTIME_PID"; then
        kill -TERM "$RUNTIME_PID" 2>/dev/null || true
        _i=0
        while [ "$_i" -lt 30 ] && kill -0 "$RUNTIME_PID" 2>/dev/null; do
            _i=$((_i + 1))
            sleep 1
        done
        kill -KILL "$RUNTIME_PID" 2>/dev/null || true
    fi
    if [ -z "$BARE_START" ]; then
        log "ERROR: bare mode needs SKYGATE_UPDATE_BARE_START in $CONF_FILE"
        return 1
    fi
    if command -v runuser > /dev/null 2>&1; then
        setsid runuser -u "$RUN_USER" -- sh -c "$BARE_START" >> "$LOG" 2>&1 &
    else
        setsid sh -c "$BARE_START" >> "$LOG" 2>&1 &
    fi
    return 0
}

# run_migrations — parse $ENV_FILE as DATA and pass it to `env` as
# KEY=VALUE arguments. No sourcing: the file is writable by the service
# user, so sourcing it as root would be a local root escalation.
run_migrations() {
    if [ ! -r "$ENV_FILE" ]; then
        log "WARN: env file $ENV_FILE not readable — running migrations with the inherited environment"
    fi
    set --
    if [ -r "$ENV_FILE" ]; then
        while IFS= read -r _line || [ -n "$_line" ]; do
            case "$_line" in
                ''|\#*) continue ;;
            esac
            _key="${_line%%=*}"
            [ "$_key" = "$_line" ] && continue
            case "$_key" in
                ''|[!A-Za-z_]*) continue ;;
                *[!A-Za-z0-9_]*) continue ;;
            esac
            _val="${_line#*=}"
            case "$_val" in
                \"*\") _val="${_val#\"}"; _val="${_val%\"}" ;;
                \'*\') _val="${_val#\'}"; _val="${_val%\'}" ;;
            esac
            set -- "$@" "$_key=$_val"
        done < "$ENV_FILE"
    fi
    if command -v runuser > /dev/null 2>&1; then
        runuser -u "$RUN_USER" -- env "$@" "$NEW_BIN" migrate-only >> "$LOG" 2>&1
        return $?
    fi
    env "$@" "$NEW_BIN" migrate-only >> "$LOG" 2>&1
    return $?
}

# atomic_install <src> — rename-based swap. `install` directly onto a
# running executable fails with ETXTBSY, so the file lands next to the
# target and is renamed over it (the running process keeps the old
# inode until it exits).
atomic_install() {
    _src="$1"
    _tmp="${BINARY_PATH}.new"
    rm -f "$_tmp" 2>/dev/null
    if ! install -m 0755 -o root -g root "$_src" "$_tmp"; then
        return 1
    fi
    if ! mv -f "$_tmp" "$BINARY_PATH"; then
        rm -f "$_tmp" 2>/dev/null
        return 1
    fi
    return 0
}

# rollback <reason> — defined before the main flow so it is always
# callable (POSIX sh defines functions as it reads them).
rollback() {
    _reason="$1"
    log "ROLLBACK: restoring the previous binary from $PREV_BINARY"
    if ! atomic_install "$PREV_BINARY"; then
        log "ERROR: rollback install failed — MANUAL INTERVENTION REQUIRED"
        finish failed "rollback could not restore the binary: $_reason"
    fi
    if ! restart_service; then
        log "ERROR: rollback restart failed — MANUAL INTERVENTION REQUIRED"
        finish failed "rollback restored the binary but could not restart: $_reason"
    fi
    if wait_for_any_health; then
        log "ROLLBACK OK: serving again (build '${LAST_BUILD:-unknown}')"
        finish rolled_back "$_reason"
    fi
    log "ERROR: service did not come back after the rollback — MANUAL INTERVENTION REQUIRED"
    finish failed "rollback restored the binary but the service is not healthy: $_reason"
}

# -------- main -------------------------------------------------------

mkdir -p "$UPDATE_DIR" 2>/dev/null || true
touch "$LOG" 2>/dev/null || true

if [ ! -r "$REQ" ]; then
    log "no update request at $REQ — nothing to do"
    exit 3
fi

case "$MODE" in
    systemd|openrc|bare) ;;
    *) log "ERROR: unknown SKYGATE_UPDATE_MODE='$MODE' (systemd|openrc|bare)" ; exit 2 ;;
esac

TARGET="$(read_prop TARGET)"
FROM_VERSION="$(read_prop FROM_VERSION)"
JOB_ID="$(read_prop JOB_ID)"
RUNTIME_PID="$(read_prop RUNTIME_PID)"
REQUESTED_AT="$(read_prop REQUESTED_AT)"

log "=== job ${JOB_ID:-?} target=$TARGET from=$FROM_VERSION mode=$MODE dry_run=$DRY_RUN requested_at=${REQUESTED_AT:-?} ==="

if ! valid_target "$TARGET"; then
    log "ERROR: refusing request — TARGET is not a valid release tag"
    finish failed "invalid target in request.props"
fi
if [ -n "$JOB_ID" ] && ! valid_hex_id "$JOB_ID"; then
    log "ERROR: refusing request — JOB_ID is not hex"
    finish failed "invalid job id in request.props"
fi

WORK="$(mktemp -d "${UPDATE_DIR}/apply.XXXXXX")" || finish failed "mktemp failed in $UPDATE_DIR"
# mktemp creates 0700 root-owned. The migration step runs the extracted
# binary as $RUN_USER (so a failed migration cannot touch root-only
# state), and a 0700 parent makes that impossible:
#   env: '.../apply.XXXXXX/skygate': Permission denied
# The contents are already SHA256-verified, so opening the directory up
# is safe — and it lives inside the service user's own update dir.
chmod 0755 "$WORK"
# shellcheck disable=SC2064
trap "rm -rf '$WORK'" EXIT

ASSET="skygate-${TARGET}-${ASSET_ARCH}.tar.gz"

# Artifact source. Default: the official GitHub release download URL for
# $OWNER/$REPO/$TARGET. An operator running an internal mirror (or an
# air-gapped network) can point SKYGATE_UPDATE_BASE_URL at
# <base>/<TAG>/<asset> + <base>/<TAG>/SHA256SUMS — root-owned config, so
# a compromised skygate still cannot choose where code comes from. A
# custom base is accepted over HTTPS, or over plain HTTP for loopback
# only (a local mirror on the same host); and it MUST provide
# SHA256SUMS, because GitHub's per-asset digest only describes the
# official asset.
MIRROR_MODE=0
if [ -n "${SKYGATE_UPDATE_BASE_URL:-}" ]; then
    case "$SKYGATE_UPDATE_BASE_URL" in
        https://*) ;;
        http://127.0.0.1*|http://localhost*|http://\[::1\]*) ;;
        *)
            log "ERROR: SKYGATE_UPDATE_BASE_URL must be https:// (plain http:// is allowed for loopback mirrors only)"
            exit 2
            ;;
    esac
    MIRROR_MODE=1
    BASE_URL="${SKYGATE_UPDATE_BASE_URL%/}/${TARGET}"
    log "artifact source: mirror ${BASE_URL} (SKYGATE_UPDATE_BASE_URL)"
else
    BASE_URL="https://github.com/${OWNER}/${REPO}/releases/download/${TARGET}"
fi

# --- 1. back up the running binary -----------------------------------
if [ ! -f "$BINARY_PATH" ]; then
    log "ERROR: $BINARY_PATH does not exist — is this a native install?"
    finish failed "binary $BINARY_PATH not found"
fi
if ! cp -p "$BINARY_PATH" "$PREV_BINARY"; then
    log "ERROR: cannot back up $BINARY_PATH"
    finish failed "backup of the current binary failed"
fi
log "backed up the current binary -> $PREV_BINARY"

# --- 2. download + verify -------------------------------------------
log "downloading $BASE_URL/$ASSET"
if ! curl -fsSL --retry 3 --retry-delay 2 --max-time 300 -o "$WORK/$ASSET" "$BASE_URL/$ASSET"; then
    log "ERROR: download failed ($BASE_URL/$ASSET)"
    finish failed "download of $ASSET failed"
fi
ACTUAL="$(sha256sum "$WORK/$ASSET" | awk '{print $1}')"
VERIFIED_BY=""

# (a) SHA256SUMS asset — the release pipeline's checksum file.
#     NOTE: the reference repo's published releases (v1.5.6 … v1.5.8)
#     do NOT have this asset, so this branch is the fast path, not the
#     only path. The pre-fix helper required it and therefore could not
#     update anything while still reporting a "secure" failure.
if curl -fsSL --retry 2 --retry-delay 2 --max-time 60 -o "$WORK/SHA256SUMS" "$BASE_URL/SHA256SUMS" 2>/dev/null; then
    EXPECTED="$(awk -v n="$ASSET" '$2 == n { print $1; exit }' "$WORK/SHA256SUMS")"
    if [ -n "$EXPECTED" ]; then
        if [ "$EXPECTED" != "$ACTUAL" ]; then
            log "ERROR: SHA256 mismatch vs SHA256SUMS (expected $EXPECTED, got $ACTUAL)"
            finish failed "SHA256 mismatch (SHA256SUMS)"
        fi
        VERIFIED_BY="SHA256SUMS asset"
    elif [ "$MIRROR_MODE" = "1" ]; then
        log "ERROR: the mirror's SHA256SUMS does not list $ASSET"
        finish failed "mirror SHA256SUMS does not list $ASSET"
    else
        log "WARN: SHA256SUMS exists but does not list $ASSET — trying the GitHub asset digest"
    fi
elif [ "$MIRROR_MODE" = "1" ]; then
    log "ERROR: SKYGATE_UPDATE_BASE_URL is set, so the mirror MUST publish SHA256SUMS — there is no GitHub digest to fall back to for a custom artifact source"
    finish failed "mirror has no SHA256SUMS (cannot verify $ASSET)"
else
    log "SHA256SUMS asset is not published for $TARGET (404) — verifying against the GitHub asset digest instead"
fi

# (b) GitHub Releases API per-asset digest. `digest` is computed by
#     GitHub from the uploaded bytes ("sha256:<hex>"), for the SAME
#     owner/repo/tag we downloaded from — the same trust root as the
#     tarball. Extracted with awk (no jq dependency): find the asset's
#     "name" field, then the first sha256: that follows it.
if [ -z "$VERIFIED_BY" ]; then
    API_URL="https://api.github.com/repos/${OWNER}/${REPO}/releases/tags/${TARGET}"
    if [ -n "${SKYGATE_UPDATE_GITHUB_TOKEN:-}" ]; then
        curl -fsSL --retry 2 --retry-delay 2 --max-time 60 \
            -H "Accept: application/vnd.github+json" \
            -H "Authorization: Bearer ${SKYGATE_UPDATE_GITHUB_TOKEN}" \
            -o "$WORK/release.json" "$API_URL" 2>/dev/null || true
    else
        curl -fsSL --retry 2 --retry-delay 2 --max-time 60 \
            -H "Accept: application/vnd.github+json" \
            -o "$WORK/release.json" "$API_URL" 2>/dev/null || true
    fi
    DIGEST="$(awk -v asset="$ASSET" '
        # GitHub pretty-prints as `"name": "x"` (space after the colon), but the
        # spacing is NOT contractual — normalise `": "` to `":"` so a compact or
        # reformatted API response cannot silently turn into "no digest".
        BEGIN { want = "\"name\":\"" asset "\"" }
        {
            line = $0
            gsub(/: /, ":", line)
            buf = buf line
        }
        END {
            i = index(buf, want)
            if (i == 0) exit 1
            rest = substr(buf, i)
            if (match(rest, /sha256:[0-9a-f]+/)) {
                print substr(rest, RSTART + 7, RLENGTH - 7)
                exit 0
            }
            exit 1
        }' "$WORK/release.json" 2>/dev/null || true)"
    if [ -z "$DIGEST" ]; then
        log "ERROR: no trustworthy checksum for $ASSET — neither a SHA256SUMS asset nor a GitHub asset digest is available"
        finish failed "could not verify $ASSET (no SHA256SUMS asset and no GitHub asset digest)"
    fi
    if [ "$DIGEST" != "$ACTUAL" ]; then
        log "ERROR: SHA256 mismatch vs GitHub asset digest (expected $DIGEST, got $ACTUAL)"
        finish failed "SHA256 mismatch (GitHub asset digest)"
    fi
    VERIFIED_BY="GitHub asset digest"
fi
log "SHA256 OK ($ACTUAL, verified against $VERIFIED_BY)"

if ! tar -xzf "$WORK/$ASSET" -C "$WORK"; then
    log "ERROR: cannot extract $ASSET"
    finish failed "tar extraction failed"
fi
if [ ! -f "$WORK/skygate" ]; then
    log "ERROR: the tarball does not contain a 'skygate' binary"
    finish failed "tarball layout unexpected"
fi
NEW_BIN="$WORK/skygate"
chmod 0755 "$NEW_BIN"

# --- 2b. pre-swap smoke test (B268) ---------------------------------
# Everything up to here proves the ARTIFACT is authentic; this proves it
# is RUNNABLE on this host. The pre-B268 applier went straight to the
# swap and then reported "healthz did not report build X" — a message
# that cannot be told apart from "the new binary cannot execute here".
if [ "$DRY_RUN" != "1" ]; then
    if ! binary_smoke_test "$NEW_BIN"; then
        log "ERROR: refusing to swap in a binary that does not run; the old binary stays in place"
        finish failed "the downloaded binary failed the pre-swap smoke test (see apply.log)"
    fi
fi

# --- 2c. pre-swap health baseline (B268) ----------------------------
# The verify step after the restart compares the build reported by
# $HEALTH_URL with $TARGET. If something OTHER than this unit already
# answers on that URL, the comparison can never succeed and the operator
# only learns it after a pointless swap + rollback. We cannot tell
# reliably who is serving (a docker-proxied container and a native unit
# look alike from the outside), but we CAN log the baseline: a healthy
# body whose build is unrelated to $FROM_VERSION is a strong hint that a
# second instance owns the port.
_pre_body="$(health_body || true)"
if [ -n "$_pre_body" ]; then
    if build_matches "$_pre_body" ""; then
        log "pre-swap health baseline: $HEALTH_URL reports build '${LAST_BUILD:-unknown}' (unit state: $(service_state))"
        if [ -n "$FROM_VERSION" ] && [ "${LAST_BUILD:-}" != "$FROM_VERSION" ]; then
            log "WARN: the health endpoint reports '${LAST_BUILD:-}' but this install believes it runs '$FROM_VERSION' — a SECOND skygate instance (container/other unit) may own that port, in which case value verification after the restart cannot succeed"
            log "WARN: listener on port $(health_port): $(port_owner | head -2 | tr '\n' ' ')"
        fi
    else
        # B269: answering with "ready":false is not a healthy baseline —
        # it is an instance that is itself mid-boot. Say so explicitly:
        # this is the exact signature of the busy-port class B268 hunts.
        log "WARN: pre-swap health baseline: $HEALTH_URL is answering but not ready (build '${LAST_BUILD:-unknown}', phase='$(boot_phase "$_pre_body")') — this instance is still starting up; the post-restart verification may race it"
    fi
else
    log "pre-swap health baseline: $HEALTH_URL is not answering while unit '$SERVICE' is '$(service_state)' — the update will fail its verification unless the restart brings this URL up"
fi

# --- 3. migrations (before the swap) --------------------------------
log "running migrations with the new binary (as $RUN_USER)"
if ! run_migrations; then
    log "ERROR: --migrate-only failed; the old binary stays in place"
    finish failed "migrations failed (the old binary is still installed)"
fi
log "migrations OK"

if [ "$DRY_RUN" = "1" ]; then
    log "DRY RUN: stopping before the binary swap"
    printf 'dry-run\n' > "$RESULT_BUILD"
    finish done ""
fi

# --- 4. swap ---------------------------------------------------------
if ! atomic_install "$NEW_BIN"; then
    log "ERROR: cannot install $BINARY_PATH"
    finish failed "binary install failed"
fi
log "installed the new binary over $BINARY_PATH"

# --- 5. restart + verify --------------------------------------------
log "restarting ($MODE)"
if ! restart_service; then
    service_diagnostics
    rollback "restart failed"
fi

if wait_for_build "$TARGET"; then
    log "update OK: $FROM_VERSION -> $LAST_BUILD"
    finish done ""
fi

# B268: say WHICH failure this is before rolling back. "did not report
# build X" covers four very different situations, and the operator could
# not tell them apart from the verdict alone. Both branches dump the same
# diagnostics — "the service is up but serving the old build" is just as
# often a port/second-instance problem as a bad artifact.
if [ -z "${LAST_BUILD:-}" ]; then
    service_diagnostics
    rollback "the service did not come up: $HEALTH_URL never returned a healthy body within ${HEALTH_TIMEOUT}s (unit state: $(service_state))"
fi
service_diagnostics
rollback "healthz did not report build '$TARGET' within ${HEALTH_TIMEOUT}s (last build: ${LAST_BUILD:-none}, unit state: $(service_state))"
