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
	"database/sql"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/telegram"
)

// NotifierSink is the subset of the telegram.Notifier
// interface that the monitor needs. Defined as an
// interface so we can wire a no-op in tests.
type NotifierSink interface {
	SendAlert(text string) int64
}

// Monitor holds the runtime state of the release-monitor
// goroutine. One instance per process.
//
// 2026-08-05 v0.33.1.10: Owner / Repo (GitHub coordinates)
// moved onto the Monitor itself; the scheduler tick reads
// them when building a Client, so a single Monitor can
// serve a fork / staging deploy pointed at a different
// repo via SKYGATE_GITHUB_REPO_OWNER / _NAME env vars.
type Monitor struct {
	HTTP       *http.Client
	Current    string          // running version (set at build time, e.g. "v0.10.7")
	Notified   map[string]bool // dedup: tags we've already sent an alert for
	Notifier   NotifierSink    // alert sink
	CheckEvery time.Duration   // 1h default
	// FirstDelay (B425) is how long after boot the FIRST check runs. Zero means
	// DefaultFirstDelay (2 min). Kept as a field so tests can drive it.
	FirstDelay time.Duration
	// DB (B425) is optional: when set, the last ANNOUNCED tag is persisted in
	// global_settings (release.notified_tag) so a restart neither repeats an alert nor
	// waits a whole interval before looking.
	DB *sql.DB
	// APIBase (B425) overrides the GitHub API host for the polls (tests; an air-gapped
	// mirror). Empty means api.github.com.
	APIBase string
	Owner   string // GitHub owner (user or org) for the Releases API
	Repo    string // GitHub repo name for the Releases API

	mu sync.Mutex // protects Notified + Latest + LastCheckedAt

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

// Start launches the background loop.
//
// B425 (2026-10-26): the FIRST check now runs shortly after boot instead of after a full
// CheckEvery interval. Measured: the operator received an update-available alert ~4 hours
// after the release was published, and two causes stacked. (1) The old code deliberately
// waited one whole interval "so a fresh start doesn't spam admins on every restart" — but a
// container is restarted by every update, so the channel went quiet for an hour exactly when
// the operator was watching, and a restart loop could postpone it indefinitely. (2) The poll
// it eventually made could be answered with 403 because /admin/update was spending the same
// anonymous GitHub quota (fixed in B424), and this file then stayed silent. The anti-spam
// purpose moved where it belongs: the last ANNOUNCED tag is persisted
// (`release.notified_tag`), so a restart re-reads it, sees the tag was already announced and
// stays quiet — while a genuinely newer release is reported within minutes.
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
	delay := m.FirstDelay
	if delay <= 0 {
		delay = DefaultFirstDelay
	}
	go func() {
		// One prompt check after boot…
		first := time.NewTimer(delay)
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			m.tick(ctx)
		}
		// …then the regular cadence.
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
//
// 2026-08-05 v0.33.1.10: builds the Client with m.Owner
// and m.Repo (set at Monitor construction in cmd/skygate
// from Cfg.GitHubOwner / Cfg.GitHubRepo). The previous
// hardcoded value http.StatusNotFound'd because the operator's actual
// repo name differed from the assumed placeholder.
func (m *Monitor) tick(ctx context.Context) {
	c := &Client{HTTP: m.HTTP, Owner: m.Owner, Repo: m.Repo, BaseURL: m.APIBase}
	r, err := c.Latest(ctx)
	if err != nil {
		// B425: a rate-limited poll is NOT silence-worthy any more. Before this it was the
		// one error that produced no log line at all — and it is the error that delays a
		// notification by hours (the operator's measured case: an alert ~4h late while
		// /admin/update was spending the same 60/h anonymous quota). The message carries the
		// reset time when GitHub sent one, plus the way out.
		if errors.Is(err, ErrRateLimited) {
			log.Printf("release-monitor: GitHub rate limit hit — the next check is in ≤%s; "+
				"set SKYGATE_GITHUB_TOKEN to raise 60 requests/hour to 5000 (%v)", m.CheckEvery, err)
			return
		}
		log.Printf("release-monitor: poll failed: %v", err)
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
	// Dedup — in memory AND persisted (B425). The persisted tag is what lets the first
	// check run two minutes after boot instead of an hour: a restart must not re-announce a
	// release the operator has already been told about, and it must not go blind either.
	if m.notifiedTag() == r.TagName {
		return
	}
	m.mu.Lock()
	if m.Notified == nil { // B425: a Monitor built without Start (tests, one-off callers)
		m.Notified = make(map[string]bool)
	}
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
	// B426: a COMPOSED alert (i18n title + facts + a cleaned excerpt) when the sink supports
	// it; the legacy plain sentence stays as the fallback for sinks that only know SendAlert
	// (the Noop notifier and the existing tests). Before this the composed path did not exist
	// and the Markdown release body reached the phone verbatim — «## v1.5.171 — …».
	var id int64
	if s, ok := m.Notifier.(updateAlertSender); ok {
		id = s.SendAlertEvent(BuildUpdateAlert(m.Current, r, ""))
	} else {
		id = m.Notifier.SendAlert(FormatAlert(current, r))
	}
	id = m.recordAnnouncement(id, r.TagName)
	log.Printf("release-monitor: alert sent for %s (running %s), alert_id=%d", r.TagName, m.Current, id)
}

// notifiedTagKey is the global_settings row that survives a restart (B425).
const notifiedTagKey = "release.notified_tag"

// DefaultFirstDelay is how long after boot the monitor makes its FIRST check (B425). Long
// enough that a crash loop cannot hammer GitHub, short enough that an operator who has just
// restarted the container sees a pending release within minutes.
const DefaultFirstDelay = 2 * time.Minute

// notifiedTag returns the last tag this installation announced, or "".
func (m *Monitor) notifiedTag() string {
	if m.DB == nil {
		return ""
	}
	v, err := db.GetGlobalSetting(m.DB, notifiedTagKey, "")
	if err != nil {
		return ""
	}
	return v
}

// persistNotifiedTag records the tag. Best-effort: losing it costs one duplicate alert after
// a restart, not a lost one.
func (m *Monitor) persistNotifiedTag(tag string) {
	if m.DB == nil {
		return
	}
	if err := db.SetGlobalSetting(m.DB, notifiedTagKey, tag); err != nil {
		log.Printf("release-monitor: could not persist the announced tag (%v) — a restart may repeat it", err)
	}
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
	// B425: the persisted tag must go too, otherwise the next restart stays quiet about a
	// release the operator has already installed (and, after a DOWNGRADE, about an older one).
	m.persistNotifiedTag("")
}

// updateAlertSender is the composer entry point. A sink that implements it gets a readable,
// structured notification (B426); one that does not keeps the legacy plain sentence.
type updateAlertSender interface {
	SendAlertEvent(a telegram.Alert) int64
}

// recordAnnouncement persists the announced tag after a send attempt and returns the id
// unchanged. B425: persistence is what lets the first check run minutes after boot without
// re-announcing a release the operator already read.
func (m *Monitor) recordAnnouncement(id int64, tag string) int64 {
	m.persistNotifiedTag(tag)
	return id
}
