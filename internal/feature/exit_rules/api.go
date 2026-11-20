// Package exit_rules — api.go owns the public REST API
// for the exit-rules feature.
//
// refactor-v0.30 Phase B step 4e (2026-07-29): moved
// from internal/handlers/exit_rules_api.go. The
// handlers used to be methods on *App; they now live
// on *Service. The /help page is still a Go template
// (exit_rules_help.html) and renders via the Backend.
//
// REST/JSON API for AI assistants and external scripts.
// The /help page documents the API endpoints for users.
package exit_rules

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// GetExitRulesAPI returns all rules for the current user as JSON.
// GET /my/exit-rules/api
func (s *Service) GetExitRulesAPI(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	rules, err := s.getDeviceRules(c.UserID)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	var out []apiRule
	for _, rl := range rules {
		out = append(out, apiRule{
			ID:          rl.ID,
			DeviceID:    rl.DeviceID,
			DeviceName:  rl.DeviceName,
			ExitNode:    rl.ExitNodeID,
			TargetType:  rl.TargetType,
			TargetValue: rl.TargetValue,
			Action:      rl.Action,
			DeviceIP:    rl.DeviceIP,
			// B377 follow-up: the documented field is now actually returned, so a
			// client (or an AI assistant) can tell a hand-typed /32 from one the
			// auto-updater derived (parent_domain = the domain) and from the
			// ranges a CDN expansion produced (parent_domain = "cdn:<p>:<domain>").
			ParentDomain: rl.ParentDomain,
			// B380: the operator's own group name travels with the rule, so a client
			// can render the same grouping the page does.
			Group: rl.GroupLabel,
		})
	}
	if out == nil {
		out = []apiRule{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"rules": out})
}

// PostExitRulesAPI creates one or more rules from JSON body.
// POST /my/exit-rules/api
// Body: {"rules": [{"device_id":2,"exit_node":"relay-3","target_type":"ip","target_value":"8.8.8.8","action":"accept"}, ...]}
func (s *Service) PostExitRulesAPI(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var req struct {
		Rules []apiRule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if len(req.Rules) == 0 {
		http.Error(w, `{"error":"empty rules array"}`, http.StatusBadRequest)
		return
	}

	// Resolve device IPs + exit-node set from headscale.
	nodes, _ := s.HS.ListAllNodes()
	nodeIPs := map[int]string{}
	exitNodeSet := map[int]bool{}
	// range over nil is a no-op, so the explicit nil check is
	// unnecessary (staticcheck S1031).
	for _, n := range nodes {
		nid, _ := strconv.Atoi(n.ID)
		if len(n.IPAddresses) > 0 {
			nodeIPs[nid] = n.IPAddresses[0]
		}
		if n.IsExitNode {
			exitNodeSet[nid] = true
		}
	}
	// 2026-07-11: bug fix — only the user's own devices can be rule sources.
	// Prevents the API consumer (AI assistant) from assigning rules to
	// another user's device via the user_id of the API caller. node_owner_map
	// is the source of truth for ownership; headscale's user_name is unreliable
	// once a tag has been applied (headscale reassigns to "tagged-devices").
	// 2026-07-12: Этап 10 part 4 — moved to
	// db.ListNodeOwnerNodeIDsByUsername.
	ownedByUser := map[int]bool{}
	snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), c.Username)
	for _, nid := range snapIDs {
		if n, err := strconv.Atoi(nid); err == nil {
			ownedByUser[n] = true
		}
	}

	added := 0
	created := 0
	addedIDs := []int{}
	dupCount := 0
	errors := []string{}
	// B507: one entry per SUBMITTED rule, so a partial success can be matched back
	// to the plan the client sent. The parallel arrays above stay for compatibility
	// (ids/errors still line up with the old contract), but a client that needs to
	// know WHICH row became what reads `results`.
	type rowResult struct {
		Index  int    `json:"index"`
		ID     int    `json:"id,omitempty"`
		Status string `json:"status"` // created | existing | error
		Reason string `json:"reason,omitempty"`
	}
	results := make([]rowResult, 0, len(req.Rules))
	// B507: relays that actually exist, so a typo is refused WITH the list instead
	// of becoming an inert rule. A headscale failure here degrades the check to
	// "cannot verify" rather than blocking the whole batch (the writer still runs).
	var exitNodeNames []string
	if ex, exErr := s.HS.ListExitNodes(); exErr == nil {
		for _, n := range ex {
			if h := strings.TrimSpace(n.Hostname); h != "" {
				exitNodeNames = append(exitNodeNames, h)
			}
		}
	} else {
		log.Printf("exit-rules API: cannot list exit nodes (%v) — the exit_node field will be accepted unverified", exErr)
	}
	// 2026-07-07: issue #12 — pre-check total limit before processing.
	// B328: the caps come from the shared resolver (global_settings > .env > default) so
	// the API and both web forms cannot drift apart on what the limit IS.
	limits, _ := s.effectiveRuleLimits(c.Username)
	maxTotal := limits.MaxTotal
	if maxTotal > 0 {
		// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledRules
		currentTotal, _ := db.CountEnabledRules(s.dbc())
		if currentTotal+len(req.Rules) > maxTotal {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"error":     fmt.Sprintf("system limit exceeded: %d/%d", currentTotal, maxTotal),
				"current":   currentTotal,
				"max":       maxTotal,
				"requested": len(req.Rules),
			})
			return
		}
	}
	for i, rl := range req.Rules {
		// 2026-07-07: per-device limit
		maxPerDevice := limits.MaxPerDevice
		if maxPerDevice > 0 {
			// B328: the SAME unit of measure as both web forms and the usage panel —
			// this caller's user-facing (non-subnet, non-derived) rules on this
			// device. Pre-B328 the API used db.CountEnabledRulesForDevice, which
			// counts EVERY enabled row including the 454 derived subnet ranges
			// system-wide: live, device 9 held 263 such rows while the page showed
			// 33/500, so the API would have refused at 500 where the panel said
			// there were 467 slots left. One cap, one unit.
			deviceRuleCount := countUserFacingForUserDevice(s.dbc(), c.UserID, rl.DeviceID)
			if deviceRuleCount >= maxPerDevice {
				errors = append(errors, fmt.Sprintf("rule[%d]: device limit exceeded (%d/%d)", i, deviceRuleCount, maxPerDevice))
				continue
			}
		}
		if rl.DeviceID == 0 || rl.TargetValue == "" {
			reason := fmt.Sprintf("rule[%d]: missing device_id or target_value", i)
			errors = append(errors, reason)
			results = append(results, rowResult{Index: i, Status: "error", Reason: reason})
			continue
		}
		// B507: validate the ROW, not just its presence. Measured on a live API run:
		// `subnet 1.2.3.4/99`, `domain mega..nz` and a misspelled relay all went to
		// the database and became inert rules with no error anywhere.
		if v := validateRuleTarget(rl.TargetType, rl.TargetValue); !v.OK {
			reason := fmt.Sprintf("rule[%d]: %s", i, v.Reason)
			errors = append(errors, reason)
			results = append(results, rowResult{Index: i, Status: "error", Reason: reason})
			continue
		}
		if v := validateRuleAction(rl.Action); !v.OK {
			reason := fmt.Sprintf("rule[%d]: %s", i, v.Reason)
			errors = append(errors, reason)
			results = append(results, rowResult{Index: i, Status: "error", Reason: reason})
			continue
		}
		if v := validateExitNode(rl.ExitNode, exitNodeNames); !v.OK {
			reason := fmt.Sprintf("rule[%d]: %s", i, v.Reason)
			errors = append(errors, reason)
			results = append(results, rowResult{Index: i, Status: "error", Reason: reason})
			continue
		}
		// 2026-07-11: bug fix — reject rules for devices the caller doesn't
		// own, and reject rules targeting exit-nodes (they are routing
		// infrastructure, not source devices to attach rules to).
		if !ownedByUser[rl.DeviceID] {
			reason := fmt.Sprintf("rule[%d]: device %d not owned by user", i, rl.DeviceID)
			errors = append(errors, reason)
			results = append(results, rowResult{Index: i, Status: "error", Reason: reason})
			continue
		}
		if exitNodeSet[rl.DeviceID] {
			reason := fmt.Sprintf("rule[%d]: device %d is an exit-node", i, rl.DeviceID)
			errors = append(errors, reason)
			results = append(results, rowResult{Index: i, Status: "error", Reason: reason})
			continue
		}
		if rl.Action == "" {
			rl.Action = "accept"
		}
		deviceIP := nodeIPs[rl.DeviceID]
		// 2026-07-28: API doesn't do DNS resolution, so domain
		// rules land with parent_domain=rl.TargetValue (for the
		// autoupdater to find them). Subnet/IP rules pass "" —
		// the user typed them manually and the autoupdater
		// should not touch them.
		apiParent := ""
		if rl.TargetType == "domain" {
			apiParent = rl.TargetValue
		}
		ins := s.insertRuleUnique(c.UserID, rl.DeviceID, rl.ExitNode, rl.TargetType, rl.TargetValue, rl.Action, deviceIP, apiParent, NormaliseGroupLabel(rl.Group))
		status, reason := ruleStatusFor(ins)
		if status == "error" {
			// B507: the reason travels with the row. The B480 case (the target is
			// reserved by ANOTHER relay) used to be reported as a successful insert,
			// so the assistant believed it had pinned a relay that would never carry
			// the traffic.
			errors = append(errors, fmt.Sprintf("rule[%d]: %s", i, reason))
			results = append(results, rowResult{Index: i, ID: ins.ID, Status: "error", Reason: reason})
			continue
		}
		results = append(results, rowResult{Index: i, ID: ins.ID, Status: status})
		if status == "existing" {
			// B507: `duplicates` used to be declared 0 and never incremented, so an
			// idempotent re-send (the agent package PROMISES it is safe) looked like a
			// fresh insert. `added` keeps its old meaning — rows this call accepted —
			// while `created`/`existing` say which kind they were.
			dupCount++
			added++
			continue
		}
		addedIDs = append(addedIDs, ins.ID)
		added++
		created++
	}

	// Apply the ACL when a row was actually WRITTEN. An idempotent re-send changed
	// nothing, so re-applying would be a no-op apply with a new snapshot version.
	//
	// B507: and the outcome of that apply now travels in the answer. Before this,
	// a failed SetPolicy still produced `200 {"added": N}` — the assistant, and any
	// script, read that as "the rules are in force" while the tailnet policy still
	// said the opposite; the failure existed only in Telegram and the ACL history.
	aclApplied := false
	aclVersion := 0
	aclError := ""
	if created > 0 {
		acl, gerr := s.generateACL()
		if gerr != nil {
			aclError = "generate ACL: " + gerr.Error()
		} else {
			ver := s.saveACLSnapshot(acl, c.Username)
			aclVersion = ver
			if err := s.HS.SetPolicy(acl); err == nil {
				aclApplied = true
				db.MarkACLApplied(s.dbc(), ver)
				db.AppendExitRuleLog(s.dbc(), ver, db.ExitRuleActionAPIBulk,
					fmt.Sprintf("user %s added %d rules via API", c.Username, created))
				// 2026-07-11: same operator-channel as the form path.
				if s.Notifier != nil {
					go s.Notifier.SendAlert(i18n.Tf(i18n.LangRU, "alert.rules.bulk_added_api", c.Username, created))
				}
				// Trigger a sync of advertised-routes to all
				// exit-nodes (same as the form path).
				if s.SyncRoutes != nil {
					_ = s.SyncRoutes()
				}
			} else {
				aclError = err.Error()
				db.MarkACLFail(s.dbc(), ver, err.Error())
				if s.Notifier != nil {
					go s.Notifier.SendAlert(i18n.Tf(i18n.LangRU, "alert.acl.bulk_apply_failed", c.Username, created, err))
				}
			}
		}
	}

	ok := aclError == ""
	resp := map[string]any{
		"ok": ok,
		// `added` keeps its historical meaning — rows this call accepted — while the
		// two new counters say WHICH KIND they were. A retry of the same batch is
		// `added=N, created=0, existing=N`, which is the truth the agent package
		// already promised ("re-sending is safe").
		"added":      added,
		"created":    created,
		"existing":   dupCount,
		"duplicates": dupCount,
		"errors":     errors,
		"ids":        addedIDs,
		"results":    results,
		"acl": map[string]any{
			"applied": aclApplied,
			"version": aclVersion,
			"error":   aclError,
			// "not_needed" is honest for a batch that wrote nothing (a pure retry):
			// the policy did not change, so nothing had to be applied.
			"status": aclStatus(created, aclApplied, aclError),
		},
	}
	if errors == nil {
		resp["errors"] = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	// A failed apply is not a client error — the rows WERE written — but it is not
	// success either, and a 200 is what made the lie possible. 502 says "the write
	// happened, the thing you asked for is not in force".
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
	}
	json.NewEncoder(w).Encode(resp)
}

// aclStatus names the three states a client must be able to tell apart.
func aclStatus(created int, applied bool, aclError string) string {
	switch {
	case aclError != "":
		return "failed"
	case created == 0:
		return "not_needed"
	case applied:
		return "applied"
	}
	return "not_needed"
}

// GetExitRulesAPIHelp renders the API documentation page.
// GET /my/exit-rules/help
//
// B377: BaseURL is passed explicitly. The page has printed `{{.BaseURL}}` in
// several places since it was written and NOTHING ever injected it (the render
// helper does not know the request's origin), so every curl example on the page
// showed a bare path while the i18n string printed the literal `{{.BaseURL}}` —
// the page that teaches the API was the one place the base URL was missing, and
// the new agent-link section would have inherited the same hole.
func (s *Service) GetExitRulesAPIHelp(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.Backend.RenderWithLayout(w, r, "exit_rules_help.html", c, map[string]any{
		"Page":    "exit-rules",
		"Title":   "Exit Rules API Help",
		"BaseURL": agentBaseURL(r),
	})
}
