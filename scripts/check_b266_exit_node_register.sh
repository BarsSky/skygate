#!/usr/bin/env bash
# check_b266_exit_node_register.sh
#
# 2026-09-19 (B266) — "register a new exit node from the panel" + the
# ssh_target option-injection fix.
#
# WHY
#
# Onboarding a relay had one step that no page could do: mint a pre-auth
# key carrying `tag:exit-node`. The generated ACL declares
#
#     tagOwners: { "tag:exit-node": ["infra@<baseDomain>"] }
#
# so headscale REJECTS the tag when the key belongs to any other user —
# the key must be issued as the technical `infra` user. The operator had
# to leave the panel, find the infra headscale user id and create the key
# by hand.
#
# During the same audit a security defect was confirmed:
# `exit_servers.ssh_target` is operator-writable (POST
# /admin/exit-nodes/add) and was appended POSITIONALLY to the ssh argv in
# internal/headscale/routes.go (`sshArgs = append(sshArgs, host, cmd)`),
# so a value beginning with `-` was parsed by ssh as an OPTION. The
# skygate container holds /var/run/docker.sock and NET_ADMIN+SYS_ADMIN,
# so `-oProxyCommand=<cmd>` was arbitrary command execution inside it,
# reachable from any admin session. B266 adds a strict shape gate,
# `--` before the host, and `-o ProxyCommand=none -o IdentitiesOnly=yes`.
#
# CONTRACTS
#   A  handler + route exist, admin-only
#   B  the key is minted as the infra user WITH the exit-node tag
#   C  the key never lands in a redirect URL (single-render, token-based)
#   D  node-name validator + command builder are pure and tested
#   E  ssh_target shape gate + argv hardening in routes.go
#   F  the /admin/exit-nodes form validates at write time
#   G  i18n keys are RU+EN paired, template renders the block
#   H  go build / go vet / focused go test

set -uo pipefail
if ! command -v go >/dev/null 2>&1; then
  for cand in '/mnt/c/Program Files/Go/bin' '/c/Program Files/Go/bin'; do
    if [ -f "$cand/go.exe" ] && "$cand/go.exe" version >/dev/null 2>&1; then
      export PATH="$cand:$PATH"; break
    fi
  done
fi
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

REG="internal/feature/admin/exit_node_register.go"
REG_TEST="internal/feature/admin/exit_node_register_b266_test.go"
ROUTES="internal/headscale/routes.go"
ROUTES_TEST="internal/headscale/routes_b266_test.go"
EXIT="internal/feature/admin/exit_nodes.go"
# B292: the shared SSH-key preflight (empty / not absolute / missing / unreadable).
SSHKEY="internal/headscale/ssh_key.go"
TPL="internal/handlers/templates/admin/exit_nodes.html"
I18N="internal/i18n/catalog_exit_nodes.go"

hdr "B266 — exit-node registration from the panel + ssh_target injection fix"

# --- A: handler, route, file ---
if [ -f "$REG" ] \
   && grep -q 'func (s \*Service) PostAdminExitNodeRegister' "$REG" \
   && grep -q 'exit-nodes/register' cmd/skygate/main.go \
   && grep -q 'PostAdminExitNodeRegister' cmd/skygate/main.go; then
  ok "A: PostAdminExitNodeRegister + POST /admin/exit-nodes/register wired"
else
  bad "A: handler or route missing (register flow cannot be reached)"
fi
if grep -q 'if c == nil || !c.IsAdmin' "$REG"; then
  ok "A2: handler is admin-only"
else
  bad "A2: handler is missing the admin gate"
fi

# --- B: key minted as infra WITH the exit-node tag ---
if grep -q 's.infraHeadscaleUserID(r.Context())' "$REG" \
   && grep -q 'CreatePreauthKeyWithTags(infraID' "$REG" \
   && grep -q '"tag:exit-node"' "$REG"; then
  ok "B: key is minted for the infra headscale user with tag:exit-node"
else
  bad "B: the key is not minted as infra with tag:exit-node (headscale will reject the tag)"
fi
if grep -q 'exitNodeRegisterMaxTTL' "$REG" && grep -q '24 \* time.Hour' "$REG"; then
  ok "B2: key lifetime is bounded (max 24h)"
else
  bad "B2: no TTL bound on the pre-auth key"
fi

# --- C: the key must never ride in a URL ---
if grep -q 'registered="+url.QueryEscape(token)' "$REG" \
   && grep -q 'exitNodeKeySettingPrefix' "$REG" \
   && grep -q 'consumeExitNodeRegisterKey' "$REG"; then
  ok "C: the key is parked under an opaque token and consumed once"
else
  bad "C: the key is not token-parked (risk: key in URL/access logs)"
fi
if grep -qE 'authkey=.*\+.*pk\.Key|authkey="\+pk\.Key' "$REG" && ! grep -q '--authkey=' "$REG"; then
  bad "C2: suspicious key interpolation into a redirect"
else
  ok "C2: no key interpolation into the redirect target"
fi

# --- D: pure helpers + tests ---
if grep -q 'func isSafeNodeName' "$REG" && grep -q 'func exitNodeRegisterCommand' "$REG"; then
  ok "D: isSafeNodeName + exitNodeRegisterCommand are present"
else
  bad "D: pure helpers missing"
fi
[ -f "$REG_TEST" ] && ok "D2: $REG_TEST present" || bad "D2: $REG_TEST missing"

# --- E: ssh_target shape gate + argv hardening ---
if grep -q 'func IsSafeSSHTarget' "$ROUTES" \
   && grep -q 'sshTargetRe' "$ROUTES" \
   && grep -q 'IsSafeSSHTarget(target)' "$ROUTES"; then
  ok "E: routes.go refuses an unsafe ssh_target"
else
  bad "E: routes.go has no ssh_target shape gate (option injection stays open)"
fi
if grep -q '"ProxyCommand=none"' "$ROUTES" \
   && grep -q '"IdentitiesOnly=yes"' "$ROUTES" \
   && grep -q 'sshArgs = append(sshArgs, "--", host, cmd)' "$ROUTES"; then
  ok "E2: argv hardened (-- before host, ProxyCommand=none, IdentitiesOnly)"
else
  bad "E2: argv hardening missing — a stored target starting with - is still an ssh option"
fi
# B292 contract renegotiation (2026-09-23): the absoluteness check moved out of
# routes.go into internal/headscale/ssh_key.go, where SSHKeyProblem performs the
# empty/absolute/exists/readable preflight in one place and the caller renders
# the same verdict on /admin/exit-nodes. The property is unchanged — a relative
# key path is still refused — so the assertion follows the implementation.
if grep -q 'filepath.IsAbs(keyPath)' "$ROUTES" || grep -q 'SSHKeyProblem(keyPath)' "$ROUTES"; then
  if grep -q 'func sshKeyPathIsAbs(' "$SSHKEY" && grep -q 'not absolute' "$SSHKEY"; then
    ok "E3: key path must be absolute (checked by the shared SSH key preflight)"
  else
    bad "E3: the absoluteness check disappeared with the refactor"
  fi
else
  bad "E3: relative key paths are still accepted"
fi
[ -f "$ROUTES_TEST" ] && ok "E4: $ROUTES_TEST present" || bad "E4: $ROUTES_TEST missing"
if grep -q 'oProxyCommand' "$ROUTES_TEST"; then
  ok "E5: the injection case is pinned by a test"
else
  bad "E5: no test pins the -oProxyCommand case"
fi

# --- F: write-time validation on the add form ---
if grep -q 'IsSafeSSHTarget(sshTarget)' "$EXIT" \
   && grep -q 'ssh_key_path должен быть абсолютным' "$EXIT"; then
  ok "F: /admin/exit-nodes/add validates ssh_target + key path at write time"
else
  bad "F: the add form does not validate the SSH fields"
fi

# --- G: i18n + template ---
for k in exit_nodes.register_title exit_nodes.register_btn exit_nodes.register_ok exit_nodes.register_hostname exit_nodes.register_err_hostname_charset exit_nodes.tutorial_step2_keyhint; do
  n=$(grep -c "\"$k\" *:" "$I18N")
  if [ "$n" -ge 2 ]; then
    ok "G: i18n $k in both catalogues"
  else
    bad "G: i18n $k defined $n time(s) — RU and EN must both have it"
  fi
done
if grep -q '/admin/exit-nodes/register' "$TPL" && grep -q 'RegisterKey.Command' "$TPL"; then
  ok "G2: template renders the register form + the one-time command"
else
  bad "G2: template does not render the register block"
fi

# --- H: build / vet / focused tests ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then ok "H: go build ./... clean"; else bad "H: go build failed"; fi
  if go vet ./internal/feature/admin/... ./internal/headscale/... >/dev/null 2>&1; then ok "H2: go vet clean"; else bad "H2: go vet reported issues"; fi
  if go test ./internal/headscale/ -run 'IsSafeSSHTarget' -count=1 >/dev/null 2>&1; then
    ok "H3: IsSafeSSHTarget tests pass"
  else
    bad "H3: IsSafeSSHTarget tests failed"
  fi
  if go test ./internal/feature/admin/ -run 'IsSafeNodeName|ExitNodeRegisterCommand' -count=1 >/dev/null 2>&1; then
    ok "H4: node-name + command-builder tests pass"
  else
    bad "H4: pure-helper tests failed"
  fi
  if go test ./internal/i18n/ -run TestCatalogsParity -count=1 >/dev/null 2>&1; then
    ok "H5: i18n parity holds"
  else
    bad "H5: i18n parity broken"
  fi
else
  skip "H: go not reachable in this bash PATH"
fi

printf '\n\033[1mB266 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
