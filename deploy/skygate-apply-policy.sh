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
STATUS="$UPDATE_DIR/policy-apply.status"
SERVICE="${SKYGATE_HEADSCALE_UNIT:-headscale}"
HS_CONFIG="${SKYGATE_HEADSCALE_CONFIG:-/etc/headscale/config.yaml}"

# B288.1 (2026-09-22): the OUTCOME of this run, in a file skygate can read.
#
# WHY: skygate hands the policy over asynchronously (a data-only request file
# this root-owned script picks up) and then has NO WAY to know whether the write
# happened. Live on `aro` the ACL was "applied" every five minutes — a fresh
# acl_snapshots row with status OK each time — while the file headscale served
# stayed the pre-B274 document (11341 bytes, 42 grants of which 16 were
# duplicates) for DAYS, and /admin/exit-nodes reported «политика УСТАРЕЛА» with
# no explanation anywhere. A request that was accepted is not a policy that was
# written, and the operator could not tell the difference.
#
# The status file is one line of key=value data (never sourced):
#   RESULT=ok|unchanged|failed  TS=<utc>  BYTES=<n>  PATH=<policy path>  REASON=<text>
status_write() {
    _res="$1"; _reason="${2:-}"
    _bytes=0
    [ -f "$POLICY_PATH" ] && _bytes="$(wc -c < "$POLICY_PATH" 2>/dev/null || echo 0)"
    printf 'RESULT=%s\nTS=%s\nPATH=%s\nBYTES=%s\nREASON=%s\n' \
        "$_res" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${POLICY_PATH:-}" "$_bytes" "$_reason" \
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

# 2026-09-20 (B272.4): UNION the incoming tagOwners into the file instead of
# replacing the document wholesale.
#
# WHY: skygate's reconciler calls EnsureTagOwner once per device, and each call
# is a read-modify-write of the WHOLE policy; because this applier runs
# asynchronously (a .path unit), the next call reads the policy BEFORE this
# write lands and its document therefore omits the previous tag. Live on the
# native host aro: three devices needed a tag, the policy kept exactly ONE
# (bytes=271), and the other two failed every tick with
# `400 requested tags [...] are invalid or not permitted`.
#
# B288.1 (2026-09-22) — THE UNION IS REMOVED. It was the right answer to the
# lost-update race above, but it created a worse one: a key the incoming
# document does NOT mention can never be deleted, so `tagOwners` only ever grew.
# Live on `aro`, the on-disk policy declared `tag:dev-daniil-homepc` (a tag no
# node wears any more) while every generated document omitted it — which means
# the two could never compare equal, the drift check re-applied the ACL and
# restarted headscale EVERY FIVE MINUTES (see the B283 note in step 2b), and
# /admin/exit-nodes kept showing «политика УСТАРЕЛА» with nothing to fix.
#
# The race it guarded against is gone: B272.7 made the tag path write the whole
# pending set in ONE read-modify-write, and B288 made the ACL generator declare
# every per-device tag the ownership record holds. Both writers now produce a
# COMPLETE document, so the requester's document is written verbatim and the
# applier stops being a second author of the policy. Parse validation below is
# unchanged, and so is the semantics-only no-op check in step 2b.
# ---------- 1b. refuse to write a policy that does not parse (B283) ----------
# A malformed write here is not a rejected request — it is the file headscale
# reads at STARTUP, so the daemon crash-loops and the tailnet loses its control
# plane. Live on `aro` (2026-09-22): a spliced 7869-byte document produced
# `hujson: line 302, column 23: invalid character ']' after object name`,
# restart counter 248, and every device disappeared from the portal. The
# previous file stays in force and the operator gets a named error instead.
if command -v python3 > /dev/null 2>&1; then
    if ! POLICY_CHECK="$POLICY_BODY" python3 -c 'import json,os; json.loads(os.environ["POLICY_CHECK"])' >/dev/null 2>&1; then
        fail "refusing to write a policy that does not parse (previous policy kept)"
    fi
else
    log "WARN: python3 not found — cannot validate the incoming policy before writing it"
fi

# (B288.1) The B272.4 tagOwners UNION with the on-disk document used to sit here.
# It is gone: see the note at step 1b above — the two writers now emit complete
# documents, and a union made a stale tag declaration impossible to remove, which
# is what kept the live policy "stale" forever.

# ---------- 2b. skip a no-op write (B283) -----------------------------------
# A file-mode write RESTARTS headscale, so a document that already says the same
# thing must not cost a control-plane restart. Live on `aro` the policy was
# rewritten every ~5 minutes with 7100/7109/7126/7135-byte variants of the same
# policy, restarting headscale each time — and every extra write was another
# chance to catch this script mid-request. Compare PARSED values, so
# indentation, key order and comments do not count as a change.
if command -v python3 > /dev/null 2>&1; then
    if POLICY_NEW="$POLICY_BODY" POLICY_CUR="$(cat "$POLICY_PATH" 2>/dev/null)" python3 - <<'PY' >/dev/null 2>&1
import json, os, sys
try:
    a = json.loads(os.environ.get("POLICY_NEW", ""))
    b = json.loads(os.environ.get("POLICY_CUR", ""))
except Exception:
    sys.exit(1)
sys.exit(0 if a == b else 1)
PY
    then
        rm -f "$REQ" 2>/dev/null || true
        log "policy unchanged (semantically equal) — not rewriting, $SERVICE is not restarted"
        status_write unchanged "the requested policy is semantically equal to the file on disk"
        exit 0
    fi
fi

tmp="$POLICY_PATH.skygate-helper.$$"
printf '%s\n' "$POLICY_BODY" > "$tmp" || fail "write $tmp failed"
[ -n "$owner" ] && chown "$owner" "$tmp" 2>/dev/null || true
chmod "$mode" "$tmp" 2>/dev/null || true
mv -f "$tmp" "$POLICY_PATH" || { rm -f "$tmp"; fail "mv $tmp -> $POLICY_PATH failed"; }
log "policy written to $POLICY_PATH (owner=$owner mode=$mode bytes=$(wc -c < "$POLICY_PATH"))"

# ---------- 3. make headscale re-read it ------------------------------------
# B283: `systemctl restart` returns 0 as soon as the unit is STARTED — a unit
# that then crash-loops still looks like success. Live on `aro` (2026-09-22)
# this script logged `done` while headscale had already died on the policy it
# had just written, and the operator only found out when every device vanished
# from the portal. So ask headscale itself, and roll back if it does not answer.
HEALTH_URL="${SKYGATE_HEADSCALE_HEALTH_URL:-http://127.0.0.1:8081/health}"
HEALTH_TRIES="${SKYGATE_POLICY_HEALTH_TRIES:-20}"
WARN_NOTE=""
if command -v systemctl > /dev/null 2>&1; then
    if ! systemctl restart "$SERVICE" >> "$LOG" 2>&1; then
        WARN_NOTE="systemctl restart $SERVICE returned non-zero"
        log "WARN: $WARN_NOTE"
    fi
    healthy=0
    if command -v curl > /dev/null 2>&1; then
        i=0
        while [ "$i" -lt "$HEALTH_TRIES" ]; do
            if curl -fsS -m 2 "$HEALTH_URL" >/dev/null 2>&1; then healthy=1; break; fi
            i=$((i + 1))
            sleep 1
        done
    else
        sleep 3
        [ "$(systemctl is-active "$SERVICE" 2>/dev/null)" = "active" ] && healthy=1
    fi
    if [ "$healthy" != "1" ]; then
        log "ERROR: $SERVICE did not answer on $HEALTH_URL within ${HEALTH_TRIES}s after writing $POLICY_PATH — restoring the previous policy and restarting"
        [ -f "$UPDATE_DIR/policy.prev" ] && cp -a "$UPDATE_DIR/policy.prev" "$POLICY_PATH"
        systemctl restart "$SERVICE" >> "$LOG" 2>&1 || true
        fail "headscale did not answer on $HEALTH_URL within ${HEALTH_TRIES}s after the write; the previous policy was restored"
    fi
else
    WARN_NOTE="no systemctl on this host — $SERVICE must be restarted by hand"
    log "WARN: $WARN_NOTE"
fi

# ---------- 4. verify headscale can actually READ it now --------------------
if command -v systemctl > /dev/null 2>&1; then
    hs_user="$(systemctl show "$SERVICE" -p User --value 2>/dev/null)"
    if [ -n "$hs_user" ] && ! sudo -u "$hs_user" test -r "$POLICY_PATH"; then
        [ -n "$WARN_NOTE" ] && WARN_NOTE="$WARN_NOTE; "
        WARN_NOTE="${WARN_NOTE}$hs_user cannot read $POLICY_PATH (owner/group/mode — see docs/troubleshooting.md 8.0.4)"
        log "WARN: $WARN_NOTE"
    fi
fi

rm -f "$REQ" 2>/dev/null || true
log "done"
status_write ok "${WARN_NOTE:-wrote $(wc -c < "$POLICY_PATH" 2>/dev/null || echo 0) bytes to $POLICY_PATH; $SERVICE restarted and answered on $HEALTH_URL}"
exit 0
