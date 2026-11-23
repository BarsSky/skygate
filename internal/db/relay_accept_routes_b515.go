package db

import (
	"database/sql"
	"strings"
)

// relay_accept_routes_b515.go — B515.1 (2026-11-22)
//
// WHERE THE PAGE GETS ITS EVIDENCE. The operator learned about the accept-routes loop only by
// measuring interface counters on two relays during one download; B514 stopped the loop, and
// B515.1 must make the state VISIBLE. Rendering it by SSH-ing to every relay on each page load
// would make the admin page slow and would turn "relay unreachable right now" into a scary
// red row — the page would report the viewer's network, not the fleet.
//
// So the OBSERVED value is written by the sync pass that already visits each relay (it sets
// `--accept-routes=false` there and reads the result back), and the page only reads this
// store. The value is the relay's own answer, never an inference from what we asked for: an
// absent row means "nobody has looked yet", which the page renders as unknown, not as fine.
const relayAcceptRoutesPrefix = "relay_accept_routes:"

// SetRelayAcceptRoutesObserved records what a relay reported for its RouteAll preference.
// `value` is the raw "true"/"false" the relay answered (or an error text the caller chose to
// keep); the page classifies it, so this layer must not normalise a failure into "false".
func SetRelayAcceptRoutesObserved(d *sql.DB, relay, value string) error {
	if d == nil || relay == "" {
		return nil
	}
	return SetGlobalSetting(d, relayAcceptRoutesPrefix+relay, value)
}

// RelayAcceptRoutesObserved returns relay hostname → the last observed raw value.
// A missing key is simply absent from the map: "unknown" is a state, not an error.
func RelayAcceptRoutesObserved(d *sql.DB) map[string]string {
	out := map[string]string{}
	if d == nil {
		return out
	}
	rows, err := d.Query("SELECT key, value FROM global_settings WHERE key LIKE ?",
		relayAcceptRoutesPrefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[strings.TrimPrefix(k, relayAcceptRoutesPrefix)] = v
		}
	}
	return out
}
