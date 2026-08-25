// skygate acl-apply — B188.1 operator escape hatch.
//
// Force a one-shot headscale ACL re-apply. Used after a
// migration that changed exit-node-pref data (e.g. the
// V061 backfill that rewrites tag:exit-X rows to
// tag:dev-infra-X + re-enables via pinning) without
// triggering any of the user-facing handlers that
// normally call ApplyACLPipelineForPlane.
//
// The command:
//   1. Loads .env config
//   2. Opens the PG DB
//   3. Looks up the admin user (skyadmin by default)
//   4. Opens a headscale client for that user's
//      control plane
//   5. Calls acl.ApplyACLPipelineForPlane — this
//      rebuilds the headscale policy from the current
//      state of device_rules + user_exit_node_prefs +
//      device_exit_node_prefs + tagOwners
//   6. Logs the result + exits 0/1.
//
// Usage:
//   skygate acl-apply [-plane URL] [-user USERNAME]
//
// Flags:
//   -plane ""     — plane URL (empty = global default)
//   -user "skyadmin" — the portal user whose headscale
//                     control plane hosts the policy.
//                     Defaults to the canonical admin.
//
// 2026-08-26: v1.5.2 (B188.1).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"skygate/internal/acl"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/headscale"
)

func runAclApply(args []string) error {
	fs := flag.NewFlagSet("acl-apply", flag.ContinueOnError)
	plane := fs.String("plane", "", "plane URL (empty = global default)")
	userName := fs.String("user", "skyadmin", "portal user whose headscale control plane hosts the policy")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("acl-apply: flag parse: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("acl-apply: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("acl-apply: open db: %w", err)
	}
	defer d.Close()
	// Resolve user_id by username (for the audit log row).
	var userID int64
	if err := d.QueryRow(
		`SELECT id FROM portal_users WHERE username = $1`,
		*userName,
	).Scan(&userID); err != nil {
		return fmt.Errorf("acl-apply: user %q lookup: %w", *userName, err)
	}
	// Open the headscale client for this user.
	hs := headscale.New(cfg.HeadscaleURL, cfg.HeadscaleKey)
	// Re-apply the ACL.
	res := acl.ApplyACLPipelineForPlane(d, hs, *plane, nil, *userName,
		fmt.Sprintf("acl-apply user=%s plane=%s (B188.1 post-migration re-apply)", *userName, *plane),
		false,
	)
	if !res.Applied {
		log.Printf("acl-apply: Applied=false err=%v", res.Err)
		return fmt.Errorf("acl-apply failed: %w", res.Err)
	}
	log.Printf("acl-apply: Applied=true user=%s plane=%s", *userName, *plane)
	_ = userID // audit log row not written here (would be redundant with the audit row ApplyACLPipelineForPlane already writes)
	return nil
}

var _ = os.Args
