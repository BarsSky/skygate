#!/usr/bin/env bash
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"   # B339: read the exit_rules SURFACE, not one file
# check_b376_exit_rules_batch_and_target_view.sh
#
# 2026-10-11 (B376) — «не удобно отображение по группам у пользователя» and
# «нет возможности набрасывать правила группой».
#
# OPERATOR REPORT (verbatim, two complaints in one message):
#
#	«помоги корректно сделать рефактор отображения правил на странице exit rules
#	 и рефактор их установки у пользователя и админа — сейчас не удобно
#	 отображение по группам у пользователя и нет возможности набрасывать правила
#	 группой»
#
# MEASURED STATE BEFORE THE CHANGE:
#
#   * /my/exit-rules rendered ONE tree — (device → exit node → CDN group → CIDR
#     rows) — so "which of my machines route this destination, and through which
#     relay?" meant opening a section per device and comparing by eye. The same
#     destination on two devices appeared twice, in two branches, with no visual
#     link between them (cdn_group.go's own header documents the shape).
#   * the only way to add N destinations was N form submits, per device. The REST
#     API had accepted a batch since forever (POST /my/exit-rules/api, api.go) but
#     it is documented on a help page and speaks JSON.
#   * the admin page could add one destination at a time, for a device id typed by
#     hand, and could not drive the tree's <details> together.
#
# WHAT THIS CONTRACT GUARDS
#   A. one installation core: the single form and the batch form share
#      addRuleTarget, so DNS resolution, /32 derivation, parent_domain
#      bookkeeping and duplicate classification cannot drift between them
#      (exactly one net.LookupHost in the whole surface)
#   B. the batch form exists for BOTH surfaces (user + admin) and is routed
#   C. the caps are enforced per row with a LIVE counter, so a paste larger than
#      the budget stops at the budget and says so
#   D. NO schema change: the batch goes through the existing insert path
#   E. the by-target view exists and is the DEFAULT, built from the same rule
#      slice as the tree, and every level collapses together (both pages)
#   F. the i18n keys exist in BOTH catalogues (a missing one renders the raw key)
#   G. the new tests actually RUN (a -run filter matching nothing exits 0 — the
#      B322 class) and the whole package still passes
#   H. bookkeeping: this script is tracked, registered and indexed
#
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B376: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

TARGET_VIEW=internal/feature/exit_rules/target_view.go
BULK_VALUES=internal/feature/exit_rules/bulk_values.go
BULK_ADD=internal/feature/exit_rules/bulk_add.go
TPL_MY=internal/handlers/templates/exit_rules.html
TPL_ADMIN=internal/handlers/templates/admin/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go

gosurface SKY_EXIT internal/feature/exit_rules/*.go
gosurface SKY_MAIN cmd/skygate/*.go

hdr "B376 — batch install + by-target view"

# --- A: one installation core -------------------------------------------------
if [ -f "$TARGET_VIEW" ] && grep -q 'func GroupRulesByTarget' "$TARGET_VIEW"; then
  ok "A1: target_view.go defines GroupRulesByTarget (the by-target projection)"
else
  bad "A1: target_view.go has no GroupRulesByTarget — the by-target view has no source"
fi

if [ -f "$BULK_VALUES" ] && grep -q 'func ParseBulkValues' "$BULK_VALUES" && grep -q 'func ParseBulkPayload' "$BULK_VALUES"; then
  ok "A2: bulk_values.go parses both the pasted list and an uploaded JSON/CSV payload"
else
  bad "A2: bulk_values.go is missing ParseBulkValues and/or ParseBulkPayload"
fi

if [ -f "$BULK_ADD" ] && grep -q 'func (s \*Service) addRuleTarget' "$BULK_ADD"; then
  ok "A3: bulk_add.go carries the shared installation core (addRuleTarget)"
else
  bad "A3: addRuleTarget is missing — the batch would duplicate the single-rule logic"
fi

# The extraction is only real if the single form CALLS the core and there is
# exactly ONE DNS resolve implementation left in the surface. Two copies is how
# the /32 rows of a domain rule drifted apart from the domain row before (B237.24
# / B298), and the drift is invisible until the autoupdater starts churning.
LOOKUPS=$(grep -c 'net.LookupHost(targetValue)' "$SKY_EXIT" || true)
if [ "$LOOKUPS" = "1" ]; then
  ok "A4: exactly one net.LookupHost(targetValue) in the exit_rules surface (no second resolve path)"
else
  bad "A4: found $LOOKUPS DNS-resolve implementations in the surface, want exactly 1 (the batch and the single form must share addRuleTarget)"
fi

# CONTRACT RENEGOTIATED (2026-10-13, B380): the call gained the operator's group label as its
# last argument, so this pattern no longer pins the argument LIST — it pins what the contract
# is actually about, which is that the single form still routes through the shared install
# core (and, via A4, that there is still exactly one DNS-resolve path in the surface).
if grep -Eq 's\.addRuleTarget\(c\.UserID, devID, deviceIP, exitNode, targetType, targetValue, action' "$SKY_EXIT"; then
  ok "A5: PostMyExitRule calls addRuleTarget (single form and batch form cannot drift)"
else
  bad "A5: PostMyExitRule no longer calls addRuleTarget — the single path has its own copy again"
fi

# The B113/B123 behaviour the single path had before the extraction must survive.
if grep -q 'isValidIPOrCIDR(targetValue)' "$SKY_EXIT" && grep -q 'buildDuplicateRedirectURL(' "$SKY_EXIT"; then
  ok "A6: the pre-existing single-rule contracts survive (IP/CIDR validation + B123 duplicate redirect)"
else
  bad "A6: the refactor dropped isValidIPOrCIDR or buildDuplicateRedirectURL from the single path"
fi

# --- B: both surfaces have the batch form, and both are routed ----------------
if grep -q 'func (s \*Service) PostMyExitRulesBulk' "$SKY_EXIT" && grep -q 'func (s \*Service) PostAdminExitRulesBulk' "$SKY_EXIT"; then
  ok "B1: both batch handlers exist (user + admin)"
else
  bad "B1: one of PostMyExitRulesBulk / PostAdminExitRulesBulk is missing"
fi

if grep -q 'POST /my/exit-rules/bulk' "$SKY_MAIN" && grep -q 'POST /admin/exit-rules/bulk' "$SKY_MAIN"; then
  ok "B2: both batch endpoints are registered in the cmd/skygate surface"
else
  bad "B2: a batch endpoint is not registered — the form would 404"
fi

if grep -q 'BulkUsersWithDevices' "$SKY_EXIT" && grep -q 'node_owner_map\|ListNodeOwnerNodeIDsByUsername\|ListAllNodeOwners' "$BULK_ADD"; then
  ok "B3: the admin picker is built from node_owner_map (ownership source of truth), not from a raw node list"
else
  bad "B3: the admin device picker does not read node_owner_map"
fi

if grep -q 'n.IsExitNode' "$BULK_ADD"; then
  ok "B4: exit nodes are excluded from the batch target devices (B265: routing infra is never a rule source)"
else
  bad "B4: bulk_add.go does not filter exit nodes out of the device set"
fi

# --- C: the caps are enforced per row on a LIVE counter ----------------------
C_EXCEED=$(grep -c 'limits.ExceedReason(counts)' "$SKY_EXIT" || true)
if [ "$C_EXCEED" -ge 2 ]; then
  ok "C1: the cap ladder is consulted by both the single path and the batch ($C_EXCEED call sites)"
else
  bad "C1: only $C_EXCEED ExceedReason call site(s) — the batch may not enforce the caps"
fi

if grep -q 'counts.PerDevice += ins.UserFacing' "$BULK_ADD" && grep -q 'counts.PerUser += ins.UserFacing' "$BULK_ADD"; then
  ok "C2: the batch increments the measured counts as it writes (one paste cannot overshoot the budget)"
else
  bad "C2: the batch does not carry the per-device/per-user counts forward — a large paste would overshoot"
fi

if grep -q 'UserFacing' "$BULK_ADD" && grep -q 'SubnetParent != ""' "$BULK_ADD"; then
  ok "C3: the budget unit is the B328 one (derived subnet rows with a parent_domain do not count)"
else
  bad "C3: the batch counts rules with a different unit than the page and the caps (B328 regression)"
fi

# --- D: no schema change -----------------------------------------------------
if ! grep -qE 'CREATE TABLE|ALTER TABLE|INSERT INTO' "$BULK_ADD"; then
  ok "D1: the batch writes through the existing insert path — no new SQL, no migration"
else
  bad "D1: bulk_add.go carries raw DDL/DML; the feature was specified as UI/API only"
fi

if grep -q 'insertRuleUnique' "$BULK_ADD" && grep -q 'db.AppendDeviceRule' "$BULK_ADD"; then
  ok "D2: the batch reuses insertRuleUnique + db.AppendDeviceRule (same dedup and row shape as the single form)"
else
  bad "D2: the batch bypasses insertRuleUnique/AppendDeviceRule — dedup and parent_domain bookkeeping would drift"
fi

# The admin batch must NOT silently start writing the live policy: the existing
# admin single path does not either (the reconciler owns that write), and moving
# it here would be a second, uncontracted behaviour change.
if awk '/func \(s \*Service\) PostAdminExitRulesBulk/,/^}/' "$SKY_EXIT" | grep -q 'applyACLIfDrifted'; then
  bad "D3: the admin batch applies the ACL directly — the admin path deliberately leaves that to the reconciler"
else
  ok "D3: the admin batch does not write the live policy (mirrors PostAdminExitRule)"
fi

if awk '/func \(s \*Service\) PostMyExitRulesBulk/,/^}/' "$SKY_EXIT" | grep -q 'applyACLIfDrifted'; then
  ok "D4: the user batch applies the ACL ONCE for the whole batch (not once per destination)"
else
  bad "D4: the user batch does not apply the ACL — the rules would sit unapplied until the next tick"
fi

# --- E: the by-target view + collapse controls -------------------------------
if grep -q 'viewMode := "target"' internal/feature/exit_rules/form_my_rules.go && grep -q '"TargetRows"' internal/feature/exit_rules/form_my_rules.go; then
  ok "E1: the by-target view is the DEFAULT and is passed to the template"
else
  bad "E1: the by-target view is not the default / not passed to the template"
fi

if grep -q 'GroupRulesByTarget(ruleRowsFromDeviceRules(rules)' internal/feature/exit_rules/form_my_rules.go; then
  ok "E2: both views are built from the SAME rule slice (they cannot disagree about scope)"
else
  bad "E2: the by-target view is built from a different slice than the tree"
fi

if grep -q 'GroupRulesByTarget' "$TARGET_VIEW" && grep -q 'DeviceName' "$TARGET_VIEW" && grep -q 'IsCDNGroup' "$TARGET_VIEW"; then
  ok "E3: a CDN rule groups by the domain the operator typed, and the devices are tags on the row"
else
  bad "E3: the by-target projection lost the CDN domain grouping or the device tags"
fi

if grep -q 'view-target-link' "$TPL_MY" && grep -q 'view-device-link' "$TPL_MY" && grep -q 'skyToggleAllDetails' "$TPL_MY"; then
  ok "E4: /my/exit-rules has the view switch and the collapse/expand-all control"
else
  bad "E4: /my/exit-rules is missing the view switch or the collapse control"
fi

if grep -q 'skyToggleAllDetails' "$TPL_ADMIN" && grep -q 'admin-rules-view' "$TPL_ADMIN"; then
  ok "E5: /admin/exit-rules can collapse/expand every level of the cross-user tree"
else
  bad "E5: /admin/exit-rules has no collapse-all control"
fi

if grep -q 'skyBulkUserChanged' "$TPL_ADMIN" && grep -q 'data-user=' "$TPL_ADMIN"; then
  ok "E6: the admin batch form shows only the selected user's devices (a hidden box cannot be submitted)"
else
  bad "E6: the admin batch form does not scope the device list to the selected user"
fi

# The admin page carries the SAME by-target view: on a cross-user page the point
# of the projection is "who else routes through this destination", which the
# device-first tree can only answer by opening every user.
if [ -f internal/feature/exit_rules/target_view_admin.go ] && grep -q 'func GroupAdminRulesByTarget' internal/feature/exit_rules/target_view_admin.go \
   && grep -q 'TargetRowsAdmin' internal/feature/exit_rules/form_admin.go; then
  ok "E7: the ADMIN page has the by-target projection too (GroupAdminRulesByTarget + TargetRowsAdmin in the context)"
else
  bad "E7: the admin page has no by-target view (form_admin.go passes no TargetRowsAdmin)"
fi

if grep -q 'r.UserName + "/" + lab' internal/feature/exit_rules/target_view_admin.go; then
  ok "E8: every admin device tag names its OWNER (a bare device name is ambiguous across users)"
else
  bad "E8: the admin device tags drop the owner — the cross-user row cannot be read"
fi

if grep -q 'admin-view-target-link' "$TPL_ADMIN" && grep -q 'TargetRowsAdmin' "$TPL_ADMIN" && grep -q 'TargetRowsAdmin' internal/feature/exit_rules/form_admin.go; then
  ok "E9: /admin/exit-rules switches views and renders the by-target table"
else
  bad "E9: /admin/exit-rules has no view switch / does not render the by-target table"
fi

# TD-29: the admin tree's own delete control posts to /admin/exit-rules/delete,
# which NO mux.Handle registers. The new view must not spread that defect, so the
# template may carry only the two pre-existing forms.
NDEL=$(grep -c 'action="/admin/exit-rules/delete"' "$TPL_ADMIN" 2>/dev/null || true)
if [ "$NDEL" -le 2 ]; then
  ok "E10: the by-target view added no delete control for the unrouted endpoint ($NDEL pre-existing form(s), see docs/ROADMAP.md TD-29)"
else
  bad "E10: the admin template now has $NDEL delete forms — the by-target view spread the TD-29 404"
fi

# --- F: i18n keys exist in BOTH catalogues ----------------------------------
F_MISS=""
for k in bulk_form_title bulk_result bulk_values_label bulk_type_auto target_col_devices \
         target_cidr_count target_show_rules view_target view_device expand_all collapse_all; do
  n=$(grep -c "\"exit_rules\.$k\"" "$CAT" || true)
  [ "$n" -ge 2 ] || F_MISS="${F_MISS}exit_rules.$k ($n)"$'\n'
done
for k in bulk_title bulk_result bulk_submit bulk_no_users; do
  n=$(grep -c "\"exit_rules_admin\.$k\"" "$CAT" || true)
  [ "$n" -ge 2 ] || F_MISS="${F_MISS}exit_rules_admin.$k ($n)"$'\n'
done
if [ -z "$F_MISS" ]; then
  ok "F1: every new user-visible string is defined in BOTH catalogues (RU + EN)"
else
  bad "F1: catalogue key(s) missing a RU or EN copy:"$'\n'"$F_MISS"
fi

if grep -q 'exit_rules.bulk_form_title' "$TPL_MY" && grep -q 'exit_rules_admin.bulk_title' "$TPL_ADMIN"; then
  ok "F2: both templates use the new keys (no raw key can render)"
else
  bad "F2: a template does not use the new catalogue keys"
fi

# --- G: the tests actually run + the package passes --------------------------
if command -v go >/dev/null 2>&1; then
  # Capture FIRST, then match: `go test … | grep -q` dies with SIGPIPE under
  # pipefail the moment grep finds its match (AGENTS trap #9).
  G_OUT="$(go test ./internal/feature/exit_rules/ -run 'TestGroupRulesByTarget|TestParseBulkValues|TestParseBulkPayload|TestCanonicalBulkValue|TestNormalizeTargetType|TestTargetRowDeviceCount' -count=1 2>&1)"
  if grep -q '^ok' <<<"$G_OUT"; then
    ok "G1: the B376 unit tests RUN and pass (target view + batch parser)"
  else
    bad "G1: the B376 unit tests do not pass:"$'\n'"$(tail -n 15 <<<"$G_OUT")"
  fi
  if grep -q 'no tests to run' <<<"$G_OUT"; then
    bad "G2: the -run filter matched NOTHING (the B322 class: a filter that cannot fail)"
  else
    ok "G2: the filter matched real tests"
  fi
else
  skip "G1/G2: go not on PATH"
fi

# --- H: bookkeeping ----------------------------------------------------------
H_MISS=""
git ls-files --error-unmatch scripts/check_b376_exit_rules_batch_and_target_view.sh >/dev/null 2>&1 \
  || H_MISS="${H_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
grep -q 'check_b376_exit_rules_batch_and_target_view.sh' scripts/verify_pre_deploy.sh \
  || H_MISS="${H_MISS}verify_pre_deploy.sh does not register B376 — the contract would never run (B322 D1)"$'\n'
grep -q '\*\*B376\*\*' AGENTS.md \
  || H_MISS="${H_MISS}AGENTS.md's block index has no B376 entry (AGENTS rule 2)"$'\n'
if [ -z "$H_MISS" ]; then
  ok "H1: tracked, registered and indexed"
else
  bad "H1:"$'\n'"$H_MISS"
fi

printf '\n\033[1mB376 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
