// v1.5.0+ / B219 + B220 — unit tests for the Patroni
// /switchover helper (db.FailoverDB) and the
// last_failover state tracking
// (db.SetLastFailover / GetLastFailover /
// ClearLastFailover).
//
// FailoverDB makes a real HTTP call to Patroni's
// REST API. We test it against an httptest.Server
// (no real Patroni required) covering:
//
//   - happy path: 200 + succeeded=true → no error
//   - 4xx rejection (e.g. "no healthy leader") →
//     error with the body in the message
//   - 200 but succeeded=false + err field → error
//   - empty URL / empty candidate → error before HTTP
//   - network error (server unreachable) → error
//
// SetLastFailover / GetLastFailover /
// ClearLastFailover talk to global_settings (real
// DB) and require SKYGATE_TEST_PG_DSN (or
// SKYGATE_DB_DSN) — they skip otherwise.
//
// The end-to-end wiring (button → handler → audit
// row) is exercised by the B219 / B220 live-verify
// on the agent (the agent doesn't have Patroni
// running, so the live-verify checks that the wiring
// returns a clean error + writes the
// db.failover.error / db.failover_rollback.error
// audit rows when Patroni is unreachable).

package db

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestFailoverDB_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/switchover" {
			t.Errorf("expected /switchover, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"succeeded": true, "switchover_timestamp": "2026-09-02 12:34:56"}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := FailoverDB(ctx, srv.URL, "", "pg-replica-1", "B219 test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Succeeded {
		t.Errorf("Succeeded = false, want true")
	}
	if res.SwitchoverTimestamp == "" {
		t.Errorf("SwitchoverTimestamp is empty")
	}
}

func TestFailoverDB_4xxRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error": "no healthy leader"}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := FailoverDB(ctx, srv.URL, "", "pg-replica-1", "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Errorf("error should mention HTTP 409, got: %v", err)
	}
	if !strings.Contains(err.Error(), "no healthy leader") {
		t.Errorf("error should include Patroni body text, got: %v", err)
	}
}

func TestFailoverDB_200ButSucceededFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"succeeded": false, "err": "another switchover in progress"}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := FailoverDB(ctx, srv.URL, "", "pg-replica-1", "")
	if err == nil {
		t.Fatalf("expected error, got nil (res=%+v)", res)
	}
	if res == nil || res.Succeeded {
		t.Errorf("res.Succeeded should be false, got %+v", res)
	}
	if !strings.Contains(err.Error(), "another switchover in progress") {
		t.Errorf("error should include Patroni err field, got: %v", err)
	}
}

func TestFailoverDB_EmptyURL(t *testing.T) {
	_, err := FailoverDB(context.Background(), "", "", "pg-replica-1", "")
	if err == nil {
		t.Fatalf("expected error for empty URL")
	}
	if !strings.Contains(err.Error(), "patroni URL") {
		t.Errorf("error should mention patroni URL, got: %v", err)
	}
}

func TestFailoverDB_EmptyCandidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("HTTP server should not be called for empty candidate")
	}))
	defer srv.Close()
	_, err := FailoverDB(context.Background(), srv.URL, "", "", "")
	if err == nil {
		t.Fatalf("expected error for empty candidate")
	}
	if !strings.Contains(err.Error(), "candidate") {
		t.Errorf("error should mention candidate, got: %v", err)
	}
}

func TestFailoverDB_NetworkError(t *testing.T) {
	// httptest server that we close immediately —
	// any call to it will fail with "connection
	// refused" or similar.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	_, err := FailoverDB(context.Background(), srv.URL, "", "pg-replica-1", "")
	if err == nil {
		t.Fatalf("expected error for unreachable server")
	}
	// We don't pin the exact error message because
	// it depends on the OS ("connection refused" on
	// Linux, "No connection could be made" on
	// Windows). Just confirm the error mentions the
	// URL or "POST" so the operator can see what
	// failed.
	if !strings.Contains(err.Error(), "POST") && !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error should mention the URL or POST, got: %v", err)
	}
}

func TestFailoverDB_BodySendsCandidate(t *testing.T) {
	// Pin that the helper sends the candidate in the
	// JSON body (not just as a query param) — the
	// Patroni API expects it in the body.
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"succeeded": true}`))
	}))
	defer srv.Close()
	_, err := FailoverDB(context.Background(), srv.URL, "current-leader", "pg-replica-2", "B219 body test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotBody, `"candidate":"pg-replica-2"`) {
		t.Errorf("body should include candidate, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"leader":"current-leader"`) {
		t.Errorf("body should include leader when set, got: %s", gotBody)
	}
}

func TestFailoverDB_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := FailoverDB(ctx, srv.URL, "", "pg-replica-1", "")
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}

func TestSetLastFailover_RoundTrip(t *testing.T) {
	// The B220 SetLastFailover / GetLastFailover /
	// ClearLastFailover helpers talk to global_settings
	// (the B145-era key-value table). These tests
	// require a real PG connection (skip if not
	// available) so we can verify the round-trip
	// against the actual schema.
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		dsn = os.Getenv("SKYGATE_DB_DSN")
	}
	if dsn == "" {
		t.Skip("SKYGATE_TEST_PG_DSN not set — skipping DB round-trip test")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()
	if err := d.Ping(); err != nil {
		t.Skipf("db not reachable: %v", err)
	}
	// Use a unique key (key = "db.last_failover" in
	// production, but for the test we use the same
	// key to exercise the production code path).
	// Pre-clean.
	_, _ = d.Exec(`DELETE FROM global_settings WHERE key = 'db.last_failover'`)
	// Set
	st := &LastFailoverState{
		Old:       "pg-old-primary",
		New:       "pg-new-primary",
		Timestamp: 1234567890,
		Operator:  "skyadmin",
		Reason:    "B220 test",
	}
	if err := SetLastFailover(d, st); err != nil {
		t.Fatalf("SetLastFailover: %v", err)
	}
	// Get
	got, err := GetLastFailover(d)
	if err != nil {
		t.Fatalf("GetLastFailover: %v", err)
	}
	if got == nil {
		t.Fatalf("GetLastFailover returned nil after SetLastFailover")
	}
	if got.Old != st.Old || got.New != st.New || got.Operator != st.Operator || got.Reason != st.Reason || got.Timestamp != st.Timestamp {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, st)
	}
	// Clear
	if err := ClearLastFailover(d); err != nil {
		t.Fatalf("ClearLastFailover: %v", err)
	}
	// Get should now return nil
	got2, err := GetLastFailover(d)
	if err != nil {
		t.Fatalf("GetLastFailover after Clear: %v", err)
	}
	if got2 != nil {
		t.Errorf("GetLastFailover after Clear = %+v, want nil", got2)
	}
}

func TestSetLastFailover_Validation(t *testing.T) {
	// Pure tests — no DB needed.
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		dsn = os.Getenv("SKYGATE_DB_DSN")
	}
	if dsn == "" {
		t.Skip("no DB DSN — skip")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Ping(); err != nil {
		t.Skipf("db not reachable: %v", err)
	}
	cases := []struct {
		name string
		st   *LastFailoverState
	}{
		{"nil state", nil},
		{"empty old", &LastFailoverState{New: "x"}},
		{"empty new", &LastFailoverState{Old: "x"}},
		{"both empty", &LastFailoverState{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := SetLastFailover(d, c.st); err == nil {
				t.Errorf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestGetLastFailover_Empty(t *testing.T) {
	// When db.last_failover is not set, GetLastFailover
	// should return (nil, nil) — not an error. This
	// is the "no rollback available" case the page
	// uses to hide the Rollback card.
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		dsn = os.Getenv("SKYGATE_DB_DSN")
	}
	if dsn == "" {
		t.Skip("no DB DSN — skip")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Ping(); err != nil {
		t.Skipf("db not reachable: %v", err)
	}
	// Clear first to guarantee the "empty" state.
	_, _ = d.Exec(`DELETE FROM global_settings WHERE key = 'db.last_failover'`)
	got, err := GetLastFailover(d)
	if err != nil {
		t.Fatalf("GetLastFailover on empty: %v", err)
	}
	if got != nil {
		t.Errorf("GetLastFailover on empty = %+v, want nil", got)
	}
}
