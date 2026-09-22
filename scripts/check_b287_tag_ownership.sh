#!/usr/bin/env bash
# check_b287_tag_ownership.sh
#
# 2026-09-22 (B287) — "the per-device tag is an ownership record".
#
# LIVE REPORT (operator, native host `aro`): "устройства что с тегами пользователя
# не отображаются в его устройствах на странице мои устройства а только во все
# устройства" — the devices carrying the user's tags do not appear on /my/devices,
# only on /admin/devices.
#
# The screenshot showed why: EVERY row on /admin/devices listed
# `tagged-devices` as the owner —
#
#	id  hostname        owner            tag
#	1   exit-node-vps   tagged-devices   tag:dev-infra-exit-node-vps
#	2   workpc          tagged-devices   tag:dev-daniil-workpc
#	3   laptop          tagged-devices   tag:dev-daniil-laptop
#
# headscale reassigns a node to the synthetic `tagged-devices` user as soon as it
# wears any tag, and the DB snapshot copied that name verbatim. Both ownership
# tests on /my/devices keyed on that column (the live `n.UserName == username`
# and `ListNodeOwnerNodeIDsByUsername`), so the user's own tagged devices were
# invisible on their page. /admin/devices listed them — as `tagged-devices`,
# which is why the operator's question was "what is wrong with the tags".
#
# The tag names the owner: `tag:dev-<user>-<host>` is minted per user and
# headscale only accepts it once that user (or the synthetic owner) owns the
# node. `internal/db/device_tag.go` is now the single place that answers the
# question.
#
# CONTRACTS
#   A. the shared helpers exist and are the only tag-minting/parsing site used
#   B. /my/devices uses them in BOTH ownership paths (live + snapshot), and the
#      synthetic `tagged-devices` owner is not by itself a "ghost" (B3/B4)
#   C. the regression tests exist and pass
#   D. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B287: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

TAGS=internal/db/device_tag.go
MYDEV=internal/feature/my/devices.go
TEST=internal/db/device_tag_b287_test.go

hdr "B287 — the per-device tag is an ownership record"

# --- A: the shared helpers ---------------------------------------------------
for fn in PerDeviceTag TagNamesUser HasPerDeviceTag ListNodeOwnerNodeIDsByUserTag; do
  if grep -q "^func $fn(" "$TAGS" 2>/dev/null; then
    ok "A1: $fn is defined in $TAGS"
  else
    bad "A1: $fn is missing — callers will re-derive the tag (and disagree)"
  fi
done
if grep -q 'tag:dev-infra-' "$TAGS" && grep -q 'IsClassTag' "$TAGS"; then
  ok "A2: infra tags and class tags are explicitly excluded from \"names this user\""
else
  bad "A2: TagNamesUser does not exclude the infra/class forms"
fi
if grep -q "ESCAPE" "$TAGS"; then
  ok "A3: the LIKE lookup escapes %/_/\\\\ (a username with a wildcard cannot match another user's tags)"
else
  bad "A3: the LIKE lookup does not escape its metacharacters"
fi

# --- B: /my/devices uses them in both paths ---------------------------------
if grep -q 'db.HasPerDeviceTag(n.Tags, username, n.Hostname)' "$MYDEV"; then
  ok "B1: the live path accepts a node whose per-device tag names the user"
else
  bad "B1: the live path still keys on n.UserName only — a tagged node hides from its owner"
fi
if grep -q 'db.ListNodeOwnerNodeIDsByUserTag(s.dbc(), username)' "$MYDEV"; then
  ok "B2: the snapshot path unions the by-tag lookup with the by-username one"
else
  bad "B2: the snapshot path still misses rows whose username is the synthetic owner"
fi
# B3: the synthetic owner is not by itself a "ghost". headscale puts a node in
# `tagged-devices` both when it was registered with a key that carried no --user
# (a real ghost, needing the B-mod-reregister flow) and when it merely wears a
# tag (correctly attributed — the tag IS the ownership record). Inviting the
# second kind to re-register would delete and re-key a device that is fine.
GHOST_SITES=$(grep -c 'IsTaggedGhost:.*tagged-devices.*&& !devTagApplied' "$MYDEV" || true)
if [ "${GHOST_SITES:-0}" -ge 2 ]; then
  ok "B3: both IsTaggedGhost sites also require that no per-device tag names the user (${GHOST_SITES} sites)"
else
  bad "B3: IsTaggedGhost is still true for a correctly tagged device (${GHOST_SITES:-0} guarded site(s), want >= 2)"
fi
if grep -q 'IsTaggedGhost:.*n.UserName == "tagged-devices"' "$MYDEV"; then
  ok "B4: the B-mod-reregister contract still sees the sentinel-owner test on both sites"
else
  bad "B4: the sentinel-owner test disappeared — check_b_mod_reregister.sh contract E expects it"
fi

# --- C: the regression tests -------------------------------------------------
if [ -f "$TEST" ]; then
  ok "C1: $TEST exists"
else
  bad "C1: $TEST is missing"
fi
if grep -q 'tagged-devices' "$TEST" && grep -q "len(byName) != 0" "$TEST"; then
  ok "C2: the test reproduces the live shape and pins the pre-B287 blind spot"
else
  bad "C2: the test does not reproduce the synthetic-owner shape"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ -run 'B287' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C3: the B287 tests pass"
  else
    bad "C3: the B287 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/db/ ./internal/feature/my/ -count=1 2>&1)"
  if ! grep -q 'FAIL' <<< "$OUT"; then
    ok "C4: internal/db and internal/feature/my pass"
  else
    bad "C4: a package failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "C3-C4: go not on PATH — run the B287 tests on the VM"
fi

# --- D: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b287_tag_ownership.sh >/dev/null 2>&1; then
  ok "D1: scripts/check_b287_tag_ownership.sh is tracked by git"
else
  bad "D1: scripts/check_b287_tag_ownership.sh is NOT tracked"
fi

printf '\n\033[1mB287 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
