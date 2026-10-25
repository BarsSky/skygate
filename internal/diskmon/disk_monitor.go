// B391 (2026-10-25, TD-34) — THE DISK SIGNAL BELONGS TO THE PROCESS THAT CAN DELIVER IT.
//
// Measured before this file existed: the around-the-clock disk check was
// `scripts/monitor_disk.sh`, installed as a host cron (`/usr/local/bin/skygate-monitor-disk`).
// It dispatched Telegram only when `SKYGATE_TELEGRAM_BOT_TOKEN` + `_CHAT_ID` were in the
// CRON environment — and on the reference host they are not in `.env` at all (the token
// lives in the DB), so the cron wrote to a LOG and nobody was told. Even with the env
// set it could not have delivered: the host cannot reach `api.telegram.org` (curl → 000,
// relay names do not resolve), while the CONTAINER — which sees the very same filesystem
// (overlay reports the host's block counts) — has the relay tunnel the notifier already
// uses.
//
// So the check moves here, next to the notifier:
//
//	read      the filesystem holding the data directory (Bavail/Bsize, the number that
//	          predicts "the next backup fails"; the root reserve is excluded on purpose)
//	classify  ok / warn / critical against two thresholds (default 85 / 95, the values
//	          the retired cron used, so an operator's habits do not change)
//	alert     ONLY on a state transition, plus a bounded repeat while the level holds —
//	          a cron every 6h that re-sends the same sentence is how an operator learns
//	          to ignore alerts. The state is persisted in `global_settings` so a restart
//	          does not re-announce a level the operator already knows.
//
// The decision half is a pure function (classifyDisk / nextDiskAction) so the behaviour
// is testable without a real full disk, and a READ FAILURE is reported as a fact about
// the check — never as "disk ok" (TD-33's rule, applied here).
package diskmon

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// Disk levels. Strings because they are persisted.
const (
	DiskLevelOK       = "ok"
	DiskLevelWarn     = "warn"
	DiskLevelCritical = "critical"
)

// What to do on a tick, given the previous and the next level.
type diskAction int

const (
	diskActionQuiet   diskAction = iota // same level, still inside the repeat window
	diskActionAlert                     // announce the new level
	diskActionRecover                   // announce that the level came back to ok
)

// DiskMonitorConfig is everything the monitor needs. Zero values fall back to the
// defaults documented on the fields, so a caller that only knows the data path gets the
// behaviour the retired cron had.
type DiskMonitorConfig struct {
	// DB is the database the level and the last alert time are persisted in.
	DB *db.ResettableDB
	// Sender is the notifier. It is the concrete telegram.Notifier shape the alert
	// path needs (SendAlertEvent); a scheduler adapter that only knows the legacy
	// SendAlert is NOT enough — see the interface below.
	Sender DiskAlertSender
	// Path is the filesystem to measure. Default: "/".
	Path string
	// WarnPct / CritPct are the thresholds in percent. Defaults: 85 / 95.
	WarnPct int
	CritPct int
	// Interval between ticks. Default 10 minutes.
	Interval time.Duration
	// RepeatAfter is how long a level may hold before it is announced again.
	// Default 6 hours — the cadence the retired cron had.
	RepeatAfter time.Duration
	// Lang is the language of the alert. Empty = i18n's default.
	Lang string
	// Now is injectable for tests.
	Now func() time.Time
	// Usage is injectable for tests: percent used, bytes free, bytes total.
	Usage func(path string) (usedPct int, free, total uint64, err error)
}

// DiskAlertSender is the slice of the notifier this monitor uses. *telegram.RealNotifier
// satisfies it directly; keeping it narrow means the monitor does not depend on the
// whole bot (the test fake is three lines).
type DiskAlertSender interface {
	SendAlertEvent(a telegram.Alert) int64
}

// DiskMonitor is the periodic disk-space check.
type DiskMonitor struct {
	cfg DiskMonitorConfig
}

// NewDiskMonitor applies the defaults.
func NewDiskMonitor(cfg DiskMonitorConfig) *DiskMonitor {
	if cfg.Path == "" {
		cfg.Path = "/"
	}
	if cfg.WarnPct <= 0 {
		cfg.WarnPct = 85
	}
	if cfg.CritPct <= 0 {
		cfg.CritPct = 95
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Minute
	}
	if cfg.RepeatAfter <= 0 {
		cfg.RepeatAfter = 6 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Usage == nil {
		cfg.Usage = diskUsagePercent
	}
	return &DiskMonitor{cfg: cfg}
}

// Start runs the monitor until ctx is cancelled. The first tick happens immediately so a
// host that is ALREADY full reports at boot instead of ten minutes later.
func (m *DiskMonitor) Start(ctx context.Context) {
	if m == nil || m.cfg.DB == nil {
		return
	}
	log.Printf("💽 disk-monitor: started (path=%s, warn=%d%%, crit=%d%%, interval=%s, repeat=%s)",
		m.cfg.Path, m.cfg.WarnPct, m.cfg.CritPct, m.cfg.Interval, m.cfg.RepeatAfter)
	m.Tick(m.cfg.Now())
	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("💽 disk-monitor: stopped")
			return
		case <-t.C:
			m.Tick(m.cfg.Now())
		}
	}
}

// Tick performs one check. Exported so a test (and a future admin button) can run it
// without a scheduler.
func (m *DiskMonitor) Tick(now time.Time) {
	if m == nil || m.cfg.DB == nil {
		return
	}
	d := m.cfg.DB.DB
	pct, free, total, err := m.cfg.Usage(m.cfg.Path)
	if err != nil {
		// A read failure is a fact about the CHECK: report it and do not touch the
		// stored level, so a transient statfs error cannot be mistaken for "disk ok".
		log.Printf("💽 disk-monitor: %v", err)
		m.send(telegram.Alert{
			Severity: telegram.SeverityWarn,
			TitleKey: "alert.disk.title_warn",
			Facts: []telegram.AlertFact{
				{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_path"), Value: m.cfg.Path},
				{Label: "error", Value: err.Error()},
			},
			Details:  i18n.Tf(m.cfg.Lang, "alert.disk.read_failed", err),
			DedupKey: "disk-read-failed:" + m.cfg.Path,
			Lang:     m.cfg.Lang,
		})
		return
	}

	next := ClassifyDisk(pct, m.cfg.WarnPct, m.cfg.CritPct)
	prev, _ := db.GetGlobalSetting(d, diskLevelKey, DiskLevelOK)
	lastAlertUnix, _ := db.GetGlobalSetting(d, diskLastAlertKey, "0")
	lastAlert := time.Unix(parseInt64(lastAlertUnix), 0)

	action := nextDiskAction(prev, next, lastAlert, now, m.cfg.RepeatAfter)
	if action == diskActionQuiet {
		if next != prev {
			// Persist a level change we chose not to announce only when it is ok,
			// so the recovery can still be reported once.
			_ = db.SetGlobalSetting(d, diskLevelKey, next)
		}
		return
	}

	if action == diskActionRecover {
		m.send(telegram.Alert{
			Severity: telegram.SeverityRecovery,
			TitleKey: "alert.disk.title_recovered",
			Facts: []telegram.AlertFact{
				{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_path"), Value: m.cfg.Path},
				{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_usage"), Value: fmt.Sprintf("%d%%", pct)},
				{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_free"), Value: humanBytes(free)},
			},
			Lang: m.cfg.Lang,
		})
		_ = db.SetGlobalSetting(d, diskLevelKey, next)
		_ = db.SetGlobalSetting(d, diskLastAlertKey, strconv.FormatInt(now.Unix(), 10))
		return
	}

	titleKey := "alert.disk.title_warn"
	sev := telegram.SeverityWarn
	if next == DiskLevelCritical {
		titleKey = "alert.disk.title_critical"
		sev = telegram.SeverityCritical
	}
	details := i18n.T(m.cfg.Lang, "alert.disk.action")
	if prev == next {
		details = fmt.Sprintf("%s\n%s", details,
			i18n.Tf(m.cfg.Lang, "alert.disk.repeat", now.Sub(lastAlert).Round(time.Minute)))
	}
	m.send(telegram.Alert{
		Severity: sev,
		TitleKey: titleKey,
		Facts: []telegram.AlertFact{
			{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_path"), Value: m.cfg.Path},
			{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_usage"), Value: fmt.Sprintf("%d%%", pct)},
			{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_free"), Value: humanBytes(free)},
			{Label: i18n.T(m.cfg.Lang, "alert.disk.fact_thresholds"),
				Value: fmt.Sprintf("%d%% / %d%%", m.cfg.WarnPct, m.cfg.CritPct)},
		},
		Details:  details,
		DedupKey: "disk:" + next + ":" + m.cfg.Path,
		Lang:     m.cfg.Lang,
	})
	log.Printf("💽 disk-monitor: %s → %s (used=%d%%, free=%s, total=%s)",
		prev, next, pct, humanBytes(free), humanBytes(total))
	_ = db.SetGlobalSetting(d, diskLevelKey, next)
	_ = db.SetGlobalSetting(d, diskLastAlertKey, strconv.FormatInt(now.Unix(), 10))
}

// send is nil-safe: a deployment without a bot still logs.
func (m *DiskMonitor) send(a telegram.Alert) {
	if m.cfg.Sender == nil {
		return
	}
	if id := m.cfg.Sender.SendAlertEvent(a); id == 0 {
		log.Printf("💽 disk-monitor: alert not delivered (bot not configured or deduplicated)")
	}
}

const (
	diskLevelKey     = "monitor.disk.level"
	diskLastAlertKey = "monitor.disk.last_alert_at"
)

// ClassifyDisk is the pure threshold decision (exported for tests and for the admin
// page that may want to show the current level without waiting for a tick).
func ClassifyDisk(pct, warnPct, critPct int) string {
	switch {
	case pct >= critPct:
		return DiskLevelCritical
	case pct >= warnPct:
		return DiskLevelWarn
	default:
		return DiskLevelOK
	}
}

// nextDiskAction decides whether THIS tick says anything.
//
// The rules, in the operator's terms: a level CHANGE is always worth one message,
// coming back to ok is worth one message, and a level that simply HOLDS repeats at
// most once per repeatAfter. Everything else is quiet — the retired cron re-sent the
// same sentence every six hours regardless of change, which is the fast road to a muted
// chat.
func nextDiskAction(prev, next string, lastAlert, now time.Time, repeatAfter time.Duration) diskAction {
	if next == prev {
		if next == DiskLevelOK {
			return diskActionQuiet
		}
		if now.Sub(lastAlert) >= repeatAfter {
			return diskActionAlert
		}
		return diskActionQuiet
	}
	if next == DiskLevelOK {
		// Only a recovery FROM a degraded level is news.
		if prev == DiskLevelWarn || prev == DiskLevelCritical {
			return diskActionRecover
		}
		return diskActionQuiet
	}
	return diskActionAlert
}

// humanBytes renders a byte count the way an operator reads it.
func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// parseInt64 tolerates the empty/garbage value a fresh install has.
func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
