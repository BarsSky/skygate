// internal/release/monitor_b425_test.go — B425 (2026-10-26).
//
// THE MEASURED CASE. The operator received the update-available alert ~4 hours after the
// release was published. Two causes stacked:
//
//  1. the monitor's FIRST check waited a full CheckEvery interval after boot — and every
//     update restarts the container, so the channel went quiet for an hour exactly when the
//     operator was watching;
//  2. the poll could be answered with 403 (the /admin/update page was spending the same
//     anonymous GitHub quota, fixed in B424), and THIS file was the one place that treated
//     a rate limit as silence: `if err != ErrRateLimited { log… }`.
//
// These tests pin the three properties that make the delay explainable and short: the first
// check fires after FirstDelay (not after an interval), an announced tag survives a restart
// (so the prompt check cannot become a repeat alert), and a rate limit is logged with the
// reset hint.
package release

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"skygate/internal/db"
)

type b425Notifier struct{ alerts []string }

func (n *b425Notifier) SendAlert(text string) int64 {
	n.alerts = append(n.alerts, text)
	return int64(len(n.alerts))
}

func b425DB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b425.db")
	if err != nil {
		t.Skipf("sqlite unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

// b425Server returns a fake GitHub Releases API serving one tag.
func b425Server(t *testing.T, tag string, status int, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if status != http.StatusOK {
			w.Header().Set("X-RateLimit-Reset", "1793000000")
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"tag_name":%q,"name":"x","body":"notes","html_url":"https://example.test/r/%s"}`, tag, tag)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestB425_FirstCheckIsPromptAfterBoot proves the first check no longer waits a full
// interval: with FirstDelay=20ms and CheckEvery=1h the monitor must poll almost at once.
func TestB425_FirstCheckIsPromptAfterBoot(t *testing.T) {
	var hits int32
	srv := b425Server(t, "v9.9.9", http.StatusOK, &hits)
	m := &Monitor{
		HTTP: srv.Client(), Current: "v1.0.0", APIBase: srv.URL,
		Notifier: &b425Notifier{}, CheckEvery: time.Hour,
		FirstDelay: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&hits) >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no poll within 3s although FirstDelay was 20ms (CheckEvery is 1h) — the "+
		"first check still waits for the interval (polls=%d)", atomic.LoadInt32(&hits))
}

// TestB425_AnnouncedTagSurvivesRestart: the persisted tag is why the prompt check is safe.
// A restarted process must NOT re-announce it, and a genuinely newer tag must still alert.
func TestB425_AnnouncedTagSurvivesRestart(t *testing.T) {
	d := b425DB(t)
	if err := db.SetGlobalSetting(d, notifiedTagKey, "v2.0.0"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	n := &b425Notifier{}
	srv := b425Server(t, "v2.0.0", http.StatusOK, nil)
	m := &Monitor{HTTP: srv.Client(), Current: "v1.0.0", Notifier: n, DB: d, APIBase: srv.URL}
	m.tick(context.Background())
	if len(n.alerts) != 0 {
		t.Fatalf("the persisted tag was announced again after a restart (%d alerts)", len(n.alerts))
	}

	// A newer release is news even though a tag is persisted.
	srv2 := b425Server(t, "v2.1.0", http.StatusOK, nil)
	m2 := &Monitor{HTTP: srv2.Client(), Current: "v1.0.0", Notifier: n, DB: d, APIBase: srv2.URL}
	m2.tick(context.Background())
	if len(n.alerts) != 1 {
		t.Fatalf("a newer release was NOT announced after a restart (alerts=%d)", len(n.alerts))
	}
	if got, _ := db.GetGlobalSetting(d, notifiedTagKey, ""); got != "v2.1.0" {
		t.Fatalf("persisted tag = %q, want v2.1.0 (the announcement must survive the next restart)", got)
	}
}

// TestB425_RateLimitIsNotSilence pins the second cause: a 403 used to produce no log line.
func TestB425_RateLimitIsNotSilence(t *testing.T) {
	srv := b425Server(t, "", http.StatusForbidden, nil)
	m := &Monitor{HTTP: srv.Client(), Current: "v1.0.0", APIBase: srv.URL, Notifier: &b425Notifier{}, CheckEvery: time.Hour}

	var buf strings.Builder
	logTo(&buf, func() { m.tick(context.Background()) })
	out := buf.String()
	if !strings.Contains(out, "rate limit") {
		t.Fatalf("a rate-limited poll produced no rate-limit log line:\n%s", out)
	}
	if !strings.Contains(out, "SKYGATE_GITHUB_TOKEN") {
		t.Fatalf("the rate-limit line does not name the way out (a token):\n%s", out)
	}
	if !strings.Contains(out, "1793000000") {
		t.Fatalf("the reset hint from X-RateLimit-Reset was dropped:\n%s", out)
	}
}

// TestB425_ResetNotifiedClearsPersistence: after a successful upgrade the old tag must not
// keep suppressing anything (the running version changed; old notifications don't apply).
func TestB425_ResetNotifiedClearsPersistence(t *testing.T) {
	d := b425DB(t)
	if err := db.SetGlobalSetting(d, notifiedTagKey, "v2.0.0"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := &Monitor{Current: "v2.0.0", DB: d, Notified: map[string]bool{"v2.0.0": true}}
	m.ResetNotified()
	if m.Notified["v2.0.0"] {
		t.Fatal("ResetNotified left the in-memory entry")
	}
	if got, _ := db.GetGlobalSetting(d, notifiedTagKey, ""); got != "" {
		t.Fatalf("ResetNotified left the persisted tag %q — the next restart would stay quiet about it", got)
	}
}

// logTo captures the standard logger for one call (the monitor logs through `log`, which is
// process-global; tests in one package run sequentially, so this is safe here).
func logTo(buf *strings.Builder, fn func()) {
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prev); log.SetFlags(prevFlags) }()
	fn()
}
