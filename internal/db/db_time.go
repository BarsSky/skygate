// internal/db/db_time.go — dialect-agnostic decoding of a timestamp
// column into a time.Time.
//
// WHY THIS EXISTS (B282, 2026-09-22)
// ----------------------------------
// The same logical column is stored with a different Go/driver value
// depending on the backend:
//
//   - PostgreSQL TIMESTAMPTZ → pgx hands the caller a time.Time;
//   - SQLite has no timestamp type at all. skygate stores Unix seconds
//     as INTEGER (audit_log.created_at, migration v0.47) while some
//     older DDL still carries `INTEGER ... DEFAULT CURRENT_TIMESTAMP`
//     (cluster_audit.created_at in migrations_sqlite.go), and
//     CURRENT_TIMESTAMP in SQLite evaluates to the TEXT form
//     "2006-01-02 15:04:05". So the very same column yields int64 OR
//     string depending on how the row was written.
//
// A reader that scans straight into a time.Time therefore works on PG
// and dies on SQLite ("unsupported Scan, storing driver.Value type
// int64 into type *time.Time"), and a reader that hardcodes
// `to_timestamp(created_at)` (PostgreSQL-only) does not even parse
// there — the live failure was /admin/audit answering 500 with
// `SQL logic error: unrecognized token: ":"` on the native `aro` host.
//
// The two halves of the fix are: build the SELECT through a dialect
// switch (to_timestamp on PG, the raw column on SQLite) and decode
// whatever the driver returns here, tolerantly.
package db

import (
	"strconv"
	"strings"
	"time"
)

// dbTimeLayouts are the textual timestamp shapes skygate can meet.
// Ordered most-specific first: RFC3339 with and without sub-second
// precision, then SQLite's CURRENT_TIMESTAMP form, then a bare date.
var dbTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	// Go's time.Time.String() form. B291 (2026-09-22): the modernc.org/sqlite
	// driver stores a BOUND time.Time as exactly this string
	// ("2026-09-23 03:29:58.2457633 +0000 UTC"), which none of the layouts above
	// match — so every timestamp the cluster/HA writers had stored on SQLite was
	// undecodable ("/admin/cluster shows no timestamps"). New code binds
	// DialectKind.TimeValue, which stores RFC3339 on SQLite; this layout keeps the
	// rows already written by the old code readable.
	"2006-01-02 15:04:05.999999999 -0700 MST",
}

// ParseDBTime decodes a value returned by database/sql for a timestamp
// column into a time.Time. The second return is false when the value is
// NULL or in a shape we do not recognise (the caller decides whether
// that is a skip or an error — a timestamp we cannot read must never be
// silently rendered as 1970).
//
// Handled shapes:
//
//	time.Time → as-is (pgx, and modernc.org/sqlite for explicit
//	            timestamps)
//	int64     → Unix seconds (the skygate convention for
//	            audit_log.created_at and friends)
//	float64   → Unix seconds with a fractional part
//	[]byte /
//	string    → all-digit text is Unix seconds; otherwise tried against
//	            the RFC3339 / "2006-01-02 15:04:05" family (SQLite's
//	            CURRENT_TIMESTAMP and PG's default text rendering)
func ParseDBTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case nil:
		return time.Time{}, false
	case time.Time:
		return t, true
	case int64:
		return time.Unix(t, 0).UTC(), true
	case int:
		return time.Unix(int64(t), 0).UTC(), true
	case float64:
		sec := int64(t)
		return time.Unix(sec, int64((t-float64(sec))*float64(time.Second))).UTC(), true
	case []byte:
		return parseDBTimeText(string(t))
	case string:
		return parseDBTimeText(t)
	default:
		return time.Time{}, false
	}
}

// parseDBTimeText decodes the textual shapes listed on ParseDBTime.
func parseDBTimeText(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// All-digit (optionally signed) text is Unix seconds. The check is
	// explicit rather than a strconv error check so "2026" is not read
	// as a year-shaped date by accident — skygate never stores a bare
	// year, and the layouts below would not match it anyway.
	if isAllDigits(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(n, 0).UTC(), true
		}
		return time.Time{}, false
	}
	for _, layout := range dbTimeLayouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts.UTC(), true
		}
	}
	return time.Time{}, false
}

// isAllDigits reports whether s is a non-empty run of ASCII digits
// (an optional leading '-' is accepted so a pre-1970 timestamp still
// decodes).
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[0] == '+' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
