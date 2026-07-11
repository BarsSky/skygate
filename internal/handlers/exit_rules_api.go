// exit_rules_api.go — extracted from exit_rules.go.
// Contains: GetExitRulesAPI, PostExitRulesAPI, GetExitRulesAPIHelp.
// REST/JSON API for AI assistants and external scripts. The /help page
// documents the API endpoints for users.

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"skygate/internal/db"
)


// GetExitRulesAPI returns all rules for the current user as JSON.
// GET /my/exit-rules/api
func (a *App) GetExitRulesAPI(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Error(w, `{"error":"unauthorized"}`, 401)
		return
	}
	rules, err := a.getDeviceRules(c.UserID)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, 500)
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
// Body: {"rules": [{"device_id":2,"exit_node":"karolina","target_type":"ip","target_value":"8.8.8.8","action":"accept"}, ...]}
func (a *App) PostExitRulesAPI(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Error(w, `{"error":"unauthorized"}`, 401)
		return
	}
	var req struct {
		Rules []apiRule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json: `+err.Error()+`"}`, 400)
		return
	}
	if len(req.Rules) == 0 {
		http.Error(w, `{"error":"empty rules array"}`, 400)
		return
	}

	// Resolve device IPs + exit-node set from headscale.
	nodes, _ := a.HS.ListAllNodes()
	nodeIPs := map[int]string{}
	exitNodeSet := map[int]bool{}
	if nodes != nil {
		for _, n := range nodes {
			nid, _ := strconv.Atoi(n.ID)
			if len(n.IPAddresses) > 0 {
				nodeIPs[nid] = n.IPAddresses[0]
			}
			if n.IsExitNode {
				exitNodeSet[nid] = true
			}
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
	snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(a.DB, c.Username)
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
	maxTotal := a.Cfg.MaxTotalRules
	if maxTotal > 0 {
		// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledRules
		currentTotal, _ := db.CountEnabledRules(a.DB)
		if currentTotal+len(req.Rules) > maxTotal {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
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
		maxPerDevice := a.Cfg.MaxRulesPerDevice
		if maxPerDevice > 0 {
			// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledRulesForDevice
			deviceRuleCount, _ := db.CountEnabledRulesForDevice(a.DB, rl.DeviceID)
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
		ok, newID := a.insertRuleUnique(c.UserID, rl.DeviceID, rl.ExitNode, rl.TargetType, rl.TargetValue, rl.Action, deviceIP)
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
		if acl, err := a.GenerateACL(); err == nil {
			ver := a.saveACLSnapshot(acl, c.Username)
			if err := a.HS.SetPolicy(acl); err == nil {
				db.MarkACLApplied(a.DB, ver)
				db.AppendExitRuleLog(a.DB, ver, db.ExitRuleActionAPIBulk,
					fmt.Sprintf("user %s added %d rules via API", c.Username, added))
				// 2026-07-11: same operator-channel as the form path.
				if a.Notifier != nil {
					go a.Notifier.SendAlert(fmt.Sprintf("📥 Bulk add by %s: %d rules (api)", c.Username, added))
				}
				_ = a.SyncAdvertisedRoutes()
			} else {
				db.MarkACLFail(a.DB, ver, err.Error())
				if a.Notifier != nil {
					go a.Notifier.SendAlert(fmt.Sprintf("❌ ACL bulk-apply failed (by %s, %d rules)\n  err: %v",
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
func (a *App) GetExitRulesAPIHelp(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	a.renderWithLayout(w, r, "exit_rules_help.html", c, map[string]any{
		"Page":  "exit-rules",
		"Title": "Exit Rules API Help",
	})
}

