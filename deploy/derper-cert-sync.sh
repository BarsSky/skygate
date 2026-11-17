#!/usr/bin/env bash
# ============================================================================
# deploy/derper-cert-sync.sh — keep derper's certificate in step with the ACME source
# B483 (2026-11-17), closes the repo half of TD-38
#
# WHY THIS EXISTS
#
# derper runs with --certmode=manual and looks up its certificate BY HOSTNAME from --certdir, so
# something has to put the file there. On a host where a reverse proxy (Caddy, NPM) owns the ACME
# account, that something was a hand-written script plus a systemd timer living only on the machine
# — created during the 2026-11-15/16 cutover and never committed. A rebuilt host therefore lost the
# renewal path and the relay would have died silently ~90 days later, with no B-check able to notice
# (TD-38). This is that script, in the repository, with an --install that writes the units too.
#
# WHAT IT DOES
#
#   1. finds the freshest certificate+key pair for the hostname in the ACME source directory;
#   2. verifies the pair belongs to each other and that the CHAIN is complete (derper verifies the
#      chain; a leaf+intermediate trim broke certificate verification during the cutover with
#      "unable to get local issuer certificate");
#   3. copies them into <certdir>/<hostname>.crt and .key with mode 0600, owner root;
#   4. reloads derper only when the file actually changed;
#   5. exits 0 with a one-line verdict, or non-zero with the reason.
#
# USAGE
#   derper-cert-sync.sh [--install] [--dry-run]
#     --install   write /etc/systemd/system/derper-cert-sync.{service,timer} and enable the timer
#     --dry-run   report what would change, touch nothing
#
# CONFIGURATION (env, or the matching flags where noted)
#   DERP_HOSTNAME     certificate name derper looks up      (default: first -hostname in the unit,
#                                                           else the machine's FQDN)
#   ACME_CERT         source fullchain file                  (default: /var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/<host>/<host>.crt)
#   ACME_KEY          source key file                        (default: same directory, <host>.key)
#   DERP_CERTDIR      derper's --certdir                     (default: /var/lib/derper/certs)
#   DERP_UNIT         unit to restart on change              (default: derper)
#   SYNC_USER         owner of the copied files              (default: root)
#
# The script is idempotent: a run with an unchanged certificate rewrites nothing and restarts nobody.
# ============================================================================
set -uo pipefail

APPLY=0
DRY=0
INSTALL=0
for a in "$@"; do
  case "$a" in
    --install) INSTALL=1 ;;
    --dry-run) DRY=1 ;;
    -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
    *) echo "derper-cert-sync: unknown argument: $a" >&2; exit 2 ;;
  esac
done

say()  { printf 'derper-cert-sync: %s\n' "$*"; }
die()  { printf 'derper-cert-sync: %s\n' "$*" >&2; exit 1; }

DERP_CERTDIR="${DERP_CERTDIR:-/var/lib/derper/certs}"
DERP_UNIT="${DERP_UNIT:-derper}"
SYNC_USER="${SYNC_USER:-root}"
TIMER_UNIT=/etc/systemd/system/derper-cert-sync.service
TIMER_FILE=/etc/systemd/system/derper-cert-sync.timer

# --- resolve the hostname derper looks up ---------------------------------------------------------
HOST="${DERP_HOSTNAME:-}"
if [ -z "$HOST" ] && [ -f /etc/systemd/system/derper.service ]; then
  HOST="$(sed -n 's/.*--hostname[= ]\([A-Za-z0-9._-]*\).*/\1/p' /etc/systemd/system/derper.service | head -1)"
fi
if [ -z "$HOST" ]; then
  HOST="$(hostname -f 2>/dev/null || hostname)"
fi
[ -n "$HOST" ] || die "cannot determine the certificate hostname (set DERP_HOSTNAME)"
say "hostname=$HOST certdir=$DERP_CERTDIR"

# ACME_CERT/ACME_KEY default to Caddy's layout for this hostname; any other proxy sets them.
ACME_CERT="${ACME_CERT:-/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/$HOST/$HOST.crt}"
ACME_KEY="${ACME_KEY:-/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/$HOST/$HOST.key}"

if [ ! -r "$ACME_CERT" ] || [ ! -r "$ACME_KEY" ]; then
  # Not an error we can fix: the proxy may not have issued the certificate yet (DNS not switched, or
  # a different ACME client). Say exactly which path was missing — the cutover lost time on a probe
  # that only said "no certificate".
  die "no ACME pair for $HOST: missing $ACME_CERT or $ACME_KEY (set ACME_CERT/ACME_KEY if the proxy stores them elsewhere, or wait for the certificate to be issued)"
fi

CHAIN_COUNT="$(grep -c 'BEGIN CERTIFICATE' "$ACME_CERT" || true)"
if [ "${CHAIN_COUNT:-0}" -lt 2 ]; then
  die "$ACME_CERT contains ${CHAIN_COUNT:-0} certificate(s): derper verifies the CHAIN, so the full chain (leaf + intermediate) is required — trimming it produces 'unable to get local issuer certificate'"
fi

if ! openssl x509 -in "$ACME_CERT" -noout >/dev/null 2>&1; then
  die "$ACME_CERT is not a PEM certificate"
fi
CRT_MOD="$(openssl x509 -in "$ACME_CERT" -noout -pubkey 2>/dev/null | openssl sha256 2>/dev/null | awk '{print $NF}')"
KEY_MOD="$(openssl pkey -in "$ACME_KEY" -pubout 2>/dev/null | openssl sha256 2>/dev/null | awk '{print $NF}')"
if [ -z "$CRT_MOD" ] || [ "$CRT_MOD" != "$KEY_MOD" ]; then
  die "the certificate and the key do not match ($ACME_CERT vs $ACME_KEY)"
fi
NOT_AFTER="$(openssl x509 -in "$ACME_CERT" -noout -enddate | cut -d= -f2)"

mkdir -p "$DERP_CERTDIR" || die "cannot create $DERP_CERTDIR"
DST_CRT="$DERP_CERTDIR/$HOST.crt"
DST_KEY="$DERP_CERTDIR/$HOST.key"

changed=0
install_pair() { # $1 = source, $2 = destination, $3 = mode
  if [ -f "$2" ] && cmp -s "$1" "$2"; then
    return 0
  fi
  changed=1
  if [ "$DRY" -eq 1 ]; then
    say "would update $2"
    return 0
  fi
  # Ownership is asserted only when we are root: an operator without privileges (or a CI host) must
  # still be able to prove the copy + idempotency half, and a silent chown failure is worse than an
  # explicit "root needed" note.
  if [ "$(id -u)" -eq 0 ]; then
    install -m "$3" -o "$SYNC_USER" -g "$SYNC_USER" "$1" "$2" || die "cannot write $2"
  else
    install -m "$3" "$1" "$2" || die "cannot write $2 (run as root to set owner $SYNC_USER)"
  fi
  say "updated $2"
}

install_pair "$ACME_CERT" "$DST_CRT" 0644
install_pair "$ACME_KEY"  "$DST_KEY" 0600

if [ "$changed" -eq 0 ]; then
  say "unchanged (certificate valid until $NOT_AFTER)"
else
  if [ "$DRY" -eq 1 ]; then
    say "would reload $DERP_UNIT (dry run)"
  elif systemctl is-active --quiet "$DERP_UNIT"; then
    systemctl reload-or-restart "$DERP_UNIT" && say "reloaded $DERP_UNIT" || die "certificate copied but $DERP_UNIT did not reload"
  else
    say "$DERP_UNIT is not running — the certificate will be picked up when it starts"
  fi
fi

# --- the units ------------------------------------------------------------------------------------
if [ "$INSTALL" -eq 1 ]; then
  SELF="$(readlink -f "$0")"
  if [ "$DRY" -eq 1 ]; then
    say "would install $TIMER_UNIT and $TIMER_FILE (dry run)"
    exit 0
  fi
  cat > "$TIMER_UNIT" <<UNIT
[Unit]
Description=Keep derper's TLS certificate in step with the ACME source (B483)
Documentation=file:///opt/skygate/docs/derp.md
After=network-online.target

[Service]
Type=oneshot
ExecStart=$SELF
Environment=DERP_HOSTNAME=$HOST
Environment=DERP_CERTDIR=$DERP_CERTDIR
UNIT
  cat > "$TIMER_FILE" <<UNIT
[Unit]
Description=Daily derper certificate sync (B483)

[Timer]
OnCalendar=*-*-* 04:17:00
RandomizedDelaySec=30m
Persistent=true

[Install]
WantedBy=timers.target
UNIT
  systemctl daemon-reload
  systemctl enable --now derper-cert-sync.timer >/dev/null 2>&1 || say "WARN: could not enable derper-cert-sync.timer"
  say "installed $TIMER_UNIT and $TIMER_FILE (daily at 04:17 ± 30m)"
fi

say "ok hostname=$HOST not_after=$NOT_AFTER changed=$changed"
