// acl_relay_health_b356.go — B356 (2026-10-07) — "is the relay this preference
// names able to carry anything right now?".
//
// WHY THIS EXISTS. Since B265 the per-device `autogroup:internet` grant carries
// `via=[<preferred tag>]` whenever the device has a resolved preference, and B188.2
// made sure that pinned grant REPLACES the loose one (grants are additive, so
// emitting both would leave the pin defeated). headscale applies `via` as a
// permission FILTER: the device may only use the relay named there for exit traffic.
//
// So "the preference exists" was silently treated as "the preference is usable", and
// the two are not the same question. Live on the reference deployment (2026-10-07):
// relays `karolina` and `sharlotta` went offline while four `device_exit_node_prefs`
// rows still named them; the ACL kept emitting
// `via: ["tag:dev-infra-karolina"]` for those devices, the loose grant was gone, and
// their internet egress was filtered into a black hole — until the preference
// reconciler (B356's other half) got around to re-pointing the row, and forever if it
// could not.
//
// This file is the SAFETY NET, not the fix: the ACL is generated before the
// reconciler converges (and the reconciler may legitimately refuse to act on an
// operator's own pin), so the grant itself must not name a relay that cannot serve.
// The device then falls back to the UNPINNED grant, which is exactly the pre-B265
// behaviour for a device without a preference.
//
// The predicate is `monitoring.ExitNodeUsable` — the SAME function the exit-node
// monitor derives `Healthy` from and B273 made the single source of truth for "can a
// device route internet through this relay right now?". Re-deriving it here (say
// `state == "online"`) would be a second health predicate and would read a working
// untagged relay as broken. A relay with NO health row is "never measured", which is
// not "broken": the pin is kept, because absence of evidence must not change a
// policy — the same rule the preference reconciler applies to the row.

package acl

import (
	"database/sql"
	"log"
	"strings"

	"skygate/internal/db"
	"skygate/internal/monitoring"
)

// relayVerdict is one relay's last health verdict, keyed by lowercased hostname.
type relayVerdict struct {
	Usable bool
	State  string
}

// relayVerdicts reads the exit-node monitor's snapshot once per policy generation.
//
// A read failure returns nil: the caller then treats every preference as "unknown"
// and keeps every pin, which is the pre-B356 document. That is deliberate — a
// generation that cannot answer a question must not answer it with "unusable", or a
// transient DB hiccup would silently unpin the whole tailnet.
func relayVerdicts(d *sql.DB) map[string]relayVerdict {
	rows, err := db.ListExitNodeHealth(d)
	if err != nil {
		log.Printf("acl: B356 cannot read exit_node_health (%v) — every per-device exit pin is kept as-is; the preference reconciler is the only protection this pass", err)
		return nil
	}
	out := make(map[string]relayVerdict, len(rows))
	for _, h := range rows {
		host := strings.ToLower(strings.TrimSpace(h.Hostname))
		if host == "" {
			continue
		}
		out[host] = relayVerdict{Usable: monitoring.ExitNodeUsable(h.State), State: h.State}
	}
	return out
}

// unusablePreferredRelay reports (state, true) when the preference names a relay the
// monitor has measured and found NOT usable. It reports ("", false) both for a
// usable relay and for one with no health row — the two cases where the pin must
// stand.
//
// The tag → hostname step is `exitNodeTagToHostname`, the same helper the per-CIDR
// pin uses (B188.2/B279.1), so a class tag or an unknown tag shape answers "" and
// leaves the pin alone rather than guessing a hostname.
func unusablePreferredRelay(prefTag string, verdicts map[string]relayVerdict) (string, bool) {
	if prefTag == "" || len(verdicts) == 0 {
		return "", false
	}
	host := strings.ToLower(strings.TrimSpace(exitNodeTagToHostname(prefTag)))
	if host == "" {
		return "", false
	}
	v, known := verdicts[host]
	if !known || v.Usable {
		return "", false
	}
	return v.State, true
}
