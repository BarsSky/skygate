#!/usr/bin/env bash
# check_b412_exit_node_provision.sh
#
# 2026-10-23 (B412) — «зарегистрировать новый exit node» ОДНОЙ кнопкой, из панели,
# без ручных команд на VPS.
#
# OPERATOR REQUEST (verbatim, the reason this block exists):
#
#   «как фича в возможностях skygate должна была быть указана настройка exit node
#    через вебинтерфейс skygate вот ее я и хочу проверить - по сути просто указав
#    необходимые параметры нового vps и нажать кнопку зарегистрировать новый exit
#    node … необходимо сделать удобную форму и не забыть про подсказки на главной
#    странице по формированию нового для администратора. Также при регистрации
#    стоит учитывать в каком виде будет запущен сервис - нативно или в докере»
#
# WHAT WAS MISSING (measured on the page the operator was looking at):
#
#   * «Зарегистрировать новый exit node» (B266) minted a tagged pre-auth key and
#     printed a command — it never touched the machine;
#   * «Добавить exit node» stored ssh_target for a node that ALREADY existed in
#     headscale, so a fresh VPS had no node_id to type in;
#   * the two look-alike blocks sat in the wrong order (the key block BELOW the
#     form that needs it) and neither said which install kind (docker / native)
#     decides the SSH key path.
#
# CONTRACTS
#   A  the new package exists, is pure where it claims to be, and is tested
#   B  the handler + route exist, admin-only, and never put the key in a URL
#   C  the remote script is idempotent, root-gated, marker-based and VALID BASH
#   D  the flow ends where it must: exit_servers row + base-route approval
#   E  install kind (docker vs native) is surfaced, not guessed
#   F  the page renders the ONE form first, the manual path second, RU+EN paired
#   G  the admin MAIN page carries the hint (operator asked for it explicitly)
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
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

PKG="internal/exitnode/provision_b412.go"
PKG_TEST="internal/exitnode/provision_b412_test.go"
HANDLER="internal/feature/admin/exit_nodes_provision.go"
PAGE="internal/feature/admin/exit_nodes_page.go"
TPL="internal/handlers/templates/admin/exit_nodes.html"
DASH="internal/handlers/templates/dashboard.html"
I18N="internal/i18n/catalog_exit_nodes.go"
I18N_MY="internal/i18n/catalog_my.go"

hdr "B412 — provisioning a new exit node from the panel (one button, SSH)"

# --- A: the package -----------------------------------------------------------
if [ -f "$PKG" ] && [ -f "$PKG_TEST" ]; then
  ok "A1: $PKG + its tests exist"
else
  bad "A1: the provisioning package or its tests are missing"
fi
for fn in 'func Validate(r Request) error' 'func BuildProvisionScript(r Request, authKey string) string' \
          'func ParseOutput(out string) Result' 'func SSHArgv(r Request, remoteCommand string) []string' \
          'func Run(ctx context.Context, r Request, script string) (Result, error)'; do
  if grep -qF "$fn" "$PKG"; then
    ok "A2: $fn"
  else
    bad "A2: missing $fn"
  fi
done
# The key MUST travel on stdin, never in the argv: ps on the target (and this
# host's own process list) would otherwise show a live tailnet credential.
if grep -qF 'cmd.Stdin = strings.NewReader(script)' "$PKG"; then
  ok "A3: the remote script (carrying the pre-auth key) is fed on stdin, not argv"
else
  bad "A3: the provisioning script is not passed on stdin (a key could end up in argv/ps)"
fi
if grep -qF 'execCommandContext = exec.CommandContext' "$PKG"; then
  ok "A4: the exec edge is injectable (tests never spawn ssh)"
else
  bad "A4: no injectable exec edge — the tests would have to run ssh for real"
fi
if [ -f "internal/feature/admin/exit_nodes_provision_b412_test.go" ]; then
  ok "A5: the card's own view is pinned by tests (install kind, key verdict, prefill)"
else
  bad "A5: no test covers exitNodeProvisionView"
fi

# --- B: handler + route + key hygiene ----------------------------------------
if grep -qF 'func (s *Service) PostAdminExitNodeProvision(w http.ResponseWriter, r *http.Request)' "$HANDLER" \
   && grep -qF 'if c == nil || !c.IsAdmin' "$HANDLER"; then
  ok "B1: PostAdminExitNodeProvision exists and is admin-only"
else
  bad "B1: the provisioning handler is missing or not admin-gated"
fi
if grep -qF '/admin/exit-nodes/provision' cmd/skygate/*.go >/dev/null 2>&1 \
   && grep -qF 'PostAdminExitNodeProvision' cmd/skygate/*.go >/dev/null 2>&1; then
  ok "B2: POST /admin/exit-nodes/provision is wired to the handler"
else
  bad "B2: the provisioning route is not registered (the form would 404)"
fi
# B266's rule, restated for B412: the minted key never rides in a redirect.
if grep -qE 'redirectProvisionFlash.*pk\.Key|err=.*pk\.Key|authorized_keys.*pk\.Key' "$HANDLER"; then
  bad "B3: the pre-auth key is interpolated into a redirect/audit surface"
else
  ok "B3: the pre-auth key is used only in the SSH payload (never in a URL or flash)"
fi
# Success and failure must not share one query key: a provisioned node flashed
# as red "err" reads like a failure to the operator.
if grep -qF 'q.Set("ok", msg)' "$HANDLER" && grep -qF 'q.Set("err", msg)' "$HANDLER"; then
  ok "B3b: success flashes as ?ok=, failures as ?err= (with the typed values re-filled)"
else
  bad "B3b: the handler has no separate success flash (a good run would render red)"
fi
if grep -qF 'CreatePreauthKeyWithTags(infraID' "$HANDLER" \
   && grep -qF '"tag:exit-node"' "$HANDLER"; then
  ok "B4: the key is minted for infra WITH tag:exit-node (headscale rejects any other owner)"
else
  bad "B4: the pre-auth key is not minted as infra with tag:exit-node"
fi
if grep -qF 'provisionMu.TryLock()' "$HANDLER"; then
  ok "B5: one provisioning run at a time (two ssh runs on one VPS race on iptables)"
else
  bad "B5: no serialisation — two concurrent runs could fight over the same host"
fi
if grep -qF 'exitnode.Validate(req)' "$HANDLER" \
   && grep -qF 'headscale.SSHKeyProblem(keyPath)' "$HANDLER"; then
  ok "B6: every value is validated, and the SSH key is preflighted, BEFORE a process is spawned"
else
  bad "B6: the handler does not validate the request / preflight the key"
fi

# --- C: the generated remote script -----------------------------------------
# The builder composes the text from constants, so the assertions read the Go
# source's ingredients (C3 then pins the composed text with real tests).
for pat in 'StagePreflight' 'is not root' 'net.ipv4.ip_forward=1' \
           '--advertise-exit-node' 'iptables -t nat -C POSTROUTING' \
           'SKYGATE_STAGE' 'SKYGATE_FAIL' 'SKYGATE_RESULT'; do
  if grep -qF -e "$pat" "$PKG"; then
    ok "C1: the script builder carries '$pat'"
  else
    bad "C1: the script builder is missing '$pat'"
  fi
done
if grep -qF 'func shQuote(' "$PKG" \
   && grep -qF "strings.ReplaceAll(s, \"'\", " "$PKG"; then
  ok "C2: shQuote escapes single quotes (second guard behind Validate)"
else
  bad "C2: shQuote does not look like a real escaper"
fi
# Idempotency is ORDER, not presence: the check has to precede the add.
if command -v go >/dev/null 2>&1; then
  if go test ./internal/exitnode/ -run 'TestBuildProvisionScript_B412' -count=1 >/dev/null 2>&1; then
    ok "C3: script shape + options + idempotency order are pinned by tests"
  else
    bad "C3: the script tests fail"
  fi
  if go test ./internal/exitnode/ -run 'TestBuildProvisionScript_B412_IsValidBash' -count=1 -v 2>&1 | grep -q 'SKIP'; then
    skip "C4: no POSIX bash available to syntax-check the generated script"
  elif go test ./internal/exitnode/ -run 'TestBuildProvisionScript_B412_IsValidBash' -count=1 >/dev/null 2>&1; then
    ok "C4: the generated script passes a real 'bash -n'"
  else
    bad "C4: the generated script is not valid bash"
  fi
else
  skip "C3/C4: go not reachable in this bash PATH"
fi

# --- D: the flow ends where it must -----------------------------------------
if grep -qF 'db.UpsertExitServer(s.dbc()' "$HANDLER"; then
  ok "D1: the exit_servers row is written (ssh_target / key / port / accept_routes)"
else
  bad "D1: the handler never creates the exit_servers row — the node would not appear on the page"
fi
if grep -qF 'waitForTailnetNode(hs, nodeName' "$HANDLER"; then
  ok "D2: the handler waits for headscale to see the node (approving earlier is a race)"
else
  bad "D2: no wait for the node — routes would be approved before headscale knows the node"
fi
if grep -qF 's.SyncRoutesForNode(nodeName)' "$HANDLER"; then
  ok "D3: the base routes are advertised/approved through the SAME sync path as Re-sync"
else
  bad "D3: the handler does not run the sync — a provisioned relay would differ from a re-synced one"
fi
if grep -qF 'exit_node_provision' "$HANDLER" && grep -qF 'exit_node_provision_probe' "$HANDLER"; then
  ok "D4: both the real run and the dry run are audited (key never logged)"
else
  bad "D4: provisioning is not audited"
fi

# --- E: install kind ---------------------------------------------------------
if grep -qF 'update.DetectInstallKind()' "$HANDLER" && grep -qF 'InContainer:' "$HANDLER"; then
  ok "E1: the handler detects the install kind instead of assuming docker"
else
  bad "E1: install kind is not detected — the key path hint would be wrong on a native host"
fi
if grep -qF 'exit_nodes.provision.install_kind' "$TPL" \
   && grep -qF 'exit_nodes.provision.key_help' "$TPL"; then
  ok "E2: the page states which install kind is running and where the key comes from"
else
  bad "E2: the page does not say which install kind / key path it will use"
fi

# --- F: the page -------------------------------------------------------------
if grep -qF 'action="/admin/exit-nodes/provision"' "$TPL"; then
  ok "F1: the provisioning form posts to the new handler"
else
  bad "F1: the template has no provisioning form"
fi
for f in host ssh_port ssh_user node_name ssh_key_path accept_routes description \
         install_tailscale setup_nat dry_run; do
  if grep -qF "name=\"$f\"" "$TPL"; then
    ok "F2: the form carries the '$f' field"
  else
    bad "F2: the form is missing the '$f' field"
  fi
done
# The ONE-button card must come BEFORE the manual path: the operator reported
# «на странице много разных мест по регистрации и непонятно какое из них корректное».
PROV_IDX=$(grep -n 'action="/admin/exit-nodes/provision"' "$TPL" | head -n 1 | cut -d: -f1)
MAN_IDX=$(grep -n 'exit_nodes.manual_title' "$TPL" | head -n 1 | cut -d: -f1)
if [ -n "$PROV_IDX" ] && [ -n "$MAN_IDX" ] && [ "$PROV_IDX" -lt "$MAN_IDX" ]; then
  ok "F3: the one-button card is rendered ABOVE the manual path (line $PROV_IDX < $MAN_IDX)"
else
  bad "F3: the manual path still comes first (provision=$PROV_IDX manual=$MAN_IDX)"
fi
if grep -qF 'id="new-exit-node"' "$TPL"; then
  ok "F4: the card has an anchor (#new-exit-node) the main page can link to"
else
  bad "F4: the card has no anchor — the dashboard link would land on the top of the page"
fi
# The manual path is NOT deleted: it is the only option for a host skygate cannot reach.
if grep -qF 'action="/admin/exit-nodes/register"' "$TPL" \
   && grep -qF 'action="/admin/exit-nodes/add"' "$TPL"; then
  ok "F5: the manual half (B266 key + row form) is still there for unreachable hosts"
else
  bad "F5: the manual path was removed — a VPS without SSH from skygate could not be added at all"
fi
for k in exit_nodes.provision.title exit_nodes.provision.submit exit_nodes.provision.blocked \
         exit_nodes.provision.host exit_nodes.provision.node_name exit_nodes.provision.ssh_key \
         exit_nodes.provision.key_help exit_nodes.provision.install_kind exit_nodes.provision.dry_run \
         exit_nodes.manual_title; do
  n=$(grep -c "\"$k\" *:" "$I18N")
  if [ "$n" -ge 2 ]; then
    ok "F6: i18n $k in both catalogues"
  else
    bad "F6: i18n $k defined $n time(s) — RU and EN must both have it"
  fi
done

# --- G: the admin MAIN page hint --------------------------------------------
if grep -qF 'dashboard.new_exit_node_title' "$DASH" \
   && grep -qF '/admin/exit-nodes#new-exit-node' "$DASH"; then
  ok "G1: the admin dashboard carries the hint and links straight to the form"
else
  bad "G1: the main page has no exit-node hint (the operator asked for it explicitly)"
fi
for k in dashboard.new_exit_node_title dashboard.new_exit_node_sub \
         dashboard.new_exit_node_step1 dashboard.new_exit_node_cta; do
  n=$(grep -c "\"$k\" *:" "$I18N_MY")
  if [ "$n" -ge 2 ]; then
    ok "G2: i18n $k in both catalogues"
  else
    bad "G2: i18n $k defined $n time(s) — RU and EN must both have it"
  fi
done
# The hint belongs to the ADMIN branch of the dashboard only.
if awk '/\{\{if \.IsAdmin\}\}/{a=NR} /dashboard.new_exit_node_title/{if (a && NR>a) {print "inside"; exit}}' "$DASH" | grep -q inside; then
  ok "G3: the hint is rendered inside the admin branch (users must not see it)"
else
  bad "G3: the hint is outside {{if .IsAdmin}} — every user would see an admin instruction"
fi

# --- G3b: the SSH KEY ITSELF, not just its path ------------------------------
# Operator, verbatim: «есть указание на путь к файлу но для пользователя это не удобно
# … администратор должен иметь возможность его скопировать и применить по инструкции
# что есть рядом чтобы добавить в vps этот ключ».
if grep -qF 'func (s *Service) sshPublicKeyFor(' "$HANDLER" \
   && grep -qF 'func authorizedKeysCommand(' "$HANDLER"; then
  ok "G4: the handler resolves the PUBLIC half and renders the authorising command"
else
  bad "G4: no public-key surface — the operator still gets only a file path"
fi
if grep -qF '{{.Provision.PubKey}}' "$TPL" \
   && grep -qF '{{.Provision.PubKeyCommand}}' "$TPL" \
   && grep -qF 'copyProvisionKey(' "$TPL"; then
  ok "G5: the card shows the key + the ready command, each with a copy button"
else
  bad "G5: the template does not render a copyable key/command"
fi
# The PRIVATE half must never reach the browser: the view carries a public key and a
# shell one-liner, nothing else.
if grep -qF 'PRIVATE KEY' "$TPL" || grep -qF 'PRIVATE KEY' "$HANDLER"; then
  bad "G6: private-key material is referenced where the page/browser could see it"
else
  ok "G6: no private-key material anywhere in the card or the handler"
fi
# A hardcoded /root/.ssh would silently write to the wrong home for a non-root user.
if grep -qF 'authorized_keys' "$HANDLER" && grep -qF '~/.ssh/authorized_keys' "$HANDLER" \
   && ! grep -qF '/root/.ssh/authorized_keys' "$HANDLER"; then
  ok "G7: the authorising command is user-relative (~/.ssh), so a non-root SSH user works"
else
  bad "G7: the command hardcodes a home directory (breaks the SSH user field)"
fi
if grep -qF 'PubKeyMissing' "$HANDLER" && grep -qF 'ssh-keygen -y -f' "$TPL"; then
  ok "G8: a missing .pub is explained with the exact command that recreates it"
else
  bad "G8: a missing public key would render as an empty box with no next step"
fi
for k in exit_nodes.provision.key_title exit_nodes.provision.key_subtitle \
         exit_nodes.provision.key_copy exit_nodes.provision.key_copy_command \
         exit_nodes.provision.key_source exit_nodes.provision.key_missing \
         exit_nodes.provision.key_same_for_all exit_nodes.provision.key_copied; do
  n=$(grep -c "\"$k\" *:" "$I18N")
  if [ "$n" -ge 2 ]; then
    ok "G9: i18n $k in both catalogues"
  else
    bad "G9: i18n $k defined $n time(s) — RU and EN must both have it"
  fi
done
for tn in TestExitNodeProvisionView_B412_ShowsCopyablePublicKey \
          TestExitNodeProvisionView_B412_MissingPublicKeyIsExplained \
          TestAuthorizedKeysCommand_B412_RefusesNonPublicKey; do
  if grep -qF "func $tn(" internal/feature/admin/exit_nodes_provision_b412_test.go 2>/dev/null; then
    ok "G10: $tn pins the public-key surface"
  else
    bad "G10: $tn is missing — the copyable key would be untested"
  fi
done

# --- H: build / vet / focused tests -----------------------------------------
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then ok "H: go build ./... clean"; else bad "H: go build failed"; fi
  if go vet ./internal/exitnode/ ./internal/feature/admin/ >/dev/null 2>&1; then
    ok "H2: go vet clean"
  else
    bad "H2: go vet reported issues"
  fi
  if go test ./internal/exitnode/ -count=1 >/dev/null 2>&1; then
    ok "H3: the exitnode package tests pass (validators, script, parser, argv)"
  else
    bad "H3: exitnode tests fail"
  fi
  if go test ./internal/i18n/ -run TestCatalogsParity -count=1 >/dev/null 2>&1; then
    ok "H4: i18n parity holds"
  else
    bad "H4: i18n parity broken"
  fi
else
  skip "H: go not reachable in this bash PATH"
fi

printf '\n\033[1mB412 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
