#!/usr/bin/env bash
# check_b292_exit_ssh_truth.sh
#
# 2026-09-23 (B292) — «он даёт ошибку при пересинхронизации».
#
# LIVE REPORT (operator, screenshot of /admin/exit-nodes):
#
#   Sync exit-node-vps: ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519):
#     Warning: Identity file /ssh-sync/id_ed25519 not accessible: No such file or
#     directory.
#     ssh: Could not resolve hostname exit-node-vps: Name or service not known
#     approved=21
#
# …rendered in the GREEN flash box, on a page whose one relay reported
# «1/1 здоровых», «2 маршрутов» advertised and «mismatch: have 2, want 19».
#
# THREE defects hide in that single line:
#
#   A. `/ssh-sync/id_ed25519` is the CONTAINER default (docker-compose binds the
#      operator's ~/.ssh there). The native `aro` install can never have that
#      path, and nothing said so — the raw ssh warning became the entire
#      operator-facing explanation.
#   B. `Could not resolve hostname exit-node-vps` means the SSH target was the
#      bare node NAME: exit_servers had neither ssh_target nor tailscale_ip. The
#      row's tailscale_ip stays empty forever because the discovery pass writes
#      INSERT OR IGNORE — while /admin/exit-nodes displayed the relay's Tailscale
#      IP (100.64.0.1) read from headscale. Page and sync disagreed, and the
#      "Use Tailscale IP" button resolves through the same empty chain, so the
#      operator had NO in-UI way out.
#   C. The per-row Re-sync handler redirected with ?ok= unconditionally, so a
#      failed SSH sync rendered as success.
#
# CONTRACTS
#   A. the SSH key preflight exists and names every blocker (+ the container default)
#   B. SetAdvertisedRoutes refuses before spawning ssh, and names the target source
#   C. the native default key path is a path that CAN exist there
#   D. the sync resolves the relay from the live headscale view and repairs the row
#   E. the per-row Re-sync flash tells the truth (ssh=err= → err=)
#   F. the page shows the key verdict per row + a banner with the fix
#   G. the installers create <data_dir>/ssh
#   H. the regression tests exist and pass; the script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B292: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

KEY=internal/headscale/ssh_key.go
ROUTES=internal/headscale/routes.go
CFG=internal/config/config.go
SYNC=internal/feature/exit_rules/sync.go
DB=internal/db/exit_servers.go
EXIT=internal/feature/admin/exit_nodes.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go
KEYTEST=internal/headscale/ssh_key_b292_test.go
SYNCTEST=internal/feature/exit_rules/sync_b292_test.go
DBTEST=internal/db/exit_servers_b292_test.go
ADMINTEST=internal/feature/admin/exit_nodes_b292_test.go

hdr "B292 — the exit-node SSH sync must name its blocker"

# --- A: the shared preflight --------------------------------------------------
if [ -f "$KEY" ]; then ok "A1: $KEY exists"; else bad "A1: $KEY is missing"; fi
for fn in SSHKeyProblem SSHKeyFixHint SSHKeyState SSHKeyStateNeedsOperator; do
  if grep -q "^func $fn(" "$KEY"; then ok "A2.$fn: $fn exists"; else bad "A2.$fn: $fn is missing"; fi
done
for verdict in "no ssh key configured" "ssh key not found" "ssh key not readable" "is a directory" "not absolute"; do
  if grep -q "$verdict" "$KEY"; then ok "A3: the '${verdict}' verdict is named"; else bad "A3: the '${verdict}' verdict is not named"; fi
done
if grep -q 'CONTAINER default' "$KEY" && grep -q 'ContainerDefaultSSHKey' "$KEY"; then
  ok "A4: the container-only default is explained, not just rejected"
else
  bad "A4: nothing explains that /ssh-sync/id_ed25519 is a container path"
fi
if grep -q 'func sshKeyPathIsAbs(' "$KEY" && grep -q 'strings.HasPrefix(p, "/")' "$KEY"; then
  ok "A5: absoluteness is POSIX-aware (filepath.IsAbs calls the container default relative on Windows)"
else
  bad "A5: the absolute-path check is platform-blind"
fi
if grep -q 'authorized_keys' "$KEY" && grep -q 'ssh_key_path' "$KEY" && grep -q 'SKYGATE_EXIT_SSH_KEY' "$KEY"; then
  ok "A6: the hint names both fields AND where the public key must go"
else
  bad "A6: the fix hint is incomplete"
fi

# --- B: refuse before spawning ssh -------------------------------------------
if grep -q 'SSHKeyProblem(keyPath)' "$ROUTES"; then
  ok "B1: SetAdvertisedRoutes preflights the key before building the ssh argv"
else
  bad "B1: SetAdvertisedRoutes still discovers a bad key from ssh stderr"
fi
if grep -q 'targetSource' "$ROUTES" && grep -q 'the node name (exit_servers has neither ssh_target nor tailscale_ip' "$ROUTES"; then
  ok "B2: a failed ssh reports WHERE the target came from"
else
  bad "B2: 'Could not resolve hostname' still has no explanation"
fi
if grep -q 'target from %s, key %s' "$ROUTES"; then
  ok "B3: the ssh error carries the target source (and the key path) in the message"
else
  bad "B3: the ssh error dropped the target source"
fi

# --- C: the default must be able to exist on this install kind ---------------
if grep -q 'func resolveExitSSHKeyPath(' "$CFG" && grep -q 'runningInContainer()' "$CFG"; then
  ok "C1: the SSH key default is resolved per install kind"
else
  bad "C1: the default is still unconditional"
fi
if grep -q 'filepath.Join(nativeDataDir(dsn), "ssh", "id_ed25519")' "$CFG"; then
  ok "C2: a native install defaults to <data dir>/ssh/id_ed25519"
else
  bad "C2: the native default is not anchored to the data dir"
fi
if grep -q 'func nativeDataDir(' "$CFG" && grep -q 'return filepath.Join(nativeDataDir(dsn), "oidc-keys")' "$CFG"; then
  ok "C3: the OIDC key dir (B270) and the SSH key dir share one anchor"
else
  bad "C3: the data-dir anchor is duplicated instead of shared"
fi
if grep -q 'headscaleContainerSSHKey = "/ssh-sync/id_ed25519"' "$CFG"; then
  ok "C4: the container default is unchanged for container installs"
else
  bad "C4: the container default changed (docker installs would break)"
fi

# --- D: resolve the relay from the live view ---------------------------------
if grep -q 'func liveExitNodeIP(' "$SYNC" && grep -q 'hs.ListAllNodes()' "$SYNC"; then
  ok "D1: the sync consults the live headscale view for the relay address"
else
  bad "D1: the sync still trusts only exit_servers.tailscale_ip"
fi
if grep -q 'db.SetExitServerTailscaleIPIfEmpty(d, node, ip)' "$SYNC"; then
  ok "D2: the resolved address is persisted so the next pass (and the button) works"
else
  bad "D2: the row is never repaired"
fi
if grep -q 'func SetExitServerTailscaleIPIfEmpty(' "$DB" && grep -q "TRIM(tailscale_ip) = ''" "$DB"; then
  ok "D3: the repair only fills an EMPTY column (an operator-set address is never overwritten)"
else
  bad "D3: the repair can clobber an operator-set address"
fi
if grep -q 'func FirstTailscaleIP(' "$DB" && grep -q 'FirstTailscaleIP(tailscaleIP)' "$DB"; then
  ok "D4: one address-picking rule is shared by the resolver and the sync"
else
  bad "D4: the address-picking rule is duplicated"
fi
if grep -q 'ssh will be given the bare node name and will most likely fail on DNS' "$SYNC"; then
  ok "D5: the un-resolvable case is logged instead of silently attempted"
else
  bad "D5: a relay with no address is still attempted silently"
fi

# --- E: the flash tells the truth --------------------------------------------
if grep -q 'func exitSyncFailed(' "$EXIT"; then
  ok "E1: the sync result is classified instead of always reported as success"
else
  bad "E1: no ok/err classification for the per-row sync"
fi
if grep -q 'if exitSyncFailed(msg) {' "$EXIT" && grep -A3 'if exitSyncFailed(msg) {' "$EXIT" | grep -q 'err=' ; then
  ok "E2: a failed SSH sync redirects with err= (red flash), not ok= (green)"
else
  bad "E2: the per-row Re-sync can still render a failure as green"
fi

# --- F: the page shows the verdict -------------------------------------------
if grep -q 'SSHKeyBlocked' "$EXIT" && grep -q 'n.SSHKeyState = headscale.SSHKeyState(' "$EXIT"; then
  ok "F1: the page computes the key verdict per row + the blocked count"
else
  bad "F1: the page has no SSH key verdict"
fi
if grep -q 'SSHKeyNote' "$EXIT"; then
  ok "F2: the reason and the fix travel to the template"
else
  bad "F2: the page cannot say WHY the key is unusable"
fi
if grep -q '{{if .SSHKeyBlocked}}' "$TMPL" && grep -q '{{if ne .SSHKeyState "ok"}}' "$TMPL"; then
  ok "F3: the template renders the banner and the per-row badge"
else
  bad "F3: the template does not render the SSH key state"
fi
RU=$(grep -c '"exit_nodes.ssh_key.banner"' "$I18N" || true)
EN=$(grep -c '"exit_nodes.ssh_key.badge"' "$I18N" || true)
if [ "${RU:-0}" -ge 2 ] && [ "${EN:-0}" -ge 2 ]; then
  ok "F4: the new i18n keys exist in RU and EN"
else
  bad "F4: i18n keys missing (banner=${RU:-0}, badge=${EN:-0}; want 2 each)"
fi

# --- G: the installers create the key directory ------------------------------
if grep -q '"\$data_dir/ssh"' deploy/install-common.sh; then
  ok "G1: install-common.sh creates <data_dir>/ssh"
else
  bad "G1: install-common.sh does not create <data_dir>/ssh"
fi
if grep -q '"\$SKYGATE_DATA_DIR/ssh"' deploy/install-alpine.sh; then
  ok "G2: install-alpine.sh creates <data_dir>/ssh"
else
  bad "G2: install-alpine.sh does not create <data_dir>/ssh"
fi

# --- H: tests + git -----------------------------------------------------------
for t in "$KEYTEST" "$SYNCTEST" "$DBTEST" "$ADMINTEST"; do
  if [ -f "$t" ]; then ok "H1: $t exists"; else bad "H1: $t is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ ./internal/db/ ./internal/feature/exit_rules/ ./internal/feature/admin/ -run 'B292|SSHKey|LiveExitNodeIP|FirstTailscaleIP|SetExitServerTailscaleIPIfEmpty|LookupExitServerSSHTarget_B292|EffectiveExitSSHKeyPath|ExitSyncFailed|ExitNodesPageRendersSSHKeyWarning|ConfigSSHKeyPath' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "H2: the B292 tests pass"
  else
    bad "H2: the B292 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "H2: go not on PATH — run the B292 tests on the VM"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ -run 'TestConfigSSHKeyPath_DefaultIsPerInstallKind' -count=1 -v 2>&1)"
  if grep -q -- '--- PASS' <<< "$OUT"; then
    ok "H3: the SSH key default test passes (container vs native branch)"
  else
    bad "H3: the config default test failed:"
    printf '%s\n' "$OUT" | tail -15 | sed 's/^/       /' >&2
  fi
else
  skip "H3: go not on PATH"
fi
if git ls-files --error-unmatch scripts/check_b292_exit_ssh_truth.sh >/dev/null 2>&1; then
  ok "H4: scripts/check_b292_exit_ssh_truth.sh is tracked by git"
else
  bad "H4: scripts/check_b292_exit_ssh_truth.sh is NOT tracked"
fi

printf '\n\033[1mB292 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
