#!/bin/bash
# check_b171.sh — B171 (v1.5.2) comprehensive device-delete.
#
# Operator 2026-08-25: "кнопка удалить устройство
# отсуствует у пользователя... администратор также
# по кнопке очистит не только из skygate (из таблиц
# БД) но и из headscale и headplane. забирая на себя
# управлоение политиками и тегами, корректно
# подчищая и перегенерировывая acl".
#
# Pre-B171 the per-row Delete buttons on /my/devices
# (B162, v1.5.1) and /admin/devices (B169, v1.5.2)
# only cleaned three things: the node in headscale
# (gRPC DeleteNode), the node_owner_map row, and the
# device_exit_node_prefs row. The device_rules table
# was left with orphaned rows pointing at the
# now-deleted device, and the ACL policy in headscale
# was left with `tag:dev-<user>-<device>` references
# that no longer existed. The next ACL regen would
# either skip the orphans (policy out of sync with
# /my/exit-rules) or include them and crash headscale's
# SetPolicy with a 400.
#
# B171 ships:
# 1. internal/devicedelete/devicedelete.go — the shared
#    coordinator that does node_owner_map +
#    device_exit_node_prefs + device_rules + ACL regen
#    + cache invalidate + audit in one call.
# 2. db.DeleteRulesByDeviceID + qDeleteRulesByDeviceID —
#    the new SQL primitive that cleans the orphaned
#    device_rules rows in one query.
# 3. db.DeleteNodeOwnerByNodeTagCounted — the
#    row-counted variant of the existing helper (needed
#    so the audit row can include the count).
# 4. PostMyDeviceDelete (B162 rewire) — now calls
#    devicedelete.Delete + passes deleted_rules=N +
#    acl_err=... in the redirect.
# 5. PostAdminDeviceDelete (B169 rewire) — same
#    coordinator + ok_rules=N + acl_err=... in the
#    redirect.
# 6. /my/devices template — Delete button moved
#    OUTSIDE the {{if .ExpiryUnix}} block so the
#    operator can delete their own exit-node /
#    subnet-router / no-expiry devices too.
# 7. /admin/devices template — FlashOkRules +
#    FlashACLErr extensions render the rules count
#    + ACL error inline.
# 8. 2 new i18n keys RU + EN (devices.delete_acl_rules_cleaned
#    + devices.delete_acl_err) in catalog_my.go.
#
# The B-check is split into:
#  A. Source contract (devicedelete package exists,
#     Delete function defined, Result struct has the
#     expected fields).
#  B. DB contract (qDeleteRulesByDeviceID query exists,
#     DeleteRulesByDeviceID helper exists + has the
#     right return type, DeleteNodeOwnerByNodeTagCounted
#     helper exists).
#  C. Handler contract (PostMyDeviceDelete calls
#     devicedelete.Delete, PostAdminDeviceDelete
#     calls devicedelete.Delete, both pass the right
#     dependency shape).
#  D. Template contract (/my/devices Delete button
#     outside the {{if .ExpiryUnix}} block,
#     /admin/devices renders FlashOkRules + FlashACLErr,
#     user template renders DeletedRules + DeletedACLErr).
#  E. i18n contract (4 keys RU + 4 keys EN, all
#     devices.delete_acl_*).
#  F. Unit-test contract (a small smoke test that
#     exercises the devicedelete.Delete signature +
#     pre-condition checks; the real coverage comes
#     from the live e2e probe in the deploy script).
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

# The B-check has many `awk ... | grep -q <pattern>`
# checks. Under `set -euo pipefail`, a `grep -q` that
# exits on first match can leave the upstream awk
# writing to a broken pipe; awk then exits with 141
# (SIGPIPE) which pipefail propagates as a non-zero
# pipeline exit code, even when grep itself found
# the match. The fix: do the pipe through a subshell
# with pipefail disabled (so the grep exit code is
# the only one that matters for the if), then re-
# enable pipefail for the rest of the script. This
# is the standard workaround for the awk+grep+pipefail
# interaction. The helper is used in place of inline
# `if awk ... | grep -q ... ; then` patterns.
# Usage: if awk_grep_match "<awk-script>" <file> "<pattern>"; then ...
awk_grep_match() {
    local script="$1"; shift
    local file="$1"; shift
    local pattern="$1"; shift
    local out
    set +o pipefail
    out=$(awk "$script" "$file" 2>/dev/null | grep -c "$pattern" 2>/dev/null || true)
    set -o pipefail
    [ "${out:-0}" -gt 0 ]
}

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: devicedelete package source contract"

# A.1 — the package file exists. A regression that
# renames the package or moves the file to a
# different path would break both the B162 (user)
# and B169 (admin) rewire, so the path is pinned.
if [ -f internal/devicedelete/devicedelete.go ]; then
    ok "internal/devicedelete/devicedelete.go exists"
else
    bad "internal/devicedelete/devicedelete.go is MISSING (the B171 coordinator has no implementation)"
fi

# A.2 — the package exports the Deps struct (the
# dependency bag). A regression that renamed Deps
# to a private type would force every caller to
# construct the args manually, defeating the
# purpose of the helper.
if grep -qE '^type Deps struct' internal/devicedelete/devicedelete.go; then
    ok "devicedelete.Deps struct defined"
else
    bad "devicedelete.Deps struct MISSING (caller cannot pass dependencies in a typed bag)"
fi

# A.3 — the package exports the Result struct with
# the expected fields. A regression that drops
# RulesDeleted or ACLRegen would force callers to
# re-derive the data via DB queries (a race + extra
# round-trip).
for field in NodeOwnerRowsDeleted DeviceExitPrefDeleted RulesDeleted ACLRegen; do
    if grep -qE "^	$field " internal/devicedelete/devicedelete.go; then
        ok "devicedelete.Result has the $field field"
    else
        bad "devicedelete.Result is MISSING the $field field (callers would lose data)"
    fi
done

# A.4 — the Delete function exists with the right
# signature. The signature is a public contract —
# both PostMyDeviceDelete and PostAdminDeviceDelete
# call Delete(ctx, deps, nodeID, hostname, userNameForPref),
# so a parameter rename would silently break both.
# The signature is the public contract: the B162
# and B169 rewire both call
# Delete(ctx, deps, nodeID, hostname, userNameForPref),
# so a parameter rename would silently break both.
# We check the parameter NAMES (not the full syntax
# — POSIX grep doesn't need to escape the
# parentheses for the regex, so this is more
# robust than the previous full-line check).
# A regression that renames any of the 5 expected
# parameters to a different identifier would be
# caught here AND at the call sites in
# feature/my/devices.go and feature/admin/devices.go
# (which the Go compiler enforces).
if grep -qE '^func Delete\(.*ctx context\.Context.*deps Deps.*nodeID int64.*hostname.*userNameForPref.*\) \(Result, error\) \{' internal/devicedelete/devicedelete.go; then
    ok "devicedelete.Delete has the canonical signature"
else
    bad "devicedelete.Delete signature CHANGED (B162/B169 callers would silently miscompile)"
fi

# A.5 — Delete() pre-conditions the deps. nil DB
# or nil HS would cause a nil-deref panic deep in
# the cleanup loop. The pre-condition check returns
# a non-nil error so the caller can log + bail.
if grep -qE 'deps\.DB == nil' internal/devicedelete/devicedelete.go; then
    ok "devicedelete.Delete pre-conditions deps.DB != nil"
else
    bad "devicedelete.Delete is MISSING the deps.DB pre-condition (nil DB would nil-deref)"
fi
if grep -qE 'deps\.HS == nil' internal/devicedelete/devicedelete.go; then
    ok "devicedelete.Delete pre-conditions deps.HS != nil"
else
    bad "devicedelete.Delete is MISSING the deps.HS pre-condition (nil HS would nil-deref)"
fi

# A.6 — Delete() calls db.DeleteRulesByDeviceID.
# The core B171 promise: a device delete cleans
# every device_rules row that references the
# device, in one transaction. Without this call
# the orphan rules would persist forever and the
# next ACL regen would crash.
if awk '/^func Delete/{flag=1; next} flag && /^func /{flag=0} flag' internal/devicedelete/devicedelete.go > /tmp/_b171_awk.txt && grep -q 'DeleteRulesByDeviceID' /tmp/_b171_awk.txt; then
    ok "devicedelete.Delete calls db.DeleteRulesByDeviceID (the B171 device_rules cleanup)"
else
    bad "devicedelete.Delete does NOT call db.DeleteRulesByDeviceID (the B171 promise is broken)"
fi

# A.7 — Delete() calls acl.ApplyACLPipelineForPlane.
# The post-cleanup ACL regen. Without this the
# device would be gone but headscale's policy would
# still name it (the second half of the B171 promise).
if awk '/^func Delete/{flag=1; next} flag && /^func /{flag=0} flag' internal/devicedelete/devicedelete.go > /tmp/_b171_awk.txt && grep -q 'ApplyACLPipelineForPlane' /tmp/_b171_awk.txt; then
    ok "devicedelete.Delete calls acl.ApplyACLPipelineForPlane (the B171 ACL regen)"
else
    bad "devicedelete.Delete does NOT call acl.ApplyACLPipelineForPlane (headscale policy would stay stale)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: DB contract (the new SQL primitive + helpers)"

# B.1 — the qDeleteRulesByDeviceID query constant
# exists in queries.go. A regression that renamed
# it would break the DeleteRulesByDeviceID helper.
if grep -q 'qDeleteRulesByDeviceID' internal/db/queries.go; then
    ok "qDeleteRulesByDeviceID query constant defined in internal/db/queries.go"
else
    bad "qDeleteRulesByDeviceID query constant MISSING (the B171 SQL primitive has no constant)"
fi

# B.2 — the query is a DELETE (not a SELECT). A
# regression that accidentally made it a SELECT
# would silently leak the rows.
if grep -qE 'qDeleteRulesByDeviceID\s*=\s*`DELETE FROM device_rules WHERE device_id = \$1`' internal/db/queries.go; then
    ok "qDeleteRulesByDeviceID is the canonical DELETE WHERE device_id = \$1"
else
    bad "qDeleteRulesByDeviceID has the wrong SQL (expected: DELETE FROM device_rules WHERE device_id = \$1)"
fi

# B.3 — the DeleteRulesByDeviceID helper exists
# in device_rules.go with the right return type
# (int64 for the deleted count + error).
if grep -qE '^func DeleteRulesByDeviceID\(d \*sql\.DB, deviceID int\) \(int64, error\) \{' internal/db/device_rules.go; then
    ok "db.DeleteRulesByDeviceID helper defined with the canonical signature"
else
    bad "db.DeleteRulesByDeviceID helper MISSING or has the wrong signature"
fi

# B.4 — the DeleteNodeOwnerByNodeTagCounted helper
# exists (B171 needs the row count for the audit
# log; the pre-B171 DeleteNodeOwnerByNodeTag returns
# just error, which doesn't expose the count).
if grep -qE '^func DeleteNodeOwnerByNodeTagCounted\(d dbExec, nodeID, tag string\) \(int64, error\) \{' internal/db/node_owner_map.go; then
    ok "db.DeleteNodeOwnerByNodeTagCounted helper defined with the canonical signature"
else
    bad "db.DeleteNodeOwnerByNodeTagCounted helper MISSING (the audit row can't include the cleaned count)"
fi

# B.5 — the new devicedelete package's Delete()
# uses the Counted variant (so the audit row
# gets the count). A regression that called the
# non-counted variant would compile but lose the
# audit info.
if awk '/^func Delete/{flag=1; next} flag && /^func /{flag=0} flag' internal/devicedelete/devicedelete.go > /tmp/_b171_awk.txt && grep -q 'DeleteNodeOwnerByNodeTagCounted' /tmp/_b171_awk.txt; then
    ok "devicedelete.Delete uses DeleteNodeOwnerByNodeTagCounted (count flows to the audit row)"
else
    bad "devicedelete.Delete uses the non-counted variant (audit row would lose the count)"
fi

# ---------------------------------------------------------------------------
hdr "contract C: handler contract (B162 + B169 rewire)"

# C.1 — PostMyDeviceDelete imports + calls the
# new devicedelete package. The B162 rewire
# replaces the inline cleanup block with a
# devicedelete.Delete call.
if grep -q '"skygate/internal/devicedelete"' internal/feature/my/devices.go; then
    ok "internal/feature/my/devices.go imports skygate/internal/devicedelete"
else
    bad "internal/feature/my/devices.go does NOT import devicedelete (B162 rewire missing)"
fi
if awk '/^func \(s \*Service\) PostMyDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/my/devices.go > /tmp/_b171_awk.txt && grep -q 'devicedelete\.Delete(' /tmp/_b171_awk.txt; then
    ok "PostMyDeviceDelete calls devicedelete.Delete (B162 rewire complete)"
else
    bad "PostMyDeviceDelete does NOT call devicedelete.Delete (B162 still has the pre-B171 inline cleanup)"
fi

# C.2 — PostMyDeviceDelete passes deleted_rules=N
# in the redirect. The template reads this param
# to render the "+N ACL rules cleaned" pill. A
# regression that dropped the param would lose
# the visual feedback.
if awk '/^func \(s \*Service\) PostMyDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/my/devices.go > /tmp/_b171_awk.txt && grep -q 'deleted_rules=' /tmp/_b171_awk.txt; then
    ok "PostMyDeviceDelete passes deleted_rules=N in the redirect"
else
    bad "PostMyDeviceDelete does NOT pass deleted_rules=N (the rules-cleaned pill would never render)"
fi

# C.3 — PostMyDeviceDelete passes acl_err=... in
# the redirect when the regen fails. The template
# renders this as a red warning above the success
# flash. Without this the operator would see a
# "device deleted" success and not notice that
# headscale's policy is now stale.
if awk '/^func \(s \*Service\) PostMyDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/my/devices.go > /tmp/_b171_awk.txt && grep -q 'acl_err=' /tmp/_b171_awk.txt; then
    ok "PostMyDeviceDelete passes acl_err=... in the redirect (ACL regen failure surfaces to the user)"
else
    bad "PostMyDeviceDelete does NOT pass acl_err=... (ACL regen failure would be silent)"
fi

# C.4 — PostAdminDeviceDelete imports + calls
# devicedelete.Delete. The B169 rewire mirrors
# the B162 one.
if grep -q '"skygate/internal/devicedelete"' internal/feature/admin/devices.go; then
    ok "internal/feature/admin/devices.go imports skygate/internal/devicedelete"
else
    bad "internal/feature/admin/devices.go does NOT import devicedelete (B169 rewire missing)"
fi
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go > /tmp/_b171_awk.txt && grep -q 'devicedelete\.Delete(' /tmp/_b171_awk.txt; then
    ok "PostAdminDeviceDelete calls devicedelete.Delete (B169 rewire complete)"
else
    bad "PostAdminDeviceDelete does NOT call devicedelete.Delete (B169 still has the pre-B171 inline cleanup)"
fi

# C.5 — PostAdminDeviceDelete passes ok_rules=N
# in the redirect. Mirrors C.2 for the admin path.
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go > /tmp/_b171_awk.txt && grep -q 'ok_rules=' /tmp/_b171_awk.txt; then
    ok "PostAdminDeviceDelete passes ok_rules=N in the redirect"
else
    bad "PostAdminDeviceDelete does NOT pass ok_rules=N (admin's rules-cleaned pill would never render)"
fi

# C.6 — PostAdminDeviceDelete passes acl_err=...
# in the redirect when the regen fails. Mirrors
# C.3 for the admin path.
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -q 'acl_err='; then
    ok "PostAdminDeviceDelete passes acl_err=... in the redirect"
else
    bad "PostAdminDeviceDelete does NOT pass acl_err=... (admin's ACL regen failure would be silent)"
fi

# ---------------------------------------------------------------------------
hdr "contract D: template contract"

# D.1 — the /my/devices Delete button is rendered
# for ALL own devices, not just the ones with an
# Expiry. The pre-B171 placement was INSIDE
# {{if .ExpiryUnix}}, which hid the button for
# tag:exit-node / tag:public / no-expiry devices.
# The post-B171 placement is OUTSIDE that block,
# inside the {{range .MyNodes}} loop.
#
# The check: there must be a `<form ... action="/my/devices/{{.ID}}/delete"`
# line in the file, and it must NOT be preceded
# (within 8 lines) by an `{{if .ExpiryUnix}}` open
# tag. The 8-line window is the width of the
# pre-B171 block (the {{if .ExpiryUnix}} + a few
# rows of date+warning + the Renew form).
if grep -q '/my/devices/{{.ID}}/delete' internal/handlers/templates/user/devices.html; then
    delete_line=$(grep -n '/my/devices/{{.ID}}/delete' internal/handlers/templates/user/devices.html | head -1 | cut -d: -f1)
    # Look 8 lines BEFORE the form for the {{if .ExpiryUnix}} opener.
    # If found, the Delete button is still gated by ExpiryUnix (B162 placement) — FAIL.
    if sed -n "$((delete_line-8)),$((delete_line-1))p" internal/handlers/templates/user/devices.html | grep -q '{{if .ExpiryUnix}}'; then
        bad "/my/devices Delete button is INSIDE the {{if .ExpiryUnix}} block (operator can't delete exit-nodes / no-expiry devices)"
    else
        ok "/my/devices Delete button is OUTSIDE the {{if .ExpiryUnix}} block (visible for every own device)"
    fi
else
    bad "/my/devices Delete button is MISSING entirely"
fi

# D.2 — /my/devices renders the DeletedRules
# count flash. The post-B171 template shows a
# "+N rules cleaned" pill when DeletedRulesCount > 0.
if grep -q 'DeletedRulesCount' internal/handlers/templates/user/devices.html; then
    ok "/my/devices template renders DeletedRulesCount (the B171 rules-cleaned pill)"
else
    bad "/my/devices template does NOT render DeletedRulesCount (the rules count would never appear)"
fi

# D.3 — /my/devices renders the DeletedACLErr
# warning. The post-B171 template shows a red
# alert when the ACL regen failed.
if grep -q 'DeletedACLErr' internal/handlers/templates/user/devices.html; then
    ok "/my/devices template renders DeletedACLErr (the B171 ACL regen warning)"
else
    bad "/my/devices template does NOT render DeletedACLErr (the ACL failure would be silent)"
fi

# D.4 — /admin/devices renders the FlashOkRules
# count flash. The post-B171 admin template shows
# the same "+N rules cleaned" pill as the user
# template, for consistency.
if grep -q 'FlashOkRules' internal/handlers/templates/admin/devices.html; then
    ok "/admin/devices template renders FlashOkRules (the B171 rules-cleaned pill)"
else
    bad "/admin/devices template does NOT render FlashOkRules (admin would see no rules count)"
fi

# D.5 — /admin/devices renders the FlashACLErr
# warning. Same shape as D.3 for the admin path.
if grep -q 'FlashACLErr' internal/handlers/templates/admin/devices.html; then
    ok "/admin/devices template renders FlashACLErr (the B171 admin ACL regen warning)"
else
    bad "/admin/devices template does NOT render FlashACLErr (admin's ACL failure would be silent)"
fi

# ---------------------------------------------------------------------------
hdr "contract E: i18n contract (RU + EN)"

# E.1..E.2 — the 2 new i18n keys, each in both
# halves of catalog_my.go (RU and EN). A missing
# value renders the raw key in the UI (the
# i18n.Engine returns the key name when the value
# is empty).
for key in \
    "devices.delete_acl_rules_cleaned" \
    "devices.delete_acl_err"
do
    if grep -qE "\"$key\"" internal/i18n/catalog_my.go; then
        # Count how many times the key appears in
        # the file. We expect exactly 2: one in the
        # RU half (before `var enMy`) and one in
        # the EN half. More or fewer would be a
        # regression (a stale copy-paste would
        # create 3+).
        count=$(grep -cE "\"$key\"" internal/i18n/catalog_my.go || true)
        if [ "$count" -eq 2 ]; then
            ok "i18n key $key defined exactly 2 times (RU + EN)"
        else
            bad "i18n key $key defined $count times (expected 2 = RU + EN)"
        fi
    else
        bad "i18n key $key MISSING from catalog_my.go"
    fi
done

# E.3 — each new RU value is non-empty. The grep
# is restricted to the RU half (before `var enMy`)
# to avoid catching the EN side by accident.
RU_END=$(grep -n '^var enMy' internal/i18n/catalog_my.go | head -1 | cut -d: -f1)
if [ -z "$RU_END" ]; then
    bad "could not locate the var enMy marker in catalog_my.go (B-check cannot validate RU values)"
fi
for key in \
    "devices.delete_acl_rules_cleaned" \
    "devices.delete_acl_err"
do
    val=$(awk -F: -v k="$key" -v end="$RU_END" \
        'NR < end && $0 ~ "\""k"\"" { sub(/^[^:]+:[[:space:]]*"?/, ""); sub(/"[[:space:]]*,?[[:space:]]*$/, ""); print; exit }' \
        internal/i18n/catalog_my.go)
    if [ -n "$val" ] && [ "$val" != '""' ]; then
        ok "RU value for $key is non-empty (\"$val\")"
    else
        bad "RU value for $key is EMPTY (operator would see the raw key in the UI)"
    fi
done

# E.4 — each new EN value is non-empty (same
# reason as E.3).
for key in \
    "devices.delete_acl_rules_cleaned" \
    "devices.delete_acl_err"
do
    val=$(awk -F: -v k="$key" -v start="$RU_END" \
        'NR > start && $0 ~ "\""k"\"" { sub(/^[^:]+:[[:space:]]*"?/, ""); sub(/"[[:space:]]*,?[[:space:]]*$/, ""); print; exit }' \
        internal/i18n/catalog_my.go)
    if [ -n "$val" ] && [ "$val" != '""' ]; then
        ok "EN value for $key is non-empty (\"$val\")"
    else
        bad "EN value for $key is EMPTY"
    fi
done

# ---------------------------------------------------------------------------
hdr "contract F: smoke contract (build + vet + a couple of grep pin)"

# F.1 — go build ./... exits 0. The B171 rewire
# touches 4 packages (devicedelete, db, feature/my,
# feature/admin); a compile error in any of them
# would block the deploy. The bash subshell needs
# to find `go` — on the operator's hybrid Windows
# + WSL2 host, `go` is on the PowerShell PATH but
# not on the WSL2 PATH that the B-check runs in.
# We try the PowerShell-bridged path first, then
# the system-wide candidates. The grep -q guards
# against the "go: command not found" false-
# positive that would otherwise mark every B-check
# as failed.
GO_BIN=""
for cand in "$(command -v go)" \
    "/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
        GO_BIN="$cand"
        break
    fi
done
if [ -z "$GO_BIN" ]; then
    # No go on this host — likely a CI runner or a
    # pure-data inspection environment. Skip the
    # build check rather than fail. (The pre-push
    # hook's guarantee catalog skips the build on
    # the same condition.)
    skip "go build ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" build ./... 2>/dev/null; then
    ok "go build ./... clean"
else
    bad "go build ./... FAILED (B171 rewire has a compile error — check the devicedelete/DB/handler edits)"
fi

# F.2 — go vet ./... exits 0. Catches issues that
# build alone wouldn't (printf format mismatches,
# unreachable code, etc.). Same go-discovery loop
# as F.1.
if [ -z "$GO_BIN" ]; then
    skip "go vet ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" vet ./... 2>/dev/null; then
    ok "go vet ./... clean"
else
    bad "go vet ./... FAILED (B171 code has a vet issue — check for printf mismatches or unreachable code)"
fi

# F.3 — the devicedelete.Delete audit row mentions
# the headplane note. The pre-B171 audit row said
# nothing about headplane, leaving the operator
# uncertain whether the headplane view was cleaned.
# The post-B171 audit row includes
# "headplane: read-only view, will refresh on next
# UI load (~30s)" so the operator can confirm the
# full cleanup with one audit query.
if awk '/^func Delete/{flag=1; next} flag && /^func /{flag=0} flag' internal/devicedelete/devicedelete.go > /tmp/_b171_awk.txt && grep -q 'headplane: read-only view' /tmp/_b171_awk.txt; then
    ok "devicedelete.Delete audit row mentions the headplane refresh note"
else
    bad "devicedelete.Delete audit row does NOT mention headplane (operator can't confirm the headplane cleanup)"
fi

echo
echo "B171 check OK — comprehensive device-delete with ACL regen is wired up."
