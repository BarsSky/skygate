#!/usr/bin/env bash
# ============================================================================
# skygate-apply-oidc.sh — B304 (v1.5.69)
# ============================================================================
# Configure headscale's OIDC block from what skygate already knows, restart
# headscale, and verify. One command, no hand-editing, no secret in this file:
# the values (including the client_secret) are read from skygate itself with
#
#     skygate oidc-export --headscale --secret
#
# which reads the SAME configuration the admin panel runs with (the saved
# oidc_settings row wins over the env vars). /admin/oidc is the source of truth;
# this script carries it to headscale.
#
# Why a script and not a button: headscale's config lives outside the skygate
# service's mount namespace / privilege boundary (the same split as the policy
# applier), so the write and the restart belong to root on the host.
#
# USAGE
#   sudo bash skygate-apply-oidc.sh                 # auto: find config, apply, restart, verify
#   sudo bash skygate-apply-oidc.sh --dry-run       # print the diff, touch nothing
#   sudo bash skygate-apply-oidc.sh --no-restart    # write the config only
#   sudo bash skygate-apply-oidc.sh --headscale-config /etc/headscale/config.yaml
#   sudo bash skygate-apply-oidc.sh --container headscale   # docker install
#   sudo bash skygate-apply-oidc.sh --rollback      # restore the newest backup
#
# SAFETY
#   * the config is backed up before any write (CONFIG.pre-oidc-b304.<ts>)
#   * the block lives between managed markers, so re-running is idempotent
#   * an EXISTING unmanaged oidc: key stops the script (it prints the block
#     instead of clobbering a configuration it did not write)
#   * a failed restart restores the backup
#   * HuJSON (.hujson) is never edited by string surgery
# ============================================================================
set -euo pipefail

MARK_BEGIN="# >>> skygate oidc (B304) — managed block, do not edit by hand"
MARK_END="# <<< skygate oidc (B304) — end managed block"

SKYGATE_BIN="${SKYGATE_BIN:-}"
SKYGATE_USER="${SKYGATE_USER:-skygate}"
CONFIG="${SKYGATE_HEADSCALE_CONFIG:-}"
CONTAINER="${SKYGATE_HEADSCALE_CONTAINER:-headscale}"
MODE="${SKYGATE_HEADSCALE_MODE:-auto}"   # auto | systemd | docker | none
DRY_RUN=0
DO_RESTART=1
ROLLBACK=0

log()  { printf '[oidc-apply] %s\n' "$*"; }
warn() { printf '[oidc-apply] WARN: %s\n' "$*" >&2; }
die()  { printf '[oidc-apply] ERROR: %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; }

# restart_headscale restarts the daemon according to --mode / auto-detection.
# Returns non-zero when nothing could be restarted, so the caller can roll back.
restart_headscale() {
  case "$MODE" in
    systemd)
      systemctl restart headscale ;;
    docker)
      docker restart "$CONTAINER" ;;
    none)
      log "restart skipped (--mode none)"; return 0 ;;
    *)
      if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -q '^headscale\.service'; then
        systemctl restart headscale
      elif command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER"; then
        docker restart "$CONTAINER"
      else
        warn "no headscale systemd unit or container '$CONTAINER' found — restart it yourself"
        return 1
      fi
      ;;
  esac
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)                DRY_RUN=1 ;;
    --no-restart)             DO_RESTART=0 ;;
    --rollback)               ROLLBACK=1 ;;
    --yes|-y)                 : ;;   # accepted for scripted use; nothing prompts
    --headscale-config)       shift; CONFIG="${1:-}" ;;
    --headscale-config=*)     CONFIG="${1#*=}" ;;
    --container)              shift; CONTAINER="${1:-}" ;;
    --container=*)            CONTAINER="${1#*=}" ;;
    --mode)                   shift; MODE="${1:-}" ;;
    --mode=*)                 MODE="${1#*=}" ;;
    --skygate-bin)            shift; SKYGATE_BIN="${1:-}" ;;
    --skygate-bin=*)          SKYGATE_BIN="${1#*=}" ;;
    -h|--help)                usage; exit 0 ;;
    *)                        die "unknown option: $1 (try --help)" ;;
  esac
  shift
done

# ------------------------------------------------------------------ root check
# SKYGATE_OIDC_APPLY_ALLOW_NONROOT=1 exists for the contract test: it drives the
# real script against a temporary config with a stub skygate binary. It is not a
# supported production mode (the write and the restart need root anyway).
if [ "$(id -u)" -ne 0 ] && [ "${SKYGATE_OIDC_APPLY_ALLOW_NONROOT:-0}" != "1" ]; then
  die "run me as root (sudo bash $0) — headscale's config and its restart both need root"
fi

# ------------------------------------------------------ locate the skygate bin
if [ -z "$SKYGATE_BIN" ]; then
  for cand in /usr/local/bin/skygate /usr/bin/skygate /opt/skygate/skygate; do
    if [ -x "$cand" ]; then SKYGATE_BIN="$cand"; break; fi
  done
fi
if [ -z "$SKYGATE_BIN" ]; then
  SKYGATE_BIN="$(command -v skygate || true)"
fi
[ -n "$SKYGATE_BIN" ] || die "skygate binary not found — pass --skygate-bin /path/to/skygate"
log "skygate binary: $SKYGATE_BIN"

# ------------------------------------------------------------- read the config
# oidc-export opens skygate's database, which belongs to the service user; run it
# as that account when it exists (the command is read-only).
export_block() {
  if [ "$(id -u)" -eq 0 ] && id "$SKYGATE_USER" >/dev/null 2>&1; then
    sudo -u "$SKYGATE_USER" -- "$SKYGATE_BIN" oidc-export "$@"
  else
    "$SKYGATE_BIN" oidc-export "$@"
  fi
}

BLOCK="$(export_block --headscale --secret)" || die "skygate oidc-export failed — is OIDC configured on /admin/oidc?"
printf '%s\n' "$BLOCK" | grep -q 'client_secret: .\+' || die "skygate has no client_secret configured — fill /admin/oidc first"
ISSUER="$(printf '%s\n' "$BLOCK" | awk '/^  issuer:/{print $2; exit}')"
[ -n "$ISSUER" ] || die "the generated block has no issuer — fill /admin/oidc first"
log "issuer: $ISSUER"

# --------------------------------------------------------- locate the config
if [ -z "$CONFIG" ]; then
  for cand in /etc/headscale/config.yaml /etc/headscale/config.yml \
              /var/lib/headscale/config.yaml /home/skyadmin/headscale/config/config.yaml; do
    if [ -f "$cand" ]; then CONFIG="$cand"; break; fi
  done
fi
if [ -z "$CONFIG" ] && [ -f /etc/headscale/config.hujson ]; then
  CONFIG=/etc/headscale/config.hujson
fi
[ -n "$CONFIG" ] && [ -f "$CONFIG" ] || die "headscale config not found — pass --headscale-config /path/to/config.yaml"
log "headscale config: $CONFIG"

# --------------------------------------------------------------- rollback mode
# Undo the OIDC change. The newest backup can already CONTAIN the managed block
# (every apply backs up the file it is about to change), so restoring it blindly
# would leave the block in place and look like a no-op — pick the newest backup
# that does NOT have it, which is the last state before our first write.
if [ "$ROLLBACK" -eq 1 ]; then
  RESTORE=""
  # Newest first by NAME (the backups are timestamped, so name order is time
  # order): ls -t only has 1-second resolution and two applies in the same second
  # would otherwise be ordered arbitrarily — which is exactly how a rollback can
  # silently restore the post-apply file.
  for cand in $(ls -1 "$CONFIG".pre-oidc-b304.* 2>/dev/null | sort -r); do
    if ! grep -qF "$MARK_BEGIN" "$cand"; then RESTORE="$cand"; break; fi
  done
  if [ -z "$RESTORE" ]; then
    if ls -1 "$CONFIG".pre-oidc-b304.* >/dev/null 2>&1; then
      die "every backup of $CONFIG already contains the managed block — remove it by hand (markers: $MARK_BEGIN / $MARK_END)"
    fi
    die "no backup of $CONFIG found ($CONFIG.pre-oidc-b304.*)"
  fi
  cp -a "$RESTORE" "$CONFIG"
  log "restored $CONFIG from $RESTORE"
  if [ "$DO_RESTART" -eq 1 ]; then restart_headscale || warn "restart after rollback failed"; fi
  exit 0
fi

# ---------------------------------------------------------------- format check
case "$CONFIG" in
  *.hujson|*.json)
    warn "HuJSON/JSON config detected: string surgery would corrupt it."
    printf '%s\n' "$BLOCK"
    die "merge the oidc: block above into $CONFIG by hand (keep its trailing-comma style), then restart headscale"
    ;;
esac

# ------------------------------------------------------ existing unmanaged key?
if grep -qE '^[[:space:]]*oidc:' "$CONFIG" && ! grep -qF "$MARK_BEGIN" "$CONFIG"; then
  warn "$CONFIG already has an oidc: section that this script did not write."
  printf '%s\n' "$BLOCK"
  die "refusing to rewrite it — merge the block above by hand (or move the old section away) and re-run"
fi

# ------------------------------------------------------------- build the result
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
awk -v b="$MARK_BEGIN" -v e="$MARK_END" '
  $0 == b { skip = 1; next }
  $0 == e { skip = 0; next }
  skip != 1 { print }
' "$CONFIG" > "$TMP"
printf '\n%s\n%s\n%s\n' "$MARK_BEGIN" "$BLOCK" "$MARK_END" >> "$TMP"

if [ "$DRY_RUN" -eq 1 ]; then
  log "--dry-run: diff for $CONFIG (nothing written)"
  diff -u "$CONFIG" "$TMP" || true
  exit 0
fi

# ------------------------------------------------------------------- write it
# The backup name must be UNIQUE: one-second granularity is not enough (two runs
# in the same second — a fix-up re-run right after the first — would overwrite the
# original backup with a copy of the already-modified config, and --rollback would
# then have nothing clean to restore).
STAMP="$(date +%Y%m%d%H%M%S)"
BACKUP="$CONFIG.pre-oidc-b304.$STAMP"
SUFFIX=1
while [ -e "$BACKUP" ]; do
  BACKUP="$CONFIG.pre-oidc-b304.$STAMP.$SUFFIX"
  SUFFIX=$((SUFFIX + 1))
done
cp -a "$CONFIG" "$BACKUP"
log "backup: $BACKUP"
cat "$TMP" > "$CONFIG"
log "wrote the managed oidc block"

# ------------------------------------------------------------------- restart
if [ "$DO_RESTART" -eq 1 ]; then
  if restart_headscale; then
    log "headscale restarted"
  else
    warn "restart failed — restoring $BACKUP"
    cp -a "$BACKUP" "$CONFIG"
    restart_headscale || true
    die "rolled back: headscale did not restart with the new config"
  fi
else
  log "--no-restart: config written, headscale not restarted"
fi

# -------------------------------------------------------------------- verify
if command -v curl >/dev/null 2>&1; then
  DISC="${ISSUER%/}/.well-known/openid-configuration"
  CODE="$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 10 "$DISC" 2>/dev/null || true)"
  if [ "$CODE" = "200" ]; then
    log "verify: $DISC -> HTTP 200"
  else
    warn "verify: $DISC -> ${CODE:-no answer} — check that headscale can reach $ISSUER"
  fi
fi
log "done. /admin/oidc/sync shows the same values; a Tailscale client login now goes through skygate."
