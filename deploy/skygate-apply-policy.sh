#!/usr/bin/env bash
# /usr/local/lib/skygate/skygate-apply-policy.sh — privileged headscale policy
# applier (B272.3).
#
# WHY: with `policy.mode: file` headscale keeps its ACL in a file that skygate
# must extend (a new per-device tag needs a `tagOwners` entry before headscale
# accepts the tag at all). The skygate unit runs with
#
#     ProtectSystem=strict
#     ReadWritePaths=${data_dir} ${etc_dir}
#
# so /etc/headscale is READ-ONLY for it by design (live error: "write policy
# file /etc/headscale/policy.hujson: open …skygate.tmp: read-only file system").
#
# The fix keeps the project's privilege split (same model as
# skygate-apply-update.sh, B261): the unprivileged service drops a DATA-ONLY
# request into its own data dir; this root-owned script applies it.
#
# SECURITY MODEL
#   - Invoked by the root-owned skygate-policy.service (fired by
#     skygate-policy.path watching <update_dir>/policy.request.props).
#   - The request file is PARSED as data, never sourced and never evaluated:
#     POLICY_PATH must be absolute, must already exist, and must be named by
#     headscale's own configuration (the applier re-reads it).
#   - The write is atomic (temp file in the target directory + mv) and the file
#     keeps the original owner/group/mode.
#   - The PREVIOUS content is saved to <update_dir>/policy.prev and restored if
#     headscale does not come back healthy.
#   - Nothing else on the host is touched.
#
# CONFIG (/etc/skygate/policy-helper.conf, root-owned, optional):
#   SKYGATE_HEADSCALE_UNIT="headscale"
#   SKYGATE_HEADSCALE_CONFIG="/etc/headscale/config.yaml"

set -uo pipefail

CONF="${SKYGATE_POLICY_HELPER_CONF:-/etc/skygate/policy-helper.conf}"
# shellcheck disable=SC1090
[ -r "$CONF" ] && . "$CONF"

UPDATE_DIR="${SKYGATE_UPDATE_DIR:-/var/lib/skygate/update}"
REQ="${SKYGATE_POLICY_REQUEST_PATH:-$UPDATE_DIR/policy.request.props}"
LOG="$UPDATE_DIR/policy-apply.log"
SERVICE="${SKYGATE_HEADSCALE_UNIT:-headscale}"
HS_CONFIG="${SKYGATE_HEADSCALE_CONFIG:-/etc/headscale/config.yaml}"

log() {
    printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG" 2>/dev/null || true
    printf '%s\n' "$*"
}

fail() {
    log "ERROR: $*"
    rm -f "$REQ" 2>/dev/null || true
    exit 1
}

[ -f "$REQ" ] || exit 0   # nothing to do (the path unit can fire spuriously)

# ---------- 1. parse the request (data only) --------------------------------
POLICY_PATH=""
POLICY_BODY=""
in_body=0
while IFS= read -r line; do
    case "$line" in
        'POLICY_BEGIN') in_body=1; continue ;;
        'POLICY_END')   in_body=0; continue ;;
    esac
    if [ "$in_body" = "1" ]; then
        if [ -z "$POLICY_BODY" ]; then POLICY_BODY="$line"; else POLICY_BODY="$POLICY_BODY
$line"; fi
        continue
    fi
    case "$line" in
        POLICY_PATH=*) POLICY_PATH="$(printf '%s' "${line#POLICY_PATH=}" | tr -d '"')" ;;
    esac
done < "$REQ"

[ -n "$POLICY_PATH" ] || fail "request has no POLICY_PATH"
[ -n "$POLICY_BODY" ] || fail "request has an empty policy body"
case "$POLICY_PATH" in
    /*) : ;;
    *) fail "POLICY_PATH must be absolute (got '$POLICY_PATH')" ;;
esac

# The target must be the policy file headscale actually reads — not an arbitrary
# path an attacker could have written into the request.
if [ -r "$HS_CONFIG" ]; then
    declared="$(awk '
        /^policy:/            { inp=1; next }
        inp && /^[^[:space:]#]/ { inp=0 }
        inp && $1 == "path:"  { gsub(/["'\'']/, "", $2); print $2; exit }
    ' "$HS_CONFIG")"
    if [ -n "$declared" ] && [ "$declared" != "$POLICY_PATH" ]; then
        fail "policy path mismatch: request=$POLICY_PATH headscale=$declared"
    fi
else
    log "WARN: $HS_CONFIG is not readable; skipping the path cross-check"
fi

[ -f "$POLICY_PATH" ] || fail "target $POLICY_PATH does not exist (refusing to create it)"

# ---------- 2. write atomically, preserving owner/group/mode ----------------
owner="$(stat -c '%U:%G' "$POLICY_PATH" 2>/dev/null || echo '')"
mode="$(stat -c '%a' "$POLICY_PATH" 2>/dev/null || echo '0640')"
cp -a "$POLICY_PATH" "$UPDATE_DIR/policy.prev" 2>/dev/null || log "WARN: could not save $UPDATE_DIR/policy.prev"

tmp="$POLICY_PATH.skygate-helper.$$"
printf '%s\n' "$POLICY_BODY" > "$tmp" || fail "write $tmp failed"
[ -n "$owner" ] && chown "$owner" "$tmp" 2>/dev/null || true
chmod "$mode" "$tmp" 2>/dev/null || true
mv -f "$tmp" "$POLICY_PATH" || { rm -f "$tmp"; fail "mv $tmp -> $POLICY_PATH failed"; }
log "policy written to $POLICY_PATH (owner=$owner mode=$mode bytes=$(wc -c < "$POLICY_PATH"))"

# ---------- 3. make headscale re-read it ------------------------------------
if command -v systemctl > /dev/null 2>&1; then
    if ! systemctl restart "$SERVICE" >> "$LOG" 2>&1; then
        log "ERROR: systemctl restart $SERVICE failed — restoring the previous policy"
        [ -f "$UPDATE_DIR/policy.prev" ] && cp -a "$UPDATE_DIR/policy.prev" "$POLICY_PATH"
        systemctl restart "$SERVICE" >> "$LOG" 2>&1 || true
        fail "headscale did not restart; previous policy restored"
    fi
else
    log "WARN: no systemctl on this host — restart $SERVICE manually so it re-reads the policy"
fi

# ---------- 4. verify headscale can actually READ it now --------------------
if command -v systemctl > /dev/null 2>&1; then
    hs_user="$(systemctl show "$SERVICE" -p User --value 2>/dev/null)"
    if [ -n "$hs_user" ] && ! sudo -u "$hs_user" test -r "$POLICY_PATH"; then
        log "WARN: $hs_user still cannot read $POLICY_PATH — check owner/group/mode (see docs/troubleshooting.md 8.0.4)"
    fi
fi

rm -f "$REQ" 2>/dev/null || true
log "done"
exit 0
