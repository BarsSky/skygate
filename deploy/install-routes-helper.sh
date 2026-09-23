#!/usr/bin/env bash
# install-routes-helper.sh — (re)install the privileged LOCAL-routes helper on an
# EXISTING native (systemd) install. B293 (2026-09-23).
#
# WHY: when an exit node IS the machine that runs skygate (the operator's `aro`:
# the local tailscaled is `exit-node-vps` / 100.64.0.1), the advertised routes are
# applied with `tailscale set` against the LOCAL daemon. skygate tries, in order:
# `tailscale set` directly (root, or the daemon's `--operator` user), then
# `sudo -n tailscale set`, then THIS helper. So the helper is the rung that makes
# the feature work on an install where the service has no root, no sudo and no
# operator grant — exactly the case the operator asked to cover ("бывает что
# ставят без [root] … чтобы не было ситуации что нет возможности настроить по
# причине доступа").
#
# Idempotent: re-running overwrites the two units (project-owned files) and the
# applier, then re-arms the path unit. Safe to run at any time.
#
# Usage (as root):
#     bash deploy/install-routes-helper.sh
#     SKYGATE_UPDATE_DIR=/var/lib/skygate/update bash deploy/install-routes-helper.sh

set -euo pipefail

if [ "$(id -u)" != "0" ]; then
    echo "install-routes-helper: must run as root (it writes /etc/systemd/system and /usr/local/lib)" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The .path unit watches <update_dir>/routes.request.props, so the value MUST
# match the running service — a mismatch is silent (the request is staged and
# nothing ever fires).
UPDATE_DIR="${SKYGATE_UPDATE_DIR:-}"
if [ -z "$UPDATE_DIR" ]; then
    for envf in /etc/skygate/skygate.env /etc/default/skygate /etc/sysconfig/skygate; do
        if [ -r "$envf" ]; then
            found="$(grep -E '^SKYGATE_UPDATE_DIR=' "$envf" | tail -1 | cut -d= -f2- | tr -d '"'"'"' ' || true)"
            [ -n "$found" ] && { UPDATE_DIR="$found"; echo "install-routes-helper: SKYGATE_UPDATE_DIR=$UPDATE_DIR (from $envf)"; break; }
        fi
    done
fi
if [ -z "$UPDATE_DIR" ]; then
    UPDATE_DIR="$(systemctl show skygate -p Environment 2>/dev/null | tr ' ' '\n' | grep -E '^SKYGATE_UPDATE_DIR=' | tail -1 | cut -d= -f2- || true)"
    [ -n "$UPDATE_DIR" ] && echo "install-routes-helper: SKYGATE_UPDATE_DIR=$UPDATE_DIR (from systemctl show skygate)"
fi
if [ -z "$UPDATE_DIR" ]; then
    UPDATE_DIR="/var/lib/skygate/update"
    echo "install-routes-helper: SKYGATE_UPDATE_DIR not configured anywhere, using $UPDATE_DIR"
    echo "install-routes-helper: WARN if the service uses a different value the helper will never fire"
fi
if [ ! -d "$UPDATE_DIR" ]; then
    echo "install-routes-helper: WARN $UPDATE_DIR does not exist yet (skygate creates it on start)"
fi

command -v systemctl >/dev/null 2>&1 || {
    echo "install-routes-helper: systemd not found — this script is for native systemd installs" >&2
    exit 1
}

if [ ! -f "$SCRIPT_DIR/install-common.sh" ]; then
    echo "install-routes-helper: $SCRIPT_DIR/install-common.sh not found — run from a full checkout" >&2
    exit 1
fi
if [ ! -f "$SCRIPT_DIR/skygate-apply-routes.sh" ]; then
    echo "install-routes-helper: $SCRIPT_DIR/skygate-apply-routes.sh not found — the applier is part of the repo" >&2
    exit 1
fi

# The helper needs a tailscale CLI; without it the applier can only report the
# failure. Warn early (the applier also checks, per run).
if ! command -v tailscale >/dev/null 2>&1; then
    echo "install-routes-helper: WARN 'tailscale' is not in PATH — set SKYGATE_TAILSCALE_CLI"
    echo "install-routes-helper:      in /etc/skygate/routes-helper.conf, or install the CLI"
fi

echo "install-routes-helper: installing the applier + the two units (update_dir=$UPDATE_DIR)"
# Reuse the project's own writer so the units can never drift from the ones a
# fresh install produces (single source of truth).
( SCRIPT_DIR="$SCRIPT_DIR"; . "$SCRIPT_DIR/install-common.sh"; write_routes_units "$UPDATE_DIR" )

systemctl daemon-reload
systemctl enable --now skygate-routes.path

echo
echo "install-routes-helper: state"
systemctl is-active skygate-routes.path | sed 's/^/  skygate-routes.path: /'
ls -l /usr/local/lib/skygate/skygate-apply-routes.sh 2>/dev/null | sed 's/^/  /'
if ls -l "$UPDATE_DIR"/routes.request.props 2>/dev/null; then
    echo "  ^ a request is already queued: the path unit fires on start, the applier will consume it"
else
    echo "  no queued routes request (nothing to flush)"
fi

cat <<EOT

install-routes-helper: done.
  Next: press Re-sync on /admin/exit-nodes (or wait for the periodic sync) and
  check the flash / the result string:
      local=ok via helper approved=N     -> applied by this root-owned applier
  and the applier's own verdict:
      cat $UPDATE_DIR/routes-apply.status
      tail -5 $UPDATE_DIR/routes-apply.log
  Do not want the helper at all? Grant the skygate user rights on the daemon
  instead (one of these, both documented in docs/networking.md):
      sudo tailscale set --operator=<skygate user>        # skygate runs it directly
      echo '<skygate user> ALL=(root) NOPASSWD: /usr/bin/tailscale set' \\
          > /etc/sudoers.d/skygate-tailscale            # skygate uses sudo -n
EOT
