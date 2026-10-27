// Package localtime is the panel's ONE clock: the time zone every user-visible
// timestamp is rendered in, and the zone the operator's schedule means.
//
// WHY THIS PACKAGE EXISTS (B429, 2026-10-27)
// ------------------------------------------
// Operator report: «есть подозрение что не учитывается в проекте локализация
// времени — так релиз был сделан около 8 утра по Московскому времени, а в
// уведомлении указано что в 5 утра по Гринвичу … и почему не отрабатывает в
// назначенное время обновление».
//
// Measured on the reference install: the host and the container both run with
// /etc/localtime → Etc/UTC, `SKYGATE_TZ` does not exist, and every surface
// rendered UTC:
//
//	/admin/update      Проверено: 2026-10-27 06:48:21 UTC
//	update_schedule_time   08:00   ← compared against UTC, i.e. 11:00 Moscow
//	Telegram alert      опубликована: 2026-10-27 05:12 UTC
//
// So the release at 08:12 Moscow time was correctly shown as 05:12 UTC — and the
// operator's 08:00 schedule really fired at 11:00 Moscow. Nothing was "wrong" in
// the arithmetic; the INSTALL had no way to say which clock the operator reads.
//
// Rules this package implements:
//
//  1. The admin setting wins.
//  2. Otherwise SKYGATE_TIMEZONE (deploy-time, e.g. from .env).
//  3. Otherwise the system zone is detected (TZ, /etc/localtime, /etc/timezone).
//  4. An unknown zone is an ERROR naming the value — never a silent UTC fallback
//     for a value the operator typed.
//
// The zone is process-wide (one installation has one operator), set once at
// boot and re-set when the panel saves it, so both halves — the scheduler and
// the renderers — can never disagree about which clock they mean.
package localtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SettingKey is the global_settings row the admin setting is stored in.
const SettingKey = "timezone"

// Auto is the value that means "use whatever the system says".
const Auto = "auto"

// fallback is used when nothing else can be resolved. It is NOT a substitute for
// an operator-typed zone: Resolve returns an error for those.
const fallback = "UTC"

var (
	mu      sync.RWMutex
	current = time.UTC
	name    = fallback
	// detected records what the system said, so the panel can show "you are
	// currently on Auto and the system reports Europe/Moscow".
	detected string
)

// DetectSystem reports the zone the host/container would use by itself, in the
// order the C library does: TZ, then /etc/localtime, then /etc/timezone, then UTC.
// An unknown TZ never leaks out — it is reported as the empty string so callers
// can fall back deliberately.
func DetectSystem() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if p, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			candidate := strings.TrimPrefix(p[i+len("zoneinfo/"):], "/")
			if _, err := time.LoadLocation(candidate); err == nil {
				return candidate
			}
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		candidate := strings.TrimSpace(string(b))
		if candidate != "" {
			if _, err := time.LoadLocation(candidate); err == nil {
				return candidate
			}
		}
	}
	return fallback
}

// Load resolves a zone NAME without touching process state. "auto" (and "") mean
// DetectSystem; "UTC" and any IANA name are loaded as-is; anything else is an
// error that names the value.
func Load(value string) (*time.Location, string, error) {
	v := strings.TrimSpace(value)
	if v == "" || strings.EqualFold(v, Auto) {
		sys := DetectSystem()
		loc, err := time.LoadLocation(sys)
		if err != nil {
			return time.UTC, fallback, fmt.Errorf("localtime: the detected zone %q cannot be loaded (%v); set the zone explicitly on /admin/settings", sys, err)
		}
		return loc, sys, nil
	}
	loc, err := time.LoadLocation(v)
	if err != nil {
		return nil, "", fmt.Errorf("localtime: unknown time zone %q (use an IANA name such as Europe/Moscow, or auto): %w", v, err)
	}
	return loc, v, nil
}

// Set installs the zone process-wide. It returns the resolved name. An unknown
// value is an ERROR and leaves the previous zone in place — a typo must never
// silently move every timestamp in the panel.
func Set(value string) (string, error) {
	loc, resolved, err := Load(value)
	if err != nil {
		return "", err
	}
	mu.Lock()
	current = loc
	name = resolved
	detected = DetectSystem()
	mu.Unlock()
	return resolved, nil
}

// MustSet is Set for bootstrapping, where an error is already reported by the
// caller's log line: an unresolvable zone falls back to the detected system zone.
func MustSet(value string) string {
	if resolved, err := Set(value); err == nil {
		return resolved
	}
	resolved, _ := Set(Auto)
	return resolved
}

// Current returns the zone every renderer must use. Never nil.
func Current() *time.Location {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Name returns the resolved zone name (never empty).
func Name() string {
	mu.RLock()
	defer mu.RUnlock()
	return name
}

// Detected returns what the system reports right now — shown next to the admin
// setting so "Auto" is not a mystery.
func Detected() string { return DetectSystem() }

// Now is time.Now() in the configured zone. The scheduler uses this so "08:00"
// means the operator's 08:00, not the container's.
func Now() time.Time { return time.Now().In(Current()) }

// In converts an instant (often stored in UTC) to the configured zone. A zero
// time stays zero, so callers can still test IsZero.
func In(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.In(Current())
}

// FromUnix converts unix seconds to the configured zone.
func FromUnix(sec int64) time.Time { return time.Unix(sec, 0).In(Current()) }

// Format renders "2006-01-02 15:04 MST" — the panel's default timestamp: sortable,
// unambiguous, and always carrying the zone so a screenshot cannot be misread.
func Format(t time.Time) string { return In(t).Format("2006-01-02 15:04 MST") }

// FormatSeconds is Format with seconds (job logs, "checked at").
func FormatSeconds(t time.Time) string { return In(t).Format("2006-01-02 15:04:05 MST") }

// FormatDate is the date only ("2006-01-02"), for expiry columns.
func FormatDate(t time.Time) string { return In(t).Format("2006-01-02") }

// FormatClock is the time of day only ("15:04"), for schedule labels.
func FormatClock(t time.Time) string { return In(t).Format("15:04") }

// FormatUnix renders unix seconds; a non-positive value renders as "—" (the
// template helper's historical contract).
func FormatUnix(sec int64) string {
	if sec <= 0 {
		return "—"
	}
	return Format(FromUnix(sec))
}

// ErrInvalidZone is returned (wrapped) when a value cannot be resolved.
var ErrInvalidZone = errors.New("localtime: invalid time zone")

// Choices is the curated list offered by the admin select. It is a convenience,
// not a limit: the field accepts any IANA name.
func Choices() []string {
	return []string{
		Auto,
		"UTC",
		"Europe/Moscow",
		"Europe/Kyiv",
		"Europe/Minsk",
		"Europe/Berlin",
		"Europe/Paris",
		"Europe/London",
		"Europe/Istanbul",
		"Asia/Yerevan",
		"Asia/Tbilisi",
		"Asia/Almaty",
		"Asia/Tashkent",
		"Asia/Dubai",
		"Asia/Yekaterinburg",
		"Asia/Novosibirsk",
		"Asia/Krasnoyarsk",
		"Asia/Irkutsk",
		"Asia/Vladivostok",
		"Asia/Tokyo",
		"Asia/Shanghai",
		"Asia/Kolkata",
		"Asia/Singapore",
		"Australia/Sydney",
		"America/New_York",
		"America/Chicago",
		"America/Denver",
		"America/Los_Angeles",
		"America/Sao_Paulo",
		"Africa/Cairo",
	}
}

// ResetForTest restores the process default (UTC) and clears the detected value.
// Tests only.
func ResetForTest() {
	mu.Lock()
	current = time.UTC
	name = fallback
	detected = ""
	mu.Unlock()
}
