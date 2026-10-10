// scheduler_b375_test.go — B375 (2026-10-10): the scheduled auto-update must
// ACTUALLY RUN.
//
// THE LIVE DEFECT (reference deployment, PostgreSQL, measured 2026-10-09/10):
// global_settings said update_schedule_enabled = 1 / update_schedule_time =
// 08:00, the operator had set exactly that in the panel — and the container
// journal held ZERO `update-scheduler:` lines. The goroutine itself was gated
// on SKYGATE_UPDATE_SCHEDULE_ENABLED (the ENV value, "false"), so there was
// nothing to arm, while main.go's else-branch told the operator that
// "/admin/update page can enable" it — a page that can only write the DB keys.
//
// These tests pin the four halves of the fix, each against a REAL SQLite
// database (db.OpenWithDialect + db.ApplyMigrations), so the toggle under test
// is the toggle the panel writes:
//
//  1. the DB decides whether the ticker ACTS (armed && !enabled ⇒ no-op);
//  2. the DB toggle ON + the exact minute ⇒ the run happens;
//  3. the B375 catch-up window (late by 5m ⇒ runs, late by 20m ⇒ does not);
//  4. the dedup is per SLOT per DAY (already ran today ⇒ does not run again),
//     which is what stops a catch-up window from firing once per tick;
//  5. the boot state is truthful: a DB schedule enabled while the ENV default
//     is false must still be reported as armed-and-active, and the trap
//     ("schedule enabled but the scheduler is not running") has its own
//     sentence instead of a promise the page cannot keep.
package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

// b375DB is a real SQLite database with the real migrations applied — the same
// schema the panel writes update_schedule_* into.
//
// The file form (not `:memory:`) is deliberate: the sqlite pool hands every new
// connection its own empty in-memory database, so a seeded row is invisible to
// the next query and the test would "pass" by finding nothing (the B276 lesson).
func b375DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b375.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// b375SetSchedule writes the two keys the /admin/update Schedule form writes.
// It goes through db.SetGlobalSetting (the panel's own writer) rather than a raw
// INSERT, so a schema change to global_settings breaks the test instead of
// letting it seed a shape production no longer uses.
func b375SetSchedule(t *testing.T, d *sql.DB, enabled bool, hhmm string) {
	t.Helper()
	if err := skygatedb.SetGlobalSettingBool(d, "update_schedule_enabled", enabled); err != nil {
		t.Fatalf("set update_schedule_enabled=%v: %v", enabled, err)
	}
	if err := skygatedb.SetGlobalSetting(d, "update_schedule_time", hhmm); err != nil {
		t.Fatalf("set update_schedule_time=%q: %v", hhmm, err)
	}
}

func b375LastRunText(t *testing.T, d *sql.DB) string {
	t.Helper()
	v, err := skygatedb.GetGlobalSetting(d, "update_schedule_last_run", "")
	if err != nil {
		t.Fatalf("read update_schedule_last_run: %v", err)
	}
	return v
}

// b375Deps builds SchedulerDeps around a fake GitHub that always offers a
// newer release. `prev` is the running build, so a target of v9.9.9 is newer.
//
// The shell is STUBBED: a real runScheduled spawns NewDockerUpgrader, which
// shells out to git/docker — on a dev box that is a multi-minute hang inside a
// unit test, and it would write real state. The stub fails every command, so
// the goroutine reaches a terminal phase immediately and the test can wait for
// it instead of leaking it.
func b375Deps(t *testing.T, d *sql.DB, prev string) SchedulerDeps {
	t.Helper()
	b375StubShell(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghRelease{
			TagName:     "v9.9.9",
			Name:        "v9.9.9",
			HTMLURL:     "https://github.com/example/skygate/releases/tag/v9.9.9",
			Body:        "B375 fixture",
			PublishedAt: time.Now().Add(-time.Hour),
		})
	}))
	t.Cleanup(srv.Close)
	return SchedulerDeps{
		DB: d,
		// The state file must be a per-test temp file: an empty path resolves
		// to the CURRENT DIRECTORY, and the tests would litter the package
		// directory with skygate-update-status-*.tmp files.
		State: NewStateStore(t.TempDir() + "/update-state.json"),
		Checker: &Checker{
			HTTPClient:     &http.Client{Transport: b375Transport(t, srv.URL)},
			Owner:          "example",
			Repo:           "skygate",
			Channel:        "stable",
			CurrentVersion: prev,
		},
		BuildVersion: prev,
		RepoPath:     t.TempDir(),
		Cfg:          SchedulerCfg{},
	}
}

// b375Transport redirects every request to the fixture server, keeping the path
// and query intact (the Checker builds
// https://api.github.com/repos/<owner>/<repo>/releases/latest).
//
// Deliberately self-contained rather than reusing the older rewriteTransport in
// checker_test.go: that helper splits the target with a regex pipeline that
// mangled the URL on this box ("lookup ://127.0.0.1"), and a broken fixture must
// never be able to make a product contract look satisfied or violated. The
// Checker calls c.getenv/HTTPClient, so the rewrite is the supported seam.
func b375Transport(t *testing.T, raw string) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse fixture server URL %q: %v", raw, err)
	}
	return b375RoundTrip{target: u}
}

type b375RoundTrip struct{ target *url.URL }

func (rt b375RoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// b375StubShell replaces the package's one subprocess chokepoint so a
// scheduled job that this test starts cannot run git/docker.
func b375StubShell(t *testing.T) {
	t.Helper()
	prev := shellExec
	shellExec = func(ctx context.Context, name string, args ...string) (string, error) {
		return "", errB375StubShell
	}
	t.Cleanup(func() { shellExec = prev })
}

// errB375StubShell is the failure the stubbed shell returns. Named so a test
// output that contains it is obviously a fixture, not a product error.
var errB375StubShell = errors.New("b375 test: subprocess execution is stubbed")

// b375AwaitTerminal waits (bounded) for the goroutine runScheduled spawned to
// reach a terminal phase, so a test cannot leak a live upgrader into the next
// one. It returns the final state, or nil when no state was ever created.
func b375AwaitTerminal(t *testing.T, st *StateStore) *State {
	t.Helper()
	for i := 0; i < 400; i++ {
		if s := st.Get(); s != nil {
			switch s.Phase {
			case PhaseDone, PhaseFailed, PhaseRolledBack, PhaseBuildDone:
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the scheduled job never reached a terminal phase (state=%+v) — the fixture leaked a goroutine", st.Get())
	return nil
}

// b375ForceDocker makes the scheduled path reachable on a Windows dev box: the
// only automated install kind it drives is Docker, and DetectInstallKind honours
// SKYGATE_INSTALL_KIND before it probes the filesystem.
func b375ForceDocker(t *testing.T) {
	t.Helper()
	t.Setenv("SKYGATE_INSTALL_KIND", "docker")
}

// b375NowMinute is the HH:MM of `now`, i.e. the schedule that is due RIGHT NOW.
// Every tick test goes through the production readSchedule/tick path with the
// real wall clock — no clock injection exists in the product, and inventing one
// for a test would pin a shape production does not have.
func b375NowMinute(now time.Time) string {
	return now.Format("15:04")
}

// ---------------------------------------------------------------------------
// 1. the DB decides whether the ticker acts
// ---------------------------------------------------------------------------

// TestB375_TickDoesNothingWhenTheDBToggleIsOff pins the FIRST half of the fix:
// armed is not the same as enabled. A scheduler that ran whenever it pleased
// would be worse than one that never ran, so the OFF row must be a hard no-op —
// including when the ENV default says true, which is the mirror image of the
// live defect.
func TestB375_TickDoesNothingWhenTheDBToggleIsOff(t *testing.T) {
	b375ForceDocker(t)
	d := b375DB(t)
	now := time.Now()
	b375SetSchedule(t, d, false, b375NowMinute(now))

	deps := b375Deps(t, d, "v1.0.0")
	// The env default is ON and the schedule time is "due" — only the DB says
	// off. Pre-B375 code paths read the env; the post-B375 tick must not.
	deps.Cfg = SchedulerCfg{UpdateScheduleEnabled: true, UpdateScheduleTime: b375NowMinute(now)}

	tick(context.Background(), deps)

	if got := b375LastRunText(t, d); got != "" {
		t.Errorf("the scheduler acted while the DATABASE said off: update_schedule_last_run=%q", got)
	}
}

// TestB375_TickRunsAtTheExactConfiguredMinute is the pre-B375 behaviour that
// must survive the window rewrite: on the minute, it runs.
func TestB375_TickRunsAtTheExactConfiguredMinute(t *testing.T) {
	b375ForceDocker(t)
	d := b375DB(t)
	b375SetSchedule(t, d, true, b375NowMinute(time.Now()))

	deps := b375Deps(t, d, "v1.0.0")
	tick(context.Background(), deps)

	if got := b375LastRunText(t, d); got == "" {
		t.Fatal("the schedule was enabled and the current minute IS the slot, but nothing ran (no update_schedule_last_run stamp)")
	}
	// The run must have STARTED, not merely been stamped: the state store is
	// what /admin/update renders.
	if st := b375AwaitTerminal(t, deps.State); st == nil {
		t.Fatal("the scheduler stamped a run but never created an update job state")
	}
}

// TestB375_TickCatchesUpFiveMinutesLate is THE new window: the container was
// recreated (or booted) just after the slot, which pre-B375 lost the whole day.
func TestB375_TickCatchesUpFiveMinutesLate(t *testing.T) {
	b375ForceDocker(t)
	d := b375DB(t)
	slot := time.Now().Add(-5 * time.Minute)
	b375SetSchedule(t, d, true, b375NowMinute(slot))

	deps := b375Deps(t, d, "v1.0.0")
	tick(context.Background(), deps)

	if got := b375LastRunText(t, d); got == "" {
		t.Fatal("the slot was 5 minutes ago (inside the 10m catch-up window) and nothing ran")
	}
	st := b375AwaitTerminal(t, deps.State)
	if st == nil {
		t.Fatal("the catch-up stamped a run but never created an update job state")
	}
	if !strings.Contains(st.ToVersion, "9.9.9") {
		t.Errorf("the catch-up targeted %q, want the newer release the checker offered", st.ToVersion)
	}
}

// TestB375_TickDoesNotFireTwentyMinutesLate keeps the window BOUNDED: "catch up"
// must not decay into "run whenever the process happens to notice".
func TestB375_TickDoesNotFireTwentyMinutesLate(t *testing.T) {
	b375ForceDocker(t)
	d := b375DB(t)
	slot := time.Now().Add(-20 * time.Minute)
	b375SetSchedule(t, d, true, b375NowMinute(slot))

	tick(context.Background(), b375Deps(t, d, "v1.0.0"))

	if got := b375LastRunText(t, d); got != "" {
		t.Errorf("the slot was 20 minutes ago (outside the 10m catch-up window) but the scheduler ran: last_run=%q", got)
	}
}

// TestB375_AlreadyRanForThisSlotTodayIsNotRepeated is the dedup that makes the
// window safe: without it a 10-minute window on a 30-second tick would start the
// updater up to twenty times. The stamp is the same shape writeLastRun produces
// (RFC 3339, UTC).
func TestB375_AlreadyRanForThisSlotTodayIsNotRepeated(t *testing.T) {
	b375ForceDocker(t)
	d := b375DB(t)
	slot := time.Now().Add(-5 * time.Minute)
	b375SetSchedule(t, d, true, b375NowMinute(slot))

	// Today, inside the window, exactly the slot minute — "already ran".
	if err := skygatedb.SetGlobalSetting(d, "update_schedule_last_run",
		slot.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed last_run: %v", err)
	}

	tick(context.Background(), b375Deps(t, d, "v1.0.0"))

	got := b375LastRunText(t, d)
	if got != slot.Format(time.RFC3339) {
		t.Errorf("the slot already ran today but the scheduler fired again: last_run=%q, want the seeded %q",
			got, slot.Format(time.RFC3339))
	}
}

// TestB375_YesterdaysRunDoesNotBlockToday pins the other side of the dedup: the
// guard is per DAY, so a stamp from an earlier local calendar day must not stop
// today's run. The build stamp is measured against local `now` (ranTodayAt
// compares local calendar days; writeLastRun happens to store UTC).
func TestB375_YesterdaysRunDoesNotBlockToday(t *testing.T) {
	b375ForceDocker(t)
	d := b375DB(t)
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	b375SetSchedule(t, d, true, b375NowMinute(now))
	if err := skygatedb.SetGlobalSetting(d, "update_schedule_last_run",
		yesterday.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed last_run: %v", err)
	}

	deps := b375Deps(t, d, "v1.0.0")
	tick(context.Background(), deps)

	got := b375LastRunText(t, d)
	if got == yesterday.Format(time.RFC3339) {
		t.Errorf("yesterday's stamp blocked today's run (last_run is still the seeded value %q)", got)
	}
	if got == "" {
		t.Fatal("no run happened at all")
	}
	b375AwaitTerminal(t, deps.State)
	// And the new stamp must belong to TODAY, in the local calendar the
	// predicate reads.
	stamp, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("the scheduler wrote an unparsable timestamp %q: %v", got, err)
	}
	local := stamp.Local()
	if local.Year() != now.Year() || local.Month() != now.Month() || local.Day() != now.Day() {
		t.Errorf("the new timestamp %q is not today (%s) in local time", got, now.Format("2006-01-02"))
	}
}

// ---------------------------------------------------------------------------
// 2. the sliding window, at the predicate level (clock-independent)
// ---------------------------------------------------------------------------

// TestB375_ScheduledRunDueWindow is the same window arithmetic without the wall
// clock: it pins the boundary the DB tests above can only probe from "now".
func TestB375_ScheduledRunDueWindow(t *testing.T) {
	base := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		now      time.Time
		hhmm     string
		wantDue  bool
		wantLate time.Duration
	}{
		{"exactly_on_the_minute", base, "08:00", true, 0},
		{"thirty_seconds_late", base.Add(30 * time.Second), "08:00", true, 30 * time.Second},
		{"five_minutes_late", base.Add(5 * time.Minute), "08:00", true, 5 * time.Minute},
		{"just_inside_the_window", base.Add(9*time.Minute + 59*time.Second), "08:00", true, 9*time.Minute + 59*time.Second},
		{"exactly_at_the_window_edge", base.Add(10 * time.Minute), "08:00", false, 0},
		{"twenty_minutes_late", base.Add(20 * time.Minute), "08:00", false, 0},
		{"one_minute_before_the_slot", base.Add(-time.Minute), "08:00", false, 0},
		{"an_hour_before_the_slot", base.Add(-time.Hour), "08:00", false, 0},
		{"invalid_time_is_never_due", base, "25:00", false, 0},
		{"empty_time_is_never_due", base, "", false, 0},
		// A different day must not be dragged in by the window arithmetic:
		// "23:55 yesterday" is a FUTURE slot today, so 00:05 does not fire.
		{"midnight_does_not_catch_up_yesterdays_slot", time.Date(2026, 10, 10, 0, 5, 0, 0, time.UTC), "23:55", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			due, late := scheduledRunDue(tc.now, tc.hhmm)
			if due != tc.wantDue {
				t.Fatalf("scheduledRunDue(%s, %q) due=%v, want %v", tc.now.Format(time.RFC3339), tc.hhmm, due, tc.wantDue)
			}
			if tc.wantDue && late != tc.wantLate {
				t.Errorf("scheduledRunDue(%s, %q) late=%s, want %s", tc.now.Format(time.RFC3339), tc.hhmm, late, tc.wantLate)
			}
		})
	}
}

// TestB375_RanTodayAt is the dedup predicate's decision table.
func TestB375_RanTodayAt(t *testing.T) {
	today := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		hhmm    string
		lastRun time.Time
		now     time.Time
		want    bool
	}{
		{"never_ran", "08:00", time.Time{}, today, false},
		{"ran_at_the_slot", "08:00", time.Date(2026, 10, 10, 8, 0, 15, 0, time.UTC), today, true},
		{"ran_late_inside_the_window", "08:00", time.Date(2026, 10, 10, 8, 7, 0, 0, time.UTC), today, true},
		{"ran_yesterday", "08:00", time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC), today, false},
		{"ran_today_BEFORE_the_slot", "20:00", time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC), today, false},
		{"zero_last_run_never_matches", "08:00", time.Time{}, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ranTodayAt(tc.hhmm, tc.lastRun, tc.now); got != tc.want {
				t.Errorf("ranTodayAt(%q, %s, %s) = %v, want %v", tc.hhmm,
					tc.lastRun.Format(time.RFC3339), tc.now.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

// TestB375_CatchUpWindowIsNamedAndBounded pins the constant the whole catch-up
// story rests on: a named window, small enough to stay "the scheduled time".
func TestB375_CatchUpWindowIsNamedAndBounded(t *testing.T) {
	if CatchUpWindow != 10*time.Minute {
		t.Fatalf("CatchUpWindow = %s, want 10m (B375's contract: bounded, named, not the whole day)", CatchUpWindow)
	}
	if CatchUpWindow <= TickInterval {
		t.Fatalf("CatchUpWindow (%s) must be larger than TickInterval (%s), otherwise the window can be missed entirely", CatchUpWindow, TickInterval)
	}
}

// ---------------------------------------------------------------------------
// 3. the DB is authoritative while the ENV default is false
// ---------------------------------------------------------------------------

// TestB375_DBScheduleEnabledWhileEnvDefaultIsFalse drives the EXACT live shape
// through the production reader: SKYGATE_UPDATE_SCHEDULE_ENABLED=false (the env
// default, i.e. cfg.UpdateScheduleEnabled == false), the panel's row saying
// enabled. The reader must answer enabled — which is what arms an acting
// scheduler — and must not claim the env is in charge.
func TestB375_DBScheduleEnabledWhileEnvDefaultIsFalse(t *testing.T) {
	d := b375DB(t)
	b375SetSchedule(t, d, true, "08:00")
	cfg := SchedulerCfg{UpdateScheduleEnabled: false, UpdateScheduleTime: "03:00"} // the live env default

	got := ReadScheduleAnswers(d, cfg)
	if !got.Enabled {
		t.Fatalf("the DB row says enabled=1 but the reader answered disabled (the live B375 defect, in reverse): %+v", got)
	}
	if !ScheduleEnabledFromDB(d, cfg) {
		t.Fatal("ScheduleEnabledFromDB disagrees with ReadScheduleAnswers")
	}
	if got.FromEnv {
		t.Error("FromEnv is true although global_settings HAS an enabled row — the boot log would blame the env for a panel-made choice")
	}
	if got.Time != "08:00" {
		t.Errorf("schedule time = %q, want the DB's %q", got.Time, "08:00")
	}
}

// TestB375_UntouchedInstallFallsBackToTheEnv is the compatibility half: an
// install that never opened the panel has no row, so the env default (and its
// documented "03:00" time default) must still win. This is why main.go keeps
// passing cfg through.
func TestB375_UntouchedInstallFallsBackToTheEnv(t *testing.T) {
	d := b375DB(t)
	cfg := SchedulerCfg{UpdateScheduleEnabled: true, UpdateScheduleTime: "04:15"}

	got := ReadScheduleAnswers(d, cfg)
	if !got.Enabled || got.Time != "04:15" {
		t.Fatalf("an install with no global_settings row must use the env default, got %+v", got)
	}
	if !got.FromEnv {
		t.Error("FromEnv must be true when global_settings has no update_schedule_enabled row — the boot log has to say the env default is in use")
	}
}

// TestB375_EmptyTimeFallsBackToTheDocumentedDefault pins the "03:00" default the
// scheduler has always applied, now via the named constant.
func TestB375_EmptyTimeFallsBackToTheDocumentedDefault(t *testing.T) {
	d := b375DB(t)
	if err := skygatedb.SetGlobalSetting(d, "update_schedule_time", ""); err != nil {
		t.Fatalf("clear the time key: %v", err)
	}
	got := ReadScheduleAnswers(d, SchedulerCfg{UpdateScheduleEnabled: true})
	if got.Time != defaultScheduleTime {
		t.Errorf("time = %q, want the default %q", got.Time, defaultScheduleTime)
	}
}

// ---------------------------------------------------------------------------
// 4. the boot state and its sentence
// ---------------------------------------------------------------------------

// TestB375_BootLogTellsTheTruth is the unit-level stand-in for main(): main() is
// not testable, so the sentence it logs is a pure function with a decision
// table. The row that matters most is the TRAP — armed=false, enabled=true —
// because the pre-B375 sentence claimed the page could fix exactly that state.
func TestB375_BootLogTellsTheTruth(t *testing.T) {
	cases := []struct {
		name        string
		armed       bool
		sched       ReadSchedule
		wantActive  bool
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:       "armed_and_the_schedule_is_on",
			armed:      true,
			sched:      ReadSchedule{Enabled: true, Time: "08:00"},
			wantActive: true,
			wantContain: []string{
				"update-scheduler: ARMED",
				"08:00",
				CatchUpWindow.String(),
			},
		},
		{
			name:       "armed_but_the_schedule_is_off",
			armed:      true,
			sched:      ReadSchedule{Enabled: false, Time: "03:00"},
			wantActive: false,
			wantContain: []string{
				"update-scheduler: ARMED",
				"schedule is OFF",
				"/admin/update",
			},
			wantAbsent: []string{"NOT RUNNING"},
		},
		{
			name:       "not_armed_while_the_schedule_is_on_is_the_trap",
			armed:      false,
			sched:      ReadSchedule{Enabled: true, Time: "08:00"},
			wantActive: false,
			wantContain: []string{
				"NOT RUNNING",
				"NO schedule can fire",
			},
			// The pre-B375 lie: the page cannot start a goroutine.
			wantAbsent: []string{"/admin/update can enable"},
		},
		{
			name:        "not_armed_and_off",
			armed:       false,
			sched:       ReadSchedule{Enabled: false, Time: "03:00"},
			wantActive:  false,
			wantContain: []string{"NOT RUNNING"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, active := SchedulerBootLog(tc.armed, tc.sched)
			if active != tc.wantActive {
				t.Errorf("active = %v, want %v (msg=%q)", active, tc.wantActive, msg)
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(msg, want) {
					t.Errorf("the boot sentence does not contain %q:\n%s", want, msg)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(msg, absent) {
					t.Errorf("the boot sentence still contains the false claim %q:\n%s", absent, msg)
				}
			}
		})
	}
}

// TestB375_BootLogNamesTheEnvFallback pins the clause the operator needs when
// the DB has no row: WHICH env variable is in charge.
func TestB375_BootLogNamesTheEnvFallback(t *testing.T) {
	msg, _ := SchedulerBootLog(true, ReadSchedule{Enabled: false, Time: "03:00", FromEnv: true})
	if !strings.Contains(msg, "SKYGATE_UPDATE_SCHEDULE_ENABLED") {
		t.Errorf("the boot sentence must name the env variable when no DB row exists:\n%s", msg)
	}
	msg, _ = SchedulerBootLog(true, ReadSchedule{Enabled: true, Time: "08:00", FromEnv: false})
	if strings.Contains(msg, "SKYGATE_UPDATE_SCHEDULE_ENABLED") {
		t.Errorf("the env fallback clause must not appear when the panel has spoken:\n%s", msg)
	}
}

// TestB375_ArmedStateIsReadableAndRaceSafe pins the process-level state the
// admin page reads: the default is "not armed" (nothing set it), SetSchedulerArmed
// records both halves, and the reader sees them. Run under -race, this also
// covers the mutex.
func TestB375_ArmedStateIsReadableAndRaceSafe(t *testing.T) {
	SetSchedulerArmed(false, "")
	if SchedulerArmed() {
		t.Fatal("SchedulerArmed() must default to false before main() starts the goroutine")
	}
	if got := SchedulerArmedReason(); got != "" {
		t.Errorf("default reason = %q, want empty", got)
	}

	SetSchedulerArmed(true, "started by cmd/skygate/main.go")
	if !SchedulerArmed() {
		t.Fatal("SchedulerArmed() = false after SetSchedulerArmed(true, …)")
	}
	if got := SchedulerArmedReason(); got != "started by cmd/skygate/main.go" {
		t.Errorf("reason = %q, want the string that was set", got)
	}
	t.Cleanup(func() { SetSchedulerArmed(false, "") })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = SchedulerArmed()
			_ = SchedulerArmedReason()
		}
	}()
	for i := 0; i < 200; i++ {
		SetSchedulerArmed(i%2 == 0, "flap")
	}
	<-done
}

// ---------------------------------------------------------------------------
// 5. the non-Docker skip is visible
// ---------------------------------------------------------------------------

// TestB375_NonDockerSkipIsLogged pins requirement (1)'s last line: the scheduled
// path supports Docker only, and that has to be a JOURNAL FACT, not a silent
// return. Pre-B375 the operator saw an armed, enabled schedule and an empty
// journal.
func TestB375_NonDockerSkipIsLogged(t *testing.T) {
	t.Setenv("SKYGATE_INSTALL_KIND", "systemd")
	d := b375DB(t)
	b375SetSchedule(t, d, true, b375NowMinute(time.Now()))

	logPath := t.TempDir() + "/b375.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()
	prev := log.Writer()
	log.SetOutput(f)
	t.Cleanup(func() { log.SetOutput(prev) })

	tick(context.Background(), b375Deps(t, d, "v1.0.0"))

	log.SetOutput(prev)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "NOT SUPPORTED on install kind") {
		t.Errorf("the scheduled path skipped a native install silently; log was:\n%s", out)
	}
	if !strings.Contains(out, "systemd") {
		t.Errorf("the skip must NAME the detected install kind; log was:\n%s", out)
	}
	if got := b375LastRunText(t, d); got != "" {
		t.Errorf("a native install must not stamp a run: last_run=%q", got)
	}
}
