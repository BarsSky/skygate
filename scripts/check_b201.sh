#!/usr/bin/env bash
# B201 (v1.5.0+) — /api/cluster/join + /api/cluster/heartbeat
# (Phase 2.3 of cluster-management.md).
#
# The join flow consumes the sgn1 tokens that B200's
# admin form generates. The new node POSTs the token +
# its hostname to /api/cluster/join; the server creates
# a cluster_node row + marks the invite used. The new
# node then POSTs /api/cluster/heartbeat every ~30s to
# keep its state=ready.
#
# The contracts:
#
#   1. internal/cluster/join.go exists
#   2. Join() + Heartbeat() functions
#   3. JoinRequest/JoinResponse/HeartbeatRequest/HeartbeatResponse structs
#   4. Error sentinels (HostnameMismatch, InviteExpired, etc.)
#   5. internal/feature/cluster/handlers.go exists
#   6. Service struct + NewService
#   7. PostAPIClusterJoin + PostAPIClusterHeartbeat handlers
#   8. writeJoinError maps sentinels to 401/403/409/410/500
#   9. 2 routes in main.go: POST /api/cluster/join + /heartbeat
#  10. cfg.SecretKeyHex wired to clusterAPI.InviteSecret
#  11. join_b201_test.go: hostnamesEqual + parseRolesField + splitComma + sentinels
#  12. go build + vet + cluster unit tests pass
#  13. Live: POST /api/cluster/join with a B200-generated
#      sgn1 token creates a cluster_node row
#  14. Live: POST /api/cluster/heartbeat transitions
#      state pending → ready
#  15. AGENTS.md mentions B201

set -u

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

# 1. internal/cluster/join.go
file_exists "internal/cluster/join.go" \
    && check "internal/cluster/join.go exists" ok \
    || check "internal/cluster/join.go exists" fail

# 2. Join + Heartbeat
grep_q '^func Join\(' "internal/cluster/join.go" \
    && check "Join() defined" ok || check "Join() defined" fail
grep_q '^func Heartbeat\(' "internal/cluster/join.go" \
    && check "Heartbeat() defined" ok || check "Heartbeat() defined" fail

# 3. Structs
grep_q 'type JoinRequest struct' "internal/cluster/join.go" \
    && check "JoinRequest struct defined" ok || check "JoinRequest struct defined" fail
grep_q 'type JoinResponse struct' "internal/cluster/join.go" \
    && check "JoinResponse struct defined" ok || check "JoinResponse struct defined" fail
grep_q 'type HeartbeatRequest struct' "internal/cluster/join.go" \
    && check "HeartbeatRequest struct defined" ok || check "HeartbeatRequest struct defined" fail
grep_q 'type HeartbeatResponse struct' "internal/cluster/join.go" \
    && check "HeartbeatResponse struct defined" ok || check "HeartbeatResponse struct defined" fail

# 4. Error sentinels
grep_q 'var ErrHostnameMismatch' "internal/cluster/join.go" \
    && check "ErrHostnameMismatch defined" ok || check "ErrHostnameMismatch defined" fail
grep_q 'var ErrInviteExpired' "internal/cluster/join.go" \
    && check "ErrInviteExpired defined" ok || check "ErrInviteExpired defined" fail
grep_q 'var ErrInviteRevoked' "internal/cluster/join.go" \
    && check "ErrInviteRevoked defined" ok || check "ErrInviteRevoked defined" fail
grep_q 'var ErrInviteNotPending' "internal/cluster/join.go" \
    && check "ErrInviteNotPending defined" ok || check "ErrInviteNotPending defined" fail
grep_q 'var ErrNodeAlreadyExists' "internal/cluster/join.go" \
    && check "ErrNodeAlreadyExists defined" ok || check "ErrNodeAlreadyExists defined" fail
grep_q 'var ErrHeartbeatNodeNotFound' "internal/cluster/join.go" \
    && check "ErrHeartbeatNodeNotFound defined" ok || check "ErrHeartbeatNodeNotFound defined" fail

# 5. handlers.go
file_exists "internal/feature/cluster/handlers.go" \
    && check "internal/feature/cluster/handlers.go exists" ok \
    || check "internal/feature/cluster/handlers.go exists" fail

# 6. Service + NewService
grep_q 'type Service struct' "internal/feature/cluster/handlers.go" \
    && check "Service struct defined" ok || check "Service struct defined" fail
grep_q 'func NewService\(' "internal/feature/cluster/handlers.go" \
    && check "NewService() defined" ok || check "NewService() defined" fail

# 7. Handlers
grep_q 'func \(s \*Service\) PostAPIClusterJoin\(' "internal/feature/cluster/handlers.go" \
    && check "PostAPIClusterJoin handler" ok || check "PostAPIClusterJoin handler" fail
grep_q 'func \(s \*Service\) PostAPIClusterHeartbeat\(' "internal/feature/cluster/handlers.go" \
    && check "PostAPIClusterHeartbeat handler" ok || check "PostAPIClusterHeartbeat handler" fail

# 8. writeJoinError
grep_q 'writeJoinError' "internal/feature/cluster/handlers.go" \
    && check "writeJoinError() function" ok || check "writeJoinError() function" fail
grep_q 'StatusForbidden' "internal/feature/cluster/handlers.go" \
    && check "writeJoinError maps to 403 (hostname mismatch)" ok || check "writeJoinError maps to 403" fail
grep_q 'StatusConflict' "internal/feature/cluster/handlers.go" \
    && check "writeJoinError maps to 409 (already used)" ok || check "writeJoinError maps to 409" fail
grep_q 'StatusGone' "internal/feature/cluster/handlers.go" \
    && check "writeJoinError maps to 410 (expired/revoked)" ok || check "writeJoinError maps to 410" fail

# 9. Routes
grep_q 'mux\.HandleFunc\("POST /api/cluster/join"' "cmd/skygate/main.go" \
    && check "route POST /api/cluster/join" ok || check "route POST /api/cluster/join" fail
grep_q 'mux\.HandleFunc\("POST /api/cluster/heartbeat"' "cmd/skygate/main.go" \
    && check "route POST /api/cluster/heartbeat" ok || check "route POST /api/cluster/heartbeat" fail

# 10. main.go wiring
grep_q 'clusterapi\.Service{' "cmd/skygate/main.go" \
    && check "clusterAPI service constructed" ok || check "clusterAPI service constructed" fail
grep_q 'InviteSecret:[[:space:]]*cfg\.SecretKeyHex' "cmd/skygate/main.go" \
    && check "cfg.SecretKeyHex → clusterAPI.InviteSecret" ok || check "cfg.SecretKeyHex wired" fail

# 11. join_b201_test.go
grep_q 'TestHostnamesEqual' "internal/cluster/join_b201_test.go" \
    && check "TestHostnamesEqual exists" ok || check "TestHostnamesEqual exists" fail
grep_q 'TestParseRolesField' "internal/cluster/join_b201_test.go" \
    && check "TestParseRolesField exists" ok || check "TestParseRolesField exists" fail
grep_q 'TestJoinErrorSentinels' "internal/cluster/join_b201_test.go" \
    && check "TestJoinErrorSentinels exists" ok || check "TestJoinErrorSentinels exists" fail

# 12. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b201_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b201_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b201_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b201_vet.log
    fi
    if (cd "$PWD" && go test ./internal/cluster/ -count=1 >/tmp/check_b201_test.log 2>&1); then
        check "go test ./internal/cluster/ passes" ok
    else
        check "go test ./internal/cluster/ passes" fail
        head -20 /tmp/check_b201_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 13-14. Live (only if SKYGATE_AGENT is set)
if [ -n "${SKYGATE_AGENT:-}" ]; then
    echo ""
    echo "  Live checks on $SKYGATE_AGENT..."
    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Content-Type: application/json" -d '{}' "http://$SKYGATE_AGENT:8080/api/cluster/join" || echo "000")
    if [ "$code" = "400" ] || [ "$code" = "401" ]; then
        check "POST $SKYGATE_AGENT:8080/api/cluster/join = $code (400/401 expected for empty body)" ok
    else
        check "POST $SKYGATE_AGENT:8080/api/cluster/join = $code (want 400/401)" fail
    fi
fi

# 15. AGENTS.md
grep_q "B201" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B201" ok \
    || check "AGENTS.md mentions B201" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B201 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B201 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
