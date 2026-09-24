// Package prefixowner — B275: which relay serves which prefix.
//
// headscale serves a subnet prefix from exactly ONE relay (the primary),
// so per-device "this prefix via my own exit node" is not expressible.
// The previous model attached `exit_node_id` to every rule and let each
// relay advertise its own rules; two devices of different users on
// different relays therefore collided on every shared CIDR (live: 28
// Cloudflare/Google ranges claimed by both `basic`→emilia and
// `skyworker`→karolina), and the primary flapped between sync passes —
// `skyworker` lost rutracker/Cloudflare whenever emilia held it.
//
// B275 moves the decision into skygate and makes it explicit, stable and
// operator-editable:
//
//	source='explicit' — the rules named this relay (majority wins,
//	                    hostname as the deterministic tie-break);
//	source='manual'   — an operator pinned it; the engine never
//	                    overwrites it (use SetManual to write one);
//	source='auto'     — the engine's own choice: the least-loaded
//	                    healthy relay, sticky (the previous owner keeps
//	                    the prefix while it is healthy and not
//	                    overloaded, so nothing migrates without a
//	                    reason).
//
// The relay advertises exactly its owned prefixes, and the per-CIDR ACL
// grant carries `via=[owner]` instead of `via=[rule's exit node]` — so a
// device whose rule names a relay that cannot serve the prefix still
// gets a working path, and the substitution is logged instead of
// silently half-working.
package prefixowner

import (
	"database/sql"
	"log"
	"sort"
	"strings"

	"skygate/internal/db"
)

// Claim is one "this device's rule asks for this prefix" statement.
type Claim struct {
	Prefix   string
	ExitNode string // the rule's exit_node_id; "" = the user expressed no preference
	DeviceID int
}

// Existing is a row already stored in prefix_owner.
type Existing struct {
	Prefix   string
	ExitNode string
	Source   string
}

// Assignment is the engine's decision for one prefix.
type Assignment struct {
	Prefix   string
	ExitNode string
	Source   string // explicit | manual | auto
	Claims   int
	Devices  int
}

// Assign computes the prefix→relay table.
//
// healthy is the set of relay hostnames that may serve traffic right now
// (an unhealthy owner loses the prefix, even a manual one — a pinned
// relay that is down cannot carry anything, and the prefix is reported
// as reassigned rather than black-holed).
func Assign(claims []Claim, healthy []string, existing []Existing) []Assignment {
	return AssignWithPreference(claims, healthy, existing, nil)
}

// PreferFunc lets the caller steer the fallback for ONE prefix whose previous owner
// can no longer serve it (B312: prefer the relay closest to the one that was lost).
//
// It is called only in the auto pass, only when the previous owner is NOT healthy
// (or is too loaded) and the prefix is therefore being reassigned — never for a
// manual pin, never for a prefix that is staying put, and never to override the
// explicit majority. Returning "" (or a name outside `healthy`) leaves the decision to
// the engine, so a preference that cannot be justified from data changes nothing.
type PreferFunc func(prefix, previousOwner string, candidates []string) string

// AssignWithPreference is Assign with the B312 location preference. Existing callers
// keep using Assign; the engine's own order (least loaded, sticky) stays the default
// and the final word whenever the preference names nobody usable.
func AssignWithPreference(claims []Claim, healthy []string, existing []Existing, prefer PreferFunc) []Assignment {
	healthySet := map[string]bool{}
	for _, h := range healthy {
		if h != "" {
			healthySet[h] = true
		}
	}
	// Group the claims per prefix.
	type group struct {
		perNode map[string]int
		devices map[int]bool
		total   int
	}
	groups := map[string]*group{}
	for _, c := range claims {
		if c.Prefix == "" {
			continue
		}
		g := groups[c.Prefix]
		if g == nil {
			g = &group{perNode: map[string]int{}, devices: map[int]bool{}}
			groups[c.Prefix] = g
		}
		g.total++
		g.devices[c.DeviceID] = true
		if c.ExitNode != "" {
			g.perNode[c.ExitNode]++
		}
	}
	prev := map[string]Existing{}
	for _, e := range existing {
		prev[e.Prefix] = e
	}

	// Load per relay, so 'auto' spreads evenly. The explicit/manual
	// choices are placed first and count toward the load.
	load := map[string]int{}
	out := make([]Assignment, 0, len(groups))

	prefixes := make([]string, 0, len(groups))
	for p := range groups {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	// Pass 1 — manual (the operator's pin, kept while its relay is healthy).
	// Pass 2 — explicit (majority of the rules that named a relay).
	// Pass 3 — auto (least loaded healthy relay, sticky to the previous
	//          owner while it is not overloaded).
	decided := map[string]bool{}
	chooseExplicit := func(p string, g *group) string {
		best, bestN := "", 0
		for node, n := range g.perNode {
			if !healthySet[node] {
				continue
			}
			if n > bestN || (n == bestN && node < best) {
				best, bestN = node, n
			}
		}
		return best
	}
	// Pass 1 — manual (the operator's pin, kept while its relay is healthy).
	//
	// B277 FIX: this pass used to run AFTER the explicit one, and the explicit pass
	// marks its prefixes "decided" — so a manual pin was ignored whenever the rules
	// named a relay, which is every prefix that exists (a prefix is in this table
	// because a rule claimed it). The advertised contract — "a source='manual'
	// operator pin is never overwritten while its relay is healthy" — was therefore
	// false in the normal case, and the UI's «Закрепить за» silently reverted on the
	// next pass. Manual is the operator speaking about ONE prefix, so it is decided
	// first: manual > explicit > auto (`Reconcile` inserts the global switch between
	// manual and explicit).
	for _, p := range prefixes {
		if e, ok := prev[p]; ok && e.Source == "manual" && healthySet[e.ExitNode] {
			g := groups[p]
			out = append(out, Assignment{Prefix: p, ExitNode: e.ExitNode, Source: "manual", Claims: g.total, Devices: len(g.devices)})
			load[e.ExitNode]++
			decided[p] = true
		}
	}
	// Pass 2 — explicit (majority of the rules that named a relay).
	for _, p := range prefixes {
		if decided[p] {
			continue
		}
		g := groups[p]
		if node := chooseExplicit(p, g); node != "" {
			out = append(out, Assignment{Prefix: p, ExitNode: node, Source: "explicit", Claims: g.total, Devices: len(g.devices)})
			load[node]++
			decided[p] = true
		}
	}
	// Candidate relays for auto: the healthy set, or — when nothing is
	// marked healthy (headscale unreachable) — the relays the rules
	// named, so the table never ends up empty.
	candidates := make([]string, 0, len(healthySet))
	for h := range healthySet {
		candidates = append(candidates, h)
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		seen := map[string]bool{}
		for _, p := range prefixes {
			for node := range groups[p].perNode {
				if !seen[node] {
					seen[node] = true
					candidates = append(candidates, node)
				}
			}
		}
		sort.Strings(candidates)
	}
	minLoad := func() int {
		best := -1
		for _, c := range candidates {
			if best < 0 || load[c] < best {
				best = load[c]
			}
		}
		return best
	}
	for _, p := range prefixes {
		if decided[p] {
			continue
		}
		g := groups[p]
		// Sticky: keep the previous owner while it is healthy and its
		// load is within one prefix of the least loaded relay.
		if e, ok := prev[p]; ok && healthySet[e.ExitNode] {
			if m := minLoad(); load[e.ExitNode] <= m+1 {
				out = append(out, Assignment{Prefix: p, ExitNode: e.ExitNode, Source: "auto", Claims: g.total, Devices: len(g.devices)})
				load[e.ExitNode]++
				continue
			}
		}
		if len(candidates) == 0 {
			continue
		}
		// B312: when this prefix is being REASSIGNED (its previous owner is gone or
		// overloaded), let the caller name the closest healthy relay. Only a name that
		// is actually in the healthy set is honoured, so a preference can never pin a
		// prefix to a relay that is down.
		if e, ok := prev[p]; ok && e.ExitNode != "" && !healthySet[e.ExitNode] && prefer != nil {
			if want := prefer(p, e.ExitNode, candidates); want != "" && healthySet[want] {
				out = append(out, Assignment{Prefix: p, ExitNode: want, Source: "auto", Claims: g.total, Devices: len(g.devices)})
				load[want]++
				continue
			}
		}
		best := candidates[0]
		for _, c := range candidates {
			if load[c] < load[best] {
				best = c
			}
		}
		out = append(out, Assignment{Prefix: p, ExitNode: best, Source: "auto", Claims: g.total, Devices: len(g.devices)})
		load[best]++
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// LoadClaims reads every enabled ip/subnet rule as a claim — ONE claim per
// (prefix, relay, device) triple (B276).
//
// The GROUP BY is load-bearing, not cosmetic. A single domain rule is expanded
// by the CDN resolver into many derived rows that can share one CIDR (one row per
// parent domain) — live on the reference host the auto-updater wrote ~145 rows and
// re-deduplicated ~110 every five minutes, and `basic` carried five
// `104.16.0.0/12` rows for one device. Assign() casts the claim COUNTS as the
// operator's vote ("explicit majority wins"), so counting rows let the updater's
// churn decide which relay owns a prefix: the same two devices could produce a
// 5:1 majority for one relay and a 1:1 tie (broken by hostname) for the other
// depending on where in its tick the dedup happened. Since B275 turns the owner
// into the per-CIDR ACL pin, a churn-driven flip silently invalidates every pin
// for that prefix, so the vote must be per DEVICE.
func LoadClaims(d *sql.DB) ([]Claim, error) {
	rows, err := d.Query(`SELECT target_value, COALESCE(exit_node_id,''), COALESCE(device_id,0)
	                     FROM device_rules WHERE enabled = 1 AND target_type IN ('ip','subnet')
	                     GROUP BY target_value, COALESCE(exit_node_id,''), COALESCE(device_id,0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Claim
	for rows.Next() {
		var c Claim
		if err := rows.Scan(&c.Prefix, &c.ExitNode, &c.DeviceID); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LoadExisting reads the current table.
func LoadExisting(d *sql.DB) ([]Existing, error) {
	rows, err := d.Query(`SELECT prefix, exit_node_id, COALESCE(source,'auto') FROM prefix_owner`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Existing
	for rows.Next() {
		var e Existing
		if err := rows.Scan(&e.Prefix, &e.ExitNode, &e.Source); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Save upserts the assignment table and returns (inserted, changed).
func Save(d *sql.DB, as []Assignment) (int, int, error) {
	prev, err := LoadExisting(d)
	if err != nil {
		return 0, 0, err
	}
	before := map[string]string{}
	for _, e := range prev {
		before[e.Prefix] = e.ExitNode
	}
	ins, chg := 0, 0
	for _, a := range as {
		if old, ok := before[a.Prefix]; !ok {
			ins++
		} else if old != a.ExitNode {
			chg++
		}
		if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
		                     VALUES ($1,$2,$3,$4,$5,$6)
		                     ON CONFLICT(prefix) DO UPDATE SET
		                       exit_node_id = excluded.exit_node_id,
		                       source = excluded.source,
		                       claims = excluded.claims,
		                       devices = excluded.devices,
		                       updated_at = excluded.updated_at`,
			a.Prefix, a.ExitNode, a.Source, a.Claims, a.Devices, nowUnix()); err != nil {
			return ins, chg, err
		}
	}
	return ins, chg, nil
}

// SetManual pins one prefix to a relay by hand (source='manual', which
// Assign never overwrites while the relay is healthy). An empty relay
// deletes the row so the engine takes the prefix back.
func SetManual(d *sql.DB, prefix, exitNode string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return sql.ErrNoRows
	}
	if exitNode == "" {
		_, err := d.Exec(`DELETE FROM prefix_owner WHERE prefix = $1`, prefix)
		return err
	}
	_, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
	                  VALUES ($1,$2,'manual',0,0,$3)
	                  ON CONFLICT(prefix) DO UPDATE SET exit_node_id = excluded.exit_node_id,
	                    source = 'manual', updated_at = excluded.updated_at`,
		prefix, exitNode, nowUnix())
	return err
}

// TagsByHost maps a relay hostname to its headscale tag using the
// node_owner_map projection (the same source the ACL generator uses for
// device tags).
func TagsByHost(d *sql.DB) map[string]string {
	rows, err := d.Query(`SELECT LOWER(COALESCE(hostname,'')), COALESCE(tag,'') FROM node_owner_map`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, t string
		if rows.Scan(&h, &t) == nil && h != "" && t != "" {
			out[h] = t
		}
	}
	return out
}

// TagByPrefix returns prefix → owner tag, the shape the ACL generator
// needs for `via=[owner]`. Prefixes without an assignment are absent, so
// the caller can fall back to the rule's own exit node.
func TagByPrefix(d *sql.DB) map[string]string {
	rows, err := d.Query(`SELECT prefix, exit_node_id FROM prefix_owner`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byHost := TagsByHost(d)
	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if rows.Scan(&p, &h) != nil || p == "" || h == "" {
			continue
		}
		if tag := byHost[strings.ToLower(h)]; tag != "" {
			out[p] = tag
		}
	}
	return out
}

// OwnerByPrefix returns prefix → owning relay hostname.
func OwnerByPrefix(d *sql.DB) map[string]string {
	rows, err := d.Query(`SELECT prefix, exit_node_id FROM prefix_owner`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if rows.Scan(&p, &h) == nil && p != "" && h != "" {
			out[p] = h
		}
	}
	return out
}

// ViaForPrefix returns the `via` tag for a CIDR rule target when the
// assignment table knows the owner. B275: the per-CIDR ACL pin must name
// the relay that CAN serve the prefix, not the relay the rule happens to
// name — otherwise a device whose rule lost the ownership contest is
// simply blocked (live: skyworker's rutracker timed out because its
// grant said via=karolina while emilia held the primary).
func ViaForPrefix(target string, tagByPrefix map[string]string) string {
	if target == "" || len(tagByPrefix) == 0 {
		return ""
	}
	return tagByPrefix[target]
}

// Reconcile is the one call the sync path makes: read the rules, the
// relay health and the current table, then write the new assignment.
// healthyRelays may be empty (headscale unreachable), in which case the
// engine falls back to the relays the rules named.
func Reconcile(d *sql.DB, healthyRelays []string) (inserted, changed int, err error) {
	return ReconcileWithPreference(d, healthyRelays, nil)
}

// ReconcileWithPreference is Reconcile with the B312 fallback preference (see
// PreferFunc): when a prefix's owner can no longer serve it, `prefer` may name the
// closest healthy relay instead of letting the engine pick the least loaded one.
func ReconcileWithPreference(d *sql.DB, healthyRelays []string, prefer PreferFunc) (inserted, changed int, err error) {
	claims, err := LoadClaims(d)
	if err != nil {
		return 0, 0, err
	}
	existing, err := LoadExisting(d)
	if err != nil {
		return 0, 0, err
	}
	as := AssignWithPreference(claims, healthyRelays, existing, prefer)

	// B277: the global "everything through one relay" switch, and the manual pins
	// that deliberately survive it. Order of authority: manual (the operator picked
	// THIS prefix) > global (the operator picked a relay for everything) > explicit
	// (the rules' majority) > auto.
	if force := ForceRelay(d); force != "" {
		if relayIsHealthy(force, healthyRelays) {
			for i := range as {
				if as[i].Source == "manual" {
					continue
				}
				as[i].ExitNode = force
				as[i].Source = "global"
			}
		} else {
			log.Printf("prefix-owner: global override relay %q is not healthy — ignoring it (the whole tailnet would lose egress)", force)
		}
	}

	// B277: drop rows whose prefix no rule claims any more. The table used to keep
	// every prefix it had ever seen (live: 1655 rows, 1497 of them dead — the page
	// was unreadable and the "nobody announces" counter described history, not the
	// network). Manual pins are deliberately kept: an operator pin is an intent that
	// may well predate the rule that will use it.
	if n, perr := Prune(d, claimedPrefixes(claims)); perr != nil {
		log.Printf("prefix-owner: prune: %v", perr)
	} else if n > 0 {
		log.Printf("prefix-owner: pruned %d assignment row(s) whose prefix no enabled rule claims any more", n)
	}

	return Save(d, as)
}

// forceRelaySettingKey is the global_settings row the override lives in.
const forceRelaySettingKey = "prefix_owner_force_relay"

// ForceRelay reads the operator's global override (B277): when set, every prefix
// that is not manually pinned is served by this relay. Empty means "no override".
//
// It is a global SETTING rather than a mass write of manual rows on purpose: the
// operator flips the whole tailnet's egress with one control and reverts it with one
// control, it also covers prefixes that appear later, and a manual per-prefix pin
// still wins (that prefix was chosen deliberately, after the global switch).
func ForceRelay(d *sql.DB) string {
	v, err := db.GetGlobalSetting(d, forceRelaySettingKey, "")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// SetForceRelay stores (or clears, with an empty relay) the global override.
func SetForceRelay(d *sql.DB, relay string) error {
	return db.SetGlobalSetting(d, forceRelaySettingKey, strings.TrimSpace(relay))
}

// relayIsHealthy reports whether the relay may carry traffic right now.
func relayIsHealthy(relay string, healthy []string) bool {
	for _, h := range healthy {
		if strings.EqualFold(h, relay) {
			return true
		}
	}
	return false
}

// claimedPrefixes builds the set of prefixes some enabled rule claims.
func claimedPrefixes(claims []Claim) map[string]bool {
	out := make(map[string]bool, len(claims))
	for _, c := range claims {
		if c.Prefix != "" {
			out[c.Prefix] = true
		}
	}
	return out
}

// Prune deletes assignment rows whose prefix is no longer claimed by an enabled
// rule, keeping every manual pin. Returns how many rows it removed (B277).
func Prune(d *sql.DB, claimed map[string]bool) (int, error) {
	rows, err := d.Query(`SELECT prefix, COALESCE(source,'auto') FROM prefix_owner`)
	if err != nil {
		return 0, err
	}
	var stale []string
	for rows.Next() {
		var prefix, source string
		if err := rows.Scan(&prefix, &source); err != nil {
			continue
		}
		if source == "manual" || claimed[prefix] {
			continue
		}
		stale = append(stale, prefix)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	deleted := 0
	for _, prefix := range stale {
		res, derr := d.Exec(`DELETE FROM prefix_owner WHERE prefix = $1 AND COALESCE(source,'auto') <> 'manual'`, prefix)
		if derr != nil {
			return deleted, derr
		}
		if n, _ := res.RowsAffected(); n > 0 {
			deleted += int(n)
		}
	}
	return deleted, nil
}
