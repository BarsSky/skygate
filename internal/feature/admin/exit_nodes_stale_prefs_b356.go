// exit_nodes_stale_prefs_b356.go — B356 (2026-10-07) — the operator-visible half,
// extended by B361 (2026-10-07) with the device that has no rules at all.
//
// /admin/exit-nodes already answers "which relay serves which prefix" (B275/B276) and
// "is the live policy the one we would generate" (B276/B288). It said NOTHING about
// `device_exit_node_prefs` — and that is where the operator's incident lived: two
// devices were pinned to a relay that had gone offline, the per-device
// `autogroup:internet` grant carried `via=[that tag]` (a permission FILTER since
// B265), and no page named the consequence.
//
// B361 adds the half B356 left out, measured on the operator's own device the same
// day: `a71` had ZERO rules, a HUMAN pin ~46 days old naming `emilia`, and
// `prefix_owner` holding 120 rows — all of them `karolina`. `emilia` was `online`, so
// the health predicate said nothing; and because the owner comparison is built from
// the device's RULES, the page had nothing to compare either. The relay that serves
// NOTHING must be named by asking about the relay, not about the device's owners.
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
//     dead or ownerless relay is exactly the state that broke the operator's device.
//
// B361 also gives the page the ONE-CLICK FIX: `CandidateTag` is the relay that owns
// the routing (the device's owners when it has rules, otherwise the relay with the
// most prefixes in the assignment table), and the template posts it to
// `/admin/devices/preferred-exit` — the same endpoint the operator used by hand.
// Clearing the pin is offered beside it, because "any exit node" is a legitimate
// choice and it is the state that always works.
//
// Cost: one `OwnerTagCountsForDeviceRules` + one `NormalizeExitNodeTag` per
// preference row, plus one read of the assignment table for the whole page.
// Preference rows are single-digit in production (only devices an operator explicitly
// pinned), and this is an admin page, so the trade is a page that tells the truth over
// one that renders in 3ms less. Unit-tested pure predicate, thin DB shell.

package admin

import (
	"database/sql"
	"sort"
	"strings"

	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/i18n"
	"skygate/internal/monitoring"
	"skygate/internal/prefixowner"
)

// StaleExitPref is one stored preference the operator has to look at.
type StaleExitPref struct {
	UserID         int64
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
	// `stale-pref-relay-owns-nothing` / `stale-pref-no-rules` /
	// `owner-overrides-rule-relay`); Cause is the i18n selector for the sentence the
	// page renders.
	Reason string
	Cause  string // "unusable" | "owner" | "no-rules"
	// CandidateTag is the relay that owns the routing and that the one-click fix
	// should switch the pin to ("" when it could not be determined — the page then
	// tells the operator to pick one).
	CandidateTag string
	// B361: the facts the operator needs in order to see WHY. Rules is the number of
	// enabled ip/subnet rules the device has (0 = its egress depends entirely on the
	// pinned relay — the a71 case); PrefRelayPrefixes is how many prefixes in
	// `prefix_owner` the PINNED relay owns (0 = it serves nothing at all);
	// AlternativeRelay / AlternativePrefixes name the busiest other relay, which is
	// what "обслуживает <other>" is about.
	Rules               int
	PrefRelayPrefixes   int
	AlternativeRelay    string
	AlternativePrefixes int
	AssignmentsReadable bool
	// ViaEnabled mirrors `device_exit_node_prefs.via_enabled`: whether the ACL actually
	// ENFORCES this preference (B265's `via` pin) or merely records it. The one-click
	// switch posts the same value back, so the control changes the relay and nothing
	// else — an operator's own via setting is not the page's to flip.
	ViaEnabled bool
	// ServesLine is the pre-rendered sentence the operator asked for: «выход закреплён
	// за <relay>; он сейчас не обслуживает ни одного вашего маршрута (обслуживает
	// <other>), трафик может не проходить». It is built HERE (handler side) so the
	// template renders one i18n key with its values and stays free of hardcoded text
	// (the B325 ratchet), and so the RU/EN choice follows the request's language.
	ServesLine string
	// RulesLine names the other half of the consequence: a device with no rules at all
	// depends entirely on its pinned relay, and `N` is what that relay advertises.
	RulesLine string
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
	// B361: one read of the assignment table for the whole page. It answers "does
	// the preferred relay own anything at all?" — the question the a71 case needed
	// and the one the per-device owner query cannot answer for a device with no
	// rules. Keyed by TAG, the same currency the ACL pin uses.
	ownerTagByPrefix := prefixowner.TagByPrefix(s.dbc())
	// `assignmentsReadable` means the TABLE has rows — not that at least one of them
	// could be resolved to a tag. A row whose relay has no `node_owner_map` entry
	// yields no tag and would make the map empty even though assignments exist, and
	// reading that as "no data" would keep a pin on a relay that owns nothing. The
	// probe is one COUNT, and an empty table keeps every pin (a fresh install must not
	// be rewritten by a page-global verdict).
	assignmentsReadable := prefixOwnerHasRows(s.dbc())
	// Prefixes per relay, and the busiest relay overall — the "обслуживает <other>"
	// half of the operator's sentence, and the default candidate for a device with
	// no rules.
	prefixesByRelay := map[string]int{}
	busiestRelay, busiestCount := "", 0
	for _, tag := range ownerTagByPrefix {
		host := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(tag)))
		if host == "" {
			continue
		}
		prefixesByRelay[host]++
		if prefixesByRelay[host] > busiestCount || (prefixesByRelay[host] == busiestCount && (busiestRelay == "" || host < busiestRelay)) {
			busiestRelay, busiestCount = host, prefixesByRelay[host]
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
		// The device's own owners, and how many enabled rules it has at all.
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
		rules := countDeviceRulesForAdmin(s.dbc(), p.UserID, p.DeviceHostname)
		prefRelayPrefixes := 0
		if relayHost != "" {
			prefRelayPrefixes = prefixesByRelay[relayHost]
		}
		// B361: the one-click fix's target. With rules, the relay that owns them;
		// without rules, the relay that owns the most prefixes — the one that
		// "обслуживает" the routing (karolina with 120 rows in the operator's case).
		// Never the pinned relay itself (that is the state being reported), and never
		// empty when the assignment table is readable.
		altRelay, altCount := "", 0
		if candidate != "" {
			if h := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(candidate))); h != "" && h != relayHost {
				altRelay, altCount = h, prefixesByRelay[h]
			}
		}
		if altRelay == "" && busiestRelay != "" && busiestRelay != relayHost {
			altRelay, altCount = busiestRelay, busiestCount
			if candidate == "" {
				if tag, terr := db.NormalizeExitNodeTag(s.dbc(), busiestRelay); terr == nil {
					candidate = tag
				}
			}
		}
		reason, stale := exit_rules.StaleExitPrefReason(
			p.ExitNodeTag,
			assignmentsReadable && prefRelayPrefixes > 0,
			known, usable,
			rules > 0,
			len(ownerTags),
			prefNamesOwnerRelay(p.ExitNodeTag, candidate),
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
		out = append(out, StaleExitPref{
			UserID:              p.UserID,
			Username:            p.Username,
			DeviceHostname:      p.DeviceHostname,
			PrefTag:             p.ExitNodeTag,
			RelayHostname:       relayHost,
			RelayState:          state,
			RelayKnown:          known,
			HumanPinned:         p.SetByUserID != 0,
			SetByUserID:         p.SetByUserID,
			Reason:              reason,
			Cause:               cause,
			CandidateTag:        candidate,
			Rules:               rules,
			PrefRelayPrefixes:   prefRelayPrefixes,
			AlternativeRelay:    altRelay,
			AlternativePrefixes: altCount,
			AssignmentsReadable: assignmentsReadable,
			ViaEnabled:          p.ViaEnabled,
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

// AnnotateStalePrefSentences fills the two pre-rendered sentences on every row, in the
// request's language.
//
// WHY THE HANDLER BUILDS THE SENTENCE. The operator asked for a specific sentence —
// «выход закреплён за <relay>; он сейчас не обслуживает ни одного вашего маршрута
// (обслуживает <other>), трафик может не проходить» — and the panel has two hard rules
// about strings: every user-visible text is an i18n key in BOTH catalogues (AGENTS rule
// 10), and no template may contain hardcoded user-visible text (the B325 ratchet).
// Rendering `{{t "…"}}` with values is exactly what `printf`-style templates are worst
// at, so the values are substituted here, through the catalogue, with the language the
// request already resolved. The template then renders ONE key per sentence.
//
// It is EXPORTED because /my/exit-nodes shows the same state to a non-admin user (the
// operator's own report was about a device, not about the admin page), and duplicating
// the sentence in a second package is how the two pages start disagreeing.
//
// `cat` may be nil (a page rendered before i18n is wired, or a test): the sentences are
// then left EMPTY and the template falls back to the structured columns it already
// renders. An empty sentence is a missing convenience; a mangled one would be a lie.
func AnnotateStalePrefSentences(rows []StaleExitPref, cat *i18n.Catalog, lang string) {
	if cat == nil || len(rows) == 0 {
		return
	}
	for i := range rows {
		r := &rows[i]
		// The catalogue's `T` takes (lang, key) only — no formatting verbs — so the
		// values are substituted into `%PLACEHOLDER%` tokens. That keeps the sentence a
		// real translatable string in BOTH catalogues (the translator sees the whole
		// sentence and its slots) while the handler owns the data.
		r.ServesLine = fillStalePrefTokens(
			cat.T(lang, "exit_nodes.prefix_owner.stale_pref_serves_nothing"),
			r.RelayHostname, itoaAdmin(r.PrefRelayPrefixes), r.AlternativeRelay, itoaAdmin(r.AlternativePrefixes))
		if r.Rules == 0 {
			r.RulesLine = fillStalePrefTokens(
				cat.T(lang, "exit_nodes.prefix_owner.stale_pref_no_rules_line"),
				r.RelayHostname, itoaAdmin(r.PrefRelayPrefixes), r.AlternativeRelay, itoaAdmin(r.AlternativePrefixes))
		} else {
			r.RulesLine = fillStalePrefTokens(
				cat.T(lang, "exit_nodes.prefix_owner.stale_pref_rules_line"),
				r.RelayHostname, itoaAdmin(r.Rules), r.AlternativeRelay, itoaAdmin(r.PrefRelayPrefixes))
		}
	}
}

// fillStalePrefTokens substitutes the four positional values into the sentence
// template's `%RELAY%` / `%N%` / `%OTHER%` / `%M%` slots.
//
// A hostname that is empty (the relay's tag named no node — a class tag, or a relay the
// ownership map does not know) becomes the catalogue's own "unknown" word rather than an
// empty hole, so the sentence still reads as a sentence.
func fillStalePrefTokens(tpl, relay, n, other, m string) string {
	if relay == "" {
		relay = "?"
	}
	if other == "" {
		other = "?"
	}
	return strings.NewReplacer(
		"%RELAY%", relay,
		"%N%", n,
		"%OTHER%", other,
		"%M%", m,
	).Replace(tpl)
}

// itoaAdmin formats a small non-negative int for a sentence. Kept local (the admin
// package has no shared itoa) so a page render never pulls in a formatting dependency.
func itoaAdmin(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// countDeviceRulesForAdmin counts a device's ENABLED ip/subnet rules — the input the
// shared staleness predicate needs in order to tell "this device has its own routing"
// from "this device leans entirely on its pinned relay" (the a71 case).
//
// One small query per preference row, and preference rows are single-digit in
// production (a row exists only for a device an operator pinned by hand). The
// per-device hostname/device_id OR is the same shape collectDevicePrefState uses, and
// `CAST(device_id AS TEXT)` is the dialect-safe comparison B319 pinned (PostgreSQL
// refuses `integer = text`).
//
// A query error answers 0, which makes the predicate take the "no rules" branch: the
// page then shows the strongest warning about a device whose rule set it could not
// read. That is the safe direction for a WARNING (the ACL never sees this number), and
// it is reported rather than hidden because the row carries the relay's facts too.
func countDeviceRulesForAdmin(d *sql.DB, userID int64, hostname string) int {
	if d == nil || hostname == "" {
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

// prefixOwnerHasRows reports whether the assignment table carries anything at all.
//
// B361: it is the difference between "the preferred relay owns none of the N assigned
// prefixes" (a fact, and the a71 case) and "nobody has assigned anything yet" (a
// question the page cannot answer, where every pin must be left alone). The generator
// applies the same rule with `len(ownerTagsByPrefix) == 0`, and the reconciler with
// `loadOwnerTagByPrefix`'s second return value.
func prefixOwnerHasRows(d *sql.DB) bool {
	if d == nil {
		return false
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM prefix_owner`).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// prefNamesOwnerRelay reports whether the preference names the SAME relay as the one
// the data plane chose for this device's prefixes.
//
// It is the `ownerTag != prefTag` test the pre-B361 predicate performed inline, hoisted
// where the hostname is available (`exit_rules.TagToHostname` is that package's job).
// Both sides are hostnames by the time they get here, and the tag FORM does not matter:
// `tag:exit-emilia` and `tag:dev-infra-emilia` are one relay before and after the B118
// rename, and treating them as different would warn about a device that is correctly
// pinned (the same trap `tagsNameSameRelay` exists for in the planner).
func prefNamesOwnerRelay(prefTag, ownerTag string) bool {
	if prefTag == "" || ownerTag == "" {
		return false
	}
	prefHost := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(prefTag)))
	ownerHost := strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(ownerTag)))
	return prefHost != "" && prefHost == ownerHost
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
