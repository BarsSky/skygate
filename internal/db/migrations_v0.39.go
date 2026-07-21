// v0.17.1 — per-user subnet cross-user IP-level sharing.
//
// The v0.16.0+ subnets feature allocates a personal
// 10.0.<uid>.0/24 to each portal user who presses
// "Allocate subnet". v0.16.6 shipped the per-user
// isolation: alice can't reach bob's subnet because
// her per-user ACL rule only has her own CIDR in
// dst. v0.17.0 added tag:subnet-router in
// tagOwners so the v0.16.7 sidecar nodes register.
//
// v0.17.1 adds the missing third leg: an explicit
// "share my subnet with user X" mechanism. Without
// it, subnets are islands — alice's tailnet devices
// can talk to alice's subnet, but alice's office
// (on her own subnet) cannot talk to her partner's
// office (on their subnet) even when both parties
// want it.
//
// The "share" is an IP-level ACL rule, NOT a DNS
// record (v0.19.0 will add MagicDNS service records
// for shared subnets). At the ACL level it's just
// another entry in alice's dst list:
//
//   alice grants bob:
//     bob's per-user rule's dst gets
//     "10.0.<alice>.0/24:*" appended.
//
//   alice revokes bob:
//     bob's per-user rule's dst drops
//     "10.0.<alice>.0/24:*".
//
// The ACL builder in v0.17.0 reads the share table
// and appends each grantor's CIDR to the grantee's
// dst. No service records, no DNS, no Tailscale
// client changes — pure ACL plumbing.
//
// Schema:
//
//   user_subnet_shares
//     (grantor_user_id, grantee_user_id, created_at)
//     PRIMARY KEY (grantor, grantee)
//
// grantor = "the user whose subnet is being shared"
// grantee = "the user who gets access to grantor's
//            subnet"
//
// The asymmetry matters: bob shares his subnet with
// alice (grantor=bob, grantee=alice) means alice
// gets access to bob's subnet. NOT the other way
// around.
//
// Indices:
//   - PRIMARY KEY (grantor, grantee) — fast
//     "is this share row present" check during
//     ACL rebuild
//   - (grantee) — fast "what subnets can I (the
//     grantee) access" lookup for the per-user
//     dst extension
//
// Both foreign keys CASCADE on delete so a
// removed portal user cleans up their share rows
// without operator intervention.
package db

import "database/sql"

// migrationV039 — v0.17.1 cross-user IP-level sharing.
//
// Idempotent: re-runs are no-ops (CREATE TABLE
// IF NOT EXISTS, CREATE INDEX IF NOT EXISTS).
func migrationV039(d *sql.DB) error {
	stmts := []string{
		// The shares table. grantor is the user whose
		// subnet is being shared; grantee is the user
		// who gets access to grantor's subnet.
		`CREATE TABLE IF NOT EXISTS user_subnet_shares (
			grantor_user_id INTEGER NOT NULL,
			grantee_user_id INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (grantor_user_id, grantee_user_id),
			FOREIGN KEY (grantor_user_id) REFERENCES portal_users(id) ON DELETE CASCADE,
			FOREIGN KEY (grantee_user_id) REFERENCES portal_users(id) ON DELETE CASCADE
		)`,
		// Index on grantee — the ACL builder iterates
		// "for each user, what subnets are shared with
		// them" which is a scan by grantee. The
		// PRIMARY KEY index covers grantor lookups
		// (grantor-side); the grantee-side needs its
		// own index.
		`CREATE INDEX IF NOT EXISTS idx_user_subnet_shares_grantee
			ON user_subnet_shares (grantee_user_id)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
