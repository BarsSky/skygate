// B391 (2026-10-25, TD-34) — the disk check that now lives in the app.
//
// These tests pin the DECISION half (thresholds, transitions, repeats) and the
// PERSISTENCE half (a restart must not re-announce a level the operator already knows),
// because those are where a disk monitor either helps or becomes noise:
//
//	ok → warn        one message
//	warn → warn      quiet until the repeat window expires
//	warn → critical  one message
//	critical → ok    one RECOVERY message
//	read error       a message about the CHECK, and the stored level is untouched
package diskmon

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

type fakeSender struct {
	alerts []telegram.Alert
}

func (f *fakeSender) SendAlertEvent(a telegram.Alert) int64 {
	f.alerts = append(f.alerts, a)
	return int64(len(f.alerts))
}

func b391DB(t *testing.T) *db.ResettableDB {
	t.Helper()
	_, d, err := db.OpenWithDialect("sqlite:" + t.TempDir() + "/disk.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	return db.NewResettableDB(d)
}

// fixedUsage returns a Usage function reporting one percentage.
func fixedUsage(pct int, free, total uint64) func(string) (int, uint64, uint64, error) {
	return func(string) (int, uint64, uint64, error) { return pct, free, total, nil }
}

func newTestMonitor(t *testing.T, d *db.ResettableDB, sender *fakeSender, usage func(string) (int, uint64, uint64, error), now func() time.Time) *DiskMonitor {
	t.Helper()
	return NewDiskMonitor(DiskMonitorConfig{
		DB:          d,
		Sender:      sender,
		Path:        "/",
		WarnPct:     85,
		CritPct:     95,
		RepeatAfter: 6 * time.Hour,
		Now:         now,
		Usage:       usage,
		Lang:        "en",
	})
}

func TestB391_ClassifyDisk(t *testing.T) {
	cases := []struct {
		pct  int
		want string
	}{
		{0, DiskLevelOK}, {84, DiskLevelOK}, {85, DiskLevelWarn},
		{94, DiskLevelWarn}, {95, DiskLevelCritical}, {100, DiskLevelCritical},
	}
	for _, c := range cases {
		if got := ClassifyDisk(c.pct, 85, 95); got != c.want {
			t.Errorf("B391: ClassifyDisk(%d) = %q, want %q", c.pct, got, c.want)
		}
	}
}

func TestB391_NextDiskAction(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		prev, now string
		last      time.Time
		want      diskAction
	}{
		{"ok stays ok", DiskLevelOK, DiskLevelOK, time.Time{}, diskActionQuiet},
		{"ok to warn", DiskLevelOK, DiskLevelWarn, time.Time{}, diskActionAlert},
		{"warn to critical", DiskLevelWarn, DiskLevelCritical, now.Add(-time.Minute), diskActionAlert},
		{"critical holds inside the window", DiskLevelCritical, DiskLevelCritical, now.Add(-time.Hour), diskActionQuiet},
		{"critical holds past the window", DiskLevelCritical, DiskLevelCritical, now.Add(-7 * time.Hour), diskActionAlert},
		{"critical recovered", DiskLevelCritical, DiskLevelOK, now.Add(-time.Hour), diskActionRecover},
		{"warn recovered", DiskLevelWarn, DiskLevelOK, time.Time{}, diskActionRecover},
		{"first observation of ok", "", DiskLevelOK, time.Time{}, diskActionQuiet},
	}
	for _, c := range cases {
		if got := nextDiskAction(c.prev, c.now, c.last, now, 6*time.Hour); got != c.want {
			t.Errorf("B391 %q: nextDiskAction(%q→%q) = %v, want %v", c.name, c.prev, c.now, got, c.want)
		}
	}
}

func TestB391_TickAnnouncesTransitionsOnlyOnce(t *testing.T) {
	d := b391DB(t)
	sender := &fakeSender{}
	clock := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	m := newTestMonitor(t, d, sender, fixedUsage(50, 100<<30, 200<<30), now)

	m.Tick(clock) // ok
	if len(sender.alerts) != 0 {
		t.Fatalf("B391: a healthy disk produced %d alert(s) — that is the cron noise this block removes", len(sender.alerts))
	}

	m = newTestMonitor(t, d, sender, fixedUsage(88, 24<<30, 200<<30), now)
	m.Tick(clock)
	if len(sender.alerts) != 1 {
		t.Fatalf("B391: ok→warn produced %d alert(s), want 1", len(sender.alerts))
	}
	if sender.alerts[0].Severity != telegram.SeverityWarn {
		t.Errorf("B391: the warn alert has severity %q", sender.alerts[0].Severity)
	}

	// Same level, inside the repeat window: silent.
	clock = clock.Add(30 * time.Minute)
	m.Tick(clock)
	if len(sender.alerts) != 1 {
		t.Errorf("B391: a repeat inside the window was sent (%d alerts)", len(sender.alerts))
	}

	// Escalation is news even inside the window.
	m = newTestMonitor(t, d, sender, fixedUsage(97, 6<<30, 200<<30), now)
	m.Tick(clock)
	if len(sender.alerts) != 2 {
		t.Fatalf("B391: warn→critical produced %d total alerts, want 2", len(sender.alerts))
	}
	if sender.alerts[1].Severity != telegram.SeverityCritical {
		t.Errorf("B391: the critical alert has severity %q", sender.alerts[1].Severity)
	}

	// Recovery is news.
	m = newTestMonitor(t, d, sender, fixedUsage(40, 120<<30, 200<<30), now)
	m.Tick(clock)
	if len(sender.alerts) != 3 {
		t.Fatalf("B391: critical→ok produced %d total alerts, want 3", len(sender.alerts))
	}
	if sender.alerts[2].Severity != telegram.SeverityRecovery {
		t.Errorf("B391: the recovery alert has severity %q", sender.alerts[2].Severity)
	}
}

func TestB391_TickSurvivesARestartWithoutRepeating(t *testing.T) {
	d := b391DB(t)
	sender := &fakeSender{}
	clock := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	first := newTestMonitor(t, d, sender, fixedUsage(90, 20<<30, 200<<30), func() time.Time { return clock })
	first.Tick(clock)
	if len(sender.alerts) != 1 {
		t.Fatalf("B391: the first warn produced %d alerts", len(sender.alerts))
	}

	// A RESTART: a brand-new monitor, same database, same level, one minute later.
	restarted := newTestMonitor(t, d, sender, fixedUsage(91, 18<<30, 200<<30), func() time.Time { return clock.Add(time.Minute) })
	restarted.Tick(clock.Add(time.Minute))
	if len(sender.alerts) != 1 {
		t.Errorf("B391: a restart re-announced a level the operator already knew (%d alerts)", len(sender.alerts))
	}
}

func TestB391_ReadFailureIsAboutTheCheckNotTheDisk(t *testing.T) {
	d := b391DB(t)
	sender := &fakeSender{}
	clock := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	usage := func(string) (int, uint64, uint64, error) {
		return 0, 0, 0, errors.New("statfs /var/lib/skygate: permission denied")
	}
	m := newTestMonitor(t, d, sender, usage, func() time.Time { return clock })
	m.Tick(clock)

	if len(sender.alerts) != 1 {
		t.Fatalf("B391: a read failure produced %d alert(s), want 1", len(sender.alerts))
	}
	// The stored level must NOT be rewritten: the disk was never measured.
	var level string
	if err := d.DB.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, diskLevelKey).Scan(&level); err != nil {
		if err != sql.ErrNoRows {
			t.Fatalf("read level: %v", err)
		}
	}
	if level != "" && level != DiskLevelOK {
		t.Errorf("B391: a read failure changed the stored level to %q", level)
	}
	// And the alert must say it is about the READ, not about the disk usage.
	if !strings.Contains(strings.ToLower(sender.alerts[0].Details), "statfs") &&
		!strings.Contains(strings.ToLower(sender.alerts[0].Details), "permission") {
		t.Errorf("B391: the alert does not carry the read error: %+v", sender.alerts[0])
	}
}

func TestB391_HumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:             "512 B",
		1536:            "1.5 KiB",
		20 << 30:        "20.0 GiB",
		uint64(1) << 40: "1.0 TiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("B391: humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
