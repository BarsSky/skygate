// preferred_exit_alert_b3913.go — B391.3 (2026-10-27): the last eight English-only alerts
// in the codebase become composed, translated notifications.
//
// WHY THIS SLICE NEEDED A SHAPE AND NOT ONLY A TRANSLATION. The seven preferred-exit
// notices and the Tailscale-discovery notice are the only alerts built from state that
// RECURS: the reconciler runs every tick, and an ORPHAN or AMBIGUOUS preference persists
// until a human resolves it, so the same message can be emitted again and again (an
// in-memory `shouldAlert` window already limits it to once an hour per device+reason, but
// that window dies with the process and says nothing about two devices with the same
// story). Translating them verbatim would have produced a repeating multi-line Russian
// wall with an SQL snippet buried in the middle. So each one is now a composed alert:
//
//	title   — what happened, in the operator's language (three families: changed,
//	          stale, needs-a-manual-decision);
//	facts   — the five fields every one of these notices carries (action, device, user,
//	          pin, reason) plus the one fact that is specific to the variant (relay
//	          state, candidates, rule count);
//	details — the actionable part (rollback SQL, cleanup SQL, where to resolve it),
//	          in a <pre> block the composer bounds and escapes;
//	DedupKey— `preferred_exit.<action>.<device>`, one hour, so a persistent state is one
//	          incident per window rather than one message per pass.
//
// The builder is PURE (no clock, no IO, no service), which is what lets the tests assert
// the message the operator will actually read, and what lets the contract pin the shape
// without running a database.
package exit_rules

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// Preferred-exit alert actions. These double as catalogue key suffixes
// (`alert.preferred_exit.action_<action>`), so the set is closed on purpose: an unknown
// action renders as itself instead of silently becoming the "changed" family.
const (
	PrefActionCreate    = "create"
	PrefActionUpdate    = "update"
	PrefActionClear     = "clear"
	PrefActionMigrate   = "migrate"
	PrefActionAmbiguous = "ambiguous"
	PrefActionOrphan    = "orphan"
	PrefActionStale     = "stale"
)

// preferredExitDedupTTL matches the in-memory `shouldAlert` window (one hour per
// device+reason). The two layers guard different things: the memory window stops a
// per-tick repeat inside one process, and the transport dedup key stops the repeat that
// survives a restart — which is exactly what a state that persists for days does.
const preferredExitDedupTTL = time.Hour

// PreferredExitAlertInput carries what the reconciler knows at the moment it changes, or
// refuses to change, one device's stored exit-node preference. Every field is copied from
// an existing log/audit line — nothing here is invented for the message.
type PreferredExitAlertInput struct {
	Action string
	// Hostname is the device whose preference is at stake; Username its portal owner.
	Hostname string
	// NewHostname is set only by MIGRATE: the device was renamed, so the message has to
	// show both names — that pair IS the incident.
	NewHostname string
	Username    string
	// OldTag / NewTag are the stored and the resolved preference. One of them may be
	// empty: CREATE has no old value, CLEAR and ORPHAN have no new one.
	OldTag string
	NewTag string
	// Reason is the reconciler's own words (the same string the audit row carries).
	Reason string
	// RelayState is what the B273 monitor last said about the relay (stale alerts).
	RelayState string
	// Candidates names the devices that made an AMBIGUOUS decision possible.
	Candidates string
	// Rules is how many device_rules pointed at the relay (CREATE / MIGRATE). The
	// reconciler carries it as an int (ReconcilerChange) and the rename migrator as an
	// int64, so the input takes the wider type and formats it either way.
	Rules int64
	// UserID is used by the two rollback/cleanup SQL hints.
	UserID int64
	// SetByUserID is non-zero when a HUMAN set the preference, which is why the engine
	// refused to rewrite it (stale alerts).
	SetByUserID int64
	// ViaEnabled is the strict-pin flag carried from the existing row (B399).
	ViaEnabled bool
}

// BuildPreferredExitAlert is the pure builder behind every preferred-exit notification.
//
// It returns the Alert the transport will render: `SendAlertEvent` gets it as-is, a
// sink that only knows SendAlert gets `ComposeAlertPlain` of the same facts, so the two
// paths cannot describe different incidents.
func BuildPreferredExitAlert(in PreferredExitAlertInput) telegram.Alert {
	lang := i18n.LangRU
	action := strings.TrimSpace(in.Action)
	if action == "" {
		action = PrefActionUpdate
	}

	a := telegram.Alert{
		Severity: preferredExitSeverity(action),
		TitleKey: preferredExitTitleKey(action),
		DedupKey: preferredExitDedupKey(action, in.Hostname, in.NewTag),
		DedupTTL: preferredExitDedupTTL,
		Lang:     lang,
	}

	// The five facts every one of these notices carries.
	device := emptyDash(in.Hostname)
	if in.NewHostname != "" && in.NewHostname != in.Hostname {
		device = emptyDash(in.Hostname) + " → " + in.NewHostname
	}
	a.Facts = append(a.Facts,
		telegram.AlertFact{Label: i18n.T(lang, "alert.preferred_exit.fact_action"), Value: i18n.T(lang, "alert.preferred_exit.action_"+action)},
		telegram.AlertFact{Label: i18n.T(lang, "alert.preferred_exit.fact_device"), Value: device},
		telegram.AlertFact{Label: i18n.T(lang, "alert.preferred_exit.fact_user"), Value: emptyDash(in.Username)},
		telegram.AlertFact{Label: i18n.T(lang, "alert.preferred_exit.fact_pin"), Value: preferredExitPin(in)},
		telegram.AlertFact{Label: i18n.T(lang, "alert.preferred_exit.fact_reason"), Value: emptyDash(in.Reason)},
	)

	// The variant-specific fact, then the details.
	switch action {
	case PrefActionStale:
		if in.RelayState != "" {
			a.Facts = append(a.Facts, telegram.AlertFact{
				Label: i18n.T(lang, "alert.preferred_exit.fact_relay"),
				Value: in.RelayState,
			})
		}
		a.Details = strings.TrimSpace(strings.Join([]string{
			i18n.Tf(lang, "alert.preferred_exit.detail_fix", emptyDash(in.Hostname)),
			preferredExitHumanNote(lang, in.SetByUserID),
		}, "\n"))
	case PrefActionAmbiguous:
		if in.Candidates != "" {
			a.Facts = append(a.Facts, telegram.AlertFact{
				Label: i18n.T(lang, "alert.preferred_exit.fact_candidates"),
				Value: in.Candidates,
			})
		}
		a.Details = i18n.T(lang, "alert.preferred_exit.detail_ambiguous")
	case PrefActionOrphan:
		a.Details = i18n.Tf(lang, "alert.preferred_exit.detail_orphan", in.UserID, emptyDash(in.Hostname))
	case PrefActionCreate:
		if in.Rules > 0 {
			a.Facts = append(a.Facts, telegram.AlertFact{
				Label: i18n.T(lang, "alert.preferred_exit.fact_rules"),
				Value: strconv.FormatInt(in.Rules, 10),
			})
		}
		a.Details = preferredExitRollbackDetail(lang, in, "DELETE FROM device_exit_node_prefs")
	case PrefActionUpdate:
		a.Details = preferredExitRollbackDetail(lang, in, "DELETE FROM device_exit_node_prefs")
	case PrefActionMigrate:
		if in.Rules > 0 {
			a.Facts = append(a.Facts, telegram.AlertFact{
				Label: i18n.T(lang, "alert.preferred_exit.fact_rules"),
				Value: strconv.FormatInt(in.Rules, 10),
			})
		}
		a.Details = i18n.T(lang, "alert.preferred_exit.detail_migrate")
	case PrefActionClear:
		a.Details = i18n.T(lang, "alert.preferred_exit.detail_clear")
	}
	return a
}

// preferredExitSeverity: the three "needs a human" states warn; the four that describe a
// change the engine already made are informational. A warning that fires for bookkeeping
// is how an operator learns to ignore warnings.
func preferredExitSeverity(action string) telegram.Severity {
	switch action {
	case PrefActionStale, PrefActionAmbiguous, PrefActionOrphan:
		return telegram.SeverityWarn
	}
	return telegram.SeverityInfo
}

func preferredExitTitleKey(action string) string {
	switch action {
	case PrefActionStale:
		return "alert.preferred_exit.title_stale"
	case PrefActionAmbiguous, PrefActionOrphan:
		return "alert.preferred_exit.title_review"
	}
	return "alert.preferred_exit.title"
}

// preferredExitDedupKey identifies the INCIDENT: the action, the device and (when there
// is one) the resolved pin. Two different devices with the same story are two incidents,
// and a re-pin of the same device to a different relay is a new one.
func preferredExitDedupKey(action, hostname, newTag string) string {
	key := "preferred_exit." + action + "." + strings.ToLower(strings.TrimSpace(hostname))
	if newTag != "" {
		key += "." + strings.ToLower(strings.TrimSpace(newTag))
	}
	return key
}

// preferredExitPin renders the stored → resolved transition as one readable value.
func preferredExitPin(in PreferredExitAlertInput) string {
	switch {
	case in.OldTag != "" && in.NewTag != "" && in.OldTag != in.NewTag:
		return in.OldTag + " → " + in.NewTag
	case in.NewTag != "":
		return in.NewTag
	case in.OldTag != "":
		return in.OldTag
	}
	return "—"
}

// preferredExitRollbackDetail is the "how do I undo this in SQL" block: the statement the
// operator pastes, with the two placeholders filled. It stays a literal SQL string (not a
// catalogue value) because it is code the operator runs, not prose.
func preferredExitRollbackDetail(lang string, in PreferredExitAlertInput, stmt string) string {
	sql := fmt.Sprintf(`-- rollback:
%s WHERE user_id=%d AND device_hostname='%s';`,
		stmt, in.UserID, emptyDash(in.Hostname))
	via := i18n.T(lang, "alert.preferred_exit.pin_loose")
	if in.ViaEnabled {
		via = i18n.T(lang, "alert.preferred_exit.pin_strict")
	}
	return i18n.Tf(lang, "alert.preferred_exit.detail_rollback", via) + "\n" + sql
}

// preferredExitHumanNote explains WHY the engine left a stale preference alone. Empty
// when no human set it, so the details never carry a sentence about nothing.
func preferredExitHumanNote(lang string, setByUserID int64) string {
	if setByUserID == 0 {
		return ""
	}
	return i18n.Tf(lang, "alert.preferred_exit.detail_human", setByUserID)
}

// preferredExitPlainFallback is the one-sentence form for a sink that cannot render a
// composed alert (tests, NoopNotifier, an older wiring).
func preferredExitPlainFallback(in PreferredExitAlertInput) string {
	return i18n.Tf(i18n.LangRU, "alert.preferred_exit.plain",
		i18n.T(i18n.LangRU, "alert.preferred_exit.action_"+in.Action),
		emptyDash(in.Hostname), preferredExitPin(in))
}

// sendPreferredExitAlert delivers the composed alert when the sink supports it and falls
// back to the plain sentence otherwise. Delivery is best-effort by design: the reconciler
// has already written the preference and the audit row, and a message that cannot be sent
// must not turn a repair into a failure.
func sendPreferredExitAlert(n ReconcilerNotifier, in PreferredExitAlertInput) {
	if n == nil {
		return
	}
	a := BuildPreferredExitAlert(in)
	if sink, ok := n.(interface{ SendAlertEvent(telegram.Alert) int64 }); ok {
		sink.SendAlertEvent(a)
		return
	}
	n.SendAlert(preferredExitPlainFallback(in))
}

// emptyDash keeps a missing value visible as a dash instead of a blank fact, so a table
// column never collapses into an unreadable empty cell.
func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// BuildDiscoveryAlert is the composed form of the Tailscale auto-discovery notice — the
// eighth and last English-only alert (B391.3). It is a pure function for the same reason
// the reconciler builders are: the message the operator reads is testable without a
// ticker, a headscale client or a notifier.
func BuildDiscoveryAlert(discovered int, tagFilter string) telegram.Alert {
	lang := i18n.LangRU
	if tagFilter == "" {
		tagFilter = "—"
	}
	return telegram.Alert{
		Severity: telegram.SeverityInfo,
		TitleKey: "alert.discovery_new_nodes.title",
		Facts: []telegram.AlertFact{
			{Label: i18n.T(lang, "alert.discovery_new_nodes.fact_found"), Value: strconv.Itoa(discovered)},
			{Label: i18n.T(lang, "alert.discovery_new_nodes.fact_filter"), Value: tagFilter},
		},
		Details:  i18n.T(lang, "alert.discovery_new_nodes.detail"),
		DedupKey: "discovery.new_nodes",
		// Thirty minutes: the discovery ticker is opt-in and hourly, so this only
		// suppresses a restart from re-sending the same tally.
		DedupTTL: 30 * time.Minute,
		Lang:     lang,
	}
}
