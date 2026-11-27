#!/usr/bin/env bash
# install-service-helper.sh — (re)install the privileged SERVICE-CONTROL helper
# on an EXISTING native (systemd) install. B533 (2026-11-27).
#
# WHY: /admin/headscale must be able to (a) say what the headscale service is
# doing and (b) restart it. The native skygate unit runs unprivileged under
# ProtectSystem=strict, so `systemctl restart headscale` is not a right it has —
# and on 2026-11-27 the operator's panel was unreachable exactly when the control
# plane needed a restart, so the console was the only door left.
#
# This installs the same privilege split the policy (B272.3) and local-routes
# (B293) appliers use: the unprivileged service drops a DATA-ONLY
# <update_dir>/service.request.props, and the root-owned
# skygate-service.path/.service pair turns its appearance into
# deploy/skygate-apply-service.sh.
#
# Idempotent: re-running overwrites the two units (project-owned files) and the
# applier, then re-arms the path unit. Safe to run at any time.
#
# Usage (as root):
#     bash deploy/install-service-helper.sh
#     SKYGATE_UPDATE_DIR=/var/lib/skygate/update bash deploy/install-service-helper.sh

set -euo pipefail

if [ "$(id -u)" != "0" ]; then
    echo "install-service-helper: must run as root (it writes /etc/systemd/system and /usr/local/lib)" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The .path unit watches <update_dir>/service.request.props, so the value MUST
# match the running service — a mismatch is silent (the request is staged and
# nothing ever fires).
UPDATE_DIR="${SKYGATE_UPDATE_DIR:-}"
if [ -z "$UPDATE_DIR" ]; then
    for envf in /etc/skygate/skygate.env /etc/default/skygate /etc/sysconfig/skygate; do
        if [ -r "$envf" ]; then
            found="$(grep -E '^SKYGATE_UPDATE_DIR=' "$envf" | tail -1 | cut -d= -f2- | tr -d '"'"'"' ' || true)"
            [ -n "$found" ] && { UPDATE_DIR="$found"; echo "install-service-helper: SKYGATE_UPDATE_DIR=$UPDATE_DIR (from $envf)"; break; }
        fi
    done
fi
if [ -z "$UPDATE_DIR" ]; then
    UPDATE_DIR="$(systemctl show skygate -p Environment 2>/dev/null | tr ' ' '\n' | grep -E '^SKYGATE_UPDATE_DIR=' | tail -1 | cut -d= -f2- || true)"
    [ -n "$UPDATE_DIR" ] && echo "install-service-helper: SKYGATE_UPDATE_DIR=$UPDATE_DIR (from systemctl show skygate)"
fi
if [ -z "$UPDATE_DIR" ]; then
    UPDATE_DIR="/var/lib/skygate/update"
    echo "install-service-helper: SKYGATE_UPDATE_DIR not configured anywhere, using $UPDATE_DIR"
    echo "install-service-helper: WARN if the service uses a different value the helper will never fire"
fi
if [ ! -d "$UPDATE_DIR" ]; then
    echo "install-service-helper: WARN $UPDATE_DIR does not exist yet (skygate creates it on start)"
fi

command -v systemctl >/dev/null 2>&1 || {
    echo "install-service-helper: systemd not found — this script is for native systemd installs" >&2
    exit 1
}

if [ ! -f "$SCRIPT_DIR/install-common.sh" ]; then
    echo "install-service-helper: $SCRIPT_DIR/install-common.sh not found — run from a full checkout" >&2
    exit 1
fi
if [ ! -f "$SCRIPT_DIR/skygate-apply-service.sh" ]; then
    echo "install-service-helper: $SCRIPT_DIR/skygate-apply-service.sh not found — the applier is part of the repo" >&2
    exit 1
fi

echo "install-service-helper: installing the applier + the two units (update_dir=$UPDATE_DIR)"
# Reuse the project's own writer so the units can never drift from the ones a
# fresh install produces (single source of truth).
( SCRIPT_DIR="$SCRIPT_DIR"; . "$SCRIPT_DIR/install-common.sh"; write_service_units "$UPDATE_DIR" )

systemctl daemon-reload
systemctl enable --now skygate-service.path

echo
echo "install-service-helper: state"
systemctl is-active skygate-service.path | sed 's/^/  skygate-service.path: /'
ls -l /usr/local/lib/skygate/skygate-apply-service.sh 2>/dev/null | sed 's/^/  /'

cat <<EOT

install-service-helper: done.
  Next: open /admin/headscale — the service card now has a Restart button, and
  its verdict is read back from:
      cat $UPDATE_DIR/service.apply.status
      tail -5 $UPDATE_DIR/service-apply.log
  The applier only ever touches the allow-listed services
  (headscale skygate caddy derper tailscaled) and only the actions
  restart|status; the allow-list lives in BOTH halves (Go and the applier).
EOT
