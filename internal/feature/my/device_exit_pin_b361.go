// device_exit_pin_b361.go — B361 (2026-10-07) — the USER-FACING half of
// "your exit is pinned to a relay that serves nothing".
//
// The operator's report was about a device, not about the admin page: «посмотри почему
// устройство a-71 skyadmin не получает доступ по маршрутизации трафика устройство
// андроид». A non-admin user whose device is in that state had NO page that said so —
// /admin/exit-nodes is operator-only, and /my/exit-nodes listed exit nodes without ever
// mentioning that this user's own device was filtered away from the routing.
//
// This file gives /my/exit-nodes the same NAMED state plus the one-click fix. The half
// that must never diverge from the engine is the VERDICT, and it is not re-derived here:
// `exit_rules.StaleExitPrefReason` is the same predicate /admin/exit-nodes and
// PlanDevicePrefChange use, with the same inputs (relay health, whether the relay owns
// any prefix at all, whether the device has rules of its own, how many relays own them,
// and whether the preference already names the owner). What this file owns is the
// SENTENCE and the CONTROL, in the user's own language.
//
// The one-click fix POSTs to the existing self-service endpoint
// `/my/devices/preferred-exit` — the same handler the user's device page uses, which
// then triggers the shared ACL re-apply (B361, pref_reapply_b361.go). No new endpoint,
// no new write path.
//
// Cost: for the caller's own preference rows only (single-digit in production, and
// usually one or two), one health read, one assignment-table read, one owner-count query
// and one rule-count query. The verdict is skipped entirely when the user has no
// preference rows at all, which is the common case.
package my

import (
	"database/sql"
	"strings"

	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/monitoring"
	"skygate/internal/prefixowner"
)

// StaleDevicePin is one of the CALLER'S OWN devices whose stored exit preference cannot
// be satisfied by the relay it names.
type StaleDevicePin struct {
	DeviceHostname string
	PrefTag        string
	// RelayHostname / RelayState are what the B273 monitor measured ("" / unknown when
	// there is no snapshot at all).
	RelayHostname string
	RelayState    string
	RelayKnown    bool
	// Reason is the shared named verdict; Cause is the i18n branch the page renders.
	Reason string
	Cause  string // "unusable" | "owner" | "no-rules"
	// CandidateTag is the relay that owns the routing — the switch target. Empty when
	// the assignment table is unreadable, in which case only "clear" is offered.
	CandidateTag string
	// ViaEnabled mirrors the stored row: whether the ACL actually enforces the pin.
	ViaEnabled bool
	// ServesSentence is the pre-rendered sentence (built here from the caller's own
	// language, through the catalogue), so the template carries no hardcoded text.
	ServesSentence string
	// FixSentence names what the buttons do.
	FixSentence string
}

// LoadStaleDevicePins returns the caller's own device preferences that the shared
// staleness predicate calls stale.
//
// Errors are swallowed into an empty result ON PURPOSE: /my/exit-nodes must render for a
// user even when one auxiliary query fails, and the page's job here is a warning, not an
// enforcement. (The ACL's own safety net — acl.servesNothing — does not depend on this
// function at all, which is what keeps a page failure from breaking a device.)
func (s *Service) LoadStaleDevicePins(userID int64) []StaleDevicePin {
	if userID == 0 {
		return nil
	}
	prefs, err := db.ListDeviceExitNodePrefsForUser(s.dbc(), userID)
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
	// The relay that carries the routing. `prefixowner.TagByPrefix` returns prefix →
	// owner TAG; the page needs the hostname and the tag, so the tag is resolved back
	// through node_owner_map for the switch control.
	ownerTagByPrefix := prefixowner.TagByPrefix(s.dbc())
	assignmentsReadable := false
	prefixesByRelay := map[string]int{}
	tagByRelay := map[string]string{}
	busiestRelay, busiestCount := "", 0
	for _, tag := range ownerTagByPrefix {
		assignmentsReadable = true
		host := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(tag)))
		if host == "" {
			continue
		}
		prefixesByRelay[host]++
		if _, ok := tagByRelay[host]; !ok {
			tagByRelay[host] = tag
		}
		if prefixesByRelay[host] > busiestCount ||
			(prefixesByRelay[host] == busiestCount && (busiestRelay == "" || host < busiestRelay)) {
			busiestRelay, busiestCount = host, prefixesByRelay[host]
		}
	}

	var out []StaleDevicePin
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
		if counts, cerr := db.OwnerTagCountsForDeviceRules(s.dbc(), userID, p.DeviceHostname); cerr == nil {
			for owner := range counts {
				if tag, terr := db.NormalizeExitNodeTag(s.dbc(), owner); terr == nil && tag != "" {
					ownerTags[owner] = tag
					candidate = tag
				}
			}
		}
		rules := countMyDeviceRules(s.dbc(), userID, p.DeviceHostname)
		prefRelayPrefixes := prefixesByRelay[relayHost]
		if candidate == "" && busiestRelay != "" && busiestRelay != relayHost {
			candidate = tagByRelay[busiestRelay]
		}
		reason, stale := exit_rules.StaleExitPrefReason(
			p.ExitNodeTag,
			assignmentsReadable && prefRelayPrefixes > 0,
			known, usable,
			rules > 0,
			len(ownerTags),
			prefNamesSameRelay(p.ExitNodeTag, candidate),
		)
		if !stale {
			continue
		}
		cause := "owner"
		switch reason {
		case exit_rules.StalePrefRelayUnusable:
			cause = "unusable"
		case exit_rules.StalePrefNoRules, exit_rules.StalePrefRelayOwnsNothing:
			cause = "no-rules"
		}
		out = append(out, StaleDevicePin{
			DeviceHostname: p.DeviceHostname,
			PrefTag:        p.ExitNodeTag,
			RelayHostname:  relayHost,
			RelayState:     state,
			RelayKnown:     known,
			Reason:         reason,
			Cause:          cause,
			CandidateTag:   candidate,
			ViaEnabled:     p.ViaEnabled,
		})
	}
	return out
}

// AnnotateDevicePinSentences fills the two sentences on every row, in the caller's
// language. It lives in this package (rather than reusing the admin helper) because a
// user page must not import the admin feature; the sentence TEMPLATES are the same i18n
// keys in both catalogues, so the two pages cannot drift in wording either.
//
// `T` takes (lang, key) with no formatting verbs, so the values are substituted into the
// catalogue's `%PLACEHOLDER%` tokens.
func (s *Service) AnnotateDevicePinSentences(pins []StaleDevicePin, lang string) {
	if s.I18n == nil || len(pins) == 0 {
		return
	}
	for i := range pins {
		p := &pins[i]
		other := strings.TrimSpace(exit_rules.TagToHostname(p.CandidateTag))
		if other == "" {
			other = "?"
		}
		relay := p.RelayHostname
		if relay == "" {
			relay = "?"
		}
		p.ServesSentence = strings.NewReplacer(
			"%RELAY%", relay,
			"%OTHER%", other,
		).Replace(s.I18n.T(lang, "exit_nodes.prefix_owner.stale_pref_serves_nothing_user"))
		p.FixSentence = s.I18n.T(lang, "exit_nodes.prefix_owner.stale_pref_action_help_user")
	}
}

// prefNamesSameRelay is the same-relay test the admin page performs
// (`prefNamesOwnerRelay`), kept here so the user page needs no admin import. Both sides
// are tags, and the tag FORM must not matter: `tag:exit-emilia` and `tag:dev-infra-emilia`
// are one relay before and after the B118 rename.
func prefNamesSameRelay(prefTag, ownerTag string) bool {
	if prefTag == "" || ownerTag == "" {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(prefTag)))
	b := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(ownerTag)))
	return a != "" && a == b
}

// countMyDeviceRules counts the caller's ENABLED ip/subnet rules for one device — the
// input that tells "this device has its own routing" from "it leans entirely on its
// pinned relay". A query error answers 0 (the warning branch), because a page must not
// go silent about a device whose rule set it could not read.
func countMyDeviceRules(d *sql.DB, userID int64, hostname string) int {
	if d == nil || userID == 0 || hostname == "" {
		return 0
	}
	var n int
	err := d.QueryRow(`
		SELECT COUNT(*)
		  FROM device_rules
		 WHERE user_id = $1
		   AND enabled = 1
		   AND target_type IN ('subnet', 'ip')
		   AND (
		     device_hostname = $2
		     OR CAST(device_id AS TEXT) IN (
		       SELECT node_id FROM node_owner_map WHERE hostname = $2
		     )
		   )
	`, userID, hostname).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}
