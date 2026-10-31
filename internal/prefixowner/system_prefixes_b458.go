package prefixowner

// system_prefixes_b458.go — B458 (2026-10-31). «DNS внутри tailnet должен ехать по
// проекту, а не случайно».
//
// THE MEASURED DEFECT (reference install, 2026-10-31): headscale hands every client
// `dns.nameservers.global: [1.1.1.1, 8.8.8.8]`, but the assignment table only carried
// `8.8.8.0/24` + `8.8.4.0/24` — and only because a USER RULE happens to claim `8.8.8.8`.
// `1.1.1.1` had no prefix at all, so half of every client's DNS left the tailnet (and on a
// filtered network it did not leave at all). Deleting that one rule would take DNS away
// from the whole tailnet: a system setting carried by a data-plane coincidence.
//
// A system prefix is a prefix the INFRASTRUCTURE requires, not one a user asked for. It is
// stored in the same table (so it is assigned, advertised, pinned and displayed by exactly
// the same machinery — no second source of truth), with source='system':
//
//	manual   — the operator pinned THIS prefix (wins over everything);
//	explicit — the rules' majority named a relay;
//	auto     — the engine spread it;
//	system   — the tailnet needs it (DNS resolvers today); a manual pin still wins, and a
//	           user rule that claims the same prefix wins too, because a rule is evidence
//	           about where traffic goes and this is only "somebody must advertise it".
//
// `Prune` keeps system rows, and EnsureSystemPrefixes is what removes one whose reason is
// gone (the resolver is no longer configured): the alternative — letting Prune delete it
// every pass and re-adding it from the sync — would churn the row and flap the advertised
// route on a five-minute cycle.

import (
	"database/sql"
	"log"
	"sort"
	"strings"
)

// SystemSource is the prefix_owner.source value for infrastructure-required prefixes.
const SystemSource = "system"

// SystemRows returns the prefixes this table carries on behalf of the infrastructure.
func SystemRows(d *sql.DB) []string {
	if d == nil {
		return nil
	}
	rows, err := d.Query(`SELECT prefix FROM prefix_owner WHERE COALESCE(source,'') = $1 ORDER BY prefix`, SystemSource)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// EnsureSystemPrefixes makes the table carry exactly `prefixes` as system-owned.
//
// It returns what it added and what it removed so the caller can journal it: a prefix that
// appears or disappears on the wire is a routing change, and routing changes that nobody
// logs are how the accident this block removes happened in the first place.
//
// An EMPTY healthy set is a no-op: without a relay there is nobody to own the prefix, and
// inventing an owner would point clients at a route that does not exist.
func EnsureSystemPrefixes(d *sql.DB, prefixes, healthy []string, w Weights) (added, removed []string, err error) {
	if d == nil {
		return nil, nil, nil
	}
	want := map[string]bool{}
	for _, p := range prefixes {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}
	if len(want) == 0 && len(prefixes) == 0 {
		return nil, nil, nil
	}
	// Existing rows, so a system row that is no longer configured can go and a prefix the
	// rules already own is never overwritten.
	have := map[string]string{}
	load := map[string]int{}
	rows, qerr := d.Query(`SELECT prefix, COALESCE(source,'auto'), COALESCE(exit_node_id,'') FROM prefix_owner`)
	if qerr != nil {
		return nil, nil, qerr
	}
	for rows.Next() {
		var p, src, owner string
		if rows.Scan(&p, &src, &owner) == nil {
			have[p] = src
			if owner != "" {
				load[owner]++
			}
		}
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return nil, nil, rerr
	}

	// Remove system rows whose reason is gone. This is the only place a system row dies.
	var gone []string
	for p, src := range have {
		if src == SystemSource && !want[p] {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		if _, derr := d.Exec(`DELETE FROM prefix_owner WHERE prefix = $1 AND COALESCE(source,'') = $2`, p, SystemSource); derr != nil {
			return added, removed, derr
		}
		removed = append(removed, p)
	}

	// Add the missing ones.
	var missing []string
	for p := range want {
		if _, ok := have[p]; !ok {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return added, removed, nil
	}
	if len(healthy) == 0 {
		return added, removed, nil
	}
	for _, p := range missing {
		owner := pickLeastRelative(healthy, load, w)
		if owner == "" {
			continue
		}
		if _, ierr := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
			VALUES ($1, $2, $3, 0, 0, $4)
			ON CONFLICT (prefix) DO NOTHING`, p, owner, SystemSource, nowUnix()); ierr != nil {
			return added, removed, ierr
		}
		load[owner]++
		added = append(added, p)
	}
	return added, removed, nil
}

// logSystemPrefixChange is one line of evidence per pass that changed what the tailnet
// carries on the operator's behalf.
func logSystemPrefixChange(added, removed []string) {
	if len(added) == 0 && len(removed) == 0 {
		return
	}
	if len(added) > 0 {
		log.Printf("prefix-owner: carrying %d infrastructure prefix(es) system-owned (DNS resolvers): %s", len(added), strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		log.Printf("prefix-owner: released %d infrastructure prefix(es) that are no longer configured: %s", len(removed), strings.Join(removed, ", "))
	}
}

// EnsureSystemPrefixesLogged is the call the sync path makes: ensure, then journal.
func EnsureSystemPrefixesLogged(d *sql.DB, prefixes, healthy []string, w Weights) error {
	added, removed, err := EnsureSystemPrefixes(d, prefixes, healthy, w)
	if err != nil {
		return err
	}
	logSystemPrefixChange(added, removed)
	return nil
}
