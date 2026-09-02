// v1.5.0+ / B214 — unit tests for the cancel + rollback
// live-runs registry + DSN parser. Most of the new
// behaviour is DB-bound (the live-verify on the agent
// exercises the cancel/rollback endpoints end-to-end);
// these tests pin the pure-Go helpers.

package dbmigrate

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestParseTargetDSNForRollback(t *testing.T) {
	cases := []struct {
		name    string
		dsn     string
		wantOK  bool
		wantH   string
		wantP   string
		wantDB  string
		wantU   string
		wantSSL string
	}{
		{
			name:   "standard libpq DSN",
			dsn:    "postgres://admin:secret@192.168.13.69:5433/skygate_staging?sslmode=disable",
			wantOK: true,
			wantH:  "192.168.13.69",
			wantP:  "5433",
			wantDB: "skygate_staging",
			wantU:  "admin",
			wantSSL: "disable",
		},
		{
			name:   "no password (just user)",
			dsn:    "postgres://scott@db.example.com/mydb",
			wantOK: true,
			wantH:  "db.example.com",
			wantP:  "",
			wantDB: "mydb",
			wantU:  "scott",
		},
		{
			name:   "no query string",
			dsn:    "postgres://u:p@h/d",
			wantOK: true,
			wantH:  "h",
			wantDB: "d",
			wantU:  "u",
		},
		{
			name:   "no sslmode in query",
			dsn:    "postgres://u:p@h/d?application_name=foo",
			wantOK: true,
			wantH:  "h",
			wantDB: "d",
			wantU:  "u",
		},
		{
			name:   "empty DSN",
			dsn:    "",
			wantOK: false,
		},
		{
			name:   "non-postgres scheme",
			dsn:    "mysql://u:p@h/d",
			wantOK: false,
		},
		{
			name:   "missing dbname (no /)",
			dsn:    "postgres://u:p@h",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, p, db, u, ssl, ok := parseTargetDSNForRollback(c.dsn)
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v", ok, c.wantOK)
				return
			}
			if !c.wantOK {
				return
			}
			if h != c.wantH {
				t.Errorf("host = %q, want %q", h, c.wantH)
			}
			if p != c.wantP {
				t.Errorf("port = %q, want %q", p, c.wantP)
			}
			if db != c.wantDB {
				t.Errorf("dbname = %q, want %q", db, c.wantDB)
			}
			if u != c.wantU {
				t.Errorf("user = %q, want %q", u, c.wantU)
			}
			if ssl != c.wantSSL {
				t.Errorf("sslmode = %q, want %q", ssl, c.wantSSL)
			}
		})
	}
}

func TestLiveRunsRegistry_RegisterAndCancel(t *testing.T) {
	// B214: registerLiveRun + CancelRun + IsRunLive
	// contract. The registry is process-scoped; we
	// use a unique runID per test to avoid cross-test
	// pollution (the live-runs map is global).
	runID := int64(time.Now().UnixNano())

	// Initially: not live.
	if IsRunLive(runID) {
		t.Fatalf("runID %d should not be live initially", runID)
	}

	// Register a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	registerLiveRun(runID, cancel)
	defer unregisterLiveRun(runID)

	if !IsRunLive(runID) {
		t.Errorf("IsRunLive(%d) = false, want true after register", runID)
	}

	// CancelRun signals the cancel func. ctx is now
	// canceled.
	if !CancelRun(runID) {
		t.Errorf("CancelRun(%d) returned false, want true", runID)
	}
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Errorf("ctx not canceled after CancelRun")
	}

	// After cancel, the registry still has the entry
	// (cleanup is the run goroutine's job). CancelRun
	// is idempotent — second call returns true (the
	// cancel func exists), but ctx is already canceled.
	if !CancelRun(runID) {
		t.Errorf("CancelRun(%d) on already-canceled returned false, want idempotent-true", runID)
	}

	// After unregister, the entry is gone. CancelRun
	// returns false (no in-flight tracking).
	unregisterLiveRun(runID)
	if IsRunLive(runID) {
		t.Errorf("IsRunLive(%d) = true after unregister, want false", runID)
	}
	if CancelRun(runID) {
		t.Errorf("CancelRun(%d) returned true after unregister, want false", runID)
	}
}

func TestLiveRunsRegistry_ConcurrentSafety(t *testing.T) {
	// B214 contract: the live-runs map is concurrent-
	// safe (multiple operators hitting the cancel
	// button for the same run, or the framework
	// registering while a cancel is in flight, must
	// not race). Hammer it with 100 goroutines doing
	// register / unregister / IsRunLive / CancelRun
	// and check that we don't panic + that the final
	// state is consistent.
	var wg sync.WaitGroup
	baseRunID := int64(9000000000)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runID := baseRunID + int64(i)
			ctx, cancel := context.WithCancel(context.Background())
			registerLiveRun(runID, cancel)
			IsRunLive(runID)
			CancelRun(runID)
			unregisterLiveRun(runID)
			_ = ctx
		}(i)
	}
	wg.Wait()
	// All 100 should be unregistered (we wait for
	// all goroutines to complete).
	for i := 0; i < 100; i++ {
		runID := baseRunID + int64(i)
		if IsRunLive(runID) {
			t.Errorf("runID %d still live after goroutines done", runID)
		}
	}
}

func TestRunCancelledStatus(t *testing.T) {
	// B214 contract: the RunCancelled sentinel exists
	// + is a distinct MigrationStatus value (not the
	// same as RunFailed or RunRolledBack). The UI
	// uses this to render the "cancelled" badge.
	if RunCancelled == RunFailed {
		t.Error("RunCancelled == RunFailed — must be distinct so the UI can tell operator-cancel from step-error")
	}
	if RunCancelled == RunRolledBack {
		t.Error("RunCancelled == RunRolledBack — must be distinct")
	}
	if RunCancelled == RunSuccess {
		t.Error("RunCancelled == RunSuccess — must be distinct")
	}
	if RunCancelled == RunRunning {
		t.Error("RunCancelled == RunRunning — must be distinct")
	}
	// And the string value is "cancelled" (lowercase)
	// so SQL filters match the rest of the run_* values.
	if string(RunCancelled) != "cancelled" {
		t.Errorf("string(RunCancelled) = %q, want 'cancelled'", string(RunCancelled))
	}
}

func TestRunIDFromPath(t *testing.T) {
	// B214 contract: runIDFromPath extracts the
	// numeric run ID from /admin/database/migrate/{id}/{suffix}.
	// The suffix must be stripped first; id is the
	// last path segment of the remaining path.
	cases := []struct {
		name   string
		path   string
		suffix  string
		wantID int64
		wantErr bool
	}{
		{
			name:   "cancel path",
			path:   "/admin/database/migrate/42/cancel",
			suffix: "/cancel",
			wantID: 42,
		},
		{
			name:   "rollback path",
			path:   "/admin/database/migrate/123/rollback",
			suffix: "/rollback",
			wantID: 123,
		},
		{
			name:   "non-numeric id",
			path:   "/admin/database/migrate/abc/cancel",
			suffix: "/cancel",
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := runIDFromPath(c.path, c.suffix)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error, got id=%d", id)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if id != c.wantID {
				t.Errorf("id = %d, want %d", id, c.wantID)
			}
		})
	}
}
