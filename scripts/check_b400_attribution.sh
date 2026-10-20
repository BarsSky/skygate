#!/usr/bin/env bash
# check_b400_attribution.sh
#
# 2026-10-20 (B400) — THE OWNERSHIP QUESTION MUST BE ASKED WHERE THE ANSWER LIVES.
#
# WHY (operator, verbatim)
#   «сейчас все устройства принадлежат ему [tagged-devices] и различие идёт только
#    строго по тегам … в начальных настройках указывается что 14 устройств не
#    определены за пользователем … вопрос как должно быть правильно — должен ли быть
#    явный пользователь у устройств или они остаются за tagged device?»
#
# MEASURED on his installation the same day:
#   node_owner_map : 14 rows, 0 unattributed (skyadmin 6 · infra 5 · michail 3)
#   headscale      : all 14 nodes owned by the built-in user `tagged-devices`
#   dashboard      : «14 node(s) have no panel owner yet»
#
#   The wizard compared headscale's `node.User` with the panel's username list. A
#   tagged node is ALWAYS `tagged-devices` there (that is what a tag does), so the
#   comparison could never succeed and the counter could never reach zero — it measured
#   headscale's bookkeeping, not the presence of an owner. Meanwhile node_owner_map —
#   the table the ACL, /my/* pages, «all my devices» and the preference reconciler all
#   read — knew every device.
#
# CONTRACTS
#   A. the wizard asks node_owner_map first (and only then headscale's user)
#   B. a tagged device with a panel row is NOT awaiting review; a tagged device
#      without one IS; an attribution naming a user the panel lacks IS
#   C. infrastructure is recognised by the tag the node carries
#   D. the answer is VISIBLE: the owner source is rendered, and the step text says
#      what the number means
#   E. the runtime reports the same question (system test) and the whole thing is
#      pinned by tests, registration and documentation
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/setupstate/setupstate.go ] || {
  printf 'B400: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b400.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

SS=internal/setupstate/setupstate.go
WIZ=internal/feature/my/dashboard_wizard_b395.go
T_DASH=internal/handlers/templates/dashboard.html
ST=internal/feature/admin/system_tests_b394_new.go
ANN=internal/feature/admin/system_tests_diagnostics_b394.go
CAT=internal/i18n/catalog_my.go
CATC=internal/i18n/catalog_common.go
T_SS=internal/setupstate/setupstate_b400_test.go
T_RENDER=internal/handlers/dashboard_wizard_render_b395_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

SS_SRC="$(src "$SS")"; WIZ_SRC="$(src "$WIZ")"; DASH_SRC="$(src "$T_DASH")"
ST_SRC="$(src "$ST")"; ANN_SRC="$(src "$ANN")"; CAT_SRC="$(src "$CAT")"; CATC_SRC="$(src "$CATC")"
TSS_SRC="$(src "$T_SS")"; TREND_SRC="$(src "$T_RENDER")"

hdr "B400 — the ownership question must be asked where the answer lives"

# ---------------------------------------------------------------------------
# A. node_owner_map comes first
# ---------------------------------------------------------------------------
hdr "A — the wizard asks the table the runtime asks"

if has 'OwnerByNodeID map[string]NodeOwner' "$SS_SRC"; then
  ok "A1: Config carries node_owner_map keyed by headscale node id"
else
  bad "A1: the wizard has no way to read node_owner_map — it can only see headscale's user"
fi
if has 'const taggedDevicesUser = "tagged-devices"' "$SS_SRC"; then
  ok "A2: headscale's built-in tagged-devices owner is NAMED (so the comparison can be explicitly rejected)"
else
  bad "A2: tagged-devices is not named — the old comparison can creep back"
fi
if has 'case hasOwner && ownerLower != "":' "$SS_SRC"; then
  ok "A3: the panel row is consulted BEFORE the headscale user"
else
  bad "A3: the headscale user is still consulted first"
fi
if has 'OwnerSourcePanel' "$SS_SRC" && has 'OwnerSourceHeadscale' "$SS_SRC"; then
  ok "A4: both sources are distinguishable in the data (the page can say which)"
else
  bad "A4: the row does not record where the owner came from"
fi
if has 'nodeOwnersByNodeID' "$WIZ_SRC" && has 'db.ListAllNodeOwners' "$WIZ_SRC"; then
  ok "A5: the wizard is fed from the same reader the ACL and /my/* use"
else
  bad "A5: the wizard is not wired to node_owner_map"
fi

# ---------------------------------------------------------------------------
# B. the four shapes
# ---------------------------------------------------------------------------
hdr "B — tagged + attributed is OWNED; tagged + absent is REVIEW"

for n in TestB400_PanelOwnerIsTheAnswer TestB400_TrulyUnattributedIsReview TestB400_OwnerNamingAnUnknownPanelUserIsReview TestB400_UntaggedNodeFallsBackToHeadscaleUser TestB400_NoPanelAttributionKeepsTheOldRule; do
  if has "func $n(" "$TSS_SRC"; then ok "B: $n pins one shape"; else bad "B: $n is missing"; fi
done
if has 'review, because that is an attribution the runtime cannot honour' "$SS_SRC"; then
  ok "B5: a row naming a user the panel lacks is rejected with its reason (not silently accepted)"
else
  bad "B5: an attribution the runtime cannot honour would be reported as owned"
fi

# ---------------------------------------------------------------------------
# C. infrastructure
# ---------------------------------------------------------------------------
hdr "C — a relay is recognised by the tag it carries"

if has 'for _, tg := range n.Tags' "$SS_SRC" && has 'tag:dev-infra-' "$SS_SRC"; then
  ok "C1: infrastructure is detected from the NODE's own tags (not only from a panel row)"
else
  bad "C1: a relay tagged before node_owner_map caught up would be offered for attribution"
fi
if has 'func TestB400_InfraIsNeverPending(' "$TSS_SRC"; then
  ok "C2: pinned by a test that deletes the panel row and keeps the tag"
else
  bad "C2: nothing pins the infra classification"
fi

# ---------------------------------------------------------------------------
# D. the answer is visible
# ---------------------------------------------------------------------------
hdr "D — the operator can SEE which source answered"

if has '.OwnerSource' "$DASH_SRC" && has 'wizard.owner_panel' "$DASH_SRC"; then
  ok "D1: the node table renders the owner source"
else
  bad "D1: the page cannot tell a panel owner from a headscale user"
fi
n_ru="$(grep -c '"wizard.owner_panel"' "$CAT" 2>/dev/null || true)"
n_hs="$(grep -c '"wizard.owner_headscale"' "$CAT" 2>/dev/null || true)"
if [ "${n_ru:-0}" -ge 2 ] && [ "${n_hs:-0}" -ge 2 ]; then
  ok "D2: both source labels exist in RU + EN ($n_ru / $n_hs entries)"
else
  bad "D2: the source labels are not in both catalogues ($n_ru / $n_hs)"
fi
if grep -q '"wizard.why_pending_review".*headscale user' "$CAT" 2>/dev/null; then
  ok "D3: the step text says the device is attached to NEITHER a panel user NOR a headscale user"
else
  bad "D3: the step text still claims the old, wrong reason"
fi
if has 'func TestDashboardWizard_OwnerSourceIsRendered(' "$TREND_SRC"; then
  ok "D4: rendering is pinned (a field nobody renders is not a fix — B395.1)"
else
  bad "D4: nothing asserts that the source reaches the page"
fi

# ---------------------------------------------------------------------------
# E. the runtime asks the same question
# ---------------------------------------------------------------------------
hdr "E — the diagnostic measures node_owner_map too"

if has 'Name:        "ownership.attribution"' "$ST_SRC"; then
  ok "E1: a system test reports attribution from node_owner_map"
else
  bad "E1: the runtime has no test for the question the wizard now asks"
fi
if has 'db.ListAllNodeOwners' "$ST_SRC"; then
  ok "E2: and it reads the SAME table (no second definition of \"owner\")"
else
  bad "E2: the test measures something else than the runtime uses"
fi
if has '"ownership.attribution"' "$ANN_SRC" && has 'st.fix.ownership_attribution' "$ANN_SRC"; then
  ok "E3: annotated (severity + remediation), so it cannot render bare"
else
  bad "E3: the annotation is missing (the B394 coverage test will fail)"
fi
if grep -q '"st.fix.ownership_attribution"' "$CATC" 2>/dev/null; then
  m="$(grep -c '"st.fix.ownership_attribution"' "$CATC" 2>/dev/null || true)"
  if [ "${m:-0}" -ge 2 ]; then
    ok "E4: the remediation text exists in RU + EN ($m entries)"
  else
    bad "E4: the remediation text is not in both catalogues ($m entries)"
  fi
else
  bad "E4: no remediation key at all"
fi
if git ls-files --error-unmatch scripts/check_b400_attribution.sh >/dev/null 2>&1; then
  ok "E5: this script is tracked by git (trap #11)"
else
  bad "E5: this script is not tracked"
fi
if grep -q 'check_b400_attribution' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E6: registered in scripts/verify_pre_deploy.sh"
else
  bad "E6: not registered in the catalog"
fi
if grep -q '^\- \*\*B400\*\*' AGENTS.md 2>/dev/null; then
  ok "E7: one-line entry in the AGENTS.md block index"
else
  bad "E7: no B400 entry in the AGENTS block index"
fi
if grep -q 'B400' docs/ROADMAP.md 2>/dev/null && grep -q 'B400' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E8: both roadmaps record the block (rule 12)"
else
  bad "E8: a roadmap does not record the block"
fi

printf '\n\033[1mB400: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
