#!/usr/bin/env bash
# check_b398_exit_node_pin.sh
#
# 2026-10-18 (B398) — A DERIVED EXIT-NODE PREFERENCE MUST NOT BECOME A PERMISSION
# FILTER, AND THE DEVICE'S OWN CHOICE MUST BE A CLAIM.
#
# WHY (measured on the operator's installation)
#   `device_exit_node_prefs` held `cyborg → tag:dev-infra-karolina` with
#   `set_by_user_id=0` — written by the auto-reconciler, not by a human — and the ACL
#   generator turned it into
#
#     { "src": ["tag:dev-skyadmin-cyborg"], "dst": ["autogroup:internet"],
#       "ip": ["*"], "via": ["tag:dev-infra-karolina"] }
#
#   `via` on an `autogroup:internet` grant is a SELECTOR, not a preference: choosing
#   emilia in the Tailscale client made the grant unmatched, headscale dropped ALL of
#   that device's exit-node traffic, and the operator reported «если выставить emilia,
#   то нет доступа ни к чему; karolina — всё грузится». Clearing the row from the panel
#   restored access. Nothing on any page said the device was restricted.
#
#   The mirror-image defect sat in the assignment: cyborg's 16 Google rules all carry an
#   EMPTY `exit_node_id` ("the engine decides"), so they voted for NO relay and the
#   engine spread those prefixes over emilia and sharlotta — relays the device never
#   chose (B397 then had to teach the sync to advertise them).
#
# CONTRACTS
#   A. the ACL pins only an EXPLICIT (human) preference; a derived one is left unpinned
#   B. a device preference VOTES for its own relay-less rules, in the right unit
#   C. the operator can SEE the pin: a diagnostic test + its remediation text
#   D. behavioural pins (Go), registration and documentation
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9/B386).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/acl/acl_generate_via.go ] || {
  printf 'B398: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b398.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

ACL=internal/acl/acl_generate_via.go
OWNER=internal/prefixowner/prefixowner.go
TESTS_DEF=internal/feature/admin/system_tests_b394_new.go
TESTS_ANN=internal/feature/admin/system_tests_diagnostics_b394.go
CAT=internal/i18n/catalog_common.go
ACL_TEST=internal/acl/acl_b398_test.go
PO_TEST=internal/prefixowner/prefixowner_b398_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

ACL_SRC="$(src "$ACL")"; OWNER_SRC="$(src "$OWNER")"
DEF_SRC="$(src "$TESTS_DEF")"; ANN_SRC="$(src "$TESTS_ANN")"; CAT_SRC="$(src "$CAT")"
ACL_TEST_SRC="$(src "$ACL_TEST")"; PO_TEST_SRC="$(src "$PO_TEST")"

hdr "B398 — a preference is not a filter, and a device's choice is a claim"

# ---------------------------------------------------------------------------
# A. the ACL pins only an explicit choice
# ---------------------------------------------------------------------------
hdr "A — only a HUMAN pin becomes a via filter"

if has 'if dp.SetByUserID <= 0 {' "$ACL_SRC"; then
  ok "A1: the per-device pin skips a DERIVED preference (set_by_user_id = 0)"
else
  bad "A1: a derived preference still becomes a via filter — choosing another exit node in the client would blackhole the device"
fi
if has 'left UNPINNED (B398)' "$ACL_SRC"; then
  ok "A2: the skip is logged with its reason and its count (visible in the journal, not silent)"
else
  bad "A2: the skip is silent"
fi
if has 'if dp.SetByUserID <= 0 {' "$ACL_SRC" && has 'viaByDevice[devTag] = dp.ExitNodeTag' "$ACL_SRC"; then
  ok "A3: an EXPLICIT pin still pins (the operator's decision is honoured)"
else
  bad "A3: the explicit path is gone"
fi

# ---------------------------------------------------------------------------
# B. the device's preference votes
# ---------------------------------------------------------------------------
hdr "B — the device's own choice votes for its relay-less rules"

if has 'JOIN device_exit_node_prefs p' "$OWNER_SRC" && has "(r.exit_node_id IS NULL OR r.exit_node_id = '')" "$OWNER_SRC"; then
  ok "B1: LoadClaims turns a device preference into a claim for its relay-less rules"
else
  bad "B1: a device's own choice still votes for nobody — the engine overrules it"
fi
if has 'JOIN node_owner_map n' "$OWNER_SRC" && has 'lower(n.hostname)' "$OWNER_SRC"; then
  ok "B2: the TAG→HOSTNAME conversion is explicit (the pref stores a tag, the assignment table a hostname)"
else
  bad "B2: the claim mixes units (tag vs hostname) — Assign would never match it as healthy"
fi
if has 'device-preference claims unavailable' "$OWNER_SRC"; then
  ok "B3: a missing preference table degrades to rules-only instead of breaking the pass"
else
  bad "B3: an old schema aborts the assignment pass"
fi

# ---------------------------------------------------------------------------
# C. the operator can see the pin
# ---------------------------------------------------------------------------
hdr "C — the pin is visible on the diagnostic page"

if has 'Name:        "exit_rules.exit_node_pin"' "$DEF_SRC"; then
  ok "C1: a system test names the pin per device"
else
  bad "C1: no diagnostic reports the pin — the operator has to find it by symptom again"
fi
if has '`via` is a FILTER' "$DEF_SRC"; then
  ok "C2: the test says the CONSEQUENCE (another exit node in the client blocks all traffic)"
else
  bad "C2: the message does not say what breaks"
fi
if has '"exit_rules.exit_node_pin"' "$ANN_SRC" && has 'st.fix.exit_node_pin' "$ANN_SRC"; then
  ok "C3: the test is annotated (severity + remediation), so it cannot render unannotated"
else
  bad "C3: the annotation is missing (the B394 coverage test will fail)"
fi
ru="$(grep -c '"st.fix.exit_node_pin"' "$CAT" 2>/dev/null || true)"
if [ "${ru:-0}" -ge 2 ]; then
  ok "C4: the remediation text exists in BOTH catalogues ($ru entries)"
else
  bad "C4: the remediation key is not in RU + EN ($ru entries)"
fi

# ---------------------------------------------------------------------------
# D. behavioural pins, registration, documentation
# ---------------------------------------------------------------------------
hdr "D — pinned by tests, registered, documented"

for n in TestB398_DerivedPreferenceIsNotAFilter TestB398_ExplicitPinStillPins; do
  if has "func $n(" "$ACL_TEST_SRC"; then ok "D1: $n pins the ACL half"; else bad "D1: $n is missing"; fi
done
for n in TestB398_DevicePreferenceVotesForItsRelaylessRules TestB398_NoPreferenceStaysEngineDecided; do
  if has "func $n(" "$PO_TEST_SRC"; then ok "D2: $n pins the assignment half"; else bad "D2: $n is missing"; fi
done
if git ls-files --error-unmatch scripts/check_b398_exit_node_pin.sh >/dev/null 2>&1; then
  ok "D3: this script is tracked by git (trap #11)"
else
  bad "D3: this script is not tracked"
fi
if grep -q 'check_b398_exit_node_pin' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D4: registered in scripts/verify_pre_deploy.sh"
else
  bad "D4: not registered in the catalog"
fi
if grep -q '^\- \*\*B398\*\*' AGENTS.md 2>/dev/null; then
  ok "D5: one-line entry in the AGENTS.md block index"
else
  bad "D5: no B398 entry in the AGENTS block index"
fi
if grep -q 'B398' docs/ROADMAP.md 2>/dev/null && grep -q 'B398' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "D6: both roadmaps record the block (rule 12)"
else
  bad "D6: a roadmap does not record the block"
fi

printf '\n\033[1mB398: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
