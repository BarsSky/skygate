// 2026-07-20: v0.20.0 — headscale version monitor runner.
//
// Companion to client.go. Holds the runtime state
// (pinned version, dedup map, notifier sink) and
// launches the periodic poll goroutine.
//
// The model mirrors internal/release/monitor_runner.go
// but is independent (no shared base struct) so the
// two monitors can evolve separately. Differences vs
// the skygate monitor:
//
//  1. The "current" version is a runtime env var
//     (SKYGATE_HEADSCALE_VERSION_PIN), NOT a
//     build-time -ldflags value. The operator may
//     change it without rebuilding skygate.
//
//  2. We write every seen release to the
//     headscale_releases DB table (one row per
//     unique tag) so /admin/headscale has a history
//     view. The skygate monitor is log-only.
//
//  3. The alert prefix is "⚠️" for breaking changes
//     (major/minor bump) vs "🔔" for patch
//     releases — same as FormatAlert, which the
//     alert text-builder uses.
//
//  4. The dedup map is keyed by tag AND by a
//     "pinned-version" stamp. When the operator
//     changes SKYGATE_HEADSCALE_VERSION_PIN (e.g.
//     after an upgrade) the dedup map is reset so
//     a fresh comparison is made on the next tick.

package headscale_version

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
)

// NotifierSink is the subset of the telegram.Notifier
// interface the monitor needs. Defined as an interface
// so we can wire a no-op in tests.
type NotifierSink interface {
	SendAlert(text string) int64
}

// HeadscaleReleaseRecord is the row shape we write to
// the headscale_releases table. Mirrors db.SaveHeadscaleRelease
// in internal/db/headscale_releases.go (kept duplicated
// here to avoid an import cycle — db can't import
// headscale_version, and headscale_version doesn't need
// to know the table's full schema).
type HeadscaleReleaseRecord struct {
	Version     string
	PublishedAt time.Time
	FirstSeenAt time.Time
	HTMLURL     string
	Name        string
	Body        string
	IsBreaking  bool
	Notified    bool
}

// Monitor holds the runtime state of the headscale
// version-monitor goroutine. One instance per process.
type Monitor struct {
	DB         *sql.DB         // for headscale_releases table writes
	HTTPClient *http.Client    // optional override; nil = use NewClient
	Pinned     string          // operator's running version ("0.29.2")
	Notified   map[string]bool // dedup: tags we've already sent an alert for
	Notifier   NotifierSink    // alert sink
	CheckEvery time.Duration   // default 24h

	// --- B297: what version is the RUNNING headscale? ----------------------
	//
	// Pinned is a DECLARATION: it is whatever the operator typed into
	// SKYGATE_HEADSCALE_VERSION_PIN, and config.go says outright that it is not
	// auto-detected. Live setup: the native host `aro` ran headscale 0.29.0 while
	// the agent VM ran 0.29.3 — a stale pin makes "a newer headscale is
	// available" wrong in whichever direction the pin is wrong, and 0.29.x is not
	// uniform in the API surface skygate depends on (approve_routes left REST at
	// 0.29.1, the expire REST path broke at 0.29.2).
	//
	// VersionProbe, when non-nil, returns the version the DAEMON answered (plus
	// the rung that answered). A detection beats the declaration everywhere the
	// comparison or the page needs a version, and the declaration is kept next to
	// it so a mismatch is visible instead of silent.
	VersionProbe func(ctx context.Context) (version, via string, err error)
	// DeclaredPin is the operator's declaration as written, kept for display even
	// after a detection supersedes it. Empty means "same as Pinned".
	DeclaredPin string

	mu                sync.Mutex
	detected          string    // last version the daemon answered
	detectedVia       string    // which rung answered it
	detectedAt        time.Time // when the last SUCCESSFUL probe ran
	detectedErr       string    // last probe failure, "" after a success
	Latest            Release   // most recent release seen
	UpdateAvailable   bool      // Latest > Pinned (semver)
	BreakingAvailable bool      // UpdateAvailable AND major/minor bump
	CheckedAt         time.Time
	History           []HeadscaleReleaseRecord // last N seen, newest first
}

// ServerVersionStatus is what /admin/headscale shows about the running daemon:
// the version detected, the version declared, which rung answered, when, and the
// last failure (if any). Every field is populated, so the page never has to
// guess whether an empty string means "unknown" or "same as the other one".
type ServerVersionStatus struct {
	Declared   string    // SKYGATE_HEADSCALE_VERSION_PIN as written
	Detected   string    // version the daemon answered ("" = never detected)
	Via        string    // which rung answered
	DetectedAt time.Time // when detection last succeeded
	Err        string    // last probe failure
	Effective  string    // what the comparison uses: Detected, else Declared
	Mismatch   bool      // both known and they differ (semver-aware)
}

// VersionStatus returns the version picture for the page. Safe to call before
// Start (the boot path probes once, so the first render is already truthful).
func (m *Monitor) VersionStatus() ServerVersionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	declared := m.DeclaredPin
	if declared == "" {
		declared = m.Pinned
	}
	st := ServerVersionStatus{
		Declared:   declared,
		Detected:   m.detected,
		Via:        m.detectedVia,
		DetectedAt: m.detectedAt,
		Err:        m.detectedErr,
	}
	st.Effective = m.detected
	if st.Effective == "" {
		st.Effective = declared
	}
	if m.detected != "" && declared != "" {
		// CompareSemver, not string equality: "0.29" and "0.29.0" are the same
		// version and must not raise a false mismatch banner.
		st.Mismatch = CompareSemver(m.detected, declared) != 0
	}
	return st
}

// ProbeVersion asks the running headscale for its version and records the
// outcome. Called once at Start (via the goroutine) and from every tick, so a
// headscale upgrade is picked up without restarting skygate.
//
// A FAILED probe keeps the last version we did read: the declaration is exactly
// what we are trying not to trust, so a transient API hiccup must not silently
// put it back in charge. The failure is recorded next to the last success.
func (m *Monitor) ProbeVersion(ctx context.Context) {
	if m == nil || m.VersionProbe == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	version, via, err := m.VersionProbe(pctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.detectedErr = err.Error()
		return
	}
	if strings.TrimSpace(version) == "" {
		m.detectedErr = "the probe answered no version"
		return
	}
	m.detected = strings.TrimSpace(version)
	m.detectedVia = via
	m.detectedAt = time.Now()
	m.detectedErr = ""
}

// effectivePinned is the version the comparison and the page should use: the
// DETECTED one when a probe has ever succeeded, the declaration otherwise.
func (m *Monitor) effectivePinned() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.effectivePinnedLocked()
}

// effectivePinnedLocked is effectivePinned for callers that already hold mu.
func (m *Monitor) effectivePinnedLocked() string {
	if m.detected != "" {
		return m.detected
	}
	return m.Pinned
}

// Snapshot returns a copy of the monitor's state for
// the /admin/headscale page render. The History slice
// is shallow — callers must not mutate it.
//
// B297: the returned `pinned` is the EFFECTIVE version — the one the running
// daemon answered when a probe has succeeded, and the operator's declaration
// only until then. Callers that need to show the declaration next to it use
// VersionStatus().
func (m *Monitor) Snapshot() (latest Release, update, breaking bool, checkedAt time.Time, history []HeadscaleReleaseRecord, pinned string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hist := make([]HeadscaleReleaseRecord, len(m.History))
	copy(hist, m.History)
	return m.Latest, m.UpdateAvailable, m.BreakingAvailable, m.CheckedAt, hist, m.effectivePinnedLocked()
}

// NewMonitor is a convenience constructor that
// pre-populates sensible defaults (1d poll interval,
// empty dedup map). Callers should still set DB,
// Pinned, and Notifier before Start().
func NewMonitor(db *sql.DB, pinned string, notifier NotifierSink) *Monitor {
	return &Monitor{
		DB:         db,
		Pinned:     pinned,
		Notifier:   notifier,
		Notified:   make(map[string]bool),
		CheckEvery: 24 * time.Hour,
	}
}

// Start launches the background loop. Returns
// immediately; cancel ctx to stop.
//
// The first tick fires after one CheckEvery interval
// (not on boot) — same convention as the skygate
// monitor, so a restart doesn't spam the admin with
// "what's available now" alerts. To force an early
// check, call CheckNow() (used by the "Run check
// now" button on /admin/headscale).
func (m *Monitor) Start(ctx context.Context) {
	if m.HTTPClient == nil {
		m.HTTPClient = NewClient().HTTP
	}
	if m.Notified == nil {
		m.Notified = make(map[string]bool)
	}
	if m.CheckEvery == 0 {
		m.CheckEvery = 24 * time.Hour
	}
	go func() {
		// B297: ask the running headscale for its version once, immediately, so
		// the first /admin/headscale render is based on what the daemon says
		// rather than on what the operator declared months ago. The call is
		// bounded inside ProbeVersion (8s) and a no-op when no probe is wired.
		m.ProbeVersion(ctx)
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

// CheckNow runs one tick synchronously. Returns the
// error from the HTTP fetch (or ErrRateLimited /
// ErrNoReleases). Used by the admin "Run check now"
// button so the operator can refresh the page state
// without waiting for the next interval.
func (m *Monitor) CheckNow(ctx context.Context) error {
	m.tick(ctx)
	return nil
}

// tick is one pass. Public for testability (tests
// inject a mock HTTP server and a no-op
// NotifierSink, then call tick directly).
func (m *Monitor) tick(ctx context.Context) {
	// B297: refresh what the RUNNING headscale is before comparing anything.
	// Deliberately first: a GitHub poll that fails (rate limit, offline host)
	// must not also skip the version refresh, and every comparison below wants
	// the detected version rather than the declaration.
	m.ProbeVersion(ctx)

	c := &Client{HTTP: m.HTTPClient}
	r, err := c.Latest(ctx)
	if err != nil {
		// Rate-limited / no releases: stay quiet,
		// retry next tick. Other errors: log so the
		// operator can see it in the container log
		// but don't spam the Telegram channel.
		if err != ErrRateLimited && err != ErrNoReleases {
			log.Printf("headscale-monitor: poll failed: %v", err)
		}
		return
	}

	// Persist to DB (best-effort). A write failure
	// doesn't stop the alert path — the operator
	// still gets the Telegram notification.
	//
	// B297: `pinned` is the detected version when a probe has succeeded, so the
	// is_breaking flag stored for a release describes what this host actually
	// runs. (Previously a stale declaration could mark a patch release as
	// breaking, or a breaking one as a patch.)
	pinned := m.effectivePinned()
	publishedAt, _ := time.Parse(time.RFC3339, r.PublishedAt)
	if m.DB != nil {
		breaking := IsBreaking(pinned, r.TagName)
		rec := HeadscaleReleaseRecord{
			Version:     r.TagName,
			PublishedAt: publishedAt,
			FirstSeenAt: time.Now(),
			HTMLURL:     r.HTMLURL,
			Name:        r.Name,
			Body:        r.Body,
			IsBreaking:  breaking,
		}
		if err := saveHeadscaleRelease(m.DB, rec); err != nil {
			log.Printf("headscale-monitor: save release: %v", err)
		}
	}

	// Update the snapshot regardless of whether the
	// new release is "interesting". The /admin/headscale
	// page wants to know "is there ANY newer release
	// out there?" so the operator can decide when to
	// upgrade.
	breaking := IsBreaking(pinned, r.TagName)
	updateAvailable := CompareSemver(r.TagName, pinned) > 0
	m.mu.Lock()
	m.Latest = *r
	m.CheckedAt = time.Now()
	m.UpdateAvailable = updateAvailable
	m.BreakingAvailable = updateAvailable && breaking
	// Rebuild the History slice from the DB. Cap at
	// 20 entries so the in-memory copy doesn't grow
	// without bound (a long-running deployment sees
	// ~one new headscale release per 6 weeks, so
	// 20 ≈ 2.3 years of history).
	if m.DB != nil {
		if hist, err := listHeadscaleReleases(m.DB, 20); err == nil {
			m.History = hist
		}
	}
	m.mu.Unlock()

	// Not new — nothing to do.
	if !updateAvailable {
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
	// B297: an alert needs a version to compare against, and a DETECTED version
	// is enough — the declaration only ever existed because skygate could not ask
	// the daemon. A host with a correct detection and no pin used to be stuck in
	// "observe only" forever.
	effective := m.effectivePinned()
	if m.Notifier == nil || effective == "" {
		return
	}
	pinnedRel := &Release{TagName: effective}
	alert := FormatAlert(pinnedRel, r, breaking)
	id := m.Notifier.SendAlert(alert)
	log.Printf("headscale-monitor: alert sent for %s (running %s, breaking=%v), alert_id=%d",
		r.TagName, effective, breaking, id)
}

// ResetNotified wipes the dedup map. Call after the
// operator upgrades (the new pinned version is now
// "running" — the old alerts don't apply). Also
// clears UpdateAvailable + BreakingAvailable so the
// banner on /admin/exit-nodes disappears immediately.
//
// Not wired to /admin/headscale as a button in v0.20.0
// — the operator just changes SKYGATE_HEADSCALE_VERSION_PIN
// and restarts skygate, which is rare enough that a
// cron-style auto-reset isn't worth the surprise.
// Future: detect "current /api/v1/node count went up
// + admin clicked 'I upgraded'" and auto-reset.
func (m *Monitor) ResetNotified() {
	m.mu.Lock()
	m.Notified = make(map[string]bool)
	m.UpdateAvailable = false
	m.BreakingAvailable = false
	m.mu.Unlock()
}

// saveHeadscaleRelease writes one row to
// headscale_releases (idempotent — INSERT OR IGNORE
// on PRIMARY KEY = version). Best-effort: a write
// failure is logged, not fatal.
func saveHeadscaleRelease(d *sql.DB, rec HeadscaleReleaseRecord) error {
	_, err := d.Exec(`
		`+db.InsertIgnorePrefix()+` INTO headscale_releases
			(version, published_at, first_seen_at, html_url, name, body, is_breaking, notified)
		VALUES (`+db.PlaceholdersList(8)+`)`+db.OnConflictDoNothing("version")+`
	`, rec.Version, rec.PublishedAt.Unix(), rec.FirstSeenAt.Unix(),
		rec.HTMLURL, rec.Name, rec.Body, boolToInt(rec.IsBreaking), boolToInt(rec.Notified))
	return err
}

// listHeadscaleReleases returns the N most recent
// headscale_releases rows, newest first (by
// published_at).
func listHeadscaleReleases(d *sql.DB, limit int) ([]HeadscaleReleaseRecord, error) {
	// 2026-09-18: `LIMIT ?` was a SQLite-era placeholder. pgx does not
	// translate `?`, so on PostgreSQL this was a syntax error
	// (SQLSTATE 42601) that the caller swallowed
	// (monitor.go:211 — `if hist, err := ...; err == nil`), leaving the
	// release history permanently empty with no visible symptom.
	// db.PlaceholdersList emits the $N form that both drivers accept.
	rows, err := d.Query(`
		SELECT version, published_at, first_seen_at, html_url, name, body, is_breaking, notified
		FROM headscale_releases
		ORDER BY published_at DESC, first_seen_at DESC
		LIMIT `+db.PlaceholdersList(1)+`
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeadscaleReleaseRecord
	for rows.Next() {
		var rec HeadscaleReleaseRecord
		var pubUnix, seenUnix, breaking, notified int
		if err := rows.Scan(&rec.Version, &pubUnix, &seenUnix, &rec.HTMLURL, &rec.Name, &rec.Body, &breaking, &notified); err != nil {
			return nil, err
		}
		if pubUnix > 0 {
			rec.PublishedAt = time.Unix(int64(pubUnix), 0)
		}
		if seenUnix > 0 {
			rec.FirstSeenAt = time.Unix(int64(seenUnix), 0)
		}
		rec.IsBreaking = breaking != 0
		rec.Notified = notified != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
