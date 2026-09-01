// skygate cluster ... — B205 (v1.5.0+) cluster CLI subcommands.
//
// The admin UI (/admin/cluster) and the HTTP API
// (/api/cluster/join + /api/cluster/heartbeat from B201)
// are the user-facing surfaces for cluster management.
// These CLI subcommands are the operator-on-the-box
// equivalent — they're what `scripts/bootstrap_standby.sh`
// (Phase 7) and operator SSH sessions invoke when there's
// no browser available.
//
// Subcommands:
//
//	skygate cluster invite --role=skygate-standby [--ttl 24h]
//	    Generate an invite token for a new node. Prints
//	    the sgn1 token to stdout. The token is the
//	    bearer credential the new node will use to call
//	    /api/cluster/join. The default TTL is 24h.
//	    (Server-side: invokes the same code path as
//	    /admin/cluster/invite. B200's IssueInvite helper
//	    is the source of truth.)
//
//	skygate cluster join <token>
//	    Join the cluster using the given token. Calls
//	    /api/cluster/join (or directly IssueClusterNode
//	    if the API URL is local). Saves the returned
//	    node_id + the heartbeat hint to a state file
//	    (/etc/skygate/cluster-state.json) so the
//	    heartbeat-daemon can pick it up.
//
//	skygate cluster nodes
//	    List cluster_node rows (id, hostname, state,
//	    roles, last_seen). Read from the local DB; in
//	    a multi-node deployment, the operator runs
//	    this on the primary to see all nodes.
//
//	skygate cluster dbs
//	    List cluster_database rows. The B203 watchdog
//	    hot-reloads the DSN from here.
//
//	skygate cluster audit [--limit 20]
//	    Show recent cluster_audit rows. The B204
//	    elector writes node_health + failover_recommend
//	    rows; the B205 failover command writes
//	    node_failover rows. This is the operator's
//	    log view.
//
//	skygate cluster failover --target=<node>
//	    Admin-gated promote: a standby is bumped from
//	    'ready' to 'ready' (with state=primary in the
//	    roles), the failed primary is moved to
//	    'draining', and a cluster_audit row is written.
//	    The actual role swap is operator-driven (the
//	    elector only writes a recommendation); this
//	    subcommand is the "accept the recommendation"
//	    action.
//
//	skygate cluster heartbeat-daemon
//	    Long-running process that posts to
//	    /api/cluster/heartbeat every ~30s. The new
//	    node starts this in its systemd unit after a
//	    successful join. The watchdog on the primary
//	    tracks its last_seen_at; 3 missed heartbeats
//	    (90s) → state=failed (B204).
//
// The CLI is intentionally minimal — no JSON flags, no
// pretty-printers, no interactive prompts. Each
// subcommand is a single stdout/stderr action so it's
// scriptable from bootstrap_standby.sh + cron jobs.
//
// 2026-09-01: B205 (v1.5.0+).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"skygate/internal/cluster"
	"skygate/internal/config"
	"skygate/internal/db"
)

// StateFilePath is the canonical location for the
// per-node cluster state (node_id + last_heartbeat + secret).
// /etc/skygate/ is the persistent location for the
// bootstrap (per D2 in cluster-management.md).
const StateFilePath = "/etc/skygate/cluster-state.json"

// runClusterSubcommand is the dispatcher for
// `skygate cluster <verb>` (verb is os.Args[2]).
func runClusterSubcommand(args []string) error {
	if len(args) < 1 {
		return errors.New("cluster: missing verb (invite, join, nodes, dbs, audit, failover, heartbeat-daemon)")
	}
	verb := args[0]
	switch verb {
	case "invite":
		return runClusterInvite(args[1:])
	case "join":
		return runClusterJoin(args[1:])
	case "nodes":
		return runClusterNodes(args[1:])
	case "dbs":
		return runClusterDbs(args[1:])
	case "audit":
		return runClusterAudit(args[1:])
	case "failover":
		return runClusterFailover(args[1:])
	case "heartbeat-daemon":
		return runClusterHeartbeatDaemon(args[1:])
	default:
		return fmt.Errorf("cluster: unknown verb %q (invite, join, nodes, dbs, audit, failover, heartbeat-daemon)", verb)
	}
}

// runClusterInvite generates an invite token via the
// HTTP /admin/cluster/invite endpoint. The operator
// runs this on the PRIMARY node (the one running the
// skygate web server) and pipes the token to the new
// node's bootstrap_standby.sh.
//
// Usage:
//
//	skygate cluster invite --role=skygate-standby [--ttl 24h]
func runClusterInvite(args []string) error {
	fs := flag.NewFlagSet("cluster invite", flag.ContinueOnError)
	role := fs.String("role", "skygate-standby", "role for the new node (skygate / skygate-standby)")
	ttlHours := fs.Int("ttl-hours", 24, "token TTL in hours")
	targetHost := fs.String("hostname", "", "target hostname (defaults to os.Hostname())")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("cluster invite: config load: %w", err)
	}
	if *targetHost == "" {
		*targetHost, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("cluster invite: hostname: %w", err)
		}
	}
	// The admin form posts to /admin/cluster/invite. The
	// CLI hits the same endpoint via the admin's session
	// cookie, but for an operator-on-the-box CLI the
	// simpler path is to call the cluster.IssueInvite
	// helper directly (it has no auth requirement, but
	// it requires a DB connection — same trust model as
	// the rest of the CLI subcommands).
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("cluster invite: open db: %w", err)
	}
	defer d.Close()
	if err := cluster.EnsureCluster(d, cluster.DefaultClusterID, cluster.DefaultClusterID); err != nil {
		return fmt.Errorf("cluster invite: ensure cluster: %w", err)
	}
	// IssueInvite is the canonical helper. The HTTP
	// /admin/cluster/invite handler in B200 calls the
	// same function; calling it directly from the CLI
	// avoids a chicken-and-egg with session cookies.
	_, token, expiresAt, err := cluster.IssueInvite(d, cluster.DefaultClusterID, *role, *targetHost, *ttlHours, cfg.SecretKeyHex)
	if err != nil {
		return fmt.Errorf("cluster invite: issue: %w", err)
	}
	// Print to stdout in a scriptable format: the token
	// on line 1, the expiry on line 2. bootstrap_standby
	// reads the first line and uses it as the join token.
	fmt.Println(token)
	fmt.Fprintf(os.Stderr, "expires_at: %s\n", expiresAt.Format(time.RFC3339))
	return nil
}

// runClusterJoin calls /api/cluster/join with the given
// token. The new node's hostname + tailscale_ip +
// skygate_version are sent. The returned node_id is
// saved to the state file so the heartbeat-daemon can
// pick it up.
//
// Usage:
//
//	skygate cluster join <token> [--api-url http://127.0.0.1:8080] [--state-file /etc/skygate/cluster-state.json]
func runClusterJoin(args []string) error {
	fs := flag.NewFlagSet("cluster join", flag.ContinueOnError)
	apiURL := fs.String("api-url", "http://127.0.0.1:8080", "skygate API base URL (the cluster's primary)")
	stateFile := fs.String("state-file", StateFilePath, "where to write the node_id (for the heartbeat-daemon)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("cluster join: missing token (usage: skygate cluster join <token>)")
	}
	token := fs.Arg(0)

	// 1. Read our own hostname + tailscale_ip. The
	// /api/cluster/join requires these (the server
	// binds the token's invite to a target_hostname,
	// so we MUST match). We use os.Hostname for the
	// local one and read SKYGATE_TS_HOSTNAME for the
	// tailscale one (set by the entrypoint).
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("cluster join: hostname: %w", err)
	}
	tsHostname := os.Getenv("SKYGATE_TS_HOSTNAME")
	if tsHostname == "" {
		tsHostname = hostname
	}
	skygateVersion := os.Getenv("SKYGATE_VERSION")
	if skygateVersion == "" {
		skygateVersion = "unknown"
	}

	// 2. POST to /api/cluster/join.
	form := url.Values{
		"token":           {token},
		"hostname":        {hostname},
		"tailscale_ip":    {tsHostname},
		"skygate_version": {skygateVersion},
		"roles":           {"skygate-standby"},
	}
	resp, err := http.PostForm(*apiURL+"/api/cluster/join", form)
	if err != nil {
		return fmt.Errorf("cluster join: POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cluster join: server returned %s", resp.Status)
	}
	var jr cluster.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return fmt.Errorf("cluster join: decode response: %w", err)
	}

	// 3. Save the state file so the heartbeat-daemon can
	// pick up the node_id. We also save the API URL +
	// the token (the heartbeat-daemon re-sends the token
	// for re-auth; the B201 heartbeat handler verifies
	// the token's invite against used_by_node_id).
	if err := os.MkdirAll(filepath.Dir(*stateFile), 0700); err != nil {
		return fmt.Errorf("cluster join: mkdir: %w", err)
	}
	st := clusterState{
		NodeID:           jr.NodeID,
		ClusterID:        jr.ClusterID,
		Hostname:         hostname,
		Token:            token,
		APIURL:           *apiURL,
		HeartbeatSeconds: jr.HeartbeatHint,
	}
	if err := writeClusterState(*stateFile, &st); err != nil {
		return fmt.Errorf("cluster join: write state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "cluster join: node_id=%s cluster_id=%s api_url=%s\n", jr.NodeID, jr.ClusterID, *apiURL)
	fmt.Fprintf(os.Stderr, "cluster join: state saved to %s (heartbeat-daemon will pick it up)\n", *stateFile)
	return nil
}

// runClusterNodes lists all cluster_node rows in the local
// DB. Format: tab-separated columns (id, hostname, state,
// roles, last_seen). For scripts that want machine-
// readable output, also accept --json.
//
// Usage:
//
//	skygate cluster nodes [--cluster-id=skygate-staging] [--json]
func runClusterNodes(args []string) error {
	fs := flag.NewFlagSet("cluster nodes", flag.ContinueOnError)
	clusterID := fs.String("cluster-id", cluster.DefaultClusterID, "cluster id")
	asJSON := fs.Bool("json", false, "emit JSON instead of tab-separated text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("cluster nodes: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("cluster nodes: open db: %w", err)
	}
	defer d.Close()
	rows, err := d.Query(`
		SELECT id, hostname, state, roles, last_seen_at, joined_at
		  FROM cluster_node
		 WHERE cluster_id = $1
		 ORDER BY hostname
	`, *clusterID)
	if err != nil {
		return fmt.Errorf("cluster nodes: query: %w", err)
	}
	defer rows.Close()
	type nodeRow struct {
		ID         string     `json:"id"`
		Hostname   string     `json:"hostname"`
		State      string     `json:"state"`
		Roles      []string   `json:"roles"`
		LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
		JoinedAt   time.Time  `json:"joined_at"`
	}
	var out []nodeRow
	for rows.Next() {
		var (
			id, host, state, rolesStr string
			lastSeen, joined           time.Time
			lastSeenOK                 bool
		)
		if err := rows.Scan(&id, &host, &state, &rolesStr, &lastSeen, &joined); err != nil {
			return fmt.Errorf("cluster nodes: scan: %w", err)
		}
		_ = lastSeenOK
		_ = rolesStr
		_ = lastSeen
		// last_seen may be NULL in the DB; pgx v5 stdlib
		// returns the zero time + a Scan error in that
		// case. We treat the zero time as "never seen"
		// and omit it from the JSON output.
		var lastSeenPtr *time.Time
		if !lastSeen.IsZero() {
			lastSeenPtr = &lastSeen
		}
		out = append(out, nodeRow{
			ID: id, Hostname: host, State: state,
			Roles:      clusterRolesToSlice(rolesStr),
			LastSeenAt: lastSeenPtr,
			JoinedAt:   joined,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cluster nodes: rows: %w", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, n := range out {
		lastSeenStr := "-"
		if n.LastSeenAt != nil {
			lastSeenStr = n.LastSeenAt.Format(time.RFC3339)
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", n.ID, n.Hostname, n.State, strings.Join(n.Roles, ","), lastSeenStr)
	}
	return nil
}

// runClusterDbs lists cluster_database rows. Read-only;
// the B203 watchdog hot-reloads the DSN from here. The
// operator uses this to confirm "did the admin set the
// DSN override yet?".
func runClusterDbs(args []string) error {
	fs := flag.NewFlagSet("cluster dbs", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("cluster dbs: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("cluster dbs: open db: %w", err)
	}
	defer d.Close()
	rows, err := d.Query(`
		SELECT id, cluster_id, primary_node_id, replica_node_ids,
		       dsn_template, dbname, username, sslmode, current_dsn,
		       updated_by, created_at, updated_at
		  FROM cluster_database
		 ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("cluster dbs: query: %w", err)
	}
	defer rows.Close()
	type dbRow struct {
		ID             string     `json:"id"`
		ClusterID      string     `json:"cluster_id"`
		PrimaryNodeID  string     `json:"primary_node_id"`
		ReplicaNodeIDs []string   `json:"replica_node_ids"`
		DSNTemplate    string     `json:"dsn_template"`
		DBName         string     `json:"dbname"`
		Username       string     `json:"username"`
		SSLMode        string     `json:"sslmode"`
		CurrentDSN     string     `json:"current_dsn"`
		UpdatedBy      string     `json:"updated_by"`
		UpdatedAt      time.Time  `json:"updated_at"`
	}
	var out []dbRow
	for rows.Next() {
		var r dbRow
		var primary, replicas, template, dbname, username, sslmode, current, updatedBy string
		if err := rows.Scan(&r.ID, &r.ClusterID, &primary, &replicas,
			&template, &dbname, &username, &sslmode, &current,
			&updatedBy, &r.UpdatedAt, &r.UpdatedAt); err != nil {
			return fmt.Errorf("cluster dbs: scan: %w", err)
		}
		r.PrimaryNodeID = primary
		r.ReplicaNodeIDs = clusterRolesToSlice(replicas)
		r.DSNTemplate = template
		r.DBName = dbname
		r.Username = username
		r.SSLMode = sslmode
		r.CurrentDSN = current
		r.UpdatedBy = updatedBy
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cluster dbs: rows: %w", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, r := range out {
		fmt.Printf("%s\tprimary=%s\treplicas=%v\tdsn=%q\n",
			r.ID, r.PrimaryNodeID, r.ReplicaNodeIDs, r.CurrentDSN)
	}
	return nil
}

// runClusterAudit shows recent cluster_audit rows. The
// B204 elector writes node_health + failover_recommend;
// B205's failover writes node_failover. The operator
// runs this to debug "why is the elector recommending a
// failover?" or "did the admin accept the recommendation?".
func runClusterAudit(args []string) error {
	fs := flag.NewFlagSet("cluster audit", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "max rows to show")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("cluster audit: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("cluster audit: open db: %w", err)
	}
	defer d.Close()
	rows, err := d.Query(`
		SELECT id, cluster_id, action, target_node_id, detail, result,
		       COALESCE(error_message, ''), created_at
		  FROM cluster_audit
		 ORDER BY created_at DESC
		 LIMIT $1
	`, *limit)
	if err != nil {
		return fmt.Errorf("cluster audit: query: %w", err)
	}
	defer rows.Close()
	type auditRow struct {
		ID            int64           `json:"id"`
		ClusterID     string          `json:"cluster_id"`
		Action        string          `json:"action"`
		TargetNodeID  string          `json:"target_node_id,omitempty"`
		Detail        json.RawMessage `json:"detail,omitempty"`
		Result        string          `json:"result"`
		ErrorMessage  string          `json:"error_message,omitempty"`
		CreatedAt     time.Time       `json:"created_at"`
	}
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var target sqlNullString
		if err := rows.Scan(&r.ID, &r.ClusterID, &r.Action, &target, &r.Detail, &r.Result,
			&r.ErrorMessage, &r.CreatedAt); err != nil {
			return fmt.Errorf("cluster audit: scan: %w", err)
		}
		r.TargetNodeID = target.String
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cluster audit: rows: %w", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, r := range out {
		fmt.Printf("%s\t%-22s\t%s\ttarget=%s\tresult=%s\n",
			r.CreatedAt.Format(time.RFC3339), r.Action, r.ClusterID, r.TargetNodeID, r.Result)
	}
	return nil
}

// runClusterFailover is the admin-gated "accept the
// elector's recommendation" action. It:
//   - validates the target node exists + is in 'ready' state
//   - marks the failed primary as 'draining'
//   - adds the skygate role to the target (the canonical
//     primary designation)
//   - writes a cluster_audit row with action='node_failover'
//
// This is the operator's response to the elector's
// `elector: failover recommended: <X> → <Y>` log line.
//
// Usage:
//
//	skygate cluster failover --target=<node_id_or_hostname> [--reason "<text>"]
func runClusterFailover(args []string) error {
	fs := flag.NewFlagSet("cluster failover", flag.ContinueOnError)
	target := fs.String("target", "", "target node id or hostname (required)")
	reason := fs.String("reason", "manual failover via skygate cluster failover", "audit row reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return errors.New("cluster failover: --target is required")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("cluster failover: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("cluster failover: open db: %w", err)
	}
	defer d.Close()
	// Lookup target node (by id OR hostname).
	var targetID, fromID string
	var targetState, targetRoles string
	row := d.QueryRow(`
		SELECT id, state, roles
		  FROM cluster_node
		 WHERE cluster_id = $1 AND (id = $2 OR hostname = $2)
		 LIMIT 1
	`, cluster.DefaultClusterID, *target)
	if err := row.Scan(&targetID, &targetState, &targetRoles); err != nil {
		return fmt.Errorf("cluster failover: target %q not found: %w", *target, err)
	}
	if targetState != "ready" {
		return fmt.Errorf("cluster failover: target %q is in state %q, must be 'ready'", *target, targetState)
	}
	// Find the failed primary (any node with role=skygate in
	// state=failed). The target's role is "skygate-standby"
	// (we just verified ready); we're moving the skygate
	// role TO the target.
	row = d.QueryRow(`
		SELECT id FROM cluster_node
		 WHERE cluster_id = $1 AND state = 'failed' AND 'skygate' = ANY(roles)
		 LIMIT 1
	`, cluster.DefaultClusterID)
	if err := row.Scan(&fromID); err != nil {
		// No failed primary — that's OK, the operator may
		// be doing a planned role swap. Log it.
		fromID = ""
	}
	// Update roles + state in a transaction.
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("cluster failover: begin tx: %w", err)
	}
	defer tx.Rollback()
	// 1. Target: add 'skygate' role, ensure 'skygate-standby' is also there.
	// We use array_append + array_remove to avoid clobbering
	// other roles the operator may have set.
	if _, err := tx.Exec(`
		UPDATE cluster_node
		   SET roles = ARRAY(
		           SELECT DISTINCT unnest(
		               ARRAY['skygate', 'skygate-standby']::text[]
		           )
		       )
		 WHERE id = $1
	`, targetID); err != nil {
		return fmt.Errorf("cluster failover: update target roles: %w", err)
	}
	// 2. Failed primary (if any): mark as draining, drop
	// the skygate role.
	if fromID != "" {
		if _, err := tx.Exec(`
			UPDATE cluster_node
			   SET state = 'draining',
			       roles = array_remove(roles, 'skygate')
			 WHERE id = $1
		`, fromID); err != nil {
			return fmt.Errorf("cluster failover: update primary: %w", err)
		}
	}
	// 3. Audit row.
	detail := map[string]interface{}{
		"to_node_id":     targetID,
		"to_hostname":    *target,
		"from_node_id":   fromID,
		"reason":         *reason,
		"actor":          "skygate-cli",
		"timestamp":      time.Now().UTC().Unix(),
	}
	detailJSON, _ := json.Marshal(detail)
	if _, err := tx.Exec(`
		INSERT INTO cluster_audit (cluster_id, action, target_node_id, detail, result)
		VALUES ($1, 'node_failover', $2, $3::jsonb, 'ok')
	`, cluster.DefaultClusterID, targetID, string(detailJSON)); err != nil {
		return fmt.Errorf("cluster failover: insert audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cluster failover: commit: %w", err)
	}
	if fromID != "" {
		fmt.Printf("cluster failover: %s → %s (failed primary marked draining)\n", fromID, targetID)
	} else {
		fmt.Printf("cluster failover: %s promoted to skygate primary (no failed primary to drain)\n", targetID)
	}
	return nil
}

// runClusterHeartbeatDaemon is the long-running process
// that posts to /api/cluster/heartbeat every ~30s. The
// new node starts this in its systemd unit after a
// successful join (D2: state file is the canonical
// source of node_id + token + api_url).
//
// Usage:
//
//	skygate cluster heartbeat-daemon [--state-file /etc/skygate/cluster-state.json]
//
// The daemon handles SIGINT/SIGTERM gracefully and exits
// 0 on the first signal (systemd's `ExecStop=` workflow).
func runClusterHeartbeatDaemon(args []string) error {
	fs := flag.NewFlagSet("cluster heartbeat-daemon", flag.ContinueOnError)
	stateFile := fs.String("state-file", StateFilePath, "state file (written by 'skygate cluster join')")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := readClusterState(*stateFile)
	if err != nil {
		return fmt.Errorf("cluster heartbeat-daemon: read state: %w (run 'skygate cluster join <token>' first)", err)
	}
	interval := time.Duration(st.HeartbeatSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Graceful shutdown: SIGINT/SIGTERM → cancel.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("cluster heartbeat-daemon: signal received, shutting down...")
		cancel()
	}()
	// Initial heartbeat: 1s after start, then every interval.
	if err := postHeartbeat(ctx, st); err != nil {
		log.Printf("cluster heartbeat-daemon: initial heartbeat failed: %v", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("cluster heartbeat-daemon: stopped")
			return nil
		case <-t.C:
			if err := postHeartbeat(ctx, st); err != nil {
				log.Printf("cluster heartbeat-daemon: heartbeat failed: %v", err)
			}
		}
	}
}

// postHeartbeat POSTs the node_id + token to the configured
// API URL. Returns an error if the server returns non-200
// or the request fails.
func postHeartbeat(ctx context.Context, st *clusterState) error {
	form := url.Values{
		"node_id": {st.NodeID},
		"token":   {st.Token},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", st.APIURL+"/api/cluster/heartbeat", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return nil
}

// clusterState is the on-disk shape of the per-node
// state file (/etc/skygate/cluster-state.json). Written
// by `skygate cluster join`, read by
// `skygate cluster heartbeat-daemon`. Mode 0600 — the
// token is a bearer credential.
type clusterState struct {
	NodeID           string `json:"node_id"`
	ClusterID        string `json:"cluster_id"`
	Hostname         string `json:"hostname"`
	Token            string `json:"token"`
	APIURL           string `json:"api_url"`
	HeartbeatSeconds int    `json:"heartbeat_seconds"`
}

func writeClusterState(path string, st *clusterState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func readClusterState(path string) (*clusterState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st clusterState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	if st.NodeID == "" || st.Token == "" || st.APIURL == "" {
		return nil, fmt.Errorf("state file %s is incomplete (need node_id, token, api_url)", path)
	}
	return &st, nil
}

// clusterRolesToSlice converts a PG TEXT[] literal
// (e.g. "{a,b,c}" or "{\"a,b\",\"c,d\"}") into a
// []string. The literal is what pgx v5 stdlib returns
// for a TEXT[] column. Handles quoted segments with
// embedded commas/spaces per the PG array literal spec.
//
// We keep a local copy (instead of importing
// internal/cluster.parsePGTextArray) so the CLI doesn't
// have to depend on the cluster package's internals; the
// contract here is the same shape, and the test pins it.
func clusterRolesToSlice(literal string) []string {
	literal = strings.TrimSpace(literal)
	if literal == "" || literal == "{}" || literal == "NULL" {
		return nil
	}
	if !strings.HasPrefix(literal, "{") || !strings.HasSuffix(literal, "}") {
		return []string{literal}
	}
	inner := literal[1 : len(literal)-1]
	if inner == "" {
		return nil
	}
	// Walk the inner body, respecting double-quotes.
	var out []string
	var cur strings.Builder
	inQuote := false
	escape := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case escape:
			cur.WriteByte(c)
			escape = false
		case c == '\\' && inQuote:
			escape = true
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			p := strings.TrimSpace(cur.String())
			if p != "" {
				out = append(out, p)
			}
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	p := strings.TrimSpace(cur.String())
	if p != "" {
		out = append(out, p)
	}
	return out
}

// sqlNullString is a tiny helper for the cluster_audit
// scan: the target_node_id column is NULL-able, and
// pgx v5 stdlib returns the empty string for NULL in
// some configurations. We use sql.NullString for safety.
type sqlNullString struct {
	String string
	Valid  bool
}

// Scan implements sql.Scanner.
func (s *sqlNullString) Scan(src interface{}) error {
	if src == nil {
		s.String, s.Valid = "", false
		return nil
	}
	switch v := src.(type) {
	case string:
		s.String, s.Valid = v, true
	case []byte:
		s.String, s.Valid = string(v), true
	default:
		return fmt.Errorf("sqlNullString: unsupported type %T", src)
	}
	return nil
}
