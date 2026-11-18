// exitpin_prefs.go — the thin adapters between the database rows and the pure
// rules in exitpin.go. Kept apart so the rules stay testable as a table.
package exitpin

import (
	"strings"

	"skygate/internal/db"
)

// FromDevice adapts a `device_exit_node_prefs` row.
func FromDevice(p db.DeviceExitNodePref) Pref {
	return Pref{
		Tag:         strings.TrimSpace(p.ExitNodeTag),
		ViaEnabled:  p.ViaEnabled,
		SetByUserID: p.SetByUserID,
		OptedOut:    p.OptedOut,
	}
}

// FromUser adapts a `user_exit_node_prefs` row.
func FromUser(p db.ExitNodePref) Pref {
	return Pref{
		Tag:         strings.TrimSpace(p.ExitNodeTag),
		ViaEnabled:  p.ViaEnabled,
		SetByUserID: p.SetByUserID,
	}
}

// RelayStates indexes the exit-node monitor's snapshot by lowercased hostname.
// The ACL resolves a preference tag to that hostname and reads the same table
// (see acl/acl_relay_health_b356.go), so the page and the policy answer with one
// measurement instead of two guesses.
func RelayStates(rows []db.ExitNodeHealth) map[string]string {
	out := make(map[string]string, len(rows))
	for _, h := range rows {
		host := strings.ToLower(strings.TrimSpace(h.Hostname))
		if host == "" {
			continue
		}
		out[host] = h.State
	}
	return out
}
