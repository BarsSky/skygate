// Package monitorinbox — B305 (v1.5.70).
//
// One sink for every monitoring signal skygate produces, with four severities,
// deduplication and an operator-facing page. The operator asked for «поле с
// уведомлениями куда будут приходить все сообщения разной важности с мониторинга
// skygate» — before this package every signal lived in its own place and most of
// them were fire-and-forget: an exit-node health crossing went to Telegram and
// vanished, a tag-reconcile failure was a metric plus an audit row, a failing
// system test was rendered once and forgotten, a degraded database was a badge on
// one page. Nothing answered "what is wrong right now, and is it new?".
//
// Design:
//
//   - Report() records an event through db.ReportMonitorEvent (dedup by
//     fingerprint) and returns whether it was NEW or REOPENED. A repeat of a
//     condition the operator has already seen bumps counters and does NOT
//     re-notify, which is what makes a flapping alert tolerable.
//   - The Telegram push is a POLICY, not the record: events at or above
//     MinNotify (default: error) are pushed, everything is stored. The threshold
//     lives in global_settings under SettingNotifyMinSeverity so the operator can
//     lower it to warning or raise it to critical without a restart.
//   - Resolve() closes a condition (the producer knows it went away — e.g. the
//     system test passed again), keeping the row as history instead of deleting
//     it.
//
// The package is deliberately tiny and dependency-light: producers in other
// packages depend on it, so it must not depend on them.
package monitorinbox

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"

	"skygate/internal/db"
)

// Severity is the urgency of an event. The four levels are the badge classes the
// panel already uses, plus "critical" for something that breaks the tailnet.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// SettingNotifyMinSeverity is the global_settings key holding the lowest severity
// that is also pushed to Telegram (everything is always recorded).
const SettingNotifyMinSeverity = "monitor.notify_min_severity"

// severityRanks orders the levels. Rank is also what the panel uses to expand
// "warning and above" into a list for the SQL filter.
var severityRanks = map[Severity]int{
	SeverityInfo:     0,
	SeverityWarning:  1,
	SeverityError:    2,
	SeverityCritical: 3,
}

// SeverityRank returns the numeric rank of a severity (unknown → info).
func SeverityRank(s Severity) int {
	if r, ok := severityRanks[Normalize(string(s))]; ok {
		return r
	}
	return 0
}

// Normalize maps arbitrary producer text onto the four levels. Producers spell
// their urgency differently ("warn", "WARNING", "fatal"), and a typo must not
// create an uncounted fifth level.
func Normalize(raw string) Severity {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical", "fatal", "panic", "emergency":
		return SeverityCritical
	case "error", "err", "fail", "failure":
		return SeverityError
	case "warning", "warn", "degraded":
		return SeverityWarning
	case "info", "notice", "ok", "":
		return SeverityInfo
	default:
		return SeverityInfo
	}
}

// LevelsAtLeast expands a minimum severity into the list the SQL filter needs
// ("warning and above" → warning, error, critical). Ordered highest first.
func LevelsAtLeast(min Severity) []string {
	minRank := SeverityRank(min)
	out := make([]string, 0, len(severityRanks))
	for sev, rank := range severityRanks {
		if rank >= minRank {
			out = append(out, string(sev))
		}
	}
	sort.Slice(out, func(i, j int) bool { return SeverityRank(Severity(out[i])) > SeverityRank(Severity(out[j])) })
	return out
}

// AllLevels lists every severity, highest first (the page's filter dropdown).
func AllLevels() []Severity {
	return []Severity{SeverityCritical, SeverityError, SeverityWarning, SeverityInfo}
}

// Notifier is the subset of telegram.Notifier the inbox pushes through.
type Notifier interface {
	SendAlert(text string) int64
}

// Event is one reportable monitoring signal.
type Event struct {
	// Source names the subsystem ("system_test", "module:tailscale", "db_health",
	// "exit_node", "derp", "tag_reconcile", "update", "backup", …).
	Source string
	// Subject is what the event is about ("headscale.ping", a hostname, a node
	// id, a prefix). Source+Subject form the dedup fingerprint.
	Subject string
	Severity Severity
	Title    string
	Body     string
	// Link is the panel page that fixes it ("" = nowhere specific).
	Link string
	// Fingerprint overrides the default Source:Subject key. Use it when one
	// subject can carry several independent conditions (append the condition).
	Fingerprint string
}

// Reported describes what Report did.
type Reported struct {
	ID          int64
	Fingerprint string
	Severity    Severity
	// Created is true when the event is new or was reopened — the only case a
	// notifier should fire for.
	Created bool
	// Notified is true when this call pushed the event (Created + above the
	// threshold + a notifier configured).
	Notified bool
}

// Inbox records events and (optionally) pushes the important ones.
type Inbox struct {
	// DB is the database source (nil = the inbox records nothing and says so
	// through Log rather than silently dropping events).
	DB db.DBSource
	// Notifier receives the text for events at or above the threshold.
	Notifier Notifier
	// MinNotify is the lowest severity that is also pushed. Empty = read
	// SettingNotifyMinSeverity from the database, defaulting to "error" — i.e.
	// warnings are recorded but do not page anyone unless the operator asks.
	MinNotify Severity
	// Logf is the log sink (defaults to log.Printf).
	Logf func(format string, args ...any)
}

func (in *Inbox) logf(format string, args ...any) {
	if in == nil {
		return
	}
	if in.Logf != nil {
		in.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// FingerprintFor returns the dedup key of an event: an explicit Fingerprint, else
// "<source>:<subject>". An event with neither is refused by the store.
func FingerprintFor(ev Event) string {
	if fp := strings.TrimSpace(ev.Fingerprint); fp != "" {
		return fp
	}
	src := strings.TrimSpace(ev.Source)
	sub := strings.TrimSpace(ev.Subject)
	if src == "" {
		return sub
	}
	if sub == "" {
		return src
	}
	return src + ":" + sub
}

// minNotify resolves the push threshold: explicit field, then the stored setting,
// then "error".
func (in *Inbox) minNotify() Severity {
	if in != nil && strings.TrimSpace(string(in.MinNotify)) != "" {
		return Normalize(string(in.MinNotify))
	}
	if in != nil && in.DB != nil && in.DB.Current() != nil {
		if v, err := db.GetGlobalSetting(in.DB.Current(), SettingNotifyMinSeverity, "error"); err == nil && strings.TrimSpace(v) != "" {
			return Normalize(v)
		}
	}
	return SeverityError
}

// Report records an event and, when it is new/reopened and at or above the push
// threshold, sends one Telegram alert.
//
// A reporting failure is returned to the caller but never prevents the alert: the
// alert is the part a human must see, while the row is history.
func (in *Inbox) Report(ev Event) (Reported, error) {
	out := Reported{Severity: Normalize(string(ev.Severity)), Fingerprint: FingerprintFor(ev)}
	if in == nil {
		return out, fmt.Errorf("monitorinbox: nil inbox")
	}
	if in.DB == nil || in.DB.Current() == nil {
		in.logf("monitor: cannot record %q (%s): no database", ev.Title, out.Fingerprint)
		return out, fmt.Errorf("monitorinbox: no database")
	}
	ev.Severity = out.Severity
	id, created, err := db.ReportMonitorEvent(in.DB.Current(), db.MonitorEvent{
		Fingerprint: out.Fingerprint,
		Source:      strings.TrimSpace(ev.Source),
		Subject:     strings.TrimSpace(ev.Subject),
		Severity:    string(out.Severity),
		Title:       ev.Title,
		Body:        ev.Body,
		Link:        ev.Link,
	})
	out.ID, out.Created = id, created
	if err != nil {
		in.logf("monitor: record failed for %q: %v", out.Fingerprint, err)
	} else if created {
		in.logf("monitor: %s [%s] %s (%s)", strings.ToUpper(string(out.Severity)), ev.Source, ev.Title, out.Fingerprint)
	}

	if !created || SeverityRank(out.Severity) < SeverityRank(in.minNotify()) || in.Notifier == nil {
		return out, err
	}
	if in.Notifier.SendAlert(FormatAlert(ev, out.Severity)) >= 0 {
		out.Notified = true
	}
	return out, err
}

// Resolve closes a condition. Producers call it when they observe recovery (a
// test passes, a relay is healthy again). Unknown fingerprints are a no-op.
func (in *Inbox) Resolve(ev Event) error {
	if in == nil || in.DB == nil || in.DB.Current() == nil {
		return nil
	}
	fp := FingerprintFor(ev)
	n, err := db.ResolveMonitorEvent(in.DB.Current(), fp)
	if err != nil {
		return err
	}
	if n > 0 {
		in.logf("monitor: resolved %s", fp)
	}
	return nil
}

// FormatAlert renders the Telegram text for an event. The severity is spelled out
// rather than left to an emoji, because the operator reads these on a phone.
func FormatAlert(ev Event, sev Severity) string {
	icon := map[Severity]string{
		SeverityInfo:     "ℹ️",
		SeverityWarning:  "⚠️",
		SeverityError:    "❌",
		SeverityCritical: "🚨",
	}[Normalize(string(sev))]
	var b strings.Builder
	b.WriteString(icon + " skygate " + string(Normalize(string(sev))))
	if src := strings.TrimSpace(ev.Source); src != "" {
		b.WriteString(" [" + src + "]")
	}
	b.WriteString("\n" + strings.TrimSpace(ev.Title))
	if sub := strings.TrimSpace(ev.Subject); sub != "" {
		b.WriteString("\n" + sub)
	}
	if body := strings.TrimSpace(ev.Body); body != "" {
		b.WriteString("\n" + body)
	}
	if link := strings.TrimSpace(ev.Link); link != "" {
		b.WriteString("\n" + link)
	}
	return b.String()
}

// OpenCount returns how many events are open (unhandled) at or above min, for the
// navigation badge.
func (in *Inbox) OpenCount(min Severity) int {
	if in == nil || in.DB == nil || in.DB.Current() == nil {
		return 0
	}
	ranks := map[string]int{}
	for sev, r := range severityRanks {
		ranks[string(sev)] = r
	}
	n, err := db.CountOpenMonitorEventsNotLowerThan(in.DB.Current(), SeverityRank(min), ranks)
	if err != nil {
		in.logf("monitor: open count failed: %v", err)
		return 0
	}
	return n
}

// Compile-time guard: the inbox only needs the DB source shape.
var _ = func() bool { var _ db.DBSource; return true }
var _ = sql.ErrNoRows
