#!/usr/bin/env bash
# B497 (2026-11-19) — THE EXIT-NODE CELL MUST SHOW WHAT THE ACL DOES, NOT WHAT A TABLE STORES.
#
# The operator compared two phones that work (`skybars`, `nothing-phone-2`) with two that do not
# (`olesya`, the A71 of `Константин`) and asked what differs. Nothing differed on screen: all five
# devices had no rules, no per-device preference and the same unpinned `dst: autogroup:internet`
# grant. What DID differ, and what no page showed, is that the only pin in the tailnet lives on
# `src: [user@domain]` grants — and headscale never matches a TAGGED node through a user selector,
# so a per-user preference reaches none of these devices (B265 measured it). The screenshot the
# operator sent showed `emilia` in a cell whose device had no preference at all: an empty cell
# rendered a <select>, and a <select> displays its first <option>.
#
# B497 therefore makes the EFFECTIVE decision a value the page prints, derived from the very
# predicates the generator applies (package internal/exitpin):
#
#   via_enabled = 1            (v0.28.5)  — a row with via off is stored, not pinned
#   set_by_user_id > 0         (B398)     — a DERIVED row must not become a filter
#   node_owner_map.tag <> ''   (B284)     — without a tag no per-device grant exists
#   relay usable               (B356/B361)— same predicate as the ACL (monitoring.ExitNodeUsable)
#   tagged ⇒ user pref inert   (B265)     — the live olesya/A71 shape
#
# Contracts:
#   A. the package exists, is pure enough to be tested as a table, and its tests are green
#   B. the five predicates above are the ones implemented (each named, not implied)
#   C. /admin/devices uses it: one owner-resolved pref map, the badge reads the pin
#   D. no relay name can be displayed for a device with no preference (the screenshot's bug)
#   E. i18n: every source and every reason exists in BOTH catalogues (the keys are assembled at
#      render time, so neither the parity test nor the B325 ratchet can see them)
#   F. DISPLAY-ONLY: no decision package imports exitpin, so this block cannot move the ACL
#   G. discoverable: registered, indexed, recorded in roadmap + release notes
#
# Every contract reads the code that ships; none of them needs a database or the live host.

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

PKG="internal/exitpin/exitpin.go"
ADAPTERS="internal/exitpin/exitpin_prefs.go"
WIRE="internal/feature/admin/devices_exitpin_b497.go"
HANDLER="internal/feature/admin/devices.go"
TPL="internal/handlers/templates/admin/devices.html"
CAT="internal/i18n/catalog_my.go"

# ═══════════════ A. the package ═══════════════
for f in "$PKG" "$ADAPTERS" "$WIRE"; do
    if [ -f "$f" ]; then ok "A1: $f exists"; else bad "A1: $f is missing"; fi
done
if grep -q '^func Effective(' "$PKG" 2>/dev/null; then
    ok "A2: Effective() is a plain function (the rules are testable as a table)"
else
    bad "A2: Effective() is missing — the rules cannot be pinned by a test"
fi
# The rules file must not read the database: a page that queries inside its own decision
# logic cannot be tested without a live stack, which is exactly how the divergence survived.
if grep -nE 'database/sql|QueryRow|Query\(' "$PKG" 2>/dev/null | grep -v '^\s*//' | grep -q .; then
    bad "A3: $PKG reads the database — the rules must stay pure"
else
    ok "A3: the rules file holds no SQL"
fi
GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
    # Trap #1: a check whose dependency is absent reports SKIP, never FAIL — this
    # workstation runs the gate through a bash whose PATH has no toolchain.
    skip "A4: no go binary in PATH — the behavioural contracts were not run"
else
    out="$("$GOBIN" test ./internal/exitpin/ ./internal/feature/admin/ ./internal/handlers/ -run 'B497' -count=1 2>&1)"; rc=$?
    case "$out" in
        *FAIL*) bad "A4: a B497 test fails: $(printf '%s' "$out" | grep -m2 -E '^(---|    )' | tr '\n' ' ')" ;;
        *)  if [ "$rc" -eq 0 ]; then ok "A4: the B497 tests are green (rules, adapters, page)"; else bad "A4: go test exited $rc"; fi ;;
    esac
fi

# ═══════════════ B. the five predicates ═══════════════
code_only() { grep -v '^\s*//' "$1" 2>/dev/null | grep -v '^\s*$'; }
miss=""
case "$(code_only "$PKG")" in
    *ViaEnabled*) : ;; *) miss="$miss via_enabled" ;;
esac
case "$(code_only "$PKG")" in
    *SetByUserID*) : ;; *) miss="$miss set_by_user_id" ;;
esac
case "$(code_only "$PKG")" in
    *DeviceTag*) : ;; *) miss="$miss device_tag" ;;
esac
case "$(code_only "$PKG")" in
    *ExitNodeUsable*) : ;; *) miss="$miss relay_health" ;;
esac
case "$(code_only "$PKG")" in
    *Tagged*) : ;; *) miss="$miss tagged_user_src" ;;
esac
if [ -z "$miss" ]; then
    ok "B1: every generator predicate is implemented (via/owner/tag/health/tagged)"
else
    bad "B1: the rules ignore:$miss"
fi
# The health predicate must be the monitor's, not a second copy: a `state == "online"` test here
# would read a working untagged relay (B273) as broken.
if grep -q 'monitoring.ExitNodeUsable' "$PKG" 2>/dev/null; then
    ok "B2: relay health delegates to monitoring.ExitNodeUsable (one predicate, B273/B356)"
else
    bad "B2: the package re-derives relay health instead of calling the monitor's predicate"
fi
if grep -q 'ReasonDerived' "$PKG" 2>/dev/null && grep -q 'ReasonTaggedNoUser' "$PKG" 2>/dev/null; then
    ok "B3: the two cases the operator hit have their own reasons (B398 derived / B265 tagged)"
else
    bad "B3: the derived-row and tagged-user cases are not distinguished"
fi

# ═══════════════ C. the page uses it ═══════════════
if grep -q 'b497Pins(' "$WIRE" 2>/dev/null && grep -q 'nodePins := b497Pins(' "$HANDLER" 2>/dev/null; then
    ok "C1: /admin/devices computes the effective pin through b497Pins"
else
    bad "C1: the handler does not call b497Pins"
fi
# The old shape merged every user's per-device rows into one hostname-keyed map — the cross-user
# leak this block removed. Assert the merge is gone, not merely bypassed.
if grep -q 'for _, dp := range deviceExitPrefs {' "$HANDLER" 2>/dev/null \
   && awk '/for _, dp := range deviceExitPrefs \{/,/^\t\}/' "$HANDLER" | grep -q 'prefByHostname'; then
    bad "C2: device_exit_node_prefs is still merged by hostname alone (cross-user leak)"
else
    ok "C2: preferences are looked up per OWNER, never by hostname alone"
fi
if grep -q 'b497PrefKey(' "$WIRE" 2>/dev/null && grep -q 'b497PrefKey(dp.UserID' "$HANDLER" 2>/dev/null; then
    ok "C3: one spelling of the (owner, device) key (case-safe)"
else
    bad "C3: the (owner, device) key is still built inline in more than one place"
fi
if grep -q '"NodePins"' "$HANDLER" 2>/dev/null; then ok "C4: the pin map reaches the template"; else bad "C4: NodePins is not passed to the template"; fi
case "$(cat "$TPL" 2>/dev/null)" in
    *'$pin.Applied'*) ok "C5: the template branches on whether the pin APPLIES" ;;
    *) bad "C5: the template cannot tell an applied pin from a stored one" ;;
esac
case "$(cat "$TPL" 2>/dev/null)" in
    *'$pin.ReasonKey'*) ok "C6: a stored-but-ignored preference renders its reason" ;;
    *) bad "C6: a withheld pin renders no reason — the operator sees a mystery again" ;;
esac
# B498 renegotiated one thing here: a per-user preference is NOT a per-device answer.
# The pin for a tagged node that has only a user row is «free choice», and the
# user-level fact belongs to the page-level notice (LostUserPrefs, check_b498_*).
if grep -q 'SourceNone, Reason: ReasonFreeChoice' "$PKG" 2>/dev/null; then
    ok "C7: a per-user preference is not a per-device answer (B498)"
else
    bad "C7: exitpin still turns a per-user row into a device badge"
fi

# ═══════════════ D. the screenshot's bug cannot come back ═══════════════
# With nothing pinned the cell must be a dash, not a <select>; the assign control, if any, lives
# inside <details> so no <option> is displayed as if it had been chosen.
sel_outside="$(awk '/\{\{\$pin := \.Pin\}\}/,/\{\{end\}\}[[:space:]]*$/' "$TPL" 2>/dev/null | awk '
    /<details/ { d = 1 }
    /<\/details>/ { d = 0 }
    /<select/ && !d { print NR": "$0 }
')"
if [ -z "$sel_outside" ]; then
    ok "D1: no <select> outside <details> in the exit cell (no phantom pin)"
else
    bad "D1: a <select> renders outside <details>:$sel_outside"
fi
# And the reason for a free choice must be stated, not implied by an empty cell.
if grep -q 'exitpin.source.none' "$CAT" 2>/dev/null; then
    ok "D2: a free choice says «выбирает само» instead of showing a dash alone"
else
    bad "D2: the free-choice label is missing"
fi

# ═══════════════ E. i18n: assembled keys ═══════════════
# The template asks for "exitpin.reason." + Reason and "exitpin.source." + Source, so a typo in a
# token would be invisible to TestCatalogsParity (key SETS only) and to the B325 ratchet (which
# looks at keys USED in templates literally). Assert each assembled key is defined TWICE (RU+EN).
missing=""
for k in source.none source.device source.user \
         reason.derived reason.via_off reason.relay_down reason.no_device_tag \
         reason.tagged_no_user_src reason.free_choice reason.no_relay; do
    n="$(grep -c "^	\"exitpin.$k\":" "$CAT" 2>/dev/null)"
    [ "${n:-0}" -ge 2 ] || missing="$missing exitpin.$k(${n:-0}/2)"
done
if [ -z "$missing" ]; then
    ok "E1: all 10 assembled i18n keys exist in BOTH catalogues"
else
    bad "E1: assembled keys missing:$missing"
fi
if grep -q 'TestB497_EveryReasonHasAnI18nKey' internal/exitpin/exitpin_b497_test.go 2>/dev/null; then
    ok "E2: a unit test keeps that link honest as reasons are added"
else
    bad "E2: nothing guards the assembled keys"
fi

# ═══════════════ F. display-only ═══════════════
leak="$(grep -rln 'internal/exitpin' --include='*.go' \
        internal/acl internal/prefixowner internal/feature/exit_rules 2>/dev/null | grep -v '_test.go')"
if [ -z "$leak" ]; then
    ok "F1: no decision package imports exitpin — the ACL generator is untouched"
else
    bad "F1: a decision package now depends on the page's view model:$leak"
fi
if grep -q 'internal/acl' "$PKG" "$ADAPTERS" 2>/dev/null; then
    bad "F2: exitpin imports the ACL package"
else
    ok "F2: exitpin does not import the ACL package"
fi

# ═══════════════ G. discoverability ═══════════════
if grep -q 'check_b497_effective_exit_pin.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "G1: registered in the catalog"
else
    bad "G1: not registered in scripts/verify_pre_deploy.sh"
fi
# Trap #11 is «.gitignore ate the file», not «you have not committed yet»: `ls-files`
# reads the INDEX, so a staged file passes, and a file that is merely untracked passes
# only when it is NOT ignored (the release commit then carries it).
if git ls-files --error-unmatch scripts/check_b497_effective_exit_pin.sh >/dev/null 2>&1; then
    ok "G2: git tracks this script (trap #11)"
elif git check-ignore -q scripts/check_b497_effective_exit_pin.sh 2>/dev/null; then
    bad "G2: this script is IGNORED by .gitignore — it would never reach the release (trap #11)"
else
    ok "G2: this script is not ignored (untracked until the release commit)"
fi
if grep -q '^\- \*\*B497\*\*' AGENTS.md 2>/dev/null; then ok "G3: the AGENTS index carries its line"; else bad "G3: no AGENTS index line"; fi
if grep -q 'B497' docs/ROADMAP.md 2>/dev/null; then ok "G4: the roadmap records it"; else bad "G4: the roadmap does not mention B497"; fi
if grep -q 'B497' RELEASE-NOTES.md 2>/dev/null; then ok "G5: the release notes describe it"; else bad "G5: no release-notes entry"; fi

echo
echo "  B497: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
