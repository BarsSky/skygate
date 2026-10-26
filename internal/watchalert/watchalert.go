// internal/watchalert — B423 (2026-10-25).
//
// THE TWO CHRONIC CONDITIONS THE 2026-10-25 INVESTIGATION FOUND, AND WHY THEY HAD NO ALERT.
//
// The reference install carried 13 310 `cluster.discovery.error` audit rows and 1802
// `login_fail` rows (plus 318 `password_change_fail`). Neither ever produced a Telegram
// message:
//
//   - the discovery ticker REPORTS a failure once an hour (B318/B354 throttle the log and
//     the audit row) but never alerts, so a tailnet that had been broken for weeks was
//     invisible unless somebody read the journal;
//   - a failed login is audited per attempt and never aggregated, so 1802 failures (a brute
//     force, a stale password in a script, or one forgetful operator) looked like nothing at
//     all from the outside.
//
// Both are "a persistent condition, not an event", and the product had no vocabulary for
// that: an hour-long outage and a single blip produced the same silence.
//
// WHAT THIS ADDS. Two small guards with PURE decision functions and per-process state:
//
//   - DiscoveryGuard: alert on the FIRST failure after a success, then at most once per
//     escalation window (default 6 h) while it keeps failing, and alert on RECOVERY. The
//     escalation window is deliberately separate from the hourly audit throttle: the journal
//     may record every hour, the operator's phone must not.
//   - LoginGuard: count failures in a sliding window (default 10 minutes) and alert once when
//     the count crosses the threshold, then at most once per cooldown (default 1 h). The alert
//     carries the count and the number of distinct source addresses — never a password, never
//     a username list.
//
// Everything is injected (clock, sender) so the policy is table-tested rather than timed.
package watchalert

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// Defaults. Chosen so that a persistent condition reaches a phone at most a handful of times
// a day, while its ONSET is immediate.
const (
	DefaultDiscoveryEscalation = 6 * time.Hour
	DefaultLoginWindow         = 10 * time.Minute
	DefaultLoginThreshold      = 10
	DefaultLoginCooldown       = time.Hour
)

// Sender is the alert entry point (composer + ledger), not a raw reply.
type Sender interface {
	SendAlertEvent(a telegram.Alert) int64
}

// Config is everything the guards need, injectable for tests.
type Config struct {
	Sender            Sender
	Lang              string
	Now               func() time.Time
	DiscoveryInterval time.Duration
	LoginWindow       time.Duration
	LoginThreshold    int
	LoginCooldown     time.Duration
}

// ------ discovery ---------------------------------------------------------

// DiscoveryGuard is the escalation policy for the discovery ticker.
type DiscoveryGuard struct {
	mu        sync.Mutex
	cfg       Config
	failing   bool
	lastAlert time.Time
	since     time.Time
}

// discoveryAlertDue is the whole policy as a pure function.
//
//	first failure after a success        → alert (the ONSET is news)
//	still failing, window not elapsed    → silence (the journal keeps the hourly record)
//	still failing, window elapsed        → alert again (it is not going away)
func discoveryAlertDue(now, lastAlert time.Time, failingBefore bool, interval time.Duration) (bool, string) {
	if !failingBefore {
		return true, "first failure after a success"
	}
	if interval <= 0 {
		return false, "escalation disabled"
	}
	if lastAlert.IsZero() || now.Sub(lastAlert) >= interval {
		return true, fmt.Sprintf("still failing after %s", interval)
	}
	return false, "within the escalation window"
}

// DiscoveryFailure records one failed tick and alerts when the policy says so.
func DiscoveryFailure(msg string) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	cfg := guard.cfg
	if cfg.Sender == nil {
		return
	}
	now := cfg.Now()
	due, why := discoveryAlertDue(now, guard.lastAlert, guard.failing, cfg.DiscoveryInterval)
	if !guard.failing {
		guard.since = now
	}
	guard.failing = true
	if !due {
		return
	}
	guard.lastAlert = now
	lang := langOf(cfg)
	guard.cfg.Sender.SendAlertEvent(telegram.Alert{
		Severity: telegram.SeverityWarn,
		TitleKey: "alert.discovery.title",
		Facts: []telegram.AlertFact{
			{Label: i18n.T(lang, "alert.discovery.fact_error"), Value: truncate(msg, 300)},
			{Label: i18n.T(lang, "alert.discovery.fact_since"), Value: guard.since.UTC().Format(time.RFC3339)},
			{Label: i18n.T(lang, "alert.discovery.fact_why"), Value: why},
		},
		DedupKey: "watchalert.discovery",
		DedupTTL: cfg.DiscoveryInterval,
		Lang:     lang,
	})
}

// DiscoverySuccess records a healthy tick; if the previous state was failing, the operator is
// told that it recovered (a fault that silently heals teaches nobody anything).
func DiscoverySuccess() {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	cfg := guard.cfg
	if !guard.failing {
		return
	}
	was := guard.since
	guard.failing = false
	guard.since = time.Time{}
	guard.lastAlert = time.Time{}
	if cfg.Sender == nil {
		return
	}
	lang := langOf(cfg)
	cfg.Sender.SendAlertEvent(telegram.Alert{
		Severity: telegram.SeverityRecovery,
		TitleKey: "alert.discovery.recovered",
		Facts: []telegram.AlertFact{
			{Label: i18n.T(lang, "alert.discovery.fact_since"), Value: was.UTC().Format(time.RFC3339)},
		},
		Lang: lang,
	})
}

// ------ login failures ----------------------------------------------------

// LoginGuard aggregates failed logins into ONE alert per cooldown. It holds only the data:
// the mutex and the config live on the discovery guard, because both detectors are
// configured once at boot and must not race with each other.
type LoginGuard struct {
	hits      []time.Time
	ips       map[string]struct{}
	lastAlert time.Time
}

// loginBurstDue is the pure decision: threshold crossed, and the cooldown has elapsed.
func loginBurstDue(count, threshold int, now, lastAlert time.Time, cooldown time.Duration) bool {
	if threshold <= 0 || count < threshold {
		return false
	}
	if lastAlert.IsZero() {
		return true
	}
	return now.Sub(lastAlert) >= cooldown
}

// LoginFailure records one failed attempt. It is called from the login handler on the same
// path that writes the audit row, so no attempt can be counted twice or missed.
func LoginFailure(remoteAddr string) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	cfg := guard.cfg
	if cfg.Sender == nil {
		return
	}
	now := cfg.Now()
	lg := loginGuard
	lg.hits = append(lg.hits, now)
	// Slide the window.
	cut := now.Add(-cfg.LoginWindow)
	i := 0
	for i < len(lg.hits) && lg.hits[i].Before(cut) {
		i++
	}
	if i > 0 {
		lg.hits = lg.hits[i:]
	}
	if lg.ips == nil {
		lg.ips = map[string]struct{}{}
	}
	lg.ips[hostOnly(remoteAddr)] = struct{}{}

	if !loginBurstDue(len(lg.hits), cfg.LoginThreshold, now, lg.lastAlert, cfg.LoginCooldown) {
		return
	}
	lg.lastAlert = now
	lang := langOf(cfg)
	ips := make([]string, 0, len(lg.ips))
	for ip := range lg.ips {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	cfg.Sender.SendAlertEvent(telegram.Alert{
		Severity: telegram.SeverityWarn,
		TitleKey: "alert.login_burst.title",
		Facts: []telegram.AlertFact{
			{Label: i18n.T(lang, "alert.login_burst.fact_count"), Value: fmt.Sprintf("%d", len(lg.hits))},
			{Label: i18n.T(lang, "alert.login_burst.fact_window"), Value: cfg.LoginWindow.String()},
			{Label: i18n.T(lang, "alert.login_burst.fact_ips"), Value: truncate(strings.Join(ips, ", "), 300)},
		},
		DedupKey: "watchalert.login_burst",
		DedupTTL: cfg.LoginCooldown,
		Lang:     lang,
	})
	// A burst that has been reported starts a fresh count, so the next alert describes the
	// next burst rather than repeating the same one.
	lg.hits = nil
	lg.ips = nil
}

// ------ wiring ------------------------------------------------------------

// guard is the process-wide state (the login handler and the discovery ticker have no Service
// in common; both are single per process).
var (
	guardMu    sync.Mutex
	guard      = &DiscoveryGuard{cfg: DefaultConfig()}
	loginGuard = &LoginGuard{}
)

// DefaultConfig is the production policy with the default intervals.
func DefaultConfig() Config {
	return Config{
		Now:               time.Now,
		DiscoveryInterval: DefaultDiscoveryEscalation,
		LoginWindow:       DefaultLoginWindow,
		LoginThreshold:    DefaultLoginThreshold,
		LoginCooldown:     DefaultLoginCooldown,
	}
}

// Configure installs the sender and the policy. Called once from the boot path; a zero
// interval means the default, and a nil Now means time.Now.
func Configure(cfg Config) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DiscoveryInterval == 0 {
		cfg.DiscoveryInterval = DefaultDiscoveryEscalation
	}
	if cfg.LoginWindow == 0 {
		cfg.LoginWindow = DefaultLoginWindow
	}
	if cfg.LoginThreshold == 0 {
		cfg.LoginThreshold = DefaultLoginThreshold
	}
	if cfg.LoginCooldown == 0 {
		cfg.LoginCooldown = DefaultLoginCooldown
	}
	guardMu.Lock()
	guard = &DiscoveryGuard{cfg: cfg}
	loginGuard = &LoginGuard{}
	guardMu.Unlock()
}

// ResetForTest clears both guards. Tests only.
func ResetForTest() { Configure(Config{}) }

func langOf(cfg Config) string {
	if cfg.Lang != "" {
		return cfg.Lang
	}
	return i18n.LangRU
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// hostOnly drops the port from net.RemoteAddr ("1.2.3.4:5678" → "1.2.3.4"), because the port
// changes on every attempt and would turn ten failures into ten distinct "sources".
func hostOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 && !strings.Contains(addr[i:], "]") {
		return addr[:i]
	}
	return addr
}
