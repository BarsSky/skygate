#!/usr/bin/env bash
# check_b284_device_tag_source.sh
#
# 2026-09-22 (B284) — "a device tag must be the tag the node carries".
#
# LIVE INCIDENT (native host `aro`, headscale `policy.mode: file`). The
# generated policy referenced two tags that existed on no node and in no
# `tagOwners` block:
#
#	tag:dev-tagged-devices-exit-node-vps
#	tag:dev-tagged-devices-workpc
#
# headscale rejects a policy that references an undeclared tag AS A WHOLE
# ("tag not found"), and in file mode it will not START on it: crash-loop
# counter 248, control plane down, every device gone from the portal.
#
# Proof from the host (python3 over the saved snapshots):
#
#	/tmp/policy.297.json undeclared: []                                  # working
#	/tmp/snap.304.json   undeclared: ['tag:dev-tagged-devices-exit-node-vps',
#	                                  'tag:dev-tagged-devices-workpc']   # refused
#
# Root cause: the generator SYNTHESISED `tag:dev-<user>-<host>` from a user name
# instead of reading the tag off the node. Three sites did it, and the name they
# used was `device_rules.user_name` / `device_exit_node_prefs.username` — on the
# live host headscale's synthetic owner for tagged nodes, `tagged-devices`.
# `tagOwners` is built from `node_owner_map.tag` (and from portal-user rows), so
# a tag minted from the synthetic owner can never be declared: the document is
# invalid by construction.
#
# CONTRACTS
#   A. the rule→tag resolver reads node_owner_map.tag and never invents one
#   B. the per-device pref loops resolve the tag by hostname, not from a user name
#   C. the regression tests exist and pass, and the B265 contract is renegotiated
#   D. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B284: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ACL=internal/acl/acl.go
TEST=internal/acl/acl_b284_test.go
B265=internal/acl/acl_b265_test.go

hdr "B284 — a device tag must be the tag the node carries"

# --- A: the resolver reads the node's tag -----------------------------------
if grep -q 'Tag string' "$ACL" && grep -q 'resolveNodeOwners' "$ACL"; then
  ok "A1: deviceOwner carries the node's tag from node_owner_map"
else
  bad "A1: deviceOwner has no tag field — the resolver cannot know what the node carries"
fi
# Inside deviceTagForRule there must be no synthesis left.
if awk '/^func deviceTagForRule\(/,/^}/' "$ACL" | grep -q '"tag:dev-"'; then
  bad "A2: deviceTagForRule still SYNTHESISES a tag (\`tag:dev-\` + user) — that is the live undeclared tag"
else
  ok "A2: deviceTagForRule invents no tag"
fi
if awk '/^func deviceTagForRule\(/,/^}/' "$ACL" | grep -q 'o.Tag'; then
  ok "A3: deviceTagForRule returns the node's tag"
else
  bad "A3: deviceTagForRule does not read the node's tag"
fi

# --- B: the pref loops resolve by hostname ----------------------------------
if grep -q 'tagsByHostOld := prefixowner.TagsByHost(d)' "$ACL" && grep -q 'tagsByHost := prefixowner.TagsByHost(d)' "$ACL"; then
  ok "B1: both pref loops resolve the device tag through node_owner_map (TagsByHost)"
else
  bad "B1: a pref loop still mints the tag from a user name"
fi
if grep -q '"tag:dev-" + dp.Username' "$ACL"; then
  bad "B2: a per-device pref loop still builds tag:dev-<username>-<host> (the synthetic-owner path)"
else
  ok "B2: no pref loop builds a tag from a username"
fi

# --- C: tests exist, pass, and the B265 contract was renegotiated -----------
if [ -f "$TEST" ]; then
  ok "C1: $TEST exists"
else
  bad "C1: $TEST is missing"
fi
if grep -q 'tagged-devices' "$TEST" && grep -q 'tag:dev-tagged-devices-' "$TEST"; then
  ok "C2: the regression test reproduces the live synthetic-owner shape"
else
  bad "C2: the regression test does not cover the synthetic owner (it would not catch the outage)"
fi
if grep -q 'B284 (2026-09-22) — CONTRACT RENEGOTIATED' "$B265"; then
  ok "C3: the B265 contract is explicitly renegotiated (synthesis removed)"
else
  bad "C3: the B265 test still pins the synthesised tag"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/acl/ -run 'B284' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C4: the B284 tests pass"
  else
    bad "C4: the B284 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/acl/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C5: the whole internal/acl package still passes (B188.2/B265/B274/B275/B276 contracts)"
  else
    bad "C5: internal/acl failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "C4-C5: go not on PATH — run the B284 tests on the VM"
fi

# --- D: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b284_device_tag_source.sh >/dev/null 2>&1; then
  ok "D1: scripts/check_b284_device_tag_source.sh is tracked by git"
else
  bad "D1: scripts/check_b284_device_tag_source.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB284 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
