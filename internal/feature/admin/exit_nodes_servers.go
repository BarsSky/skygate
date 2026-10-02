// exit_nodes_servers.go — which exit nodes become exit_servers rows.
//
// Split out of exit_nodes.go in refactor Phase D (2026-10-01): the include
// predicate, the ensure/backfill pass and the tiny string helper it needs.

package admin

import (
	"fmt"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// ensureExitServers walks every headscale node and INSERT
// OR IGNOREs a row in exit_servers for any node that either
// (a) has an exit-node tag, or (b) advertises any routes.
// The "OR IGNORE" preserves the operator's manual row
// (possibly with enabled=0) so the discovery pass can't
// accidentally re-enable a node the operator disabled.
//
// 2026-07-31: v0.32.7 — exclude subnet-routers. Pre-fix
// `ensureExitServers` also matched any node that advertises
// any routes (condition b), which incorrectly included
// per-user subnet-routers (e.g. skygate-subnet-admin with
// tag:subnet-router advertising 10.0.1.0/24). The subnet-router
// is a LAN bridge for the tailnet, not an exit-node — it
// doesn't route traffic to the internet, doesn't have the
// tag:exit-* role, and shouldn't appear on /admin/exit-nodes.
// The fix: also skip nodes whose tags contain
// `tag:subnet-router` (and the `tag:dev-*` family which is
// the per-device v0.28.0 marker for user devices — those
// don't belong on an exit-node admin page either). A
// `tag:public`-only node with subnet routes is still
// included (public-tagged nodes are the relays that may
// legitimately advertise both 0.0.0.0/0 and a /32 set).
// shouldIncludeAsExitServer is the pure filter extracted from
// ensureExitServers (v0.32.7). Returns true if a node with
// the given tags + available-route count should appear on
// /admin/exit-nodes.
//
// Exclusion rules (added 2026-07-31, v0.32.7):
//   - tag:subnet-router → false (it's a LAN bridge, not an exit)
//   - tag:dev-*        → false UNLESS the node ALSO has a
//     tag:exit-node tag (v0.33.1.30 B82 override: a per-user
//     device that the operator has explicitly promoted to
//     exit-node IS an exit node — the v0.32.7 default of
//     excluding every tag:dev-* node was too aggressive for
//     the case where the operator wants a per-user-tagged
//     workstation to also act as an exit-node for the
//     tailnet. Real-world example: emilia/karolina/sharlotta
//     on the live VM — tagged as `tag:dev-skyadmin-<name>`
//     for the per-user ACL grant AND actually used as
//     exit-nodes via device_rules.exit_node_id references)
//
// Inclusion rules:
//   - any tag:exit-* tag → true
//   - has 1+ advertised route → true
//
// 2026-07-31: extracted from ensureExitServers so the filter
// logic is unit-testable without a live headscale.
// 2026-08-09: v0.33.1.30 B82 — `tag:dev-* + tag:exit-node`
// combo now passes the filter (the v0.32.7 default
// excluded all per-user devices; the v0.33.1.29 B81 fix
// surfaced this for operators who tagged their real
// exit-nodes as `tag:dev-skyadmin-*` and lost them to the
// B21 cleanup pass).
func shouldIncludeAsExitServer(tags []string, availableRouteCount int) bool {
	hasExitTag := false
	isSubnetRouter := false
	isPerUserDevice := false
	for _, t := range tags {
		if strings.Contains(t, "exit-node") {
			hasExitTag = true
		}
		if t == "tag:subnet-router" {
			isSubnetRouter = true
		}
		if strings.HasPrefix(t, "tag:dev-") {
			isPerUserDevice = true
		}
	}
	// tag:subnet-router is ALWAYS excluded — a LAN bridge is
	// not an exit-node regardless of other tags (the v0.32.7
	// intent: don't pollute the exit-nodes page with subnet
	// routers the operator didn't ask for).
	if isSubnetRouter {
		return false
	}
	// v0.33.1.30 B82 override: a per-user device that ALSO
	// has an explicit tag:exit-node IS an exit node (the
	// operator's intent — they tagged it themselves with
	// the standard exit-node tag). The original v0.32.7
	// default was "tag:dev-* → always excluded" which
	// silently removed emilia/karolina/sharlotta from
	// /admin/exit-nodes even though they were actively
	// used as exit-nodes via device_rules.exit_node_id.
	if isPerUserDevice && !hasExitTag {
		return false
	}
	return hasExitTag || availableRouteCount > 0
}

func (s *Service) ensureExitServers() {
	nodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		return
	}
	// Index nodes by ID for the cleanup pass below.
	nodeByID := make(map[string]headscale.NodeView, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	// Step 1: insert any node that should be in
	// exit_servers. The "OR IGNORE" preserves the
	// operator's manual row (possibly with enabled=0) so
	// the discovery pass can't accidentally re-enable a
	// node the operator disabled.
	for _, n := range nodes {
		if shouldIncludeAsExitServer(n.Tags, len(n.AvailableRoutes)) {
			db.InsertIgnoreExitServerOnDiscovery(s.dbc(), n.ID, n.GivenName, strings.Join(n.IPAddresses, ","))
		}
	}
	// Step 2 (v0.32.7): clean up rows that the pre-fix
	// filter would have included but the new one excludes
	// (e.g. skygate-subnet-admin with tag:subnet-router
	// that was inserted into exit_servers before the
	// v0.32.7 fix tightened the filter). Without this,
	// the stale row would keep showing up on
	// /admin/exit-nodes even after the new code excludes
	// it from inserts.
	//
	// We only delete rows whose headscale node still exists
	// (node_id is in our current node list) and now fails
	// the filter. Rows for nodes that disappeared from
	// headscale are operator artifacts (e.g. the user
	// deleted the node from the tailnet) — leave those
	// alone; the operator can `kill` them via the
	// /admin/exit-nodes page or directly in the DB.
	rows, _ := s.dbc().Query("SELECT id, node_id FROM exit_servers")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			var nid string
			if err := rows.Scan(&id, &nid); err != nil {
				continue
			}
			n, ok := nodeByID[nid]
			if !ok {
				continue // node gone from headscale — leave row
			}
			if shouldIncludeAsExitServer(n.Tags, len(n.AvailableRoutes)) {
				continue // still qualifies — keep row
			}
			// Node no longer qualifies — delete the row.
			// Best-effort: errors are logged but not fatal
			// (the next page load will retry).
			// 2026-08-05 v0.33.1.12: db.PlaceholdersList(1) so
			// "?" → "$1" on PG. Without this the auto-cleanup
			// (which fires from the background discovery loop)
			// silently fails on PG and the next page load
			// retries forever.
			if _, err := s.dbc().Exec("DELETE FROM exit_servers WHERE id = "+db.PlaceholdersList(1), id); err != nil {
				s.Backend.Audit(0, "skygate", "exit_server_cleanup_failed",
					fmt.Sprintf("node_id=%s id=%d: %v", nid, id, err))
			}
		}
	}
}

// firstNonEmptyStr returns a unless it is empty, in which case it returns
// b. Added 2026-09-18 (R6) so a handler can prefer an operator-supplied
// ?err= flash over its own internally-generated one without an if-block
// at every call site.
func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
