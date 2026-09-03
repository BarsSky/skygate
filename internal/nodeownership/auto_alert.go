package nodeownership

// auto_alert.go — v1.5.2 / B227 — observability hook for
// the B77 tag-autoupdater's AddTag failure path.
//
// Why this exists
// ---------------
// Pre-B227, when B77 (the node-discovery autoupdater in
// auto.go) tried to apply `tag:dev-<user>-<device>` to a
// node and headscale rejected it (e.g. because the headscale
// ACL policy doesn't permit the auto-generated namespace —
// the live case from 2026-09-03 is `skygate-host-1-1` which
// has `tag:infra-skygate-host-1-1` and B77 keeps trying to
// add `tag:dev-skyadmin-skygate-host-1-1`, getting
// `InvalidArgument: requested tags ... are invalid or not
// permitted` every 5 min), the only signal was:
//
//	2026/09/03 14:10:26 warn: auto-apply dev tag
//	  "tag:dev-skyadmin-skygate-host-1-1" to node 43: ...
//	  — keeping existing tags as fallback
//
// The operator had to either tail skygate stderr or grep
// `audit_log` for manual traces — neither surfaces in
// the existing B225 (Telegram alerts) or B226 (Prometheus
// metrics) family. B227 closes the gap with three signals:
//
//  1. Prometheus counter `skygate_tag_autoupdate_failures_total`
//     (declared in internal/metrics/collector.go) — bumped
//     on EVERY failure, label-bounded by (node_id, hostname,
//     reason) so Prom queries can `rate()` per-node.
//  2. audit_log row with action `tag.autoupdate_failed`,
//     target_type=`headscale_node`, target_id=nodeID —
//     visible in /admin/audit (B207+), joins the B221
//     target_id routing.
//  3. Telegram alert (the B225 family) — rate-limited to
//     1 per (node_id, reason) per hour so the operator
//     doesn't get spammed at the 5-min autoupdater cadence.
//     When the Telegram bot is unconfigured (SKYGATE_TELEGRAM_*
//     empty), the SendAlert is a no-op and the metric + audit
//     are the durable signals.
//
// Concurrency
// -----------
// AutoBackfill runs in a single goroutine per skygate
// process (one tick at a time, see auto.go:121-132). The
// rate-limit map is mutex-guarded for defense-in-depth
// (a future parallel-tick refactor shouldn't race on
// the map). The metric + audit writes are concurrency-
// safe by construction (Prom counter is internal-mutex
// guarded; audit_log INSERT is PG-side serialized).

import (
	"log"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/metrics"
)

// AlertSink — minimal local interface so AutoBackfill
// can push Telegram alerts WITHOUT importing
// internal/telegram (which would create a cycle through
// internal/handlers → internal/telegram → ...). Mirrors
// the pattern in internal/monitoring, internal/watchdog,
// internal/release, etc — local interface, no-op fallback,
// production wiring via main.go.
type AlertSink interface {
	SendAlert(text string) int64
}

// noopAlertSink — the fallback when SKYGATE_TELEGRAM_BOT_TOKEN
// is empty. Returns 0 from SendAlert to match the production
// telegram.NoopNotifier contract. The metric + audit are
// still recorded when a real failure is reported through
// TagAlertSink.ReportFailure — the Telegram alert is the
// only path that no-ops.
type noopAlertSink struct{}

// SendAlert is the no-op implementation of AlertSink.
func (noopAlertSink) SendAlert(string) int64 { return 0 }

// NoopAlertSink is exported so main.go + the manual
// Backfill callers (feature/my, feature/admin/devices)
// can use it as the default when no Telegram bot is
// configured. The metric + audit paths still fire.
var NoopAlertSink AlertSink = noopAlertSink{}

// FailureReason — classifies a B77 AddTag error into
// one of three buckets for the operator:
//
//   - acl_reject: headscale policy doesn't permit the
//     auto-generated `tag:dev-<user>-<device>` (e.g.
//     skygate-host-1-1 with `tag:infra-skygate-host-1-1`).
//     The fix is operator-side: edit the headscale ACL
//     to allow the dev-tag namespace, OR re-tag the node
//     away from `tag:infra-*`.
//   - rpc_error: transient gRPC error (network, 5xx,
//     timeout). The next tick should retry; no operator
//     action needed.
//   - unknown: anything that doesn't match the two
//     patterns above. Surface the raw error in the
//     audit row + Telegram alert so the operator can
//     diagnose.
//
// The classification is a string match on err.Error()
// (no structured gRPC status code available — the
// headscale client returns the wrapped error string).
type FailureReason string

const (
	ReasonACLReject FailureReason = "acl_reject"
	ReasonRPCError  FailureReason = "rpc_error"
	ReasonUnknown   FailureReason = "unknown"
)

// ClassifyFailure parses the headscale error string
// and returns the matching FailureReason. The
// pre-B227 code only logged the raw error; the
// operator had to read the headscale source to
// understand whether the failure was "permanent
// (config)" or "transient (retry later)".
//
// Patterns matched (in priority order):
//   1. gRPC ACL codes (InvalidArgument — most common
//      headscale ACL reject: "requested tags ... are
//      invalid or not permitted"; PermissionDenied;
//      FailedPrecondition — covers the "tag already
//      exists" duplicate-add case).
//   2. gRPC transient codes (Unavailable — network
//      down; Internal — 5xx; DeadlineExceeded — timeout).
//   3. Anything else → unknown.
func ClassifyFailure(err error) FailureReason {
	if err == nil {
		return ReasonUnknown
	}
	s := err.Error()
	// ACL-reject codes first — these are the ones
	// that need operator attention (the headscale
	// policy is misconfigured, or the node has a
	// non-dev-tag namespace).
	if strings.Contains(s, "InvalidArgument") ||
		strings.Contains(s, "PermissionDenied") ||
		strings.Contains(s, "FailedPrecondition") {
		return ReasonACLReject
	}
	// Transient codes — the next tick will retry,
	// the operator doesn't need to act.
	if strings.Contains(s, "Unavailable") ||
		strings.Contains(s, "Internal") ||
		strings.Contains(s, "DeadlineExceeded") {
		return ReasonRPCError
	}
	return ReasonUnknown
}

// rateLimitWindow — minimum gap between two Telegram
// alerts for the SAME (node_id, reason) tuple. The
// autoupdater runs every 5 min by default; a hard ACL
// reject like skygate-host-1-1 will hit this every tick
// — without rate-limiting, the operator's Telegram
// would flood with identical alerts.
//
// The audit_log + Prometheus counter are NOT rate-
// limited (they're the durable record + the metric
// for Prom alerts); only the Telegram SendAlert is.
//
// 1 hour is the v1.5.2 default. Operator can change
// it in code (constant) if a different cadence is
// needed.
const rateLimitWindow = 1 * time.Hour

// TagAlertSink — the B227 alert dispatcher. Holds a
// Notifier (Telegram), a DB (for audit_log writes),
// and the in-memory rate-limit map.
//
// Constructed once at boot in main.go and passed into
// AutoBackfill. The manual Backfill callers (feature/my
// + feature/admin/devices) construct their own
// TagAlertSink with NoopAlertSink so they get the
// metric + audit but no Telegram alert (the operator
// initiated the action and sees the HTTP response).
type TagAlertSink struct {
	// Notifier — AlertSink interface (Telegram in
	// production, NoopAlertSink when no bot is
	// configured). SendAlert returns the message id
	// (0 = not actually sent).
	Notifier AlertSink
	// DB — for audit_log writes via
	// db.AppendAuditLogWithTarget. The struct
	// resolves the live *sql.DB per call (B224
	// ResettableDB pattern), so a B203 PG swap
	// doesn't strand captured pool.
	DB db.DBSource
	// Now — injectable clock for tests. Production
	// leaves this nil and the sink uses time.Now().
	Now func() time.Time

	// mu guards lastAlert. The autoupdater is
	// single-threaded today, but the mutex keeps
	// the sink future-proof for parallel-tick
	// refactors.
	mu sync.Mutex
	// lastAlert — map keyed by nodeID + "\x00" + reason
	// (the NUL separator is the same trick the B226
	// metric vec uses to handle label values that
	// might contain commas or quotes).
	lastAlert map[string]time.Time
}

// NewTagAlertSink returns a TagAlertSink ready for use.
// main.go is the only call site for the production
// (Telegram-wired) sink. Manual Backfill callers
// (feature/my, feature/admin/devices) call this with
// NoopAlertSink to get the metric + audit but no alert.
func NewTagAlertSink(notifier AlertSink, d db.DBSource) *TagAlertSink {
	if notifier == nil {
		notifier = NoopAlertSink
	}
	return &TagAlertSink{
		Notifier:  notifier,
		DB:        d,
		Now:       time.Now,
		lastAlert: make(map[string]time.Time),
	}
}

// ReportFailure is the B227 hook called from the B77
// AddTag error path (nodeownership.go:633). It:
//
//  1. Always increments skygate_tag_autoupdate_failures_total
//     (Prometheus; rate-limit-independent — the metric
//     is the "operator can build a Prom alert" surface).
//  2. Always writes an audit_log row (durable record;
//     visible in /admin/audit; rate-limit-independent).
//  3. Sends a Telegram alert (rate-limited to
//     1 per (node_id, reason) per hour).
//
// The function never panics, never returns an error —
// both the metric + audit + alert are best-effort
// (the next tick will retry, so a single missed
// alert is recoverable). A nil receiver is a no-op
// (defensive: manual Backfill callers can pass nil
// to opt out of observability entirely).
func (s *TagAlertSink) ReportFailure(nodeID, hostname, devTag string, addErr error) {
	if s == nil {
		return
	}
	reason := ClassifyFailure(addErr)
	// 1. Metric (always, no rate limit).
	//    Cardinality is bounded: node_id ~ tens of
	//    nodes per cluster, hostname ~ tens, reason
	//    ∈ {acl_reject, rpc_error, unknown}. Prom
	//    queries: `sum by (hostname) (rate(
	//    skygate_tag_autoupdate_failures_total[5m]))`
	//    surfaces "which node has been broken for
	//    the last 5 minutes".
	metrics.TagAutoupdateFailuresCounter.
		WithLabelValues(nodeID, hostname, string(reason)).
		Inc()
	// 2. Audit row (always, no rate limit). Use
	//    B221's AppendAuditLogWithTarget so /admin/audit
	//    can route by target_type="headscale_node".
	//    userID=0 + username="system" — the autoupdater
	//    has no human actor. target_id = nodeID (the
	//    headscale node id, string-typed to match the
	//    headscale CLI convention).
	if s.DB != nil {
		conn := s.DB.Current()
		if conn != nil {
			detail := buildFailureDetail(hostname, devTag, reason, addErr)
			if err := db.AppendAuditLogWithTarget(
				conn, 0, "system",
				"tag.autoupdate_failed",
				detail,
				"headscale_node", nodeID,
			); err != nil {
				log.Printf("tag-alert: audit_log write failed for node=%s hostname=%s: %v", nodeID, hostname, err)
			}
		}
	}
	// 3. Telegram alert (rate-limited to 1 per
	//    (node_id, reason) per hour). The operator's
	//    Telegram would flood with identical alerts
	//    at the 5-min autoupdater cadence; the 1h
	//    window ensures at most 1 alert per (node,
	//    reason) per hour. The audit_log + metric
	//    are the durable record of the failures
	//    between alerts.
	key := nodeID + "\x00" + string(reason)
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
	text := buildAlertText(nodeID, hostname, devTag, reason, addErr, now)
	if id := s.Notifier.SendAlert(text); id == 0 {
		// 0 = noopAlertSink (no Telegram bot). The
		// audit row + metric still record the event;
		// the operator can correlate via /admin/audit
		// or a Prom rate().
		log.Printf("tag-alert: SendAlert returned 0 (noop notifier); audit row + metric still recorded for node=%s reason=%s", nodeID, reason)
	}
}

// now returns the sink's current time. Production
// uses time.Now(); tests inject a deterministic clock.
func (s *TagAlertSink) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// buildFailureDetail renders the audit_log detail
// text (multi-line, operator-readable). Joined with
// "\n" so the field renders cleanly in /admin/audit.
func buildFailureDetail(hostname, devTag string, reason FailureReason, addErr error) string {
	var b strings.Builder
	b.WriteString("hostname=")
	b.WriteString(hostname)
	b.WriteString("\nfailed_tag=")
	b.WriteString(devTag)
	b.WriteString("\nreason=")
	b.WriteString(string(reason))
	b.WriteString("\nerror=")
	b.WriteString(addErr.Error())
	return b.String()
}

// buildAlertText renders the Telegram alert. Format
// matches the B225 family (emoji + multi-line body
// the operator can copy-paste into /admin/audit search).
func buildAlertText(nodeID, hostname, devTag string, reason FailureReason, addErr error, now time.Time) string {
	var b strings.Builder
	b.WriteString("❌ skygate tag autoupdate FAILED")
	b.WriteString("\nnode: ")
	b.WriteString(nodeID)
	b.WriteString(" (")
	b.WriteString(hostname)
	b.WriteString(")")
	b.WriteString("\nfailed tag: ")
	b.WriteString(devTag)
	b.WriteString("\nreason: ")
	b.WriteString(string(reason))
	b.WriteString("\nerror: ")
	b.WriteString(addErr.Error())
	b.WriteString("\ntimestamp: ")
	b.WriteString(now.UTC().Format(time.RFC3339))
	return b.String()
}
