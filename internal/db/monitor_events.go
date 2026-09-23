// internal/db/monitor_events.go — B305 (v1.5.70).
//
// Typed access to the monitoring inbox (v0.76 monitor_events). The semantics the
// page and the producers rely on:
//
//   * Report is an UPSERT keyed on fingerprint: a recurring condition updates ONE
//     row (repeats++, last_seen=now, state back to open) instead of adding a row
//     per occurrence, so a flapping alert cannot drown the list. It reports
//     whether the row was NEW (or reopened), which is what a notifier needs: a
//     repeat of a condition the operator already saw must not re-page them.
//   * Resolve marks a condition gone (state=resolved) without deleting history.
//   * Ack is the operator saying "I know" — it keeps the row in the list but out
//     of the unhandled count.
package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MonitorEvent is one row of the monitoring inbox.
type MonitorEvent struct {
	ID          int64
	Fingerprint string
	Source      string
	Subject     string
	Severity    string
	State       string
	Title       string
	Body        string
	Link        string
	FirstSeen   int64
	LastSeen    int64
	Repeats     int
	AckedAt     int64
	AckedBy     string
	ResolvedAt  int64
}

// MonitorEventListFilter is the page's query: which states, which minimum
// severity, how many rows.
type MonitorEventListFilter struct {
	// States to include (empty = all). Typical: ["open"] or ["open","acked"].
	States []string
	// Severities to include (empty = all). The page passes a minimum-severity
	// expansion done in Go, because SQL ordering of the four levels is awkward
	// across dialects.
	Severities []string
	Source     string
	Limit      int
}

const monitorEventCols = `id, fingerprint, source, subject, severity, state, title, body, link,
	first_seen, last_seen, repeats, acked_at, acked_by, resolved_at`

// ReportMonitorEvent upserts an event by fingerprint. Returns the row id and
// whether this call CREATED or REOPENED it (i.e. whether a notifier should fire).
//
// A row that was 'resolved' reopens on the next report; a row that is already
// 'open' only bumps last_seen/repeats and refreshes severity/title/body.
func ReportMonitorEvent(d *sql.DB, ev MonitorEvent) (id int64, created bool, err error) {
	if d == nil {
		return 0, false, fmt.Errorf("monitor_events: no database")
	}
	if strings.TrimSpace(ev.Fingerprint) == "" {
		return 0, false, fmt.Errorf("monitor_events: empty fingerprint (it is the dedup key)")
	}
	now := time.Now().Unix()
	if ev.FirstSeen == 0 {
		ev.FirstSeen = now
	}
	if ev.LastSeen == 0 {
		ev.LastSeen = now
	}
	if ev.Severity == "" {
		ev.Severity = "info"
	}
	if ev.State == "" {
		ev.State = "open"
	}

	// Was there a row, and in which state? Determines the "created" answer.
	var prevState string
	prevErr := d.QueryRow(`SELECT state FROM monitor_events WHERE fingerprint = $1`, ev.Fingerprint).Scan(&prevState)
	switch {
	case prevErr == sql.ErrNoRows:
		created = true
	case prevErr != nil:
		return 0, false, fmt.Errorf("monitor_events: read state: %w", prevErr)
	default:
		created = prevState != "open"
	}

	// The upsert works on both dialects (SQLite >= 3.24 and PostgreSQL both
	// support ON CONFLICT ... DO UPDATE). Reopening clears the resolved stamp but
	// KEEPS acked_at: the operator acknowledging an issue is information, and the
	// notifier decision is driven by `created`, not by the ack.
	const q = `INSERT INTO monitor_events
		(fingerprint, source, subject, severity, state, title, body, link, first_seen, last_seen, repeats)
		VALUES ($1, $2, $3, $4, 'open', $5, $6, $7, $8, $9, 1)
		ON CONFLICT(fingerprint) DO UPDATE SET
			source = excluded.source,
			subject = excluded.subject,
			severity = excluded.severity,
			state = 'open',
			title = excluded.title,
			body = excluded.body,
			link = excluded.link,
			last_seen = excluded.last_seen,
			repeats = monitor_events.repeats + 1,
			resolved_at = 0`
	if _, err := d.Exec(q, ev.Fingerprint, ev.Source, ev.Subject, ev.Severity,
		ev.Title, ev.Body, ev.Link, ev.FirstSeen, ev.LastSeen); err != nil {
		return 0, false, fmt.Errorf("monitor_events: upsert: %w", err)
	}
	if err := d.QueryRow(`SELECT id FROM monitor_events WHERE fingerprint = $1`, ev.Fingerprint).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("monitor_events: read back id: %w", err)
	}
	return id, created, nil
}

// ResolveMonitorEvent marks a condition as gone. Returns the number of rows
// changed (0 when the fingerprint is unknown or was already resolved).
func ResolveMonitorEvent(d *sql.DB, fingerprint string) (int64, error) {
	if d == nil || strings.TrimSpace(fingerprint) == "" {
		return 0, nil
	}
	res, err := d.Exec(`UPDATE monitor_events
		SET state = 'resolved', resolved_at = $2
		WHERE fingerprint = $1 AND state <> 'resolved'`, fingerprint, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("monitor_events: resolve: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListMonitorEvents returns the rows the page shows, newest activity first.
func ListMonitorEvents(d *sql.DB, f MonitorEventListFilter) ([]MonitorEvent, error) {
	if d == nil {
		return nil, nil
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var where []string
	var args []any
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholdersFromTo(len(args)+1, len(args)+len(f.States))+")")
		for _, s := range f.States {
			args = append(args, s)
		}
	}
	if len(f.Severities) > 0 {
		where = append(where, "severity IN ("+placeholdersFromTo(len(args)+1, len(args)+len(f.Severities))+")")
		for _, s := range f.Severities {
			args = append(args, s)
		}
	}
	if s := strings.TrimSpace(f.Source); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("source = $%d", len(args)))
	}
	q := `SELECT ` + monitorEventCols + ` FROM monitor_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY last_seen DESC, id DESC LIMIT $%d", len(args))

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("monitor_events: list: %w", err)
	}
	defer rows.Close()
	out := []MonitorEvent{}
	for rows.Next() {
		var e MonitorEvent
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Source, &e.Subject, &e.Severity, &e.State,
			&e.Title, &e.Body, &e.Link, &e.FirstSeen, &e.LastSeen, &e.Repeats,
			&e.AckedAt, &e.AckedBy, &e.ResolvedAt); err != nil {
			return nil, fmt.Errorf("monitor_events: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountMonitorEventsByState returns the per-state counts for the page badges
// (open / acked / resolved).
func CountMonitorEventsByState(d *sql.DB) (map[string]int, error) {
	out := map[string]int{}
	if d == nil {
		return out, nil
	}
	rows, err := d.Query(`SELECT state, COUNT(*) FROM monitor_events GROUP BY state`)
	if err != nil {
		return out, fmt.Errorf("monitor_events: count: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return out, fmt.Errorf("monitor_events: count scan: %w", err)
		}
		out[state] = n
	}
	return out, rows.Err()
}

// CountOpenMonitorEventsNotLowerThan counts unhandled (open) events whose severity
// is at least minRank — the number the nav badge shows. Ranking is done in Go
// because the four levels are not ordered by their text in SQL.
func CountOpenMonitorEventsNotLowerThan(d *sql.DB, minRank int, ranks map[string]int) (int, error) {
	if d == nil {
		return 0, nil
	}
	rows, err := d.Query(`SELECT severity FROM monitor_events WHERE state = 'open'`)
	if err != nil {
		return 0, fmt.Errorf("monitor_events: open count: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var sev string
		if err := rows.Scan(&sev); err != nil {
			return 0, fmt.Errorf("monitor_events: open count scan: %w", err)
		}
		if ranks[sev] >= minRank {
			n++
		}
	}
	return n, rows.Err()
}

// AckMonitorEvent marks one row acknowledged by an operator.
func AckMonitorEvent(d *sql.DB, id int64, by string) (int64, error) {
	if d == nil || id <= 0 {
		return 0, nil
	}
	res, err := d.Exec(`UPDATE monitor_events
		SET acked_at = $2, acked_by = $3,
		    state = CASE WHEN state = 'resolved' THEN 'resolved' ELSE 'acked' END
		WHERE id = $1`, id, time.Now().Unix(), by)
	if err != nil {
		return 0, fmt.Errorf("monitor_events: ack: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// AckAllOpenMonitorEvents acknowledges every OPEN event (the page's "acknowledge
// all" button). Resolved rows are left alone.
func AckAllOpenMonitorEvents(d *sql.DB, by string) (int64, error) {
	if d == nil {
		return 0, nil
	}
	res, err := d.Exec(`UPDATE monitor_events
		SET acked_at = $1, acked_by = $2, state = 'acked'
		WHERE state = 'open'`, time.Now().Unix(), by)
	if err != nil {
		return 0, fmt.Errorf("monitor_events: ack all: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
