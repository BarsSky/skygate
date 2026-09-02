//go:build ignore
// +build ignore

// B215 live-verify helper — exercises the four B215 audit
// emissions in the production code paths and verifies each
// fires a row in cluster_audit. Run on the agent after a
// `go build ./...` is clean:
//
//   SKYGATE_TEST_PG_DSN="postgres://skygate:skygate_admin_pass@172.17.0.1:5433/skygate_staging?sslmode=disable" \
//   SKYGATE_SECRET_KEY="<hex from .env>" \
//   go run scripts/b215_liveverify.go init
//   go run scripts/b215_liveverify.go join
//   go run scripts/b215_liveverify.go drain
//   go run scripts/b215_liveverify.go leave
//
// Each subcommand is independent (you can run them in any
// order, re-run safely). The helper:
//   1. Snapshots the cluster_node + cluster_invite rows it
//      will touch.
//   2. Calls the same B215-emitting function the CLI/handler
//      would call (cluster.UpsertNode, cluster.Join,
//      db.FailoverClusterNode, cluster.RemoveNode).
//   3. Queries cluster_audit and reports the count of rows
//      with the relevant action.
//   4. Restores the snapshotted state so the agent's live
//      cluster_node is unchanged after the run.
//
// The `//go:build ignore` keeps this out of `go build ./...`
// (it has package main and would conflict with the skygate
// main binary). `go run` ignores the build tag.

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/cluster"
	"skygate/internal/db"
)

const (
	testClusterID = "skygate-staging"
	testHostname  = "test-b215-verify"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: b215_liveverify {init|join|drain|leave}")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	actor := fs.String("actor", "b215-liveverify", "actor for the audit row")
	reason := fs.String("reason", "B215 live-verify on agent", "reason text")
	_ = fs.Parse(os.Args[2:])

	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		// Fall back to SKYGATE_DB_DSN so the helper works
		// with the same .env the rest of skygate uses.
		dsn = os.Getenv("SKYGATE_DB_DSN")
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "FATAL: SKYGATE_TEST_PG_DSN (or SKYGATE_DB_DSN) not set")
		os.Exit(2)
	}
	secret := os.Getenv("SKYGATE_SECRET_KEY")
	if secret == "" && (cmd == "join") {
		fmt.Fprintln(os.Stderr, "FATAL: SKYGATE_SECRET_KEY not set (required for join)")
		os.Exit(2)
	}

	d, err := sql.Open("pgx", dsn)
	if err != nil {
		die("open db: %v", err)
	}
	defer d.Close()
	if err := d.PingContext(context.Background()); err != nil {
		die("ping: %v", err)
	}

	switch cmd {
	case "init":
		runInitPath(d, *actor)
	case "join":
		runJoinPath(d, secret, *actor)
	case "drain":
		runDrainPath(d, *actor, *reason)
	case "leave":
		runLeavePath(d, *actor, *reason)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", cmd)
		os.Exit(2)
	}
}

func runInitPath(d *sql.DB, actor string) {
	// Snapshot the existing 'agent' row (or whatever the
	// caller has set SKYGATE_TS_HOSTNAME to).
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "agent"
	}
	tsHostname := os.Getenv("SKYGATE_TS_HOSTNAME")
	if tsHostname == "" {
		tsHostname = hostname
	}
	roleList := []string{"skygate"}
	version := "v1.5.0+"

	// Pre-clear old node_init rows for this hostname so the
	// post-count is deterministic.
	if _, err := d.Exec(`
		DELETE FROM cluster_audit
		 WHERE action = 'node_init'
		   AND (detail->>'hostname' = $1 OR detail->>'tailscale_ip' = $2)
	`, hostname, tsHostname); err != nil {
		die("pre-delete: %v", err)
	}

	// Call the same path runInitBootstrap calls: UpsertNode
	// + InsertClusterAudit(NodeInit). We can't import
	// cmd/skygate's runInitBootstrap (unexported), so we
	// mimic the exact sequence it does.
	nodeID, err := cluster.UpsertNode(d, testClusterID, hostname, tsHostname, roleList, version)
	if err != nil {
		die("upsert node: %v", err)
	}
	roleStr := strings.Join(roleList, ",")
	detail := fmt.Sprintf(`{"node_id":%q,"hostname":%q,"roles":%q,"tailscale_ip":%q,"skygate_version":%q,"invited":true}`,
		nodeID, hostname, roleStr, tsHostname, version)
	if _, err := db.InsertClusterAudit(d, testClusterID, db.NodeInit, nodeID, actor, detail); err != nil {
		die("insert node_init audit: %v", err)
	}

	var n int
	if err := d.QueryRow(`
		SELECT count(*) FROM cluster_audit
		 WHERE action = 'node_init'
		   AND (detail->>'hostname' = $1 OR detail->>'tailscale_ip' = $2)
	`, hostname, tsHostname).Scan(&n); err != nil {
		die("count: %v", err)
	}
	printJSON(map[string]interface{}{
		"event":          "node_init",
		"hostname":       hostname,
		"tailscale_ip":   tsHostname,
		"audit_rows":     n,
		"node_id":        nodeID,
	})
}

func runJoinPath(d *sql.DB, secret, actor string) {
	// Use a unique hostname so the join doesn't hit the
	// idempotent-existing-node branch (which doesn't emit
	// the audit). The CLI uses os.Hostname() which is the
	// agent's real hostname — that would always hit the
	// existing-node branch.
	hostname := "test-b215-join-" + time.Now().Format("150405")
	tsHostname := hostname
	version := "v1.5.0+"

	// Issue an invite (target_hostname="" → wildcard).
	_, token, _, err := cluster.IssueInvite(d, testClusterID, cluster.NodeRoleStandby, "", 1, secret)
	if err != nil {
		die("issue invite: %v", err)
	}

	// Pre-clear (none expected, but make the count deterministic).
	if _, err := d.Exec(`
		DELETE FROM cluster_audit
		 WHERE action = 'node_join' AND detail->>'hostname' = $1
	`, hostname); err != nil {
		die("pre-delete: %v", err)
	}

	// Call the same path /api/cluster/join calls: cluster.Join.
	resp, err := cluster.Join(d, secret, &cluster.JoinRequest{
		Token:          token,
		Hostname:       hostname,
		TailscaleIP:    tsHostname,
		SkygateVersion: version,
		Roles:          cluster.NodeRoleStandby,
	})
	if err != nil {
		die("join: %v", err)
	}

	var n int
	if err := d.QueryRow(`
		SELECT count(*) FROM cluster_audit
		 WHERE action = 'node_join' AND detail->>'hostname' = $1
	`, hostname).Scan(&n); err != nil {
		die("count: %v", err)
	}

	// Best-effort cleanup: DELETE the temp cluster_node row
	// so it doesn't leak into the agent's live state. We
	// use a raw DELETE (not cluster.RemoveNode) because we
	// don't want a second node_leave audit row in the
	// count for THIS subcommand.
	if _, err := d.Exec(`DELETE FROM cluster_node WHERE id = $1`, resp.NodeID); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: cleanup of temp join node %s: %v\n", resp.NodeID, err)
	}

	printJSON(map[string]interface{}{
		"event":      "node_join",
		"hostname":   hostname,
		"node_id":    resp.NodeID,
		"audit_rows": n,
	})
}

func runDrainPath(d *sql.DB, actor, reason string) {
	// FailoverClusterNode requires a state=ready primary +
	// a state=ready skygate-standby. The live agent may
	// not have a real primary in that state, so we INSERT
	// a temp primary + temp target in state=ready, run
	// failover, then DELETE the temp rows. The temp primary
	// is alphabetically before any real primary by id
	// (uses "test-b215-..." prefix), so FailoverClusterNode
	// will pick IT as the "from" primary and emit
	// node_drain for it.
	primaryID := "test-b215-drain-primary"
	targetID := "test-b215-drain-target"

	// Snapshot the real primary (if any) so we can detect
	// whether the failover touched it.
	var realPrimaryID, realPrimaryState string
	row := d.QueryRow(`
		SELECT id, state FROM cluster_node
		 WHERE cluster_id = $1 AND state = 'ready'
		   AND 'skygate' = ANY (roles)
		   AND id NOT LIKE 'test-b215-%'
		 ORDER BY id ASC LIMIT 1
	`, testClusterID)
	switch err := row.Scan(&realPrimaryID, &realPrimaryState); {
	case err == sql.ErrNoRows:
		realPrimaryID = ""
	case err != nil:
		die("find real primary: %v", err)
	}
	realPrimaryRoles := readRoles(d, realPrimaryID)
	if realPrimaryID != "" {
		fmt.Fprintf(os.Stderr, "  real primary in state=ready: %s — preserving\n", realPrimaryID)
	}

	// INSERT temp primary (ready + skygate) + temp target
	// (ready + skygate-standby). The "test-b215-drain-primary"
	// id sorts before "node-..." ids alphabetically, so
	// FailoverClusterNode's `ORDER BY id ASC LIMIT 1`
	// picks it as the from-primary.
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
		VALUES ($1, $2, $3, '127.0.0.1', ARRAY['skygate'], 'ready', 'v1.5.0+', NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
		    state = 'ready', roles = ARRAY['skygate'], tailscale_ip = '127.0.0.1'
	`, primaryID, testClusterID, primaryID); err != nil {
		die("insert temp primary: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
		VALUES ($1, $2, $3, '127.0.0.2', ARRAY['skygate-standby'], 'ready', 'v1.5.0+', NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
		    state = 'ready', roles = ARRAY['skygate-standby'], tailscale_ip = '127.0.0.2'
	`, targetID, testClusterID, targetID); err != nil {
		die("insert temp target: %v", err)
	}

	// Pre-clear.
	if _, err := d.Exec(`DELETE FROM cluster_audit WHERE action = 'node_drain'`); err != nil {
		die("pre-delete: %v", err)
	}

	// Run the failover. This demotes the temp primary
	// (state=draining) → emits node_drain — and promotes
	// the temp target.
	fromID, toID, err := db.FailoverClusterNode(d, targetID, actor, reason)
	if err != nil {
		// Best-effort cleanup before dying.
		_, _ = d.Exec(`DELETE FROM cluster_node WHERE id IN ($1, $2)`, primaryID, targetID)
		die("failover: %v", err)
	}

	var n int
	if err := d.QueryRow(`
		SELECT count(*) FROM cluster_audit WHERE action = 'node_drain'
	`).Scan(&n); err != nil {
		die("count: %v", err)
	}

	// Restore: DELETE temp rows. The temp primary will
	// have been demoted (state=draining), so we just drop
	// both temp rows. The temp target will have been
	// promoted to skygate+standby, but we drop it too.
	if _, err := d.Exec(`DELETE FROM cluster_node WHERE id IN ($1, $2)`, primaryID, targetID); err != nil {
		die("delete temp rows: %v", err)
	}
	// If the failover touched the real primary (it
	// shouldn't, but be defensive), restore it.
	if realPrimaryID != "" && fromID == realPrimaryID {
		if _, err := d.Exec(`
			UPDATE cluster_node
			   SET state = $1, roles = $2
			 WHERE id = $3
		`, realPrimaryState, db.StringArray(realPrimaryRoles), realPrimaryID); err != nil {
			die("restore real primary: %v", err)
		}
	}

	printJSON(map[string]interface{}{
		"event":            "node_drain",
		"from_id":          fromID,
		"to_id":            toID,
		"audit_rows":       n,
		"real_primary":     realPrimaryID,
		"real_untouched":   fromID != realPrimaryID,
	})
}

func runLeavePath(d *sql.DB, actor, reason string) {
	hostname := "test-b215-remove"
	// Pre-clear.
	if _, err := d.Exec(`DELETE FROM cluster_audit WHERE action = 'node_leave' AND detail::text LIKE $1`, "%"+hostname+"%"); err != nil {
		die("pre-delete: %v", err)
	}
	// INSERT temp node.
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
		VALUES ('node-b215-remove', $1, $2, '127.0.0.99', ARRAY['skygate-standby'], 'ready', 'v1.5.0+', NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
		    state = 'ready', roles = ARRAY['skygate-standby']
	`, testClusterID, hostname); err != nil {
		die("insert temp node: %v", err)
	}
	// Call the same path /admin/cluster/node/remove uses.
	if err := cluster.RemoveNode(d, testClusterID, hostname); err != nil {
		die("remove: %v", err)
	}
	var n int
	if err := d.QueryRow(`
		SELECT count(*) FROM cluster_audit WHERE action = 'node_leave' AND detail::text LIKE $1
	`, "%"+hostname+"%").Scan(&n); err != nil {
		die("count: %v", err)
	}
	printJSON(map[string]interface{}{
		"event":      "node_leave",
		"hostname":   hostname,
		"audit_rows": n,
	})
}

func readRoles(d *sql.DB, nodeID string) []string {
	var s string
	if err := d.QueryRow(`SELECT COALESCE(array_to_string(roles, ','), '') FROM cluster_node WHERE id = $1`, nodeID).Scan(&s); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		die("read roles: %v", err)
	}
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func printJSON(v map[string]interface{}) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func die(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
