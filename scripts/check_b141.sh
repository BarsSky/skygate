#!/usr/bin/env bash
#===============================================================================
# Skygate v1.4.0 (B141) — "Adopt as skygate user" button on HSOrphans
#
# Pins the v1.4.0 fix for the operator-reported issue
# "HSOrphans — нет кнопки adopt":
# the pre-B141 /admin/users UI only DISPLAYED the HSOrphans
# list (a v0.20.0 feature rendered by admin/users.html:62-88
# from the Headscale-only users gathered by users.go:67-75).
# To adopt one of these headscale-only users into skygate, the
# operator had to:
#   1. run a manual `INSERT INTO portal_users (...) VALUES (...)`
#      with the headscale_user_id,
#   2. then make a separate API call (or another SQL UPDATE) to
#      bcrypt-set the password.
# B141 wraps that into a single inline <form> per orphan row.
#
# Post-B141: each /admin/users HSOrphans table row has an
# "Adopt" form that posts to /admin/users/HSOrphan/adopt with
# the headscale id (hidden) and the operator-chosen initial
# password. The handler:
#   - re-validates the headscale user exists (404 on stale form),
#   - re-validates the username matches the skygate pattern,
#   - INSERTs the row with is_admin=0 + ON CONFLICT(username)
#     DO NOTHING (atomic primitive; concurrent clicks are safe),
#   - emits an "hs_orphan_adopt" audit log entry,
#   - 303-redirects to /admin/users?adopted=<username>.
# The pre-B141 page also had no flash banner; B141 adds the
# same FlashSuccess/FlashError banner that the other admin
# pages use, so the operator can see the adopt outcome.
#
# What this script verifies (live, on the VM):
#   A. internal/db/queries.go: qInsertPortalUserAdopt SQL
#      constant with ON CONFLICT(username) DO NOTHING
#   B. internal/db/portal_users.go: InsertPortalUserAdopt
#      helper + handles sql.ErrNoRows (already-adopted case)
#   C. internal/feature/admin/users.go: PostAdminHSOrphanAdopt
#      handler + validateHSOrphanName helper + net/url import
#   D. cmd/skygate/main.go: route POST /admin/users/HSOrphan/adopt
#      registered
#   E. internal/handlers/templates/admin/users.html: per-row
#      adopt form (action="/admin/users/HSOrphan/adopt") with
#      hidden hs_id + password input + adopt button + flash
#      banner section (FlashHSOrphanAdopt / FlashHSOrphanExists)
#   F. internal/feature/admin/users.go GetAdminUsers reads
#      ?adopted= and ?already_adopted= query params
#   G. internal/i18n/catalog_admin.go: 4 new users.* keys in
#      BOTH RU + EN (hs_orphan_adopt_btn, hs_orphan_adopt_help,
#      hs_orphan_adopt_password_ph, hs_orphan_adopted_flash)
#   H. Unit test file: users_b141_test.go with 4 test functions
#      + go test passes
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
PORTAL_GO="internal/db/portal_users.go"
ADMIN_USERS_GO="internal/feature/admin/users.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/users.html"
I18N="internal/i18n/catalog_admin.go"
TEST_FILE="internal/feature/admin/users_b141_test.go"

for f in "${QUERIES}" "${PORTAL_GO}" "${ADMIN_USERS_GO}" "${MAIN_GO}" "${TEMPLATE}" "${I18N}" "${TEST_FILE}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: queries.go — qInsertPortalUserAdopt SQL constant
# ------------------------------------------------------------------------------
echo
echo "=== A. db/queries.go: qInsertPortalUserAdopt (ON CONFLICT DO NOTHING) ==="
a_const=$(grep -cE '^[[:space:]]*qInsertPortalUserAdopt[[:space:]]*=' "${QUERIES}" || true)
a_onconflict=$(grep -cE 'ON CONFLICT\(username\) DO NOTHING' "${QUERIES}" || true)
a_returning=$(grep -cE 'qInsertPortalUserAdopt.*RETURNING id' "${QUERIES}" || true)
a_isadmin_0=$(grep -cE 'VALUES \(\$1, \$2, 0, \$3\)' "${QUERIES}" || true)
if [ "${a_const}" -ge 1 ] && [ "${a_onconflict}" -ge 1 ] && [ "${a_returning}" -ge 1 ] && [ "${a_isadmin_0}" -ge 1 ]; then
    ok "qInsertPortalUserAdopt constant + ON CONFLICT(username) DO NOTHING + RETURNING id + is_admin hardcoded to 0"
else
    bad "queries.go incomplete: const=${a_const} onconflict=${a_onconflict} returning=${a_returning} isadmin_0=${a_isadmin_0}"
fi

# ------------------------------------------------------------------------------
# Contract B: db/portal_users.go — InsertPortalUserAdopt helper
# ------------------------------------------------------------------------------
echo
echo "=== B. db/portal_users.go: InsertPortalUserAdopt + ErrNoRows handling ==="
b_fn=$(grep -cE '^func InsertPortalUserAdopt\(' "${PORTAL_GO}" || true)
b_uses_const=$(grep -cE 'QueryRow\(qInsertPortalUserAdopt' "${PORTAL_GO}" || true)
b_handles_no_rows=$(grep -cE 'err == sql\.ErrNoRows' "${PORTAL_GO}" || true)
b_returns_3=$(grep -cE 'InsertPortalUserAdopt\(.*\) \(int64, bool, error\)' "${PORTAL_GO}" || true)
if [ "${b_fn}" -ge 1 ] && [ "${b_uses_const}" -ge 1 ] && [ "${b_handles_no_rows}" -ge 1 ] && [ "${b_returns_3}" -ge 1 ]; then
    ok "InsertPortalUserAdopt helper + uses qInsertPortalUserAdopt + handles sql.ErrNoRows + returns (int64, bool, error)"
else
    bad "portal_users.go incomplete: fn=${b_fn} uses_const=${b_uses_const} handles_no_rows=${b_handles_no_rows} returns_3=${b_returns_3}"
fi

# ------------------------------------------------------------------------------
# Contract C: admin/users.go — handler + validator + url import
# ------------------------------------------------------------------------------
echo
echo "=== C. admin/users.go: PostAdminHSOrphanAdopt + validateHSOrphanName + net/url import ==="
c_handler=$(grep -cE 'func \(s \*Service\) PostAdminHSOrphanAdopt' "${ADMIN_USERS_GO}" || true)
c_validator=$(grep -cE '^func validateHSOrphanName' "${ADMIN_USERS_GO}" || true)
c_url_import=$(grep -cE '^\s*"net/url"' "${ADMIN_USERS_GO}" || true)
c_handler_uses_db=$(grep -cE 'db\.InsertPortalUserAdopt\(s\.DB' "${ADMIN_USERS_GO}" || true)
c_handler_uses_validator=$(grep -cE 'validateHSOrphanName\(hsName\)' "${ADMIN_USERS_GO}" || true)
c_audit=$(grep -cE 'hs_orphan_adopt' "${ADMIN_USERS_GO}" || true)
if [ "${c_handler}" -ge 1 ] && [ "${c_validator}" -ge 1 ] && [ "${c_url_import}" -ge 1 ] && [ "${c_handler_uses_db}" -ge 1 ] && [ "${c_handler_uses_validator}" -ge 1 ] && [ "${c_audit}" -ge 1 ]; then
    ok "Handler + validator + net/url import + db.InsertPortalUserAdopt call + validateHSOrphanName + audit (all 6 present)"
else
    bad "users.go incomplete: handler=${c_handler} validator=${c_validator} url_import=${c_url_import} handler_uses_db=${c_handler_uses_db} handler_uses_validator=${c_handler_uses_validator} audit=${c_audit}"
fi

# ------------------------------------------------------------------------------
# Contract D: main.go — route registration
# ------------------------------------------------------------------------------
echo
echo "=== D. cmd/skygate/main.go: POST /admin/users/HSOrphan/adopt ==="
d_route=$(grep -cE 'POST /admin/users/HSOrphan/adopt' "${MAIN_GO}" || true)
d_handler=$(grep -cE 'adminSvc\.PostAdminHSOrphanAdopt' "${MAIN_GO}" || true)
if [ "${d_route}" -ge 1 ] && [ "${d_handler}" -ge 1 ]; then
    ok "main.go: route + handler reference (both present)"
else
    bad "main.go incomplete: route=${d_route} handler=${d_handler}"
fi

# ------------------------------------------------------------------------------
# Contract E: template — per-row form + flash banner
# ------------------------------------------------------------------------------
echo
echo "=== E. users.html: per-row adopt form + flash banner section ==="
e_form_action=$(grep -cE 'action="/admin/users/HSOrphan/adopt"' "${TEMPLATE}" || true)
e_hidden_hs_id=$(grep -cE 'name="hs_id"' "${TEMPLATE}" || true)
e_password_input=$(grep -cE 'name="password".*minlength="6"' "${TEMPLATE}" || true)
e_adopt_button=$(grep -cE 'users\.hs_orphan_adopt_btn' "${TEMPLATE}" || true)
e_flash_adopt=$(grep -cE '\.FlashHSOrphanAdopt' "${TEMPLATE}" || true)
e_flash_exists=$(grep -cE '\.FlashHSOrphanExists' "${TEMPLATE}" || true)
if [ "${e_form_action}" -ge 1 ] && [ "${e_hidden_hs_id}" -ge 1 ] && [ "${e_password_input}" -ge 1 ] && [ "${e_adopt_button}" -ge 1 ] && [ "${e_flash_adopt}" -ge 1 ] && [ "${e_flash_exists}" -ge 1 ]; then
    ok "Template: per-row form + hidden hs_id + password input + adopt button + both flash banners (all 6 present)"
else
    bad "Template incomplete: form_action=${e_form_action} hidden_hs_id=${e_hidden_hs_id} password_input=${e_password_input} adopt_button=${e_adopt_button} flash_adopt=${e_flash_adopt} flash_exists=${e_flash_exists}"
fi

# ------------------------------------------------------------------------------
# Contract F: GetAdminUsers reads the new query params
# ------------------------------------------------------------------------------
echo
echo "=== F. admin/users.go: GetAdminUsers reads ?adopted= + ?already_adopted= ==="
f_adopt=$(grep -cE 'r\.URL\.Query\(\)\.Get\("adopted"\)' "${ADMIN_USERS_GO}" || true)
f_already=$(grep -cE 'r\.URL\.Query\(\)\.Get\("already_adopted"\)' "${ADMIN_USERS_GO}" || true)
f_flash_data=$(grep -cE '"FlashHSOrphanAdopt":' "${ADMIN_USERS_GO}" || true)
if [ "${f_adopt}" -ge 1 ] && [ "${f_already}" -ge 1 ] && [ "${f_flash_data}" -ge 1 ]; then
    ok "GetAdminUsers reads ?adopted= + ?already_adopted= + populates FlashHSOrphanAdopt (all 3 present)"
else
    bad "GetAdminUsers incomplete: adopt=${f_adopt} already=${f_already} flash_data=${f_flash_data}"
fi

# ------------------------------------------------------------------------------
# Contract G: i18n — 4 new keys in BOTH RU and EN
# ------------------------------------------------------------------------------
echo
echo "=== G. catalog_admin.go: 4 new users.* keys (RU + EN) ==="
keys="hs_orphan_adopt_btn hs_orphan_adopt_help hs_orphan_adopt_password_ph hs_orphan_adopted_flash"
miss=0
for k in $keys; do
    n=$(grep -cE "\"users\.${k}\"" "${I18N}" || true)
    if [ "${n}" -lt 2 ]; then
        miss=$((miss+1))
        echo "    missing EN/RU entry for users.${k} (count=${n})"
    fi
done
if [ "${miss}" -eq 0 ]; then
    ok "All 4 new users.* keys present in both RU + EN (8 total occurrences)"
else
    bad "i18n: ${miss} of 4 keys missing in one of the languages"
fi

# ------------------------------------------------------------------------------
# Contract H: unit test + go test
# ------------------------------------------------------------------------------
echo
echo "=== H. users_b141_test.go: 4 test functions + go test passes ==="
h_valid=$(grep -cE '^func TestValidateHSOrphanName_Valid' "${TEST_FILE}" || true)
h_empty=$(grep -cE '^func TestValidateHSOrphanName_Empty' "${TEST_FILE}" || true)
h_invalid=$(grep -cE '^func TestValidateHSOrphanName_Invalid' "${TEST_FILE}" || true)
h_cross=$(grep -cE '^func TestValidateHSOrphanName_MatchesPostAdminUserPattern' "${TEST_FILE}" || true)
test_count=$((h_valid+h_empty+h_invalid+h_cross))
if [ "${test_count}" -ge 4 ]; then
    ok "4 test functions present (Valid/Empty/Invalid/MatchesPostAdminUserPattern)"
else
    bad "Test file incomplete: test_count=${test_count} (expected 4)"
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
    warn "go not found — skipping H go-test (other 7 contracts still hold)"
else
    test_out=$("${GO}" test -count=1 -run 'TestValidateHSOrphanName' ./internal/feature/admin/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "go test PASSes for validateHSOrphanName (B141 compiles + tests green)"
    else
        bad "go test FAILED: ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B141 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B141 contracts all hold."
    exit 0
fi
echo
echo "B141 has failing contracts — fix the source files above."
exit 1
