#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20.2 (B132) — per-row "Re-sync" + mismatch explainer
#
# Pins the v1.3.20.2 fix for the operator-reported issue
# "не понятно что с этим делать администратор не имеет
# никакого инструмента для исправления этих рассхождений":
# the /admin/exit-nodes page had no per-row tool to fix a
# mismatch, only the global "Sync all" button (which re-runs
# SetAdvertisedRoutes on every node and re-masks the per-node
# SSH error).
#
# Post-B132: each row has a "Re-sync" button that targets one
# hostname via POST /admin/exit-nodes/{hostname}/sync and
# returns a single-entry JSON map with the per-node result.
# The mismatch tag has a tooltip explaining what the numbers
# mean. The "Use Tailscale IP" button has a visible "TS IP"
# label (was icon-only, operators consistently missed it).
#
# What this script verifies (live, on the VM):
#   A. internal/feature/exit_rules/sync.go: per-row
#      SyncAdvertisedRoutesForNode function exists, and the
#      shared syncOneExitNode helper is extracted
#   B. internal/feature/admin/exit_nodes.go: per-row handler
#      PostAdminExitNodeSync exists with r.PathValue("hostname")
#   C. internal/feature/admin/service.go: SyncRoutesForNode
#      callback field exists
#   D. cmd/skygate/main.go: adminSvc.SyncRoutesForNode wired
#      + POST /admin/exit-nodes/{hostname}/sync route registered
#   E. internal/handlers/templates/admin/exit_nodes.html:
#      per-row "Re-sync" button (form action=/admin/exit-nodes/<host>/sync)
#      + mismatch tag has title="...mismatch_help..." tooltip
#      + "Use Tailscale IP" button has "TS IP" text label
#   F. internal/i18n/catalog_exit_nodes.go: 7 new keys
#      (resync_button, resync_help, mismatch_help, last_sync,
#      last_sync_never, use_ts_ip_short, use_ts_ip_help_tooltip)
#      in BOTH RU + EN maps
#   G. Live smoke test: build the package, no compile errors
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

SYNC_GO="internal/feature/exit_rules/sync.go"
ADMIN_EXIT_GO="internal/feature/admin/exit_nodes.go"
ADMIN_SVC_GO="internal/feature/admin/service.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/exit_nodes.html"
I18N="internal/i18n/catalog_exit_nodes.go"

for f in "${SYNC_GO}" "${ADMIN_EXIT_GO}" "${ADMIN_SVC_GO}" "${MAIN_GO}" "${TEMPLATE}" "${I18N}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: exit_rules per-row sync + shared helper
# ------------------------------------------------------------------------------
echo
echo "=== A. exit_rules/sync.go: per-row SyncAdvertisedRoutesForNode + syncOneExitNode helper ==="
a_pernode=$(grep -cE 'func \(s \*Service\) SyncAdvertisedRoutesForNode' "${SYNC_GO}" || true)
a_helper=$(grep -cE 'func syncOneExitNode' "${SYNC_GO}" || true)
a_loop_call=$(grep -cE 'syncOneExitNode\(s\.HS' "${SYNC_GO}" || true)
a_pernode_call=$(grep -cE 'syncOneExitNode\(s\.HS' "${SYNC_GO}" || true)
if [ "${a_pernode}" -ge 1 ] && [ "${a_helper}" -ge 1 ] && [ "${a_loop_call}" -ge 1 ] && [ "${a_pernode_call}" -ge 1 ]; then
    ok "SyncAdvertisedRoutesForNode + syncOneExitNode helper + both call sites (all 4 present)"
else
    bad "exit_rules refactor incomplete: pernode_fn=${a_pernode} helper=${a_helper} loop=${a_loop_call} pernode_call=${a_pernode_call}"
fi

# ------------------------------------------------------------------------------
# Contract B: admin per-row handler
# ------------------------------------------------------------------------------
echo
echo "=== B. admin/exit_nodes.go: PostAdminExitNodeSync handler uses r.PathValue(\"hostname\") ==="
b_handler=$(grep -cE 'func \(s \*Service\) PostAdminExitNodeSync' "${ADMIN_EXIT_GO}" || true)
b_pathval=$(grep -cE 'r\.PathValue\("hostname"\)' "${ADMIN_EXIT_GO}" || true)
b_audit=$(grep -cE 'exit_node_sync_one' "${ADMIN_EXIT_GO}" || true)
if [ "${b_handler}" -ge 1 ] && [ "${b_pathval}" -ge 1 ] && [ "${b_audit}" -ge 1 ]; then
    ok "PostAdminExitNodeSync handler + r.PathValue(\"hostname\") + audit action exit_node_sync_one (all 3 present)"
else
    bad "admin per-row handler incomplete: handler=${b_handler} pathval=${b_pathval} audit=${b_audit}"
fi

# ------------------------------------------------------------------------------
# Contract C: admin Service.SyncRoutesForNode callback field
# ------------------------------------------------------------------------------
echo
echo "=== C. admin/service.go: SyncRoutesForNode field exists ==="
c_field=$(grep -cE 'SyncRoutesForNode\s+func\(node string\) map\[string\]string' "${ADMIN_SVC_GO}" || true)
if [ "${c_field}" -ge 1 ]; then
    ok "Service.SyncRoutesForNode func(node string) map[string]string field exists"
else
    bad "Service.SyncRoutesForNode field missing"
fi

# ------------------------------------------------------------------------------
# Contract D: main.go wire-up + route registration
# ------------------------------------------------------------------------------
echo
echo "=== D. main.go: adminSvc.SyncRoutesForNode wired + POST /admin/exit-nodes/{hostname}/sync ==="
d_wire=$(grep -cE 'adminSvc\.SyncRoutesForNode\s*=' "${MAIN_GO}" || true)
d_wire_to=$(grep -cE 'adminSvc\.SyncRoutesForNode\s*=\s*exitRulesSvc\.SyncAdvertisedRoutesForNode' "${MAIN_GO}" || true)
d_route=$(grep -cE 'POST /admin/exit-nodes/\{hostname\}/sync' "${MAIN_GO}" || true)
if [ "${d_wire}" -ge 1 ] && [ "${d_wire_to}" -ge 1 ] && [ "${d_route}" -ge 1 ]; then
    ok "main.go: adminSvc.SyncRoutesForNode wired to exitRulesSvc.SyncAdvertisedRoutesForNode + POST /admin/exit-nodes/{hostname}/sync route registered"
else
    bad "main.go wire-up incomplete: wire=${d_wire} wire_to=${d_wire_to} route=${d_route}"
fi

# ------------------------------------------------------------------------------
# Contract E: template — per-row button + tooltip + TS IP label
# ------------------------------------------------------------------------------
echo
echo "=== E. exit_nodes.html: per-row Re-sync form + mismatch tooltip + TS IP label ==="
e_resync_form=$(grep -cE 'action="/admin/exit-nodes/\{\{\.Hostname\}\}/sync"' "${TEMPLATE}" || true)
e_mismatch_tooltip=$(grep -cE 'title="\{\{t "exit_nodes.mismatch_help"\}\}"' "${TEMPLATE}" || true)
e_tsip_label=$(grep -cE '\{\{t "exit_nodes.use_ts_ip_short"\}\}' "${TEMPLATE}" || true)
if [ "${e_resync_form}" -ge 1 ] && [ "${e_mismatch_tooltip}" -ge 1 ] && [ "${e_tsip_label}" -ge 1 ]; then
    ok "Template has per-row Re-sync form + mismatch tooltip + TS IP label (all 3 present)"
else
    bad "Template incomplete: resync_form=${e_resync_form} mismatch_tooltip=${e_mismatch_tooltip} tsip_label=${e_tsip_label}"
fi

# ------------------------------------------------------------------------------
# Contract F: i18n — 7 new keys in BOTH RU and EN
# ------------------------------------------------------------------------------
echo
echo "=== F. catalog_exit_nodes.go: 7 new exit_nodes.* keys (RU + EN) ==="
# Count occurrences of each key. The grep on the file finds BOTH
# the RU and EN map entries (one occurrence each). 2 = both.
keys="resync_button resync_help mismatch_help last_sync last_sync_never use_ts_ip_short use_ts_ip_help_tooltip"
miss_ru=0
miss_en=0
for k in $keys; do
    n=$(grep -cE "\"exit_nodes\.${k}\"" "${I18N}" || true)
    if [ "${n}" -lt 2 ]; then
        miss_en=$((miss_en+1))
        echo "    missing EN/RU entry for exit_nodes.${k} (count=${n})"
    fi
done
if [ "${miss_en}" -eq 0 ]; then
    ok "All 7 new exit_nodes.* keys present in both RU + EN (14 total occurrences)"
else
    bad "i18n: ${miss_en} of 7 keys missing in one of the languages"
fi

# ------------------------------------------------------------------------------
# Contract G: live go test
# ------------------------------------------------------------------------------
echo
echo "=== G. live go test (exit_rules + admin + i18n) ==="
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in "/c/Program Files/Go/bin/go.exe" "/usr/local/go/bin/go" "/snap/bin/go"; do
        if [ -x "${cand}" ]; then GO="${cand}"; break; fi
    done
fi
if [ -z "${GO}" ]; then
    warn "go not found — skipping G (other 6 contracts still hold)"
else
    test_out=$("${GO}" test -count=1 ./internal/feature/exit_rules/ ./internal/feature/admin/ ./internal/i18n/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "go test PASSes for exit_rules + admin + i18n (B132 compiles + tests green)"
    else
        bad "go test FAILED: ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B132 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B132 contracts all hold."
    exit 0
fi
echo
echo "B132 has failing contracts — fix the source files above."
exit 1
