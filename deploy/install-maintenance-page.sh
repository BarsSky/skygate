#!/usr/bin/env bash
# ============================================================================
# deploy/install-maintenance-page.sh — B485
#
# Installs the "panel is updating, this page returns by itself" error page and wires it into the
# panel's Caddy site block. See deploy/caddy/maintenance.caddy for WHY this is the proxy's job and
# not the panel's.
#
# USAGE
#   sudo bash deploy/install-maintenance-page.sh                 # install + wire the panel block
#   sudo bash deploy/install-maintenance-page.sh --dry-run       # show the diff, change nothing
#   sudo bash deploy/install-maintenance-page.sh --host head.example.com --caddyfile /etc/caddy/Caddyfile
#
# WHAT IT TOUCHES
#   /var/lib/skygate/maintenance/index.html    the page
#   /var/lib/skygate/maintenance/maintenance.caddy  the snippet the site block imports
#   <caddyfile>                                ONE inserted `import` line per run, backed up first
#
# The patch is idempotent: a Caddyfile that already imports the snippet is left alone. The new
# Caddyfile is validated with `caddy validate` BEFORE anything is reloaded; a validation failure
# restores the backup and exits non-zero, so a typo can never leave the operator without TLS.
# ============================================================================
set -uo pipefail

CADDYFILE=/etc/caddy/Caddyfile
DEST_DIR=/etc/caddy/maintenance
HOST=""
DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --caddyfile) CADDYFILE="${2:-}"; shift 2 ;;
    --dest)      DEST_DIR="${2:-}"; shift 2 ;;
    --host)      HOST="${2:-}"; shift 2 ;;
    --dry-run)   DRY=1; shift ;;
    -h|--help) sed -n '2,32p' "$0"; exit 0 ;;
    *) printf 'maintenance: unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

say() { printf 'maintenance: %s\n' "$*"; }
die() { printf 'maintenance: %s\n' "$*" >&2; exit 1; }

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_PAGE="$SELF_DIR/caddy/maintenance/index.html"
SRC_SNIP="$SELF_DIR/caddy/maintenance.caddy"
[ -f "$SRC_PAGE" ] || die "missing $SRC_PAGE — run this from the repository checkout (deploy/)"
[ -f "$SRC_SNIP" ] || die "missing $SRC_SNIP — run this from the repository checkout (deploy/)"
[ -f "$CADDYFILE" ] || die "no Caddyfile at $CADDYFILE (pass --caddyfile)"

# The panel's name: the operator's site block, guessed from the Caddyfile when not given.
if [ -z "$HOST" ]; then
  HOST="$(awk '/^[A-Za-z0-9*.-]+[[:space:]]*\{/ {gsub(/[[:space:]]*\{.*/,""); print; exit}' "$CADDYFILE")"
fi
[ -n "$HOST" ] || die "could not find a site block in $CADDYFILE — pass --host"

say "host=$HOST caddyfile=$CADDYFILE dest=$DEST_DIR"

IMPORT_LINE="	import $DEST_DIR/maintenance.caddy"

# --- 1. the page + snippet ----------------------------------------------------------------------
if [ "$DRY" -eq 1 ]; then
  say "would install $DEST_DIR/index.html + maintenance.caddy + maintenance-server.caddy"
else
  install -d -m 0755 "$DEST_DIR"
  install -m 0644 "$SRC_PAGE" "$DEST_DIR/index.html"
  install -m 0644 "$SRC_SNIP" "$DEST_DIR/maintenance.caddy"
  install -m 0644 "$SELF_DIR/caddy/maintenance-server.caddy" "$DEST_DIR/maintenance-server.caddy"
  say "installed the page and the snippet into $DEST_DIR"
fi

# --- 2. wire it into the panel's site block -----------------------------------------------------
if grep -qF "$DEST_DIR/maintenance.caddy" "$CADDYFILE"; then
  say "the site block already imports the snippet — nothing to patch"
  NEW=""
else
  NEW="$(mktemp)"
  awk -v host="$HOST" -v ins="$IMPORT_LINE" '
    BEGIN { inblk = 0; done = 0; depth = 0 }
    {
      line = $0
      if (!inblk && !done && line ~ ("^[[:space:]]*" host "[[:space:]]*\\{")) { inblk = 1 }
      if (inblk && !done && line ~ /^[[:space:]]*reverse_proxy[[:space:]]/) {
        print ins; done = 1
      }
      print line
      if (inblk && line ~ /^[[:space:]]*reverse_proxy/) { inblk = 2 }
    }
    END { if (!done) exit 3 }
  ' "$CADDYFILE" > "$NEW" || { rm -f "$NEW"; die "could not find the reverse_proxy line of the $HOST block in $CADDYFILE"; }

  if [ "$DRY" -eq 1 ]; then
    say "would add this line before reverse_proxy in the $HOST block:"
    printf '  %s\n' "$IMPORT_LINE"
    rm -f "$NEW"
    exit 0
  fi

  BACKUP="$CADDYFILE.bak-$(date -u +%Y%m%d%H%M%S)"
  cp -a "$CADDYFILE" "$BACKUP" || die "cannot back up $CADDYFILE"
  say "backup: $BACKUP"
  install -m 0644 "$NEW" "$CADDYFILE"; rm -f "$NEW"
  say "added the import line to the $HOST block"
fi

# --- 2b. import the loopback static server at TOP LEVEL ------------------------------------------
SERVER_IMPORT="import $DEST_DIR/maintenance-server.caddy"
if grep -qF "$SERVER_IMPORT" "$CADDYFILE"; then
  say "the Caddyfile already imports the static server"
else
  if [ "$DRY" -eq 1 ]; then
    say "would append to the Caddyfile: $SERVER_IMPORT"
  else
    printf '\n# Skygate maintenance page (B485): loopback static server for the proxy error handler.\n%s\n' "$SERVER_IMPORT" >> "$CADDYFILE"
    say "appended the static-server import"
  fi
fi

# --- 3. validate before reloading ---------------------------------------------------------------
if command -v caddy >/dev/null 2>&1; then
  if OUT="$(caddy validate --config "$CADDYFILE" --adapter caddyfile 2>&1)"; then
    say "caddy validate: ok"
  else
    [ -n "${BACKUP:-}" ] && cp -a "$BACKUP" "$CADDYFILE" && say "restored $BACKUP after a validation failure"
    printf '%s\n' "$OUT" | tail -5 >&2
    die "the patched Caddyfile does not validate — nothing was reloaded"
  fi
  if systemctl is-active --quiet caddy; then
    if systemctl reload caddy 2>/dev/null || systemctl restart caddy; then
      say "caddy reloaded"
    else
      die "caddy did not reload — check journalctl -u caddy"
    fi
  else
    say "caddy is not running here — the change takes effect when it starts"
  fi
else
  say "caddy is not on PATH — validate and reload it by hand (caddy validate --config $CADDYFILE)"
fi

say "done. Test it: sudo systemctl stop skygate, open the panel, then start it again."
