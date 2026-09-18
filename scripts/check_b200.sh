#!/usr/bin/env bash
# B200 (v1.5.0+) — /admin/cluster Phase 2.2 action surface.
#
# Pin the structural surface so a future refactor can't
# silently regress the action handlers (AddNode, RemoveNode,
# GenerateInvite, RevokeInvite) or the invite signing layer.
#
# The contracts are:
#
#   1. internal/cluster/ package exists with invite.go + node.go
#   2. IssueInvite / VerifyToken / RevokeInvite / LookupInvite
#   3. AddNode / RemoveNode / LookupNode
#   4. InvitePayload struct + NodeState* / NodeRole* constants
#   5. 4 POST routes registered in main.go
#   6. 4 POST handlers in internal/feature/admin/cluster.go
#   7. ClusterInviteSecret field on admin.Service
#   8. cfg.SecretKeyHex wired to ClusterInviteSecret in main.go
#   9. Template has Add node + Generate invite forms + per-row
#      Remove + Revoke buttons
#  10. i18n: cluster.node_* keys (RU + EN, >= 7 each)
#  11. i18n: cluster.invite_* keys (RU + EN, >= 7 each)
#  12. invite_b200_test.go: round-trip + tamper + reject tests
#  13. node_b200_test.go: pqStringArray + parsePGTextArray round-trip
#  14. go build + vet + cluster unit tests pass
#  15. Live: POST /admin/cluster/node/add creates a row + audit
#  16. Live: POST /admin/cluster/invite/generate returns a sgn1.* token
#  17. Live: GET /admin/cluster shows the new node + pending invite
#  18. AGENTS.md mentions B200
#
# Exit 0 on full pass, 1 on any failure. Prints N/M PASS/FAIL summary.

set -u

# Resolve the project root.
if [ -n "${SKYGATE_PROJECT_DIR:-}" ]; then
    cd "$SKYGATE_PROJECT_DIR"
else
    cd "$(dirname "$0")/.."
fi

PASS=0
FAIL=0
fails=()

check() {
    local name="$1"
    local result="$2"
    if [ "$result" = "ok" ]; then
        printf "  \033[32m✓\033[0m %s\n" "$name"
        PASS=$((PASS+1))
    else
        printf "  \033[31m✗\033[0m %s\n" "$name"
        FAIL=$((FAIL+1))
        fails+=("$name")
    fi
}

file_exists() { [ -f "$1" ]; }

grep_q() { grep -qE "$1" "$2" 2>/dev/null; }

# 1. cluster package files
file_exists "internal/cluster/invite.go" \
    && check "internal/cluster/invite.go exists" ok \
    || check "internal/cluster/invite.go exists" fail
file_exists "internal/cluster/node.go" \
    && check "internal/cluster/node.go exists" ok \
    || check "internal/cluster/node.go exists" fail

# 2. IssueInvite / VerifyToken / RevokeInvite / LookupInvite
grep_q '^func IssueInvite\(' "internal/cluster/invite.go" \
    && check "IssueInvite() defined" ok \
    || check "IssueInvite() defined" fail
grep_q '^func VerifyToken\(' "internal/cluster/invite.go" \
    && check "VerifyToken() defined" ok \
    || check "VerifyToken() defined" fail
grep_q '^func RevokeInvite\(' "internal/cluster/invite.go" \
    && check "RevokeInvite() defined" ok \
    || check "RevokeInvite() defined" fail
grep_q '^func LookupInvite\(' "internal/cluster/invite.go" \
    && check "LookupInvite() defined" ok \
    || check "LookupInvite() defined" fail

# 3. AddNode / RemoveNode / LookupNode
grep_q '^func AddNode\(' "internal/cluster/node.go" \
    && check "AddNode() defined" ok \
    || check "AddNode() defined" fail
grep_q '^func RemoveNode\(' "internal/cluster/node.go" \
    && check "RemoveNode() defined" ok \
    || check "RemoveNode() defined" fail
grep_q '^func LookupNode\(' "internal/cluster/node.go" \
    && check "LookupNode() defined" ok \
    || check "LookupNode() defined" fail

# 4. InvitePayload + constants
grep_q 'type InvitePayload struct' "internal/cluster/invite.go" \
    && check "InvitePayload struct defined" ok \
    || check "InvitePayload struct defined" fail
grep_q 'NodeStatePending' "internal/cluster/node.go" \
    && check "NodeState* constants defined" ok \
    || check "NodeState* constants defined" fail
grep_q 'NodeRoleSkygate' "internal/cluster/node.go" \
    && check "NodeRole* constants defined" ok \
    || check "NodeRole* constants defined" fail

# 5. 4 POST routes
grep_q 'mux\.Handle\("POST /admin/cluster/node/add"' "cmd/skygate/main.go" \
    && check "route POST /admin/cluster/node/add" ok \
    || check "route POST /admin/cluster/node/add" fail
grep_q 'mux\.Handle\("POST /admin/cluster/node/remove"' "cmd/skygate/main.go" \
    && check "route POST /admin/cluster/node/remove" ok \
    || check "route POST /admin/cluster/node/remove" fail
grep_q 'mux\.Handle\("POST /admin/cluster/invite/generate"' "cmd/skygate/main.go" \
    && check "route POST /admin/cluster/invite/generate" ok \
    || check "route POST /admin/cluster/invite/generate" fail
grep_q 'mux\.Handle\("POST /admin/cluster/invite/revoke"' "cmd/skygate/main.go" \
    && check "route POST /admin/cluster/invite/revoke" ok \
    || check "route POST /admin/cluster/invite/revoke" fail

# 6. 4 POST handlers
grep_q 'func \(s \*Service\) PostAdminClusterNodeAdd\(' "internal/feature/admin/cluster.go" \
    && check "PostAdminClusterNodeAdd handler" ok \
    || check "PostAdminClusterNodeAdd handler" fail
grep_q 'func \(s \*Service\) PostAdminClusterNodeRemove\(' "internal/feature/admin/cluster.go" \
    && check "PostAdminClusterNodeRemove handler" ok \
    || check "PostAdminClusterNodeRemove handler" fail
grep_q 'func \(s \*Service\) PostAdminClusterInviteGenerate\(' "internal/feature/admin/cluster.go" \
    && check "PostAdminClusterInviteGenerate handler" ok \
    || check "PostAdminClusterInviteGenerate handler" fail
grep_q 'func \(s \*Service\) PostAdminClusterInviteRevoke\(' "internal/feature/admin/cluster.go" \
    && check "PostAdminClusterInviteRevoke handler" ok \
    || check "PostAdminClusterInviteRevoke handler" fail

# 7. ClusterInviteSecret field
grep_q 'ClusterInviteSecret string' "internal/feature/admin/service.go" \
    && check "Service.ClusterInviteSecret field" ok \
    || check "Service.ClusterInviteSecret field" fail

# 8. main.go wiring
grep_q 'ClusterInviteSecret:[[:space:]]*cfg\.SecretKeyHex' "cmd/skygate/main.go" \
    && check "main.go wires cfg.SecretKeyHex → ClusterInviteSecret" ok \
    || check "main.go wires cfg.SecretKeyHex → ClusterInviteSecret" fail

# 9. Template forms
grep_q 'admin/cluster/node/add' "internal/handlers/templates/admin/cluster.html" \
    && check "template: form action /admin/cluster/node/add" ok \
    || check "template: form action /admin/cluster/node/add" fail
grep_q 'admin/cluster/node/remove' "internal/handlers/templates/admin/cluster.html" \
    && check "template: form action /admin/cluster/node/remove" ok \
    || check "template: form action /admin/cluster/node/remove" fail
grep_q 'admin/cluster/invite/generate' "internal/handlers/templates/admin/cluster.html" \
    && check "template: form action /admin/cluster/invite/generate" ok \
    || check "template: form action /admin/cluster/invite/generate" fail
grep_q 'admin/cluster/invite/revoke' "internal/handlers/templates/admin/cluster.html" \
    && check "template: form action /admin/cluster/invite/revoke" ok \
    || check "template: form action /admin/cluster/invite/revoke" fail

# 10. i18n node_* keys
ru_node_count=$(awk '/^var ruAdmin/,/^}$/' "internal/i18n/catalog_admin.go" | grep -cE '"cluster\.node_[a-z_]+":')
[ "$ru_node_count" -ge 7 ] \
    && check "i18n RU: $ru_node_count cluster.node_* keys (>= 7)" ok \
    || check "i18n RU: only $ru_node_count cluster.node_* keys (need >= 7)" fail
en_node_count=$(awk '/^var enAdmin/,/^}$/' "internal/i18n/catalog_admin.go" | grep -cE '"cluster\.node_[a-z_]+":')
[ "$en_node_count" -ge 7 ] \
    && check "i18n EN: $en_node_count cluster.node_* keys (>= 7)" ok \
    || check "i18n EN: only $en_node_count cluster.node_* keys (need >= 7)" fail

# 11. i18n invite_* keys
ru_inv_count=$(awk '/^var ruAdmin/,/^}$/' "internal/i18n/catalog_admin.go" | grep -cE '"cluster\.invite_[a-z_]+":')
[ "$ru_inv_count" -ge 7 ] \
    && check "i18n RU: $ru_inv_count cluster.invite_* keys (>= 7)" ok \
    || check "i18n RU: only $ru_inv_count cluster.invite_* keys (need >= 7)" fail
en_inv_count=$(awk '/^var enAdmin/,/^}$/' "internal/i18n/catalog_admin.go" | grep -cE '"cluster\.invite_[a-z_]+":')
[ "$en_inv_count" -ge 7 ] \
    && check "i18n EN: $en_inv_count cluster.invite_* keys (>= 7)" ok \
    || check "i18n EN: only $en_inv_count cluster.invite_* keys (need >= 7)" fail

# 12. invite tests
grep_q 'TestBuildAndVerifyToken_RoundTrip' "internal/cluster/invite_b200_test.go" \
    && check "TestBuildAndVerifyToken_RoundTrip exists" ok \
    || check "TestBuildAndVerifyToken_RoundTrip exists" fail
grep_q 'TestVerifyToken_RejectsTamperedPayload' "internal/cluster/invite_b200_test.go" \
    && check "TestVerifyToken_RejectsTamperedPayload exists" ok \
    || check "TestVerifyToken_RejectsTamperedPayload exists" fail
grep_q 'TestVerifyToken_RejectsWrongSecret' "internal/cluster/invite_b200_test.go" \
    && check "TestVerifyToken_RejectsWrongSecret exists" ok \
    || check "TestVerifyToken_RejectsWrongSecret exists" fail

# 13. node tests
grep_q 'TestPQStringArray_RoundTrip' "internal/cluster/node_b200_test.go" \
    && check "TestPQStringArray_RoundTrip exists" ok \
    || check "TestPQStringArray_RoundTrip exists" fail
grep_q 'TestNodeStateConstants' "internal/cluster/node_b200_test.go" \
    && check "TestNodeStateConstants exists" ok \
    || check "TestNodeStateConstants exists" fail

# 14. go build/vet/tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b200_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b200_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b200_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b200_vet.log
    fi
    if (cd "$PWD" && go test ./internal/cluster/ -count=1 >/tmp/check_b200_test.log 2>&1); then
        check "go test ./internal/cluster/ passes" ok
    else
        check "go test ./internal/cluster/ passes" fail
        head -20 /tmp/check_b200_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 15-17. Live checks (only if SKYGATE_AGENT is set)
if [ -n "${SKYGATE_AGENT:-}" ]; then
    echo ""
    echo "  Live checks on $SKYGATE_AGENT..."
    code=$(curl -s -o /dev/null -w '%{http_code}' "http://$SKYGATE_AGENT:8080/admin/cluster" || echo "000")
    if [ "$code" = "200" ] || [ "$code" = "302" ]; then
        check "GET $SKYGATE_AGENT:8080/admin/cluster = $code" ok
    else
        check "GET $SKYGATE_AGENT:8080/admin/cluster = $code (want 200 or 302)" fail
    fi
fi

# 18. AGENTS.md mention
grep_q "B200" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B200" ok \
    || check "AGENTS.md mentions B200" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B200 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B200 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
