// Package exit_rules — agent_endpoint.go (B377, 2026-10-11).
//
// GET /my/exit-rules/agent — the AI-assistant bootstrap. The operator creates a
// personal API token on /my/tokens and copies ONE link into the chat; the
// assistant fetches it and receives the package built by agent_brief.go (role,
// credentials, API contract, the constraints that are not guessable, and the
// live fleet state).
//
// Authentication: the credential may arrive three ways, all resolved by the same
// handlers.currentUser the rest of the panel uses —
//
//  1. `Authorization: Bearer <token>` (how an assistant calls the API after the
//     first fetch),
//  2. a `skygate_session` cookie (a human opening the link in their browser),
//  3. `?token=<token>` — the paste-the-link flow, accepted for THIS path only.
//     That exception lives in handlers.currentUser next to the Bearer
//     implementation, so the bcrypt walk has one home and no other route can
//     quietly start accepting a token from a URL.
//
// Because of (3) the route is registered WITHOUT the session-shaped authMW (it
// would redirect an assistant's fetch to /login); this handler's own 401 is the
// gate.
//
// Secret hygiene, because the token travels in a URL:
//
//   - the response is `Cache-Control: no-store` and `Referrer-Policy: no-referrer`,
//     so a shared machine or a proxy cache does not keep the brief;
//   - the token itself is NEVER echoed into the body or any header (the link is
//     the credential, the package only says where it came from);
//   - the brief names the account and the write endpoint, nothing else: the
//     assistant gets the endpoints it needs, not a token-scanning surface;
//   - `last_used_at` is touched by the auth path exactly as for a Bearer call, so
//     the operator can see the assistant actually used the token, and revoking it
//     on /my/tokens is enough to cut the assistant off.
package exit_rules

import (
	"log"
	"net/http"
	"strings"
	"time"

	"skygate/internal/db"
)

// GetMyExitRulesAgentBrief serves the assistant package (Markdown by default,
// JSON with ?format=json).
//
// The path itself is pinned in cmd/skygate/routes.go (registration) and in
// handlers.currentUser (the ?token= allowance); this file deliberately does not
// declare a second copy of the literal, because an unexported const here could
// not be the one those two places read.
func (s *Service) GetMyExitRulesAgentBrief(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		// The link is followed by a tool, not a browser: answer in the format it
		// asked for instead of redirecting to /login (a redirect would look like
		// a successful fetch of an HTML login page).
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized: the token is missing, expired or revoked"}`))
			return
		}
		http.Error(w, "unauthorized: the token is missing, expired or revoked", http.StatusUnauthorized)
		return
	}

	snap := s.buildAgentSnapshot(r, c.Username, c.IsAdmin, c.UserID)

	if wantsJSON(r) {
		body, err := BuildAgentBriefJSON(snap)
		if err != nil {
			http.Error(w, "could not render the package: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		writeAgentBriefHeaders(w)
		_, _ = w.Write(body)
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	writeAgentBriefHeaders(w)
	_, _ = w.Write([]byte(BuildAgentBriefMarkdown(snap)))
}

// writeAgentBriefHeaders marks the response as a secret-bearing, never-cached
// document. Kept in one place so the JSON and Markdown paths cannot drift.
func writeAgentBriefHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// wantsJSON reports whether the caller asked for the machine-readable twin:
// ?format=json, or an Accept header that prefers JSON over Markdown/HTML.
func wantsJSON(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "json":
		return true
	case "md", "markdown", "text":
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" {
		return false
	}
	// curl's default is `*/*`; a tool that accepts JSON explicitly should get it.
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/markdown")
}

// buildAgentSnapshot collects the live state. Every read is best-effort: a page
// that cannot name a device still tells the assistant the ids, and a headscale
// outage must not turn the package into a 500 — the whole point is to hand a
// model what IS known plus the constraints it cannot infer.
func (s *Service) buildAgentSnapshot(r *http.Request, username string, isAdmin bool, userID int64) AgentSnapshot {
	snap := AgentSnapshot{
		BaseURL:      agentBaseURL(r),
		Username:     username,
		IsAdmin:      isAdmin,
		TokenFromURL: strings.TrimSpace(r.URL.Query().Get("token")) != "",
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Devices:      []AgentDeviceInfo{},
		ExitNodes:    []string{},
		Rules:        []AgentRuleInfo{},
	}

	limits, _ := s.effectiveRuleLimits(username)
	snap.Limits = AgentLimits{
		MaxPerDevice: limits.MaxPerDevice,
		MaxPerUser:   limits.MaxPerUser,
		MaxTotal:     limits.MaxTotal,
	}
	if s.I18n != nil { // not needed for the brief, but keeps the field honest
		_ = s.I18n
	}
	if u, err := db.CountEnabledNonSubnetRulesForUser(s.dbc(), userID); err == nil {
		snap.UsageUserFacing = u
	}
	if t, err := db.CountEnabledRules(s.dbc()); err == nil {
		snap.UsageTotal = t
	}

	if d := s.DomainResolveInterval(); d > 0 {
		snap.DomainResolveMins = int(d.Minutes())
	}

	// Devices: node_owner_map is the ownership source of truth (the same set the
	// ACL can match and the batch form accepts), so a device the assistant is
	// told about is a device it can actually use.
	ids, _, names := s.userDevicesForBulk(username)
	for _, id := range ids {
		hn := names[id]
		info := AgentDeviceInfo{ID: id, Hostname: hn}
		if n, err := db.CountEnabledRulesForDevice(s.dbc(), id); err == nil {
			info.Rules = n
		}
		if n, err := db.CountEnabledNonSubnetRulesForUserDevice(s.dbc(), userID, id); err == nil {
			info.UserFacing = n
		}
		if hn != "" {
			if pref, err := PreferredExitNodeForRule(s.dbc(), userID, hn); err == nil {
				info.PreferredExitNode = pref
				info.Pinned = pref != ""
			}
		}
		snap.Devices = append(snap.Devices, info)
	}

	if s.HS != nil {
		if nodes, err := s.HS.ListExitNodes(); err == nil {
			for _, n := range nodes {
				if n.Hostname != "" {
					snap.ExitNodes = append(snap.ExitNodes, n.Hostname)
				}
			}
		}
	}

	if rules, err := s.getDeviceRules(userID); err == nil {
		for _, rr := range rules {
			name := rr.DeviceName
			if name == "" {
				name = names[rr.DeviceID]
			}
			snap.Rules = append(snap.Rules, AgentRuleInfo{
				DeviceID:     rr.DeviceID,
				DeviceName:   name,
				ExitNode:     rr.ExitNodeID,
				TargetType:   rr.TargetType,
				TargetValue:  rr.TargetValue,
				Action:       rr.Action,
				ParentDomain: rr.ParentDomain,
				AllDevices:   rr.AllDevices,
			})
		}
	}

	log.Printf("exit-rules: agent package served to %s (devices=%d rules=%d token_from_url=%v)",
		username, len(snap.Devices), len(snap.Rules), snap.TokenFromURL)
	return snap
}
