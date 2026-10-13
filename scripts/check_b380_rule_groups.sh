#!/usr/bin/env bash
# scripts/check_b380_rule_groups.sh
#
# B380 (2026-10-13) — THE OPERATOR'S OWN NAME FOR A GROUP OF RULES.
#
# THE REQUEST (verbatim): «возможность добавить в exit rules для пользователей
# группировать те правила что они задают так им будет проще ориентироваться не только
# по домену но и по пользовательскому названию».
#
# WHY THIS IS NOT "just a text column". Three things about it are contracts, not taste:
#
#   1. THE LABEL MUST NOT REACH ROUTING. Two other groupings on the same page are DERIVED
#      from the destination (CDN ranges, B237.22; the operator's typed target, B376). This
#      one is USER DATA. If a rename could change the ACL, the prefix assignment or the
#      exit-node choice, then renaming a folder would silently re-route traffic — so the
#      rename path is asserted to contain NO ACL write, and the ACL generator is asserted
#      not to read the column at all.
#   2. THE LABEL MUST SURVIVE THE DERIVATION. A domain rule becomes a domain row PLUS its
#      resolved /32 rows, and the /32 rows are what the page lists. A label that stopped
#      at the domain row would leave every visible row ungrouped. Same for the B276.1
#      all-devices fan-out copies.
#   3. " Игры" AND "Игры " MUST BE ONE GROUP. Whitespace-only and case-only differences
#      are what make a free-text grouping feature lose trust: the page shows a group the
#      user cannot reproduce by typing its name. Normalisation is therefore enforced at
#      the writers (not only at the form), and the view and the rename fold the same way.
#
# CONTRACTS
#   A. schema: additive column in BOTH migration chains, registered in lockstep
#   B. the write path carries the label to the domain row, its derived /32 rows and the
#      all-devices fan-out copies
#   C. the rename action: routed, owner-scoped, case-insensitive, refuses an empty source
#   D. the group view exists, is reachable, and the page offers the user's own names
#   E. the label does not touch routing (no ACL write on rename; the generator ignores it)
#   F. i18n pairs (RU + EN)
#   G. behavioural: the six B380 tests
#   H. bookkeeping

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B380: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MIG=internal/db/migrations_v0_79_rule_group.go
SQUERY=internal/db/queries.go
DBRULES=internal/db/device_rules.go
FANOUT=internal/db/device_rules_all_devices.go
CORE=internal/feature/exit_rules/bulk_add.go
STORE=internal/feature/exit_rules/store.go
LABEL=internal/feature/exit_rules/group_label_b380.go
ACTION=internal/feature/exit_rules/group_actions_b380.go
ROUTES=cmd/skygate/routes.go
MYTPL=internal/handlers/templates/exit_rules.html
ADMTPL=internal/handlers/templates/admin/exit_rules.html
I18N=internal/i18n/catalog_exit_rules.go
TESTS=internal/feature/exit_rules/group_label_b380_test.go

hdr "B380 — rules the user can group by their own name"

# ---------------------------------------------------------------------------
hdr "A. schema: additive, in BOTH chains, registered in lockstep"
# ---------------------------------------------------------------------------
if [ -f "$MIG" ]; then
  ok "A1: $MIG exists"
else
  bad "A1: the V079 migration file is missing — the column would exist in no chain"
fi
if grep -q 'ADD COLUMN IF NOT EXISTS group_label TEXT NOT NULL' "$MIG"; then
  ok "A2: the column is additive with a DEFAULT, so every pre-V079 row stays valid"
else
  bad "A2: the ALTER is not the additive 'ADD COLUMN IF NOT EXISTS … NOT NULL DEFAULT' form"
fi
if grep -q 'defaults to empty' "$MIG" || grep -q "DEFAULT ''" "$MIG"; then
  ok "A3: an existing install keeps its behaviour (empty label = ungrouped)"
else
  bad "A3: the migration does not state what an existing row becomes"
fi
if grep -q 'migrateV079PG' internal/db/driver_postgres.go && grep -q 'migrateV079SQLite' internal/db/driver_sqlite.go; then
  ok "A4: V079 is registered in BOTH chains (rule 9)"
else
  bad "A4: V079 is missing from one of the two migration registries"
fi
if grep -q 'migrateV079SQLite' "$MIG" && grep -q 'execSQLiteDDL' "$MIG"; then
  ok "A5: the SQLite side goes through the single DDL chokepoint (execSQLiteDDL)"
else
  bad "A5: the SQLite migration does not use execSQLiteDDL"
fi
# B11 forbids destructive DDL; this asserts the SPECIFIC column never gets dropped.
if grep -qE 'DROP COLUMN|DROP TABLE' "$MIG"; then
  bad "A6: the migration contains destructive DDL"
else
  ok "A6: no destructive DDL (a rollback is a code change, not a dropped column)"
fi

# ---------------------------------------------------------------------------
hdr "B. the write path carries the label everywhere the page reads"
# ---------------------------------------------------------------------------
if grep -q 'group_label' "$SQUERY" && grep -q 'group_label' "$DBRULES"; then
  ok "B1: the insert and the view/admin SELECTs carry the column"
else
  bad "B1: the column is written but never read (or vice versa)"
fi
if grep -q 'COALESCE(d.group_label' "$SQUERY" && grep -q 'COALESCE(r.group_label' "$SQUERY"; then
  ok "B2: both the /my and the /admin selects expose it (so the admin page can show it too)"
else
  bad "B2: one of the two view families does not return the label"
fi
# The derived /32 rows and the domain row must BOTH get it — one variable, two writers.
if grep -q 'AppendDeviceRule(s.dbc(), userID, devID, exitNode, "domain", targetValue, action, deviceIP, targetValue, groupLabel' "$CORE" \
   && grep -q 'insertRuleUnique(userID, devID, exitNode, res.TypeInserted, ip, action, deviceIP, res.SubnetParent, groupLabel)' "$CORE"; then
  ok "B3: the domain row AND its derived /32 rows are written with the label (the row the page lists included)"
else
  bad "B3: the label does not reach both the domain row and its derived /32 rows"
fi
if grep -q 'g.ParentDomain, g.GroupLabel' internal/feature/exit_rules/all_devices.go; then
  ok "B4: the all-devices fan-out copies carry it (a rule is not ungrouped on the other devices)"
else
  bad "B4: the fan-out copies lose the label"
fi
if grep -Eq 'GroupLabel:[[:space:]]+r\.GroupLabel' "$CORE"; then
  ok "B5: the RuleRow projection carries it, so the views and tests read one shape"
else
  bad "B5: the RuleRow projection drops the label"
fi
# Normalisation is enforced at the writers, not only at the form.
if grep -q 'groupLabel = NormaliseGroupLabel(groupLabel)' "$CORE" && grep -q 'groupLabel = NormaliseGroupLabel(groupLabel)' "$STORE"; then
  ok "B6: both writers normalise, so no caller can store a label the page cannot reproduce"
else
  bad "B6: normalisation happens only at the form layer — another caller could store ' Игры'"
fi

# ---------------------------------------------------------------------------
hdr "C. the rename action"
# ---------------------------------------------------------------------------
if grep -q 'POST /my/exit-rules/group' "$ROUTES" && grep -q 'PostMyExitRuleGroup' "$ROUTES"; then
  ok "C1: the rename route is registered (behind the session middleware, like its neighbours)"
else
  bad "C1: no route for the rename action"
fi
if grep -q 'func SetDeviceRuleGroup' "$DBRULES" && grep -q 'WHERE user_id = \$2' "$DBRULES"; then
  ok "C2: the UPDATE is scoped by user_id (a rename is a user action on their own list)"
else
  bad "C2: the rename is not owner-scoped — it could rewrite another user's list"
fi
if grep -q 'LOWER(COALESCE(group_label' "$DBRULES"; then
  ok "C3: the rename folds case, exactly like the view — otherwise a merged group could not be renamed as one"
else
  bad "C3: the rename is case-sensitive while the view folds, so the two disagree"
fi
if grep -q 'refuses an empty source group' "$DBRULES"; then
  ok "C4: an empty source group is refused (it would stamp a label on every ungrouped rule)"
else
  bad "C4: SetDeviceRuleGroup accepts an empty source"
fi
if grep -q 'exit_rule_group_rename' "$ACTION"; then
  ok "C5: the rename writes an audit row"
else
  bad "C5: the rename is silent in the audit log"
fi

# ---------------------------------------------------------------------------
hdr "D. the group view"
# ---------------------------------------------------------------------------
if grep -q 'case "group", "group_label", "labels"' internal/feature/exit_rules/form_my_rules.go; then
  ok "D1: ?view=group is accepted (with two legacy spellings, so an old link still lands)"
else
  bad "D1: the group view is not selectable"
fi
if grep -q '"LabelGroups"' internal/feature/exit_rules/form_my_rules.go && grep -q 'LabelGroups' "$MYTPL"; then
  ok "D2: the page renders the label groups"
else
  bad "D2: the handler computes the groups but the template does not render them"
fi
if grep -q 'GroupNamesForSuggestions' internal/feature/exit_rules/form_my_rules.go && grep -q 'group-label-names' "$MYTPL"; then
  ok "D3: the forms offer the user's own existing names as suggestions (no typo-duplicated groups)"
else
  bad "D3: the group input offers no suggestions"
fi
if grep -q 'view-group-link' "$MYTPL"; then
  ok "D4: the view switch exposes the third view next to «по цели» / «по устройству»"
else
  bad "D4: the view switch has no group entry"
fi
if grep -q 'action="/my/exit-rules/group"' "$MYTPL"; then
  ok "D5: the group view renders the rename form"
else
  bad "D5: there is no way to rename a group from the page"
fi
if grep -q 'name="group_label"' "$MYTPL" && grep -q 'name="bulk_group_label"' "$MYTPL"; then
  ok "D6: the single form and the batch form both accept a group"
else
  bad "D6: one of the two add forms cannot set a group"
fi
if grep -q 'name="group_label"' "$ADMTPL" && grep -q 'form_group_label' internal/feature/exit_rules/form_admin.go; then
  ok "D7: the admin add form can file a rule into the TARGET user's group (and preserves it on error)"
else
  bad "D7: the admin path cannot set or preserve a group"
fi

# ---------------------------------------------------------------------------
hdr "E. the label must not touch routing"
# ---------------------------------------------------------------------------
# The rename handler must contain NO ACL write: the label is presentation, and a rename
# that could re-route traffic would be a trap.
if grep -q 'applyACLIfDrifted\|SetPolicy\|ApplyGeneratedPolicy' "$ACTION"; then
  bad "E1: the rename path writes the ACL — renaming a folder would re-route traffic"
else
  ok "E1: the rename path contains NO ACL write (the label is presentation, and the audit row says so)"
fi
# Nothing in the policy generator may read the column.
if grep -rq 'group_label' internal/acl/ 2>/dev/null; then
  bad "E2: the ACL generator reads group_label — the label would influence routing"
else
  ok "E2: the ACL generator does not mention group_label"
fi
if grep -rq 'group_label' internal/prefixowner/ 2>/dev/null; then
  bad "E2b: the prefix-owner engine reads group_label"
else
  ok "E2b: the prefix-owner engine does not mention group_label"
fi
# The domain auto-updater is the path that creates MOST derived rows (every re-resolve),
# and it writes them with its own INSERT rather than through the shared writer — so it is
# the one place a label can silently go missing. It must inherit the DOMAIN row's label in
# the same statement.
NSYNC=$(grep -c 'group_label' internal/feature/exit_rules/sync_domain.go || true)
NSUBQ=$(grep -c 'COALESCE((SELECT group_label FROM device_rules' internal/feature/exit_rules/sync_domain.go || true)
NINS=$(grep -c 'INSERT INTO device_rules' internal/feature/exit_rules/sync_domain.go || true)
if [ "$NINS" -gt 0 ] && [ "$NSYNC" -ge "$NINS" ] && [ "$NSUBQ" -ge "$NINS" ]; then
  ok "E3: every derived INSERT in the auto-updater inherits the domain row's label ($NSUBQ of $NINS carry the subquery)"
else
  bad "E3: the auto-updater creates derived rows WITHOUT the label ($NSYNC label mentions, $NSUBQ subqueries, $NINS inserts) — a grouped domain would lose its group on the next resolve"
fi

# ---------------------------------------------------------------------------
hdr "F. i18n pairs (rule 10)"
# ---------------------------------------------------------------------------
MISSING=""
for k in "exit_rules.view_group" "exit_rules.group_label" "exit_rules.group_label_hint" \
         "exit_rules.group_label_bulk_hint" "exit_rules.group_ungrouped" "exit_rules.group_rule_count" \
         "exit_rules.group_rename_label" "exit_rules.group_rename_btn" "exit_rules.group_clear_btn" \
         "exit_rules.group_rename_hint" "exit_rules.group_empty"; do
  n=$(grep -c "\"$k\"" "$I18N" || true)
  [ "$n" = "2" ] || MISSING="${MISSING}${k}($n) "
done
if [ -z "$MISSING" ]; then
  ok "F1: every group key exists in BOTH catalogues"
else
  bad "F1: keys not present exactly twice (RU+EN): $MISSING"
fi

# ---------------------------------------------------------------------------
hdr "G. behavioural: the six B380 tests"
# ---------------------------------------------------------------------------
if [ -f "$TESTS" ]; then
  if command -v go >/dev/null 2>&1; then
    out="$(go test -count=1 -timeout 300s -run 'B380' ./internal/feature/exit_rules/ 2>&1)"; rc=$?
    if [ "$rc" -eq 0 ]; then
      ok "G1: the B380 tests pass (normaliser, order with ungrouped last, write path incl. derived rows, owner-scoped rename, the handler)"
    else
      bad "G1: the B380 tests failed:"
      printf '%s\n' "$out" | tail -n 8 | sed 's/^/        /' >&2
    fi
  else
    skip "G1: no go on PATH"
  fi
  if grep -q 'user_id = 8' "$TESTS"; then
    ok "G2: the rename test plants a DECOY row of a second user and asserts it does not move"
  else
    bad "G2: the owner scope is asserted nowhere — a cross-user rename would pass"
  fi
  if grep -q 'derived /32' "$TESTS"; then
    ok "G3: the derivation inheritance is asserted, not assumed"
  else
    bad "G3: nothing pins that a derived /32 inherits the label"
  fi
else
  bad "G3: $TESTS is missing — nothing behavioural pins this feature"
fi

# ---------------------------------------------------------------------------
hdr "H. bookkeeping"
# ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b380_rule_groups.sh >/dev/null 2>&1; then
  ok "H1: this script is tracked by git (AGENTS trap #11)"
else
  bad "H1: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'check_b380_rule_groups.sh' scripts/verify_pre_deploy.sh; then
  ok "H2: verify_pre_deploy.sh registers B380"
else
  bad "H2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B380\*\*' AGENTS.md; then
  ok "H3: AGENTS.md's block index carries B380"
else
  bad "H3: the block index does not know B380 (AGENTS rule 2)"
fi

printf '\n\033[1mB380 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
