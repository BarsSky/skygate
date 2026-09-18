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
#   4. extract, run `<new binary> --migrate-only` as $RUN_USER (BEFORE
#      the swap: a failed migration leaves the old binary in place and
#      needs no rollback at all)
#   5. swap the binary ATOMICALLY via a rename (writing directly over a
#      running executable fails with ETXTBSY)
#   6. restart ($MODE=systemd: systemctl restart $SERVICE;
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
build_matches() {
    _body="$1"
    _want="$2"
    case "$_body" in
        *'"status":"ok"'*) ;;
        *) return 1 ;;
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

# wait_for_build <expected tag>
wait_for_build() {
    _want="$1"
    _i=0
    while [ "$_i" -lt "$HEALTH_TIMEOUT" ]; do
        _i=$((_i + 1))
        _body="$(health_body || true)"
        if [ -n "$_body" ] && build_matches "$_body" "$_want"; then
            printf '%s\n' "$LAST_BUILD" > "$RESULT_BUILD"
            log "healthz reports build '$LAST_BUILD' after ${_i}s"
            return 0
        fi
        if [ -n "$_body" ] && [ "$_i" -eq 1 ]; then
            log "healthz is up but reports build '${LAST_BUILD:-none}', waiting for '$_want'"
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

restart_service() {
    if [ "$MODE" = "systemd" ]; then
        if ! command -v systemctl > /dev/null 2>&1; then
            log "ERROR: systemctl not found (MODE=systemd)"
            return 1
        fi
        systemctl restart "$SERVICE" >> "$LOG" 2>&1
        return $?
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
        runuser -u "$RUN_USER" -- env "$@" "$NEW_BIN" --migrate-only >> "$LOG" 2>&1
        return $?
    fi
    env "$@" "$NEW_BIN" --migrate-only >> "$LOG" 2>&1
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
    systemd|bare) ;;
    *) log "ERROR: unknown SKYGATE_UPDATE_MODE='$MODE'" ; exit 2 ;;
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
BASE_URL="https://github.com/${OWNER}/${REPO}/releases/download/${TARGET}"

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
    else
        log "WARN: SHA256SUMS exists but does not list $ASSET — trying the GitHub asset digest"
    fi
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
        { buf = buf $0 }
        END {
            i = index(buf, "\"name\": \"" asset "\"")
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
    rollback "restart failed"
fi

if wait_for_build "$TARGET"; then
    log "update OK: $FROM_VERSION -> $LAST_BUILD"
    finish done ""
fi

rollback "healthz did not report build '$TARGET' within ${HEALTH_TIMEOUT}s (last build: ${LAST_BUILD:-none})"
