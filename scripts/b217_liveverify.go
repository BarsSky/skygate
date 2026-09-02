//go:build ignore
// +build ignore

// B217 live-verify helper — exercises the three
// Phase 2.2 admin endpoints (Approve, Drain,
// Drain+Remove) via real HTTP and asserts the
// cluster_audit events fire. Run on the agent after
// `go build ./...` is clean:
//
//   SKYGATE_BASE_URL=http://127.0.0.1:8080 \
//   /snap/go/current/bin/go run scripts/b217_liveverify.go
//
// The helper:
//   1. INSERTs 3 temp cluster_node rows (one in
//      each state we'll exercise: pending, ready,
//      failed) via cluster.UpsertNode.
//   2. For each row, POSTs the corresponding
//      admin endpoint with the session JWT.
//   3. Queries cluster_audit for the expected
//      action rows.
//   4. DELETEs the temp rows + drains/cleans up.
//
// `//go:build ignore` keeps this out of `go build ./…`.
// `go run` ignores the build tag.

package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/auth"
)

const (
	cookieName    = "skygate_session"
	testClusterID = "skygate-staging"
	hostnamePending  = "test-b217-pending-node"
	hostnameReady    = "test-b217-ready-node"
	hostnameFailed   = "test-b217-drain-remove-node"
)

func main() {
	fs := flag.NewFlagSet("b217-liveverify", flag.ExitOnError)
	baseURL := fs.String("base-url", os.Getenv("SKYGATE_BASE_URL"), "skygate base URL (e.g. http://127.0.0.1:8080)")
	adminUID := fs.Int64("admin-uid", 1, "numeric uid of an admin user")
	adminUsername := fs.String("admin-username", "skyadmin", "admin username for the JWT claim")
	_ = fs.Parse(os.Args[1:])

	if *baseURL == "" {
		*baseURL = "http://127.0.0.1:8080"
	}
	secret := os.Getenv("SKYGATE_JWT_SECRET")
	if secret == "" {
		secret = os.Getenv("SKYGATE_SECRET_KEY")
	}
	if secret == "" {
		die("SKYGATE_JWT_SECRET (or SKYGATE_SECRET_KEY) not set")
	}

	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		dsn = os.Getenv("SKYGATE_DB_DSN")
	}
	if dsn == "" {
		die("SKYGATE_TEST_PG_DSN (or SKYGATE_DB_DSN) not set")
	}

	// 1. Mint a session JWT.
	tok, err := auth.IssueJWT(secret, *adminUID, *adminUsername, true, 1)
	if err != nil {
		die("issue JWT: %v", err)
	}
	fmt.Fprintf(os.Stderr, "issued JWT for uid=%d username=%s\n", *adminUID, *adminUsername)

	// 2. Open the DB and set up 3 temp rows.
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		die("open db: %v", err)
	}
	defer d.Close()
	setupTempRows(d)

	// 3. Exercise the 3 endpoints.
	exerciseApprove(*baseURL, tok, d)
	exerciseDrain(*baseURL, tok, d)
	exerciseDrainRemove(*baseURL, tok, d)

	// 4. Cleanup.
	cleanupTempRows(d)
	fmt.Fprintln(os.Stderr, "\nB217 live-verify DONE")
}

func setupTempRows(d *sql.DB) {
	fmt.Fprintln(os.Stderr, "=== Setup: insert 3 temp cluster_node rows ===")
	for _, h := range []struct {
		host   string
		state  string
		roles  string
	}{
		{hostnamePending, "pending", "skygate-standby"},
		{hostnameReady, "ready", "skygate-standby"},
		{hostnameFailed, "failed", "skygate-standby"},
	} {
		// Best-effort cleanup first (in case a
		// prior run left these behind).
		_, _ = d.Exec(`DELETE FROM cluster_node WHERE hostname = $1`, h.host)
		// Pre-clear any audit rows for this hostname
		// (so the count is deterministic).
		_, _ = d.Exec(`DELETE FROM cluster_audit WHERE detail->>'hostname' = $1`, h.host)
		_, err := d.Exec(`
			INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
			VALUES ($1, $2, $3, '127.0.0.99', $4, $5, 'v1.5.0+', NOW(), NOW())
			ON CONFLICT (id) DO UPDATE SET
			    state = $5, roles = $4, hostname = $3
		`, "node-b217-"+h.host, testClusterID, h.host, "{"+h.roles+"}", h.state)
		if err != nil {
			die("insert temp %s: %v", h.host, err)
		}
		fmt.Fprintf(os.Stderr, "  inserted %s (state=%s)\n", h.host, h.state)
	}
}

func cleanupTempRows(d *sql.DB) {
	fmt.Fprintln(os.Stderr, "=== Cleanup: delete 3 temp cluster_node rows ===")
	for _, h := range []string{hostnamePending, hostnameReady, hostnameFailed} {
		if _, err := d.Exec(`DELETE FROM cluster_node WHERE hostname = $1`, h); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN: cleanup %s: %v\n", h, err)
		}
	}
}

func exerciseApprove(baseURL, tok string, d *sql.DB) {
	fmt.Fprintln(os.Stderr, "\n=== Step 1: POST /admin/cluster/node/approve (hostname="+hostnamePending+") ===")
	// Pre-clear to make the count deterministic.
	_, _ = d.Exec(`DELETE FROM cluster_audit WHERE action = 'node_approve' AND detail->>'hostname' = $1`, hostnamePending)
	statusCode, body := postAdmin(baseURL, tok, "/admin/cluster/node/approve",
		url.Values{"hostname": {hostnamePending}})
	fmt.Fprintf(os.Stderr, "  POST → %d\n", statusCode)
	if statusCode != 303 && statusCode != 302 {
		die("approve: expected 303/302, got %d (body: %s)", statusCode, truncate(body, 200))
	}
	// Verify the row is now state=ready.
	var state string
	_ = d.QueryRow(`SELECT state FROM cluster_node WHERE hostname = $1`, hostnamePending).Scan(&state)
	fmt.Fprintf(os.Stderr, "  state after approve: %s\n", state)
	if state != "ready" {
		die("approve: state = %s, want ready", state)
	}
	// Verify the audit row. node_approve's JSON DOES
	// include hostname (buildApproveDetail adds it),
	// so we can query by hostname here. The drain
	// helpers don't include hostname, so they use
	// target_node_id (see below).
	var n int
	_ = d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE action = 'node_approve' AND detail->>'hostname' = $1`, hostnamePending).Scan(&n)
	fmt.Fprintf(os.Stderr, "  node_approve audit rows: %d\n", n)
	if n != 1 {
		die("approve: expected 1 audit row, got %d", n)
	}
	fmt.Fprintln(os.Stderr, "  [PASS] approve")
}

func exerciseDrain(baseURL, tok string, d *sql.DB) {
	fmt.Fprintln(os.Stderr, "\n=== Step 2: POST /admin/cluster/node/drain (hostname="+hostnameReady+") ===")
	_, _ = d.Exec(`DELETE FROM cluster_audit WHERE action = 'node_drain' AND detail->>'hostname' = $1`, hostnameReady)
	statusCode, body := postAdmin(baseURL, tok, "/admin/cluster/node/drain",
		url.Values{"hostname": {hostnameReady}, "reason": {"B217 live-verify test"}})
	fmt.Fprintf(os.Stderr, "  POST → %d\n", statusCode)
	if statusCode != 303 && statusCode != 302 {
		die("drain: expected 303/302, got %d (body: %s)", statusCode, truncate(body, 200))
	}
	// Verify the row is now state=draining AND still exists.
	var state string
	_ = d.QueryRow(`SELECT state FROM cluster_node WHERE hostname = $1`, hostnameReady).Scan(&state)
	fmt.Fprintf(os.Stderr, "  state after drain: %s\n", state)
	if state != "draining" {
		die("drain: state = %s, want draining", state)
	}
	// Verify the audit row has reason. The buildDrainDetail
	// JSON doesn't include "hostname" (the cluster_audit
	// row already has target_node_id, which is the
	// cluster_node.id — the natural key for the row).
	// So we look up by target_node_id instead.
	var reason string
	_ = d.QueryRow(`SELECT detail->>'reason' FROM cluster_audit
	                 WHERE action = 'node_drain' AND target_node_id = $1
	                 ORDER BY id DESC LIMIT 1`, "node-b217-"+hostnameReady).Scan(&reason)
	fmt.Fprintf(os.Stderr, "  node_drain audit reason: %q\n", reason)
	if reason != "B217 live-verify test" {
		die("drain: audit reason = %q, want 'B217 live-verify test'", reason)
	}
	fmt.Fprintln(os.Stderr, "  [PASS] drain (row preserved in state=draining)")

	// Restore: reset to ready so subsequent tests can
	// reuse the hostname.
	_, _ = d.Exec(`UPDATE cluster_node SET state = 'ready' WHERE hostname = $1`, hostnameReady)
	_, _ = d.Exec(`DELETE FROM cluster_audit WHERE action = 'node_drain' AND detail->>'hostname' = $1`, hostnameReady)
}

func exerciseDrainRemove(baseURL, tok string, d *sql.DB) {
	fmt.Fprintln(os.Stderr, "\n=== Step 3: POST /admin/cluster/node/drain-remove (hostname="+hostnameFailed+") ===")
	_, _ = d.Exec(`DELETE FROM cluster_audit WHERE action IN ('node_drain', 'node_leave') AND detail->>'hostname' = $1`, hostnameFailed)
	statusCode, body := postAdmin(baseURL, tok, "/admin/cluster/node/drain-remove",
		url.Values{"hostname": {hostnameFailed}, "reason": {"B217 live-verify drain+remove"}})
	fmt.Fprintf(os.Stderr, "  POST → %d\n", statusCode)
	if statusCode != 303 && statusCode != 302 {
		die("drain-remove: expected 303/302, got %d (body: %s)", statusCode, truncate(body, 200))
	}
	// Verify the row is GONE.
	var n int
	_ = d.QueryRow(`SELECT count(*) FROM cluster_node WHERE hostname = $1`, hostnameFailed).Scan(&n)
	fmt.Fprintf(os.Stderr, "  rows after drain+remove: %d (want 0)\n", n)
	if n != 0 {
		die("drain-remove: row still exists")
	}
	// Verify BOTH audit rows fired. Use target_node_id
	// (the cluster_node.id) since the build* JSON
	// doesn't include hostname.
	var drainN, leaveN int
	_ = d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE action = 'node_drain' AND target_node_id = $1`, "node-b217-"+hostnameFailed).Scan(&drainN)
	_ = d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE action = 'node_leave' AND target_node_id = $1`, "node-b217-"+hostnameFailed).Scan(&leaveN)
	fmt.Fprintf(os.Stderr, "  node_drain audit rows: %d (want 1), node_leave audit rows: %d (want 1)\n", drainN, leaveN)
	if drainN != 1 {
		die("drain-remove: expected 1 node_drain row, got %d", drainN)
	}
	if leaveN != 1 {
		die("drain-remove: expected 1 node_leave row, got %d", leaveN)
	}
	// Verify the node_leave row's last_state field.
	var lastState string
	_ = d.QueryRow(`SELECT detail->>'last_state' FROM cluster_audit
	                 WHERE action = 'node_leave' AND target_node_id = $1
	                 ORDER BY id DESC LIMIT 1`, "node-b217-"+hostnameFailed).Scan(&lastState)
	fmt.Fprintf(os.Stderr, "  node_leave audit last_state: %q (want 'draining')\n", lastState)
	if lastState != "draining" {
		die("drain-remove: leave detail.last_state = %q, want 'draining'", lastState)
	}
	fmt.Fprintln(os.Stderr, "  [PASS] drain-remove (row deleted, 2 audit rows in 1 tx)")
}

func postAdmin(baseURL, tok, path string, form url.Values) (int, string) {
	req, _ := http.NewRequest("POST", baseURL+path,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: noRedirect}
	resp, err := httpClient.Do(req)
	if err != nil {
		die("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func noRedirect(req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

func die(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
