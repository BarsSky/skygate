// B390 (2026-10-25, TD-33 H1/H2) — A REPLY MUST NOT STATE A VALUE IT DOES NOT HAVE.
//
// The operator's bot answered /exit_nodes and /myexitnodes with "offline" for EVERY
// relay, including the healthy one his devices were routing through. The reason was not
// headscale: both replies read
//
//	SELECT node_id, COALESCE(last_seen, 0), COALESCE(online, 0) FROM devices
//
// and the `devices` table has NO WRITER anywhere in the repository (its only INSERT was
// a test fixture removed in v1.3.0). The query therefore always returned zero rows, the
// map stayed empty, `online == 0` was read as "not online", and every relay was
// reported dead — the B273 incident class, on the two commands B273 did not touch.
//
// The truthful source already exists: `exit_node_health`, the snapshot the exit-node
// monitor (B273) refreshes every couple of minutes and that /admin/exit-nodes and
// /exit_nodes_health render. It carries the same ladder the panel shows
// (online / untagged / degraded / offline) plus the last_seen headscale reported.
//
// The rule this file enforces: a node the snapshot does not describe is reported as
// "no data", never as "offline". An absent value and a measured zero are different
// facts, and only one of them is ours to state.
package telegram

import (
	"database/sql"
	"skygate/internal/db"
	"skygate/internal/i18n"
)

// exitNodeHealthIndex loads the monitor's current snapshot keyed by node id.
//
// A read failure is returned to the caller (which reports it as a fact about the READ,
// never as a status for the nodes); an empty table is NOT an error — a fresh install has
// simply not been sampled yet, and the replies say so.
func exitNodeHealthIndex(d *sql.DB) (map[string]db.ExitNodeHealth, error) {
	out := map[string]db.ExitNodeHealth{}
	if d == nil {
		return out, nil
	}
	rows, err := db.ListExitNodeHealth(d)
	if err != nil {
		return out, err
	}
	for _, h := range rows {
		out[h.NodeID] = h
	}
	return out, nil
}

// exitNodeStatusLabel turns the monitor's state into a localized word.
//
// B273 ordered the ladder by what actually blocks client egress, and the panel shows all
// four rungs; the bot used to collapse them to one hardcoded pair. `untagged` is a
// WORKING relay that merely lacks tag:exit-node, so calling it "offline" (or hiding it)
// would be the same lie in a different place.
func exitNodeStatusLabel(lang, state string) string {
	switch state {
	case "online":
		return i18n.T(lang, "bot.exit_nodes.status_online")
	case "untagged":
		return i18n.T(lang, "bot.exit_nodes.status_untagged")
	case "degraded":
		return i18n.T(lang, "bot.exit_nodes.status_degraded")
	case "offline":
		return i18n.T(lang, "bot.exit_nodes.status_offline")
	default:
		// "", "unknown" and any future value: we have not measured this node.
		return i18n.T(lang, "bot.exit_nodes.status_no_data")
	}
}

// exitNodeStatusFor is the one-liner both replies use: the snapshot's state when the
// node is described, otherwise "no data" — never a guess.
func exitNodeStatusFor(lang string, idx map[string]db.ExitNodeHealth, nodeID string) (string, db.ExitNodeHealth) {
	h, ok := idx[nodeID]
	if !ok {
		return i18n.T(lang, "bot.exit_nodes.status_no_data"), db.ExitNodeHealth{}
	}
	return exitNodeStatusLabel(lang, h.State), h
}

// deviceCountForUser is the H2 half: /my_status said "devices: 0" when the read FAILED,
// because both the legacy and the rich path discarded the error and formatted a zero
// that had never been measured (the B186.3 guard only pinned that the call existed).
//
// The error is returned so the caller can print an unavailable marker plus the reason
// instead of a number.
func deviceCountForUser(d *sql.DB, username string) (int64, error) {
	owned, err := db.ListNodeOwnersByUsername(d, username)
	if err != nil {
		return 0, err
	}
	return int64(len(owned)), nil
}
