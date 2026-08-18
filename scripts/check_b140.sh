#!/usr/bin/env bash
#===============================================================================
# Skygate v1.4.0 (B140) — per-row accept_routes toggle on /admin/exit-nodes
#
# Pins the v1.4.0 fix for the operator-reported issue
# "после добавления exit-node нельзя изменить accept_routes":
# the pre-B140 admin UI only let the operator set accept_routes
# at initial node add (the "Add exit node" form on the same page),
# not edit it per-row afterwards. Changing accept_routes for an
# existing node required either a full re-add (which clobbered
# every other field via UpsertExitServer's ON CONFLICT DO UPDATE)
# or direct SQL.
#
# Post-B140: each /admin/exit-nodes table row has an inline
# <form action="/admin/exit-nodes/{nodeID}/accept-routes"> with
# a 3-option <select> (1 / 0 / -1) and an Apply button. Clicking
# Apply updates just the accept_routes column, leaving the other
# 6 columns untouched. The handler validates the state via
# parseAcceptRoutesFormValue (rejects unknown values with a
# clear error) and emits an "exit_node_set_accept_routes" audit
# log entry.
#
# What this script verifies (live, on the VM):
#   A. internal/db/queries.go: qUpdateExitServerAcceptRoutes SQL
#      constant exists with the right shape
#   B. internal/db/exit_servers.go: SetExitServerAcceptRoutes +
#      GetExitServerHostname helpers exist + ErrExitServerNotFound
#   C. internal/feature/admin/exit_nodes.go: PostAdminExitNodeSetAcceptRoutes
#      handler + parseAcceptRoutesFormValue helper + errors import
#   D. cmd/skygate/main.go: route POST /admin/exit-nodes/{node_id}/accept-routes
#      registered
#   E. internal/handlers/templates/admin/exit_nodes.html: per-row form
#      with action="/admin/exit-nodes/{{.NodeID}}/accept-routes" +
#      <select name="state"> with 3 options
#   F. internal/i18n/catalog_exit_nodes.go: 4 new keys in BOTH
#      RU + EN (accept_routes_label, accept_routes_help,
#      accept_routes_btn_set, accept_routes_updated)
#   G. Unit test file: internal/feature/admin/exit_nodes_b140_test.go
#      + 6 test functions + go test passes
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

QUERIES="internal/db/queries.go"
EXIT_SRV_GO="internal/db/exit_servers.go"
ADMIN_EXIT_GO="internal/feature/admin/exit_nodes.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/exit_nodes.html"
I18N="internal/i18n/catalog_exit_nodes.go"
TEST_FILE="internal/feature/admin/exit_nodes_b140_test.go"

for f in "${QUERIES}" "${EXIT_SRV_GO}" "${ADMIN_EXIT_GO}" "${MAIN_GO}" "${TEMPLATE}" "${I18N}" "${TEST_FILE}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: queries.go — qUpdateExitServerAcceptRoutes SQL constant
# ------------------------------------------------------------------------------
echo
echo "=== A. db/queries.go: qUpdateExitServerAcceptRoutes ==="
a_const=$(grep -cE '^[[:space:]]*qUpdateExitServerAcceptRoutes[[:space:]]*=' "${QUERIES}" || true)
a_shape=$(grep -cE 'UPDATE exit_servers SET accept_routes = \$1 WHERE node_id = \$2' "${QUERIES}" || true)
if [ "${a_const}" -ge 1 ] && [ "${a_shape}" -ge 1 ]; then
    ok "qUpdateExitServerAcceptRoutes constant + correct SQL shape (UPDATE … WHERE node_id = \$2)"
else
    bad "queries.go incomplete: const=${a_const} shape=${a_shape}"
fi

# ------------------------------------------------------------------------------
# Contract B: db/exit_servers.go — SetExitServerAcceptRoutes + helpers
# ------------------------------------------------------------------------------
echo
echo "=== B. db/exit_servers.go: SetExitServerAcceptRoutes + GetExitServerHostname + ErrExitServerNotFound ==="
b_setter=$(grep -cE '^func SetExitServerAcceptRoutes\(' "${EXIT_SRV_GO}" || true)
b_getter=$(grep -cE '^func GetExitServerHostname\(' "${EXIT_SRV_GO}" || true)
b_err=$(grep -cE '^var ErrExitServerNotFound = errors\.New' "${EXIT_SRV_GO}" || true)
b_setter_uses_q=$(grep -cE 'd\.Exec\(qUpdateExitServerAcceptRoutes' "${EXIT_SRV_GO}" || true)
b_setter_validates=$(grep -cE 'state must be -1, 0, or 1' "${EXIT_SRV_GO}" || true)
if [ "${b_setter}" -ge 1 ] && [ "${b_getter}" -ge 1 ] && [ "${b_err}" -ge 1 ] && [ "${b_setter_uses_q}" -ge 1 ] && [ "${b_setter_validates}" -ge 1 ]; then
    ok "SetExitServerAcceptRoutes + GetExitServerHostname + ErrExitServerNotFound + state validation (all 5 present)"
else
    bad "exit_servers.go incomplete: setter=${b_setter} getter=${b_getter} err=${b_err} setter_uses_q=${b_setter_uses_q} setter_validates=${b_setter_validates}"
fi

# ------------------------------------------------------------------------------
# Contract C: admin/exit_nodes.go — handler + parser + errors import
# ------------------------------------------------------------------------------
echo
echo "=== C. admin/exit_nodes.go: PostAdminExitNodeSetAcceptRoutes + parseAcceptRoutesFormValue ==="
c_handler=$(grep -cE 'func \(s \*Service\) PostAdminExitNodeSetAcceptRoutes' "${ADMIN_EXIT_GO}" || true)
c_parser=$(grep -cE '^func parseAcceptRoutesFormValue' "${ADMIN_EXIT_GO}" || true)
c_errors_import=$(grep -cE '^\s*"errors"' "${ADMIN_EXIT_GO}" || true)
c_handler_uses_q=$(grep -cE 'db\.SetExitServerAcceptRoutes\(s\.DB' "${ADMIN_EXIT_GO}" || true)
c_handler_uses_err=$(grep -cE 'errors\.Is\(err, db\.ErrExitServerNotFound\)' "${ADMIN_EXIT_GO}" || true)
c_audit=$(grep -cE 'exit_node_set_accept_routes' "${ADMIN_EXIT_GO}" || true)
if [ "${c_handler}" -ge 1 ] && [ "${c_parser}" -ge 1 ] && [ "${c_errors_import}" -ge 1 ] && [ "${c_handler_uses_q}" -ge 1 ] && [ "${c_handler_uses_err}" -ge 1 ] && [ "${c_audit}" -ge 1 ]; then
    ok "Handler + parser + errors import + db.SetExitServerAcceptRoutes call + ErrExitServerNotFound check + audit (all 6 present)"
else
    bad "exit_nodes.go incomplete: handler=${c_handler} parser=${c_parser} errors_import=${c_errors_import} handler_uses_q=${c_handler_uses_q} handler_uses_err=${c_handler_uses_err} audit=${c_audit}"
fi

# ------------------------------------------------------------------------------
# Contract D: main.go — route registration
# ------------------------------------------------------------------------------
echo
echo "=== D. cmd/skygate/main.go: POST /admin/exit-nodes/{node_id}/accept-routes ==="
d_route=$(grep -cE 'POST /admin/exit-nodes/\{node_id\}/accept-routes' "${MAIN_GO}" || true)
d_handler=$(grep -cE 'adminSvc\.PostAdminExitNodeSetAcceptRoutes' "${MAIN_GO}" || true)
if [ "${d_route}" -ge 1 ] && [ "${d_handler}" -ge 1 ]; then
    ok "main.go: route + handler reference (both present)"
else
    bad "main.go incomplete: route=${d_route} handler=${d_handler}"
fi

# ------------------------------------------------------------------------------
# Contract E: template — per-row form
# ------------------------------------------------------------------------------
echo
echo "=== E. exit_nodes.html: per-row accept_routes form + 3-option select ==="
e_form_action=$(grep -cE 'action="/admin/exit-nodes/\{\{\.NodeID\}\}/accept-routes"' "${TEMPLATE}" || true)
e_select_name=$(grep -cE 'name="state"' "${TEMPLATE}" || true)
e_option_true=$(grep -cE '<option value="1"' "${TEMPLATE}" || true)
e_option_false=$(grep -cE '<option value="-1"' "${TEMPLATE}" || true)
e_option_default=$(grep -cE '<option value="0"' "${TEMPLATE}" || true)
if [ "${e_form_action}" -ge 1 ] && [ "${e_select_name}" -ge 1 ] && [ "${e_option_true}" -ge 1 ] && [ "${e_option_false}" -ge 1 ] && [ "${e_option_default}" -ge 1 ]; then
    ok "Template: per-row form + select + 3 options (1/-1/0) all present"
else
    bad "Template incomplete: form_action=${e_form_action} select_name=${e_select_name} option_true=${e_option_true} option_false=${e_option_false} option_default=${e_option_default}"
fi

# ------------------------------------------------------------------------------
# Contract F: i18n — 4 new keys in BOTH RU and EN
# ------------------------------------------------------------------------------
echo
echo "=== F. catalog_exit_nodes.go: 4 new exit_nodes.* keys (RU + EN) ==="
keys="accept_routes_label accept_routes_help accept_routes_btn_set accept_routes_updated"
miss=0
for k in $keys; do
    n=$(grep -cE "\"exit_nodes\.${k}\"" "${I18N}" || true)
    if [ "${n}" -lt 2 ]; then
        miss=$((miss+1))
        echo "    missing EN/RU entry for exit_nodes.${k} (count=${n})"
    fi
done
if [ "${miss}" -eq 0 ]; then
    ok "All 4 new exit_nodes.* keys present in both RU + EN (8 total occurrences)"
else
    bad "i18n: ${miss} of 4 keys missing in one of the languages"
fi

# ------------------------------------------------------------------------------
# Contract G: unit test + go test
# ------------------------------------------------------------------------------
echo
echo "=== G. exit_nodes_b140_test.go: 6 test functions + go test passes ==="
g_tp=$(grep -cE '^func TestParseAcceptRoutesFormValue_True' "${TEST_FILE}" || true)
g_de=$(grep -cE '^func TestParseAcceptRoutesFormValue_Default' "${TEST_FILE}" || true)
g_fa=$(grep -cE '^func TestParseAcceptRoutesFormValue_False' "${TEST_FILE}" || true)
g_tr=$(grep -cE '^func TestParseAcceptRoutesFormValue_TrimsWhitespace' "${TEST_FILE}" || true)
g_ru=$(grep -cE '^func TestParseAcceptRoutesFormValue_RejectsUnknown' "${TEST_FILE}" || true)
g_em=$(grep -cE '^func TestParseAcceptRoutesFormValue_EmptyStringIsError' "${TEST_FILE}" || true)
test_count=$((g_tp+g_de+g_fa+g_tr+g_ru+g_em))
if [ "${test_count}" -ge 6 ]; then
    ok "6 test functions present (True/Default/False/TrimsWhitespace/RejectsUnknown/EmptyStringIsError)"
else
    bad "Test file incomplete: test_count=${test_count} (expected 6)"
fi

GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in "/c/Program Files/Go/bin/go.exe" "/usr/local/go/bin/go" "/snap/bin/go"; do
        if [ -x "${cand}" ]; then GO="${cand}"; break; fi
    done
fi
if [ -z "${GO}" ]; then
    warn "go not found — skipping G go-test (other 6 contracts still hold)"
else
    test_out=$("${GO}" test -count=1 -run 'TestParseAcceptRoutesFormValue' ./internal/feature/admin/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "go test PASSes for parseAcceptRoutesFormValue (B140 compiles + tests green)"
    else
        bad "go test FAILED: ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B140 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B140 contracts all hold."
    exit 0
fi
echo
echo "B140 has failing contracts — fix the source files above."
exit 1
