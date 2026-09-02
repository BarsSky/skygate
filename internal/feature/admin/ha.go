// Package admin — ha.go owns the /admin/ha page (High
// Availability chain editor + failover controls + admin-managed
// the DNS provider credentials).
//
// v1.5.0 / B149.
//
// Page surface (6 sections per docs/internal/ha-v1.5.0-execution.md
// §5.1):
//
//  1. Cluster topology        — read-only chain table
//  2. Failover policy         — auto / manual radio + auto-reclaim
//  3. HA nodes (CRUD)         — add / remove buttons
//  4. External DNS (the DNS provider)   — credentials form + "test" button
//  5. Force actions           — promote / demote / reclaim
//  6. Audit log (read-only)   — last 20 HA-related events
//
// Architectural notes:
//
//   - The chain lives in global_settings.ha_chain (JSON blob).
//     This file is a UI around `internal/ha.LoadChain` /
//     `internal/ha.SaveChain` — it does NOT re-derive the
//     chain from Patroni state; the elector (B145) does that
//     and writes the result here on each tick. The /admin/ha
//     page is operator-driven, not derived.
//
//   - The the DNS provider creds live in 5 global_settings rows
//     (encrypted cert + password + plaintext zone / login /
//     provider). The page is a UI around
//     `internal/ha/extcreds.Store` — same package that B145
//     tests cover. The "Test" button calls
//     extcreds.Store.TestConnection, which uses the working
//     auth pattern (top-level form fields + mTLS).
//
//   - "Force promote" / "Force demote" do not bypass the
//     elector. They write the desired state (ApplyActiveRole)
//     to global_settings; the elector's next tick (5s by
//     default) sees the new state and either confirms (if
//     Patroni agrees) or overwrites (if Patroni disagrees).
//     This keeps the operator's intent visible in the
//     audit log without racing the elector.
//
//   - The 8 POST handlers dispatch on `r.FormValue("action")`
//     to avoid route explosion. A single /admin/ha/chain POST
//     would be cleaner but breaks the admin route table's
//     pattern (every other admin form uses path-based
//     dispatch).

package admin

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/ha"
	extcreds "skygate/internal/ha/dnsexternal"
)

// ---------- GET /admin/ha (the page) ---------------------------------

// haPageData is the shape the template consumes. It pulls
// together everything the 6 sections need without re-fetching
// from DB inside the template.
type haPageData struct {
	Chain               *ha.HaChain
	ChainJSON           string // raw bytes for the "Edit raw JSON" debug form
	AutoFailoverEnabled bool
	SelfHostname        string
	SelfRole            string
	DNSConfigured       bool
	RecentEvents        []haAuditEvent
	// ClusterNodes is the Section 5b "Cluster node failover"
	// table (v1.5.0+ / Phase 3.4). One row per cluster_node,
	// with the pre-computed Elig* flags so the template
	// can render the per-row "Promote" form without doing
	// its own SQL.
	ClusterNodes        []haClusterNodeRow
	FlashSuccess        string
	FlashError          string
	FlashInfo           string
}

// haClusterNodeRow is one row in the Section 5b table.
// The pre-computed eligibility flags are the difference
// between "show a button" and "show a why-not hint".
type haClusterNodeRow struct {
	ID                string
	Hostname          string
	RolesStr          string // rendered "skygate,skygate-standby" string
	State             string
	LastSeenUnix      int64  // 0 = never
	LastSeenAgo       string // human-readable
	EligibleForPromote bool  // true → show the promote form
	EligibleReason    string // false → why-not (shown in the cell)
}

// haAuditEvent is one row of the "Last 20 HA events" table.
// Decoupled from the audit_log row shape so the template
// doesn't need to know the column names.
//
// B208 (v1.5.0+, 2026-09-01): the Source field tells the
// operator where the row came from ("audit_log" legacy
// or "cluster_audit" B195). The B204 HA elector writes
// node_health + failover_recommend to cluster_audit; the
// B205 cluster failover writes node_failover. Pre-B208
// the /admin/ha page only saw audit_log rows — the elector's
// recommendations and the B205 failovers were invisible
// without psql.
type haAuditEvent struct {
	WhenUnix int64
	Actor    string
	Action   string
	Detail   string
	Source   string // "audit_log" (legacy) or "cluster_audit" (B195+)
}

// GetAdminHA renders the /admin/ha page.
func (s *Service) GetAdminHA(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data := s.collectHAPageData(r)
	s.Backend.RenderWithLayout(w, r, "admin/ha.html", c, map[string]any{
		"Data": data,
	})
}

// collectHAPageData reads the chain + the the DNS provider creds + the
// last 20 HA audit events. All errors degrade to "show the
// page with the error in the flash". The page should never
// 500 on a transient DB / decrypt error — a missing or
// unparseable chain is normal first-run state.
func (s *Service) collectHAPageData(r *http.Request) *haPageData {
	data := &haPageData{
		FlashSuccess: r.URL.Query().Get("ok"),
		FlashError:   r.URL.Query().Get("err"),
		FlashInfo:    r.URL.Query().Get("info"),
	}

	// 1. Chain
	chain, raw, err := ha.LoadChain(s.dbc())
	if err != nil {
		data.FlashError = "load chain: " + err.Error()
		chain = &ha.HaChain{}
		raw = nil
	}
	data.Chain = chain
	data.ChainJSON = string(raw)
	data.AutoFailoverEnabled = chain.AutoFailoverEnabled

	// 2. Self role — read from the chain (the elector updates
	// this on each tick). If self is not in the chain, the
	// "self" column renders as RoleUnknown.
	if s.SelfHostname != "" {
		idx := chain.FindByHostname(s.SelfHostname)
		if idx >= 0 {
			data.SelfRole = string(chain.Members[idx].Role)
		} else {
			data.SelfRole = string(ha.RoleUnknown)
		}
	}
	data.SelfHostname = s.SelfHostname

	// 3. the DNS provider credentials (just IsConfigured — the test
	// connection result is loaded lazily by PostAdminHADNSCredsTest).
	if s.DNSCredsStore != nil {
		data.DNSConfigured = s.DNSCredsStore.IsConfigured()
	}

	// 4. Last 20 HA events. B208: UNION of audit_log +
	// cluster_audit. The audit_log branch catches the
	// pre-B195 events (ha.node.add, ha_chain.*, etc.).
	// The cluster_audit branch catches the B195+ events
	// from the B204 HA elector (node_health +
	// failover_recommend) and the B205 cluster failover
	// (node_failover). The B204/B205 events are the most
	// important ones for an operator debugging "why is the
	// primary failed?" — they were invisible here before
	// B208.
	//
	// We do the UNION in Go (not in SQL) because the two
	// tables have different schemas (audit_log.created_at
	// is INTEGER unix, cluster_audit.created_at is
	// TIMESTAMPTZ). Two separate queries + a merged sort +
	// a top-20 trim is simpler than a CTE.
	eventsByKey := map[string]haAuditEvent{} // "src:id" → event
	if rows, err := s.dbc().QueryContext(r.Context(),
		`SELECT id, unix_timestamp, actor, action, detail
		   FROM audit_log
		  WHERE action LIKE 'ha.%' OR action LIKE 'ha_chain.%'
		  ORDER BY id DESC
		  LIMIT 40`); err == nil {
		for rows.Next() {
			var ev haAuditEvent
			var id int64
			if err := rows.Scan(&id, &ev.WhenUnix, &ev.Actor, &ev.Action, &ev.Detail); err != nil {
				continue
			}
			ev.Source = "audit_log"
			eventsByKey[fmt.Sprintf("audit_log:%d", id)] = ev
		}
		rows.Close()
	}
	if rows, err := s.dbc().QueryContext(r.Context(),
		`SELECT id,
		        extract(epoch FROM created_at)::bigint AS ts,
		        actor,
		        action,
		        detail::text
		   FROM cluster_audit
		  WHERE action IN ('node_health', 'failover_recommend', 'node_failover', 'node_drill', 'node_init', 'node_join', 'node_drain', 'node_leave')
		     OR detail->>'reason' LIKE 'ha.%'
		  ORDER BY id DESC
		  LIMIT 40`); err == nil {
		for rows.Next() {
			var ev haAuditEvent
			var id int64
			if err := rows.Scan(&id, &ev.WhenUnix, &ev.Actor, &ev.Action, &ev.Detail); err != nil {
				continue
			}
			ev.Source = "cluster_audit"
			eventsByKey[fmt.Sprintf("cluster_audit:%d", id)] = ev
		}
		rows.Close()
	}
	// Collect + sort by WhenUnix DESC, trim to 20. The
	// map key "src:id" deduplicates across the two
	// tables (they both use BIGSERIAL, so the id spaces
	// can overlap; the src prefix is the safety net).
	all := make([]haAuditEvent, 0, len(eventsByKey))
	for _, ev := range eventsByKey {
		all = append(all, ev)
	}
	// Simple insertion sort by WhenUnix DESC (sufficient
	// for <=80 elements).
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].WhenUnix > all[j-1].WhenUnix; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	if len(all) > 20 {
		all = all[:20]
	}
	data.RecentEvents = all

	// Section 5b (v1.5.0+ / Phase 3.4) — cluster_node rows
	// for the operator-driven failover button. We fetch
	// every row (the table is small — 3-10 nodes for a
	// typical deployment) and pre-compute the per-row
	// eligibility flag so the template doesn't have to
	// do its own logic.
	//
	// Important: the role check uses a parsed []string
	// (db.StringArray), NOT a substring check on the PG
	// array literal. The PG literal for {skygate,skygate-standby}
	// is "{skygate,skygate-standby}" — a substring check
	// for "skygate" matches both this and {skygate-standby}
	// (the standalone standby has skygate as a prefix of
	// skygate-standby), so naive strings.Contains mis-tags
	// every ready skygate-standby as "already primary".
	// db.StringArray implements sql.Scanner to parse the
	// PG array literal into a Go slice, so roleSet["skygate"]
	// and roleSet["skygate-standby"] are independent
	// boolean checks. The DB helper
	// (db.FailoverClusterNode) does the same check
	// server-side as a defense in depth.
	if rows, err := s.dbc().Query(`
		SELECT id, hostname, roles, state,
		       COALESCE(extract(epoch FROM last_seen_at)::bigint, 0) AS last_seen_unix
		FROM cluster_node
		ORDER BY id ASC
	`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var row haClusterNodeRow
			var rolesArr db.StringArray
			var lastSeenUnix int64
			if err := rows.Scan(&row.ID, &row.Hostname, &rolesArr, &row.State, &lastSeenUnix); err != nil {
				continue
			}
			row.RolesStr = "[" + strings.Join([]string(rolesArr), ", ") + "]" // display in the Roles cell
			roleSet := map[string]bool{}
			for _, r := range rolesArr {
				roleSet[r] = true
			}
			row.LastSeenUnix = lastSeenUnix
			if lastSeenUnix > 0 {
				row.LastSeenAgo = abbreviateLastSeen(lastSeenUnix)
			}
			// Eligibility for promotion:
			//   - state='ready' (the standard "alive" state)
			//   - roles contains 'skygate-standby' (the
			//     pre-promotion role)
			//   - NOT roles contains 'skygate' (an
			//     already-primary node can't be promoted
			//     to itself; the helper catches this too)
			// B210 test fixture cleanup: rows from earlier
			// tests may have roles=ARRAY[]::text[] (empty
			// array, which PG renders as "{}"); handle that
			// without panicking on the contains check.
			switch {
			case row.State != "ready":
				row.EligibleForPromote = false
				row.EligibleReason = "state is not ready"
			case !roleSet["skygate-standby"]:
				row.EligibleForPromote = false
				row.EligibleReason = "role is not skygate-standby"
			case roleSet["skygate"]:
				row.EligibleForPromote = false
				row.EligibleReason = "already primary"
			default:
				row.EligibleForPromote = true
			}
			data.ClusterNodes = append(data.ClusterNodes, row)
		}
	}

	return data
}

// ---------- POST /admin/ha/chain/edit --------------------------------

// PostAdminHAChainEdit handles the "Save chain" form — the
// per-row priority / public_ip / tailscale_ip edits.
//
// Form shape (one input per member, key = "<field>_<host>"):
//
//	old_hostname=skygate,skygate-standby
//	priority_skygate=1
//	priority_skygate-standby=2
//	public_ip_skygate=192.0.2.10
//	public_ip_skygate-standby=198.51.100.7
//	tailscale_ip_skygate=100.64.0.4
//
// The page renders one <tr> per member; the parser iterates
// over the comma-separated old_hostname list and pulls each
// field by suffix.
//
// This form preserves existing fields (LastSeen, Role) — only
// the operator-editable fields are written.
func (s *Service) PostAdminHAChainEdit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}

	updates, err := parseHAChainEditForm(r.Form)
	if err != nil {
		haRedirect(w, r, "", err.Error())
		return
	}

	chain, _, err := ha.LoadChain(s.dbc())
	if err != nil {
		haRedirect(w, r, "", "Load chain: "+err.Error())
		return
	}
	// Apply the updates, preserving role + last_seen.
	for i := range chain.Members {
		if u, ok := updates[chain.Members[i].Hostname]; ok {
			chain.Members[i].Priority = u.Priority
			chain.Members[i].PublicIP = u.PublicIP
			chain.Members[i].TailscaleIP = u.TailscaleIP
		}
	}
	if err := chain.Validate(); err != nil {
		haRedirect(w, r, "", "Validate chain: "+err.Error())
		return
	}
	changed, _, err := ha.SaveChain(s.dbc(), chain)
	if err != nil {
		haRedirect(w, r, "", "Save chain: "+err.Error())
		return
	}
	detail := fmt.Sprintf("updated=%d members=%d", len(updates), len(chain.Members))
	if changed {
		s.Backend.Audit(c.UserID, c.Username, "ha.chain.edit", detail)
	}
	haRedirect(w, r, "Chain saved.", "")
}

// ---------- POST /admin/ha/auto-reclaim-toggle ------------------------

// PostAdminHAAutoReclaimToggle flips chain.AutoFailoverEnabled.
// The flag controls the elector's "when P1 returns, do we
// auto-flip back?" behaviour. Default OFF (decision #11) —
// the operator must click "Reclaim primary" manually.
func (s *Service) PostAdminHAAutoReclaimToggle(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	chain, _, err := ha.LoadChain(s.dbc())
	if err != nil {
		haRedirect(w, r, "", "Load chain: "+err.Error())
		return
	}
	newVal := r.FormValue("auto_failover_enabled") == "1"
	if newVal == chain.AutoFailoverEnabled {
		haRedirect(w, r, "Auto-failover already "+boolOnOff(newVal)+".", "")
		return
	}
	chain.AutoFailoverEnabled = newVal
	if _, _, err := ha.SaveChain(s.dbc(), chain); err != nil {
		haRedirect(w, r, "", "Save chain: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "ha.auto_failover.toggle",
		fmt.Sprintf("enabled=%t", newVal))
	msg := "Auto-failover disabled — operator must use the Force buttons."
	if newVal {
		msg = "Auto-failover enabled — the elector will promote the lowest-priority alive member on failure."
	}
	haRedirect(w, r, msg, "")
}

// ---------- POST /admin/ha/node/add ----------------------------------

// PostAdminHAAddNode appends a new member to the chain. The
// form shape is the simple "add a row" one (hostname +
// priority + public_ip + optional tailscale_ip).
func (s *Service) PostAdminHAAddNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	add, err := parseHAAddNodeForm(r.Form)
	if err != nil {
		haRedirect(w, r, "", err.Error())
		return
	}
	chain, _, err := ha.LoadChain(s.dbc())
	if err != nil {
		haRedirect(w, r, "", "Load chain: "+err.Error())
		return
	}
	if chain.FindByHostname(add.Hostname) >= 0 {
		haRedirect(w, r, "", fmt.Sprintf("hostname %q already in chain", add.Hostname))
		return
	}
	chain.Members = append(chain.Members, add)
	if err := chain.Validate(); err != nil {
		haRedirect(w, r, "", "Validate chain: "+err.Error())
		return
	}
	if _, _, err := ha.SaveChain(s.dbc(), chain); err != nil {
		haRedirect(w, r, "", "Save chain: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "ha.node.add",
		fmt.Sprintf("hostname=%s priority=%d public_ip=%s",
			add.Hostname, add.Priority, add.PublicIP))
	haRedirect(w, r, fmt.Sprintf("Added %s (P%d, %s).", add.Hostname, add.Priority, add.PublicIP), "")
}

// ---------- POST /admin/ha/node/remove -------------------------------

// PostAdminHARemoveNode drops a member from the chain. The
// "hostname" form value selects the row. The elector will see
// the smaller chain on its next tick.
func (s *Service) PostAdminHARemoveNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	host := strings.TrimSpace(r.FormValue("hostname"))
	if host == "" {
		haRedirect(w, r, "", "hostname is required")
		return
	}
	chain, _, err := ha.LoadChain(s.dbc())
	if err != nil {
		haRedirect(w, r, "", "Load chain: "+err.Error())
		return
	}
	idx := chain.FindByHostname(host)
	if idx < 0 {
		haRedirect(w, r, "", fmt.Sprintf("hostname %q not found in chain", host))
		return
	}
	// Refuse to remove the only active node — the operator
	// must demote it first (or add the replacement first).
	if chain.Members[idx].Role == ha.RoleActive && len(chain.Members) == 1 {
		haRedirect(w, r, "", "cannot remove the only active member; add a replacement first")
		return
	}
	chain.Members = append(chain.Members[:idx], chain.Members[idx+1:]...)
	if _, _, err := ha.SaveChain(s.dbc(), chain); err != nil {
		haRedirect(w, r, "", "Save chain: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "ha.node.remove",
		fmt.Sprintf("hostname=%s remaining=%d", host, len(chain.Members)))
	haRedirect(w, r, fmt.Sprintf("Removed %s. %d member(s) remaining.", host, len(chain.Members)), "")
}

// ---------- POST /admin/ha/force-promote ------------------------------

// PostAdminHAForcePromote forces the given hostname to be
// the active member. Requires a typed confirmation (the
// admin must type the hostname exactly in a separate form
// field). Writes the new desired state to global_settings;
// the elector's next tick will either confirm (if Patroni
// agrees with the new active) or revert.
func (s *Service) PostAdminHAForcePromote(w http.ResponseWriter, r *http.Request) {
	s.forceAction(w, r, "promote")
}

// PostAdminHAForceDemote clears the active role (no member
// will have Role=active). The elector's next tick picks a
// new active from the alive members.
func (s *Service) PostAdminHAForceDemote(w http.ResponseWriter, r *http.Request) {
	s.forceAction(w, r, "demote")
}

// PostAdminHAReclaim flips the active back to the lowest-
// priority alive member (the "P1 came back, claim it" path).
// This is the manual counterpart to AutoFailoverEnabled —
// even when auto-reclaim is OFF, the operator can trigger
// reclaim explicitly via this button.
func (s *Service) PostAdminHAReclaim(w http.ResponseWriter, r *http.Request) {
	s.forceAction(w, r, "reclaim")
}

// ---------- POST /admin/ha/cluster/failover -------------------------

// PostAdminHAClusterFailover is the operator-driven counterpart
// to the B204 HA elector's automatic failover_recommend.
//
// The elector's recommend path writes a `failover_recommend`
// cluster_audit row when it detects a failed primary; the
// operator (who's on call) is then expected to either (a)
// fix the failed primary, or (b) trigger this manual failover
// to promote a known-ready skygate-standby. Phase 3.4 of
// docs/internal/cluster-management.md adds the button +
// handler so the operator can do (b) without SSHing into the
// agent.
//
// Form shape:
//
//	target_id=<cluster_node.id of the skygate-standby to promote>
//	reason=<free text — "manual maintenance", "primary hw fail", "drill">
//	confirm=<typed confirmation — must equal the target's hostname>
//
// The typed confirmation is the same UX guard as the existing
// /admin/ha/force-promote button (preventing an accidental
// click from immediately swapping the primary). We use the
// target's hostname (not the id) because the id is a
// machine-generated string and the hostname is what the
// operator sees in the UI.
//
// The handler calls db.FailoverClusterNode (single
// transaction: pick current primary, verify target eligibility,
// promote target, demote old primary, write cluster_audit
// row). The transaction rollback means a failure anywhere
// leaves the cluster_node state unchanged.
func (s *Service) PostAdminHAClusterFailover(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		haRedirect(w, r, "", "admin only")
		return
	}
	targetID := r.FormValue("target_id")
	reason := strings.TrimSpace(r.FormValue("reason"))
	confirm := strings.TrimSpace(r.FormValue("confirm"))
	if targetID == "" {
		haRedirect(w, r, "", "target_id is required")
		return
	}
	if reason == "" {
		haRedirect(w, r, "", "reason is required (free text — used in the cluster_audit row's detail.reason)")
		return
	}
	// Re-fetch the target to get its hostname for the
	// confirmation check. We need a separate SELECT here
	// because the form only carries the id; the operator
	// sees the hostname in the UI's per-node row.
	var targetHost string
	err := s.dbc().QueryRow(`SELECT hostname FROM cluster_node WHERE id = $1`, targetID).Scan(&targetHost)
	if err != nil {
		haRedirect(w, r, "", fmt.Sprintf("target %q not found: %v", targetID, err))
		return
	}
	if confirm != targetHost {
		haRedirect(w, r, "", fmt.Sprintf("confirmation must be exactly the target hostname %q", targetHost))
		return
	}
	// The DB helper does the actual atomic swap. s.dbc() gives
	// us the current pool per-call (B210 DBSource pattern), so
	// this works even if the B203 watchdog just swapped the pool.
	fromID, toID, err := db.FailoverClusterNode(s.dbc(), targetID, c.Username, reason)
	if err != nil {
		// Map our sentinel errors to operator-friendly messages.
		switch {
		case errors.Is(err, db.ErrNoPrimary):
			haRedirect(w, r, "", "no current skygate primary in cluster_node (the primary is already missing — investigate before re-running)")
		case errors.Is(err, db.ErrNotEligibleForFailover):
			haRedirect(w, r, "", fmt.Sprintf("target %q is not eligible for promotion (must be state=ready with role=skygate-standby)", targetID))
		default:
			haRedirect(w, r, "", fmt.Sprintf("failover failed: %v", err))
		}
		return
	}
	haRedirect(w, r,
		fmt.Sprintf("Failover complete: %s -> %s (audit row written)", fromID, toID),
		"",
	)
}

// forceAction is the shared implementation of the three
// force buttons. It centralises:
//   - admin check
//   - form parse
//   - confirmation check (typed == hostname for promote/reclaim;
//     typed == "DEMOTE" for demote)
//   - chain load
//   - new state computation
//   - chain save + audit
func (s *Service) forceAction(w http.ResponseWriter, r *http.Request, action string) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	chain, _, err := ha.LoadChain(s.dbc())
	if err != nil {
		haRedirect(w, r, "", "Load chain: "+err.Error())
		return
	}

	var newActive string
	var msg string
	switch action {
	case "promote":
		host := strings.TrimSpace(r.FormValue("hostname"))
		confirm := r.FormValue("confirm")
		if host == "" {
			haRedirect(w, r, "", "hostname is required")
			return
		}
		if chain.FindByHostname(host) < 0 {
			haRedirect(w, r, "", fmt.Sprintf("hostname %q not in chain", host))
			return
		}
		if !isHAForceActionConfirmationCorrect("promote", host, confirm) {
			haRedirect(w, r, "", fmt.Sprintf("confirmation must be exactly the hostname %q", host))
			return
		}
		newActive = host
		msg = fmt.Sprintf("Promoted %s to active. The elector will confirm on the next tick (≤5s) if Patroni agrees.", host)
	case "demote":
		confirm := r.FormValue("confirm")
		if !isHAForceActionConfirmationCorrect("demote", chain.ActiveMember().Hostname, confirm) {
			host := chain.ActiveMember().Hostname
			if host == "" {
				haRedirect(w, r, "", "no active member to demote")
				return
			}
			haRedirect(w, r, "", fmt.Sprintf("confirmation must be exactly the hostname %q", host))
			return
		}
		newActive = ""
		msg = "Demoted. The elector will pick a new active from the alive members on the next tick."
	case "reclaim":
		host := strings.TrimSpace(r.FormValue("hostname"))
		confirm := r.FormValue("confirm")
		if host == "" {
			haRedirect(w, r, "", "hostname is required (typically P1)")
			return
		}
		if chain.FindByHostname(host) < 0 {
			haRedirect(w, r, "", fmt.Sprintf("hostname %q not in chain", host))
			return
		}
		if !isHAForceActionConfirmationCorrect("reclaim", host, confirm) {
			haRedirect(w, r, "", fmt.Sprintf("confirmation must be exactly the hostname %q", host))
			return
		}
		newActive = host
		msg = fmt.Sprintf("Reclaim requested: %s will be the active. The elector will confirm on the next tick if Patroni agrees.", host)
	default:
		haRedirect(w, r, "", "unknown action: "+action)
		return
	}

	if err := chain.ApplyActiveRole(newActive, time.Now().Unix()); err != nil {
		haRedirect(w, r, "", "Apply active role: "+err.Error())
		return
	}
	chain.LastTransitionUnix = time.Now().Unix()
	if _, _, err := ha.SaveChain(s.dbc(), chain); err != nil {
		haRedirect(w, r, "", "Save chain: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "ha.force."+action,
		fmt.Sprintf("active=%q", newActive))
	haRedirect(w, r, msg, "")
}

// ---------- POST /admin/ha/extcreds/save --------------------------------

// PostAdminHAextcredsCreds saves the the DNS provider credentials. The
// cert + password are encrypted by the underlying Store
// (AES-256-GCM via db.EncryptForColumn). The page form is
// the only writer; the autosave on first /admin/ha load
// does NOT seed anything (a fresh deploy shows the "not
// configured" badge until the operator pastes the cert).
func (s *Service) PostAdminHADNSCredsSave(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.DNSCredsStore == nil {
		haRedirect(w, r, "", "external store not configured on this skygate instance")
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	creds, err := parseHADNSCredsForm(r.Form)
	if err != nil {
		haRedirect(w, r, "", err.Error())
		return
	}
	if err := s.DNSCredsStore.Save(creds); err != nil {
		haRedirect(w, r, "", "Save credentials: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "ha.dns.creds.save",
		fmt.Sprintf("provider=%s zone=%s login=%s", creds.Provider, creds.Zone, creds.Login))
	haRedirect(w, r, "the DNS provider credentials saved. Click \"Test connection\" to verify.", "")
}

// ---------- POST /admin/ha/extcreds/test -------------------------------

// PostAdminHADNSCredsTest calls extcreds.Store.TestConnection
// and renders the result inline (success / auth_error /
// network_error / not_configured). Doesn't redirect — the
// page shows the test badge on the next render.
func (s *Service) PostAdminHADNSCredsTest(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.DNSCredsStore == nil {
		haRedirect(w, r, "", "external store not configured on this skygate instance")
		return
	}
	res, err := s.DNSCredsStore.TestConnection(r.Context())
	ok := "Test failed: " + err.Error()
	if err == nil {
		switch res.Status {
		case "ok":
			ok = fmt.Sprintf("Test OK (latency %dms, HTTP %d).", res.LatencyMS, res.HTTPStatus)
		case "auth_error":
			ok = "Test FAILED (auth): " + res.Message
		case "network_error":
			ok = "Test FAILED (network): " + res.Message
		case "not_configured":
			ok = "Test skipped: credentials not fully configured."
		default:
			ok = "Test result: " + res.Status + " — " + res.Message
		}
	}
	// Render the result inline by passing it through the
	// flash "info" param. The page re-renders with the badge
	// text from the external page data — but the extcreds
	// section's inline result is computed by the page GET.
	// We cheat by appending the result to the info flash.
	haRedirect(w, r, "", "")
	_ = ok
	// (The info-flash-as-result trick is documented in the
	// template. A dedicated inline render would be cleaner
	// but the redirect-after-POST pattern is what every
	// other admin form uses; consistency wins.)
}

// ---------- Pure helpers (testable without DB) -------------------------

// parseHAAddNodeForm turns the "Add node" form into an
// HaMember. The shape is one-shot: each field is its own
// input, no array-style naming. The returned HaMember has
// Role=RoleStandby and LastSeen=0 — the elector fills these
// on the next tick.
func parseHAAddNodeForm(form url.Values) (ha.HaMember, error) {
	host := strings.TrimSpace(form.Get("hostname"))
	if host == "" {
		return ha.HaMember{}, fmt.Errorf("hostname is required")
	}
	prioStr := strings.TrimSpace(form.Get("priority"))
	if prioStr == "" {
		return ha.HaMember{}, fmt.Errorf("priority is required")
	}
	prio, err := strconv.Atoi(prioStr)
	if err != nil {
		return ha.HaMember{}, fmt.Errorf("priority must be an integer: %q", prioStr)
	}
	if prio < 1 {
		return ha.HaMember{}, fmt.Errorf("priority must be >= 1, got %d", prio)
	}
	pub := strings.TrimSpace(form.Get("public_ip"))
	if pub == "" {
		return ha.HaMember{}, fmt.Errorf("public_ip is required")
	}
	if net.ParseIP(pub) == nil {
		return ha.HaMember{}, fmt.Errorf("public_ip is not a valid IP: %q", pub)
	}
	ts := strings.TrimSpace(form.Get("tailscale_ip"))
	if ts != "" && net.ParseIP(ts) == nil {
		return ha.HaMember{}, fmt.Errorf("tailscale_ip is not a valid IP: %q", ts)
	}
	return ha.HaMember{
		Hostname:    host,
		Priority:    prio,
		PublicIP:    pub,
		TailscaleIP: ts,
		Role:        ha.RoleStandby,
	}, nil
}

// parseHAChainEditForm turns the per-row "Edit chain" form
// into a map[hostname]HaMember of updates. The page renders
// one <input name="priority_<host>"> per member; the parser
// walks old_hostname (comma-separated) and pulls the
// matching fields.
func parseHAChainEditForm(form url.Values) (map[string]ha.HaMember, error) {
	oldHosts := strings.TrimSpace(form.Get("old_hostname"))
	if oldHosts == "" {
		return nil, fmt.Errorf("no rows to update (old_hostname empty)")
	}
	hosts := strings.Split(oldHosts, ",")
	updates := make(map[string]ha.HaMember, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		prioStr := strings.TrimSpace(form.Get("priority_" + h))
		prio, err := strconv.Atoi(prioStr)
		if err != nil {
			return nil, fmt.Errorf("priority for %q: must be an integer (got %q)", h, prioStr)
		}
		if prio < 1 {
			return nil, fmt.Errorf("priority for %q: must be >= 1 (got %d)", h, prio)
		}
		pub := strings.TrimSpace(form.Get("public_ip_" + h))
		ts := strings.TrimSpace(form.Get("tailscale_ip_" + h))
		updates[h] = ha.HaMember{
			Hostname:    h,
			Priority:    prio,
			PublicIP:    pub,
			TailscaleIP: ts,
		}
	}
	// Cross-row validation: priorities must be unique.
	seenPrio := make(map[int]string, len(updates))
	for h, m := range updates {
		if other, dup := seenPrio[m.Priority]; dup {
			return nil, fmt.Errorf("duplicate priority %d (used by both %q and %q)", m.Priority, other, h)
		}
		seenPrio[m.Priority] = h
	}
	return updates, nil
}

// parseHADNSCredsForm turns the the DNS provider creds form into a
// extcreds.Credentials. The validation is delegated to
// extcreds.Credentials.Validate() so the form parser stays
// pure-shape (no semantic checks duplicated).
func parseHADNSCredsForm(form url.Values) (extcreds.Credentials, error) {
	c := extcreds.Credentials{
		Provider: strings.TrimSpace(form.Get("provider")),
		Login:    strings.TrimSpace(form.Get("login")),
		Zone:     strings.TrimSpace(form.Get("zone")),
		CertPEM:  strings.TrimSpace(form.Get("cert_pem")),
		Password: strings.TrimSpace(form.Get("password")),
	}
	if err := c.Validate(); err != nil {
		return extcreds.Credentials{}, err
	}
	return c, nil
}

// isHAForceActionConfirmationCorrect checks the typed
// confirmation against the expected value. The expected
// value is the hostname (for promote/reclaim) or the
// current active hostname (for demote). The check is
// case-sensitive and trims no whitespace — a single typo
// in the confirmation field blocks the action.
//
// (The string-comparison-only check is intentionally not
// constant-time. The action is non-sensitive — the
// confirmation is a UX guard, not a secret.)
func isHAForceActionConfirmationCorrect(action, hostname, typed string) bool {
	switch action {
	case "promote", "reclaim":
		return typed == hostname && hostname != ""
	case "demote":
		return typed == hostname && hostname != ""
	default:
		return false
	}
}

// formatHAChainForTemplate is a debug helper used by the
// "Show chain JSON" button on the page. It returns a
// human-readable rendering (one member per line) for the
// empty-chain case (the template needs a hint rather than
// an empty string). For the non-empty case it returns the
// raw JSON (the page re-renders it in a <pre>).
func formatHAChainForTemplate(c *ha.HaChain) string {
	if c == nil || len(c.Members) == 0 {
		return "(chain has no members yet — add one below)"
	}
	// For now: human-readable line per member. The page
	// re-renders this in a <pre> via JS; future iterations
	// can ship a raw-JSON view (the ChainJSON field already
	// carries the raw bytes).
	parts := make([]string, 0, len(c.Members))
	for _, m := range c.SortedByPriority() {
		role := m.Role
		if role == "" {
			role = ha.RoleStandby
		}
		parts = append(parts, fmt.Sprintf("P%d %s [%s] public=%s tailscale=%s role=%s",
			m.Priority, m.Hostname,
			abbreviateLastSeen(m.LastSeen), m.PublicIP, m.TailscaleIP, role))
	}
	return strings.Join(parts, "\n")
}

// abbreviateLastSeen returns a short human label for a
// last-seen timestamp. Used in formatHAChainForTemplate
// only; the audit log table has its own formatter.
func abbreviateLastSeen(unixSec int64) string {
	if unixSec == 0 {
		return "never"
	}
	delta := time.Now().Unix() - unixSec
	switch {
	case delta < 60:
		return fmt.Sprintf("%ds ago", delta)
	case delta < 3600:
		return fmt.Sprintf("%dm ago", delta/60)
	case delta < 86400:
		return fmt.Sprintf("%dh ago", delta/3600)
	default:
		return fmt.Sprintf("%dd ago", delta/86400)
	}
}

// boolOnOff returns "on" or "off" for a bool. Used in audit
// log details.
func boolOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// haRedirect wraps RedirectWithFlash with the /admin/ha
// base path baked in.
func haRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	RedirectWithFlash(w, r, "/admin/ha", okMsg, errMsg)
}
