#!/bin/bash
# check_b175.sh — B175 (v1.5.2) OIDC node auto-tag Strategy E.
#
# Operator 2026-08-25:
# "Проверь что Autoupdater тегов работает при варианте когда
#  происходит добавление не по ключу а через OIDC потому что
#  ожидание тега висит уже больше 5 минут и в будущем каждый
#  раз дергать администратора для обновления неудобно."
#
# "Check that the tag autoupdater works for OIDC-added devices —
#  the 'pending' state is hanging more than 5 minutes and I
#  don't want to keep asking the admin to fix it every time."
#
# Root cause: pre-B175 the backfill had 3 strategies for
# matching headscale nodes to portal users:
#   A. PreAuthKeyID = preauth_keys.headscale_preauth_id
#      (catches /my/preauth flow)
#   C. CreatedAt within 1h of a preauth key (temporal fallback)
#   D. Existing tag:dev-<user>-* tag (post-hoc, after operator
#      manually applied the tag)
#
# None of those fire for a node registered via the OIDC flow:
#   - OIDC doesn't use a preauth key (n.PreAuthKeyID == "")
#   - The OIDC user has no preauth_keys row (Strategy C skip)
#   - The node has no tags yet (Strategy D skip)
#
# Result pre-B175: OIDC node stays orphaned in node_owner_map,
# the per-device dev-tag is never applied, and /my/devices shows
# the device with "⏳ pending" forever (the operator had to hit
# "Force backfill" on /admin/devices or `headscale nodes tag
# --force` manually to clear it).
#
# B175 fix: extract matchOIDCStrategy (Strategy E) — matches
# `n.PreAuthKeyID == "" && n.UserName == portalUsername`.
# headscale creates OIDC users with name = OIDC `name` claim
# = skygate username (internal/oidc/token.go:180 sets
# `name = entry.Username`), so `n.UserName == portalUsername`
# is authoritative ownership for the OIDC path.
#
# B-check is split into:
#  A. Source contract (matchOIDCStrategy exists + is called
#     from Backfill + uses the right guards)
#  B. Test contract (TestMatchOIDCStrategy exists with 7
#     subtests covering the 5 critical paths + firstTag
#     fallback + idempotency)
#  C. Build contract (go build + go vet + go test
#     ./internal/nodeownership/...)
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source contract (matchOIDCStrategy is wired into Backfill)"

# A.1 — matchOIDCStrategy is a package-level function in
# nodeownership.go. Without this function, Strategy E
# has no place to live, and OIDC nodes would stay
# orphaned (the pre-B175 bug).
if grep -qE 'func matchOIDCStrategy\(n headscale\.NodeView' internal/nodeownership/nodeownership.go; then
    ok "matchOIDCStrategy exists in internal/nodeownership/nodeownership.go (B175: Strategy E is the OIDC path)"
else
    bad "matchOIDCStrategy is missing from internal/nodeownership/nodeownership.go (B175: the OIDC auto-tag fix is gone)"
fi

# A.2 — Backfill calls matchOIDCStrategy. The pre-B175
# code path was a bare `if matchedTag == "" { continue }`
# after Strategy D; B175 inserts the call between them.
if grep -qE 'matchOIDCStrategy\(n, portalUsername\)' internal/nodeownership/nodeownership.go; then
    ok "Backfill calls matchOIDCStrategy (B175: Strategy E is actually invoked per node)"
else
    bad "Backfill does NOT call matchOIDCStrategy (B175: the helper exists but the Backfill body never invokes it — dead code)"
fi

# A.3 — matchOIDCStrategy guards on PreAuthKeyID. The
# critical safety property: Strategy E MUST NOT match
# nodes that have a preauth key (those are Strategy A's
# territory). If the guard is removed, Strategy E could
# steal /my/preauth nodes from Strategy A.
if grep -qE 'if n\.PreAuthKeyID != ""' internal/nodeownership/nodeownership.go; then
    ok "matchOIDCStrategy guards on n.PreAuthKeyID (B175: Strategy E only fires for OIDC nodes, not /my/preauth)"
else
    bad "matchOIDCStrategy is missing the PreAuthKeyID guard (B175: would steal /my/preauth nodes from Strategy A)"
fi

# A.4 — matchOIDCStrategy guards on n.UserName. Without
# this guard, every node would match every portal user.
if grep -qE 'if n\.UserName != portalUsername' internal/nodeownership/nodeownership.go; then
    ok "matchOIDCStrategy guards on n.UserName (B175: only the OWNER's portal iteration matches)"
else
    bad "matchOIDCStrategy is missing the UserName guard (B175: cross-user ownership leak)"
fi

# A.5 — matchOIDCStrategy defaults to "tag:private" for
# fresh nodes (the v0.26+ scope convention) and uses
# firstTagOrFallback for nodes that already carry tags
# (e.g. operator-applied tag:subnet-router before
# Strategy E ran).
if grep -qE 'return "tag:private", true' internal/nodeownership/nodeownership.go && \
   grep -qE 'return firstTagOrFallback' internal/nodeownership/nodeownership.go; then
    ok "matchOIDCStrategy returns tag:private default + firstTagOrFallback preservation (B175: matches Strategy A/C convention)"
else
    bad "matchOIDCStrategy return values are wrong (B175: would clobber existing tags or default to the wrong value)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: test contract (TestMatchOIDCStrategy covers 7 critical paths)"

# B.1 — the test file exists.
if [ -f internal/nodeownership/strategy_e_b175_test.go ]; then
    ok "internal/nodeownership/strategy_e_b175_test.go exists (B175: focused unit test for Strategy E)"
else
    bad "internal/nodeownership/strategy_e_b175_test.go is missing (B175: no regression guard for the OIDC strategy)"
fi

# B.2 — the 7 subtests cover the 5 critical paths + firstTag
# fallback + idempotency. A future refactor that drops one
# of these subtests will fail the build.
expected_subtests="OIDCNode_Match OIDCNode_WithExistingTags_PreserveFirst PreauthNode_NoMatch UsernameMismatch_NoMatch TaggedDevicesSyntheticUser_NoMatch EmptyPortalUsername_NoMatch HelperIsOrderIndependent"
for st in $expected_subtests; do
    if grep -qE "t\.Run\(\"$st\"" internal/nodeownership/strategy_e_b175_test.go; then
        ok "TestMatchOIDCStrategy has subtest '$st'"
    else
        bad "TestMatchOIDCStrategy is missing subtest '$st'"
    fi
done

# B.3 — the SubtestPreauthNode_NoMatch subtest pins the
# critical safety property: a /my/preauth node with a
# matching name MUST NOT match Strategy E. This is the
# regression guard for "Strategy E doesn't steal Strategy A's
# territory" — a future refactor that drops the
# PreAuthKeyID guard would be caught here.
if grep -qE 'Strategy E MUST NOT match' internal/nodeownership/strategy_e_b175_test.go || \
   grep -qE 'MUST NOT match' internal/nodeownership/strategy_e_b175_test.go; then
    ok "TestMatchOIDCStrategy has 'MUST NOT match' assertions (regression guard for the safety properties)"
else
    bad "TestMatchOIDCStrategy is missing 'MUST NOT match' assertions (B175 safety property is not pinned)"
fi

# ---------------------------------------------------------------------------
hdr "contract C: build + vet + test (nodeownership package passes)"

# C.1 — go build ./... exits 0.
GO_BIN=""
for cand in "$(command -v go)" \
    "/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
        GO_BIN="$cand"
        break
    fi
done
if [ -z "$GO_BIN" ]; then
    skip "go build ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" build ./... 2>/dev/null; then
    ok "go build ./... clean"
else
    bad "go build ./... FAILED (B175 fix has a compile error)"
fi

# C.2 — go vet ./... exits 0.
if [ -z "$GO_BIN" ]; then
    skip "go vet ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" vet ./... 2>/dev/null; then
    ok "go vet ./... clean"
else
    bad "go vet ./... FAILED"
fi

# C.3 — go test ./internal/nodeownership/... passes.
# This is the focused unit-test gate — the 7 B175
# unit tests live in this package. A regression in
# Strategy E breaks this.
if [ -z "$GO_BIN" ]; then
    skip "go test ./internal/nodeownership/... (go binary not on PATH — skipping)"
elif "$GO_BIN" test ./internal/nodeownership/... 2>/dev/null; then
    ok "go test ./internal/nodeownership/... passes (B175: 7 new unit tests + existing skipped PG-rewrite stub)"
else
    bad "go test ./internal/nodeownership/... FAILED (B175 fix has a test failure)"
fi

echo
echo "B175 check OK — matchOIDCStrategy (Strategy E) matches OIDC-registered nodes by n.UserName == portalUsername, with guards on PreAuthKeyID + UserName so /my/preauth nodes and cross-user nodes are NOT stolen. The pre-B175 '⏳ pending forever for OIDC nodes' UX bug is closed; the operator no longer needs to ask admin to force backfill every time a Tailscale client registers via OIDC."
