#!/usr/bin/env bash
# check_b397_route_union.sh
#
# 2026-10-18 (B397) — EVERY RELAY THE ASSIGNMENT TABLE NAMES MUST BE VISITED WITH THE
# UNION OF WHAT IT OWNS, BECAUSE `--advertise-routes` REPLACES THE WHOLE SET.
#
# WHY (measured on the operator's installation, 2026-10-18)
#   The B394 catalogue's `prefix_owner.consistency` reported four prefixes "owned by X,
#   but X does not advertise it". The database said:
#
#     prefix_owner: karolina|explicit|215   emilia|auto|2   sharlotta|auto|2
#     the four: 64.233.162.91/32 + .136/32 -> emilia, .93/32 + .190/32 -> sharlotta
#     claims:   1 each, from cyborg's 16 Google rules, ALL with an empty exit_node_id
#
#   A rule with an empty `exit_node_id` claims the prefix but names no relay, so
#   `exitRoutes` never lists it; the engine auto-assigned those prefixes to emilia and
#   sharlotta "for load". The sync then visited emilia (a rule named her for something
#   else) and applied her BASES only — the per-rule loop can only advertise prefixes in
#   that relay's OWN candidate list — while the "relays the table names" loop SKIPPED
#   her precisely because a rule already named her (`relaysFromAssignment`'s `already`
#   guard). Nobody advertised those four prefixes; every device pinned to them
#   black-holed; only the system test noticed.
#
# CONTRACTS
#   A. one pass per relay over the UNION (table ∪ that relay's rule candidates)
#   B. the old skip-rule is gone for good (no `relaysFromAssignment` with `already`)
#   C. the base routes are still prepended at the apply boundary (replace semantics)
#   D. the B352 prune loop and the zero-claim release (Prune) still exist
#   E. behavioural pin (Go tests), registration and documentation
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9/B386).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/exit_rules/sync_routes.go ] || {
  printf 'B397: cannot locate internal/feature/exit_rules from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b397.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

# The exit-rules route-sync SURFACE (B339: a surface, not one file), non-test files.
# internal/prefixowner/prefixowner.go is here because the zero-claim release (Prune)
# and the claims/the assignment split live there.
SURF="$SKY_TMP/route_surface.go"
for f in internal/feature/exit_rules/sync_routes.go internal/feature/exit_rules/relay_keepalive_b352.go internal/feature/exit_rules/prefix_owner.go internal/prefixowner/prefixowner.go; do
  cat "$f" >> "$SURF" 2>/dev/null || true
done
GO_TEST=internal/feature/exit_rules/sync_routes_b397_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }

hdr "B397 — one pass per relay, over the union of what it owns"

SURF_SRC="$(cat "$SURF" 2>/dev/null || true)"
TEST_SRC="$(cat "$GO_TEST" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# A. the union
# ---------------------------------------------------------------------------
hdr "A — the apply loop visits the union and advertises the union"

if has 'relaysToSync(owners, exitRoutes)' "$SURF_SRC" && has 'func relaysToSync(' "$SURF_SRC"; then
  ok "A1: the apply loop iterates relaysToSync(owners, exitRoutes) — rules AND table"
else
  bad "A1: the apply loop does not start from the union of rules and the assignment table"
fi
if has 'mergedOwnedPrefixes(node, exitRoutes[node], owners)' "$SURF_SRC" && has 'func mergedOwnedPrefixes(' "$SURF_SRC"; then
  ok "A2: each relay is applied with mergedOwnedPrefixes (its rule candidates ∪ its table rows)"
else
  bad "A2: a relay's advertise set is not the union — a table-owned prefix can still be dropped"
fi
# A3 RENEGOTIATED by B441 (2026-10-29). It asserted the two literal `add(...)` calls, and
# B441 found that the FIRST of them — OwnedPrefixes(node, ruleCandidates, owners) — silently
# dropped a rule candidate whose table row had not been written yet (OwnedPrefixes keeps a
# prefix only when owners[p] == node), so on an empty table the union returned NOTHING. The
# halves are now written out: a candidate is kept when the table has no opinion about it,
# and the table's own rows are added after. Both halves must still be present, which is what
# this contract is about.
if has 'owners[p] == "" || owners[p] == node' "$SURF_SRC" && has 'OwnedPrefixesForRelay(node, owners)' "$SURF_SRC"; then
  ok "A3: both halves are explicitly unioned (the rules when the table has no opinion, then the table's own rows)"
elif has 'add(OwnedPrefixes(node, ruleCandidates, owners))' "$SURF_SRC" && has 'add(OwnedPrefixesForRelay(node, owners))' "$SURF_SRC"; then
  ok "A3: both halves are explicitly unioned (the pre-B441 form)"
else
  bad "A3: mergedOwnedPrefixes does not union both halves"
fi

# ---------------------------------------------------------------------------
# B. the skip rule is gone
# ---------------------------------------------------------------------------
hdr "B — the 'already covered' skip that skipped the owner is gone"

if has 'func relaysFromAssignment(' "$SURF_SRC"; then
  bad "B1: relaysFromAssignment still exists — its 'already' guard is what skipped emilia"
else
  ok "B1: relaysFromAssignment (with its 'already' guard) is gone"
fi
if has 'relaysToSync(' "$SURF_SRC" && ! has 'if _, covered := already[owner]; covered' "$SURF_SRC"; then
  ok "B2: no relay is skipped for being named by a rule"
else
  bad "B2: a relay can still be skipped for being named by a rule"
fi

# ---------------------------------------------------------------------------
# C. replace semantics: the base routes are still prepended
# ---------------------------------------------------------------------------
hdr "C — the base routes are prepended at the apply boundary"

if has 'approveRoutes := []string{"0.0.0.0/0", "::/0"}' "$SURF_SRC"; then
  ok "C1: syncOneExitNode still prepends 0.0.0.0/0 + ::/0 (a table-only set cannot unmake an exit node)"
else
  bad "C1: the base routes are not prepended — a relay could stop advertising the default route"
fi

# ---------------------------------------------------------------------------
# D. the prune halves that already existed
# ---------------------------------------------------------------------------
hdr "D — the B352 prune loop and the zero-claim release still exist"

if has 'relaysToKeepSynced(covered)' "$SURF_SRC"; then
  ok "D1: B352's prune loop is intact (a relay that owns nothing is painted DOWN to the bases)"
else
  bad "D1: the B352 prune loop is gone — a stale advertisement would survive forever"
fi
if has 'func Prune(' "$SURF_SRC" && has 'source == "manual" || claimed[prefix]' "$SURF_SRC"; then
  ok "D2: Prune still releases unclaimed auto rows and keeps operator pins (this half already existed)"
else
  bad "D2: the zero-claim release no longer keeps manual pins"
fi

# ---------------------------------------------------------------------------
# E. behavioural pin, registration, documentation
# ---------------------------------------------------------------------------
hdr "E — pinned by tests, registered, documented"

for name in TestB397_RelaysToSyncIsTheUnion TestB397_MergedOwnedPrefixesIsTheUnion TestB397_MergedOwnedPrefixesKeepsOneAdvertiser TestB397_RelaysToSyncIgnoresGarbage; do
  if has "func $name(" "$TEST_SRC"; then
    ok "E1: $name pins it behaviourally"
  else
    bad "E1: $name is missing"
  fi
done
if git ls-files --error-unmatch scripts/check_b397_route_union.sh >/dev/null 2>&1; then
  ok "E2: this script is tracked by git (trap #11)"
else
  bad "E2: this script is not tracked"
fi
if grep -q 'check_b397_route_union' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E3: registered in scripts/verify_pre_deploy.sh"
else
  bad "E3: not registered in the catalog"
fi
if grep -q '^\- \*\*B397\*\*' AGENTS.md 2>/dev/null; then
  ok "E4: one-line entry in the AGENTS.md block index"
else
  bad "E4: no B397 entry in the AGENTS block index"
fi
if grep -q 'B397' docs/ROADMAP.md 2>/dev/null && grep -q 'B397' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E5: both roadmaps record the block (rule 12)"
else
  bad "E5: a roadmap does not record the block"
fi

printf '\n\033[1mB397: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
