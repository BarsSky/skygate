// migrate_internal.go — the panel actions a migration plan can name (B483).
//
// The plan's "internal" steps are not shell commands: they are questions about THIS installation —
// does headscale see the nodes, did the tags survive, does the live policy equal the generated one,
// does every relay advertise exactly the prefixes it owns, does the relay answer a DERP upgrade.
// The plan carries their NAMES (internal_nodes_online, …) and this file is where those names get an
// implementation; `migrate.RunStep` resolves them through `Deps.Internal` and reports an unwired
// name as PENDING, never as a host failure.
//
// Everything here is READ-ONLY by design, with one exception that is also read-only in disguise: the
// relay row (internal_derp_relay_upsert) is not written by the wizard — registering a relay stays the
// existing /admin/derp/relays/init page (B164), and the step is a MANUAL step that points at it. A
// wizard that silently invents a region id and a URL is how an installation ends up with a relay
// nobody can find.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/derphealth"
	"skygate/internal/headscale"
	"skygate/internal/migrate"
)

// migrateInternalActions wires every name that internal/migrate/plan.go can put into a step.
//
// The run is captured because one action needs its answers (the DERP probe asks about the relay the
// operator is moving). A name that is not in this map is reported as pending by the executor, so
// adding a step to the plan without adding it here is visible rather than silent.
func (s *Service) migrateInternalActions(run *db.HostMigration) map[string]migrate.InternalFunc {
	return map[string]migrate.InternalFunc{
		"internal_nodes_online":      s.migrateActionNodesOnline,
		"internal_tags_present":      s.migrateActionTagsPresent,
		"internal_policy_equivalent": s.migrateActionPolicyEquivalent,
		"internal_routes_converged":  s.migrateActionRoutesConverged,
		"internal_derp_probe":        s.migrateActionDerpProbe(run),
		"external_derp_probe_101":    s.migrateActionDerpProbe(run),
	}
}

// migrateActionNodesOnline answers "does the control plane see the tailnet?".
//
// It is deliberately NOT "are all nodes online": a cutover is finished long before every laptop has
// woken up, and failing on a sleeping phone would make the wizard unusable. What must be true is that
// the plane has nodes AND at least one is connected — that is what proves the new host's headscale is
// speaking to real clients rather than to an empty database.
func (s *Service) migrateActionNodesOnline(ctx context.Context) (string, error) {
	hs := s.HSGlobalFn()
	if hs == nil {
		return "", errors.New("the headscale client is not configured on this panel")
	}
	nodes, err := hs.ListAllNodesFresh()
	if err != nil {
		return "", fmt.Errorf("headscale nodes list: %w", err)
	}
	online := 0
	for _, n := range nodes {
		if n.Online {
			online++
		}
	}
	report := fmt.Sprintf("nodes=%d online=%d", len(nodes), online)
	if len(nodes) == 0 {
		return report, errors.New("headscale reports no nodes at all — it is either a fresh database or the wrong one")
	}
	if online == 0 {
		return report, errors.New("no node is online — clients did not reconnect to this control plane")
	}
	return report, nil
}

// migrateActionTagsPresent answers "did the tags survive the move?".
//
// This is the check the migration needed most and did not have: headscale drops every tag on an OIDC
// re-authentication (B477), and a device without its tag matches no per-device ACL rule — so the
// tailnet looks healthy while one device is silently outside the rules. node_owner_map says what tag a
// node SHOULD carry; headscale says what it does carry; the difference is the report.
func (s *Service) migrateActionTagsPresent(ctx context.Context) (string, error) {
	hs := s.HSGlobalFn()
	if hs == nil {
		return "", errors.New("the headscale client is not configured on this panel")
	}
	rows, err := s.dbc().Query(`SELECT node_id, COALESCE(hostname,''), COALESCE(tag,'') FROM node_owner_map`)
	if err != nil {
		return "", fmt.Errorf("node_owner_map: %w", err)
	}
	defer rows.Close()
	expected := map[string]string{} // node_id -> tag
	names := map[string]string{}    // node_id -> hostname
	for rows.Next() {
		var id, host, tag string
		if err := rows.Scan(&id, &host, &tag); err != nil {
			return "", fmt.Errorf("node_owner_map scan: %w", err)
		}
		if strings.TrimSpace(tag) != "" {
			expected[id] = tag
			names[id] = host
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(expected) == 0 {
		// No per-device tags are configured: there is nothing to lose, and saying "ok" is truthful.
		return "no per-device tags are configured on this installation", nil
	}
	nodes, err := hs.ListAllNodesFresh()
	if err != nil {
		return "", fmt.Errorf("headscale nodes list: %w", err)
	}
	have := map[string]map[string]bool{}
	byHost := map[string]map[string]bool{}
	for _, n := range nodes {
		set := map[string]bool{}
		for _, t := range n.Tags {
			set[t] = true
		}
		have[n.ID] = set
		byHost[strings.ToLower(n.Hostname)] = set
	}
	var missing []string
	ok := 0
	for id, tag := range expected {
		set, known := have[id]
		if !known {
			// The node may have re-registered with a new id; fall back to the hostname the map recorded.
			set = byHost[strings.ToLower(names[id])]
		}
		if set == nil {
			missing = append(missing, names[id]+" (node not found)")
			continue
		}
		if set[tag] {
			ok++
			continue
		}
		missing = append(missing, names[id]+" lacks "+tag)
	}
	report := fmt.Sprintf("tagged nodes ok=%d missing=%d", ok, len(missing))
	if len(missing) > 0 {
		return report + "; " + strings.Join(missing, ", "),
			fmt.Errorf("%d device(s) do not carry the tag their rules depend on — apply the tag or re-run the tag reconcile before switching DNS", len(missing))
	}
	return report, nil
}

// migrateActionPolicyEquivalent answers "is the live policy the one this panel generates?".
//
// After a move the panel can be running while headscale still serves the OLD policy — the ACL is
// pushed by a background pass, and B476 showed that a push can fail for minutes with the pages
// looking normal. Comparing the live document with a freshly generated one is the only honest answer,
// and it is exactly what /admin/exit-nodes flags.
func (s *Service) migrateActionPolicyEquivalent(ctx context.Context) (string, error) {
	hs := s.HSGlobalFn()
	if hs == nil {
		return "", errors.New("the headscale client is not configured on this panel")
	}
	live, err := hs.GetACL()
	if err != nil {
		return "", fmt.Errorf("headscale GET policy: %w", err)
	}
	generated, err := acl.GenerateACLWithVia(s.dbc())
	if err != nil {
		return "", fmt.Errorf("generate the ACL: %w", err)
	}
	same, cmpErr := headscale.PolicyEquivalent(live, generated)
	if cmpErr != nil {
		return "", fmt.Errorf("compare policies: %w", cmpErr)
	}
	report := fmt.Sprintf("live=%d bytes generated=%d bytes equivalent=%v", len(live), len(generated), same)
	if !same {
		return report, errors.New("the live policy differs from the one this panel generates — run skygate acl-apply and re-check before switching DNS")
	}
	return report, nil
}

// migrateActionRoutesConverged answers "does every relay advertise exactly the prefixes it owns?".
//
// headscale serves a prefix from exactly ONE relay (§B274), and the assignment lives in prefix_owner.
// A relay that advertises a prefix it does not own competes with the owner; a relay that is missing one
// it owns leaves clients without that route — both are silent, so the wizard names them.
func (s *Service) migrateActionRoutesConverged(ctx context.Context) (string, error) {
	hs := s.HSGlobalFn()
	if hs == nil {
		return "", errors.New("the headscale client is not configured on this panel")
	}
	rows, err := s.dbc().Query(`SELECT COALESCE(exit_node_id,''), prefix FROM prefix_owner`)
	if err != nil {
		return "", fmt.Errorf("prefix_owner: %w", err)
	}
	defer rows.Close()
	owned := map[string]map[string]bool{}
	total := 0
	for rows.Next() {
		var relay, prefix string
		if err := rows.Scan(&relay, &prefix); err != nil {
			return "", fmt.Errorf("prefix_owner scan: %w", err)
		}
		if strings.TrimSpace(relay) == "" {
			continue
		}
		if owned[relay] == nil {
			owned[relay] = map[string]bool{}
		}
		owned[relay][prefix] = true
		total++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if total == 0 {
		return "no prefix ownership rows: there is no relay assignment to converge", nil
	}
	nodes, err := hs.ListAllNodesFresh()
	if err != nil {
		return "", fmt.Errorf("headscale nodes list: %w", err)
	}
	advertised := map[string]map[string]bool{}
	for _, n := range nodes {
		set := map[string]bool{}
		for _, r := range n.AvailableRoutes {
			r = strings.TrimSpace(r)
			if r == "" || r == "0.0.0.0/0" || r == "::/0" {
				continue
			}
			set[r] = true
		}
		if len(set) > 0 {
			advertised[strings.ToLower(n.Hostname)] = set
		}
	}
	var problems []string
	for relay, prefixes := range owned {
		have := advertised[strings.ToLower(relay)]
		var missing, extra []string
		for p := range prefixes {
			if !have[p] {
				missing = append(missing, p)
			}
		}
		for p := range have {
			if !prefixes[p] {
				extra = append(extra, p)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s does not advertise %d owned prefix(es) (%s)", relay, len(missing), joinLimited(missing, 3)))
		}
		if len(extra) > 0 {
			problems = append(problems, fmt.Sprintf("%s advertises %d prefix(es) it does not own (%s)", relay, len(extra), joinLimited(extra, 3)))
		}
	}
	report := fmt.Sprintf("owned prefixes=%d relays=%d", total, len(owned))
	if len(problems) > 0 {
		return report + "; " + strings.Join(problems, "; "),
			errors.New("relay route advertisement does not match the ownership table — run the route sync before switching DNS")
	}
	return report, nil
}

// migrateActionDerpProbe answers "does the relay accept a DERP upgrade?".
//
// 101 is the only healthy answer: through a proxy that negotiated HTTP/2 the upgrade is impossible
// (426), and a probe that accepted that as "alive" is exactly the false green B478 removed.
func (s *Service) migrateActionDerpProbe(run *db.HostMigration) migrate.InternalFunc {
	return func(ctx context.Context) (string, error) {
		relays, err := db.ListDerpRelays(s.dbc())
		if err != nil {
			return "", fmt.Errorf("derp_relays: %w", err)
		}
		if len(relays) == 0 {
			return "", errors.New("no DERP relay is registered — register one on /admin/derp/relays/init")
		}
		target, ok := pickDerpRelayForRun(relays, run)
		if !ok {
			return "", fmt.Errorf("no enabled DERP relay found among %d registered row(s)", len(relays))
		}
		info := derphealth.DERPInfo{
			RegionID:   target.RegionID,
			RegionCode: target.RegionCode,
			RegionName: target.RegionName,
			Host:       target.Hostname,
			URL:        target.URL,
		}
		probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		code, perr := derphealth.ProbeOne(probeCtx, info, &http.Client{Timeout: 15 * time.Second})
		report := fmt.Sprintf("relay=%s url=%s status=%d", target.Hostname, target.URL, code)
		if perr != nil {
			return report, fmt.Errorf("probe %s: %w", target.Hostname, perr)
		}
		if code != http.StatusSwitchingProtocols {
			return report, fmt.Errorf("relay %s answered %d, not 101 (a proxy that negotiates HTTP/2 cannot carry a DERP upgrade)", target.Hostname, code)
		}
		return report, nil
	}
}

// pickDerpRelayForRun prefers the relay the operator is moving — the one whose hostname or URL
// mentions the new host — and falls back to the first enabled non-bundled relay.
func pickDerpRelayForRun(relays []db.DerpRelay, run *db.HostMigration) (db.DerpRelay, bool) {
	host := ""
	if run != nil {
		host = strings.ToLower(strings.TrimSpace(run.TargetHost))
	}
	if host != "" {
		for _, r := range relays {
			if !r.Enabled {
				continue
			}
			if strings.Contains(strings.ToLower(r.Hostname), host) || strings.Contains(strings.ToLower(r.URL), host) {
				return r, true
			}
		}
	}
	for _, r := range relays {
		if r.Enabled && !r.IsBundled {
			return r, true
		}
	}
	for _, r := range relays {
		if r.Enabled {
			return r, true
		}
	}
	return db.DerpRelay{}, false
}

// joinLimited keeps a failure report short: the first n items plus a count of the rest.
func joinLimited(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

// migrateInternalActionNames lists the wired names, so the page can say which of a plan's internal
// steps this build can actually run (an unwired one is pending, not failed).
func migrateInternalActionNames() []string {
	return []string{
		"internal_nodes_online",
		"internal_tags_present",
		"internal_policy_equivalent",
		"internal_routes_converged",
		"internal_derp_probe",
		"external_derp_probe_101",
	}
}

// ensure the sql import is used even if a future refactor drops a query.
var _ = sql.ErrNoRows
