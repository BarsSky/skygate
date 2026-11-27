#!/usr/bin/env bash
# check_b533_headscale_service_control.sh
#
# 2026-11-27 (B533) — «посмотри в чём дело с headscale; надо уметь перезапускать
# сервис из skygate, видеть сводку состояния и шаги решения по этому состоянию».
#
# THE MEASURED INCIDENT. The provider moved the production VM to a new IPv4, the
# zone kept pointing head/skygate/derp at the old one, and every device in the
# tailnet lost its coordination server. headscale itself was healthy; the panel
# answered /healthz on loopback and was unreachable from outside; the only signal
# anywhere said "headscale is down", which named the product instead of the name.
# A restart would have changed nothing, and there was no way to ask the panel to
# do even that.
#
# WHAT THIS CONTRACT PINS
#   A. the privileged applier is DATA-ONLY, allow-listed on BOTH sides, and
#      tracked by git (AGENTS trap #11: a file that only exists on this disk);
#   B. the Go half names the same allow-lists and the mismatch predicate;
#   C. the installer writes the unit pair from the shared writer, and the route
#      and the button exist;
#   D. the remediation that names the INCIDENT (dns_mismatch) exists, and a
#      RESTART is not offered for a healthy service;
#   E. every headscale_service.* i18n key exists in BOTH catalogues.
#
# WHERE IT SKIPS: the live leg (helper installed / unit active) needs the host
# and SKIPs when systemd or the applier is absent — a check must never turn a
# missing dependency into a product verdict.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B533: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

APPLIER=deploy/skygate-apply-service.sh
INSTALLER=deploy/install-service-helper.sh
COMMON=deploy/install-common.sh
GO=internal/headscale/service_control_b533.go
GOT=internal/headscale/service_control_b533_test.go
ADMIN=internal/feature/admin/headscale.go
TPL=internal/handlers/templates/admin/headscale.html
I18N=internal/i18n/catalog_admin.go
# ROUTES is the entrypoint SURFACE, not one file: B528 step 1 (v1.5.265) moved the
# /admin/headscale* group to cmd/skygate/routes_headscale.go and this very contract went
# red for a pure code move — the failure mode scripts/lib/gosurface.sh exists to prevent.
# Reading the package means the route stays covered wherever it lives.
. scripts/lib/gosurface.sh
gosurface ROUTES cmd/skygate/*.go || {
  printf '  \033[31mFAIL\033[0m C: cannot read the cmd/skygate surface (gosurface could not build it)\n' >&2
  printf '\nB533: 0 pass, 1 fail, 0 skip\n'
  exit 1
}

hdr "B533 — headscale service: state, restart from the panel, remediation steps"

# --- A: the privileged applier -------------------------------------------------
A_MISS=""
[ -f "$APPLIER" ] || A_MISS="${A_MISS}${APPLIER} is missing"$'\n'
git ls-files --error-unmatch "$APPLIER" >/dev/null 2>&1 \
  || A_MISS="${A_MISS}${APPLIER} is NOT tracked by git (trap #11: the applier the operator is told to install would be absent from the release)"$'\n'
grep -q 'headscale skygate caddy derper tailscaled' "$APPLIER" \
  || A_MISS="${A_MISS}the applier has no service allow-list"$'\n'
grep -q 'not in the allow-list' "$APPLIER" \
  || A_MISS="${A_MISS}the applier does not refuse a service outside the allow-list"$'\n'
grep -q 'action .*is not in the allow-list' "$APPLIER" \
  || A_MISS="${A_MISS}the applier does not refuse an action outside the allow-list"$'\n'
if grep -qE '^\s*(\.|source)\s+"?\$REQ' "$APPLIER"; then
  A_MISS="${A_MISS}the applier SOURCES the unprivileged request file (it must be parsed as data)"$'\n'
fi
grep -q 'eval ' "$APPLIER" && A_MISS="${A_MISS}the applier calls eval()"$'\n'
grep -q 'mv -f "\$_tmp" "\$STATUS"' "$APPLIER" \
  || A_MISS="${A_MISS}the applier does not publish its verdict atomically (a reader can see half a status file)"$'\n'
grep -q 'rm -f "\$REQ"' "$APPLIER" \
  || A_MISS="${A_MISS}the applier never removes the request — the path unit cannot re-arm"$'\n'
if [ -z "$A_MISS" ]; then ok "A: ${APPLIER} is data-only, allow-listed, atomic and tracked by git"
else while IFS= read -r l; do [ -n "$l" ] && bad "A: $l"; done <<< "$A_MISS"; fi

# --- B: the Go half ------------------------------------------------------------
B_MISS=""
[ -f "$GO" ] || B_MISS="${B_MISS}${GO} is missing"$'\n'
grep -q 'ServiceAllowedServices = \[\]string{"headscale", "skygate", "caddy", "derper", "tailscaled"}' "$GO" \
  || B_MISS="${B_MISS}the Go allow-list of services is not the documented one"$'\n'
grep -q 'ServiceAllowedActions = \[\]string{"restart", "status"}' "$GO" \
  || B_MISS="${B_MISS}the Go allow-list of actions is not restart|status"$'\n'
grep -q 'func ServiceRequestValid' "$GO" \
  || B_MISS="${B_MISS}ServiceRequestValid is missing: the request would reach the applier unvalidated"$'\n'
grep -q 'if !ServiceHelperArmed()' "$GO" \
  || B_MISS="${B_MISS}RequestServiceAction does not refuse when the helper is not installed"$'\n'
grep -q 'func ControlNamePointsElsewhere' "$GO" \
  || B_MISS="${B_MISS}the name/address mismatch predicate is missing — this is the 2026-11-27 incident made visible"$'\n'
if [ -z "$B_MISS" ]; then ok "B: the Go half validates the same allow-lists and measures the name mismatch"
else while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"; fi

# --- C: wiring (installer, route, page) ----------------------------------------
C_MISS=""
grep -q 'write_service_units()' "$COMMON" \
  || C_MISS="${C_MISS}write_service_units is not defined in ${COMMON}"$'\n'
grep -q 'write_service_units "\$update_dir"' "$COMMON" \
  || C_MISS="${C_MISS}write_service_units is never CALLED by the shared install tail (the unit pair would never be written)"$'\n'
grep -q 'PathExists=\${update_dir}/service.request.props' "$COMMON" \
  || C_MISS="${C_MISS}the path unit does not watch service.request.props"$'\n'
[ -f "$INSTALLER" ] || C_MISS="${C_MISS}${INSTALLER} is missing (an existing install could not add the helper)"$'\n'
grep -q 'write_service_units "\$UPDATE_DIR"' "$INSTALLER" \
  || C_MISS="${C_MISS}${INSTALLER} does not reuse the shared writer"$'\n'
grep -q 'systemctl enable --now skygate-service.path' "$INSTALLER" \
  || C_MISS="${C_MISS}${INSTALLER} does not arm skygate-service.path"$'\n'
grep -q 'POST /admin/headscale/service/restart' "$ROUTES" \
  || C_MISS="${C_MISS}the restart route is not registered"$'\n'
grep -q 'func (s \*Service) PostAdminHeadscaleServiceRestart' "$ADMIN" \
  || C_MISS="${C_MISS}the restart handler is missing"$'\n'
grep -q 'RequestServiceAction("restart", "headscale")' "$ADMIN" \
  || C_MISS="${C_MISS}the handler does not stage the allow-listed request"$'\n'
grep -q 'headscale.restart_queued' "$ADMIN" \
  || C_MISS="${C_MISS}the restart is not audited"$'\n'
grep -q 'action="/admin/headscale/service/restart"' "$TPL" \
  || C_MISS="${C_MISS}the page has no restart form pointing at the route"$'\n'
grep -q '{{if not .RestartBlock}}' "$TPL" \
  || C_MISS="${C_MISS}the restart button is not hidden when the helper is absent or the service is not local (it would promise what it cannot do)"$'\n'
grep -q 'headscale_service.name_mismatch' "$TPL" \
  || C_MISS="${C_MISS}the page does not surface the name mismatch"$'\n'
if [ -z "$C_MISS" ]; then ok "C: installer, unit pair, route, handler, audit and page are wired"
else while IFS= read -r l; do [ -n "$l" ] && bad "C: $l"; done <<< "$C_MISS"; fi

# --- D: remediation names the incident, and a healthy service is not restarted --
D_MISS=""
grep -q 'add("dns_mismatch"' "$GO" \
  || D_MISS="${D_MISS}the dns_mismatch remediation is not derived — the 2026-11-27 state would offer a restart instead"$'\n'
grep -q 'case ServiceFailed, ServiceStopped:' "$GO" \
  || D_MISS="${D_MISS}the start/logs remediations are not limited to a stopped or failed service"$'\n'
grep -q 'add("helper_missing"' "$GO" \
  || D_MISS="${D_MISS}the helper_missing remediation is not derived"$'\n'
[ -f "$GOT" ] || D_MISS="${D_MISS}${GOT} is missing (the matrix is not pinned)"$'\n'
grep -q 'TestB533_DeriveRemediations_TheIncidentIsNamedAsDNS' "$GOT" \
  || D_MISS="${D_MISS}the incident is not pinned as a test"$'\n'
grep -q 'TestB533_RequestAllowList' "$GOT" \
  || D_MISS="${D_MISS}the allow-list is not pinned as a test"$'\n'
grep -q 'TestB533_CollectServiceFacts_SystemdActiveButNamePointsElsewhere' "$GOT" \
  || D_MISS="${D_MISS}the live state (host .140, name .217) is not pinned as a test"$'\n'
if [ -z "$D_MISS" ]; then ok "D: the incident is a named remediation and a test, and a healthy service is never 'restarted'"
else while IFS= read -r l; do [ -n "$l" ] && bad "D: $l"; done <<< "$D_MISS"; fi

# --- E: i18n pairing for headscale_service.* -----------------------------------
E_BAD="$(grep -o '"headscale_service\.[a-z_0-9]*"' "$I18N" | sort | uniq -c | awk '$1 != 2 {print $2" (x"$1")"}')"
if [ -z "$E_BAD" ]; then
  ok "E: every headscale_service.* key exists in both catalogues"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "E: i18n key appears $l — RU+EN must be a pair"; done <<< "$E_BAD"
fi

# --- F: live leg (SKIPs without the host) --------------------------------------
if command -v systemctl >/dev/null 2>&1 && [ -f /etc/systemd/system/skygate-service.path ]; then
  if systemctl is-enabled --quiet skygate-service.path 2>/dev/null; then
    ok "F: skygate-service.path is enabled on this host"
  else
    bad "F: skygate-service.path exists but is not enabled — the restart button would stage a request nobody consumes"
  fi
else
  skip "F: the privileged helper's path unit is not installed on this host (expected off the production install)"
fi

printf '\nB533: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
