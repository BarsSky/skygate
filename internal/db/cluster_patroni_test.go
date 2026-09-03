// v1.5.0+ / B219 — unit tests for the Patroni
// /switchover helper (db.FailoverDB).
//
// The helper makes a real HTTP call to Patroni's
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
// The end-to-end wiring (button → handler → audit
// row) is exercised by the B219 live-verify on the
// agent (the agent doesn't have Patroni running, so
// the live-verify checks that the wiring returns a
// clean error + writes a db.failover.error audit
// row when Patroni is unreachable).

package db

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
