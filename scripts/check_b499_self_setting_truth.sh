#!/usr/bin/env bash
# B499 (2026-11-19) — ДВЕ ВЕЩИ, КОТОРЫЕ «ВЫСТАВЛЯЮТСЯ» И «ПОЯВЛЯЮТСЯ» САМИ.
#
# Both answers come from the same operator conversation, and both are the engine
# contradicting a decision the operator had already made.
#
# (C) «На телефоны не выставляется принудительная привязка — они свободно выбирают
#     exit node, потому что у них нет accept-routes, а правила регламентируют сами.»
#     That decision is implemented three ways: `via_enabled` is opt-in, the ACL emits
#     `via` only for via_enabled rows, and B398 refuses to let a DERIVED row become a
#     filter. And yet every CREATE branch of the preference reconciler hard-coded
#     `ViaEnabled: true`, so the row the engine wrote for `basic` said `via_enabled=1`
#     with `set_by_user_id=0`. B398 made it HARMLESS; nothing made it HONEST — the flag
#     is what the pages read, which is why the operator kept seeing a strict pin that
#     did not exist. B499 writes `via_enabled = 0` for every derived row.
#
# (D) «a71 добавлен через OIDC, может это повлияло?» — measured: a71 is
#     `register_method=authkey` (key 129), NOT OIDC; the OIDC devices are `skybars` and
#     `skybars-secure`. But OIDC left a real trace: it created DUPLICATE headscale users
#     named `skyadmin` (ids 88, 90), and the reconciler answered with this hourly row —
#
#       {"outcome":"duplicate_name","old_hs_id":1,"candidate_hs_id":"88",
#        "action":"operator_must_pick","hint":"... users delete -i <oidc-id> --force ..."}
#
#     Three defects: `<oidc-id>` is not an id; nothing ever DECIDED the case; and the
#     «is this link still valid?» test scanned only the deduplicated by-name map, so a
#     healthy row could be reported as a duplicate for weeks. B499 makes `CreateUser`
#     find-before-create (so skygate cannot mint a duplicate), decides the unambiguous
#     case itself (one candidate referenced by the portal table, or none referenced and
#     one strictly older — the OPPOSITE of the pre-B243 «last entry wins» bug), refuses
#     only the genuinely ambiguous one (two portal users on two different duplicates),
#     and always names REAL ids.
#
# Contracts:
#   A. C — no CREATE branch claims strict mode; the three reasons still exist; a human's
#      switch is still carried forward unchanged
#   B. D — CreateUser cannot create a duplicate; the resolver exists and is used; the
#      duplicate audit row names real ids; the by-name stub that could only answer "2"
#      is gone; the link test uses the full candidate list
#   D. neither half reaches the ACL generator (both are data-plane honesty, not policy)
#   E. discoverable: registered, tracked, indexed, roadmap, release notes
#   F. the behavioural halves run when a Go toolchain is available (SKIP otherwise)

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

RECON="internal/feature/exit_rules/reconciler.go"
RECON_T="internal/feature/exit_rules/reconciler_b499_test.go"
B399_T="internal/feature/exit_rules/reconciler_b399_test.go"
REC="internal/headscale/reconcile.go"
RESOLVER="internal/headscale/reconcile_b499.go"
USERS="internal/headscale/users.go"

# ═══════════════ A. C — a derived row must not CLAIM strict mode ═══════════════
if [ -f "$RECON" ]; then ok "A1: $RECON exists"; else bad "A1: $RECON is missing"; fi
true_left="$(grep -c 'ViaEnabled:     true' "$RECON" 2>/dev/null)"
if [ "${true_left:-0}" = "0" ]; then
    ok "A2: no CREATE branch sets ViaEnabled: true any more"
else
    bad "A2: ${true_left} branch(es) still write a derived row as strict"
fi
false_count="$(grep -c 'ViaEnabled:     false, // B499' "$RECON" 2>/dev/null)"
if [ "${false_count:-0}" -ge 3 ]; then
    ok "A3: all three CREATE reasons write via_enabled=false (found ${false_count})"
else
    bad "A3: only ${false_count}/3 CREATE branches were changed"
fi
miss=""
for reason in missing-pref-unanimous missing-pref-owner-derived owner-overrides-rule-relay; do
    grep -q "$reason" "$RECON" 2>/dev/null || miss="$miss $reason"
done
if [ -z "$miss" ]; then
    ok "A4: the three CREATE reasons still reachable (no branch was deleted to satisfy A2)"
else
    bad "A4: a CREATE reason disappeared:$miss"
fi
# The human's switch must still be carried forward: an UPDATE writes ch.ViaEnabled,
# which the plan copies from the stored row. Assert the copy, not the output.
if grep -q 'ViaEnabled:     s.ExistingPrefVia' "$RECON" 2>/dev/null; then
    ok "A5: an UPDATE still carries the operator's stored flag"
else
    bad "A5: an UPDATE no longer carries ExistingPrefVia — a human's switch would be reset"
fi
if grep -q 'reconciler_b499_test.go' scripts/verify_pre_deploy.sh 2>/dev/null || grep -q 'B499' "$RECON_T" 2>/dev/null; then
    ok "A6: the derived-flag decision is pinned by a test file"
else
    bad "A6: no test pins the derived flag"
fi

# ═══════════════ B. D — duplicates cannot be minted, and cannot deadlock ═══════════════
list_at="$(awk '/func \(c \*Client\) CreateUser/{f=1} f && /ListUsersFresh\(\)/{print NR; exit}' "$USERS" 2>/dev/null)"
post_at="$(awk '/func \(c \*Client\) CreateUser/{f=1} f && /c\.do\("POST"/{print NR; exit}' "$USERS" 2>/dev/null)"
if [ -n "${list_at:-}" ] && [ -n "${post_at:-}" ] && [ "$list_at" -lt "$post_at" ]; then
    ok "B1: CreateUser lists users BEFORE posting (find-before-create, lines $list_at < $post_at)"
else
    bad "B1: CreateUser still posts before looking (list=$list_at post=$post_at)"
fi
if grep -q 'EqualFold\|ToLower' "$USERS" 2>/dev/null; then
    ok "B2: the existing-user lookup is case-insensitive"
else
    bad "B2: the lookup is case-sensitive — headscale's case may differ from the portal's"
fi
if grep -q 'ReconcileDuplicateResolved' "$REC" 2>/dev/null; then
    ok "B3: a resolved duplicate has its own outcome (not lumped with 'operator must pick')"
else
    bad "B3: no ReconcileDuplicateResolved outcome — nothing distinguishes a repair from a refusal"
fi
if grep -q 'b499PickCanonical(' "$REC" 2>/dev/null && grep -q 'func b499PickCanonical(' "$RESOLVER" 2>/dev/null; then
    ok "B4: the reconciler decides an unambiguous duplicate through b499PickCanonical"
else
    bad "B4: the resolver is missing or unused"
fi
if grep -q 'b499DeleteHint' "$REC" 2>/dev/null; then
    ok "B5: the audit row names the real ids to delete"
else
    bad "B5: the audit row still cannot tell the operator which id to delete"
fi
# The placeholder may survive in a COMMENT that quotes the historical audit row (it
# should — it is the evidence); what must not survive is a placeholder in the CODE that
# builds the hint. Capture first (B386/B388), then match.
rec_code="$(grep -v '^\s*//' "$REC" 2>/dev/null)"
case "$rec_code" in
    *'<oidc-id>'*) bad "B6: the placeholder <oidc-id> is still in the audit text — that is why the row repeated for weeks" ;;
    *)             ok "B6: no <oidc-id> placeholder left in the generated hint" ;;
esac
if grep -q 'func countByName' "$REC" 2>/dev/null; then
    bad "B7: countByName survived — it could only ever answer 2"
else
    ok "B7: the always-2 stub is gone (the real candidate list is used)"
fi
if grep -q 'b499HasID(candidatesByName' "$REC" 2>/dev/null; then
    ok "B8: «is this link still valid?» checks the FULL candidate list"
else
    bad "B8: the link test still scans only the deduplicated by-name map (a healthy row can look stale)"
fi
if grep -q 'TestB499_PickCanonicalTable' internal/headscale/reconcile_b499_test.go 2>/dev/null; then
    ok "B9: the decision table is pinned (referenced / oldest / ambiguous / bad id)"
else
    bad "B9: nothing pins the duplicate decision"
fi

# ═══════════════ D. neither half reaches the generator ═══════════════
# B386/B388: capture FIRST, then match — a `producer | grep -q` pipeline lets the
# producer's status (SIGPIPE under pipefail) decide the verdict.
acl_mentions="$(grep -rn 'ViaEnabled' --include='*.go' internal/acl 2>/dev/null)"
if [ -n "$acl_mentions" ]; then
    # the ACL legitimately READS the row; what must not change is that it learns a new
    # source of truth. Assert the generator still consults the stored row + B398.
    if grep -q 'SetByUserID' internal/acl/acl_generate_via.go 2>/dev/null; then
        ok "D1: the ACL still decides from the stored row and its B398 filter"
    else
        bad "D1: the ACL's B398 filter disappeared"
    fi
else
    bad "D1: the ACL no longer reads the preference table at all"
fi
if grep -q 'internal/acl' "$RESOLVER" "$RECON" 2>/dev/null; then
    bad "D2: a data-plane file imports the ACL generator"
else
    ok "D2: neither the reconciler nor the resolver imports the ACL"
fi

# ═══════════════ E. discoverability ═══════════════
if grep -q 'check_b499_self_setting_truth.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "E1: registered in the catalog"
else
    bad "E1: not registered in scripts/verify_pre_deploy.sh"
fi
if git ls-files --error-unmatch scripts/check_b499_self_setting_truth.sh >/dev/null 2>&1; then
    ok "E2: git tracks this script (trap #11)"
elif git check-ignore -q scripts/check_b499_self_setting_truth.sh 2>/dev/null; then
    bad "E2: this script is IGNORED by .gitignore (trap #11)"
else
    ok "E2: this script is not ignored (untracked until the release commit)"
fi
if grep -q '^\- \*\*B499\*\*' AGENTS.md 2>/dev/null; then ok "E3: the AGENTS index carries its line"; else bad "E3: no AGENTS index line"; fi
if grep -q 'B499' docs/ROADMAP.md 2>/dev/null; then ok "E4: the roadmap records it"; else bad "E4: the roadmap does not mention B499"; fi
if grep -q 'B499' RELEASE-NOTES.md 2>/dev/null; then ok "E5: the release notes describe it"; else bad "E5: no release-notes entry"; fi

# ═══════════════ F. behaviour ═══════════════
GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
    skip "F1: no go binary in PATH — the behavioural tests were not run"
else
    if out="$("$GOBIN" test ./internal/headscale/ ./internal/feature/exit_rules/ -run 'B499|B399|Reconcile|CreateUser' -count=1 2>&1)"; then
        ok "F1: the C and D tests are green (flags, decision table, find-before-create)"
    else
        bad "F1: go test failed:
$(printf '%s\n' "$out" | tail -20)"
    fi
fi

echo
echo "  B499: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
