// Package exit_rules — store.go owns the DB / headscale
// helpers that the form_* + api.go handlers lean on.
//
// refactor-v0.30 Phase B step 4e (2026-07-29): moved from
// internal/handlers/exit_rules.go. The helpers used to be
// methods on *App; they now live on *Service. The apiRule
// JSON type also moved (the form_my and api.go handlers
// read/write it).
//
// generateACL is a thin wrapper around acl.GenerateACL
// (which is a free function in internal/acl so the bot
// can call it without *App). The wrapper exists for
// symmetry with the form code; the Service could call
// the free function directly, but keeping the wrapper
// preserves the App.GenerateACL signature in case a
// test or external caller needs it.

package exit_rules

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
)

// apiRule is the JSON structure for rule creation/listing.
type apiRule struct {
	ID          int    `json:"id,omitempty"`
	DeviceID    int    `json:"device_id"`
	DeviceName  string `json:"device_name,omitempty"`
	ExitNode    string `json:"exit_node"`
	TargetType  string `json:"target_type"` // "ip", "subnet", "domain"
	TargetValue string `json:"target_value"`
	Action      string `json:"action"` // "accept" or "deny"
	DeviceIP    string `json:"device_ip,omitempty"`
}

// insertRuleUnique returns:
//   (true, existingID) — rule already existed; do not re-insert.
//   (true, 0)          — new rule inserted successfully.
//   (false, 0)         — DB error.
//
// 2026-07-07: issue #5 — dedup protection.
// 2026-07-11: Этап 9 part 2 — the SELECT-then-INSERT pattern is
// now composed of db.FindDeviceRuleID + db.AppendDeviceRule so
// the SQL strings live in queries.go.
//
// 2026-07-28: parentDomain is now an explicit parameter. The
// form passes the ORIGINAL DOMAIN when inserting /32 rules
// derived from DNS resolution, so the autoupdater can find
// them on the next tick and update them in place.
//
// For manual subnet/IP rules (no DNS-resolve parent), pass "".
func (s *Service) insertRuleUnique(userID int64, deviceID int, exitNode, targetType, targetValue, action, deviceIP, parentDomain string) (bool, int) {
	existingID, err := db.FindDeviceRuleID(s.DB, userID, deviceID, exitNode, targetType, targetValue)
	if err == nil {
		return true, existingID
	}
	if !errors.Is(err, db.ErrNotFound) {
		return false, 0
	}
	// not found → insert. parentDomain is caller-supplied; the
	// form passes the original domain for DNS-resolved /32 rules.
	newID, err := db.AppendDeviceRule(s.DB, userID, deviceID, exitNode, targetType, targetValue, action, deviceIP, parentDomain, "", "")
	if err != nil {
		return false, 0
	}
	return true, int(newID)
}

// getDeviceRules returns the user's rules with the
// DeviceName field populated from headscale (matched on
// Tailscale IP). The DeviceName field still needs the
// headscale IP-to-hostname lookup, which is Service-level
// (not pure DB), so that part stays here.
func (s *Service) getDeviceRules(userID int64) ([]db.DeviceRule, error) {
	rr, err := db.GetDeviceRulesForUser(s.DB, userID)
	if err != nil {
		return nil, err
	}
	// Resolve device hostnames from headscale API — match by Tailscale IP.
	if nodes, e := s.HS.ListAllNodes(); e == nil {
		for i := range rr {
			if rr[i].DeviceIP == "" {
				continue
			}
			for _, n := range nodes {
				found := false
				for _, ip := range n.IPAddresses {
					if ip == rr[i].DeviceIP {
						hn := n.GivenName
						if hn == "" {
							hn = n.Hostname
						}
						rr[i].DeviceName = hn
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}
	}
	return rr, nil
}

// getUserDevices returns the user's devices. Falls back to
// headscale's NodeList() when the devices table has no rows
// for the user (a fresh user has no rows yet — the backfill
// on /my/devices adds them, but a test / first-load race
// could see 0 rows).
func (s *Service) getUserDevices(userID int) ([]map[string]any, error) {
	rows, err := s.DB.Query(db.QSelectUserDevices, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dd []map[string]any
	for rows.Next() {
		var id int
		var hn string
		var ls sql.NullInt64
		if err := rows.Scan(&id, &hn, &ls); err != nil {
			return nil, err
		}
		m := map[string]any{"id": id, "hostname": hn}
		if ls.Valid {
			m["last_seen"] = time.Unix(ls.Int64, 0).Format("2006-01-02 15:04")
		}
		dd = append(dd, m)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if len(dd) == 0 {
		if nodes, err := s.HS.NodeList(); err == nil {
			for _, n := range nodes {
				dd = append(dd, map[string]any{"id": n["id"], "hostname": n["hostname"], "is_hs": true})
			}
		}
	}
	return dd, nil
}

// generateACL is a thin wrapper around the acl policy
// builder. Returns the policy JSON to push to headscale.
//
// 2026-07-29: bug fix — honour SKYGATE_ACL_VIA_ENABLED
// the same way ApplyACLPipelineForPlane does. Pre-fix,
// the form_my / form_admin / api.go paths always called
// acl.GenerateACL (no via) regardless of the env var, so
// an operator who set SKYGATE_ACL_VIA_ENABLED=true saw the
// `via` field silently dropped the moment a user touched
// /my/exit-rules (the per-device-pref path uses
// ApplyACLPipelineForPlane which DOES honour the env var,
// so via would be set — then the next /my/exit-rules click
// would overwrite headscale with the no-via version).
// Symptom: skygate DB had `via:` in snapshot 1024, but
// headscale's current policy had 0 `via:` entries (the
// /my/exit-rules click after the per-device-pref change
// overwrote the with-via policy). Live verification: see
// the AGENTS.md "via: sync bug" note.
//
// Fix: read the env var and dispatch to the right
// generator. The with-via path produces an extra
// `via: ["<tag>"]` on each per-user + per-device grant
// (Tailscale's packet filter uses this to pin the device
// to the operator-chosen exit-node). When the env var is
// false (default), the behaviour is identical to
// pre-fix (the legacy no-via path).
func (s *Service) generateACL() (string, error) {
	if os.Getenv("SKYGATE_ACL_VIA_ENABLED") == "true" {
		return acl.GenerateACLWithVia(s.DB)
	}
	return acl.GenerateACL(s.DB)
}

// saveACLSnapshot persists one acl_snapshots row and
// returns the new version. Wraps acl.SaveACLSnapshot.
// The Service's Notifier is passed as the Alerter (it
// satisfies the interface implicitly via SendAlert).
// When Notifier is nil the free function skips the
// alert, matching the previous App behaviour.
func (s *Service) saveACLSnapshot(config, username string) int {
	return acl.SaveACLSnapshot(s.DB, config, username, s.Notifier)
}

// getMaxRulesForUser returns the per-user rule limit or
// the default. Reads SKYGATE_USER_MAX_RULES="user:N"
// overrides from the loaded config (s.Cfg.UserMaxRules).
//
// 2026-07-29: extracted from internal/handlers/handlers.go
// during Phase B step 4e. The form_my + form_admin
// handlers needed the per-user cap and the *App method
// was the wrong layer (App has no per-feature state).
// Moved verbatim: same fallback default
// (MaxRulesPerDevice, default 200), same env-var lookup
// via s.Cfg. The legacy App.getMaxRulesForUser wrapper
// is removed in step 4f when form_my.go moves to
// feature/exit_rules/ (the only remaining caller).
func (s *Service) getMaxRulesForUser(username string) int {
	if s.Cfg == nil {
		return 0
	}
	if v, ok := s.Cfg.UserMaxRules[username]; ok {
		return v
	}
	return s.Cfg.MaxRulesPerDevice
}

// mustCompile is a tiny compile-time assertion that
// the package fmt is reachable from this file. The
// form/api handlers use fmt.Sprintf + fmt.Errorf so
// the import is needed; the helpers above don't.
var _ = fmt.Sprintf

// readUserMaxRulesEnv is an env-var helper kept for
// documentation purposes — the *Cfg.UserMaxRules map is
// populated at boot from SKYGATE_USER_MAX_RULES by
// config.Load, so Service.getMaxRulesForUser never
// reads the env var directly. The helper below mirrors
// the original handler behaviour in case a future
// release needs the env-var path again (e.g. hot-reload
// without a service restart).
func readUserMaxRulesEnv(username string) (int, bool) {
	raw := os.Getenv("SKYGATE_USER_MAX_RULES")
	if raw == "" {
		return 0, false
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.Index(pair, ":")
		if idx <= 0 {
			continue
		}
		k, v := pair[:idx], pair[idx+1:]
		if strings.TrimSpace(k) != username {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
