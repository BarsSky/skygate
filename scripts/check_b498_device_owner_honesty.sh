#!/usr/bin/env bash
# B498 (2026-11-19) — «ОНИ ОТОБРАЖАЮТСЯ ПО-ДРУГОМУ» И «ПОЧЕМУ ВЫСТАВЛЯЕТСЯ НЕВЕРНАЯ НАСТРОЙКА».
#
# Two questions from the operator, both answered by measurement on the live host, and both were
# real defects of the PANEL — the ACL was identical for all five phones:
#
#   every phone got  { "src": ["tag:dev-<user>-<device>"], "dst": ["autogroup:internet"], "ip": ["*"] }
#   (unpinned), plus the per-user grant with `via` on `src: <user>@domain`, which headscale never
#   matches against a TAGGED node (B265) — so the stored per-user preference governs nothing.
#
# 1. olesya and a71 rendered a relay badge; nothing-phone-2, skybars-secure and s24-fe--ned rendered
#    a DASH. The page derived a device's owner by cutting `tag:dev-<user>-<host>` at the LAST hyphen
#    and looking the left half up as a portal username:
#
#        tag:dev-michail-olesya           -> michail                OK
#        tag:dev-skyadmin-a71             -> skyadmin               OK
#        tag:dev-michail-nothing-phone-2  -> michail-nothing-phone   no such user -> uid 0
#        tag:dev-skyadmin-skybars-secure  -> skyadmin-skybars       no such user -> uid 0
#        tag:dev-skyadmin-s24-fe--ned     -> skyadmin-s24-fe-       no such user -> uid 0
#
#    uid 0 rendered a dash AND hid the assign control, so the devices that looked broken could not
#    be pinned at all. The owner now comes from `node_owner_map.username` (correct on every row),
#    with `tagged_by_user_id` as the fallback for headscale's synthetic `tagged-devices` owner.
#
# 2. the badge those two DID show was the per-user row — printed inside each device cell as if the
#    device were pinned to it. That is what «продолжает выставляться неверная настройка» described.
#    A per-user preference is not any device's setting, so the device answer is now «free choice»
#    and the user-level fact is a page-level notice naming the person, the relay and the number of
#    devices it fails to reach.
#
# Contracts:
#   A. the resolver exists, reads the OWNERSHIP ROW, and is tested against the five live shapes
#   B. tag-string splitting is gone from the handler (the defect, not just its symptom)
#   C. a per-user row is not a per-device answer; the notice replaces it
#   D. the notice is rendered once, with its three facts, and i18n keys exist in both catalogues
#   E. display-only: no decision package imports anything new (the ACL cannot move)
#   F. discoverable: registered, tracked, indexed, roadmap, release notes

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

OWNER="internal/feature/admin/devices_owner_b498.go"
HANDLER="internal/feature/admin/devices.go"
TPL="internal/handlers/templates/admin/devices.html"
CAT="internal/i18n/catalog_my.go"
PIN="internal/exitpin/exitpin.go"

# ═══════════════ A. the resolver ═══════════════
if [ -f "$OWNER" ]; then ok "A1: $OWNER exists"; else bad "A1: $OWNER is missing"; fi
if grep -q '^func b498OwnerUID(' "$OWNER" 2>/dev/null; then
    ok "A2: the owner resolver is one pure function"
else
    bad "A2: b498OwnerUID is missing"
fi
# The authority must be the ownership row, not the tag.
if grep -q 'row.Username' "$OWNER" 2>/dev/null && grep -q 'row.TaggedByUserID' "$OWNER" 2>/dev/null; then
    ok "A3: it reads username + tagged_by_user_id from the row"
else
    bad "A3: the resolver does not read the ownership columns"
fi
if grep -q 'TestB498_OwnerSurvivesAHyphenatedHostname' internal/feature/admin/devices_owner_b498_test.go 2>/dev/null; then
    ok "A4: a test pins the five live tag shapes (three of which used to be a dash)"
else
    bad "A4: nothing pins the hyphenated-hostname case"
fi

# ═══════════════ B. the defect is gone ═══════════════
# The old derivation was `LastIndex(rest, "-")` over the tag. If it comes back, the
# dash comes back with it.
if grep -q 'LastIndex(rest' "$HANDLER" 2>/dev/null; then
    bad "B1: the handler still splits the dev-tag at the last hyphen (uid 0 for hyphenated hostnames)"
else
    ok "B1: no tag-string owner derivation left in the handler"
fi
if grep -q 'b498OwnerUID(' "$HANDLER" 2>/dev/null; then
    ok "B2: the handler resolves owners through b498OwnerUID"
else
    bad "B2: the handler does not use the resolver"
fi
if grep -q 'node_owner_map.username' "$OWNER" 2>/dev/null || grep -q 'portal_users' "$HANDLER" 2>/dev/null; then
    ok "B3: the attribution comes from the ownership table"
else
    bad "B3: the ownership table is not consulted"
fi

# ═══════════════ C. a per-user row is not a device answer ═══════════════
# The tagged branch must fall through to the free-choice answer, and LostUserPref
# must exist so the fact is not simply deleted.
if grep -q 'func LostUserPref(' "$PIN" 2>/dev/null; then
    ok "C1: LostUserPref exists (the fact is MOVED, not dropped)"
else
    bad "C1: LostUserPref is missing — the user-level fact would be lost"
fi
if awk '/if p := in.User;/,/^\t}/' "$PIN" 2>/dev/null | grep -q '!in.Tagged'; then
    ok "C2: Effective() applies a per-user row only to an UNTAGGED node"
else
    bad "C2: Effective() can still turn a per-user row into a device answer"
fi
if grep -q 'LostUserPrefs' "$HANDLER" 2>/dev/null; then
    ok "C3: the handler passes the notice to the page"
else
    bad "C3: the page is not given the lost-preference notice"
fi

# ═══════════════ D. the notice ═══════════════
case "$(cat "$TPL" 2>/dev/null)" in
    *'.LostUserPrefs'*) ok "D1: the template renders the notice" ;;
    *) bad "D1: the template ignores LostUserPrefs" ;;
esac
case "$(cat "$TPL" 2>/dev/null)" in
    *'lost_user_pref_row'*) ok "D2: it uses the formatted, translated sentence" ;;
    *) bad "D2: the notice has no sentence" ;;
esac
missing=""
for k in lost_user_pref_title lost_user_pref_help lost_user_pref_row; do
    n="$(grep -c "^	\"devices.$k\":" "$CAT" 2>/dev/null)"
    [ "${n:-0}" -ge 2 ] || missing="$missing devices.$k(${n:-0}/2)"
done
if [ -z "$missing" ]; then
    ok "D3: all three i18n keys exist in BOTH catalogues"
else
    bad "D3: keys missing:$missing"
fi
# The notice must not be per-device: it lives once, above the table.
if [ "$(grep -c 'lost_user_pref_title' "$TPL" 2>/dev/null)" = "1" ]; then
    ok "D4: the notice appears exactly once on the page"
else
    bad "D4: the notice is rendered more than once (or not at all)"
fi

# ═══════════════ E. display-only ═══════════════
leak="$(grep -rln 'devices_owner_b498\|LostUserPref' --include='*.go' internal/acl internal/prefixowner internal/feature/exit_rules 2>/dev/null | grep -v '_test.go')"
if [ -z "$leak" ]; then
    ok "E1: no decision package knows about the page notice"
else
    bad "E1: a decision package imports the page logic:$leak"
fi
if grep -q 'internal/acl' "$OWNER" 2>/dev/null; then bad "E2: the owner resolver imports the ACL"; else ok "E2: the owner resolver does not import the ACL"; fi

# ═══════════════ F. discoverability ═══════════════
if grep -q 'check_b498_device_owner_honesty.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "F1: registered in the catalog"
else
    bad "F1: not registered in scripts/verify_pre_deploy.sh"
fi
if git ls-files --error-unmatch scripts/check_b498_device_owner_honesty.sh >/dev/null 2>&1; then
    ok "F2: git tracks this script (trap #11)"
elif git check-ignore -q scripts/check_b498_device_owner_honesty.sh 2>/dev/null; then
    bad "F2: this script is IGNORED by .gitignore (trap #11)"
else
    ok "F2: this script is not ignored (untracked until the release commit)"
fi
if grep -q '^\- \*\*B498\*\*' AGENTS.md 2>/dev/null; then ok "F3: the AGENTS index carries its line"; else bad "F3: no AGENTS index line"; fi
if grep -q 'B498' docs/ROADMAP.md 2>/dev/null; then ok "F4: the roadmap records it"; else bad "F4: the roadmap does not mention B498"; fi
if grep -q 'B498' RELEASE-NOTES.md 2>/dev/null; then ok "F5: the release notes describe it"; else bad "F5: no release-notes entry"; fi

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
    skip "G1: no go binary in PATH — the behavioural tests were not run"
else
    if out="$("$GOBIN" test ./internal/exitpin/ ./internal/feature/admin/ ./internal/handlers/ -run 'B497|B498' -count=1 2>&1)"; then
        ok "G1: the B497/B498 tests are green (rules, resolver, notice, render)"
    else
        bad "G1: go test failed:
$(printf '%s\n' "$out" | tail -20)"
    fi
fi

echo
echo "  B498: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
