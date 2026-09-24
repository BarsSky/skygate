// internal/feature/exit_rules/location_b312.go — B312 (v1.5.77).
//
// WHY (2026-09-23, operator request): «если доступ никак нельзя восстановить, то
// необходимо компенсировать правила по другим exit node, но предлагаю приоритет
// расставлять на сходный признак расположения по exit node, и как отдельная фича в
// exit node также отображать локацию где расположен exit node».
//
// Two halves, one fact:
//
//  1. WHERE each relay is — detected once per refresh window from the relay's public
//     address and stored in exit_servers (V077), shown on /admin/exit-nodes, editable
//     by hand (a manual value is never overwritten). The lookup is optional and
//     offline-safe: `SKYGATE_GEO_LOOKUP=off` disables it, and a failure only means
//     "still unknown".
//  2. WHEN an owner becomes unreachable, its prefixes are given to the CLOSEST
//     healthy relay instead of to whichever one the engine happened to visit first:
//     same city beats same country, same country beats a known-different place, and
//     distance in kilometres breaks the tie inside a tier. That is the "priority by
//     location" the operator asked for, and it is data-driven — a relay with no
//     location never gains or loses a preference it cannot justify.
package exit_rules

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/geoloc"
)

// ExitLocationRefresh is how long a detected location is trusted before it is looked
// up again. Thirty days: a relay does not move, but a bad first answer (or a provider
// that moved the box) should not be permanent.
const ExitLocationRefresh = 30 * 24 * time.Hour

var (
	locationRefreshMu   = make(chan struct{}, 1)
	locationRefreshLast time.Time
)

// locationRefreshInterval bounds how often the automatic lookup runs at all, so the
// sync tick (minutes) cannot turn into a lookup loop. Twelve hours is far more often
// than a relay moves and far less often than an API would care about.
const locationRefreshInterval = 12 * time.Hour

// resetLocationRefreshForTest clears the 12h guard so a test can make two passes look
// like two different days.
func resetLocationRefreshForTest() {
	locationRefreshLast = time.Time{}
}

// RefreshExitNodeLocations fills the location of every due relay. Best effort by
// design: a disabled lookup, a DNS failure, an API timeout and an API refusal all log
// once and leave the relay "unknown" — nothing here may fail a sync tick.
func (s *Service) RefreshExitNodeLocations() int {
	if s == nil || s.dbc() == nil {
		return 0
	}
	// One lookup pass at a time, and not more often than locationRefreshInterval.
	select {
	case locationRefreshMu <- struct{}{}:
	default:
		return 0
	}
	defer func() { <-locationRefreshMu }()
	if !locationRefreshLast.IsZero() && time.Since(locationRefreshLast) < locationRefreshInterval {
		return 0
	}

	d := s.dbc()
	now := time.Now().Unix()
	due := db.ExitServersDueForLocation(d, now, int64(ExitLocationRefresh.Seconds()))
	if len(due) == 0 {
		return 0
	}
	if geoloc.LookupDisabled() {
		log.Printf("exit-node location: %d relay(s) are due but the lookup is disabled (SKYGATE_GEO_LOOKUP) — set the location by hand on /admin/exit-nodes to use location-priority failover", len(due))
		locationRefreshLast = time.Now()
		return 0
	}
	updated, failed := 0, 0
	for _, c := range due {
		addr := geoloc.AddressForLookup(sshHostOf(c.SSHTarget))
		if addr == "" {
			// No public address to ask about (a tailnet/LAN target and no DNS answer).
			// Record the attempt so the next pass does not retry it immediately.
			_ = db.SetExitLocationAuto(d, c.Hostname, "", "", "", "", now)
			failed++
			continue
		}
		loc, err := geoloc.Lookup(context.Background(), addr)
		if err != nil {
			log.Printf("exit-node location(%s): %v — leaving the location unknown (address %s)", c.Hostname, err, addr)
			_ = db.SetExitLocationAuto(d, c.Hostname, "", "", "", "", now)
			failed++
			continue
		}
		lat, lon := geoloc.FormatCoords(loc.Lat, loc.Lon, loc.HasCoords)
		if err := db.SetExitLocationAuto(d, c.Hostname, loc.Label, loc.Country, lat, lon, now); err != nil {
			log.Printf("exit-node location(%s): cannot store the detected location: %v", c.Hostname, err)
			failed++
			continue
		}
		log.Printf("exit-node location(%s): %s (from %s)", c.Hostname, loc.Label, addr)
		updated++
	}
	locationRefreshLast = time.Now()
	if updated > 0 || failed > 0 {
		log.Printf("exit-node location: %d updated, %d still unknown (lookup %s)", updated, failed, geoloc.EndpointLabel())
	}
	return updated
}

// sshHostOf extracts the host part of an `[user@]host[:port]` target.
func sshHostOf(target string) string {
	_, host, _ := splitSSHEndpoint(target)
	if strings.TrimSpace(host) == "" {
		// The target may be a bare hostname without a user/port.
		return strings.TrimSpace(target)
	}
	return host
}

// LocationOf reads one relay's location as the assignment engine needs it.
func LocationOf(d *sql.DB, relay string) geoloc.Location {
	row, err := db.GetExitLocation(d, relay)
	if err != nil || !row.Known() {
		// The stored hostname may differ in case from the relay name used by the rules.
		for name, l := range db.ListExitLocations(d) {
			if strings.EqualFold(name, relay) && l.Known() {
				row = l
				break
			}
		}
		if !row.Known() {
			return geoloc.Location{}
		}
	}
	lat, lon, has := geoloc.ParseCoords(row.LatRaw, row.LonRaw)
	return geoloc.Location{Label: row.Label, Country: row.Country, Lat: lat, Lon: lon, HasCoords: has}
}

// NearestHealthyRelay picks the healthy relay closest to `from` (B312). It returns ""
// when it has no reason to prefer anyone, and the caller then keeps the engine's own
// choice — a preference that cannot be justified from data must not silently steer the
// assignment.
//
// Order: same city/label > same country > smaller great-circle distance > "".
func NearestHealthyRelay(from string, candidates []string, locOf func(string) geoloc.Location) string {
	if len(candidates) == 0 {
		return ""
	}
	origin := locOf(from)
	if origin.Empty() && !origin.HasCoords {
		return ""
	}
	best, bestTier, bestDist := "", -1, -1.0
	for _, c := range candidates {
		if strings.EqualFold(c, from) {
			continue
		}
		loc := locOf(c)
		tier := geoloc.SameArea(origin, loc)
		dist := geoloc.DistanceKM(origin, loc)
		switch {
		case tier > bestTier:
			best, bestTier, bestDist = c, tier, dist
		case tier == bestTier && tier > geoloc.AreaUnknown && dist >= 0 && (bestDist < 0 || dist < bestDist):
			best, bestDist = c, dist
		}
	}
	if bestTier <= geoloc.AreaUnknown {
		return ""
	}
	return best
}

// nearestRelayPreference is the callback prefixowner's assignment uses: for a prefix
// whose previous owner is unreachable, name the closest healthy relay (or "" to leave
// the decision to the engine).
func (s *Service) nearestRelayPreference() func(prefix, previousOwner string, candidates []string) string {
	return func(prefix, previousOwner string, candidates []string) string {
		d := s.dbc()
		if d == nil || previousOwner == "" || len(candidates) == 0 {
			return ""
		}
		cache := map[string]geoloc.Location{}
		locOf := func(relay string) geoloc.Location {
			if l, ok := cache[relay]; ok {
				return l
			}
			l := LocationOf(d, relay)
			cache[relay] = l
			return l
		}
		chosen := NearestHealthyRelay(previousOwner, candidates, locOf)
		if chosen == "" {
			return ""
		}
		log.Printf("prefix-owner: %s moves to %s — the previous owner %s is unreachable and %s is the closest healthy relay (%s)",
			prefix, chosen, previousOwner, chosen, describeLocation(locOf(chosen)))
		return chosen
	}
}

// describeLocation renders a stored location for the log ("Frankfurt am Main, Germany").
func describeLocation(l geoloc.Location) string {
	parts := []string{}
	if strings.TrimSpace(l.City) != "" {
		parts = append(parts, l.City)
	}
	if strings.TrimSpace(l.Country) != "" {
		parts = append(parts, l.Country)
	}
	if len(parts) == 0 {
		if strings.TrimSpace(l.Label) != "" {
			return l.Label
		}
		return "location unknown"
	}
	return strings.Join(parts, ", ")
}
