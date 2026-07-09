package db

import "database/sql"

// migrateV026 (2026-07-09): per-exit-node Tailscale AcceptRoutes policy.
//
// accept_routes controls whether the exit-node accepts subnet routes
// advertised by its Tailscale peers. Stored on exit_servers (where the
// admin's per-node Tailscale flags already live) and consumed by
// HS.SetAdvertisedRoutes() so that every SSH-driven `tailscale set` on
// the node also re-applies --accept-routes.
//
// Values: 0 = unset (do not change AcceptRoutes on the node), 1 = true,
// -1 = false.
//
// Why this exists: with --accept-routes=true on a node that also hosts
// another VPN server (Amnezia-AWG, OpenVPN, WireGuard), Tailscale pulls
// Google/Telegram/etc. into source-routing table 52 and any traffic from
// the other VPN container gets routed to the wrong peer (Telegram/Google
// end up black-holed via emilia). Setting this to -1 on a node that
// co-hosts another VPN is the only correct fix.
func migrateV026(d *sql.DB) error {
	stmts := []string{
		"ALTER TABLE exit_servers ADD COLUMN accept_routes INTEGER NOT NULL DEFAULT 0",
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			// ignore: column may already exist on a re-run
			continue
		}
	}
	return nil
}
