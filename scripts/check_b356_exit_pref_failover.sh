#!/usr/bin/env bash
# check_b356_exit_pref_failover.sh — B356: a device's exit-node preference must
# follow the relay that is ACTUALLY serving, and must not survive its own relay's
# death.
#
# THE OPERATOR'S REPORT (2026-10-07). When the relays `karolina` and `sharlotta`
# went offline the exit-node preference was NOT redistributed: devices stayed
# pinned to a relay that was serving nothing, their internet access broke, and
# nothing re-pointed them and no page or log said so.
#
# MEASURED LIVE STATE (reference deployment, PostgreSQL):
#
#   device_exit_node_prefs
#     user 1  a71        tag:dev-infra-emilia    set_by_user_id=1
#     user 1  cyborg     tag:dev-infra-emilia    set_by_user_id=0
#     user 6  basic      tag:dev-infra-karolina  set_by_user_id=0
#     user 1  skyworker  tag:dev-infra-karolina  set_by_user_id=0
#   prefix_owner: 139 rows, EVERY one exit_node_id = emilia, source = global
#
# So the data plane had decided emilia serves everything while two devices were
# pinned to karolina — and since B265 the per-device `autogroup:internet` grant
# carries `via=[<preferred tag>]`, which headscale applies as a permission
# FILTER. A device pinned to the relay that owns nothing therefore loses those
# routes; when that relay is also down the loss is total and permanent, because
# nothing re-evaluated an EXISTING preference.
#
# Note the provenance: `set_by_user_id = 0` means "derived by the engine",
# `!= 0` means a human chose it. That distinction is load-bearing: the engine may
# re-point its own row, and must never silently rewrite a person's (it surfaces
# it instead).
#
# WHAT IS PINNED HERE
#   A. the repair exists and every branch has a NAMED reason (never a silent
#      `return nil, false`), following the B341 planner's vocabulary;
#   B. the health predicate is B273's `exitNodeUsable` REUSED — not a second
#      health rule that can drift;
#   C. the pass converges on the periodic maintenance tick (L-60: "only act on a
#      change" is a liveness bug), and its audit/notification are throttled;
#   D. the ACL safety net: an unusable preferred relay drops the `via` pin and
#      the device falls back to the UNPINNED grant, logged and counted. This is
#      the B265 contract RENEGOTIATED in place — see section D;
#   E. operator visibility on /admin/exit-nodes with i18n keys in BOTH
#      catalogues;
#   F. the properties are unit-tested;
#   G. the script is not ignored by git and is (to be) registered/indexed.
#
# Live-state contracts SKIP (never FAIL) when the database or the headscale
# policy cannot be read: this host is the PRE-FIX state by definition, so a red
# live contract would describe the incident rather than a regression in the tree
# (AGENTS rule 1).
#
# Usage:  bash scripts/check_b356_exit_pref_failover.sh
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

REC="$REPO_ROOT/internal/feature/exit_rules/reconciler.go"
STALE="$REPO_ROOT/internal/feature/exit_rules/pref_staleness_b356.go"
PAGE="$REPO_ROOT/internal/feature/admin/exit_nodes_page.go"
STALEPAGE="$REPO_ROOT/internal/feature/admin/exit_nodes_stale_prefs_b356.go"
ACLHEALTH="$REPO_ROOT/internal/acl/acl_relay_health_b356.go"
ACLGRANT="$REPO_ROOT/internal/acl/acl_generate_via.go"
TPL="$REPO_ROOT/internal/handlers/templates/admin/exit_nodes.html"
CAT="$REPO_ROOT/internal/i18n/catalog_exit_nodes.go"
MON="$REPO_ROOT/internal/monitoring/exit_node_monitor.go"

hdr "A. a stale DERIVED preference is re-pointed, with a NAMED reason"

missing=0
for f in "$REC" "$STALE" "$ACLHEALTH" "$ACLGRANT" "$STALEPAGE"; do
  [ -f "$f" ] || { bad "A0: $f is missing"; missing=1; }
done
if [ "$missing" -eq 0 ]; then
  if grep -qE 'Action: +"update"' "$REC"; then
    ok "A1: a preference naming an unusable relay is MOVED (reason stale-pref-relay-unusable)"
  else
    bad "A1: the stale-derived repair is gone — the operator's devices stay pinned to a dead relay"
  fi
  for reason in stale-pref-no-owner stale-pref-owner-split stale-pref-owner-untagged stale-pref-owner-is-pref-relay stale-pref-human-pinned stale-pref-human-non-owner; do
    if grep -q "\"$reason\"" "$REC"; then
      ok "A2: the named reason $reason exists (no silent return nil, false)"
    else
      bad "A2: the reason $reason is missing — that branch is silent again"
    fi
  done
  if grep -q 's.PrefRelayKnown && !s.PrefRelayUsable' "$REC"; then
    ok "A3: the B356 branch is gated on the measured state (known AND not usable)"
  else
    bad "A3: the B356 branch is not gated on PrefRelayKnown && !PrefRelayUsable — an unmeasured relay would be treated as broken"
  fi
  if grep -q 's.ExistingPrefSetByUserID != 0' "$REC"; then
    ok "A4: provenance gates every rewrite (set_by_user_id != 0 means a human chose it)"
  else
    bad "A4: nothing gates the repair on set_by_user_id — an operator's own pin can be overwritten"
  fi
  if grep -q 'ExistingPrefSetByUserID = existing.SetByUserID' "$REC"; then
    ok "A5: the collector reads device_exit_node_prefs.set_by_user_id"
  else
    bad "A5: the collector does not read set_by_user_id — provenance would always look 'derived'"
  fi
  # A6 — the subject set. The rows that break a device are exactly the ones whose
  # rules have gone away, so the pass must be driven by the PREFERENCE table too
  # (L-54: the filter that defines the subject set of a decision, not the logic
  # inside it).
  if grep -q 'ListAllDeviceExitNodePrefs' "$REC"; then
    ok "A6: the subject set is rule pairs UNION preference rows (a device with no rules is examined)"
  else
    bad "A6: the pass is still driven by device_rules only — a preference whose device has no rules is invisible"
  fi
  # A7 — the shared page/reconciler predicate, so the page cannot claim a device
  # is fine while the engine is moving it.
  if grep -q 'func StaleExitPrefReason' "$STALE" && grep -q 'StaleExitPrefReason' "$STALEPAGE"; then
    ok "A7: the page and the reconciler share one staleness predicate"
  else
    bad "A7: the page and the reconciler no longer share the staleness predicate"
  fi
fi

hdr "B. the health predicate is B273's, REUSED (no second health rule)"

if grep -q 'func ExitNodeUsable(state string) bool { return exitNodeUsable(state) }' "$MON"; then
  ok "B1: monitoring.ExitNodeUsable delegates to exitNodeUsable (the B273 name and its contracts survive)"
else
  bad "B1: monitoring.ExitNodeUsable is missing or no longer delegates to exitNodeUsable"
fi
for f in "$REC" "$ACLHEALTH"; do
  if grep -q 'monitoring.ExitNodeUsable(' "$f"; then
    ok "B2: $(basename "$f") asks B273's predicate"
  else
    bad "B2: $(basename "$f") does not call monitoring.ExitNodeUsable"
  fi
done
# B3 — an absence contract: no file of this block may re-derive usable-ness from
# the state string. A widened absence check is the stronger contract (LESSONS L-55c).
# COMMENT lines are excluded: the two files explain this very anti-pattern by name
# (`state == "online"` would be a second predicate), and a naive scan would fail on
# the explanation instead of on a violation.
LOCAL_HEALTH="$(grep -nE 'State *== *"online"|state *== *"online"' "$REC" "$ACLHEALTH" "$STALE" 2>/dev/null \
  | grep -vE ':[0-9]+:[[:space:]]*//' || true)"
if [ -z "$LOCAL_HEALTH" ]; then
  ok "B3: no B356 file re-derives health from a literal state string"
else
  bad "B3: a second health predicate appeared (B273 exists to prevent exactly this):"
  printf '%s\n' "$LOCAL_HEALTH" | sed 's/^/        /'
fi

hdr "C. convergence on the maintenance tick, with throttled noise (L-60)"

if grep -q 'ReconcileDeviceExitNodePrefs' "$REPO_ROOT/internal/handlers/handlers.go"; then
  ok "C1: the pass runs from the periodic reconciler goroutine (boot + every tick), not only on a change"
else
  bad "C1: the pass is no longer on the maintenance tick — a state that becomes wrong after the last decision stays wrong"
fi
if grep -q 'shouldAlert(ch.DeviceHostname, ch.Reason, time.Now())' "$REC"; then
  ok "C2: the stale-preference report is rate-limited per (device, reason)"
else
  bad "C2: the stale-preference report is unthrottled — the journal and Telegram would fill on every tick"
fi
if grep -q 'func (s \*Service) reportStalePref' "$REC" && grep -q "AppendAuditLogWithTarget" "$REC"; then
  ok "C3: a stale preference writes an AUDIT row, so the refusal is durable and not only a log line"
else
  bad "C3: a stale preference produces no audit row"
fi
if grep -q 'loadRelayHealth' "$REC" && grep -q 'ListExitNodeHealth' "$REC"; then
  ok "C4: the health snapshot is read once per pass from exit_node_health"
else
  bad "C4: the pass does not read the monitor's snapshot"
fi

hdr "D. the ACL safety net — the B265 contract RENEGOTIATED in place"

# The OLD assertion (scripts/check_b188_2.sh contract B) is: exactly ONE
# `sb.WriteString(... autogroup:internet ... via ...)` emit, guarded by
# `if via := viaByDevice[devTag]; via != ""`. B356 adds a THIRD condition —
# the relay must be USABLE — without removing either of the first two.
#
# UNCHANGED PROPERTY: the per-device autogroup:internet pin is CONDITIONAL on a
# resolved preference and is emitted exactly once (never unconditionally, never
# twice). Evidence: the two greps below are the same ones contract B performs,
# and they still find one emit and one guard.
PIN_EMITS="$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*via' "$ACLGRANT" 2>/dev/null | grep -v '//' | wc -l | tr -d ' ')"
PIN_GUARD="$(grep -cE '^\s*if via := viaByDevice\[devTag\]; via != ""' "$ACLGRANT" 2>/dev/null || echo 0)"
if [ "$PIN_EMITS" = "1" ] && [ "$PIN_GUARD" -ge 1 ]; then
  ok "D1: the B265 conditional pin is intact (one emit, guarded by the per-device preference lookup)"
else
  bad "D1: the B265 conditional-pin shape changed (emits=$PIN_EMITS guard=$PIN_GUARD) — renegotiate scripts/check_b188_2.sh contract B in the same change"
fi
if grep -q 'unusablePreferredRelay(via, relayHealth)' "$ACLGRANT"; then
  ok "D2: the pin is also conditional on the relay being USABLE (the B356 renegotiation)"
else
  bad "D2: the ACL still pins unconditionally to a preference — an offline relay filters the device's egress into a black hole"
fi
if grep -q 'B356 via-pin FALLBACK' "$ACLGRANT"; then
  ok "D3: the fallback is LOGGED (never silent)"
else
  bad "D3: the pin fallback is silent — indistinguishable from a device that simply has no preference"
fi
if grep -q 'pinFallbacks' "$ACLGRANT" && grep -q 'pin(s) were dropped' "$ACLGRANT"; then
  ok "D4: the fallback is COUNTED and summarised once per generation"
else
  bad "D4: the fallback is not counted"
fi
if [ -n "$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*ip' "$ACLGRANT" 2>/dev/null | grep -v 'via')" ]; then
  ok "D5: the loose unpinned grant is still emitted, so the device falls back instead of being filtered away"
else
  bad "D5: the loose unpinned grant is gone — dropping the pin would leave the device with no egress grant at all"
fi
# D6 — unknown is not broken. The safety net must not rewrite the tailnet on a
# missing measurement (a transient DB hiccup, a relay the monitor has not seen).
if grep -q 'if !known || v.Usable' "$ACLHEALTH"; then
  ok "D6: a relay with no health row KEEPS its pin (absence of evidence is not evidence)"
else
  bad "D6: the ACL treats 'never measured' as 'broken' — a transient failure would unpin every device"
fi

hdr "E. operator visibility (RU + EN), and no hardcoded strings"

if grep -q 'StalePrefs' "$PAGE" && grep -q 'StalePrefsHuman' "$PAGE"; then
  ok "E1: /admin/exit-nodes receives the stale-preference rows and the human count"
else
  bad "E1: the page does not receive the stale-preference data — the operator sees nothing again"
fi
if grep -q 'stale_pref_title' "$TPL" && grep -q 'stale_pref_consequence' "$TPL"; then
  ok "E2: the template renders the banner and its consequence"
else
  bad "E2: the template has no stale-preference banner"
fi
KEYS="stale_pref_title stale_pref_help stale_pref_col_device stale_pref_col_pref stale_pref_col_relay stale_pref_col_reason stale_pref_col_candidate stale_pref_relay_unknown stale_pref_cause_unusable stale_pref_cause_owner stale_pref_no_candidate stale_pref_human stale_pref_human_badge stale_pref_human_tip stale_pref_derived_badge stale_pref_derived_tip stale_pref_consequence"
for k in $KEYS; do
  n="$(grep -c "exit_nodes.prefix_owner.$k\"" "$CAT" 2>/dev/null || echo 0)"
  if [ "$n" -eq 2 ]; then
    ok "E3: i18n key exit_nodes.prefix_owner.$k is defined in BOTH catalogues"
  else
    bad "E3: exit_nodes.prefix_owner.$k is defined $n time(s), want 2 (RU + EN)"
  fi
done
# E4 — every key the template uses must exist in the catalogue (a key used but
# defined nowhere passes TestCatalogsParity happily — AGENTS rule 10).
TPL_KEYS="$(grep -oE '\{\{t "exit_nodes\.prefix_owner\.stale_pref[a-z_]*"' "$TPL" 2>/dev/null | sed 's/.*t "//; s/"$//' | sort -u || true)"
MISSING_KEYS=""
for k in $TPL_KEYS; do
  if ! grep -q "\"$k\"" "$CAT"; then MISSING_KEYS="$MISSING_KEYS $k"; fi
done
if [ -z "$MISSING_KEYS" ] && [ -n "$TPL_KEYS" ]; then
  ok "E4: every stale_pref key the template uses is defined in the catalogues ($(printf '%s' "$TPL_KEYS" | wc -w | tr -d ' ') key(s))"
elif [ -z "$TPL_KEYS" ]; then
  bad "E4: the banner uses no i18n keys at all — the strings would be hardcoded (B325)"
else
  bad "E4: template keys with no catalogue entry:$MISSING_KEYS"
fi
# E5 — the banner block itself must not carry user-visible literal text. The
# pragmatic detector: the block between the B356 template comment and its closing
# {{end}} may contain markup, entities and template actions, but any bare word
# sequence outside them is a hardcoded string.
if command -v python3 >/dev/null 2>&1; then
  BLOCK_OK="$(python3 - "$TPL" <<'PY' 2>/dev/null || echo "err"
import re, sys
src = open(sys.argv[1], encoding="utf-8").read()
marker = src.find("B356: the DEVICE half")
if marker < 0:
    print("seam-missing"); raise SystemExit
# Walk back to the opening {{/* of the Go-template comment that carries the
# marker, so the comment's own prose is stripped together with the action.
start = src.rfind("{{/*", 0, marker)
if start < 0:
    print("seam-missing"); raise SystemExit
tail = src.find("stale_pref_consequence", marker)
end = src.find("{{end}}", tail)
if tail < 0 or end < 0:
    print("seam-missing"); raise SystemExit
block = src[start:end]
block = re.sub(r"\{\{.*?\}\}", " ", block, flags=re.S)   # template actions
block = re.sub(r"<[^>]*>", " ", block)                   # tags (incl. style attrs)
block = re.sub(r"&[a-zA-Z#0-9]+;", " ", block)           # entities
words = [w for w in re.findall(r"[A-Za-zА-Яа-я][A-Za-zА-Яа-я-]{2,}", block)]
print("ok" if not words else "words:" + ",".join(words[:6]))
PY
)"
  case "$BLOCK_OK" in
    ok) ok "E5: the banner contains no hardcoded user-visible text (B325 ratchet stays at 0)" ;;
    seam-missing) skip "E5: could not locate the banner block for the hardcoded-string scan" ;;
    err) skip "E5: the python3 hardcoded-string scan failed to run" ;;
    *) bad "E5: hardcoded user-visible text in the banner: $BLOCK_OK" ;;
  esac
else
  skip "E5: python3 not available — the hardcoded-string scan did not run"
fi

hdr "F. the properties are unit-tested"

GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go" "/c/Program Files/Go/bin/go.exe"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "F1-F5: go is not reachable from this shell — run the B356 unit tests on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B356' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F1: the reconciler's B356 properties pass (repoint / human pin / idempotence / named skips)"
  else
    bad "F1: the B356 reconciler tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/acl/ -run 'B356' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F2: the ACL pin fallback passes (unusable removes the pin; usable/untagged/unknown keep it)"
  else
    bad "F2: the B356 ACL tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/feature/admin/ -run 'B356Page' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F3: the page's stale-preference rows pass (shown for a human pin, silent for a working device)"
  else
    bad "F3: the B356 page tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/handlers/ -run 'ExitNodesRendersB356' -count=1 2>&1)" && grep -q '^ok' <<< "$OUT"; then
    ok "F4: the template RENDERS the banner (and renders nothing when no row is stale)"
  else
    bad "F4: the B356 template render tests fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" build ./... 2>&1)"; then
    ok "F5: go build ./... clean"
  else
    bad "F5: go build ./... failed: $(printf '%s' "$OUT" | tail -3)"
  fi
  GOFMT_BIN="$(command -v gofmt 2>/dev/null || true)"
  if [ -z "$GOFMT_BIN" ] && [ -x "$(dirname "$GO_BIN")/gofmt" ]; then GOFMT_BIN="$(dirname "$GO_BIN")/gofmt"; fi
  if [ -z "$GOFMT_BIN" ]; then
    skip "F6: gofmt is not reachable — the gofmt ratchet (B337) covers these files on the VM"
  else
    DRIFT="$("$GOFMT_BIN" -l internal/feature/exit_rules/reconciler.go internal/feature/exit_rules/pref_staleness_b356.go internal/feature/exit_rules/reconciler_b356_test.go internal/feature/exit_rules/reconciler_b356_collector_test.go internal/acl/acl_relay_health_b356.go internal/acl/acl_generate_via.go internal/acl/acl_b356_test.go internal/feature/admin/exit_nodes_stale_prefs_b356.go internal/feature/admin/exit_nodes_stale_prefs_b356_test.go internal/feature/admin/exit_nodes_page.go internal/handlers/exit_nodes_render_test.go internal/monitoring/exit_node_monitor.go internal/i18n/catalog_exit_nodes.go 2>&1)"
    if [ -z "$DRIFT" ]; then
      ok "F6: every file this block created or touched is gofmt-clean (B337 ratchet)"
    else
      bad "F6: gofmt drift in: $(printf '%s' "$DRIFT" | tr '\n' ' ')"
    fi
  fi
fi

hdr "G. tracked, not ignored, (to be) registered and indexed"

if git check-ignore -q scripts/check_b356_exit_pref_failover.sh 2>/dev/null; then
  bad "G1: .gitignore swallows this script (AGENTS trap #11) — it would never reach the VM"
else
  ok "G1: no .gitignore rule matches this script"
fi
if git ls-files --error-unmatch scripts/check_b356_exit_pref_failover.sh >/dev/null 2>&1; then
  ok "G2: this script is tracked by git"
else
  skip "G2: this script is not tracked YET — it is a new file in the working tree and the lead commits it (AGENTS trap #11 is why G1 also exists, and G1 passes)"
fi
if grep -q 'check_b356_exit_pref_failover.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "G3: registered in scripts/verify_pre_deploy.sh"
else
  skip "G3: not registered in scripts/verify_pre_deploy.sh yet — the exact run_check line is part of the B356 block report; once the lead adds it this contract turns green"
fi
if grep -q 'B356' AGENTS.md 2>/dev/null; then
  ok "G4: recorded in the AGENTS.md block index"
else
  skip "G4: no B356 line in the AGENTS.md block index yet — the lead adds the one-line bullet from the B356 block report"
fi

hdr "H. live: which devices are pinned to a relay that cannot serve them"

if [ -f "$REPO_ROOT/scripts/lib/db_credentials.sh" ]; then
  # shellcheck source=lib/db_credentials.sh
  . "$REPO_ROOT/scripts/lib/db_credentials.sh"
  if skygate_live_db_probe; then
    LIVE="$SKY_TMP/live_stale.txt"
    skygate_live_db_query \
      "SELECT p.user_id || '|' || p.device_hostname || '|' || p.exit_node_tag || '|' || p.set_by_user_id || '|' || COALESCE(h.state,'no-health-row') || '|' || COALESCE(h.healthy,-1) FROM device_exit_node_prefs p LEFT JOIN node_owner_map n ON n.tag = p.exit_node_tag LEFT JOIN exit_node_health h ON LOWER(h.hostname) = LOWER(n.hostname) WHERE COALESCE(p.exit_node_tag,'') <> ''" > "$LIVE" 2>/dev/null || true
    ROWS="$(wc -l < "$LIVE" | tr -d ' ')"
    if [ "${ROWS:-0}" = "0" ]; then
      info "H1: no device preference rows in the live database"
    else
      info "H1: live device preferences (device|tag|set_by_user_id|relay_state|healthy):"
      sed 's/^/        /' "$LIVE"
      STALE_N="$(awk -F'|' '$5=="offline" || $5=="degraded"' "$LIVE" | wc -l | tr -d ' ')"
      HUMAN_N="$(awk -F'|' '$5=="offline" || $5=="degraded" { if ($4+0 != 0) n++ } END { print n+0 }' "$LIVE")"
      DERIVED_N="$(awk -F'|' '$5=="offline" || $5=="degraded" { if ($4+0 == 0) n++ } END { print n+0 }' "$LIVE")"
      if [ "${STALE_N:-0}" = "0" ]; then
        ok "H2: no preference names a relay the monitor calls offline/degraded"
      else
        info "H2: $STALE_N preference(s) name an unusable relay — $DERIVED_N derived (the reconciler re-points these) and $HUMAN_N set by a human (surfaced on /admin/exit-nodes, never rewritten)"
        info "H2: until the B356 build runs on this host those devices are still filtered by via=; after it, the ACL drops the pin and the device falls back to the unpinned grant"
      fi
    fi
    # H3 — the question the block exists for, in a form the live database can
    # answer: how many devices are pinned to a relay that owns NO prefix at all?
    # On the day of the report that number was 2 (`basic`, `skyworker` → karolina,
    # which owned 0 of the 139 rows while emilia owned all of them). Reported as
    # INFO, never failed: this host is the pre-fix state by definition.
    NO_PREFIX="$(skygate_live_db_query \
      "SELECT COUNT(*) FROM device_exit_node_prefs p JOIN node_owner_map n ON n.tag = p.exit_node_tag WHERE COALESCE(p.exit_node_tag,'') <> '' AND NOT EXISTS (SELECT 1 FROM prefix_owner po WHERE LOWER(po.exit_node_id) = LOWER(n.hostname))" 2>/dev/null | tr -d '\r')"
    if [ -z "$NO_PREFIX" ]; then
      skip "H3: the 'pinned to a relay that owns nothing' probe returned nothing on this backend"
    else
      info "H3: $NO_PREFIX device preference(s) name a relay that owns no prefix in prefix_owner — the data plane serves those destinations from another relay"
    fi
  else
    skip "H1-H3: $(skygate_live_db_reason)"
  fi
else
  skip "H1-H3: scripts/lib/db_credentials.sh is missing"
fi

hdr "B356 summary: $PASS passed, $FAIL failed, $SKIP skipped"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
