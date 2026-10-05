// relay_keepalive_b352.go — B352 (2026-10-05).
//
// A RELAY THAT LOST ITS LAST RULE KEEPS ITS LAST ADVERTISEMENT FOREVER.
//
// Every sync path is built from `device_rules.exit_node_id`: SyncAdvertisedRoutes
// iterates the DISTINCT relays named by rules, StaggeredSync builds `nodes` with the
// same GROUP BY, and the per-row "Re-sync" button returns
// `info=no IP/subnet rules target this node`. So when the assignment engine moves the
// last prefix away from a relay — which is exactly what B275/B276 are for — nobody
// ever visits that relay again, and `tailscale set --advertise-routes=` is REPLACE, so
// the stale set stays on the node.
//
// Live (2026-10-05): karolina timed out once, its 251 prefixes moved to emilia and
// sharlotta, and when the prefixes came back emilia still advertised 29 of them. Since
// headscale serves a subnet prefix from exactly ONE relay (B274), those 29 destinations
// were contested: the device pinned to the new owner had `via=karolina` in its grant
// while the primary for those prefixes could be emilia, and it lost them. Clearing the
// stale set by hand required SSH to the relay, because the panel refused to touch a
// relay with no rules.
//
// The record that skygate HAS configured a relay is `relay_apply_state:<relay>` (B309)
// — written on every application, success or failure. Such a relay must keep being
// visited so its advertisement can follow the assignment table down to zero ("advertise
// nothing but the base routes", which is what OwnedPrefixesForRelay returns for it).
package exit_rules

import (
	"sort"
	"strings"

	"skygate/internal/prefixowner"
)

// relaysToKeepSynced returns the relay hostnames that must be visited even though the
// caller's rule-derived list does not mention them: every relay with a recorded route
// application, plus every relay the assignment table currently gives a prefix to.
//
// `already` is the caller's own set (rule-derived relays, or relays it has handled in
// an earlier loop), so no relay is synced twice in one pass.
func (s *Service) relaysToKeepSynced(already map[string]bool) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || already[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	// (a) every relay skygate has applied routes to at least once.
	if rows, err := s.dbc().Query(
		`SELECT key FROM global_settings WHERE key LIKE $1`, SettingRelayApplyStatePrefix+"%"); err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				add(strings.TrimPrefix(key, SettingRelayApplyStatePrefix))
			}
		}
		rows.Close()
	}

	// (b) every relay the assignment table names — including one that owns nothing but
	// is still recorded (covered by (a)); this half keeps a relay that owns prefixes
	// alive even if its apply record was never written (a pre-B309 deployment).
	if rows, err := s.dbc().Query(`SELECT DISTINCT exit_node_id FROM prefix_owner`); err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				add(name)
			}
		}
		rows.Close()
	}
	sort.Strings(out)
	return out
}

// ownedPrefixesForNodeFromTable returns the prefixes the PERSISTED assignment table
// hands to `node` — the authoritative answer for a relay whose rules are gone, where
// the in-memory claim map has nothing to say. An empty result is a valid answer: it
// means "this relay must advertise nothing beyond the base routes".
func (s *Service) ownedPrefixesForNodeFromTable(node string) []string {
	tbl := prefixowner.OwnerByPrefix(s.dbc())
	if len(tbl) == 0 {
		return nil
	}
	return OwnedPrefixesForRelay(node, tbl)
}

// hasRelayApplyRecord reports whether skygate has ever applied routes to this relay.
// It is what makes "no rules target this node" a reason to PRUNE rather than to skip.
func (s *Service) hasRelayApplyRecord(node string) bool {
	return RelayApplyStateOf(s.dbc(), node).At > 0
}
