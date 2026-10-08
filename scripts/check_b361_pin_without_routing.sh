#!/usr/bin/env bash
# check_b361_pin_without_routing.sh — B361 (2026-10-07): a stored exit-node pin must
# not outlive the routing it names, and a device with no rules must not be invisible.
#
# THE OPERATOR'S REPORT (2026-10-07, the same day as B356).
#   «посмотри почему устройство a-71 skyadmin не получает доступ по маршрутизации
#    трафика устройство андроид»
#
# MEASURED, all of it, on the reference deployment:
#
#   device_rules          a71 has ZERO rows (skyworker 139, basic 33, cyborg 12)
#   device_exit_node_prefs
#                         ONE row: src tag:dev-skyadmin-a71 → tag:dev-infra-emilia,
#                         set_by_user_id = 1 (a HUMAN), recorded ~46 days earlier
#   prefix_owner          120 rows, ALL of them karolina (B360 had just returned the
#                         assignment to its majority claimant)
#   advertised routes     karolina 122, emilia 2 (0.0.0.0/0, ::/0)
#   emilia's state        `online`
#
# `via` in headscale is a permission FILTER, not a preference: a71 was allowed to exit
# ONLY through a relay tagged tag:dev-infra-emilia, so every destination karolina now
# carried became unreachable for it. The B356 safety net did NOT fire — it drops the pin
# only when the relay is UNUSABLE (offline/degraded) and emilia was online — and the B356
# reconciler skipped the device with `stale-pref-no-owner`, a named skip in a log nobody
# reads, because the device has no rules to derive an owner from.
#
# WHAT THIS SCRIPT PINS
#   A. the predicate exists, in ONE place, and is the same one the generator uses:
#      "the preferred relay serves something this device can use" = with rules, one of
#      their prefixes is owned by that relay in prefix_owner; with no rules, that relay
#      owns at least one prefix at all. THE CONTRACT THAT MUST FAIL IF A PIN CAN EVER BE
#      EMITTED FOR A RELAY THAT OWNS NOTHING is A4 (source) and F1 (behaviour).
#   B. the pin is WITHHELD, never the row: the generator emits the UNPINNED grant and
#      leaves `device_exit_node_prefs` untouched — set_by_user_id included. THE CONTRACT
#      THAT MUST FAIL IF THE HUMAN'S ROW IS EVER DELETED OR REWRITTEN BY THIS PATH is B4
#      (source: no write of any kind on this path) and G3 (behaviour: the row survives).
#   C. the reconciler NAMES the state instead of a bare skip (B361 item 2), and the
#      reason names the relay and the prefix count it owns.
#   D. propagation: a stored-preference change spends the SAME shared apply budget as
#      every other trigger (no second throttle), and a burst is ONE apply.
#   E. both pages render the state and a one-click control, with every string an i18n key
#      in BOTH catalogues (B325).
#   F. the scripted timelines of internal/acl/acl_b361_test.go and
#      internal/feature/exit_rules/pref_reapply_b361_test.go actually run and pass.
#   G. the script is tracked by git (never swallowed by .gitignore — AGENTS trap #11).
#
# Live-state contracts report SKIP (never FAIL) when the database or the headscale policy
# cannot be read: this host may legitimately be the pre-fix state, and a red live contract
# would describe the incident rather than a regression in the tree (AGENTS rule 1).
#
# Usage:  bash scripts/check_b361_pin_without_routing.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
info() { printf '  \033[36mINFO\033[0m %s\n' "$*"; }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

OWNERSHIP="$REPO_ROOT/internal/acl/acl_relay_ownership_b361.go"
ACLGRANT="$REPO_ROOT/internal/acl/acl_generate_via.go"
ACLHEALTH="$REPO_ROOT/internal/acl/acl_relay_health_b356.go"
ACLTEST="$REPO_ROOT/internal/acl/acl_b361_test.go"
REC="$REPO_ROOT/internal/feature/exit_rules/reconciler.go"
STALE="$REPO_ROOT/internal/feature/exit_rules/pref_staleness_b356.go"
REAPPLY="$REPO_ROOT/internal/feature/exit_rules/pref_reapply_b361.go"
# sync.go was split three ways (2026-10-08, pure move); the shared apply
# slot + the exported trigger live in sync_acl.go.
SYNC="$REPO_ROOT/internal/feature/exit_rules/sync_acl.go"
REAPPLYTEST="$REPO_ROOT/internal/feature/exit_rules/pref_reapply_b361_test.go"
STALEPAGE="$REPO_ROOT/internal/feature/admin/exit_nodes_stale_prefs_b356.go"
MYPIN="$REPO_ROOT/internal/feature/my/device_exit_pin_b361.go"
MYHANDLER="$REPO_ROOT/internal/feature/my/device_exit_pref.go"
ADMINTMPL="$REPO_ROOT/internal/handlers/templates/admin/exit_nodes.html"
MYTMPL="$REPO_ROOT/internal/handlers/templates/user/exit_nodes.html"
CAT="$REPO_ROOT/internal/i18n/catalog_exit_nodes.go"
MAINPKG="$REPO_ROOT/cmd/skygate"   # the PACKAGE surface, not main.go: B339 forbids reading it as one file
# The FORMATTER still needs a file — gofmt has no notion of a surface — so the one file
# this block touched is assembled from a relative package variable rather than written
# out. It must stay RELATIVE: this script runs from the repo root and reaches gofmt
# through the Windows binary, which cannot resolve a /mnt/c/... path.
MAINPKG_REL="cmd/skygate"
MAINFILE_REL="$MAINPKG_REL/main.go"

hdr "A. the predicate: does the preferred relay serve anything this device can use?"

missing=0
for f in "$OWNERSHIP" "$ACLGRANT" "$ACLTEST" "$REC" "$STALE" "$REAPPLY" "$STALEPAGE" "$MYPIN" "$MYTMPL"; do
  [ -f "$f" ] || { bad "A0: $f is missing"; missing=1; }
done
if [ "$missing" -eq 0 ]; then
  # A1 — the predicate is a pure function with a name that says what it answers.
  if grep -q 'func servesNothing(' "$OWNERSHIP"; then
    ok "A1: servesNothing is the one predicate for 'the preferred relay serves nothing this device can use'"
  else
    bad "A1: the ownership predicate is gone — the pin can name a relay that owns nothing again"
  fi
  # A2 — the two halves, in the documented ORDER: rules first, then the no-rules case.
  if grep -q 'if len(claimedPrefixes) > 0 {' "$OWNERSHIP" \
     && grep -q 'the device has no rules of its own and the preferred relay owns no prefix at all' "$OWNERSHIP"; then
    ok "A2: both halves exist and are named (rules claim a prefix / a no-rules device needs ANY prefix)"
  else
    bad "A2: the predicate lost one of its two halves — a71 (no rules) or cyborg (rules owned elsewhere) would be invisible again"
  fi
  # A3 — unknown is not broken: an EMPTY assignment table keeps every pin, exactly as a
  # relay with no health row does (the B356 D6 rule, applied to ownership).
  if grep -q 'len(ownerTagsByPrefix) == 0 {' "$OWNERSHIP" \
     && grep -q 'evidence must not rewrite a policy' "$OWNERSHIP"; then
    ok "A3: an empty assignment table keeps every pin (a fresh install is not rewritten)"
  else
    bad "A3: the ownership check treats 'no data' as 'owns nothing' — the first generation on a fresh install would strip every pin"
  fi
  # A4 — THE CONTRACT THE TASK ASKS FOR BY NAME: a pin can never be emitted for a relay
  # that owns nothing. Both facts must be consulted in the emit path itself.
  if grep -q 'servesNothing(via, ownerTagByPrefix, claimedByDevice\[devTag\])' "$ACLGRANT"; then
    ok "A4: the per-device pin is conditional on the ownership predicate (a relay that owns nothing never receives a pin)"
  else
    bad "A4: the pin no longer consults the ownership predicate — a relay with zero prefixes in prefix_owner can be pinned again"
  fi
  # A5 — the reason is logged PER DEVICE and COUNTED per generation (never silent).
  if grep -q 'B361 via-pin WITHHELD' "$ACLGRANT" && grep -q 'pinFallbacks' "$ACLGRANT"; then
    ok "A5: withholding a pin is logged per device and counted per generation"
  else
    bad "A5: the withheld pin is silent — indistinguishable from a device that has no preference"
  fi
  # A6 — the B356 health gate is still there (the two conditions are ANDed, not swapped).
  if grep -q 'unusablePreferredRelay(via, relayHealth)' "$ACLGRANT" && grep -q 'func unusablePreferredRelay' "$ACLHEALTH"; then
    ok "A6: B356's health gate survives beside the ownership gate"
  else
    bad "A6: the health gate disappeared — an offline relay would be pinned again"
  fi
  # A7 — one predicate, two surfaces: the reconciler and both pages must read the SAME
  # function rather than re-deriving staleness (L-54).
  if grep -q 'func servesNothing(' "$OWNERSHIP" \
     && grep -q 'exit_rules.StaleExitPrefReason' "$STALEPAGE" \
     && grep -q 'exit_rules.StaleExitPrefReason' "$MYPIN"; then
    ok "A7: /admin/exit-nodes, /my/exit-nodes and the reconciler share one staleness predicate"
  else
    bad "A7: a surface re-derives the verdict — the pages and the engine can disagree"
  fi
fi

hdr "B. the stored row is NEVER rewritten — only the PIN is withheld"

if [ "$missing" -eq 0 ]; then
  # B1 — the generator only READS the preference table on this path.
  WRITES="$(grep -cE 'SetDeviceExitNodePref|DeleteDeviceExitNodePref|db\.SetDeviceExitNode|UPDATE device_exit_node_prefs|DELETE FROM device_exit_node_prefs' "$ACLGRANT" 2>/dev/null | head -1)"
  [ -n "$WRITES" ] || WRITES=0
  if [ "$WRITES" = "0" ]; then
    ok "B1: the ACL generator contains no write to device_exit_node_prefs (read-only by construction)"
  else
    bad "B1: the generator writes the preference table ($WRITES write call(s)) — an ACL pass must never mutate the operator's data"
  fi
  # B2 — the withheld pin falls through to the UNPINNED emit (the device keeps egress).
  if grep -qE '^\s*sb\.WriteString.*autogroup:internet.*ip' "$ACLGRANT" 2>/dev/null \
     && [ -n "$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*ip' "$ACLGRANT" | grep -v 'via')" ]; then
    ok "B2: the unpinned grant is still emitted, so withholding the pin does not remove egress"
  else
    bad "B2: the unpinned fallback is gone — withholding the pin would leave the device with no internet grant at all"
  fi
  # B3 — and the B188.2 SHAPE survives: exactly one conditional via-bearing emit.
  PIN_EMITS="$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*via' "$ACLGRANT" 2>/dev/null | grep -v '//' | wc -l | tr -d ' ')"
  PIN_GUARD="$(grep -cE '^\s*if via := viaByDevice\[devTag\]; via != ""' "$ACLGRANT" 2>/dev/null || echo 0)"
  if [ "$PIN_EMITS" = "1" ] && [ "${PIN_GUARD:-0}" -ge 1 ]; then
    ok "B3: scripts/check_b188_2.sh contract B's shape is intact (one emit, guarded by the per-device lookup)"
  else
    bad "B3: the conditional-pin shape changed (emits=$PIN_EMITS guard=$PIN_GUARD) — renegotiate check_b188_2.sh contract B in the same change"
  fi
  # B4 — THE CONTRACT THE TASK ASKS FOR BY NAME: this path deletes or rewrites the human's
  # row nowhere. The B356 reconciler is allowed to write a DERIVED row (that is its job,
  # and it refuses a human's), and the pages write only from a form POST; what must never
  # happen is the ACL/withhold path touching the table.
  if ! grep -qE 'SetDeviceExitNodePref|DeleteDeviceExitNodePref' "$OWNERSHIP" \
     && ! grep -qE 'SetDeviceExitNodePref|DeleteDeviceExitNodePref' "$STALEPAGE"; then
    ok "B4: neither the ownership predicate nor the admin page writes the stored preference"
  else
    bad "B4: the withhold path writes device_exit_node_prefs — the operator's row would not survive"
  fi
  # B5 — the human's provenance is still read and still respected by the planner.
  if grep -q 'ExistingPrefSetByUserID != 0' "$REC" && grep -q 'stale-pref-human-pinned' "$REC"; then
    ok "B5: a HUMAN's row is still surfaced and never rewritten by the planner"
  else
    bad "B5: the human-provenance guard is gone — the engine can silently undo an operator's choice"
  fi
fi

hdr "C. a device with no rules must not be invisible (B361 item 2)"

if [ "$missing" -eq 0 ]; then
  if grep -q '"stale-pref-no-rules"' "$REC" && grep -q 'StalePrefNoRules = "stale-pref-no-rules"' "$STALE"; then
    ok "C1: the no-rules device has its own NAMED reason (never a bare skip, never a silent nil)"
  else
    bad "C1: the a71 shape has no named reason — it is invisible again"
  fi
  if grep -q 'PrefRelayOwnsPrefix' "$REC" && grep -q 'PrefRelayPrefixCount' "$REC"; then
    ok "C2: the reconciler carries the relay's ownership facts into the decision"
  else
    bad "C2: the reconciler cannot state that the pinned relay serves 0 prefixes"
  fi
  if grep -q 'func loadOwnerTagByPrefix' "$REC" && grep -q 'prefixowner.TagsByHost' "$REC"; then
    ok "C3: the ownership facts come from the SAME projection the ACL pin uses (prefixowner)"
  else
    bad "C3: the reconciler reads ownership from a second source — the two halves can disagree again"
  fi
  if grep -q 'reportStalePref' "$REC" && grep -q 'shouldAlert(ch.DeviceHostname, ch.Reason' "$REC"; then
    ok "C4: the new reason is audited and notified (rate-limited per device+reason), not only logged"
  else
    bad "C4: the new reason produces no durable trail"
  fi
fi

hdr "D. propagation through the ONE shared throttle (B361 item 4)"

if [ "$missing" -eq 0 ]; then
  if grep -q 'func takeACLApplySlot' "$SYNC" && grep -q 'func (s \*Service) ReapplyACLIfDrifted' "$SYNC"; then
    ok "D1: the shared apply slot has one implementation (takeACLApplySlot) and one exported trigger"
  else
    bad "D1: the shared apply budget is gone or duplicated"
  fi
  # D2 — no SECOND throttle: the preference path must not DECLARE a budget of its own.
  # (It takes `throttle time.Duration` as a parameter — the caller passes the shared
  # `ownershipACLThrottle` — so the check looks for a literal duration assignment, which
  # is what declaring a second budget looks like. Prose and the pass-through signature
  # are not timers.)
  if ! grep -qE '= *[0-9]+ *\* *time\.(Second|Minute|Hour)|Throttle *[:=] *[0-9]' "$REAPPLY"; then
    ok "D2: the preference trigger declares no budget of its own (it spends the shared apply slot)"
  else
    bad "D2: the preference path declares its own throttle — two budgets can starve each other"
  fi
  if grep -q 'return takeACLApplySlot(now, throttle)' "$REAPPLY"; then
    ok "D3: TakeACLReapplySlot IS takeACLApplySlot (one budget, shared with the ownership path)"
  else
    bad "D3: the exported slot is a copy, not the same function"
  fi
  if grep -q 'ReapplyACLAfterPreferenceChange' "$MYHANDLER" && grep -rq --include='*.go' 'SetPreferenceACLReapply' "$MAINPKG"; then
    ok "D4: the preference handlers call the trigger and main.go wires it to the exit-rules drift check"
  else
    bad "D4: a stored preference change does not reach the ACL promptly (the live ~65-minute convergence)"
  fi
  # D5 — a nil hook is a no-op, never a panic: the write must not depend on the trigger.
  if grep -q 'if prefReapply == nil' "$REAPPLY"; then
    ok "D5: an unwired trigger is a no-op (a preference write never fails because of it)"
  else
    bad "D5: an unwired trigger panics"
  fi
fi

hdr "E. visible and fixable on BOTH pages, with i18n keys in BOTH catalogues"

if [ "$missing" -eq 0 ]; then
  if grep -q 'ServesLine' "$STALEPAGE" && grep -q 'serves_nothing' "$STALEPAGE"; then
    ok "E1: /admin/exit-nodes builds the operator's sentence from the catalogue"
  else
    bad "E1: the admin page does not render the named sentence"
  fi
  if grep -q 'func (s \*Service) LoadStaleDevicePins' "$MYPIN" \
     && grep -q 'LoadStaleDevicePins' "$REPO_ROOT/internal/feature/my/exit_nodes.go" \
     && grep -q 'StalePins' "$MYTMPL"; then
    ok "E2: /my/exit-nodes shows the user's own pinned devices (the page a non-admin can open)"
  else
    bad "E2: the user page says nothing about the user's own device"
  fi
  # The one-click fix posts to the EXISTING endpoints — no new write path.
  if grep -q 'action="/admin/devices/preferred-exit"' "$ADMINTMPL" \
     && grep -q 'action="/my/devices/preferred-exit"' "$MYTMPL"; then
    ok "E3: both pages offer the one-click switch/clear through the existing preference endpoints"
  else
    bad "E3: the fix control is missing (or posts somewhere new — a second write path)"
  fi
  # E4 — the switch carries the candidate relay AND the stored via flag, and the clear
  # sends an empty tag — a control that sent the wrong value would re-pin the device.
  if grep -q 'name="tag" value="{{.CandidateTag}}"' "$ADMINTMPL" \
     && grep -q 'name="tag" value=""' "$ADMINTMPL" \
     && grep -q 'name="tag" value="{{.CandidateTag}}"' "$MYTMPL" \
     && grep -q 'name="tag" value=""' "$MYTMPL"; then
    ok "E4: the switch posts the candidate relay and the clear posts an empty tag"
  else
    bad "E4: the fix control does not carry the right hidden fields"
  fi
  KEYS="stale_pref_serves_nothing stale_pref_no_rules_line stale_pref_rules_line stale_pref_action_switch stale_pref_action_clear stale_pref_action_help stale_pref_cause_no_rules stale_pref_serves_nothing_user stale_pref_title_user stale_pref_action_switch_user stale_pref_action_clear_user stale_pref_action_help_user"
  for k in $KEYS; do
    n="$(grep -c "\"exit_nodes.prefix_owner.$k\"" "$CAT" 2>/dev/null || echo 0)"
    if [ "$n" -eq 2 ]; then
      ok "E5: i18n key exit_nodes.prefix_owner.$k is defined in BOTH catalogues (RU + EN)"
    else
      bad "E5: exit_nodes.prefix_owner.$k is defined $n time(s), want 2 (RU + EN — AGENTS rule 10)"
    fi
  done
  # E6 — every key the two templates use must exist in the catalogue (a key used but
  # defined nowhere passes TestCatalogsParity happily).
  TPL_KEYS="$(grep -ohE '\{\{t "exit_nodes\.prefix_owner\.stale_pref[a-z_]*"' "$ADMINTMPL" "$MYTMPL" 2>/dev/null | sed 's/.*t "//; s/"$//' | sort -u || true)"
  MISSING_KEYS=""
  for k in $TPL_KEYS; do
    if ! grep -q "\"$k\"" "$CAT"; then MISSING_KEYS="$MISSING_KEYS $k"; fi
  done
  if [ -z "$MISSING_KEYS" ] && [ -n "$TPL_KEYS" ]; then
    ok "E6: every stale_pref key the templates use is defined in the catalogues ($(printf '%s' "$TPL_KEYS" | wc -w | tr -d ' ') key(s))"
  elif [ -z "$TPL_KEYS" ]; then
    bad "E6: the banners use no i18n keys at all — the strings would be hardcoded (B325)"
  else
    bad "E6: template keys with no catalogue entry:$MISSING_KEYS"
  fi
fi

hdr "F. the scripted timelines actually run"

GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go" "/c/Program Files/Go/bin/go.exe" "/mnt/c/Program Files/Go/bin/go.exe"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "F1-F6: go is not reachable from this shell — run the B361 tests on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/acl/ -run 'B361' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F1: the a71 timeline passes (no rules + ownerless relay → unpinned grant, row untouched)"
  else
    bad "F1: the B361 ACL tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/acl/ -run 'TestB361_HealthyButOwnerlessRelayNeverGetsThePin' -count=1 -v 2>&1)" \
     && grep -q -- '--- PASS' <<< "$OUT"; then
    ok "F2: the exact B356 gap is covered (the relay is 'online' in the monitor AND owns nothing → no pin)"
  else
    bad "F2: the online-but-ownerless case is not covered: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/acl/ -run 'B356' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F3: the B356 health cases still pass (the ownership gate did not replace the health gate)"
  else
    bad "F3: the B356 ACL tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B361' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F4: the propagation timelines pass (prompt re-apply; a burst is one apply)"
  else
    bad "F4: the B361 propagation tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B356' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F5: the B356 reconciler/page cases still pass (the no-rules reason is the sharper one)"
  else
    bad "F5: the B356 reconciler tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/feature/admin/ -run 'B356Page|B361Page' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F6: the page rows pass (a71 shown with its reason, candidate and human badge; a working device silent)"
  else
    bad "F6: the B361 page tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  GOFMT_BIN="$(command -v gofmt 2>/dev/null || true)"
  if [ -z "$GOFMT_BIN" ] && [ -x "$(dirname "$GO_BIN")/gofmt" ]; then GOFMT_BIN="$(dirname "$GO_BIN")/gofmt"; fi
  if [ -z "$GOFMT_BIN" ] && [ -x "$(dirname "$GO_BIN")/gofmt.exe" ]; then GOFMT_BIN="$(dirname "$GO_BIN")/gofmt.exe"; fi
  if [ -z "$GOFMT_BIN" ]; then
    skip "F7: gofmt is not reachable — the B337 ratchet covers these files on the VM"
  else
    DRIFT="$("$GOFMT_BIN" -l internal/acl/acl_relay_ownership_b361.go internal/acl/acl_b361_test.go internal/acl/acl_generate_via.go internal/feature/exit_rules/reconciler.go internal/feature/exit_rules/pref_staleness_b356.go internal/feature/exit_rules/pref_reapply_b361.go internal/feature/exit_rules/pref_reapply_b361_test.go internal/feature/exit_rules/sync.go internal/feature/admin/exit_nodes_stale_prefs_b356.go internal/feature/admin/exit_nodes_stale_prefs_b356_test.go internal/feature/admin/exit_nodes_page.go internal/feature/my/device_exit_pin_b361.go internal/feature/my/device_exit_pref.go internal/feature/my/exit_nodes.go internal/feature/exit_rules/reconciler_b356_test.go internal/feature/exit_rules/reconciler_b356_collector_test.go internal/handlers/exit_nodes_render_test.go internal/handlers/exit_nodes_user_render_test.go internal/i18n/catalog_exit_nodes.go "$MAINFILE_REL" 2>&1)"
    if [ -z "$DRIFT" ]; then
      ok "F7: every file this block created or touched is gofmt-clean (B337 ratchet)"
    else
      bad "F7: gofmt drift in: $(printf '%s' "$DRIFT" | tr '\n' ' ')"
    fi
  fi
fi

hdr "G. tracked, not ignored, and registered"

if git check-ignore -q scripts/check_b361_pin_without_routing.sh 2>/dev/null; then
  bad "G1: .gitignore swallows this script (AGENTS trap #11) — it would never reach the VM"
else
  ok "G1: no .gitignore rule matches this script"
fi
if git ls-files --error-unmatch scripts/check_b361_pin_without_routing.sh >/dev/null 2>&1; then
  ok "G2: this script is tracked by git"
else
  skip "G2: this script is not tracked YET — it is a new file in the working tree and the lead commits it (G1 is why this is safe)"
fi
# G3 — THE CONTRACT THE TASK ASKS FOR BY NAME, behaviourally: the human's ROW survives the
# withhold. It is asserted by the Go timeline (F1) and re-checked here from the test's own
# source so a future edit that drops the assertion is caught.
if grep -q 'the stored preference is GONE' "$ACLTEST" && grep -q 'set_by_user_id' "$ACLTEST"; then
  ok "G3: the a71 timeline asserts the stored row (with set_by_user_id) is untouched"
else
  bad "G3: the timeline no longer proves the human's row survives"
fi
if grep -q 'check_b361_pin_without_routing.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "G4: registered in scripts/verify_pre_deploy.sh"
else
  skip "G4: not registered in scripts/verify_pre_deploy.sh yet — the exact run_check line is part of the B361 report; once the lead adds it this turns green"
fi
if grep -q 'B361' AGENTS.md 2>/dev/null; then
  ok "G5: recorded in the AGENTS.md block index"
else
  skip "G5: no B361 line in the AGENTS.md block index yet — the lead adds the one-line bullet from the B361 report"
fi

hdr "H. live: how many devices are pinned to a relay that serves nothing"

if [ -f "$REPO_ROOT/scripts/lib/db_credentials.sh" ]; then
  # shellcheck source=lib/db_credentials.sh
  . "$REPO_ROOT/scripts/lib/db_credentials.sh"
  if skygate_live_db_probe; then
    LIVE="$SKY_TMP/live_pins.txt"
    skygate_live_db_query \
      "SELECT p.device_hostname || '|' || p.exit_node_tag || '|' || p.set_by_user_id || '|' ||
              COALESCE((SELECT COUNT(*) FROM device_rules r WHERE r.user_id = p.user_id AND r.enabled = 1
                          AND r.target_type IN ('subnet','ip')
                          AND (r.device_hostname = p.device_hostname
                               OR CAST(r.device_id AS TEXT) IN (SELECT node_id FROM node_owner_map WHERE hostname = p.device_hostname))), 0) || '|' ||
              COALESCE((SELECT COUNT(*) FROM prefix_owner po JOIN node_owner_map n ON LOWER(n.hostname) = LOWER(po.exit_node_id)
                         WHERE n.tag = p.exit_node_tag), 0)
         FROM device_exit_node_prefs p
        WHERE COALESCE(p.device_hostname,'') <> '' AND COALESCE(p.exit_node_tag,'') <> ''" \
      > "$LIVE" 2>/dev/null || true
    ROWS="$(wc -l < "$LIVE" | tr -d ' ')"
    if [ "${ROWS:-0}" = "0" ]; then
      info "H1: no per-device preference rows in the live database"
    else
      info "H1: live device pins (device|tag|set_by_user_id|rules|prefixes owned by the pinned relay):"
      sed 's/^/        /' "$LIVE"
      # The a71 shape: rules=0 and prefixes=0. Reported, never failed — this host is the
      # pre-fix state by definition, and the informational line is what the operator reads.
      NORULES="$(awk -F'|' '$4+0 == 0 && $5+0 == 0' "$LIVE" | wc -l | tr -d ' ')"
      if [ "${NORULES:-0}" = "0" ]; then
        ok "H2: no device is pinned to a relay that serves nothing"
      else
        info "H2: $NORULES device(s) match the a71 shape (no rules of their own AND a pinned relay that owns 0 prefixes) — the ACL withholds their pin from the B361 build on, and both pages name them"
      fi
    fi
  else
    skip "H1-H2: $(skygate_live_db_reason)"
  fi
else
  skip "H1-H2: scripts/lib/db_credentials.sh is missing"
fi

hdr "B361 summary: $PASS passed, $FAIL failed, $SKIP skipped"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
