#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.2 follow-up (B123) — Exit Rules duplicate alert UX
#
# Pins the Goal 39 follow-up that gives the "duplicate rule"
# alert on /my/exit-rules enough context for the user to find
# the existing rule. Before B123, the alert only said
# "правило для X уже существует — не дублируем" which left the
# user hunting through the rule list, especially in the
# shared-IP case where one /32 already exists for a DIFFERENT
# parent_domain.
#
# What this script verifies:
#   A. form_my.go has the buildDuplicateRedirectURL helper
#   B. The POST handler calls buildDuplicateRedirectURL (NOT
#      the old inline ?existing= redirect)
#   C. The redirect URL contains the 4 new params:
#      target, existing_id, blocking_ip, parent_domain
#      (and re-fills form_device_id, form_exit_node,
#       form_target_type, form_target_value, form_action)
#   D. The GET handler reads the new query params and passes
#      them to the template (existing_id as int, blocking_ip,
#      parent_domain as strings; back-compat: ?existing=
#      falls back to ?target=)
#   E. exit_rules.html adds id="rule-{{.ID}}" anchor to each
#      rule row so the alert's "→ к правилу #N" link scrolls
#      to it
#   F. exit_rules.html duplicate alert renders blocking_ip,
#      parent_domain, and the link to the rule (when each is
#      set); the "duplicate-alert" id is present so CSS/JS
#      can target it
#   G. i18n: 3 new keys (duplicate_blocking,
#      duplicate_parent, duplicate_view) exist in BOTH RU and
#      EN; the B4 TestCatalogsParity already covers this but
#      a direct grep is the contract pin
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

# Allow override so this script works from /tmp on the VM
: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

FORM_MY="internal/feature/exit_rules/form_my.go"
TEMPLATE="internal/handlers/templates/exit_rules.html"
CATALOG="internal/i18n/catalog_exit_rules.go"

[ -f "${FORM_MY}" ]     || { bad "source file not found: ${FORM_MY}";     exit 1; }
[ -f "${TEMPLATE}" ]    || { bad "source file not found: ${TEMPLATE}";    exit 1; }
[ -f "${CATALOG}" ]     || { bad "source file not found: ${CATALOG}";     exit 1; }

# ------------------------------------------------------------------------------
# Contract A: form_my.go has the buildDuplicateRedirectURL helper
# ------------------------------------------------------------------------------
echo
echo "=== A. form_my.go: buildDuplicateRedirectURL helper exists ==="
if grep -q '^func buildDuplicateRedirectURL(' "${FORM_MY}"; then
    ok "buildDuplicateRedirectURL() function defined in form_my.go"
else
    bad "buildDuplicateRedirectURL() helper missing — no testable URL builder"
fi
# The helper must accept the 4 key params: target, existingID,
# blockingIP, parentDomain. The exact signature may grow with
# form_* params, but these 4 must always be there.
if awk '/^func buildDuplicateRedirectURL\(/,/^}/' "${FORM_MY}" | \
        grep -qE 'target[[:space:]]+string' && \
   awk '/^func buildDuplicateRedirectURL\(/,/^}/' "${FORM_MY}" | \
        grep -qE 'existingID[[:space:]]+int' && \
   awk '/^func buildDuplicateRedirectURL\(/,/^}/' "${FORM_MY}" | \
        grep -qE 'blockingIP' && \
   awk '/^func buildDuplicateRedirectURL\(/,/^}/' "${FORM_MY}" | \
        grep -qE 'parentDomain'; then
    ok "helper signature: (target string, existingID int, blockingIP, parentDomain, ...)"
else
    bad "helper signature missing one of: target, existingID, blockingIP, parentDomain"
fi
# Note: the awk range above may miss param names that are
# wrapped onto continuation lines (Go allows multi-line func
# signatures). Do a defensive string-level check on the function
# block (from `^func buildDuplicateRedirectURL(` to the next
# blank line + non-indented `}`).
helper_block=$(awk '/^func buildDuplicateRedirectURL\(/,/^\}/' "${FORM_MY}")
missing=0
# Use unique tokens (no trailing space) since `blockingIP,`
# and `parentDomain` are separated by a comma in the source
# (Go allows `blockingIP, parentDomain string`).
for p in "target string" "existingID int" "blockingIP" "parentDomain"; do
    if ! echo "${helper_block}" | tr -d '\n' | grep -q "${p}"; then
        bad "helper signature param missing: ${p}"
        missing=$((missing+1))
    fi
done
[ "${missing}" -eq 0 ] && ok "all 4 key params (target/existingID/blockingIP/parentDomain) present in signature"

# ------------------------------------------------------------------------------
# Contract B: POST handler uses the helper (not the old ?existing=)
# ------------------------------------------------------------------------------
echo
echo "=== B. form_my.go: PostMyExitRule uses buildDuplicateRedirectURL ==="
# The "all duplicates" redirect in PostMyExitRule must call
# the helper. Pre-fix, the redirect was inline:
#     http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?duplicate=1&existing=%s", ...))
# which is the BUG — it doesn't carry the new params.
if grep -q 'buildDuplicateRedirectURL(targetValue' "${FORM_MY}"; then
    ok "PostMyExitRule calls buildDuplicateRedirectURL(targetValue, ...)"
else
    bad "PostMyExitRule does NOT call buildDuplicateRedirectURL — old ?existing= redirect is still active"
fi
# Defensive: the old single-param redirect must be GONE.
if grep -qE 'duplicate=1&existing=' "${FORM_MY}"; then
    bad "old '?duplicate=1&existing=' inline redirect still present (B123 should have replaced it)"
else
    ok "old '?duplicate=1&existing=' inline redirect is gone"
fi

# ------------------------------------------------------------------------------
# Contract C: the redirect URL contains all 4 new params + form_*
# ------------------------------------------------------------------------------
echo
echo "=== C. redirect URL has target + existing_id + blocking_ip + parent_domain + form_* ==="
HELPER_BODY=$(awk '/^func buildDuplicateRedirectURL\(/,/^}/' "${FORM_MY}")
need=("target=" "existing_id=" "blocking_ip=" "parent_domain=" \
      "form_device_id=" "form_exit_node=" "form_target_type=" "form_target_value=" "form_action=")
missing=0
for needle in "${need[@]}"; do
    if echo "${HELPER_BODY}" | grep -qF "${needle}"; then
        ok "redirect includes ${needle}"
    else
        bad "redirect missing ${needle}"
        missing=$((missing+1))
    fi
done
[ "${missing}" -eq 0 ] || bad "redirect is missing ${missing} required param(s) — alert will be incomplete"

# ------------------------------------------------------------------------------
# Contract D: GET handler reads new params + back-compat for ?existing=
# ------------------------------------------------------------------------------
echo
echo "=== D. form_my.go GetMyExitRules reads new params + back-compat ==="
if grep -q 'Query().Get("existing_id")' "${FORM_MY}"; then
    ok "GET handler reads existing_id"
else
    bad "GET handler missing Query().Get(\"existing_id\")"
fi
if grep -q 'Query().Get("blocking_ip")' "${FORM_MY}"; then
    ok "GET handler reads blocking_ip"
else
    bad "GET handler missing Query().Get(\"blocking_ip\")"
fi
if grep -q 'Query().Get("parent_domain")' "${FORM_MY}"; then
    ok "GET handler reads parent_domain"
else
    bad "GET handler missing Query().Get(\"parent_domain\")"
fi
# Back-compat: ?existing= should still be honored (treated as target)
if grep -q 'Query().Get("existing")' "${FORM_MY}" && \
   grep -q 'target := r.URL.Query().Get("target")' "${FORM_MY}"; then
    ok "back-compat: ?existing= falls back to ?target= in GET handler"
else
    bad "GET handler missing back-compat for ?existing="
fi
# Template data dict: blocking_ip, parent_domain, existing_id
# are all passed.
if grep -q '"blocking_ip":' "${FORM_MY}" && \
   grep -q '"parent_domain":' "${FORM_MY}" && \
   grep -q '"existing_id":' "${FORM_MY}"; then
    ok "template data dict exposes blocking_ip + parent_domain + existing_id"
else
    bad "template data dict missing one of: blocking_ip, parent_domain, existing_id"
fi

# ------------------------------------------------------------------------------
# Contract E: rule row has id="rule-{{.ID}}" anchor
# ------------------------------------------------------------------------------
echo
echo "=== E. exit_rules.html: rule row has id=\"rule-{{.ID}}\" anchor ==="
# Pre-B123 the row was: <tr class="rule-row" data-type="..." data-target="...">
# Post-B123 it must also have id="rule-{{.ID}}" so the
# alert's "→ к правилу #N" link can scroll to it.
if grep -qE 'tr class="rule-row" id="rule-{{.ID}}"' "${TEMPLATE}"; then
    ok "rule row has id=\"rule-{{.ID}}\" anchor"
else
    bad "rule row missing id=\"rule-{{.ID}}\" — the \"jump to rule\" link would 404"
fi

# ------------------------------------------------------------------------------
# Contract F: duplicate alert renders blocking_ip + parent_domain + link
# ------------------------------------------------------------------------------
echo
echo "=== F. exit_rules.html: duplicate alert has blocking_ip, parent_domain, link ==="
# The alert must have id="duplicate-alert" for CSS/JS targeting
# and a stable handle (e.g. for future e2e tests that want to
# assert "alert appeared").
if grep -q 'id="duplicate-alert"' "${TEMPLATE}"; then
    ok "duplicate alert has id=\"duplicate-alert\""
else
    bad "duplicate alert missing id=\"duplicate-alert\" — CSS/JS can't target it stably"
fi
# The alert must render .blocking_ip, .parent_domain, .existing_id
# (gated by {{if ...}} so absent values don't render empty cells).
if grep -qE '\{\{\.blocking_ip\}\}' "${TEMPLATE}"; then
    ok "alert renders {{.blocking_ip}}"
else
    bad "alert missing {{.blocking_ip}} rendering"
fi
if grep -qE '\{\{\.parent_domain\}\}' "${TEMPLATE}"; then
    ok "alert renders {{.parent_domain}}"
else
    bad "alert missing {{.parent_domain}} rendering"
fi
# The link to the rule: only renders when existing_id > 0.
if grep -qE 'href="#rule-\{\{\.existing_id\}\}"' "${TEMPLATE}"; then
    ok "alert has href=\"#rule-{{.existing_id}}\" link"
else
    bad "alert missing #rule-{{.existing_id}} link — user can't jump to the existing rule"
fi

# ------------------------------------------------------------------------------
# Contract G: i18n — 3 new keys in BOTH RU and EN
# ------------------------------------------------------------------------------
echo
echo "=== G. i18n: 3 new duplicate-alert keys in RU + EN ==="
# All 3 keys must appear in catalog_exit_rules.go. The B4
# TestCatalogsParity already enforces parity; this is a direct
# pin.
need_keys=("exit_rules.duplicate_blocking" "exit_rules.duplicate_parent" "exit_rules.duplicate_view")
for k in "${need_keys[@]}"; do
    count=$(grep -c "\"${k}\"" "${CATALOG}" 2>/dev/null || echo 0)
    if [ "${count}" -ge 2 ]; then
        ok "key ${k} present in both RU + EN (${count} occurrences)"
    else
        bad "key ${k} missing or only in one language (count=${count}, want >=2)"
    fi
done

# ------------------------------------------------------------------------------
# Contract H: Go test exists for buildDuplicateRedirectURL
# ------------------------------------------------------------------------------
echo
echo "=== H. Go test for buildDuplicateRedirectURL ==="
TEST_FILE="internal/feature/exit_rules/form_my_b123_test.go"
if [ -f "${TEST_FILE}" ]; then
    ok "test file exists: ${TEST_FILE}"
    # B-check runs in bash where `go` may not be on PATH
    # (CI / WSL / cross-compile hosts). Only attempt to run
    # the test if `go` is available; otherwise rely on the
    # file's existence + the local `make verify-pre` pass.
    if command -v go >/dev/null 2>&1; then
        if go test -count=1 -short -run 'TestBuildDuplicateRedirectURL' ./internal/feature/exit_rules/ >/dev/null 2>&1; then
            ok "TestBuildDuplicateRedirectURL_* all pass"
        else
            bad "TestBuildDuplicateRedirectURL_* failed — run 'go test -run TestBuildDuplicateRedirectURL ./internal/feature/exit_rules/'"
        fi
    else
        warn "go not on PATH — skipping live test run (file existence is the contract pin)"
    fi
else
    bad "test file missing: ${TEST_FILE}"
fi

# ------------------------------------------------------------------------------
# Bonus: i18n parity (B4 already enforces it, but pin for the
# specific 3 keys added in B123 so a future revert can't slip
# through silently).
# ------------------------------------------------------------------------------
echo
echo "=== I. B4 parity: each of the 3 new keys has identical arg-count in RU and EN ==="
# Pull the RU + EN values for each key on the SAME line (not
# the next line). The catalog layout is:
#   "key" : "value",  ← RU
#   "key" : "value",  ← EN
# Use grep -n to get the line numbers, then awk to extract
# the value of each (sed would do this too, but awk keeps
# us free of GNU-vs-BSD differences).
for k in "${need_keys[@]}"; do
    ru_line=$(grep -n "\"${k}\"" "${CATALOG}" | head -1 | cut -d: -f1)
    en_line=$(grep -n "\"${k}\"" "${CATALOG}" | sed -n '2p' | cut -d: -f1)
    if [ -z "${ru_line}" ] || [ -z "${en_line}" ]; then
        bad "could not find both RU and EN for ${k} (ru_line=${ru_line}, en_line=${en_line})"
        continue
    fi
    ru_val=$(sed -n "${ru_line}p" "${CATALOG}" | sed -E 's/^[^:]+:[[:space:]]*"//; s/",?[[:space:]]*$//')
    en_val=$(sed -n "${en_line}p" "${CATALOG}" | sed -E 's/^[^:]+:[[:space:]]*"//; s/",?[[:space:]]*$//')
    # Count %s, %d placeholders
    ru_fmt=$(printf '%s' "${ru_val}" | grep -oE '%[sd]' | wc -l | tr -d ' ')
    en_fmt=$(printf '%s' "${en_val}" | grep -oE '%[sd]' | wc -l | tr -d ' ')
    if [ "${ru_fmt}" = "${en_fmt}" ]; then
        ok "${k} RU/EN have matching arg-count (${ru_fmt} placeholder(s))"
    else
        bad "${k} RU has ${ru_fmt} placeholder(s), EN has ${en_fmt} — would crash tf()"
    fi
done

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
