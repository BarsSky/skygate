// internal/feature/exit_rules/prefix_failover_b430.go — B430 (2026-10-27).
//
// «КОГДА УЗЕЛ ПАДАЕТ, ПРЕФИКСЫ ПЕРЕЕЗЖАЮТ МОЛЧА».
//
// Measured on the reference install: a relay that stops advertising its prefixes is
// handled by the engine exactly as designed — every prefix that relay owned is
// assigned to a healthy one and the per-CIDR `via=` pins are rewritten, so the
// devices keep reaching the destinations THROUGH ANOTHER RELAY. What the operator
// saw was devices that suddenly could not reach those destinations, and the only
// trace of the reason was a log line nobody reads: there was no alert, no audit row,
// and /admin/exit-nodes showed the new owner matter-of-factly, as if it had always
// been that way.
//
// B430 turns that pass into one short composed notification through the same alert
// path as every other operator signal (B391/B426): what moved, from which relay, to
// which relay, why, and how many prefixes are affected. It is deliberately ONE
// message per pass — a dead relay owns dozens of prefixes, and a per-prefix alert
// would be a flood, not a signal — and it is deduplicated per (from,to) pair, because
// a flapping relay can move the same prefixes back and forth.
package exit_rules

import (
	"fmt"
	"log"
	"strings"
	"time"

	"skygate/internal/i18n"
	"skygate/internal/prefixowner"
	"skygate/internal/telegram"
)

// PrefixFailoverTitleKey is the catalogue key of the notification title.
const PrefixFailoverTitleKey = "alert.prefix_failover.title"

// maxFailoverPrefixesListed bounds the excerpt: the operator needs to recognise the
// shape of the move, not read the whole table (it is on /admin/exit-nodes).
const maxFailoverPrefixesListed = 8

// alertEventSender is the composed-alert capability the notifier may have (the same
// optional pattern B426 uses for the release monitor): a sink that only knows
// SendAlert keeps a one-line sentence and loses nothing.
type alertEventSender interface {
	SendAlertEvent(a telegram.Alert) int64
}

// BuildPrefixFailoverAlert is the composed notification for one reconcile pass that
// had to move prefixes off an unhealthy relay. Pure, so the message the operator
// will read is testable without a notifier, a clock or a database.
func BuildPrefixFailoverAlert(moves []prefixowner.PrefixMove) telegram.Alert {
	// Group by (from → to) so a single dead relay produces ONE fact line per
	// destination instead of one per prefix.
	type pair struct{ from, to string }
	counts := map[pair]int{}
	var order []pair
	for _, m := range moves {
		p := pair{from: m.From, to: m.To}
		if _, seen := counts[p]; !seen {
			order = append(order, p)
		}
		counts[p]++
	}
	facts := []telegram.AlertFact{
		{Label: i18n.T(i18n.LangRU, "alert.prefix_failover.fact_moved"), Value: fmt.Sprintf("%d", len(moves))},
	}
	for _, p := range order {
		// One fact per (from → to) route: the total alone does not tell the operator
		// WHICH relay took over, and with two relays down there are two answers.
		facts = append(facts, telegram.AlertFact{
			Label: p.from + " → " + p.to,
			Value: fmt.Sprintf("%d", counts[p]),
		})
	}
	listed := moves
	if len(listed) > maxFailoverPrefixesListed {
		listed = listed[:maxFailoverPrefixesListed]
	}
	lines := make([]string, 0, len(listed)+1)
	for _, m := range listed {
		lines = append(lines, fmt.Sprintf("%s → %s", m.Prefix, m.To))
	}
	if extra := len(moves) - len(listed); extra > 0 {
		lines = append(lines, fmt.Sprintf("… +%d", extra))
	}
	details := strings.Join(lines, "\n")
	// The dedup identity is the ROUTE the traffic took, not the prefix set: a
	// flapping relay that moves 28 prefixes back and forth must page once per
	// direction, not once per prefix per minute.
	key := "prefix.failover"
	for _, p := range order {
		key += "." + p.from + ">" + p.to
	}
	if len(key) > 200 {
		key = key[:200]
	}
	return telegram.Alert{
		Severity: telegram.SeverityWarn,
		TitleKey: PrefixFailoverTitleKey,
		Facts:    facts,
		Details:  details,
		DedupKey: key,
		// Half an hour: long enough that a relay flapping within one maintenance
		// window is one incident, short enough that a second, genuine outage an hour
		// later is news again.
		DedupTTL: 30 * time.Minute,
		Lang:     i18n.LangRU,
	}
}

// notifyPrefixFailover sends one message about a forced move. Best-effort: the
// reconcile pass must never fail because a notification could not be delivered (the
// table and the ACL are already correct by the time this runs).
func (s *Service) notifyPrefixFailover(moves []prefixowner.PrefixMove) {
	if len(moves) == 0 || s.Notifier == nil {
		return
	}
	alert := BuildPrefixFailoverAlert(moves)
	// The alert's facts need the active catalogue; the label is resolved by the
	// composer from the key, so an empty lang falls back to RU exactly like every
	// other composed alert.
	if sender, ok := s.Notifier.(alertEventSender); ok {
		if id := sender.SendAlertEvent(alert); id > 0 {
			log.Printf("prefix-failover: notified %d prefix move(s) off an unhealthy relay (alert_id=%d)", len(moves), id)
			return
		}
		return
	}
	// Legacy sink: one plain sentence from the CATALOGUE (the B391 ratchet counts raw
	// English alert text, and a translated sentence is also the honest thing to send).
	from, to := moves[0].From, moves[0].To
	if id := s.Notifier.SendAlert(i18n.Tf(i18n.LangRU, "alert.prefix_failover.plain", len(moves), from, to)); id > 0 {
		log.Printf("prefix-failover: notified %d prefix move(s) off %s (plain transport, alert_id=%d)", len(moves), from, id)
	}
}
