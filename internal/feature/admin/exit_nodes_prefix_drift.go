// exit_nodes_prefix_drift.go — the prefix-assignment truth block (B275/B276).
//
// Split out of exit_nodes.go in refactor Phase D (2026-10-01). Three writers
// disagree about which relay serves which prefix — the assignment table
// (prefix_owner), the advertised routes and the generated ACL — and this file is
// the machinery that measures the disagreement and lets the operator re-pin a
// prefix. It is the densest reasoning in the page, so it gets its own file.

package admin

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/headscale"
	"skygate/internal/prefixowner"
)

// relayChoicesFor returns the distinct relay hostnames shown in the
// prefix-assignment select (the exit_servers rows the page already rendered,
// de-duplicated and sorted). B275.1.
func relayChoicesFor(nodes []ExitNodeInfo) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range nodes {
		h := strings.TrimSpace(n.Hostname)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// PrefixOwnerRow is one row of the /admin/exit-nodes
// "prefix assignment" table (B275.1).
//
// The table is the authority for WHICH relay advertises a prefix: headscale
// serves a subnet prefix from exactly one relay, so the per-rule exit node can
// be honoured for only one of two devices that want the same CIDR. The engine
// (internal/prefixowner) picks the owner — explicit rules win, the rest is spread
// over the healthy relays and is sticky — and the operator can pin a prefix by
// hand here (source='manual', which the engine never overwrites while that relay
// is healthy).
type PrefixOwnerRow struct {
	Prefix   string
	ExitNode string
	// Source is explicit | manual | auto — why this relay owns the prefix.
	Source  string
	Claims  int
	Devices int
	// Advertised is true when the owning relay currently reports the prefix in
	// its available routes; Assigned-but-not-advertised is the state the
	// operator has to look at (the relay's route sync has not caught up yet, or
	// the SSH sync failed).
	Advertised bool
	// B276: AlsoBy lists the OTHER relays that advertise the same prefix. Two
	// advertisers is the B274 state — headscale picks one primary and can move it
	// between passes, so the pin matches only half the time.
	AlsoBy []string
	// B276: Unserved is true when NO relay advertises the prefix: the rule is
	// dead weight (the ACL may grant it, but no route exists to carry it).
	Unserved bool
}

// PrefixDriftStats is the B276 "why is this prefix not working" summary the exit
// nodes page renders. It answers the three questions that were previously only
// answerable by diffing SQL against headscale by hand:
//
//  1. does the owning relay actually advertise its prefixes (NotAdvertised /
//     Unserved / Duplicated);
//  2. is the policy headscale serves the one skygate would generate right now
//     (PolicyInSync) — the stale-ACL class that silently killed every pin for a
//     prefix when the assignment moved to another relay;
//  3. which rows the page is showing out of the whole table (Shown/Total).
type PrefixDriftStats struct {
	Total          int
	Shown          int
	NotAdvertised  int
	Unserved       int
	Duplicated     int
	PinnedDrifted  int
	PolicyChecked  bool
	PolicyInSync   bool
	PolicyErr      string
	PolicyBytes    int
	PolicyLiveByte int
	// PolicyDetail names which sections of the two documents differ (B288), so
	// the banner cannot blame the `via` pins for a difference that is, say, two
	// extra tag declarations. Empty when the policies are equivalent.
	PolicyDetail string
	// PolicyApply is what the privileged applier recorded the last time it ran
	// (B288.1: "ok @ 21:47, 5082 bytes" or "failed — headscale did not answer on
	// …"). It is the missing half of the drift story: a stale policy whose
	// applier says OK means the write happened but headscale serves something
	// else; an applier that says failed names its reason outright.
	PolicyApply string
	// LiveReadErr is why the AНОНС (advertised-routes) column could not be filled
	// this render (B294): without it, an unreachable headscale made every prefix
	// look unadvertised and «никто не объявляет: N» read as a fact.
	LiveReadErr string
	// LiveReadHint is the actionable half of LiveReadErr (which env var to check,
	// how to probe the API) — see headscale.ACLReadHintFor.
	LiveReadHint string
	// PolicyVia names the SOURCE of an "in sync" verdict when it did not come from
	// a live read (B295): "compared with the last APPLIED snapshot vN (headscale did
	// not answer)". Empty means the verdict came from headscale itself.
	PolicyVia string
	// TransportFailed lists the relays whose LAST route application failed inside
	// the B309 window. They are excluded from the healthy set the prefix
	// assignment uses, so their prefixes have been handed to a relay that answers
	// — and this list is the explanation the operator needs, because re-applying
	// the ACL can never fix "нет маршрута" on a relay skygate cannot reach.
	TransportFailed []RelayTransportNote
	// TransportPaths lists the relays whose last application SUCCEEDED, with the
	// transport that carried it (B310): "tailnet" is the path that survives a
	// blocked/withdrawn public address, "public" is the one that does not.
	TransportPaths []RelayTransportNote
	// TailnetReady/IP/Iface/Reason describe skygate's OWN tailnet presence (B310).
	// When it is false, every tailnet target is unreachable by construction and the
	// page says so instead of showing an unexplained ssh timeout.
	TailnetReady  bool
	TailnetIP     string
	TailnetIface  string
	TailnetReason string
	// StaleAdvertisements lists the relays that are excluded from the healthy set
	// AND still advertise prefixes the assignment table no longer gives them
	// (B374). It is the other half of TransportFailed: that banner says "this relay
	// could not be configured and its prefixes moved"; this one says "and it is
	// still SERVING them, because the portal cannot reach it to prune the
	// advertisement". Without it the operator sees the prefixes move on one page and
	// the routes stay live in headscale with no line anywhere connecting the two.
	//
	// The value is `relay_advertise_stale:<relay>` in global_settings, written by the
	// route-convergence pass and cleared by it the moment the advertisement matches
	// (or by the next successful re-sync).
	StaleAdvertisements []StaleAdvertisementNote
}

// StaleAdvertisementNote is one relay's B374 stale-advertisement warning, rendered
// next to the B309 transport banner.
type StaleAdvertisementNote struct {
	// Relay is the relay name as recorded (lower-cased).
	Relay string
	// Age is how long ago the fact was recorded, for the same "how stale is this?"
	// reading the transport banner gives.
	Age string
	// Count is how many advertised prefixes the assignment table no longer assigns
	// this relay.
	Count int
	// Reason is the stored sentence, shown verbatim so the page and the journal
	// cannot disagree.
	Reason string
}

// RelayTransportNote is one relay's last route application on the prefix card
// (B309 for the failures, B310 for the path in use).
type RelayTransportNote struct {
	// Relay is the relay name as recorded (lower-cased).
	Relay string
	// Reason is the transport/approval error the last application produced.
	Reason string
	// Age is how long ago that application ran ("3m12s").
	Age string
	// Prefixes is how many rows of the assignment table still point at this relay
	// as their owner (0 when it owned nothing — the transport is still broken and
	// worth naming, it just did not move anything).
	Prefixes int
	// Via is the transport that carried (or failed to carry) the routes:
	// "local", "tailnet", "public", "name" (B310).
	Via string
	// Endpoint is the address used, e.g. "tailnet 100.64.0.2:18022" (B310).
	Endpoint string
	// OK is true when the application succeeded.
	OK bool
}

// prefixDriftRowLimit caps how many rows the page renders. The assignment table
// holds every prefix the rules ever produced (live: ~1500 rows), and rendering all
// of them with a relay <select> per row made the page megabytes of HTML for no
// benefit — the operator needs the drifted rows, not the healthy ones.
const prefixDriftRowLimit = 200

// loadPrefixOwnerRows reads the assignment table, resolves what each relay
// advertises and what headscale serves, and returns the rows worth looking at plus
// the summary the page renders (B276: the rows are the drifted and manually pinned
// ones, not all ~1500 table entries, and the summary says whether the LIVE policy
// still matches the table).
//
// Signature note (B276): this used to return just the rows (B275.1). The page needs
// both halves from ONE headscale read, so the contract in
// scripts/check_b275_1_prefix_ui.sh was renegotiated to the two-value form — the
// property it protects (the page reads the assignment table and hands rows to the
// template) is unchanged.
func (s *Service) loadPrefixOwnerRows() ([]PrefixOwnerRow, PrefixDriftStats) {
	var stats PrefixDriftStats
	rows, err := s.dbc().Query(`SELECT prefix, exit_node_id, COALESCE(source,'auto'),
	                                   COALESCE(claims,0), COALESCE(devices,0)
	                            FROM prefix_owner ORDER BY prefix`)
	if err != nil {
		log.Printf("[exit-nodes] prefix_owner read failed: %v", err)
		return nil, stats
	}
	defer rows.Close()
	// advertised[relay] = set of prefixes the relay currently advertises, and
	// advertisers[prefix] = the relays advertising it, so both "my owner is silent"
	// and "two relays claim it" are visible from one headscale read.
	advertised := map[string]map[string]bool{}
	advertisers := map[string][]string{}
	// B294: an unreachable headscale used to leave `advertised` EMPTY, and the
	// table then rendered every prefix as «нет / нет маршрута / никто не
	// объявляет» — absence of EVIDENCE presented as a negative FACT. That is the
	// live `aro` screenshot: 19 prefixes, 0 advertised, 19 «проблемных», while the
	// real cause was `dial tcp 127.0.0.1:8081: connect: connection refused`. The
	// rows stay, but the reason travels to the page so the operator sees "we could
	// not ask headscale" instead of "nothing is advertised".
	if hsNodes, herr := s.HSGlobalFn().ListAllNodes(); herr == nil {
		for _, n := range hsNodes {
			set := map[string]bool{}
			for _, p := range n.AvailableRoutes {
				set[p] = true
				advertisers[p] = append(advertisers[p], strings.ToLower(n.Hostname))
			}
			advertised[strings.ToLower(n.Hostname)] = set
		}
	} else {
		stats.LiveReadErr = herr.Error()
		stats.LiveReadHint = headscale.ACLReadHintFor(s.HSGlobalFn(), herr)
		log.Printf("[exit-nodes] cannot read the advertised routes from headscale (%v) — the АНОНС column cannot be trusted this render", herr)
	}
	var all []PrefixOwnerRow
	for rows.Next() {
		var r PrefixOwnerRow
		if err := rows.Scan(&r.Prefix, &r.ExitNode, &r.Source, &r.Claims, &r.Devices); err != nil {
			continue
		}
		if set := advertised[strings.ToLower(r.ExitNode)]; set != nil {
			r.Advertised = set[r.Prefix]
		}
		owner := strings.ToLower(r.ExitNode)
		for _, a := range advertisers[r.Prefix] {
			if a != owner {
				r.AlsoBy = append(r.AlsoBy, a)
			}
		}
		r.Unserved = len(advertisers[r.Prefix]) == 0
		all = append(all, r)
	}
	stats.Total = len(all)
	// B277 RENEGOTIATION of the B276 row filter: the loader used to hand the template
	// only the drifted and manually pinned rows, because the table held every prefix it
	// had ever seen (live: 1655 rows, 1497 dead) and printing them all produced
	// megabytes of HTML. The engine now prunes the dead rows (prefixowner.Prune), so
	// the table describes the network (~150 rows) and the operator needs ALL active
	// prefixes on screen — a group is pinned as a group, and a healthy Cloudflare row
	// that is hidden cannot be pinned. The drift filter moved to the page
	// (`?only=drift`, loadPrefixAdminView).
	out := make([]PrefixOwnerRow, 0, len(all))
	for _, r := range all {
		drifted := !r.Advertised || r.Unserved || len(r.AlsoBy) > 0
		switch {
		case r.Unserved:
			stats.Unserved++
		case !r.Advertised:
			stats.NotAdvertised++
		}
		if len(r.AlsoBy) > 0 {
			stats.Duplicated++
		}
		if r.Source == "manual" && drifted {
			stats.PinnedDrifted++
		}
		if len(out) < prefixDriftRowLimit {
			out = append(out, r)
		}
	}
	stats.Shown = len(out)
	s.fillPolicyDrift(&stats)
	s.fillTransportState(&stats, all)
	return out, stats
}

// fillTransportState answers the two questions the prefix card could not answer
// before B309/B310: WHICH relays cannot be configured at all (B309, they lose their
// prefixes), and WHICH PATH is management actually using (B310 — the tailnet, which
// survives a blocked public IP, or the public address, which does not).
//
// WHY the page needs this: the exclusion MOVES prefixes, and a move that the
// operator did not ask for is exactly what made the old behaviour so confusing —
// the table showed «нет маршрута» on rows that no ACL re-apply could repair,
// because the owner relay was unreachable, while every page and every log line
// called that relay healthy. The banner names the relay, the reason and the age,
// and says that the move is automatic and temporary.
func (s *Service) fillTransportState(stats *PrefixDriftStats, rows []PrefixOwnerRow) {
	// B310: skygate's own tailnet presence. Without it every tailnet target can
	// only time out — which is exactly what the live agent VM host did (no
	// tailscaled at all, so 100.64.0.2 was routed to the LAN gateway).
	st := exit_rules.SkygateTailnetState()
	stats.TailnetReady = st.Ready
	stats.TailnetIP = st.IP
	stats.TailnetIface = st.Iface
	stats.TailnetReason = st.Reason

	states := exit_rules.ListRelayApplyStates(s.dbc())
	if len(states) == 0 {
		return
	}
	owned := map[string]int{}
	for _, r := range rows {
		owned[strings.ToLower(r.ExitNode)]++
	}
	now := time.Now().Unix()
	for relay, st := range states {
		failed := st.Failed(now, exit_rules.RelayApplyFailureWindow)
		note := RelayTransportNote{
			Relay:    relay,
			Reason:   st.Detail,
			Age:      time.Since(time.Unix(st.At, 0)).Truncate(time.Second).String(),
			Prefixes: owned[relay],
			Via:      st.Via,
			Endpoint: st.Endpoint,
			OK:       st.OK,
		}
		if failed {
			stats.TransportFailed = append(stats.TransportFailed, note)
			continue
		}
		// Only relays we know something about (a recorded transport), so a fresh
		// install does not render an empty list of "unknown".
		if note.Via != "" || note.Endpoint != "" {
			stats.TransportPaths = append(stats.TransportPaths, note)
		}
	}
	sort.Slice(stats.TransportFailed, func(i, j int) bool {
		if stats.TransportFailed[i].Prefixes != stats.TransportFailed[j].Prefixes {
			return stats.TransportFailed[i].Prefixes > stats.TransportFailed[j].Prefixes
		}
		return stats.TransportFailed[i].Relay < stats.TransportFailed[j].Relay
	})
	sort.Slice(stats.TransportPaths, func(i, j int) bool {
		return stats.TransportPaths[i].Relay < stats.TransportPaths[j].Relay
	})
	// B374: the excluded relays that are STILL advertising. Read from the same
	// global_settings family the transport banner uses, so the two cannot be filled
	// from different sources.
	stale := exit_rules.ListStaleAdvertisements(s.dbc())
	if len(stale) > 0 {
		names := make([]string, 0, len(stale))
		for relay := range stale {
			names = append(names, relay)
		}
		sort.Strings(names)
		for _, relay := range names {
			st := stale[relay]
			stats.StaleAdvertisements = append(stats.StaleAdvertisements, StaleAdvertisementNote{
				Relay:  relay,
				Age:    time.Since(time.Unix(st.At, 0)).Truncate(time.Second).String(),
				Count:  st.Count,
				Reason: st.Reason,
			})
		}
	}
}

// fillPolicyDrift compares the policy headscale is serving with the one skygate
// would generate right now (B276). A mismatch means the per-CIDR `via` pins were
// produced from an older assignment table — the exact silent failure this block
// closes.
func (s *Service) fillPolicyDrift(stats *PrefixDriftStats) {
	gen, err := acl.GenerateACLLiveFormat(s.dbc())
	if err != nil {
		stats.PolicyErr = "generate: " + err.Error()
		return
	}
	stats.PolicyBytes = len(gen)
	hs := s.HSGlobalFn()
	if hs == nil {
		stats.PolicyErr = "no headscale client"
		return
	}
	live, err := hs.GetACL()
	if err != nil {
		// B295: headscale did not answer. Before reporting «неизвестно», answer the
		// same question from the last APPLIED snapshot in acl_snapshots — skygate
		// wrote it, so it is what headscale was serving. On a `policy.mode: file`
		// host this matters a lot: every apply restarts headscale (0.29 re-reads the
		// file only at startup), so a read that lands in that window used to leave
		// the page permanently «состояние политики неизвестно» even though nothing
		// was wrong.
		if version, verr := db.LastAppliedACLVersion(s.dbc()); verr == nil && version > 0 {
			if snapshot, serr := db.GetACLConfig(s.dbc(), version); serr == nil && strings.TrimSpace(snapshot) != "" {
				if same, cmpErr := headscale.PolicyEquivalent(gen, snapshot); cmpErr == nil && same {
					stats.PolicyChecked = true
					stats.PolicyInSync = true
					stats.PolicyLiveByte = len(snapshot)
					stats.PolicyVia = headscale.SnapshotInSyncHint(version)
					log.Printf("[exit-nodes] live policy unreadable (%v) but the generated policy equals the last applied snapshot v%d — reporting «в синхроне» from the snapshot", err, version)
					return
				}
			}
		}
		stats.PolicyErr = "read live policy: " + err.Error()
		return
	}
	stats.PolicyLiveByte = len(live)
	same, cmpErr := headscale.PolicyEquivalent(gen, live)
	if cmpErr != nil {
		stats.PolicyErr = "compare: " + cmpErr.Error()
		return
	}
	stats.PolicyChecked = true
	stats.PolicyInSync = same
	// B288.1: report what the privileged applier said the last time it ran. A
	// missing file means the applier has never run (or is older than B288.1).
	if st, ok := headscale.ReadPolicyApplyStatus(); ok {
		stats.PolicyApply = st.Summary()
	}
	if !same {
		// B288: say WHAT differs. The banner used to explain every drift with
		// the `via`-pin story; live on `aro` the only differences were 16
		// duplicated grants (semantically no-ops) and two tag declarations, so
		// the explanation pointed at the one thing that was not wrong.
		if detail, dErr := headscale.PolicyDriftDetail(gen, live); dErr == nil {
			stats.PolicyDetail = detail
		}
	}
}

// PostAdminExitPrefixOwner pins one prefix to one relay (B275.1), or hands it
// back to the engine when the relay field is empty. Admin-only, audited.
func (s *Service) PostAdminExitPrefixOwner(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	prefix := strings.TrimSpace(r.FormValue("prefix"))
	relay := strings.TrimSpace(r.FormValue("relay"))
	if prefix == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("prefix is empty"), http.StatusSeeOther)
		return
	}
	if err := prefixowner.SetManual(s.dbc(), prefix, relay); err != nil {
		log.Printf("[exit-nodes] SetManual(%s, %s): %v", prefix, relay, err)
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	action := "prefix_owner_pin"
	if relay == "" {
		action = "prefix_owner_auto"
	}
	s.Backend.Audit(c.UserID, c.Username, action, fmt.Sprintf("prefix=%s relay=%s", prefix, relay))
	// B277: re-apply the ACL right away. prefixowner.Assign keeps a manual row it
	// finds, so the next sync pass reports no CHANGE for a manual pin — without this
	// the pin would sit in the database while headscale kept serving the old relay.
	s.finishPrefixChange(w, r, c.Username, action+" prefix="+prefix+" relay="+relay,
		"назначение сохранено: "+prefix+" → "+relay)
}

// PostAdminExitNodeACLResync regenerates the headscale policy from the current
// database state and pushes it (B276).
//
// This is the operator's escape hatch for the class this block closes: the
// assignment table and the advertised routes are recomputed every few minutes,
// but the ACL is only regenerated when a rule/user/device changes — so after an
// ownership flip the live policy keeps pinning prefixes to the relay that no
// longer serves them, and the clients silently lose those routes. The sync paths
// now re-apply automatically; the button exists because the operator may have just
// moved a prefix by hand (or a sync failed) and should not have to wait for a tick.
//
// Admin-only, audited like every other ACL apply.
func (s *Service) PostAdminExitNodeACLResync(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var alerter acl.Alerter
	if s.Notifier != nil {
		alerter = s.Notifier
	}
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	results := acl.ApplyACLForAllPlanes(s.dbc(),
		func(string) *headscale.Client { return s.HSGlobalFn() },
		alerter,
		c.Username,
		fmt.Sprintf("acl resync from /admin/exit-nodes by %s (prefix ownership pins)", c.Username),
		viaFlag,
	)
	for _, res := range results {
		if res.Err != nil {
			log.Printf("[exit-nodes] ACL resync by %s failed: %v", c.Username, res.Err)
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("ACL re-apply failed: "+res.Err.Error()), http.StatusSeeOther)
			return
		}
	}
	log.Printf("[exit-nodes] ACL resync by %s: %d plane(s) updated", c.Username, len(results))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(fmt.Sprintf("ACL regenerated from the current assignment table (%d plane(s))", len(results))), http.StatusSeeOther)
}
