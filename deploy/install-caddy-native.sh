#!/usr/bin/env bash
# ============================================================================
# deploy/install-caddy-native.sh — recreate the NATIVE (systemd) reverse proxy
#
# WHY THIS EXISTS (TD-38)
#
# During the 2026-11-15 host migration the reverse proxy of the production
# panel host was written BY HAND: the panel site with its maintenance-page
# import, the headscale site with the HTTP/1.1 shape, and the DERP site with its
# own certificate name. None of it was in the repository, so a rebuilt host
# would come up with no public entry point at all and no contract would notice.
# This script is that missing piece: it renders deploy/caddy/native/Caddyfile.tmpl
# with the host's real names, validates it, keeps a backup and reloads Caddy.
#
# It also installs the maintenance page (B485) unless told not to, because the
# rendered file IMPORTS that page — a Caddyfile that imports a missing file
# fails to load, and a failed load takes the whole site down.
#
# USAGE
#   sudo bash deploy/install-caddy-native.sh \
#       --panel-host panel.example.com \
#       --headscale-host head.example.com \
#       --derp-host derp.example.com \
#       --acme-email admin@example.com
#
#   bash deploy/install-caddy-native.sh --panel-host panel.example.com --dry-run
#
# FLAGS
#   --panel-host HOST        public name of the panel (required)
#   --headscale-host HOST    public name of the headscale API (required)
#   --derp-host HOST         public name of the relay; omit to skip the DERP site
#   --acme-email ADDR        ACME account contact (strongly recommended)
#   --panel-port PORT        loopback panel port (default 8080)
#   --headscale-port PORT    loopback headscale port (default 50444)
#   --derp-port PORT         derper's local TLS port (default 8443)
#   --maintenance-dir DIR    where the maintenance page is installed
#                            (default /etc/caddy/maintenance)
#   --caddyfile PATH         target (default /etc/caddy/Caddyfile)
#   --no-maintenance         do NOT install the maintenance page (you already
#                            have it at --maintenance-dir)
#   --no-reload              write + validate, but do not reload Caddy
#   --force                  overwrite even if the target is not ours
#   --dry-run                print the rendered file and stop
#
# EXIT: 0 ok · 1 usage error · 2 validation failed · 3 refusing to overwrite
# ============================================================================

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="${SCRIPT_DIR}/caddy/native/Caddyfile.tmpl"

PANEL_HOST=""
HEADSCALE_HOST=""
DERP_HOST=""
ACME_EMAIL=""
PANEL_PORT=8080
HEADSCALE_PORT=50444
DERP_PORT=8443
MAINTENANCE_DIR="/etc/caddy/maintenance"
CADDYFILE="/etc/caddy/Caddyfile"
DO_MAINTENANCE=1
DO_RELOAD=1
FORCE=0
DRY=0

say()  { echo "[caddy-native] $*"; }
warn() { echo "[caddy-native] WARN: $*" >&2; }
die()  { local rc="$1"; shift; echo "[caddy-native] ERROR: $*" >&2; exit "$rc"; }
usage() { sed -n '2,60p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

need_val() { # need_val <flag> <value?>
    [ -n "${2:-}" ] || die 1 "$1 needs a value"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --panel-host)       need_val "$1" "${2:-}"; PANEL_HOST="$2"; shift 2 ;;
        --headscale-host)   need_val "$1" "${2:-}"; HEADSCALE_HOST="$2"; shift 2 ;;
        --derp-host)        need_val "$1" "${2:-}"; DERP_HOST="$2"; shift 2 ;;
        --acme-email)       need_val "$1" "${2:-}"; ACME_EMAIL="$2"; shift 2 ;;
        --panel-port)       need_val "$1" "${2:-}"; PANEL_PORT="$2"; shift 2 ;;
        --headscale-port)   need_val "$1" "${2:-}"; HEADSCALE_PORT="$2"; shift 2 ;;
        --derp-port)        need_val "$1" "${2:-}"; DERP_PORT="$2"; shift 2 ;;
        --maintenance-dir)  need_val "$1" "${2:-}"; MAINTENANCE_DIR="$2"; shift 2 ;;
        --caddyfile)        need_val "$1" "${2:-}"; CADDYFILE="$2"; shift 2 ;;
        --no-maintenance)   DO_MAINTENANCE=0; shift ;;
        --no-reload)        DO_RELOAD=0; shift ;;
        --force)            FORCE=1; shift ;;
        --dry-run)          DRY=1; shift ;;
        -h|--help)          usage; exit 0 ;;
        *) die 1 "unknown flag: $1 (see --help)" ;;
    esac
done

[ -f "$TEMPLATE" ] || die 1 "template not found: $TEMPLATE"
[ -n "$PANEL_HOST" ] || die 1 "--panel-host is required"
[ -n "$HEADSCALE_HOST" ] || die 1 "--headscale-host is required"
for pair in "panel-port:$PANEL_PORT" "headscale-port:$HEADSCALE_PORT" "derp-port:$DERP_PORT"; do
    case "${pair#*:}" in
        ''|*[!0-9]*) die 1 "invalid ${pair%%:*}: ${pair#*:}" ;;
    esac
done
case "$MAINTENANCE_DIR" in /*) : ;; *) die 1 "--maintenance-dir must be an absolute path" ;; esac

if [ "$DRY" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
    die 1 "run as root (or pass --dry-run to just see the result)"
fi

# ── the maintenance page must exist BEFORE a Caddyfile that imports it ────────
if [ "$DO_MAINTENANCE" -eq 1 ]; then
    page_installer="${SCRIPT_DIR}/install-maintenance-page.sh"
    if [ -f "$page_installer" ]; then
        if [ "$DRY" -eq 1 ]; then
            say "would install the maintenance page into $MAINTENANCE_DIR"
        else
            # --caddyfile is what makes the site import the page; --dest is where the
            # two files and index.html go. Failure here must stop the run: writing a
            # Caddyfile whose import is missing would take the site down.
            bash "$page_installer" --caddyfile "$CADDYFILE" --dest "$MAINTENANCE_DIR" \
                || die 2 "installing the maintenance page failed; the Caddyfile needs it"
        fi
    else
        [ -f "$MAINTENANCE_DIR/index.html" ] \
            || die 2 "no install-maintenance-page.sh and no page in $MAINTENANCE_DIR"
    fi
fi

# ── render ───────────────────────────────────────────────────────────────────
RENDERED="$(mktemp)" || die 1 "mktemp failed"
trap 'rm -f "$RENDERED"' EXIT

acme_line=""
if [ -n "$ACME_EMAIL" ]; then
    # A top-level global option must come BEFORE the first site block.
    acme_line="{"
    acme_line="${acme_line}
	email ${ACME_EMAIL}
}"
else
    warn "no --acme-email: Let's Encrypt cannot tell you about expiry (the account still works)"
fi
# Every site is rendered as a literal block; an empty --derp-host removes the
# section rather than leaving a comment, so `caddy validate` sees no empty site.
if [ -n "$DERP_HOST" ]; then
    derp_site="
# DERP relay under its own name: SNI must match derper's --hostname, and the
# internal hop to the relay verifies against that same name (trap 3).
${DERP_HOST} {
	encode zstd gzip
	reverse_proxy https://127.0.0.1:${DERP_PORT} {
		transport http {
			tls_server_name ${DERP_HOST}
		}
	}
}
"
else
    derp_site="
# DERP site omitted: this host does not serve a relay (no --derp-host).
"
fi

# Rendering is done in BASH, not sed: two placeholders expand to MULTI-LINE blocks
# (the global ACME option and the DERP site), and sed cannot carry a newline in a
# replacement — measured: "sed: char 23: unterminated `s' command", after which the
# script happily reported "rendered 0 line(s)" and would have written an EMPTY
# Caddyfile. Bash replacement is also immune to `&`, `|` and `\` in a hostname.
render_into() { # render_into <output>
    local line
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            *'@@ACME_EMAIL_LINE@@'*) printf '%s\n' "$acme_line"; continue ;;
        esac
        line="${line//@@PANEL_HOST@@/$PANEL_HOST}"
        line="${line//@@HEADSCALE_HOST@@/$HEADSCALE_HOST}"
        line="${line//@@PANEL_PORT@@/$PANEL_PORT}"
        line="${line//@@HEADSCALE_PORT@@/$HEADSCALE_PORT}"
        line="${line//@@MAINTENANCE_DIR@@/$MAINTENANCE_DIR}"
        case "$line" in
            *'@@DERP_SITE@@'*) printf '%s\n' "$derp_site" ;;
            *) printf '%s\n' "$line" ;;
        esac
    done < "$TEMPLATE" > "$1"
}

render_into "$RENDERED"

if [ ! -s "$RENDERED" ]; then
    die 1 "rendered file is EMPTY — nothing to write ($TEMPLATE)"
fi

if grep -q '@@[A-Z_]*@@' "$RENDERED"; then
    die 1 "unresolved placeholder(s) in the rendered file: $(grep -o '@@[A-Z_]*@@' "$RENDERED" | sort -u | tr '\n' ' ')"
fi

# ── validate BEFORE anything is written (and before --dry-run returns) ───────
# A dry run that does not validate would let an operator "verify" a template that
# Caddy rejects; the whole point of rendering into a temp file is that the real
# parser answers before the live file is touched.
if command -v caddy >/dev/null 2>&1; then
    vout="$(caddy validate --config "$RENDERED" --adapter caddyfile 2>&1)"; vrc=$?
    if [ "$vrc" -ne 0 ]; then
        printf '%s\n' "$vout" >&2
        die 2 "caddy validate rejected the rendered Caddyfile — nothing was written"
    fi
    say "caddy validate: ok"
else
    warn "caddy binary not found — cannot validate the rendered file"
fi

if [ "$DRY" -eq 1 ]; then
    say "rendered $(wc -l < "$RENDERED") line(s) — would write $CADDYFILE (dry run: nothing written)"
    echo "----------------------------------------------------------------"
    cat "$RENDERED"
    echo "----------------------------------------------------------------"
    exit 0
fi

# ── backup + write + reload ──────────────────────────────────────────────────
if [ -f "$CADDYFILE" ] && [ "$FORCE" -eq 0 ]; then
    if ! grep -q 'install-caddy-native' "$CADDYFILE" && ! grep -q 'caddy-native' "$CADDYFILE"; then
        die 3 "$CADDYFILE exists and was not written by this installer — re-run with --force to overwrite it (a backup is always kept)"
    fi
fi
if [ -f "$CADDYFILE" ]; then
    backup="${CADDYFILE}.bak-$(date +%Y%m%d%H%M%S)"
    cp -p "$CADDYFILE" "$backup"
    say "backup: $backup"
fi
install -d -m 0755 "$(dirname "$CADDYFILE")"
# Mode 0644: Caddy runs as its own user; a root-only file is unreadable for it.
install -m 0644 "$RENDERED" "$CADDYFILE"
say "wrote $CADDYFILE"

if [ "$DO_RELOAD" -eq 1 ]; then
    if systemctl is-active --quiet caddy 2>/dev/null; then
        if systemctl reload caddy 2>/dev/null; then
            say "caddy reloaded"
        else
            warn "systemctl reload caddy failed — the OLD config is still serving; check journalctl -u caddy"
        fi
    else
        warn "caddy.service is not active — wrote the file, did not reload (systemctl start caddy when ready)"
    fi
fi

say "done: panel=${PANEL_HOST} headscale=${HEADSCALE_HOST} derp=${DERP_HOST:-none}"
