// exit_nodes_stale_prefs_b356.go — B356 (2026-10-07) — the operator-visible half.
//
// /admin/exit-nodes already answers "which relay serves which prefix" (B275/B276) and
// "is the live policy the one we would generate" (B276/B288). It said NOTHING about
// `device_exit_node_prefs` — and that is where the operator's incident lived: two
// devices were pinned to a relay that had gone offline, the per-device
// `autogroup:internet` grant carried `via=[that tag]` (a permission FILTER since
// B265), and no page named the consequence.
//
// The rows below are the same judgement the reconciler makes, from the shared
// `exit_rules.StaleExitPrefReason` predicate, so the page cannot claim a device is
// fine while the engine is moving it. What differs is the ACTION the page can
// describe:
//
//   - derived (`set_by_user_id = 0`) → the reconciler re-points it on the next pass;
//     the page says what it will become.
//   - human (`set_by_user_id != 0`) → the engine deliberately refuses to rewrite it;
//     the page says so, with the relay's measured state, because a human pin to a
//     dead relay is exactly the state that broke the operator's device.
//
// Cost: one `OwnerTagCountsForDeviceRules` + one `NormalizeExitNodeTag` per
// preference row. Preference rows are single-digit in production (only devices an
// operator explicitly pinned), and this is an admin page, so the trade is a page that
// tells the truth over one that renders in 3ms less. Unit-tested pure predicate, thin
// DB shell.

package admin

import (
	"sort"
	"strings"

	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/monitoring"
)

// StaleExitPref is one stored preference the operator has to look at.
type StaleExitPref struct {
	Username       string
	DeviceHostname string
	PrefTag        string
	// RelayHostname / RelayState are what the B273 monitor last measured for the
	// relay the preference names ("" when there is no snapshot at all — the page
	// then says "unknown" instead of inventing a verdict).
	RelayHostname string
	RelayState    string
	RelayKnown    bool
	// HumanPinned is `set_by_user_id != 0`: a person chose this value, so the engine
	// will NOT rewrite it. SetByUserID is carried for the tooltip.
	HumanPinned bool
	SetByUserID int64
	// Reason is the shared, named verdict (`stale-pref-relay-unusable` /
	// `stale-pref-relay-owns-nothing` / `owner-overrides-rule-relay`); Cause is the
	// i18n selector for the sentence the page renders.
	Reason string
	Cause  string // "unusable" | "owner"
	// CandidateTag is the relay the data plane actually chose ("" when it could not
	// be determined — the page then tells the operator to pick one).
	CandidateTag string
}

// loadStaleExitPrefs returns every preference whose relay cannot serve the device,
// derived or human. Failures are non-fatal: a page that cannot judge a row must not
// 500, and the count is reported on the page itself.
func (s *Service) loadStaleExitPrefs() []StaleExitPref {
	prefs, err := db.ListAllDeviceExitNodePrefs(s.dbc())
	if err != nil || len(prefs) == 0 {
		return nil
	}
	healthByHost := map[string]db.ExitNodeHealth{}
	if rows, herr := db.ListExitNodeHealth(s.dbc()); herr == nil {
		for _, h := range rows {
			host := strings.ToLower(strings.TrimSpace(h.Hostname))
			if host != "" {
				healthByHost[host] = h
			}
		}
	}
	var out []StaleExitPref
	for _, p := range prefs {
		if p.ExitNodeTag == "" || p.DeviceHostname == "" {
			continue
		}
		relayHost := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(p.ExitNodeTag)))
		known, usable, state := false, false, ""
		if h, ok := healthByHost[relayHost]; ok {
			known, usable, state = true, monitoring.ExitNodeUsable(h.State), h.State
		}
		ownerTags := map[string]string{}
		candidate := ""
		if counts, cerr := db.OwnerTagCountsForDeviceRules(s.dbc(), p.UserID, p.DeviceHostname); cerr == nil {
			for owner := range counts {
				if tag, terr := db.NormalizeExitNodeTag(s.dbc(), owner); terr == nil && tag != "" {
					ownerTags[owner] = tag
					candidate = tag
				}
			}
		}
		reason, stale := exit_rules.StaleExitPrefReason(p.ExitNodeTag, known, usable, ownerTags)
		if !stale {
			continue
		}
		cause := "owner"
		if reason == exit_rules.StalePrefRelayUnusable {
			cause = "unusable"
		}
		out = append(out, StaleExitPref{
			Username:       p.Username,
			DeviceHostname: p.DeviceHostname,
			PrefTag:        p.ExitNodeTag,
			RelayHostname:  relayHost,
			RelayState:     state,
			RelayKnown:     known,
			HumanPinned:    p.SetByUserID != 0,
			SetByUserID:    p.SetByUserID,
			Reason:         reason,
			Cause:          cause,
			CandidateTag:   candidate,
		})
	}
	// Stable order so the page does not reshuffle between two loads of the same
	// state (the table is read by a human comparing it with a screenshot).
	sort.Slice(out, func(i, j int) bool {
		if out[i].Username != out[j].Username {
			return out[i].Username < out[j].Username
		}
		return out[i].DeviceHostname < out[j].DeviceHostname
	})
	return out
}

// countHumanStalePrefs is the half of the banner that needs an operator decision:
// the engine will not touch these rows, so the device keeps working only because the
// ACL fell back to the unpinned grant.
func countHumanStalePrefs(rows []StaleExitPref) int {
	n := 0
	for _, r := range rows {
		if r.HumanPinned {
			n++
		}
	}
	return n
}
