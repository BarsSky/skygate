// skygate join ... — B212 (v1.5.0+) cluster onboarding CLI.
//
// The admin UI (/admin/cluster) and the existing cluster
// CLI subcommands (invite / nodes / dbs / audit /
// failover / heartbeat-daemon) all assume the new node
// is already a member of the cluster. `skygate join` is
// the missing piece: the operator-on-the-box command
// that joins THIS node to a cluster as a standby using
// a fresh invite token. It is the standby-side
// counterpart of `skygate init` (B211, which is the
// primary-side counterpart).
//
// Phase 2.4 of docs/internal/cluster-management.md. The
// full Phase 2.4 spec also covers OS prereqs install +
// headscale preauth registration + systemd bring-up —
// those are out of scope for B212 (the spec's sub-tasks
// 2.4.2 + 2.4.3 land in a follow-up after the live
// bootstrap pipeline is exercised end-to-end). B212
// covers the join handshake + DSN bootstrap +
// state-file write, which is what the rest of Phase 3
// already depends on:
//
//   2.4.1  Validate token (cluster.VerifyToken local
//          sanity check before the HTTP POST; the
//          primary still verifies the signature in
//          /api/cluster/join's cluster.Join call)
//   2.4.2  Bootstrap services: DSN bootstrap —
//          the primary returns a ready-to-use DSN
//          (B212: JoinResponse.DSN), and the
//          standby writes it to the requested
//          env file (default: /etc/skygate/dbs.env)
//          so the standby's own skygate process
//          can source it. The role is opt-in: the
//          operator can also use the standby's
//          own .env DSN.
//   2.4.3  Register in headscale: NOT YET — the
//          headscale preauth is a separate step
//          (the standby runs `tailscale up
//          --authkey=...` after the join). This
//          is on the Phase 2.4 follow-up list.
//
// Subcommands:
//
//	skygate join <token>         # join THIS node as a standby (alias for `skygate cluster join`)
//	                             # B212: adds token sanity check + DSN bootstrap + next-steps message
//	skygate join status          # show this node's join state (node_id + last_seen from state file)
//	                             # requires `skygate cluster join` to have been run first
//
// The "no verb" path is the canonical "I just installed
// skygate on a new box, give it an invite token, and
// bring it into the cluster" flow. The `status` verb
// exists for the operator to confirm "did the join
// actually take?" without reading the raw state file.
//
// Both paths open the state file directly (no web
// server, no DB) — same pattern as the rest of
// `skygate cluster ...` and `skygate init ...`.
//
// 2026-09-02: B212 (v1.5.0+).

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// joinStateFile is the canonical location for the
// per-node join state (node_id + cluster_id + token +
// API URL). /etc/skygate/ is the persistent location
// for the bootstrap (per D2 in cluster-management.md).
//
// This is the SAME path the pre-B212 `skygate cluster
// join` used (/etc/skygate/cluster-state.json via
// StateFilePath in cluster.go), so the top-level
// `skygate join` is drop-in compatible with the
// existing `skygate cluster heartbeat-daemon` (which
// reads the same state file).
const joinStateFile = "/etc/skygate/cluster-state.json"

// runJoin is the dispatcher for `skygate join <verb>`
// or `skygate join <token>`. Disambiguation rule:
// if args[0] is a known verb (status) it dispatches;
// otherwise args[0] is the invite token and we fall
// through to the default verb (cluster-join).
func runJoin(args []string) error {
	if len(args) == 0 {
		return errors.New("join: missing token (usage: skygate join <token>)")
	}
	switch args[0] {
	case "--help", "-h", "help":
		printJoinUsage()
		return nil
	case "status":
		return runJoinStatus(args[1:])
	}
	// Default verb: <token>... — pass to runClusterJoin.
	return runClusterJoin(args)
}

// runJoinStatus shows the current state of THIS node's
// join (the cluster-state.json written by `skygate join`).
// Useful for "did the join actually take?" and "is the
// heartbeat-daemon still pointing at the right primary?".
//
// Output (text mode, the default):
//
//	state_file:   /etc/skygate/cluster-state.json
//	cluster_id:   skygate-staging
//	node_id:      node-abc123def456
//	hostname:     skygate-standby
//	api_url:      http://192.168.13.69:8080
//	heartbeat:    30s
//	token_age:    4h32m (issued ~4h32m ago; expires in 19h28m)
//
// The token age is informational (it reads the JWT's
// "exp" claim and compares to NOW). Tokens default to
// 24h TTL, so "token_age" > 24h means the token is
// EXPIRED and the next heartbeat will be rejected by
// the primary.
//
// `--json` switches to JSON output (machine-readable,
// for monitoring scripts).
func runJoinStatus(args []string) error {
	fs := flag.NewFlagSet("join status", flag.ContinueOnError)
	stateFile := fs.String("state-file", joinStateFile, "where to read the join state JSON")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := readClusterState(*stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No state file → the operator hasn't
			// joined yet. Return a friendly "not joined"
			// report rather than failing.
			if *asJSON {
				out := map[string]interface{}{
					"state_file": *stateFile,
					"joined":     false,
					"reason":     "no state file — run `skygate join <token>` first",
				}
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("state_file: %s\n", *stateFile)
			fmt.Println("joined:     false (no state file — run `skygate join <token>` first)")
			return nil
		}
		return fmt.Errorf("join status: read state: %w", err)
	}

	// Read the JWT's "exp" claim to compute token age.
	// The clusterState.Token is the raw sgn1 token; we
	// extract the payload (middle dot-separated field),
	// base64-decode it, and parse the JSON. This is the
	// same format B200's cluster.VerifyToken accepts.
	tokenAge, tokenExpiresAt := parseTokenAge(st.Token)

	type statusReport struct {
		StateFile      string `json:"state_file"`
		Joined         bool   `json:"joined"`
		ClusterID      string `json:"cluster_id"`
		NodeID         string `json:"node_id"`
		Hostname       string `json:"hostname"`
		APIURL         string `json:"api_url"`
		HeartbeatSecs  int    `json:"heartbeat_seconds"`
		TokenIssuedAt  string `json:"token_issued_at,omitempty"`
		TokenExpiresAt string `json:"token_expires_at,omitempty"`
		TokenAge       string `json:"token_age,omitempty"`
		TokenRemaining string `json:"token_remaining,omitempty"`
	}
	rep := statusReport{
		StateFile:     *stateFile,
		Joined:        true,
		ClusterID:     st.ClusterID,
		NodeID:        st.NodeID,
		Hostname:      st.Hostname,
		APIURL:        st.APIURL,
		HeartbeatSecs: st.HeartbeatSeconds,
	}
	if !tokenExpiresAt.IsZero() {
		rep.TokenExpiresAt = tokenExpiresAt.Format(time.RFC3339)
		rep.TokenAge = tokenAge.String()
		remaining := time.Until(tokenExpiresAt)
		if remaining > 0 {
			rep.TokenRemaining = remaining.String()
		} else {
			rep.TokenRemaining = "EXPIRED"
		}
	}

	if *asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("state_file:     %s\n", rep.StateFile)
	fmt.Printf("cluster_id:     %s\n", rep.ClusterID)
	fmt.Printf("node_id:        %s\n", rep.NodeID)
	fmt.Printf("hostname:       %s\n", rep.Hostname)
	fmt.Printf("api_url:        %s\n", rep.APIURL)
	fmt.Printf("heartbeat:      %ds\n", rep.HeartbeatSecs)
	if rep.TokenExpiresAt != "" {
		fmt.Printf("token_expires:  %s (in %s)\n", rep.TokenExpiresAt, rep.TokenRemaining)
		fmt.Printf("token_age:      %s\n", rep.TokenAge)
	} else {
		fmt.Println("token_expires:  (could not parse JWT — token may be in an unexpected format)")
	}
	return nil
}

// printJoinUsage prints the top-level skygate join help.
func printJoinUsage() {
	fmt.Println("skygate join <verb> [args]")
	fmt.Println("")
	fmt.Println("  <token>          join THIS node to the cluster using the given invite token")
	fmt.Println("                    (alias for `skygate cluster join <token>`, B212 adds DSN bootstrap)")
	fmt.Println("  status           show THIS node's join state (reads " + joinStateFile + ")")
	fmt.Println("")
	fmt.Println("Flags (join <token>):")
	fmt.Println("  --api-url=URL       skygate API base URL (the cluster's primary)")
	fmt.Println("  --state-file=PATH   where to write the join state JSON (default: /etc/skygate/cluster-state.json)")
	fmt.Println("  --role=CSV          comma-separated role list (default: skygate-standby)")
	fmt.Println("  --write-dsn-to=PATH if set, write a KEY=VALUE env file with the primary's DSN")
	fmt.Println("  --dsn-key=KEY       env key name for --write-dsn-to (default: SKYGATE_DB_DSN)")
	fmt.Println("  --no-heartbeat-hint suppress the 'next steps' message at the end")
	fmt.Println("Flags (join status):")
	fmt.Println("  --state-file=PATH   where to read the join state JSON (default: /etc/skygate/cluster-state.json)")
	fmt.Println("  --json              emit JSON instead of text")
}

// parseTokenAge extracts the JWT's "exp" claim and
// returns the token's age + absolute expiry time.
// Returns (0, zero time) if the token is not a
// well-formed sgn1 JWT (we don't error — the status
// command should be defensive about unexpected
// state-file contents).
//
// The sgn1 format is "<b64-payload>.<b64-sig>"
// (no header — the "sgn1" prefix is a literal
// sgn1.<...>.<...>... actually; we accept either
// with or without the "sgn1." prefix for safety).
func parseTokenAge(token string) (age time.Duration, expiresAt time.Time) {
	if token == "" {
		return
	}
	// Strip the "sgn1." prefix if present.
	stripped := strings.TrimPrefix(token, "sgn1.")
	parts := strings.SplitN(stripped, ".", 3)
	if len(parts) < 2 {
		return
	}
	// The payload is the FIRST part (the sgn1 format
	// is payload.signature; the B200 / B201 docs use
	// the same convention).
	// base64-decode it (RawURLEncoding matches the
	// b64 encoding B200 uses for the payload).
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return
	}
	if claims.Exp == 0 {
		return
	}
	expiresAt = time.Unix(claims.Exp, 0).UTC()
	age = time.Since(expiresAt)
	if age < 0 {
		// Token is still in the future (issued at < now).
		// Negative age is not meaningful; report 0.
		age = 0
	}
	return
}
