#!/bin/bash
# check_b164.sh — DERP server init on a new host
# (B164, v1.5.1)
#
# Operator 2026-08-24 UX report: "отсутствует
# возможность инициализации нового DERP сервера
# администратором на новом хосте, параметры
# которого укажет администратор (доступ по ssh
# для автонастройки по кнопке как и exit node),
# а также регистрация его в списках". The
# pre-B164 /admin/derp/relays page only supported
# adding EXISTING relays (paste hostname + region
# metadata). B164 adds the "Initialize on a new
# host" flow: admin fills the form, skygate
# SSHes to the target, runs deploy/derp-init.sh,
# and the relay is registered in derp_relays.
#
# B164 (this file) pins the fix:
#  1. /admin/derp/relays/init page + handler
#  2. Form fields (hostname + public_ip + region_*
#     + ssh_* + derp_port + stun_port + sort_order)
#  3. POST handler that runs the deploy script
#  4. Deploy script deploy/derp-init.sh that
#     installs derper on the remote host
#  5. New i18n keys (RU + EN) for the form
#  6. Suggested region_id + sort_order pre-fill
#  7. Audit log on both success + failure

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: handler exists in admin/derp_init.go ==="
if grep -qE 'func \(s \*Service\) GetAdminDerpRelaysInit' internal/feature/admin/derp_init.go; then
    ok "GetAdminDerpRelaysInit handler defined on *Service"
else
    bad "GetAdminDerpRelaysInit handler MISSING"
fi
if grep -qE 'func \(s \*Service\) PostAdminDerpRelaysInit' internal/feature/admin/derp_init.go; then
    ok "PostAdminDerpRelaysInit handler defined on *Service"
else
    bad "PostAdminDerpRelaysInit handler MISSING"
fi

echo ""
echo "=== contract B: route registered in main.go ==="
if grep -qE 'mux\.Handle\("GET /admin/derp/relays/init"' cmd/skygate/main.go; then
    ok "GET /admin/derp/relays/init route registered"
else
    bad "GET /admin/derp/relays/init route MISSING"
fi
if grep -qE 'mux\.Handle\("POST /admin/derp/relays/init"' cmd/skygate/main.go; then
    ok "POST /admin/derp/relays/init route registered"
else
    bad "POST /admin/derp/relays/init route MISSING"
fi
# Both must be behind authMW (admin-only surface).
if grep -qE '/admin/derp/relays/init".*authMW' cmd/skygate/main.go; then
    ok "/admin/derp/relays/init routes are behind authMW"
else
    bad "/admin/derp/relays/init NOT behind authMW (security regression)"
fi

echo ""
echo "=== contract C: template exists + has the form fields ==="
if [ -f internal/handlers/templates/admin/derp_relays_init.html ]; then
    ok "admin/derp_relays_init.html exists"
else
    bad "admin/derp_relays_init.html MISSING"
fi
# Form fields: hostname, region_id, region_code, region_name,
# ssh_user, ssh_target, ssh_key_path, ssh_port, derp_port,
# stun_port, sort_order, notes.
for field in hostname region_id region_code region_name ssh_user ssh_target ssh_key_path ssh_port derp_port stun_port sort_order; do
    if grep -qE "name=\"$field\"" internal/handlers/templates/admin/derp_relays_init.html; then
        ok "form has field '$field'"
    else
        bad "form MISSING field '$field'"
    fi
done

echo ""
echo "=== contract D: handler validates + runs the script ==="
# Form validation: region_id range, ssh_target
# format (must contain '@'), required fields.
if grep -qE 'regionID < 1 \|\| regionID > 999' internal/feature/admin/derp_init.go; then
    ok "PostAdminDerpRelaysInit validates region_id range (1-999)"
else
    bad "PostAdminDerpRelaysInit: region_id range check MISSING"
fi
if grep -qE '!strings\.Contains\(sshTarget, "@"\)' internal/feature/admin/derp_init.go; then
    ok "PostAdminDerpInitInit validates ssh_target has '@' (user@host)"
else
    bad "PostAdminDerpInitInit: ssh_target '@' check MISSING"
fi
# Must call headscale.RunScript (the same primitive
# the headscale provisioner uses).
if grep -qE 'headscale\.RunScript' internal/feature/admin/derp_init.go; then
    ok "PostAdminDerpRelaysInit uses headscale.RunScript"
else
    bad "PostAdminDerpRelaysInit: headscale.RunScript call MISSING"
fi
# The script path constant is referenced.
if grep -qE 'DerpInitScriptPath' internal/feature/admin/derp_init.go; then
    ok "PostAdminDerpRelaysInit references DerpInitScriptPath"
else
    bad "PostAdminDerpRelaysInit: DerpInitScriptPath MISSING"
fi

echo ""
echo "=== contract E: handler inserts into derp_relays + audit log ==="
# After the script succeeds, the handler must
# insert a derp_relays row (so the new relay is
# registered in the live policy on the next
# /admin/exit-rules/reapply).
if grep -qE 'db\.AddDerpRelay\(s\.DB, row\)' internal/feature/admin/derp_init.go; then
    ok "PostAdminDerpRelaysInit inserts into derp_relays"
else
    bad "PostAdminDerpRelaysInit: derp_relays insert MISSING (relay would be installed but not registered)"
fi
# Audit log on success + failure.
if grep -qE 'derp_init\.ok' internal/feature/admin/derp_init.go && \
   grep -qE 'derp_init\.fail' internal/feature/admin/derp_init.go; then
    ok "PostAdminDerpRelaysInit writes 'derp_init.ok' + 'derp_init.fail' audit rows"
else
    bad "PostAdminDerpRelaysInit: audit log on success/failure MISSING"
fi

echo ""
echo "=== contract F: deploy script exists + is executable + has the expected flow ==="
if [ -x deploy/derp-init.sh ]; then
    ok "deploy/derp-init.sh exists and is executable"
else
    bad "deploy/derp-init.sh MISSING or not executable (chmod +x deploy/derp-init.sh)"
fi
# The script must install Go + derper + systemd + open
# firewall + start the service. 7-step flow.
for step in "go install tailscale.com" "systemd" "derper.service" "firewall" "systemctl restart derper" "cert" "DERP_PORT"; do
    if grep -qE "$step" deploy/derp-init.sh; then
        ok "deploy/derp-init.sh references '$step'"
    else
        bad "deploy/derp-init.sh: '$step' step MISSING"
    fi
done
# The script must emit a complete JSON object on stdout.
if grep -qE '"hostname".*"derp_port".*"stun_port".*"duration_ms"' deploy/derp-init.sh; then
    ok "deploy/derp-init.sh emits a JSON object with all required fields"
else
    bad "deploy/derp-init.sh: JSON output shape MISSING or incomplete"
fi

echo ""
echo "=== contract G: i18n keys (RU + EN) ==="
needed=(
  "derp_init.title"
  "derp_init.subtitle"
  "derp_init.hostname"
  "derp_init.region_id"
  "derp_init.region_code"
  "derp_init.region_name"
  "derp_init.ssh_user"
  "derp_init.ssh_target"
  "derp_init.ssh_key_path"
  "derp_init.derp_port"
  "derp_init.stun_port"
  "derp_init.sort_order"
  "derp_init.submit"
  "derp_init.warning"
)
for k in "${needed[@]}"; do
    c=$(grep -cE "\"$k\"" internal/i18n/catalog_derp.go 2>/dev/null || true)
    c=${c:-0}
    if [ "$c" -ge 2 ]; then
        ok "i18n key '$k' present in both RU and EN"
    else
        bad "i18n key '$k' MISSING in catalog_derp.go (found $c entries — need 2 for RU+EN)"
    fi
done

echo ""
echo "=== contract H: build + vet clean ==="
out=$(go build ./... 2>&1)
if [ -z "$out" ]; then
    ok "go build ./... clean"
else
    bad "go build output: $out"
fi
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet output: $out"
fi

echo ""
echo "=== summary ==="
echo "B164: DERP server init on a new host (SSH-based auto-config)"
echo "all contracts satisfied"
