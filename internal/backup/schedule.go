// 2026-07-14: Этап 14 v6 — tiny cron-style schedule parser.
//
// We intentionally avoid pulling in github.com/robfig/cron/v3
// because the only patterns the UI exposes are "every minute" and
// "at HH:MM daily". A 60-line parser is easier to audit and
// doesn't add a third-party dep to go.mod.
//
// Supported forms (whitespace-tolerant):
//
//	"*"          — every minute
//	"M H"        — at minute M of hour H, daily
//	"M H * * *"  — same as above
//
// Anything else returns an error; the admin should use system
// cron for more complex schedules. ParseSchedule is also
// invoked by Config.Validate() so the form catches bad input
// before the user clicks "Run now".

package backup

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron-style expression. next returns
// the next time (UTC) the schedule fires, given a reference
// time. Callers use time.Now() as the reference.
type Schedule struct {
	// "*" means every minute; Minute and Hour are ignored
	// in that case.
	EveryMinute bool
	Minute      int // 0-59
	Hour        int // 0-23
}

// ParseSchedule parses the 2- or 5-field form described above.
// Empty string is treated as "*" (every minute) — matches
// the de-facto cron default.
func ParseSchedule(s string) (*Schedule, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return &Schedule{EveryMinute: true}, nil
	}
	fields := strings.Fields(s)
	switch len(fields) {
	case 2:
		// "M H" form.
		m, err := parseIntField(fields[0], 0, 59, "minute")
		if err != nil {
			return nil, err
		}
		h, err := parseIntField(fields[1], 0, 23, "hour")
		if err != nil {
			return nil, err
		}
		return &Schedule{Minute: m, Hour: h}, nil
	case 5:
		// "M H * * *" form. We only care about the first
		// two fields; the rest must be "*" (we don't
		// support day-of-week or day-of-month yet).
		for i, f := range fields {
			if i < 2 {
				continue
			}
			if f != "*" {
				return nil, errors.New("only '*' allowed in day-of-month / month / day-of-week fields")
			}
		}
		m, err := parseIntField(fields[0], 0, 59, "minute")
		if err != nil {
			return nil, err
		}
		h, err := parseIntField(fields[1], 0, 23, "hour")
		if err != nil {
			return nil, err
		}
		return &Schedule{Minute: m, Hour: h}, nil
	default:
		return nil, errors.New("schedule must be '*', 'M H', or 'M H * * *'")
	}
}

// Next returns the next time the schedule fires at or after
// `from` (UTC). For "every minute" the answer is always
// `from` truncated to the minute (caller compares against
// time.Now() and we want stable behavior at the minute
// boundary). For "M H daily" we step forward to the next
// matching minute-of-hour. We deliberately don't try to
// be clever with DST or timezones — the UI exposes the
// field as a local-time cron-style expression and the
// scheduler runs in the container's local time.
func (s *Schedule) Next(from time.Time) time.Time {
	if s.EveryMinute {
		// Round down to the minute.
		return time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), from.Minute(), 0, 0, from.Location())
	}
	// Find the next moment at s.Hour:s.Minute. If from is
	// already past today's slot, jump to tomorrow.
	now := from
	candidate := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate
}

func parseIntField(s string, lo, hi int, name string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "*" {
		return 0, errors.New(name + " field cannot be '*' in 'M H' form")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.New(name + " field is not an integer: " + s)
	}
	if n < lo || n > hi {
		return 0, errors.New(name + " out of range")
	}
	return n, nil
}
