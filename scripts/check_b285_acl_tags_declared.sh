#!/usr/bin/env bash
# check_b285_acl_tags_declared.sh
#
# 2026-09-22 (B285) — "a policy must declare every tag its grants reference".
#
# Live on `aro`, one pass after the B284 fix landed:
#
#	acl-drift: re-apply FAILED (refusing to set a policy that references 1
#	tag(s) missing from tagOwners: tag:dev-daniil-workpc — headscale rejects
#	such a document and will not start on it) — the live policy stays stale;
#	the next pass retries
#
# The refusal came from B283's guard, and it was CORRECT: headscale rejects such
# a document as a whole ("tag not found"), and on a `policy.mode: file` host the
# daemon then will not START (crash-loop 248, control plane down, every device
# gone from the portal). But it also meant the ACL could never apply: the
# generated document named a tag it never declared.
#
# Why the two disagreed: the grants take their device tag from
# `node_owner_map.tag` (B284) while the tagOwners block derives its entries from
# `GetPerUserDeviceTags`, which needs a PORTAL-user row. On the live host both
# node_owner_map rows are owned by headscale's synthetic `tagged-devices`, so
# the per-user block emitted nothing while the grants named
# `tag:dev-daniil-workpc` (the client) and `tag:dev-infra-exit-node-vps` (the
# relay; only the B283 owner-tag path covers that one).
#
# Fix: the generator collects every tag its grants name and sweeps the missing
# ones into tagOwners, so the document is self-consistent by construction — the
# same invariant `SetPolicy` enforces (B283) is now satisfied instead of merely
# reported.
#
# CONTRACTS
#   A. the generator sweeps its grant tags into tagOwners
#   B. the regression test exists, asserts the invariant, and passes
#   C. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B285: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ACL=internal/acl/acl.go
TEST=internal/acl/acl_b285_test.go

hdr "B285 — a policy must declare every tag its grants reference"

# --- A: the sweep -----------------------------------------------------------
if grep -q 'grantTags\[devTag\] = true' "$ACL"; then
  ok "A1: the grants loop records every device tag it names"
else
  bad "A1: the grants loop does not collect its tags — the tagOwners sweep has nothing to work from"
fi
if grep -q 'missingGrantTags' "$ACL" && \
   { grep -qF 'emitTagOwner2(tag, ownerJSON)' "$ACL" || grep -qF 'emitTagOwner2(tag, "[\""+owner+"\"]")' "$ACL"; }; then
  ok "A2: the generator declares the missing grant tags (owner derived from the tag)"
else
  bad "A2: no sweep — a grant tag missing from tagOwners keeps the whole ACL unappliable"
fi
if grep -q 'B285' "$ACL"; then
  ok "A3: the sweep carries its incident reference"
else
  bad "A3: the sweep is undocumented"
fi

# --- B: the regression test -------------------------------------------------
if [ -f "$TEST" ]; then
  ok "B1: $TEST exists"
else
  bad "B1: $TEST is missing"
fi
if grep -q 'UndeclaredTags(gen)' "$TEST"; then
  ok "B2: the test asserts the headscale invariant (no undeclared tag references)"
else
  bad "B2: the test does not assert the invariant"
fi
if grep -q 'tagged-devices' "$TEST"; then
  ok "B3: the test reproduces the live shape (node_owner_map owned by the synthetic user)"
else
  bad "B3: the test does not cover the synthetic-owner shape — it would pass vacuously"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/acl/ -run 'B285' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "B4: the B285 test passes"
  else
    bad "B4: the B285 test failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/acl/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "B5: the whole internal/acl package still passes"
  else
    bad "B5: internal/acl failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "B4-B5: go not on PATH — run the B285 tests on the VM"
fi

# --- C: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b285_acl_tags_declared.sh >/dev/null 2>&1; then
  ok "C1: scripts/check_b285_acl_tags_declared.sh is tracked by git"
else
  bad "C1: scripts/check_b285_acl_tags_declared.sh is NOT tracked"
fi

printf '\n\033[1mB285 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
