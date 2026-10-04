#!/usr/bin/env bash
# check_b346_release_pin.sh — B346: ONE pinned release that every instance
# orients on.
#
# WHY THIS FILE EXISTS (measured on the live host, 2026-10-04)
# -----------------------------------------------------------
# Operator requirement, verbatim: «Чтобы версии совпадали необходимо
# зафиксировать релиз на который ориентируются все экземпляры».
#
# The live state made the requirement concrete. `git ls-remote --tags origin`
# ended at **v1.5.94**, which points at commit 4b2186b (B328); the authoring
# tree was **21 commits ahead of origin/main** with none of them published;
# and the running instance answered /healthz with
#
#     "build":"v1.5.94+4b2186b"
#
# while its WORKING TREE carried those 21 commits. Every surface said "up to
# date", `v1.5.94` described code that was not running, and no two instances
# could be proven to run the same code. "Follow the latest release" cannot
# express "every instance on THIS release", and the three places that choose a
# target chose it independently:
#
#   /admin/update "Update now"  → GitHub's latest
#   B130 scheduled updater      → GitHub's latest
#   B342 cluster onboarding     → releaseTagFromBuild(primary's own label)
#
# B346 adds `global_settings["update.pinned_release"]` and makes all three read
# it. A release tag only: `NormalizePinnedRelease` rejects a branch, a raw
# commit, a `git describe` label and the updater's own `skygate-pre-update-*`
# tag, because a pin that cannot be checked out on every install kind is a lie
# about version equality.
#
# Deliberate semantics (documented, not accidental):
#   - a pin OLDER than the running build is still applied — pinning a
#     known-good release is how a host that drifted forward is pulled back;
#   - "already on the pin" starts no job (no reschedule loop);
#   - an unparsable stored value is IGNORED by the workers (they fall back to
#     "latest") and reported by the page, so a typo cannot wedge updates.
#
# What this script verifies:
#   A. one implementation of the pin (key, canonicaliser, two consumers)
#   B. EVERY target-choosing path honours it, in the right ORDER
#   C. the page shows the pin, the drift and the save result (RU+EN)
#   D. the decision is unit-tested, including the downgrade case
#   E. tracked, registered, indexed, and the gofmt ratchet paid
#
# Usage:  bash scripts/check_b346_release_pin.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

HELPER=internal/update/pinned_release.go
HELPER_TEST=internal/update/pinned_release_test.go
SCHED=internal/update/scheduler.go
ADMIN_PIN=internal/feature/admin/update_pin.go
ADMIN_UPDATE=internal/feature/admin/update.go
ONBOARD=internal/feature/admin/cluster_onboard_b342.go
ROUTES=cmd/skygate/routes.go
TPL=internal/handlers/templates/admin/update.html
CATALOG=internal/i18n/catalog_update.go
ALLOW=scripts/gofmt_legacy_allowlist.txt
RATCHET=scripts/check_b337_gofmt_ratchet.sh
CAT=scripts/verify_pre_deploy.sh

for f in "$HELPER" "$HELPER_TEST" "$SCHED" "$ADMIN_PIN" "$ADMIN_UPDATE" "$ONBOARD" \
         "$ROUTES" "$TPL" "$CATALOG" "$ALLOW" "$RATCHET" "$CAT"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B346 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# grep -c returns 0 matches as "0" but exits 1; never let pipefail see it.
count() { grep -c -- "$1" "$2" 2>/dev/null || true; }

hdr "A. one implementation of the pin"

for fn in NormalizePinnedRelease ReleaseOfBuildLabel PinnedReleaseFromDB PinnedTargetFor; do
  if grep -q "^func $fn(" "$HELPER"; then
    ok "A1: $fn is defined once, in $HELPER"
  else
    bad "A1: $fn is not defined in $HELPER — a second copy is how two paths start disagreeing"
  fi
done

if grep -q 'PinnedReleaseKey = "update.pinned_release"' "$HELPER"; then
  ok "A2: the settings key is declared as a constant"
else
  bad "A2: PinnedReleaseKey is not declared in $HELPER"
fi

# The admin half duplicates the literal ON PURPOSE (the check keeps them equal).
if grep -q '"update.pinned_release"' "$ADMIN_PIN"; then
  ok "A3: the admin handler writes the SAME key string (asserted, not assumed — a typo here disables the pin silently)"
else
  bad "A3: the admin handler does not name the 'update.pinned_release' key"
fi

if [ "$(count 'update.pinned_release' "$HELPER")" -ge 1 ] && [ "$(count 'update.pinned_release' "$ADMIN_PIN")" -ge 1 ]; then
  ok "A4: both spellings are present — the deliberate duplication is visible in one grep"
else
  bad "A4: one of the two spellings is missing"
fi

hdr "B. every target-choosing path honours the pin"

# B1: the page's target. `.Target` must be the pin, and the old unconditional
# `target = result.Latest` must be gone from the render path.
if grep -q 'pinned := s.PinnedRelease()' "$ADMIN_UPDATE"; then
  ok "B1: /admin/update resolves the pin before choosing its target"
else
  bad "B1: the render path does not read the pin — the page would offer GitHub's latest while the pin says otherwise"
fi
if grep -q 'case pinned != "":' "$ADMIN_UPDATE"; then
  ok "B2: the pin has PRIORITY over result.Latest in the target switch"
else
  bad "B2: the target switch does not give the pin priority"
fi

# B3: Apply must resolve the pin BEFORE the GitHub round trip, so an offline
# host with a pin can still apply it. The order is compared INSIDE
# PostAdminUpdateApply — a file-wide grep would compare against the Push
# handler's own Copy, which is exactly the false PASS this contract exists to
# prevent (measured: a whole-file grep reported "pin=710 github=512" and failed
# for the wrong reason).
APPLY_BODY="$(awk '/func \(s \*Service\) PostAdminUpdateApply\(/,/^}/' "$ADMIN_UPDATE")"
APPLY_LINE="$(printf '%s\n' "$APPLY_BODY" | grep -n 'target = s.PinnedRelease()' | head -1 | cut -d: -f1 || true)"
GITHUB_LINE="$(printf '%s\n' "$APPLY_BODY" | grep -n 'checker := &update.Checker' | head -1 | cut -d: -f1 || true)"
if [ -n "$APPLY_LINE" ] && [ -n "$GITHUB_LINE" ] && [ "$APPLY_LINE" -lt "$GITHUB_LINE" ]; then
  ok "B3: inside Apply, the pin is resolved (line $APPLY_LINE) BEFORE the GitHub check (line $GITHUB_LINE) — an offline host with a pin can still apply it"
else
  bad "B3: Apply does not resolve the pin before the network call (pin=$APPLY_LINE github=$GITHUB_LINE within PostAdminUpdateApply)"
fi

# B4: Push ("force a rebuild") must not restore a divergent build.
PIN_IN_PUSH="$(awk '/func \(s \*Service\) PostAdminUpdatePush/,/^}/' "$ADMIN_UPDATE" | grep -c 's.PinnedRelease()' || true)"
if [ "$PIN_IN_PUSH" -ge 1 ]; then
  ok "B4: 'Push update' defaults to the pin, not to the running build"
else
  bad "B4: 'Push update' would rebuild the divergent build it is supposed to replace"
fi

# B5: the scheduled updater. The pin branch must come BEFORE the GitHub check,
# otherwise an offline host (the exact case a pin is for) never runs.
SCHED_PIN_LINE="$(grep -n 'PinnedReleaseFromDB(deps.DB)' "$SCHED" | cut -d: -f1 | head -1 || true)"
SCHED_GH_LINE="$(grep -n 'deps.Checker.Check(ctxCheck)' "$SCHED" | cut -d: -f1 | head -1 || true)"
if [ -n "$SCHED_PIN_LINE" ] && [ -n "$SCHED_GH_LINE" ] && [ "$SCHED_PIN_LINE" -lt "$SCHED_GH_LINE" ]; then
  ok "B5: the scheduler's pin branch (line $SCHED_PIN_LINE) precedes the GitHub check (line $SCHED_GH_LINE)"
else
  bad "B5: the scheduler checks GitHub before (or instead of) the pin (pin=$SCHED_PIN_LINE github=$SCHED_GH_LINE)"
fi
if grep -q 'PinnedTargetFor(pin, deps.BuildVersion)' "$SCHED"; then
  ok "B6: the scheduler's target decision goes through the pure helper (unit-tested, not inline)"
else
  bad "B6: the scheduler decides inline — the decision is then unpinnable by a test"
fi

# B7: a host that JOINS the cluster must be installed with the same release.
if awk '/PostAdminClusterOnboard\(/,/^}/' "$ONBOARD" | grep -q 's.PinnedRelease()'; then
  ok "B7: the B342 onboarding install block uses the pinned release"
else
  bad "B7: cluster onboarding ignores the pin — the joining host would install a different release"
fi

# B8: the route exists, or the form posts into the void.
if grep -q 'POST /admin/update/pin' "$ROUTES"; then
  ok "B8: POST /admin/update/pin is registered"
else
  bad "B8: the route is not registered — the form would 404"
fi

hdr "C. the page shows the pin, the drift and the result"

if grep -q 'action="/admin/update/pin"' "$TPL"; then
  ok "C1: the pin form exists on /admin/update"
else
  bad "C1: no pin form on /admin/update"
fi

# The old form posted .Latest, which would install the latest release while
# the page displayed the pin — the exact contradiction B346 removes.
if grep -q 'name="target" value="{{.Target}}"' "$TPL"; then
  ok "C2: the Apply form posts .Target (the pin), not .Latest"
else
  bad "C2: the Apply form does not post .Target — 'Update now' would ignore the pin"
fi
if grep -q 'name="target" value="{{.Latest}}"' "$TPL"; then
  bad "C3: the pre-B346 value=\"{{.Latest}}\" is still in the template"
else
  ok "C3: the pre-B346 .Latest form value is gone"
fi

# The button must appear on drift alone: GitHub may have nothing newer while
# this host is off the pinned tag.
if grep -q 'and (or .IsNewer .PinDrift) (not .DevBuild)' "$TPL"; then
  ok "C4: the Update button appears on newer-release OR drift"
else
  bad "C4: the button is still gated on .IsNewer only — a drifted instance would show no way back"
fi

if grep -q '{{if .PinDrift}}' "$TPL" && grep -q 'update.pin_drift' "$TPL"; then
  ok "C5: the drift banner is rendered from .PinDrift"
else
  bad "C5: the drift banner is missing — the page would not say that the versions diverge"
fi

# Every key the template uses must exist in BOTH catalogues (B325's class: the
# parity test compares key SETS, so a one-sided key passes it and renders raw).
MISSING=""
for key in $(grep -o 't "update\.pin_[a-z_]*"' "$TPL" | sed 's/t "//;s/"//' | sort -u); do
  N="$(count "\"$key\"" "$CATALOG")"
  if [ "$N" -lt 2 ]; then MISSING="$MISSING $key($N)"; fi
done
if [ -z "$MISSING" ]; then
  ok "C6: every update.pin_* key used by the template exists in RU and EN"
else
  bad "C6: keys not defined in both catalogues:$MISSING"
fi

hdr "D. the decision is unit-tested"

for tn in TestNormalizePinnedRelease_B346 TestPinnedTargetFor_B346 TestPinnedReleaseFromDB_B346; do
  if grep -q "func $tn(" "$HELPER_TEST"; then
    ok "D1: $tn exists"
  else
    bad "D1: $tn is missing"
  fi
done

if grep -q 'pin_OLDER_than_build_still_applies' "$HELPER_TEST"; then
  ok "D2: the deliberate DOWNGRADE case is pinned (a pin older than the build is applied on purpose)"
else
  bad "D2: the downgrade semantics are untested — a future 'fix' could silently stop pulling drifted hosts back"
fi

GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "D3: go is not on PATH — run the B346 tests on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/update/ -run 'B346' -count=1 2>&1)"; then
    ok "D3: the B346 cases pass ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "D3: the B346 cases fail: $(printf '%s' "$OUT" | tail -3)"
  fi
fi

hdr "E. tracked, registered, indexed, ratchet paid"

if git ls-files --error-unmatch scripts/check_b346_release_pin.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (AGENTS trap #11)"
else
  bad "E1: NOT tracked by git — a .gitignore rule can eat a new script"
fi
if grep -q 'check_b346_release_pin.sh' "$CAT"; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered — it would never run"
fi
if grep -q '\*\*B346\*\*' AGENTS.md; then
  ok "E3: recorded in the AGENTS.md block index"
else
  bad "E3: no B346 line in the AGENTS.md block index (AGENTS rule 2)"
fi

# B346 touched internal/i18n/catalog_update.go, so it had to leave the gofmt
# allow-list and the budget had to fall — the two halves go together (B337 D1).
if grep -qx 'internal/i18n/catalog_update.go' "$ALLOW"; then
  bad "E4: catalog_update.go is still allow-listed although this change formatted it (B337 D1)"
else
  ok "E4: catalog_update.go left the gofmt allow-list"
fi
if grep -q '^FROZEN=267$' "$RATCHET"; then
  ok "E5: the B337 budget fell to 267 with it"
else
  bad "E5: the B337 FROZEN budget was not lowered (expected 267)"
fi

hdr "B346 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
