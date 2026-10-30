// relay_request_handlers_b440.go — B440 part 2: the HTTP surface, the page data and
// the runtime wiring of the request channel (the logic lives in relay_request_b440.go).
//
// Three actions, one decision:
//
//	POST /my/exit-rules/relay-request     a user asks for another relay for a rule
//	POST /admin/exit-rules/relay-request  the operator approves or rejects one ask
//	POST /admin/exit-rules/relay-policy   the operator sets the policy (global / per user)
//
// The data-plane change is the operator's own manual pin (prefixowner.SetManual) plus
// the SAME ACL re-apply the prefix page runs — one decision, one apply path, one audit
// trail, which is why this file calls Service.ReapplyACLIfDrifted instead of inventing
// a second pipeline.
//
// HONESTY NOTE (cross-user). Headscale serves a prefix from exactly ONE relay, so an
// approved request MOVES the prefix for EVERY user who has a rule on it. The admin card
// therefore lists the other users whose enabled rules touch the same addresses
// (relayRequestAffectedUsers) — the operator approves a cross-user change knowingly, not
// a private one.
package exit_rules

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/prefixowner"
)

// RelayRuleChoice is one of the user's rules in the request form's select.
type RelayRuleChoice struct {
	ID     int64
	Label  string
	Device string
	Target string
}

// RelayRequestRow is a stored request plus what the operator needs to judge it.
type RelayRequestRow struct {
	RelayRequest
	// AffectedUsers are the OTHER portal users whose enabled rules touch the same
	// prefixes: an approved request moves the prefix for all of them (one relay per
	// prefix), and a page that hid that would make a shared decision look private.
	AffectedUsers []string
	// AffectedLabel is AffectedUsers joined for display — computed here because the
	// template funcmap has no join helper and adding one would have to be mirrored in
	// every test funcmap for no gain.
	AffectedLabel string
	// StatusLabel is the translated status, resolved here with the same globals the
	// template funcmap's `t` reads: a template cannot call tf with a dynamic key.
	StatusLabel string
}

// RelayPolicyRow is one stored policy row, ready to render.
type RelayPolicyRow struct {
	Scope string
	Mode  string
}

// RelayCardData is everything either page needs to render the channel.
type RelayCardData struct {
	// PolicyMode is the EFFECTIVE mode for the viewer (the user's own row over the
	// global one on /my/exit-rules; the global row on the admin page).
	PolicyMode string
	// Relays are the relay hostnames a request may name (exit nodes with a per-node
	// tag; a relay without one cannot be pinned — B284).
	Relays []string
	// Rules are the viewer's requestable rules ("" on the admin page).
	Rules []RelayRuleChoice
	// Pending / Recent are the queue and the decided tail (admin page).
	Pending []RelayRequestRow
	Recent  []RelayRequestRow
	// Mine is this user's own request history (user page).
	Mine []RelayRequestRow
	// Users are the portal usernames the per-user policy may name (admin page).
	Users []string
	// Policies is every stored policy row and GlobalMode the global default.
	Policies   []RelayPolicyRow
	GlobalMode string
	// Modes are the selectable modes, in display order.
	Modes []string
	// FlashOK / FlashErr carry the redirect's sentence (B237.19 flash pattern: the
	// result travels in the query string, so a refresh does not re-submit the form).
	FlashOK  string
	FlashErr string
}

// relayRequestEnv builds the injected environment for this request's side effects.
// actor is the username recorded on the policy/audit rows.
func (s *Service) relayRequestEnv(actor string) RelayRequestEnv {
	return RelayRequestEnv{
		DB: s.dbc(),
		Apply: func(prefixes []string, relayHost string) error {
			return s.applyRelayPin(actor, prefixes, relayHost)
		},
		NotifyUser: func(userID int64, kind, severity, title, body, link string) {
			if s.dbc() == nil {
				return
			}
			if _, err := db.InsertNotification(s.dbc(), db.Notification{
				UserID: userID, Type: kind, Severity: severity,
				Title: title, Body: body, Link: link,
			}); err != nil {
				log.Printf("[relay-request] panel notification for user %d: %v", userID, err)
			}
		},
		NotifyAdmins: func(kind, severity, title, body, link string) {
			// Panel: one row per admin (delegated admins included, B264). Telegram: the
			// same admin alert channel the rest of this package uses — one message, not
			// one per admin.
			if s.dbc() != nil {
				for _, id := range adminUserIDs(s.dbc()) {
					if _, err := db.InsertNotification(s.dbc(), db.Notification{
						UserID: id, Type: kind, Severity: severity,
						Title: title, Body: body, Link: link,
					}); err != nil {
						log.Printf("[relay-request] panel notification for admin %d: %v", id, err)
					}
				}
			}
			if s.Notifier != nil {
				go s.Notifier.SendAlert(title + "\n" + body)
			}
		},
	}
}

// adminUserIDs lists the panel admins, so an operator-facing notification is written
// for every one of them (B264 delegation).
func adminUserIDs(d *sql.DB) []int64 {
	rows, err := d.Query(`SELECT id FROM portal_users WHERE is_admin = 1 ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// applyRelayPin is the ONLY place that turns an approval into a data-plane change:
// pin every prefix to the relay (prefix_owner stores the HOSTNAME — prefixowner.ViaForPrefix
// converts it to the tag the ACL needs), then re-apply the ACL through the shared
// throttled path.
func (s *Service) applyRelayPin(actor string, prefixes []string, relayHost string) error {
	host := NormalizeRelay(relayHost)
	if host == "" {
		return ErrRelayRequestNoRelay
	}
	if prefixowner.TagsByHost(s.dbc())[host] == "" {
		return fmt.Errorf("relay %q carries no per-node tag, so nothing can be pinned to it (B284)", host)
	}
	clean := cleanPrefixes(prefixes)
	for _, p := range clean {
		if err := prefixowner.SetManual(s.dbc(), p, host); err != nil {
			return fmt.Errorf("pin %s → %s: %w", p, host, err)
		}
	}
	s.ReapplyACLIfDrifted(actor, fmt.Sprintf("relay request approved: %d prefix(es) → %s", len(clean), host))
	// B441 (2026-10-29): the ACL is only the CONTROL plane. `via` tells a client which
	// relay it MAY use; the prefix still has to be advertised by that relay, and a relay
	// whose rules do not name these prefixes only starts advertising them after a sync
	// pass that reads the assignment table. Without this push an approved request moved
	// the permission and left the route where it was — the client would be pinned to a
	// relay that is not serving the prefix yet. In a goroutine because the sync applies
	// routes over SSH, one relay at a time, and an HTTP handler must not wait for that.
	if s.SyncRoutes != nil {
		go func() {
			res := s.SyncRoutes()
			log.Printf("[relay-request] route sync after pinning %d prefix(es) to %s: %d relay(s) touched",
				len(clean), host, len(res))
		}()
	}
	return nil
}

// relayChoices lists the relays a request may name: exit nodes that carry a per-node
// tag. A relay without a tag cannot be pinned (B284), so offering it would only produce
// a request that cannot be applied.
func (s *Service) relayChoices() []string {
	out := []string{}
	if s.HS == nil {
		return out
	}
	tagsByHost := prefixowner.TagsByHost(s.dbc())
	nodes, err := s.HS.ListAllNodes()
	if err != nil {
		log.Printf("[relay-request] list nodes for the relay select: %v", err)
		return out
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		if !n.IsExitNode {
			continue
		}
		host := NormalizeRelay(n.Hostname)
		if host == "" || seen[host] || tagsByHost[host] == "" {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

// relayRequestPrefixes resolves the addresses a rule actually moves: the target itself
// for a subnet/IP rule, every resolved range for a domain rule (including the CDN alias,
// through the shared lookup). A domain that is not resolved yet yields its derived
// per-range rows instead of an empty ask.
func (s *Service) relayRequestPrefixes(userID, deviceID int64, exitNode, targetType, target string) []string {
	out := []string{}
	switch targetType {
	case "subnet", "ip":
		out = append(out, target)
	case "domain":
		if resolved, err := LoadResolvedByDomain(s.dbc()); err == nil {
			for prefix := range LookupResolvedForDomain(resolved, userID, deviceID, exitNode, target) {
				out = append(out, prefix)
			}
		}
		if len(out) == 0 {
			rows, err := s.dbc().Query(`SELECT target_value FROM device_rules
			                              WHERE user_id = $1 AND parent_domain = $2 AND enabled = 1
			                                AND target_type IN ('subnet','ip')`, userID, target)
			if err == nil {
				for rows.Next() {
					var v string
					if rows.Scan(&v) == nil {
						out = append(out, v)
					}
				}
				rows.Close()
			}
		}
	}
	return cleanPrefixes(out)
}

// relayRequestRules lists the viewer's requestable rules. Only rules the user owns are
// listed: asking to move somebody else's destination is the admin's call, not theirs.
//
// B449 (2026-10-30) — THE PLACEHOLDER WAS SQLITE'S. The query was written with `?`, which is
// SQLite syntax; PostgreSQL parses it as an operator and answers
//
//	ERROR: syntax error at or near "AND" (SQLSTATE 42601)
//
// so the request form listed NO rules for any user while the journal carried that line on
// every page load. Every other query in this project uses `$n` (the dialect helpers rewrite
// nothing), and this file now matches them.
func (s *Service) relayRequestRules(userID int64) []RelayRuleChoice {
	out := []RelayRuleChoice{}
	rows, err := s.dbc().Query(`SELECT id, device_hostname, target_type, target_value FROM device_rules
	                              WHERE user_id = $1 AND enabled = 1
	                              ORDER BY device_hostname, target_value, id LIMIT 300`, userID)
	if err != nil {
		log.Printf("[relay-request] list the user's rules: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r RelayRuleChoice
		var targetType string
		if rows.Scan(&r.ID, &r.Device, &targetType, &r.Target) != nil {
			continue
		}
		// The select's label is the device and the destination: the user picks the
		// rule they are thinking about, not a numeric id.
		r.Label = fmt.Sprintf("%s → %s", r.Device, r.Target)
		if r.Device == "" {
			r.Label = fmt.Sprintf("#%d → %s", r.ID, r.Target)
		}
		out = append(out, r)
	}
	return out
}

// relayRequestAffectedUsers names the OTHER users whose enabled rules touch these
// addresses — the cross-user fact an approval would change for them too.
func (s *Service) relayRequestAffectedUsers(prefixes []string) []string {
	if len(prefixes) == 0 {
		return nil
	}
	marks := make([]string, 0, len(prefixes))
	args := make([]any, 0, len(prefixes))
	for _, p := range prefixes {
		marks = append(marks, "?")
		args = append(args, p)
	}
	q := `SELECT DISTINCT username FROM device_rules
	       WHERE enabled = 1 AND target_value IN (` + strings.Join(marks, ",") + `)
	       ORDER BY username`
	rows, err := s.dbc().Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && strings.TrimSpace(name) != "" {
			out = append(out, name)
		}
	}
	return out
}

// decorateRelayRequests attaches the cross-user list to each row.
func (s *Service) decorateRelayRequests(in []RelayRequest) []RelayRequestRow {
	out := make([]RelayRequestRow, 0, len(in))
	for _, r := range in {
		// The requester is not "affected by others": the list answers «кого ещё это
		// заденет», and a row that names the asker would hide the cross-user fact.
		affected := make([]string, 0, 4)
		for _, name := range s.relayRequestAffectedUsers(r.Prefixes) {
			if !strings.EqualFold(name, r.Username) {
				affected = append(affected, name)
			}
		}
		out = append(out, RelayRequestRow{
			RelayRequest: r, AffectedUsers: affected, AffectedLabel: strings.Join(affected, ", "),
			StatusLabel: relayStatusLabel(r.Status),
		})
	}
	return out
}

// relayModes is the display order of the policy select.
func relayModes() []string {
	return []string{RelayPolicyRequireApproval, RelayPolicyAutoApprove, RelayPolicyDeny}
}

// myRelayRequestCard fills the user-page card.
func (s *Service) myRelayRequestCard(c *auth.Claims, r *http.Request) RelayCardData {
	card := RelayCardData{
		Relays:   s.relayChoices(),
		Rules:    s.relayRequestRules(c.UserID),
		Modes:    relayModes(),
		FlashOK:  r.URL.Query().Get("relay_ok"),
		FlashErr: r.URL.Query().Get("relay_err"),
	}
	if mode, err := GetRelayPolicy(s.dbc(), c.Username); err == nil {
		card.PolicyMode = mode
	} else {
		card.PolicyMode = RelayPolicyRequireApproval
	}
	if mine, err := ListRelayRequestsByUser(s.dbc(), c.UserID, 50); err == nil {
		card.Mine = s.decorateRelayRequests(mine)
	}
	return card
}

// adminRelayRequestCard fills the admin-page card: the queue, the decided tail, and
// every stored policy so the operator sees what is in force per scope.
func (s *Service) adminRelayRequestCard(r *http.Request) RelayCardData {
	card := RelayCardData{
		Modes:    relayModes(),
		FlashOK:  r.URL.Query().Get("relay_ok"),
		FlashErr: r.URL.Query().Get("relay_err"),
	}
	if pending, err := ListRelayRequests(s.dbc(), RelayRequestPending, 100); err == nil {
		card.Pending = s.decorateRelayRequests(pending)
	}
	if recent, err := ListRelayRequests(s.dbc(), "", 40); err == nil {
		decided := make([]RelayRequest, 0, len(recent))
		for _, req := range recent {
			if req.Status != RelayRequestPending {
				decided = append(decided, req)
			}
		}
		card.Recent = s.decorateRelayRequests(decided)
	}
	if policies, err := ListRelayPolicies(s.dbc()); err == nil {
		scopes := make([]string, 0, len(policies))
		for scope := range policies {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		for _, scope := range scopes {
			card.Policies = append(card.Policies, RelayPolicyRow{Scope: scope, Mode: policies[scope]})
		}
	}
	if global, err := GetRelayPolicy(s.dbc(), ""); err == nil {
		card.GlobalMode = global
	} else {
		card.GlobalMode = RelayPolicyRequireApproval
	}
	card.PolicyMode = card.GlobalMode
	card.Relays = s.relayChoices()
	if users, err := db.GetAllPortalUsers(s.dbc()); err == nil {
		names := make([]string, 0, len(users))
		for _, u := range users {
			if strings.TrimSpace(u.Username) != "" {
				names = append(names, u.Username)
			}
		}
		sort.Strings(names)
		card.Users = names
	}
	return card
}

// PostMyRelayRequest handles the user's ask.
func (s *Service) PostMyRelayRequest(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	back := "/my/exit-rules"
	ruleID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("rule_id")), 10, 64)
	toRelay := strings.TrimSpace(r.FormValue("to_relay"))
	if ruleID <= 0 || toRelay == "" {
		http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(s.I18n.T(lang, "exit_rules.relay_request_bad_form")), http.StatusSeeOther)
		return
	}
	var deviceHost, target, kind, ruleRelay string
	var deviceID int64
	if err := s.dbc().QueryRow(`SELECT device_hostname, target_value, target_type, COALESCE(device_id,0), exit_node_id
	                              FROM device_rules WHERE id = $1 AND user_id = $2 AND enabled = 1`, ruleID, c.UserID).
		Scan(&deviceHost, &target, &kind, &deviceID, &ruleRelay); err != nil {
		http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(s.I18n.T(lang, "exit_rules.relay_request_not_yours")), http.StatusSeeOther)
		return
	}
	prefixes := s.relayRequestPrefixes(c.UserID, deviceID, ruleRelay, kind, target)
	if len(prefixes) == 0 {
		http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(s.I18n.T(lang, "exit_rules.relay_request_no_prefixes")), http.StatusSeeOther)
		return
	}
	owner := NormalizeRelay(prefixowner.OwnerByPrefix(s.dbc())[prefixes[0]])
	out, err := CreateRelayRequest(s.relayRequestEnv(c.Username), RelayRequestInput{
		UserID: c.UserID, Username: c.Username, DeviceHostname: deviceHost,
		FromRelay: owner, ToRelay: toRelay, Prefixes: prefixes,
		Note: strings.TrimSpace(r.FormValue("note")),
	})
	if err != nil {
		msg := s.I18n.T(lang, "exit_rules.relay_request_error")
		switch err {
		case ErrRelayRequestDenied:
			msg = s.I18n.T(lang, "exit_rules.relay_request_denied")
		case ErrRelayRequestSameRelay:
			msg = s.I18n.T(lang, "exit_rules.relay_request_same")
		case ErrRelayRequestNoPrefixes:
			msg = s.I18n.T(lang, "exit_rules.relay_request_no_prefixes")
		}
		s.Backend.Audit(c.UserID, c.Username, "relay_request_refused",
			fmt.Sprintf("rule=%d to=%s err=%v", ruleID, toRelay, err))
		http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	action := "relay_request_created"
	if out.Request.Status == RelayRequestAutoApproved {
		action = "relay_request_auto_approved"
	}
	s.Backend.Audit(c.UserID, c.Username, action,
		fmt.Sprintf("request=%d device=%s from=%s to=%s prefixes=%d", out.Request.ID, deviceHost, owner, toRelay, len(prefixes)))
	msg := s.I18n.T(lang, "exit_rules.relay_request_sent_flash")
	if out.Request.Status == RelayRequestAutoApproved {
		msg = s.I18n.Tf(lang, "exit_rules.relay_request_auto_flash", toRelay)
	}
	http.Redirect(w, r, back+"?relay_ok="+url.QueryEscape(msg), http.StatusSeeOther)
}

// PostAdminRelayRequestDecide approves or rejects one pending ask.
func (s *Service) PostAdminRelayRequestDecide(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	back := "/admin/exit-rules"
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	decision := strings.TrimSpace(r.FormValue("decision"))
	note := strings.TrimSpace(r.FormValue("note"))
	if id <= 0 || (decision != "approve" && decision != "reject") {
		http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(s.I18n.T(lang, "exit_rules_admin.relay_request_bad_form")), http.StatusSeeOther)
		return
	}
	req, err := DecideRelayRequest(s.relayRequestEnv(c.Username), id, decision == "approve", c.Username, note)
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "relay_request_decide_failed",
			fmt.Sprintf("request=%d decision=%s err=%v", id, decision, err))
		http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "relay_request_"+req.Status,
		fmt.Sprintf("request=%d user=%s to=%s prefixes=%d note=%q", req.ID, req.Username, req.ToRelay, len(req.Prefixes), note))
	http.Redirect(w, r, back+"?relay_ok="+url.QueryEscape(s.I18n.Tf(lang, "exit_rules_admin.relay_request_decided_flash",
		req.Username, s.I18n.T(lang, "exit_rules.relay_request_status_"+req.Status))), http.StatusSeeOther)
}

// PostAdminRelayPolicy stores the policy for one scope ("" username = global).
func (s *Service) PostAdminRelayPolicy(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	back := "/admin/exit-rules"
	username := strings.TrimSpace(r.FormValue("username"))
	mode := strings.TrimSpace(r.FormValue("mode"))
	// Clearing a scope is how the operator hands a user back to the global policy: an
	// explicit "" is stored as its own mode ("inherit"), never as a silent default.
	if mode == "inherit" {
		mode = ""
	}
	if mode == "" {
		if err := DeleteRelayPolicy(s.dbc(), username); err != nil {
			http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(s.I18n.Tf(lang, "exit_rules_admin.relay_policy_error", err.Error())), http.StatusSeeOther)
			return
		}
		s.Backend.Audit(c.UserID, c.Username, "relay_policy_cleared", "scope="+relayScopeLabel(username))
		http.Redirect(w, r, back+"?relay_ok="+url.QueryEscape(s.I18n.Tf(lang, "exit_rules_admin.relay_policy_cleared", relayScopeLabel(username))), http.StatusSeeOther)
		return
	}
	if err := SetRelayPolicy(s.dbc(), username, mode, c.Username); err != nil {
		http.Redirect(w, r, back+"?relay_err="+url.QueryEscape(s.I18n.Tf(lang, "exit_rules_admin.relay_policy_error", err.Error())), http.StatusSeeOther)
		return
	}
	scope := relayScopeLabel(username)
	s.Backend.Audit(c.UserID, c.Username, "relay_policy_set", fmt.Sprintf("scope=%s mode=%s", scope, mode))
	http.Redirect(w, r, back+"?relay_ok="+url.QueryEscape(s.I18n.Tf(lang, "exit_rules_admin.relay_policy_saved", scope, mode)), http.StatusSeeOther)
}

// relayStatusLabel translates a stored status with the panel's current language.
func relayStatusLabel(status string) string {
	lang, _ := i18n.GlobalLang.Load().(string)
	return i18n.GlobalCatalog.T(lang, "exit_rules.relay_request_status_"+status)
}

// relayScopeLabel renders a policy scope for the audit row and the flash.
func relayScopeLabel(username string) string {
	u := strings.TrimSpace(strings.ToLower(username))
	if u == "" {
		return "global"
	}
	return "user:" + u
}
