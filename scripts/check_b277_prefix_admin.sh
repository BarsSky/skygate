#!/usr/bin/env bash
# check_b277_prefix_admin.sh
#
# 2026-09-21 (B277, v1.5.37) — managing the prefix assignment at the operator's scale.
#
# B275.1 shipped the honest minimum: one row per prefix with a relay <select> and a
# Save button. On the live host that page had 1655 rows, 1497 of which were claimed by
# no rule at all (the table keeps every prefix it has ever seen), the operator's real
# question is "which relay serves Cloudflare / this device / everything", and answering
# it meant hundreds of clicks. B277 adds:
#
#   1. pruning — a row whose prefix no enabled rule claims is removed, so the table
#      describes the network instead of its history (manual pins are kept: an operator
#      pin is an intent that may predate the rule that will use it);
#   2. grouping — by owner relay, by domain/CDN, or by device — with per-group counts
#      and a bulk pin, because "all of Cloudflare through emilia" is one decision;
#   3. a GLOBAL override (one setting, `prefix_owner_force_relay`): every prefix that is
#      not pinned by hand is served by that relay, including prefixes that appear
#      later, and an unhealthy relay is refused (it would take all egress away);
#   4. every mutation re-applies the ACL immediately — a manual pin produces no
#      "changed" count on the next pass (Assign keeps the row it finds), so without it
#      the pin would sit in the database while headscale served the old relay.
#
# And it fixes the reason the pin did not stick in the first place: Assign ran the
# EXPLICIT pass before the MANUAL one, and the explicit pass marks its prefixes decided
# — so «Закрепить за» was silently reverted on the next pass for every prefix whose
# rules named a relay, which is every prefix that exists.
#
# CONTRACTS
#   A  pruning: dead automatic rows go, manual pins stay
#   B  precedence: manual > global > explicit > auto, and an unhealthy relay is ignored
#   C  the global override is a setting (one control, covers future prefixes)
#   D  the page groups and can act on a group; every mutation re-applies the ACL
#   E  Go behaviour contracts
#   F  live state (SKIPs when no DB)
#   G  the script itself is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B277: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

PKG=internal/prefixowner/prefixowner.go
ADMIN=internal/feature/admin/exit_nodes.go
PADMIN=internal/feature/admin/prefix_admin_b277.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
MAIN=cmd/skygate/main.go
CAT=internal/i18n/catalog_exit_nodes.go

hdr "B277 — prefix assignment: pruning, grouping, global override"

# --- A: pruning ---------------------------------------------------------------
if grep -q 'func Prune(d \*sql.DB, claimed map\[string\]bool) (int, error)' "$PKG"; then
  ok "A1: prefixowner.Prune drops rows whose prefix no rule claims"
else
  bad "A1: the table still keeps every prefix it has ever seen"
fi
if grep -q "COALESCE(source,'auto') <> 'manual'" "$PKG"; then
  ok "A2: a manual pin survives the prune (an intent may predate the rule that uses it)"
else
  bad "A2: the prune would delete operator pins"
fi
if grep -q 'Prune(d, claimedPrefixes(claims))' "$PKG"; then
  ok "A3: the prune runs inside Reconcile, on the claims it just read (one source of truth)"
else
  bad "A3: nothing prunes the table"
fi

# --- B: precedence ------------------------------------------------------------
MANUAL_LINE="$(grep -n '// Pass 1 — manual' "$PKG" | head -1 | cut -d: -f1)"
EXPLICIT_LINE="$(grep -n '// Pass 2 — explicit' "$PKG" | head -1 | cut -d: -f1)"
if [ -n "$MANUAL_LINE" ] && [ -n "$EXPLICIT_LINE" ] && [ "$MANUAL_LINE" -lt "$EXPLICIT_LINE" ]; then
  ok "B1: the manual pass runs BEFORE the explicit pass (line $MANUAL_LINE < $EXPLICIT_LINE) — «Закрепить за» can no longer be reverted by the rules"
else
  bad "B1: an operator pin on a prefix whose rules name a relay is still ignored (manual=$MANUAL_LINE explicit=$EXPLICIT_LINE)"
fi
if grep -q 'B277 FIX: this pass used to run AFTER the explicit one' "$PKG"; then
  ok "B2: the reason it must stay first is written down where the next reader will look"
else
  bad "B2: the ordering has no explanation — it will be 'tidied' back"
fi
if grep -q 'func relayIsHealthy(relay string, healthy \[\]string) bool' "$PKG" && grep -q 'is not healthy — ignoring it' "$PKG"; then
  ok "B3: a global pin to an unhealthy relay is refused (otherwise all egress is lost)"
else
  bad "B3: an unhealthy relay could be forced onto the whole tailnet"
fi

# --- C: the global override ---------------------------------------------------
if grep -q 'func ForceRelay(d \*sql.DB) string' "$PKG" && grep -q 'func SetForceRelay(d \*sql.DB, relay string) error' "$PKG"; then
  ok "C1: the override is a global SETTING (read/write helpers), not a mass write of manual rows"
else
  bad "C1: no global override"
fi
if grep -q 'prefix_owner_force_relay' "$PKG"; then
  ok "C2: it lives under a named key, so the operator can see and clear it"
else
  bad "C2: the setting has no stable key"
fi
if grep -q 'as\[i\].Source == "manual"' "$PKG" && grep -q 'as\[i\].Source = "global"' "$PKG"; then
  ok "C3: the override skips manual pins and marks what it moved as source='global'"
else
  bad "C3: the override either loses information or defeats manual pins"
fi

# --- D: the page ---------------------------------------------------------------
if grep -q 'func (s \*Service) buildPrefixGroups' "$PADMIN" && grep -q '"domain"' "$PADMIN" && grep -q '"device"' "$PADMIN"; then
  ok "D1: grouping by owner / domain-CDN / device"
else
  bad "D1: the page still renders one flat list"
fi
if grep -q 'func (s \*Service) PostAdminExitPrefixOwnerBulk' "$PADMIN" \
   && grep -q 'POST /admin/exit-nodes/prefix-owner-bulk' "$MAIN"; then
  ok "D2: a whole group can be pinned (or handed back to auto) in one action"
else
  bad "D2: no bulk action"
fi
if grep -q 'func (s \*Service) PostAdminExitNodePrefixForce' "$PADMIN" \
   && grep -q 'POST /admin/exit-nodes/prefix-owner-force' "$MAIN"; then
  ok "D3: the global override has its own form + route"
else
  bad "D3: the global override cannot be set from the page"
fi
if grep -q 'func (s \*Service) finishPrefixChange' "$PADMIN" && [ "$(grep -c 's.finishPrefixChange(' "$PADMIN" "$ADMIN" | awk -F: '{s+=$2} END {print s}')" -ge 3 ]; then
  ok "D4: every prefix mutation (single / bulk / global) re-applies the ACL immediately"
else
  bad "D4: a mutation can leave headscale serving the old relay"
fi
if grep -q 'ApplyACLForAllPlanes' "$PADMIN"; then
  ok "D5: the re-apply goes through the standard per-plane pipeline (snapshot + audit)"
else
  bad "D5: the re-apply bypasses the audited pipeline"
fi
if grep -q 'unknown exit node' "$PADMIN"; then
  ok "D6: a typo in the global relay is refused instead of stored"
else
  bad "D6: a bad relay name would be accepted"
fi
if grep -q 'prefix_owner.force_save' "$TMPL" && grep -q 'prefix-owner-bulk' "$TMPL" && grep -q 'prefix_owner.group_domain' "$TMPL"; then
  ok "D7: the template renders the group switcher, the per-group bulk form and the global control"
else
  bad "D7: the new controls are not rendered"
fi
KEYS=0
for k in group_by group_owner group_domain group_device group_prefixes group_apply group_auto group_auto_tip force_title force_help force_active force_save source_global source_global_tip filter_all filter_drift select_all_visible select_all_in_group selected_count pin_selected; do
  n="$(grep -c "exit_nodes.prefix_owner.$k\"" "$CAT")"
  [ "$n" -ge 2 ] && KEYS=$((KEYS+1))
done
if [ "$KEYS" -ge 20 ]; then
  ok "D8: every new key exists in RU and EN (20/20 pairs)"
else
  bad "D8: only $KEYS/20 keys are present in both catalogues"
fi
if grep -q 'func (s \*Service) loadPrefixAdminView' "$PADMIN" && grep -q 'PrefixAdmin' "$ADMIN"; then
  ok "D9: the page builds one view (rows + groups + stats + the current override)"
else
  bad "D9: the grouping is not wired into the page"
fi
if grep -q 'prefixowner.ForceRelay' "$PADMIN" && grep -q '"global"' "$TMPL"; then
  ok "D10: the page shows the active override and marks the rows it governs"
else
  bad "D10: the operator cannot tell that a global override is in force"
fi
# D11-D16 (B277 follow-up, 2026-09-21): the operator asked for three things on top
# of the group/bulk surface —
#   (1) collapsible <details> per group (like exit_rules CDN groups, so a CDN-sized
#       group does not eat the whole page),
#   (2) multi-checkbox selection across groups with one bulk submit (so a hand-picked
#       cross-group pin is not N submits),
#   (3) a per-group "вернуть авто" button (the bulk form can already do this via an
#       empty relay, but the operator asked for a separate one-click control).
if grep -qE '<details[^>]*class="prefix-group"' "$TMPL" && grep -qE '<summary[^>]*>' "$TMPL"; then
  ok "D11: each group renders inside <details>/<summary> (collapsible, open by default)"
else
  bad "D11: groups are still plain divs — a large CDN group takes the whole page"
fi
if grep -q 'input type="checkbox" class="prefix-check"' "$TMPL" && grep -q 'multi-pin-form' "$TMPL"; then
  ok "D12: every prefix row has a .prefix-check bound to the multi-pin form"
else
  bad "D12: no per-row checkboxes — the operator can only pin whole groups"
fi
if grep -q 'class="group-select-all"' "$TMPL" && grep -q 'selectAllInGroup' "$TMPL"; then
  ok "D13: per-group \"select all\" + the JS helper that scopes it to one <details>"
else
  bad "D13: a group with 50 prefixes cannot be selected in one click"
fi
if grep -q 'id="multi-pin-form"' "$TMPL" && grep -q 'id="multi-pin-count"' "$TMPL" && grep -q 'id="select-all-visible"' "$TMPL"; then
  ok "D14: global multi-pin form + visible counter + page-level \"select all\""
else
  bad "D14: the cross-group selection has no submit surface"
fi
if grep -q 'prefix-owner-multi' "$MAIN" && grep -q 'func (s \*Service) PostAdminExitPrefixOwnerMulti' "$PADMIN"; then
  ok "D15: PostAdminExitPrefixOwnerMulti route + handler exist (separate audit from bulk)"
else
  bad "D15: the multi-pin form has nowhere to submit"
fi
if grep -q 'name="relay" value=""' "$TMPL" && grep -q 'exit_nodes.prefix_owner.group_auto"' "$CAT" && grep -q 'group_apply' "$TMPL"; then
  ok "D16: the per-group \"вернуть авто\" form posts to bulk with empty relay (and i18n exists in both)"
else
  bad "D16: no per-group auto control — the operator must type \"\" in the select"
fi
# D17 (v1.5.38, 2026-09-21): regression guard for the template typo that
# crashed /admin/exit-nodes on first render after the B277 deploy. Inside the
# {{with .PrefixAdmin}} block the context is PrefixAdminView, which carries
# .Relays (not .RelayChoices — that lives on the top-level view). Bare
# .RelayChoices inside the with-block fails with "can't evaluate field
# RelayChoices in type admin.PrefixAdminView" the moment the template engine
# reaches that range. Assert: every .RelayChoices reference inside the
# with-block has the $ prefix (i.e. $.RelayChoices, breaking out of the
# sub-context).
WITH_LINE="$(grep -n '{{with .PrefixAdmin}}' "$TMPL" | head -1 | cut -d: -f1)"
ENDWITH_LINE="$(awk -v start="$WITH_LINE" 'NR>=start && /\{\{end\}\}/ {print NR; exit}' "$TMPL")"
if [ -n "$WITH_LINE" ] && [ -n "$ENDWITH_LINE" ]; then
  BAD="$(awk -v s="$WITH_LINE" -v e="$ENDWITH_LINE" \
         'NR>=s && NR<=e && /[^$]\.RelayChoices/ && !/\$.*RelayChoices/' "$TMPL")"
  if [ -z "$BAD" ]; then
    ok "D17: every .RelayChoices inside {{with .PrefixAdmin}} uses the $ prefix (top-level view, not PrefixAdminView)"
  else
    bad "D17: bare .RelayChoices inside {{with .PrefixAdmin}} (lines $WITH_LINE..$ENDWITH_LINE) — the template crashes on render: $(echo "$BAD" | head -3)"
  fi
else
  bad "D17: could not locate {{with .PrefixAdmin}} ... {{end}} block in the template"
fi

# --- E: behaviour --------------------------------------------------------------
if [ -f internal/prefixowner/prefixowner_b277_test.go ] && grep -q 'func TestB277_ManualPinBeatsTheRulesOwnRelay' internal/prefixowner/prefixowner_b277_test.go; then
  ok "E1: the precedence regression is pinned (manual beats the rules' relay; an unhealthy pin still loses)"
else
  bad "E1: no regression test for the pin that did not stick"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(timeout 600 go test -count=1 -run 'B277' ./internal/prefixowner/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "E2: the B277 contracts pass (prune keeps manual pins, precedence holds, the override is stored and cleared)"
  else
    bad "E2: the B277 contracts failed: $OUT"
  fi
  OUT2="$(timeout 600 go test -count=1 ./internal/feature/admin/ ./internal/prefixowner/ 2>&1)"
  if grep -q '^ok' <<< "$OUT2" && ! grep -q '^FAIL' <<< "$OUT2"; then
    ok "E3: the admin page and the engine test suites pass"
  else
    bad "E3: package tests failed: $OUT2"
  fi
else
  skip "E2/E3: go not on PATH"
fi

# --- F: live state -------------------------------------------------------------
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'skygate-pg-local'; then
  N="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -t -A -c "SELECT COUNT(*) FROM prefix_owner" 2>/dev/null | tr -d '[:space:]')"
  DEAD="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -t -A -c "SELECT COUNT(*) FROM prefix_owner p WHERE COALESCE(p.source,'auto') <> 'manual' AND NOT EXISTS (SELECT 1 FROM device_rules r WHERE r.enabled = 1 AND r.target_value = p.prefix)" 2>/dev/null | tr -d '[:space:]')"
  if [ -n "${N:-}" ]; then
    ok "F1: the live table holds $N row(s), of which $DEAD are dead automatic rows (B277 prunes those on the next sync pass)"
  else
    skip "F1: cannot read prefix_owner (no permissions / no table)"
  fi
  F="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -t -A -c "SELECT COALESCE(value,'') FROM global_settings WHERE key='prefix_owner_force_relay'" 2>/dev/null | tr -d '[:space:]')"
  if [ -n "${F:-}" ]; then
    ok "F2: a global override is ACTIVE on this instance: every non-manual prefix goes through $F"
  else
    skip "F2: no global override set (normal)"
  fi
else
  skip "F1/F2: no local skygate PostgreSQL to inspect (live checks belong on the VM)"
fi

# --- G: tracked by git (trap #11) ----------------------------------------------
if git ls-files --error-unmatch scripts/check_b277_prefix_admin.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b277_prefix_admin.sh is tracked by git"
else
  bad "G1: scripts/check_b277_prefix_admin.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB277 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
