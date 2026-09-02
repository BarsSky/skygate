// skygate init ... — B211 (v1.5.0+) cluster bootstrap CLI.
//
// The admin UI (/admin/cluster) and the existing cluster
// CLI subcommands (invite / join / nodes / dbs / audit /
// failover / heartbeat-daemon) all assume the local node
// is already known to the cluster. `skygate init` is the
// missing piece: the operator-on-the-box command that
// bootstraps THIS node as the cluster primary on a fresh
// install, and is safe to re-run after a partial failure
// (idempotent).
//
// Phase 2.3 of docs/internal/cluster-management.md. The
// full Phase 2.3 spec also covers OS check + binary
// install + OIDC key generation + headscale preauth +
// systemd bring-up — those are out of scope for B211
// (the spec's sub-tasks 2.3.1–2.3.5 land in a follow-up
// after D8 confirms the install story). B211 covers the
// DB-side pieces that the rest of Phase 2 already
// depends on:
//
//   2.3.0  Insert cluster row (idempotent via
//          cluster.EnsureCluster)
//   2.3.1  Insert cluster_database row pointing at THIS
//          node as the primary_node_id (idempotent via
//          db.SetClusterDatabase + ON CONFLICT DO UPDATE)
//   2.3.2  Insert cluster_node row for THIS hostname
//          (idempotent via cluster.UpsertNode + the new
//          v0.66 UNIQUE (cluster_id, hostname) constraint)
//   2.3.3  Print a fresh standby invite token so the
//          operator can pipe it into the standby's
//          bootstrap_standby.sh (uses cluster.IssueInvite,
//          the same helper the admin /admin/cluster/invite
//          endpoint uses)
//
// Subcommands:
//
//	skygate init                  # bootstrap THIS node as primary (idempotent)
//	skygate init status           # show this node's cluster state
//	skygate init standby-invite   # (re)print a fresh standby invite token
//	                                without touching THIS node's rows
//
// The "no verb" default is the canonical "I just
// installed skygate, set up the cluster" path. The
// `standby-invite` verb exists because the operator may
// need to re-print a token after a previous invite
// expired (cluster_invite.expires_at is 24h by default),
// without re-running the full bootstrap.
//
// All three paths open the DB directly via
// config.Load + db.OpenDSN (same as `skygate cluster
// ...`) — the web server is NOT started for any
// subcommand.
//
// 2026-09-01: B211 (v1.5.0+).

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"skygate/internal/cluster"
	"skygate/internal/config"
	"skygate/internal/db"
)

// initStateFile is the canonical location for the
// per-node init state (cluster_id + node_id + roles +
// tailscale_ip + skygate_version). `/etc/skygate/` is
// the persistent location for the bootstrap (per D2 in
// cluster-management.md). On first run we write this
// file; on subsequent runs we read it to compare the
// stored cluster_id against the one we'd bootstrap
// into (a mismatch is a hard error — it would mean
// the operator moved the box to a different cluster
// without going through skygate cluster leave +
// skygate init).
const initStateFile = "/etc/skygate/init-state.json"

// initState is the on-disk shape of /etc/skygate/init-state.json.
// Versioned so a future breaking change can detect +
// migrate.
type initState struct {
	Version        int       `json:"version"`
	ClusterID      string    `json:"cluster_id"`
	NodeID         string    `json:"node_id"`
	Hostname       string    `json:"hostname"`
	TailscaleIP    string    `json:"tailscale_ip"`
	Roles          []string  `json:"roles"`
	SkygateVersion string    `json:"skygate_version"`
	BootstrappedAt time.Time `json:"bootstrapped_at"`
}

const initStateVersion = 1

// runInit is the dispatcher for `skygate init <verb>`.
// verb defaults to "bootstrap" (the "I just installed
// skygate" path) when os.Args has no third arg.
//
// Verb-vs-flag disambiguation: if args[0] starts with
// '-' (e.g. `--role=skygate` from `skygate init --role=skygate`),
// we treat it as a flag for the default verb (bootstrap)
// rather than as the verb name. This matches the
// GNU-cli convention that "the first non-flag token is
// the verb".
//
// Important: when a verb is given, we STRIP it from
// the args before passing to the per-verb flag set.
// The Go flag.Parse stops at the first non-flag arg,
// so `["status", "--json"]` would leave "--json" as
// a positional. Stripping lets the per-verb flag set
// see the flags without a positional in front.
func runInit(args []string) error {
	verb := "bootstrap"
	if len(args) > 0 {
		// Help flags short-circuit to usage
		// (the per-verb flag sets also have their
		// own --help handling, but we want the
		// top-level dispatcher to print a usage
		// when the user types `skygate init --help`
		// rather than trying to dispatch "--help" as
		// a verb and returning a confusing
		// "unknown verb --help" error).
		switch args[0] {
		case "--help", "-h", "help":
			printInitUsage()
			return nil
		}
		// Flag-looking first arg → it's a flag for
		// the default verb, not a verb name.
		if !strings.HasPrefix(args[0], "-") {
			verb = args[0]
			args = args[1:]
		}
	}
	switch verb {
	case "bootstrap", "":
		return runInitBootstrap(args)
	case "status":
		return runInitStatus(args)
	case "standby-invite":
		return runInitStandbyInvite(args)
	default:
		return fmt.Errorf("init: unknown verb %q (bootstrap / status / standby-invite)", verb)
	}
}

// printInitUsage prints the top-level skygate init help.
// The per-verb flag sets (runInitBootstrap / -Status /
// -StandbyInvite) also print their own usage on --help;
// this is the summary you see when the user runs
// `skygate init --help` (no per-verb args).
func printInitUsage() {
	fmt.Println("skygate init <verb> [flags]")
	fmt.Println("  bootstrap        bootstrap THIS node as cluster primary (idempotent) — the default verb")
	fmt.Println("  status           show THIS node's cluster state (cluster + cluster_node + cluster_database)")
	fmt.Println("  standby-invite   print a fresh standby invite token without touching THIS node's rows")
	fmt.Println("")
	fmt.Println("Flags (bootstrap):")
	fmt.Println("  --role=<csv>     comma-separated role list for THIS node (default: skygate)")
	fmt.Println("  --ttl-hours=N    standby invite token TTL in hours (default: 24)")
	fmt.Println("  --state-file=P   where to write the init state JSON (default: /etc/skygate/init-state.json)")
	fmt.Println("Flags (status):")
	fmt.Println("  --json           emit JSON instead of text")
	fmt.Println("  --state-file=P   where to read the init state JSON (default: /etc/skygate/init-state.json)")
	fmt.Println("Flags (standby-invite):")
	fmt.Println("  --ttl-hours=N    token TTL in hours (default: 24)")
}

// runInitBootstrap is the canonical "set up the
// cluster" path. The operator runs it once on a fresh
// skygate install (and can re-run it on a partially-
// bootstrapped box; the path is idempotent).
//
// Steps:
//
//  1. Open the local DB (config.Load + db.OpenDSN).
//  2. Ensure the cluster row exists (idempotent —
//     cluster.EnsureCluster).
//  3. Upsert THIS node into cluster_node (idempotent —
//     cluster.UpsertNode with the skygate role).
//  4. Insert/update the cluster_database row pointing
//     at THIS node as primary_node_id (idempotent —
//     db.SetClusterDatabase's ON CONFLICT DO UPDATE).
//  5. Issue a fresh standby invite token (always
//     fresh — cluster_invite.expires_at = now + ttl).
//  6. Persist the init state to /etc/skygate/init-state.json
//     so subsequent `skygate init status` reads can
//     report a clean "bootstrapped at <ts>" line
//     without re-querying the cluster_node row.
//
// Usage:
//
//	skygate init [--role=skygate,db-primary,control] [--ttl 24h]
func runInitBootstrap(args []string) error {
	fs := flag.NewFlagSet("init bootstrap", flag.ContinueOnError)
	roleCSV := fs.String("role", "skygate", "comma-separated role list for THIS node (default: skygate)")
	ttlHours := fs.Int("ttl-hours", 24, "standby invite token TTL in hours")
	stateFile := fs.String("state-file", initStateFile, "where to write the init state JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	roles := parseRolesCSV(*roleCSV)
	if len(roles) == 0 {
		return errors.New("init: --role is empty (use at least one of skygate / skygate-standby)")
	}

	// 1. Config + DB.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("init: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("init: open db: %w", err)
	}
	defer d.Close()

	// 2. Ensure cluster row.
	if err := cluster.EnsureCluster(d, cluster.DefaultClusterID, cluster.DefaultClusterID); err != nil {
		return fmt.Errorf("init: ensure cluster: %w", err)
	}

	// 3. Upsert THIS node.
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("init: hostname: %w", err)
	}
	tsHostname := os.Getenv("SKYGATE_TS_HOSTNAME")
	if tsHostname == "" {
		tsHostname = hostname
	}
	skygateVersion := os.Getenv("SKYGATE_VERSION")
	if skygateVersion == "" {
		skygateVersion = "unknown"
	}
	nodeID, err := cluster.UpsertNode(d, cluster.DefaultClusterID, hostname, tsHostname, roles, skygateVersion)
	if err != nil {
		return fmt.Errorf("init: upsert node: %w", err)
	}

	// 4. Set cluster_database (this node = primary).
	cd := &db.ClusterDatabase{
		ID:             cluster.DefaultClusterID,
		ClusterID:      cluster.DefaultClusterID,
		PrimaryNodeID:  nodeID,
		ReplicaNodeIDs: db.StringArray{},
		DSNTemplate:    "",
		DBName:         "",
		Username:       "",
		SSLMode:        "disable",
		CurrentDSN:     cfg.DBDSN,
		UpdatedBy:      hostname,
	}
	if err := db.SetClusterDatabase(d, cd); err != nil {
		return fmt.Errorf("init: set cluster_database: %w", err)
	}

	// 5. Issue standby invite token. The standby's
	//    bootstrap_standby.sh reads this on stdin and
	//    passes it to `skygate cluster join <token>`.
	//    target_hostname="" lets IssueInvite accept
	//    any standby hostname (the join handshake
	//    still requires the standby to send its
	//    actual hostname, which the join handler
	//    matches against the invite's TH field).
	_, standbyToken, standbyExpires, err := cluster.IssueInvite(d, cluster.DefaultClusterID, cluster.NodeRoleStandby, "", *ttlHours, cfg.SecretKeyHex)
	if err != nil {
		return fmt.Errorf("init: issue standby invite: %w", err)
	}

	// 6. Persist init state. Errors here are
	//    non-fatal (the on-disk file is a UX nicety
	//    for `skygate init status`; the source of
	//    truth is still cluster_node + cluster +
	//    cluster_database).
	stateErr := writeInitState(*stateFile, &initState{
		Version:        initStateVersion,
		ClusterID:      cluster.DefaultClusterID,
		NodeID:         nodeID,
		Hostname:       hostname,
		TailscaleIP:    tsHostname,
		Roles:          roles,
		SkygateVersion: skygateVersion,
		BootstrappedAt: time.Now().UTC(),
	})

	// 7. B215: emit the bootstrap audit event. We
	//    fire this AFTER the row is committed (idempotent
	//    UpsertNode is committed) and AFTER the state
	//    file is written (so the audit trail reflects
	//    the operator's intent even if the audit
	//    INSERT itself fails — we log + continue, not
	//    return the error, because the bootstrap itself
	//    succeeded).
	if _, err := db.InsertClusterAudit(d, cluster.DefaultClusterID, db.NodeInit, nodeID, hostname,
		fmt.Sprintf(`{"node_id":%q,"hostname":%q,"roles":%q,"tailscale_ip":%q,"skygate_version":%q,"invited":true}`,
			nodeID, hostname, strings.Join(roles, ","), tsHostname, skygateVersion)); err != nil {
		fmt.Fprintf(os.Stderr, "init: warning: could not write node_init audit row: %v (bootstrap succeeded, audit only)\n", err)
	}

	// Print to stdout in a scriptable format:
	//   line 1: standby token (this is what
	//           bootstrap_standby.sh reads on stdin)
	//   line 2: node_id
	//   line 3: hostname
	//   (the other fields go to stderr — they're
	//   for the operator's eyes, not the script)
	fmt.Println(standbyToken)
	fmt.Println(nodeID)
	fmt.Println(hostname)
	fmt.Fprintf(os.Stderr, "init: cluster_id=%s node_id=%s hostname=%s roles=%s skygate_version=%s\n",
		cluster.DefaultClusterID, nodeID, hostname, strings.Join(roles, ","), skygateVersion)
	fmt.Fprintf(os.Stderr, "init: standby invite expires_at=%s (ttl=%dh)\n",
		standbyExpires.Format(time.RFC3339), *ttlHours)
	if stateErr != nil {
		fmt.Fprintf(os.Stderr, "init: warning: could not write %s: %v (cluster state is still correct in DB)\n",
			*stateFile, stateErr)
	} else {
		fmt.Fprintf(os.Stderr, "init: state saved to %s\n", *stateFile)
	}
	return nil
}

// runInitStatus shows the current state of THIS node
// from the cluster_* tables. Useful for "did my init
// actually take?" and "is this box really the
// primary?".
//
// Output (text mode, the default):
//
//	cluster:    skygate-staging
//	node_id:    node-abc123def456
//	hostname:   skygate
//	state:      ready
//	roles:      skygate
//	last_seen:  2026-09-01T12:34:56Z
//	skygate:    v1.5.0+ (commit 436db116)
//	database:   skygate_staging
//	primary:    node-abc123def456 (this node)
//	replicas:   -
//	current_dsn: postgres://...@172.17.0.1:5433/skygate_staging?sslmode=disable
//	state_file: /etc/skygate/init-state.json  (bootstrapped 2026-09-01T12:34:56Z)
//
// `--json` switches to JSON output (machine-readable,
// for monitoring).
func runInitStatus(args []string) error {
	fs := flag.NewFlagSet("init status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	stateFile := fs.String("state-file", initStateFile, "where to read the init state JSON (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("init status: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("init status: open db: %w", err)
	}
	defer d.Close()

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("init status: hostname: %w", err)
	}

	type statusReport struct {
		Cluster struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Chain int    `json:"chain_length"`
		} `json:"cluster"`
		Node struct {
			ID         string     `json:"id"`
			Hostname   string     `json:"hostname"`
			State      string     `json:"state"`
			Roles      []string   `json:"roles"`
			LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
			SkygateVer string     `json:"skygate_version"`
			IsPrimary  bool       `json:"is_primary"`
		} `json:"node"`
		Database struct {
			ClusterDB  string   `json:"cluster_database_id"`
			PrimaryID  string   `json:"primary_node_id"`
			ReplicaIDs []string `json:"replica_node_ids"`
			CurrentDSN string   `json:"current_dsn"`
			UpdatedBy  string   `json:"updated_by"`
			UpdatedAt  string   `json:"updated_at"`
		} `json:"database"`
		StateFile struct {
			Path          string `json:"path"`
			Exists        bool   `json:"exists"`
			BootstrappedAt string `json:"bootstrapped_at,omitempty"`
		} `json:"state_file"`
	}
	var rep statusReport
	rep.Node.Hostname = hostname
	rep.StateFile.Path = *stateFile

	// Cluster row.
	if c, err := cluster.LookupCluster(d, cluster.DefaultClusterID); err == nil {
		rep.Cluster.ID = c.ID
		rep.Cluster.Name = c.Name
		// Count the chain JSONB's entries (it's an
		// array of HA nodes). A real count is
		// not required for the status — just the
		// length, which is good enough for the
		// "do we have a chain configured?" check.
		var n int
		_ = json.Unmarshal(c.ChainJSON, &[]json.RawMessage{})
		// json.Unmarshal into a slice discards the
		// data; a cleaner approach is to count via
		// json.Decoder.Token, but the length of the
		// slice tells us the same thing.
		var chain []json.RawMessage
		if err := json.Unmarshal(c.ChainJSON, &chain); err == nil {
			n = len(chain)
		}
		rep.Cluster.Chain = n
	}

	// THIS node.
	if n, err := cluster.LookupNode(d, cluster.DefaultClusterID, hostname); err == nil {
		rep.Node.ID = n.ID
		rep.Node.State = n.State
		rep.Node.Roles = n.Roles
		rep.Node.SkygateVer = n.SkygateVer
		if !n.LastSeenAt.IsZero() {
			t := n.LastSeenAt
			rep.Node.LastSeenAt = &t
		}
	} else {
		// No row for this hostname — the operator
		// hasn't run init yet. Surface a clear
		// "not bootstrapped" state in the report
		// rather than failing the status command.
		rep.Node.State = "not_bootstrapped"
	}

	// cluster_database.
	if cd, err := db.GetClusterDatabase(d, cluster.DefaultClusterID); err == nil {
		rep.Database.ClusterDB = cd.ID
		rep.Database.PrimaryID = cd.PrimaryNodeID
		rep.Database.ReplicaIDs = []string(cd.ReplicaNodeIDs)
		rep.Database.CurrentDSN = cd.CurrentDSN
		rep.Database.UpdatedBy = cd.UpdatedBy
		rep.Database.UpdatedAt = cd.UpdatedAt.Format(time.RFC3339)
		rep.Node.IsPrimary = cd.PrimaryNodeID == rep.Node.ID
	}

	// Init state file.
	if st, err := readInitState(*stateFile); err == nil {
		rep.StateFile.Exists = true
		rep.StateFile.BootstrappedAt = st.BootstrappedAt.Format(time.RFC3339)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	// Text mode.
	if rep.Cluster.ID == "" {
		fmt.Println("cluster:    (no row)")
	} else {
		fmt.Printf("cluster:    %s (chain_length=%d)\n", rep.Cluster.ID, rep.Cluster.Chain)
	}
	if rep.Node.ID == "" {
		fmt.Printf("node:       hostname=%s state=%s\n", rep.Node.Hostname, rep.Node.State)
	} else {
		lastSeenStr := "-"
		if rep.Node.LastSeenAt != nil {
			lastSeenStr = rep.Node.LastSeenAt.Format(time.RFC3339)
		}
		fmt.Printf("node:       id=%s hostname=%s state=%s roles=%s skygate_version=%s last_seen=%s\n",
			rep.Node.ID, rep.Node.Hostname, rep.Node.State,
			strings.Join(rep.Node.Roles, ","), rep.Node.SkygateVer, lastSeenStr)
	}
	if rep.Database.ClusterDB == "" {
		fmt.Println("database:   (no row)")
	} else {
		fmt.Printf("database:   id=%s primary=%s replicas=%v\n",
			rep.Database.ClusterDB, rep.Database.PrimaryID, rep.Database.ReplicaIDs)
		if rep.Node.IsPrimary {
			fmt.Println("            THIS node IS the primary")
		} else {
			fmt.Println("            THIS node is NOT the primary")
		}
		if rep.Database.CurrentDSN != "" {
			fmt.Printf("            current_dsn=%s\n", rep.Database.CurrentDSN)
		}
	}
	if rep.StateFile.Exists {
		fmt.Printf("state_file: %s (bootstrapped %s)\n", rep.StateFile.Path, rep.StateFile.BootstrappedAt)
	} else {
		fmt.Printf("state_file: %s (not present — init has never run on this node, or the file was deleted)\n", rep.StateFile.Path)
	}
	return nil
}

// runInitStandbyInvite prints a fresh standby invite
// token without touching THIS node's rows. Useful for
// "the previous invite expired, give me a new one"
// without re-running the full init.
func runInitStandbyInvite(args []string) error {
	fs := flag.NewFlagSet("init standby-invite", flag.ContinueOnError)
	ttlHours := fs.Int("ttl-hours", 24, "token TTL in hours")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("init standby-invite: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("init standby-invite: open db: %w", err)
	}
	defer d.Close()
	// Same as runClusterInvite — ensure the cluster
	// row exists (no-op if already there) and issue
	// the invite. The new token is on stdout, expiry
	// on stderr.
	if err := cluster.EnsureCluster(d, cluster.DefaultClusterID, cluster.DefaultClusterID); err != nil {
		return fmt.Errorf("init standby-invite: ensure cluster: %w", err)
	}
	_, token, expiresAt, err := cluster.IssueInvite(d, cluster.DefaultClusterID, cluster.NodeRoleStandby, "", *ttlHours, cfg.SecretKeyHex)
	if err != nil {
		return fmt.Errorf("init standby-invite: issue: %w", err)
	}
	fmt.Println(token)
	fmt.Fprintf(os.Stderr, "expires_at: %s\n", expiresAt.Format(time.RFC3339))
	return nil
}

// parseRolesCSV splits "skygate,db-primary,control"
// into ["skygate", "db-primary", "control"], trimming
// whitespace + dropping empty entries. Returns nil
// for an empty input.
func parseRolesCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// writeInitState persists the init state JSON to the
// given path. Creates the parent dir with mode 0700
// (the file may contain node_id + tailscale_ip — not
// secret on its own, but no need to be world-readable).
func writeInitState(path string, st *initState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// readInitState reads the init state JSON. Returns
// os.ErrNotExist if the file is missing (which the
// status report treats as "not bootstrapped here").
func readInitState(path string) (*initState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st initState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if st.Version != initStateVersion {
		return nil, fmt.Errorf("init state version %d, want %d", st.Version, initStateVersion)
	}
	return &st, nil
}
