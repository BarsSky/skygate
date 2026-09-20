#!/usr/bin/env bash
# install-policy-helper.sh — (re)install the privileged headscale-policy helper
# on an EXISTING native (systemd) install.
#
# WHY: `write_policy_units()` in deploy/install-common.sh only runs during a
# fresh install. A host installed before v1.5.16 therefore never gets
# `skygate-policy.path` / `skygate-policy.service`, and because the skygate unit
# runs with ProtectSystem=strict the policy file is READ-ONLY for it — so
# `ensureTagIsPermitted` can never add the tagOwners entry, headscale keeps
# answering `400 requested tags [...] are invalid or not permitted`, and no
# device tag ever lands. Live case: the native host `aro` ran 13 h with
# `skygate-policy.path: inactive/missing`, `tag:dev-* entries: 0` in the policy
# and `tag-reconcile: applied=0 failed=3` every five minutes.
#
# Idempotent: re-running overwrites the two units (project-owned files) and the
# applier, then re-arms the path unit. Safe to run at any time.
#
# Usage (as root):
#     bash deploy/install-policy-helper.sh
#     SKYGATE_UPDATE_DIR=/var/lib/skygate/update bash deploy/install-policy-helper.sh

set -euo pipefail

if [ "$(id -u)" != "0" ]; then
    echo "install-policy-helper: must run as root (it writes /etc/systemd/system and /usr/local/lib)" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The .path unit watches <update_dir>/policy.request.props, so the value MUST
# match the running service. Prefer the service's own env file over a guess:
# a mismatch here is silent (the file is staged, nothing ever fires).
UPDATE_DIR="${SKYGATE_UPDATE_DIR:-}"
if [ -z "$UPDATE_DIR" ]; then
    for envf in /etc/skygate/skygate.env /etc/default/skygate /etc/sysconfig/skygate; do
        if [ -r "$envf" ]; then
            found="$(grep -E '^SKYGATE_UPDATE_DIR=' "$envf" | tail -1 | cut -d= -f2- | tr -d '"'"'"' ' || true)"
            [ -n "$found" ] && { UPDATE_DIR="$found"; echo "install-policy-helper: SKYGATE_UPDATE_DIR=$UPDATE_DIR (from $envf)"; break; }
        fi
    done
fi
if [ -z "$UPDATE_DIR" ]; then
    # Fall back to what the RUNNING service reports (systemd Environment=).
    UPDATE_DIR="$(systemctl show skygate -p Environment 2>/dev/null | tr ' ' '\n' | grep -E '^SKYGATE_UPDATE_DIR=' | tail -1 | cut -d= -f2- || true)"
    [ -n "$UPDATE_DIR" ] && echo "install-policy-helper: SKYGATE_UPDATE_DIR=$UPDATE_DIR (from systemctl show skygate)"
fi
if [ -z "$UPDATE_DIR" ]; then
    UPDATE_DIR="/var/lib/skygate/update"
    echo "install-policy-helper: SKYGATE_UPDATE_DIR not configured anywhere, using $UPDATE_DIR"
    echo "install-policy-helper: WARN if the service uses a different value the helper will never fire"
fi
if [ ! -d "$UPDATE_DIR" ]; then
    echo "install-policy-helper: WARN $UPDATE_DIR does not exist yet (skygate creates it on start)"
fi

command -v systemctl >/dev/null 2>&1 || {
    echo "install-policy-helper: systemd not found — this script is for native systemd installs" >&2
    exit 1
}

if [ ! -f "$SCRIPT_DIR/install-common.sh" ]; then
    echo "install-policy-helper: $SCRIPT_DIR/install-common.sh not found — run from a full checkout" >&2
    exit 1
fi
if [ ! -f "$SCRIPT_DIR/skygate-apply-policy.sh" ]; then
    echo "install-policy-helper: $SCRIPT_DIR/skygate-apply-policy.sh not found — the applier is part of the repo" >&2
    exit 1
fi

echo "install-policy-helper: installing the applier + the two units (update_dir=$UPDATE_DIR)"
# Reuse the project's own writer so the units can never drift from the ones a
# fresh install produces (single source of truth).
( SCRIPT_DIR="$SCRIPT_DIR"; . "$SCRIPT_DIR/install-common.sh"; write_policy_units "$UPDATE_DIR" )

systemctl daemon-reload
systemctl enable --now skygate-policy.path

echo
echo "install-policy-helper: state"
systemctl is-active skygate-policy.path  | sed 's/^/  skygate-policy.path: /'
ls -l /usr/local/lib/skygate/skygate-apply-policy.sh 2>/dev/null | sed 's/^/  /'
ls -l "$UPDATE_DIR"/policy.request.props 2>/dev/null \
  && echo "  ^ a request is already queued: the path unit fires on start, the applier will consume it" \
  || echo "  no queued policy request (nothing to flush)"

cat <<EOT

install-policy-helper: done.
  Next: let the tag reconciler run (it ticks every 5 min, or restart skygate),
  then check:
      journalctl -u skygate -n 100 | grep tag-reconcile     # expect failed=0
      headscale nodes list | grep -E 'workpc|laptop|homepc' # Tags column populated
      grep -c 'tag:dev-' /etc/headscale/policy.hujson       # tagOwners entries now present
  If the reconciler still fails, the journal line names the reason
  (see docs/troubleshooting.md 8.0.4).
EOT
