#!/usr/bin/env bash
# ============================================================================
# check_b471_relay_group_move.sh — B471
#
# «ЕСЛИ ЗАХОТИМ ПОМЕНЯТЬ ГРУППУ ПРАВИЛ НА ДРУГОЙ EXIT NODE — ВСЁ ПРОЙДЁТ ШТАТНО?»
#
# Three questions from the operator, three answers that had to become code:
#
#   1. is there a way to move a SELECTION of rules at once?  — there was not: the panel
#      could add rules in a batch (B376) and delete them one by one, but a rule's natural
#      key INCLUDES its relay, so "delete + add" produces a SECOND claim on the same prefix
#      and lets the hostname tie-break pick the owner instead of the operator;
#   2. will it go through cleanly?                            — only if the three steps run:
#      the rules, the manual PIN (a prefix pinned by hand does not follow a rule rewrite,
#      B275) and, after both, the ACL re-apply plus the route sync (B441);
#   3. can a NON-ADMIN ask?                                   — B440 shipped the request
#      channel, but it moved ONE rule per ask, and on PostgreSQL it did not work at all:
#      its queries used `?` placeholders (SQLite syntax; PostgreSQL answers SQLSTATE 42601).
#
# Contracts:
#   A. the group move exists: route, handler, admin guard, audit
#   B. it rewrites rules IN PLACE through the shared primitive (never add-a-copy)
#   C. the manual pin is a SEPARATE decision — planned, reported, and only taken on request
#   D. a move finishes by re-applying the ACL and syncing routes (the «штатно» part)
#   E. one bad row does not abort the batch; the limits are enforced
#   F. a non-admin can ask for a GROUP (multi-rule request) and one ask = one decision
#   G. the decision card shows the manual pins the approval would overwrite
#   H. no `?` placeholder is left in the request/bulk SQL (the PostgreSQL break)
#   I. i18n pairs exist for every new user-visible string
#   J. the pages RENDER (executed by tests, not greps)
#   K. the docs tell the operator how this works (EN + RU)
#   L. the detector discriminates the pre-B471 shape
# ============================================================================
set -uo pipefail
# B339: read the cmd/skygate SURFACE, never cmd/skygate/main.go as one file.
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b471.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

BULK=internal/feature/exit_rules/relay_bulk_b471.go
REQ=internal/feature/exit_rules/relay_request_b440.go
REQH=internal/feature/exit_rules/relay_request_handlers_b440.go
BULKTEST=internal/feature/exit_rules/relay_bulk_b471_test.go
RENDERTEST=internal/handlers/relay_bulk_b471_render_test.go
ADMINTPL=internal/handlers/templates/admin/exit_rules.html
MYTPL=internal/handlers/templates/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go
DOC=docs/acl-rules-reference.md
RUDOC=docs/ru/exit-relay-prefix.md
gosurface CMD cmd/skygate/*.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$BULK" "$REQ" "$REQH" "$BULKTEST" "$RENDERTEST" "$ADMINTPL" "$MYTPL" "$CAT" "$DOC" "$RUDOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the verb exists ---------------------------------------------------------
if has "$CMD" 'POST /admin/exit-rules/bulk-relay'; then
  ok "A1: the group move is a registered route"
else
  bad "A1: /admin/exit-rules/bulk-relay is not routed"
fi
if has "$BULK" 'func (s *Service) PostAdminBulkRelayMove('; then
  ok "A2: the handler exists"
else
  bad "A2: no handler for the group move"
fi
if has "$BULK" 'if c == nil || !c.IsAdmin {'; then
  ok "A3: the handler refuses a non-admin (defense in depth, like the other admin verbs)"
else
  bad "A3: the bulk move does not check the caller"
fi
if has "$BULK" 'admin_bulk_relay_move'; then
  ok "A4: the move is audited under its own action name"
else
  bad "A4: a group move would be invisible in /admin/audit"
fi

# --- B: in place, through the shared primitive ----------------------------------
if has "$BULK" 'db.UpdateDeviceRuleExitNode(d, int(rule.ID), res.Target)'; then
  ok "B1: rules are rewritten through the SAME primitive the panel and the CLI use"
else
  bad "B1: the bulk move writes device_rules some other way"
fi
if grep -n 'INSERT INTO device_rules' "$BULK" >/dev/null 2>&1; then
  bad "B2: the group move INSERTs rows — a copy is not a move (the natural key includes the relay)"
else
  ok "B2: no row is inserted: a move cannot create a second claim on a prefix"
fi
if has "$BULK" 'if oldRelay == target {' && has "$BULK" 'plan.Unchanged = append'; then
  ok "B3: a selection already on the target is reported as unchanged instead of rewritten"
else
  bad "B3: re-submitting the same selection would rewrite rows needlessly"
fi

# --- C: the manual pin is its own decision --------------------------------------
if has "$BULK" 'takePins && NormalizeRelay(pin.Relay) == oldRelay'; then
  ok "C1: a manual pin follows only when it is the SAME decision and the operator asked"
else
  bad "C1: the plan does not distinguish 'this decision' from 'somebody else's pin'"
fi
if has "$BULK" 'pinKeep' && has "$BULK" 'PinsToKeep'; then
  ok "C2: pins held by ANOTHER relay are reported, not silently taken"
else
  bad "C2: a pin on a third relay would be moved (or ignored) without a word"
fi
if has "$BULK" 'prefixowner.SetManual(d, p, res.Target)'; then
  ok "C3: the pin write uses the same primitive as the panel (SetManual)"
else
  bad "C3: the pin is written some other way"
fi
if has "$ADMINTPL" 'name="move_pins"'; then
  ok "C4: taking the pins is an explicit checkbox, not an implicit side effect"
else
  bad "C4: the form has no way to say whether pins should follow"
fi

# --- D: the move must actually take effect ---------------------------------------
if has "$BULK" 's.ReapplyACLIfDrifted(c.Username'; then
  ok "D1: after the move the ACL is re-applied (the per-CIDR via pins follow the table)"
else
  bad "D1: the policy would keep naming the old relay"
fi
if has "$BULK" 's.SyncRoutes()'; then
  ok "D2: the routes are synced, so the new relay advertises (and is approved for) the prefix"
else
  bad "D2: the ACL would name a relay that is not serving the prefix yet (the B441 hole)"
fi
if has "$BULK" 'allowed := false' && has "$BULK" 's.relayChoices()'; then
  ok "D3: the target must be a tagged exit node — the same list the request form offers"
else
  bad "D3: the form could pin a prefix to a relay that cannot carry it"
fi

# --- E: robustness ----------------------------------------------------------------
if has "$BULK" 'res.Problems = append(res.Problems'; then
  ok "E1: a row that cannot be written is reported and the batch continues (B449's rule)"
else
  bad "E1: one bad row would abort the rest of the selection"
fi
if has "$BULK" 'const BulkRelayMaxRules = 200' && has "$BULK" 'len(ids) > BulkRelayMaxRules'; then
  ok "E2: a select-all is bounded, with the limit named in the flash"
else
  bad "E2: an unbounded selection could trigger hundreds of writes and SSH calls"
fi

# --- F: a group request from a non-admin -----------------------------------------
if has "$REQH" 'ruleIDs := parseRuleIDs(r.Form["rule_id"])'; then
  ok "F1: the request form reads EVERY selected rule (multi-select), not just the first"
else
  bad "F1: a non-admin can still only ask for one rule at a time"
fi
if has "$MYTPL" 'name="rule_id" multiple'; then
  ok "F2: the user's picker is a multi-select"
else
  bad "F2: the picker is single-select, so the handler's group support is unreachable"
fi
if has "$REQH" 'prefixes = cleanPrefixes(prefixes)'; then
  ok "F3: the group ask unions the resolved prefixes into ONE decision"
else
  bad "F3: a group ask would file several requests"
fi
if has "$BULK" 'func parseRuleIDs('; then
  ok "F4: the ids are parsed, de-duplicated and bounded in one place"
else
  bad "F4: no shared id parser"
fi

# --- G: the decision card tells the truth about pins -----------------------------
if has "$REQH" 'ManualLabel: manualPinsLabelFrom(pins, r.Prefixes)'; then
  ok "G1: a pending request names the manual pins it would overwrite (and where they are)"
else
  bad "G1: 'approved and nothing moved' would look like a bug in the apply path"
fi
if has "$ADMINTPL" 'relay_request_manual_pins'; then
  ok "G2: the badge is rendered in the queue"
else
  bad "G2: the label is computed but never shown"
fi

# --- H: the PostgreSQL break -----------------------------------------------------
# The B440 queries were written with `?` (SQLite's form) and PostgreSQL answers SQLSTATE
# 42601 on every one of them, so the whole request channel was dead on the live install.
# The check reads the SQL LITERALS, not the prose: comment lines are excluded (trap #15).
_ph=0
for f in "$REQ" "$REQH"; do
  if grep -n -- '`[^`]*?[^`]*`' "$f" 2>/dev/null | grep -v ':[[:space:]]*//' | grep -q .; then
    _ph=$((_ph+1))
  fi
done
if [ "$_ph" -eq 0 ]; then
  ok "H1: no \`?\` placeholder is left in the request/bulk SQL (PostgreSQL would refuse it)"
else
  bad "H1: a query still uses \`?\` — on PostgreSQL that is a syntax error, i.e. the feature is dead there"
fi
if has "$REQH" 'db.PlaceholdersList(len(prefixes))'; then
  ok "H2: the dynamic IN list is built by the shared dialect helper"
else
  bad "H2: the dynamic IN list is hand-rolled"
fi

# --- I: i18n pairs ---------------------------------------------------------------
_c=0
for key in exit_rules.relay_request_rule_multi \
           exit_rules_admin.bulk_relay_title exit_rules_admin.bulk_relay_submit \
           exit_rules_admin.relay_request_manual_pins; do
  n=$(grep -c "\"$key\"" "$CAT" 2>/dev/null || true)
  if [ "${n:-0}" -ge 2 ]; then _c=$((_c+1)); fi
done
if [ "$_c" -ge 4 ]; then
  ok "I1: every new string exists in BOTH catalogues (RU and EN)"
else
  bad "I1: only $_c of 4 new keys are paired"
fi

# --- J: the pages render ---------------------------------------------------------
_j=0
for t in TestB471_AdminPageRendersTheBulkRelayForm TestB471_AdminDecisionCardShowsManualPins \
         TestB471_MyPageRendersTheGroupSelect; do
  has "$RENDERTEST" "func $t(" && _j=$((_j+1))
done
if [ "$_j" -ge 3 ]; then
  ok "J1: the form, the manual-pin badge and the multi-select are pinned by RENDER tests"
else
  bad "J1: only $_j of 3 render tests exist"
fi
_k=0
for t in TestB471_PlanMovesOnlyWhatChanges TestB471_PinsFollowOnlyTheSameDecision \
         TestB471_ApplyWritesInPlaceAndMovesThePlannedPins TestB471_ApplyKeepsGoingAfterABadRow \
         TestB471_ManualPinsLabelNamesWhereThePinStays; do
  has "$BULKTEST" "func $t(" && _k=$((_k+1))
done
if [ "$_k" -ge 5 ]; then
  ok "J2: the plan, the in-place write and the pin classification are pinned by five tests"
else
  bad "J2: only $_k of 5 behaviour tests exist"
fi

# --- K: documented ---------------------------------------------------------------
if has "$DOC" 'bulk-relay'; then
  ok "K1: the EN reference documents the group move"
else
  bad "K1: the operator procedure is not documented"
fi
if has "$RUDOC" 'bulk-relay'; then
  ok "K2: the RU runbook carries the same procedure"
else
  bad "K2: the RU operator has no runbook"
fi

# --- L: the detector discriminates the pre-B471 shape ----------------------------
cat > "$SKY_TMP/old.go" <<'EOF'
// The admin page can add rules in a batch and delete them one by one.
// A user may ask for another relay for one of their rules.
EOF
discriminates=1
for _m in 'PostAdminBulkRelayMove(' 'admin_bulk_relay_move' 'BulkRelayMaxRules'; do
  has "$BULK" "$_m" || discriminates=0
done
has "$REQH" 'parseRuleIDs(r.Form["rule_id"])' || discriminates=0
has "$MYTPL" 'name="rule_id" multiple' || discriminates=0
has "$CAT" 'exit_rules_admin.bulk_relay_title' || discriminates=0
for _m in 'PostAdminBulkRelayMove(' 'parseRuleIDs(' 'bulk_relay_title' 'name="rule_id" multiple'; do
  has "$SKY_TMP/old.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "L1: every predicate is present now and absent from the pre-B471 shape"
else
  bad "L1: the predicates do not discriminate the pre-B471 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB471: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB471: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
