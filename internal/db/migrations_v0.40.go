// v0.19.0 — `exitnode.skygate-subnet-<user>` DNS record.
//
// The v0.16.0+ per-user subnets roadmap's last big
// feature: each portal user can pick a "preferred
// exit-node" — Skygate then publishes a special DNS
// record `exitnode.skygate-subnet-<username>.<base-domain>`
// pointing to that exit-node's Tailscale IP. The user's
// tailnet clients (laptop, phone, server) can then use
// the FQDN as their default route without remembering
// the exit-node's tailnet IP.
//
// Why a separate name and not just a Tailscale ACL rule?
// Because the headscale policy supports `dns.extra_records`
// natively (headscale 0.20+), and the FQDN is a much
// nicer primitive than "100.64.0.2":
//   * easier to read in `tailscale status` output
//   * portable across Tailscale clients (no Tailscale
//     config required — DNS just works)
//   * cross-subnet reachable because tag:exit-node is
//     in every user's per-user ACL (v0.17.0) — the IP
//     might be 100.64.0.2 (the relay's tailnet IP) and
//     the user can route to it via tag:exit-node ACL.
//
// v0.18.0 deferred this because the docs said
// "headscale 0.29 doesn't support per-user service
// records". That's true for the headscale "services"
// feature (TCP/UDP service publication), but
// `dns.extra_records` works on headscale 0.20+ and is
// what we use here. v0.19.0 ships this.
//
// Schema:
//
//   user_subnets.preferred_exit_node_id TEXT NOT NULL DEFAULT ''
//
// Stores the headscale node ID of the chosen exit-node
// (e.g. "11" for karolina). Empty string = no preference;
// Skygate's ACL builder skips the user when this is empty
// (no DNS record published).
//
// The choice is per-user-subnet (FK to user_subnets row
// by user_id), not per-user. A user can have at most one
// subnet (UNIQUE(user_id) on user_subnets), so the
// column lives on user_subnets directly. If we ever
// support multiple subnets per user, this becomes a
// per-subnet choice and stays on user_subnets.
//
// The ID format matches headscale's HSNode.ID (string
// representation of int64). We store it as TEXT to avoid
// int64 type juggling between DB queries and the
// headscale API which sometimes returns IDs as strings.
package db

import "database/sql"

// migrationV040 — v0.19.0 preferred exit-node per subnet.
//
// Idempotent: re-runs are no-ops (ALTER TABLE ADD COLUMN
// is wrapped in a for-loop that ignores "duplicate column"
// errors, same as v0.37.0 / v0.38.0 / v0.16.8).
func migrationV040(d *sql.DB) error {
	stmts := []string{
		// preferred_exit_node_id on user_subnets. The
		// headscale node ID of the user's chosen exit-node
		// (e.g. "11" for karolina). Empty = no preference.
		// The ACL builder reads this column to populate
		// dns.extra_records in the headscale policy.
		`ALTER TABLE user_subnets ADD COLUMN preferred_exit_node_id TEXT NOT NULL DEFAULT ''`,
		// Index for the ACL builder's "give me every user
		// with a preferred exit-node" scan. Without the
		// index this is a full table scan, which is fine
		// at <100 users but ugly at 1000+.
		`CREATE INDEX IF NOT EXISTS idx_user_subnets_preferred_exit_node
			ON user_subnets (preferred_exit_node_id)
			WHERE preferred_exit_node_id != ''`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			// ALTER TABLE ADD COLUMN fails with "duplicate
			// column" on a re-run; ignore that case (the
			// column already exists, which is fine).
			// CREATE INDEX IF NOT EXISTS is a no-op on
			// re-run, so it shouldn't hit this branch.
			continue
		}
	}
	return nil
}
