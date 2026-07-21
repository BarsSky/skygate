// 2026-07-14: Этап 14 v8 — release-monitor runner.
//
// This file adds the long-running loop on top of monitor.go.
// The Monitor struct holds the runtime state (current version,
// a dedup map of already-notified tags, a notifier sink)
// and Start() launches the tick goroutine.
//
// Why a dedup map and not just a single "last seen tag" string?
// The Notifier.SendAlert signature may return before the
// Telegram message is delivered (HTTP timeout, rate limit at
// Telegram, etc). The dedup map ensures we don't spam the
// admin with the same release N times across N hourly ticks
// if the first send silently failed. We reset the map only
// when the running version itself changes (an admin
// upgraded) — a successful upgrade is the natural "I have
// seen this" signal.

package release

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

// NotifierSink is the subset of the telegram.Notifier
// interface that the monitor needs. Defined as an
// interface so we can wire a no-op in tests.
type NotifierSink interface {
	SendAlert(text string) int64
}

// Monitor holds the runtime state of the release-monitor
// goroutine. One instance per process.
type Monitor struct {
	HTTP      *http.Client
	Current   string            // running version (set at build time, e.g. "v0.10.7")
	Notified  map[string]bool   // dedup: tags we've already sent an alert for
	Notifier  NotifierSink      // alert sink
	CheckEvery time.Duration    // 1h default

	mu     sync.Mutex  // protects Notified + Latest + LastCheckedAt

	// 2026-07-15: v0.14.0 — web UI integration. The handler
	// reads (Latest, UpdateAvailable, CheckedAt) on every
	// page render to surface a "newer version available"
	// banner on /dashboard and the admin pages. Latest is
	// the most recent release returned by GitHub (whatever
	// the latest tick saw); UpdateAvailable is the
	// precomputed boolean (Latest.TagName > Current). The
	// monitor updates these on every tick, so the banner
	// disappears within an hour of an upgrade.
	Latest          Release
	UpdateAvailable bool
	CheckedAt       time.Time
}

// Snapshot returns the current state of the monitor in a
// copy (so callers can read the fields without holding the
// mutex). Used by App's render path to read the monitor's
// state on every page render.
func (m *Monitor) Snapshot() (latest Release, updateAvailable bool, checkedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Latest, m.UpdateAvailable, m.CheckedAt
}

// Start launches the background loop. The first tick fires
// after one CheckEvery interval (so a fresh start doesn't
// spam admins with "what's available now" alerts on every
// restart). Returns immediately; cancel ctx to stop.
func (m *Monitor) Start(ctx context.Context) {
	if m.HTTP == nil {
		m.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if m.Notified == nil {
		m.Notified = make(map[string]bool)
	}
	if m.CheckEvery == 0 {
		m.CheckEvery = 1 * time.Hour
	}
	go func() {
		t := time.NewTicker(m.CheckEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.tick(ctx)
			}
		}
	}()
}

// tick is one pass of the monitor. Public for testability
// (tests call tick directly with a mock HTTP server and a
// no-op NotifierSink).
func (m *Monitor) tick(ctx context.Context) {
	c := &Client{HTTP: m.HTTP, Repo: "BarsSky/skygate"}
	r, err := c.Latest(ctx)
	if err != nil {
		// Rate-limited: stay quiet, retry next tick.
		// Other errors: log so the operator can see it
		// in the container log but don't spam alerts.
		if err != ErrRateLimited {
			log.Printf("release-monitor: poll failed: %v", err)
		}
		return
	}
	// 2026-07-15: v0.14.0 — update the snapshot regardless
	// of whether the new release is "interesting". The web
	// banner wants to know "is there ANY newer release out
	// there?" so the operator can decide when to upgrade;
	// the dedup path below (which only fires for
	// semver-greater tags) is separate.
	m.mu.Lock()
	m.Latest = *r
	m.CheckedAt = time.Now()
	if r.TagName != "" && CompareSemver(r.TagName, m.Current) > 0 {
		m.UpdateAvailable = true
	} else {
		m.UpdateAvailable = false
	}
	m.mu.Unlock()

	// Same version running — nothing to do.
	if r.TagName == m.Current {
		return
	}
	// Newer than running? Compare semver.
	if CompareSemver(r.TagName, m.Current) <= 0 {
		// Older or same (e.g. a -dev build running against
		// a release). Don't alert.
		return
	}
	// Dedup.
	m.mu.Lock()
	already := m.Notified[r.TagName]
	m.Notified[r.TagName] = true
	m.mu.Unlock()
	if already {
		return
	}
	if m.Notifier == nil {
		return
	}
	current := &Release{TagName: m.Current}
	alert := FormatAlert(current, r)
	id := m.Notifier.SendAlert(alert)
	log.Printf("release-monitor: alert sent for %s (running %s), alert_id=%d", r.TagName, m.Current, id)
}

// ResetNotified wipes the dedup map. Call after a successful
// upgrade (the new version is now Current; old notifications
// don't apply any more). Also clears UpdateAvailable so the
// /dashboard banner disappears immediately on upgrade
// (instead of waiting up to CheckEvery for the next tick
// to overwrite it).
func (m *Monitor) ResetNotified() {
	m.mu.Lock()
	m.Notified = make(map[string]bool)
	m.UpdateAvailable = false
	m.mu.Unlock()
}
