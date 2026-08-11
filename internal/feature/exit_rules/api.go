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
	"net/http"
	"strconv"

	"skygate/internal/db"
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
	snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.DB, c.Username)
	for _, nid := range snapIDs {
		if n, err := strconv.Atoi(nid); err == nil {
			ownedByUser[n] = true
		}
	}

	added := 0
	addedIDs := []int{}
	dupCount := 0
	errors := []string{}
	// 2026-07-07: issue #12 — pre-check total limit before processing
	maxTotal := 0
	if s.Cfg != nil {
		maxTotal = s.Cfg.MaxTotalRules
	}
	if maxTotal > 0 {
		// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledRules
		currentTotal, _ := db.CountEnabledRules(s.DB)
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
		maxPerDevice := 0
		if s.Cfg != nil {
			maxPerDevice = s.Cfg.MaxRulesPerDevice
		}
		if maxPerDevice > 0 {
			// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledRulesForDevice
			deviceRuleCount, _ := db.CountEnabledRulesForDevice(s.DB, rl.DeviceID)
			if deviceRuleCount >= maxPerDevice {
				errors = append(errors, fmt.Sprintf("rule[%d]: device limit exceeded (%d/%d)", i, deviceRuleCount, maxPerDevice))
				continue
			}
		}
		if rl.DeviceID == 0 || rl.TargetValue == "" {
			errors = append(errors, fmt.Sprintf("rule[%d]: missing device_id or target_value", i))
			continue
		}
		// 2026-07-11: bug fix — reject rules for devices the caller doesn't
		// own, and reject rules targeting exit-nodes (they are routing
		// infrastructure, not source devices to attach rules to).
		if !ownedByUser[rl.DeviceID] {
			errors = append(errors, fmt.Sprintf("rule[%d]: device %d not owned by user", i, rl.DeviceID))
			continue
		}
		if exitNodeSet[rl.DeviceID] {
			errors = append(errors, fmt.Sprintf("rule[%d]: device %d is an exit-node", i, rl.DeviceID))
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
		ok, newID := s.insertRuleUnique(c.UserID, rl.DeviceID, rl.ExitNode, rl.TargetType, rl.TargetValue, rl.Action, deviceIP, apiParent)
		if !ok {
			errors = append(errors, fmt.Sprintf("rule[%d]: db error", i))
			continue
		}
		if newID == 0 {
			errors = append(errors, fmt.Sprintf("rule[%d]: insert returned no id", i))
			continue
		}
		addedIDs = append(addedIDs, newID)
		added++
	}

	// Apply ACL if anything was added
	if added > 0 {
		if acl, err := s.generateACL(); err == nil {
			ver := s.saveACLSnapshot(acl, c.Username)
			if err := s.HS.SetPolicy(acl); err == nil {
				db.MarkACLApplied(s.DB, ver)
				db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionAPIBulk,
					fmt.Sprintf("user %s added %d rules via API", c.Username, added))
				// 2026-07-11: same operator-channel as the form path.
				if s.Notifier != nil {
					go s.Notifier.SendAlert(fmt.Sprintf("📥 Bulk add by %s: %d rules (api)", c.Username, added))
				}
				// Trigger a sync of advertised-routes to all
				// exit-nodes (same as the form path).
				if s.SyncRoutes != nil {
					_ = s.SyncRoutes()
				}
			} else {
				db.MarkACLFail(s.DB, ver, err.Error())
				if s.Notifier != nil {
					go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL bulk-apply failed (by %s, %d rules)\n  err: %v",
						c.Username, added, err))
				}
			}
		}
	}

	resp := map[string]any{"added": added,
		"duplicates": dupCount, "errors": errors, "ids": addedIDs}
	if errors == nil {
		resp["errors"] = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetExitRulesAPIHelp renders the API documentation page.
// GET /my/exit-rules/help
func (s *Service) GetExitRulesAPIHelp(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.Backend.RenderWithLayout(w, r, "exit_rules_help.html", c, map[string]any{
		"Page":  "exit-rules",
		"Title": "Exit Rules API Help",
	})
}
