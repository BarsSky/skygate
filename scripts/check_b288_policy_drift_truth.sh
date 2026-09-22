#!/usr/bin/env bash
# check_b288_policy_drift_truth.sh
#
# 2026-09-22 (B288) — "дрейф политики должен означать дрейф, и он должен
# устраняться сам".
#
# LIVE REPORT (operator, native host `aro`): on /admin/exit-nodes the red banner
# «политика headscale УСТАРЕЛА» kept coming back while Exit Rules showed every
# rule green and the tailnet had exactly ONE exit node to choose from.
#
# What the live documents actually said:
#
#   generated 5082 bytes / live 11341 bytes
#   grants     26               42        (16 of the live ones are DUPLICATES)
#   tagOwners   6                8        (live declares tag:dev-daniil-homepc,
#                                          tag:dev-daniil-laptop, and owns
#                                          tag:dev-infra-exit-node-vps together
#                                          with tagged-devices@)
#
# A headscale policy is a SET of statements — grants are additive — so the byte
# difference described no behavioural difference at all; the banner was true and
# useless. Three separate defects produced it, and one more was found next to
# them:
#
#   1. the comparison was `reflect.DeepEqual` after key-order normalisation, so a
#      duplicated grant (the pre-B274 generator emitted one per rule row) read as
#      drift forever;
#   2. THREE writers emitted `tagOwners` for a per-device tag with THREE different
#      owner sets (`<user>@` / `<portal-user>@ + tagged-devices@` /
#      `<row-username>@ + tagged-devices@`), so each apply undid the other's entry;
#   3. the generator's declarations came from a portal-user JOIN plus a
#      grant-referenced sweep, so a tag recorded in node_owner_map but referenced
#      by no grant was NOT declared — applying the generated policy would have
#      DELETED a declaration the node wears;
#   4. the check that would have healed all of it ran only when the assignment
#      table MOVED (`chg > 0`), which on a healthy install never happens: the
#      banner was permanent by construction.
#
# CONTRACTS
#   A. the comparison is set-based, and only the order-sensitive legacy list is
#      not normalised
#   B. one owner derivation, and every per-device tag the ownership record holds
#      is declared
#   C. a stable assignment table still heals a stale live policy (throttled)
#   D. the admin page names WHICH section differs
#   E. the legacy generator's malformed tag:public JSON entry is fixed
#   F. the regression tests exist and pass
#   G. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B288: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CMP=internal/headscale/policy_compare_b288.go
EQ=internal/headscale/policy_equivalent_b276.go
TAGS=internal/db/device_tag.go
ACL=internal/acl/acl.go
AUTO=internal/nodeownership/auto.go
NODEOWN=internal/nodeownership/nodeownership.go
SYNC=internal/feature/exit_rules/sync.go
PAGE=internal/feature/admin/exit_nodes.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go

hdr "B288 — policy drift must mean drift (and must heal itself)"

# --- A: set semantics --------------------------------------------------------
if [ -f "$CMP" ] && grep -q '^func normalizePolicy(' "$CMP"; then
  ok "A1: normalizePolicy is the comparison's canonicaliser ($CMP)"
else
  bad "A1: $CMP does not define normalizePolicy — a duplicated grant would read as drift forever"
fi
if [ -f "$EQ" ] && grep -q 'reflect.DeepEqual(normalizePolicy(am), normalizePolicy(bm))' "$EQ"; then
  ok "A2: PolicyEquivalent compares the NORMALISED documents"
else
  bad "A2: PolicyEquivalent still compares the raw decoded documents"
fi
if grep -q 'case "grants", "ssh":' "$CMP" && grep -q 'case "acls", "rules":' "$CMP"; then
  ok "A3: grants/ssh are treated as sets while acls/rules keep their order"
else
  bad "A3: the normaliser does not distinguish additive rule lists from the first-match legacy list"
fi
if grep -q 'func PolicyDriftDetail(' "$CMP"; then
  ok "A4: PolicyDriftDetail names the differing section"
else
  bad "A4: PolicyDriftDetail is missing — the page cannot say what actually differs"
fi

# --- B: one owner derivation + complete declarations -------------------------
if grep -q '^func PerDeviceTagUser(' "$TAGS" && grep -q '^func TagOwnersForUser(' "$TAGS"; then
  ok "B1: the shared tag parser and owner derivation live in $TAGS"
else
  bad "B1: PerDeviceTagUser / TagOwnersForUser are missing from $TAGS"
fi
if grep -q '^func ListDevTagsFromOwnerMap(' "$TAGS"; then
  ok "B2: the ownership record is readable as a tag list (ListDevTagsFromOwnerMap)"
else
  bad "B2: ListDevTagsFromOwnerMap is missing — declarations a node wears can still be dropped"
fi
ACL_OWNER_SITES=$(grep -c 'db.TagOwnersForUser(' "$ACL" || true)
if [ "${ACL_OWNER_SITES:-0}" -ge 2 ]; then
  ok "B3: the ACL generator derives owners through db.TagOwnersForUser (${ACL_OWNER_SITES} sites)"
else
  bad "B3: the ACL generator still builds dev-tag owner lists by hand (${ACL_OWNER_SITES:-0} shared site(s), want >= 2)"
fi
if grep -q 'devTagsToDeclare(d' "$ACL" && grep -q 'ListDevTagsFromOwnerMap' "$ACL"; then
  ok "B4: both generators declare the union that includes the ownership record"
else
  bad "B4: a generator still declares only the JOIN/pref-derived tags"
fi
if grep -q 'db.PerDeviceTagUser(row.Tag)' "$AUTO" && grep -q 'dbpkg.TagOwnersForUser(' "$NODEOWN"; then
  ok "B5: the tag reconciler and the ownership backfill use the same derivation"
else
  bad "B5: a tag-writing path still derives its own owner set (the writers would fight over the policy)"
fi
if grep -qE '"tagged-devices@"[[:space:]]*\+[[:space:]]*base' "$TAGS"; then
  ok "B6: the sentinel co-owner is part of the derivation (re-applying a tag to a tagged node stays possible)"
else
  bad "B6: the sentinel owner is not in the shared derivation"
fi

# --- C: the self-heal --------------------------------------------------------
if grep -q 's.periodicDriftCheck()' "$SYNC"; then
  ok "C1: reconcilePrefixOwnership consults the drift check on every pass"
else
  bad "C1: the drift check is still gated on a table CHANGE — a stable host can never heal"
fi
if grep -q '^func (s \*Service) periodicDriftCheck()' "$SYNC" && grep -q 'periodicDriftCheckInterval' "$SYNC"; then
  ok "C2: the periodic check exists and is throttled"
else
  bad "C2: periodicDriftCheck / periodicDriftCheckInterval are missing"
fi
if grep -q 'applyACLIfDriftedMode("skygate-periodic-drift"' "$SYNC"; then
  ok "C3: the periodic path has its own stable actor name for the audit trail"
else
  bad "C3: the periodic path does not identify itself in the audit trail"
fi
if grep -q 'chg > 0 {' "$SYNC"; then
  ok "C4: the ownership-change path is still there (B276 behaviour preserved)"
else
  bad "C4: the ownership-change trigger disappeared"
fi

# --- D: the page names the difference ---------------------------------------
if grep -q 'PolicyDetail' "$PAGE" && grep -q 'headscale.PolicyDriftDetail' "$PAGE"; then
  ok "D1: /admin/exit-nodes computes the drift detail"
else
  bad "D1: the page does not compute PolicyDetail"
fi
if grep -q '{{if .PolicyDetail}}' "$TMPL" && grep -q 'policy_autoresync' "$TMPL"; then
  ok "D2: the template renders the detail and says the sync heals it automatically"
else
  bad "D2: the template still explains every drift with the via-pin text alone"
fi
RU_KEYS=$(grep -c '"exit_nodes.prefix_owner.policy_diff"' "$I18N" || true)
EN_KEYS=$(grep -c '"exit_nodes.prefix_owner.policy_autoresync"' "$I18N" || true)
if [ "${RU_KEYS:-0}" -ge 2 ] && [ "${EN_KEYS:-0}" -ge 2 ]; then
  ok "D3: both new i18n keys exist in RU and EN"
else
  bad "D3: i18n keys missing (policy_diff=${RU_KEYS:-0}, policy_autoresync=${EN_KEYS:-0}; want 2 each)"
fi

# --- E: the legacy generator's JSON -----------------------------------------
if grep -q 'sb.WriteString("    \\"tag:public\\": \[\\"" + envAdminIdentity() + "@" + baseDomain + "\\"\]")' "$ACL"; then
  ok "E1: the legacy generator emits a well-formed tag:public array"
else
  bad "E1: the legacy tag:public entry is missing its closing quote again (invalid JSON on a host without SKYGATE_ACL_VIA_ENABLED)"
fi

# --- F: the regression tests -------------------------------------------------
for t in internal/headscale/policy_compare_b288_test.go \
         internal/db/device_tag_b288_test.go \
         internal/acl/acl_b288_test.go \
         internal/feature/exit_rules/sync_b288_test.go; do
  if [ -f "$t" ]; then
    ok "F1: $t exists"
  else
    bad "F1: $t is missing"
  fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ ./internal/db/ ./internal/acl/ ./internal/feature/exit_rules/ -run 'B288' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F2: the B288 tests pass in all four packages"
  else
    bad "F2: the B288 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "F2: go not on PATH — run the B288 tests on the VM"
fi

# --- G: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b288_policy_drift_truth.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b288_policy_drift_truth.sh is tracked by git"
else
  bad "G1: scripts/check_b288_policy_drift_truth.sh is NOT tracked"
fi

printf '\n\033[1mB288 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
