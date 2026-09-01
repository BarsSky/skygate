#!/usr/bin/env bash
# B205 (v1.5.0+) — `skygate cluster ...` CLI subcommands.
#
# Phase 4 of docs/internal/cluster-management.md. The
# admin UI (/admin/cluster) and the HTTP API
# (/api/cluster/join + /api/cluster/heartbeat from B201)
# are the user-facing surfaces; these CLI subcommands are
# the operator-on-the-box equivalent. The full
# subcommand list:
#
#   skygate cluster invite          # print sgn1 token to stdout
#   skygate cluster join <token>    # register this node in the cluster
#   skygate cluster nodes           # list cluster_node rows
#   skygate cluster dbs             # list cluster_database rows
#   skygate cluster audit           # show recent cluster_audit rows
#   skygate cluster failover --target=<node>  # admin-gated promote
#   skygate cluster heartbeat-daemon            # long-running: post every 30s
#
# The contracts:
#
#   1. cmd/skygate/cluster.go exists (~600 lines)
#   2. runClusterSubcommand dispatcher (verb switch)
#   3. runClusterInvite generates a token via cluster.IssueInvite
#   4. runClusterJoin POSTs /api/cluster/join + writes state file
#   5. runClusterNodes reads cluster_node + tab/JSON output
#   6. runClusterDbs reads cluster_database + tab/JSON output
#   7. runClusterAudit reads cluster_audit + tab/JSON output
#   8. runClusterFailover: target ready check + roles update + audit row
#   9. runClusterHeartbeatDaemon: long-running, 30s tick, SIGINT clean
#  10. clusterRolesToSlice handles PG TEXT[] literals (incl. quoted segments)
#  11. cmd/skygate/main.go: `cluster` case dispatches to runClusterSubcommand
#  12. help text mentions `cluster <verb>` line
#  13. Unit tests: 6+ covering helpers + state file + dispatcher
#  14. go build + vet + cmd/skygate unit tests pass
#  15. AGENTS.md mentions B205

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
        printf "  \31m✗\033[0m %s\n" "$name"
        FAIL=$((FAIL+1))
        fails+=("$name")
    fi
}

file_exists() { [ -f "$1" ]; }
grep_q() { grep -qE "$1" "$2" 2>/dev/null; return $?; }

# 1. cluster.go
file_exists "cmd/skygate/cluster.go" \
    && check "cmd/skygate/cluster.go exists" ok \
    || check "cmd/skygate/cluster.go exists" fail

# 2. Dispatcher
grep_q '^func runClusterSubcommand' "cmd/skygate/cluster.go" \
    && check "runClusterSubcommand dispatcher defined" ok \
    || check "runClusterSubcommand dispatcher defined" fail
grep -A20 '^func runClusterSubcommand' "cmd/skygate/cluster.go" | grep -q "case \"invite\"" \
    && check "dispatcher handles 'invite' verb" ok \
    || check "dispatcher handles 'invite' verb" fail
grep -A20 '^func runClusterSubcommand' "cmd/skygate/cluster.go" | grep -q "case \"join\"" \
    && check "dispatcher handles 'join' verb" ok \
    || check "dispatcher handles 'join' verb" fail
grep -A20 '^func runClusterSubcommand' "cmd/skygate/cluster.go" | grep -q "case \"nodes\"" \
    && check "dispatcher handles 'nodes' verb" ok \
    || check "dispatcher handles 'nodes' verb" fail
grep -A20 '^func runClusterSubcommand' "cmd/skygate/cluster.go" | grep -q "case \"dbs\"" \
    && check "dispatcher handles 'dbs' verb" ok \
    || check "dispatcher handles 'dbs' verb" fail
grep -A20 '^func runClusterSubcommand' "cmd/skygate/cluster.go" | grep -q "case \"audit\"" \
    && check "dispatcher handles 'audit' verb" ok \
    || check "dispatcher handles 'audit' verb" fail
grep -A20 '^func runClusterSubcommand' "cmd/skygate/cluster.go" | grep -q "case \"failover\"" \
    && check "dispatcher handles 'failover' verb" ok \
    || check "dispatcher handles 'failover' verb" fail
grep -A20 '^func runClusterSubcommand' "cmd/skygate/cluster.go" | grep -q "case \"heartbeat-daemon\"" \
    && check "dispatcher handles 'heartbeat-daemon' verb" ok \
    || check "dispatcher handles 'heartbeat-daemon' verb" fail

# 3. runClusterInvite
grep_q '^func runClusterInvite' "cmd/skygate/cluster.go" \
    && check "runClusterInvite defined" ok \
    || check "runClusterInvite defined" fail
grep -A30 '^func runClusterInvite' "cmd/skygate/cluster.go" | grep -q "cluster\.IssueInvite" \
    && check "runClusterInvite calls cluster.IssueInvite" ok \
    || check "runClusterInvite calls cluster.IssueInvite" fail

# 4. runClusterJoin
grep_q '^func runClusterJoin' "cmd/skygate/cluster.go" \
    && check "runClusterJoin defined" ok \
    || check "runClusterJoin defined" fail
grep -A50 '^func runClusterJoin' "cmd/skygate/cluster.go" | grep -q "/api/cluster/join" \
    && check "runClusterJoin POSTs to /api/cluster/join" ok \
    || check "runClusterJoin POSTs to /api/cluster/join" fail
grep -A100 '^func runClusterJoin' "cmd/skygate/cluster.go" | grep -q "writeClusterState" \
    && check "runClusterJoin writes state file" ok \
    || check "runClusterJoin writes state file" fail

# 5. runClusterNodes
grep_q '^func runClusterNodes' "cmd/skygate/cluster.go" \
    && check "runClusterNodes defined" ok \
    || check "runClusterNodes defined" fail
grep -A30 '^func runClusterNodes' "cmd/skygate/cluster.go" | grep -q "cluster_node" \
    && check "runClusterNodes reads cluster_node table" ok \
    || check "runClusterNodes reads cluster_node table" fail

# 6. runClusterDbs
grep_q '^func runClusterDbs' "cmd/skygate/cluster.go" \
    && check "runClusterDbs defined" ok \
    || check "runClusterDbs defined" fail
grep -A30 '^func runClusterDbs' "cmd/skygate/cluster.go" | grep -q "cluster_database" \
    && check "runClusterDbs reads cluster_database table" ok \
    || check "runClusterDbs reads cluster_database table" fail

# 7. runClusterAudit
grep_q '^func runClusterAudit' "cmd/skygate/cluster.go" \
    && check "runClusterAudit defined" ok \
    || check "runClusterAudit defined" fail
grep -A30 '^func runClusterAudit' "cmd/skygate/cluster.go" | grep -q "cluster_audit" \
    && check "runClusterAudit reads cluster_audit table" ok \
    || check "runClusterAudit reads cluster_audit table" fail

# 8. runClusterFailover
grep_q '^func runClusterFailover' "cmd/skygate/cluster.go" \
    && check "runClusterFailover defined" ok \
    || check "runClusterFailover defined" fail
grep -A100 '^func runClusterFailover' "cmd/skygate/cluster.go" | grep -q "'node_failover'" \
    && check "runClusterFailover writes 'node_failover' audit row" ok \
    || check "runClusterFailover writes 'node_failover' audit row" fail
grep -A150 '^func runClusterFailover' "cmd/skygate/cluster.go" | grep -q "tx\.Commit" \
    && check "runClusterFailover uses a transaction" ok \
    || check "runClusterFailover uses a transaction" fail

# 9. runClusterHeartbeatDaemon
grep_q '^func runClusterHeartbeatDaemon' "cmd/skygate/cluster.go" \
    && check "runClusterHeartbeatDaemon defined" ok \
    || check "runClusterHeartbeatDaemon defined" fail
grep -A40 '^func runClusterHeartbeatDaemon' "cmd/skygate/cluster.go" | grep -q "SIGINT" \
    && check "runClusterHeartbeatDaemon handles SIGINT" ok \
    || check "runClusterHeartbeatDaemon handles SIGINT" fail
grep -A40 '^func runClusterHeartbeatDaemon' "cmd/skygate/cluster.go" | grep -q "postHeartbeat" \
    && check "runClusterHeartbeatDaemon calls postHeartbeat" ok \
    || check "runClusterHeartbeatDaemon calls postHeartbeat" fail

# 10. clusterRolesToSlice
grep_q '^func clusterRolesToSlice' "cmd/skygate/cluster.go" \
    && check "clusterRolesToSlice defined" ok \
    || check "clusterRolesToSlice defined" fail
grep -A30 '^func clusterRolesToSlice' "cmd/skygate/cluster.go" | grep -q 'inQuote' \
    && check "clusterRolesToSlice handles quoted segments" ok \
    || check "clusterRolesToSlice handles quoted segments" fail

# 11. main.go dispatches
grep_q 'case "cluster":' "cmd/skygate/main.go" \
    && check "main.go has 'cluster' case" ok \
    || check "main.go has 'cluster' case" fail
grep -A15 'case "cluster":' "cmd/skygate/main.go" | grep -q "runClusterSubcommand" \
    && check "main.go cluster case calls runClusterSubcommand" ok \
    || check "main.go cluster case calls runClusterSubcommand" fail

# 12. help text
grep -A20 'case "help"' "cmd/skygate/main.go" | grep -q "cluster <verb>" \
    && check "help text mentions 'cluster <verb>'" ok \
    || check "help text mentions 'cluster <verb>'" fail

# 13. Unit tests
file_exists "cmd/skygate/cluster_b205_test.go" \
    && check "cluster_b205_test.go exists" ok \
    || check "cluster_b205_test.go exists" fail
ut=$(grep -c '^func Test' "cmd/skygate/cluster_b205_test.go")
[ "$ut" -ge 6 ] \
    && check "cluster unit tests: $ut (>= 6)" ok \
    || check "cluster unit tests: $ut (need >= 6)" fail

# 14. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b205_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b205_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b205_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b205_vet.log
    fi
    if (cd "$PWD" && go test -count=1 -run 'TestClusterRolesToSlice|TestSqlNullString|TestRunClusterSubcommand|TestClusterState|TestReadClusterState' ./cmd/skygate/ >/tmp/check_b205_test.log 2>&1); then
        check "B205 unit tests pass" ok
    else
        check "B205 unit tests pass" fail
        head -20 /tmp/check_b205_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 15. AGENTS.md mention
grep_q "B205" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B205" ok \
    || check "AGENTS.md mentions B205" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B205 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B205 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
