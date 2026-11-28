// auto_alert_b536.go — B536 (2026-11-28)
//
// THE OPERATOR'S SCREEN. Telegram, 08:05, right under «⏰ Плановое обновление skygate
// начинается»:
//
//	❌ skygate tag autoupdate FAILED
//	node: 198 (skybars-1)
//	failed tag: tag:dev-skyadmin-skybars-1
//	reason: unknown
//	error: duplicate name: "skybars-1" (id=198 …) shares the base name of "skybars" (id=55 …)
//	timestamp: 2026-11-28T05:05:16Z
//
// Every one of those five labels is wrong for what happened. No tag was written for that
// node by this path, nothing failed, and the "reason" is not `unknown` — it is B534's
// detector REPORTING that one device sits behind two headscale nodes. B534 routed its
// report through `ReportFailure` because that hook already had the three signals (audit,
// metric, rate-limited Telegram), so the report inherited the failure's costume — and the
// operator reasonably read the pair as "the update failed".
//
// WHAT THIS FILE DOES. The report gets its own channel and its own words:
//
//	audit    node.duplicate          (target_type=headscale_node, target_id=new node)
//	metric   skygate_duplicate_nodes_total{node_id, kind}
//	Telegram "⚠️ Дубликат узла" with the pair, the evidence kind, the action, and NO
//	         "failed tag", no "reason: unknown" — a report is not a failure.
//
// The rate limit is shared with the failure alerts (one per node+kind per hour) because
// the detector runs on the same 5-minute tick: without it the operator would get the same
// sentence twelve times an hour.
package nodeownership

import (
	"log"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/metrics"
)

// duplicateAlertAction is the audit action of a REPORTED duplicate (B536). It is a noun
// on purpose: /admin/audit's action column is a list of what happened, and
// `tag.autoupdate_failed` describes something else entirely.
const duplicateAlertAction = "node.duplicate"

// ReportDuplicate records a duplicate-node REPORT: audit row, its own metric, and a
// Telegram message that says "duplicate", never "failed".
//
// It never touches skygate_tag_autoupdate_failures_total — that series answers "which node's
// tag write is broken", and a duplicate answers a different question. A nil receiver is a
// no-op, like ReportFailure.
func (s *TagAlertSink) ReportDuplicate(d NodeDuplicate) {
	if s == nil {
		return
	}
	kind := string(d.Kind)
	// 1. Its own metric (always, no rate limit).
	metrics.DuplicateNodesCounter.WithLabelValues(d.NewID, kind).Inc()
	// 2. Audit row (always). B221's helper so /admin/audit can route by target.
	if s.DB != nil {
		if conn := s.DB.Current(); conn != nil {
			if err := db.AppendAuditLogWithTarget(
				conn, 0, "system",
				duplicateAlertAction,
				buildDuplicateDetail(d),
				"headscale_node", d.NewID,
			); err != nil {
				log.Printf("duplicate-alert: audit_log write failed for node=%s: %v", d.NewID, err)
			}
		}
	}
	// 3. Telegram, rate-limited per (node, kind) — the detector runs every 5 minutes.
	key := d.NewID + "\x00duplicate\x00" + kind
	now := s.now()
	s.mu.Lock()
	last, seen := s.lastAlert[key]
	shouldAlert := !seen || now.Sub(last) >= rateLimitWindow
	if shouldAlert {
		s.lastAlert[key] = now
	}
	s.mu.Unlock()
	if !shouldAlert {
		return
	}
	if id := s.Notifier.SendAlert(buildDuplicateText(d, now)); id == 0 {
		log.Printf("duplicate-alert: SendAlert returned 0 (noop notifier); audit row + metric still recorded for node=%s kind=%s", d.NewID, kind)
	}
}

// buildDuplicateDetail renders the audit_log detail for /admin/audit. Facts only, one per
// line, so an operator searching for the hostname finds the row.
func buildDuplicateDetail(d NodeDuplicate) string {
	var b strings.Builder
	b.WriteString("kind=")
	b.WriteString(string(d.Kind))
	b.WriteString("\nnew_node=")
	b.WriteString(d.NewID)
	b.WriteString("\nnew_name=")
	b.WriteString(d.NewGiven)
	b.WriteString("\nnew_hostname=")
	b.WriteString(d.NewHost)
	b.WriteString("\nnew_owner=")
	b.WriteString(strings.TrimSpace(d.NewOwner))
	b.WriteString("\nold_node=")
	b.WriteString(d.OldID)
	b.WriteString("\nold_name=")
	b.WriteString(d.OldGiven)
	b.WriteString("\nold_hostname=")
	b.WriteString(d.OldHost)
	b.WriteString("\nold_online=")
	if d.OldOnline {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteString("\nold_last_seen=")
	b.WriteString(d.OldLastSeen)
	return b.String()
}

// buildDuplicateText is the Telegram message. It must be readable as a REPORT: the title
// says what it is, the evidence line says how the detector knows, and the last line says
// what the operator can do. It names both nodes because the whole point is the pair.
func buildDuplicateText(d NodeDuplicate, now time.Time) string {
	var b strings.Builder
	b.WriteString("⚠️ Дубликат узла — один аппарат за двумя нодами")
	b.WriteString("\nновая нода: ")
	b.WriteString(d.NewID)
	b.WriteString(" (")
	b.WriteString(d.NewGiven)
	if d.NewHost != "" && !strings.EqualFold(d.NewHost, d.NewGiven) {
		b.WriteString(", hostname ")
		b.WriteString(d.NewHost)
	}
	b.WriteString(")")
	b.WriteString("\nстарая нода: ")
	b.WriteString(d.OldID)
	b.WriteString(" (")
	b.WriteString(d.OldGiven)
	if d.OldHost != "" && !strings.EqualFold(d.OldHost, d.OldGiven) {
		b.WriteString(", hostname ")
		b.WriteString(d.OldHost)
	}
	if d.OldOnline {
		b.WriteString(", online")
	} else {
		b.WriteString(", offline")
		if d.OldLastSeen != "" {
			b.WriteString(" с ")
			b.WriteString(d.OldLastSeen)
		}
	}
	b.WriteString(")")
	b.WriteString("\nпризнак: ")
	switch d.Kind {
	case DuplicateSameHostLive:
		b.WriteString("тот же hostname, ОБЕ ноды online — трафик может уйти на любую")
	case DuplicateSameHostStale:
		b.WriteString("тот же hostname, старая нода offline — это пере-регистрация устройства")
	default:
		b.WriteString("совпадает базовое имя, hostname разные — коллизия имён")
	}
	b.WriteString("\nчто это значит: правила, пины exit-узла и dev-теги привязаны к HOSTNAME, поэтому новая нода не подходит ни под одно правило, написанное для старой")
	b.WriteString("\nчто делать: удалите лишнюю ноду на /admin/devices (обычно это старая offline-нода) — после этого отчёт перестанет приходить")
	b.WriteString("\nэто НЕ ошибка тегирования и не провал обновления: тег для этой ноды не писался")
	b.WriteString("\ntimestamp: ")
	b.WriteString(now.UTC().Format(time.RFC3339))
	return b.String()
}
