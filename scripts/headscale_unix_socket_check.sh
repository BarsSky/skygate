#!/usr/bin/env bash
# headscale_unix_socket_check.sh — regression guard for the 2026-09-18 fix.
#
# NOTE ON THE FILENAME: this is deliberately NOT named check_*.sh.
# .gitignore:205 has a bare `check_*.sh` pattern that re-ignores
# scripts/check_*.sh even though .gitignore:93 negates it (last match wins
# in gitignore), so a new scripts/check_*.sh cannot be committed without
# `git add -f`. See docs/plans/2026-09-17-sqlite-pg-and-headscale-hardening.md
# section 12.7.
#
# BACKGROUND
# ----------
# The rendered headscale config only set `grpc_listen_addr`. Without an
# explicit `unix_socket`, the headscale SERVER does not create the unix
# socket while the in-container CLI still dials the default path, so every
# `docker exec <ctr> headscale <cmd>` call fails with:
#
#   connecting to /var/run/headscale/headscale.sock: context deadline exceeded
#
# The CLI has no --socket/--unix-socket flag (only -c/--config), so the
# socket MUST be declared in the config file. Impact of the omission:
# approve-routes, node tag/untag, ACL file-mode fallback, preauth CLI
# fallback and user delete were ALL silently dead
# (internal/headscale/routes.go:86, tags.go:48/92, acl.go:107/157,
#  preauth.go:130/203, users.go:152/210, nodes.go:358).
#
# This script pins the two template sources so the line cannot be dropped
# again. It is intentionally offline (grep only) so it runs anywhere.
#
# Usage:  bash scripts/headscale_unix_socket_check.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TPL="$REPO_ROOT/deploy/templates/headscale-config.yaml.tmpl"
BOOT="$REPO_ROOT/deploy/headscale-users/headscale-bootstrap.sh"

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }
ok()   { echo "ok  : $*"; }

echo "=== A. main headscale config template declares unix_socket ==="
if [ ! -f "$TPL" ]; then
  fail "missing $TPL"
else
  grep -qE '^unix_socket:[[:space:]]+/var/run/headscale/headscale\.sock' "$TPL" \
    && ok "unix_socket line present" \
    || fail "unix_socket line missing from headscale-config.yaml.tmpl"
  grep -qE '^unix_socket_permission:' "$TPL" \
    && ok "unix_socket_permission present" \
    || fail "unix_socket_permission missing from headscale-config.yaml.tmpl"
fi

echo
echo "=== B. per-user control-plane bootstrap declares unix_socket ==="
if [ ! -f "$BOOT" ]; then
  fail "missing $BOOT"
else
  grep -qE '^unix_socket:[[:space:]]+/var/run/headscale/headscale\.sock' "$BOOT" \
    && ok "unix_socket line present" \
    || fail "unix_socket line missing from headscale-bootstrap.sh"
fi

echo
echo "=== C. grpc_listen_addr is NOT relied on alone ==="
# Cheap sanity: if the template has a gRPC block it must also carry the socket.
if [ -f "$TPL" ]; then
  if grep -qE '^grpc_listen_addr:' "$TPL" && ! grep -qE '^unix_socket:' "$TPL"; then
    fail "template sets grpc_listen_addr but no unix_socket (the exact 2026-09-18 bug)"
  else
    ok "grpc_listen_addr + unix_socket are consistent"
  fi
fi

echo
echo "=== D. runtime note (manual, live VM only) ==="
cat <<'NOTE'
Not checked here (needs the live host):
  docker exec headscale /ko-app/headscale nodes list
If this returns "connecting to /var/run/headscale/headscale.sock: context
deadline exceeded", the RENDERED config on the VM is stale — re-render it
from the template and `docker restart headscale`.
NOTE

echo
if [ "$FAILED" -eq 0 ]; then
  echo "RESULT: PASS — headscale unix_socket contracts hold"
  exit 0
fi
echo "RESULT: FAIL — see above" >&2
exit 1
