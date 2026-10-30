// backup_stale_cause_b446_test.go — B446. The diagnostic must name the CAUSE, and every branch of
// it must be distinguishable from the others, because each one has a different remedy.
package admin

import (
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
)

// b446Primed returns a test DB with the backup settings overwritten (empty = delete the key).
func b446Primed(t *testing.T, kv map[string]string) *dbDBSource {
	t.Helper()
	src := b282SQLiteDB(t)
	for k, v := range kv {
		if v == "" {
			if _, err := src.db.Exec(`DELETE FROM global_settings WHERE key = ?`, k); err != nil {
				t.Fatalf("delete %s: %v", k, err)
			}
			continue
		}
		if err := db.SetGlobalSetting(src.db, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	SetTestService(&Service{DB: src})
	t.Cleanup(func() { SetTestService(nil) })
	return src
}

// TestB446_DisabledIsNotADestinationProblem: the live install had `enabled=1`, but a DISABLED
// install is the first thing the diagnostic must name — not S3.
func TestB446_DisabledIsNotADestinationProblem(t *testing.T) {
	b446Primed(t, map[string]string{"backup.enabled": "0"})
	cause := backupStaleCause(getTestService(), time.Now().Add(-9*24*time.Hour).Unix())
	if cause == "" {
		t.Fatal("a disabled backup produced no explanation")
	}
	if !strings.Contains(cause, "DISABLED") {
		t.Errorf("cause = %q, want it to say backups are disabled", cause)
	}
	if strings.Contains(cause, "destination") {
		t.Errorf("cause = %q — a disabled backup must not blame the destination", cause)
	}
}

// TestB446_NothingSchedulesIt is the second branch: in-app OFF, and system cron is invisible to
// the panel, so it may not be credited (the pre-B446 text assumed it).
func TestB446_NothingSchedulesIt(t *testing.T) {
	b446Primed(t, map[string]string{
		"backup.enabled":        "1",
		"backup.in_app_enabled": "0",
		"backup.schedule":       "0 4 * * *",
		"backup.last_status":    "ok",
		"backup.last_run":       "1792555205",
	})
	cause := backupStaleCause(getTestService(), 1792555205)
	if !strings.Contains(cause, "in-app scheduler is OFF") {
		t.Errorf("cause = %q, want it to name the disabled in-app scheduler", cause)
	}
	if !strings.Contains(cause, "cron") {
		t.Errorf("cause = %q, want it to name the only other door (system cron)", cause)
	}
}

// TestB446_MissedSlotIsNamedAndWillBeCaughtUp is the live case: everything enabled, the schedule
// set, and the last run nine days old — the catch-up this block adds means the next tick fires.
func TestB446_MissedSlotIsNamedAndWillBeCaughtUp(t *testing.T) {
	b446Primed(t, map[string]string{
		"backup.enabled":        "1",
		"backup.in_app_enabled": "1",
		"backup.schedule":       "0 4 * * *",
		"backup.last_status":    "ok",
	})
	old := time.Now().Add(-9 * 24 * time.Hour).Unix()
	cause := backupStaleCause(getTestService(), old)
	if !strings.Contains(cause, "catch up") {
		t.Errorf("cause = %q, want it to say the scheduler will catch the slot up", cause)
	}
	if strings.Contains(cause, "destination") {
		t.Errorf("cause = %q — with status=ok the destination was never in doubt", cause)
	}
}

// TestB446_FailedRunNamesItsOwnError: only a FAILED run makes the destination the suspect, and
// then the stored error is the answer instead of a guess.
func TestB446_FailedRunNamesItsOwnError(t *testing.T) {
	b446Primed(t, map[string]string{
		"backup.enabled":        "1",
		"backup.in_app_enabled": "1",
		"backup.schedule":       "0 4 * * *",
		"backup.last_status":    "fail",
		"backup.last_error":     "s3: 403 Forbidden from minio.skynas.ru",
	})
	cause := backupStaleCause(getTestService(), time.Now().Add(-9*24*time.Hour).Unix())
	if !strings.Contains(cause, "403 Forbidden") {
		t.Errorf("cause = %q, want the stored error quoted", cause)
	}
	if !strings.Contains(cause, `status "fail"`) {
		t.Errorf("cause = %q, want the status named", cause)
	}
}

// TestB446_HealthyInstallHasNoExcuse: a fresh run must not produce a sentence at all, or every
// healthy install would render a scary explanation under a passing test.
func TestB446_HealthyInstallHasNoExcuse(t *testing.T) {
	b446Primed(t, map[string]string{
		"backup.enabled":        "1",
		"backup.in_app_enabled": "1",
		"backup.schedule":       "0 4 * * *",
		"backup.last_status":    "ok",
	})
	if cause := backupStaleCause(getTestService(), time.Now().Add(-2*time.Hour).Unix()); cause != "" {
		t.Fatalf("a fresh run produced an excuse: %q", cause)
	}
}
