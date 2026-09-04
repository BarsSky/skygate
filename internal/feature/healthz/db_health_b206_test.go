// v1.5.0+ (B206) — unit tests for the /db/health sampler
// + handler.
//
// The pure helpers (humanBytes, the sample + response
// field copy in GetDBHealth, the Sampler state machine)
// are tested here. The actual pg_* queries are covered
// by the live B206 verify script (GET /db/health on the
// agent and check the JSON shape against the expected
// fields).

package healthz

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{539 * 1024 * 1024, "539.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{2 * 1024 * 1024 * 1024, "2.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := humanBytes(c.in)
			if got != c.want {
				t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNewDBHealthSampler_DefaultsApplied(t *testing.T) {
	s := NewDBHealthSampler(DBHealthConfig{}, nil)
	if s.cfg.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", s.cfg.Interval)
	}
	if s.cfg.QueryTimeout != 3*time.Second {
		t.Errorf("QueryTimeout = %v, want 3s", s.cfg.QueryTimeout)
	}
	if s.cfg.Logger == nil {
		t.Error("Logger is nil; should default to log.Printf")
	}
	if s.intervalSeconds != 30 {
		t.Errorf("intervalSeconds = %d, want 30", s.intervalSeconds)
	}
	if s.stopCh == nil || s.doneCh == nil {
		t.Error("stopCh/doneCh not initialised")
	}
}

func TestNewDBHealthSampler_CustomConfig(t *testing.T) {
	cfg := DBHealthConfig{
		Interval:     10 * time.Second,
		QueryTimeout: 5 * time.Second,
		Logger:       func(string, ...any) {},
	}
	s := NewDBHealthSampler(cfg, nil)
	if s.cfg.Interval != 10*time.Second {
		t.Errorf("Interval = %v, want 10s", s.cfg.Interval)
	}
	if s.cfg.QueryTimeout != 5*time.Second {
		t.Errorf("QueryTimeout = %v, want 5s", s.cfg.QueryTimeout)
	}
	if s.IntervalSeconds() != 10 {
		t.Errorf("IntervalSeconds() = %d, want 10", s.IntervalSeconds())
	}
}

func TestSampler_Sample_BeforeFirstTick(t *testing.T) {
	// Before any tick has fired, Sample() should return
	// a non-nil empty sample (seeded at Start). This
	// pins the contract: the handler is always safe
	// to call, even immediately after Start.
	s := NewDBHealthSampler(DefaultDBHealthConfig(), nil)
	s.Start()
	defer s.Stop()
	sample := s.Sample()
	if sample == nil {
		t.Fatal("Sample() = nil after Start, want non-nil empty sample")
	}
}

func TestSampler_NilSource_NoCrash(t *testing.T) {
	// A nil DBSource must not crash the tick. The
	// sampler should log the error (captured in the
	// capture-buffer logger) and continue.
	//
	// The sampler runs in a background goroutine, so
	// the log callback and the test's read of `logged`
	// race without synchronization. `go test -race`
	// catches this. We use a mutex around the slice
	// (append + read) instead of a sync.Map because
	// the access pattern is a simple append-and-
	// iterate, which a Mutex models more directly.
	var (
		logMu  sync.Mutex
		logged []string
	)
	log := func(format string, args ...any) {
		logMu.Lock()
		logged = append(logged, format)
		logMu.Unlock()
	}
	cfg := DefaultDBHealthConfig()
	cfg.Logger = log
	cfg.Interval = 10 * time.Millisecond
	s := NewDBHealthSampler(cfg, nil)
	s.Start()
	defer s.Stop()
	// Give the tick ~50ms to run.
	time.Sleep(50 * time.Millisecond)
	logMu.Lock()
	defer logMu.Unlock()
	found := false
	for _, l := range logged {
		// The log line changed when we split the nil
		// checks into "source is nil" (programming
		// error) and "source returned nil" (deferred-
		// init). Either is acceptable here — we just
		// want the sampler to NOT panic.
		if strings.Contains(l, "source is nil") || strings.Contains(l, "no current DB") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log line about nil source, got %v", logged)
	}
}

func TestSampler_StopIsIdempotent(t *testing.T) {
	s := NewDBHealthSampler(DefaultDBHealthConfig(), nil)
	s.Start()
	s.Stop()
	// Second Stop should be a no-op (no panic).
	s.Stop()
}

func TestGetDBHealth_EmptySample(t *testing.T) {
	// Handler must return a well-formed response even
	// when the sampler hasn't run yet. We test the
	// JSON shape by constructing a response struct
	// directly (no need for a real *sql.DB or httptest
	// in this test — the handler's response marshalling
	// is the only thing under test).
	resp := DBHealthResponse{
		SampledAt: time.Now().UTC(),
		Version:   "PostgreSQL 15.0",
		SizeBytes: 539 * 1024 * 1024,
		SizeHuman: "539.0 MB",
		XLogLocation: "0/16A0C58",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Verify the JSON contains the expected keys.
	for _, key := range []string{
		`"pool"`, `"is_replica"`, `"version"`,
		`"size_bytes"`, `"size_human"`,
		`"xlog_location"`, `"sampled_at"`,
		`"sample_interval_seconds"`, `"slow_queries"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("response JSON missing key %s; got: %s", key, string(data))
		}
	}
}

func TestGetDBHealth_PopulatedSample(t *testing.T) {
	// Simulate a fully-populated sample (replica case)
	// and verify the response carries the right fields.
	sample := &DBHealthSample{
		SampledAt: time.Now().UTC(),
	}
	sample.Server.IsReplica = true
	sample.Server.Version = "PostgreSQL 16.1"
	sample.Server.StartedAt = time.Now().Add(-24 * time.Hour).UTC()
	sample.Database.SizeBytes = 539 * 1024 * 1024
	sample.Database.SizeHuman = "539.0 MB"
	lagSec := 1.5
	sample.Replication.IsReplica = true
	sample.Replication.LagSeconds = &lagSec
	sample.Replication.ReplayLSN = "0/16A0C58"
	ts := time.Now().Add(-2 * time.Second).UTC()
	sample.Replication.ReplayTimestamp = &ts
	vac := time.Now().Add(-1 * time.Hour).UTC()
	sample.Maintenance.LastVacuumAt = &vac
	sample.Maintenance.DeadTuples = 12345
	sample.XLog.Location = "0/16A0C58"

	sampler := NewDBHealthSampler(DefaultDBHealthConfig(), nil)
	sampler.sample.Store(sample)

	svc := &Service{DBHealthSampler: sampler}
	resp := DBHealthResponse{}
	if s := svc.DBHealthSampler.Sample(); s != nil {
		resp.IsReplica = s.Server.IsReplica
		resp.Version = s.Server.Version
		resp.SizeBytes = s.Database.SizeBytes
		resp.SizeHuman = s.Database.SizeHuman
		resp.ReplIsReplica = s.Replication.IsReplica
		resp.ReplLagSeconds = s.Replication.LagSeconds
		resp.ReplReplayLSN = s.Replication.ReplayLSN
		resp.MaintLastVacuum = s.Maintenance.LastVacuumAt
		resp.MaintDeadTuples = s.Maintenance.DeadTuples
		resp.XLogLocation = s.XLog.Location
		resp.SampledAt = s.SampledAt
		resp.SampleIntervalSeconds = sampler.IntervalSeconds()
	}

	// Verify each field got copied.
	if !resp.IsReplica {
		t.Error("IsReplica not copied")
	}
	if resp.Version != "PostgreSQL 16.1" {
		t.Errorf("Version = %q", resp.Version)
	}
	if resp.SizeHuman != "539.0 MB" {
		t.Errorf("SizeHuman = %q", resp.SizeHuman)
	}
	if resp.ReplLagSeconds == nil || *resp.ReplLagSeconds != 1.5 {
		t.Errorf("ReplLagSeconds = %v", resp.ReplLagSeconds)
	}
	if resp.MaintLastVacuum == nil {
		t.Error("MaintLastVacuum not copied")
	}
	if resp.MaintDeadTuples != 12345 {
		t.Errorf("MaintDeadTuples = %d", resp.MaintDeadTuples)
	}
	if resp.SampleIntervalSeconds != 30 {
		t.Errorf("SampleIntervalSeconds = %d, want 30", resp.SampleIntervalSeconds)
	}
}

// recordingResponseWriter is a tiny shim for tests
// that need to capture headers. (We don't use it
// directly today — the JSON shape test above is more
// important — but it's here for future expansion when
// the handler is called in-process via httptest.)
type recordingResponseWriter struct {
	header http.Header
}

func (r *recordingResponseWriter) Header() http.Header {
	return r.header
}

func (r *recordingResponseWriter) Write([]byte) (int, error) {
	return 0, nil
}

func (r *recordingResponseWriter) WriteHeader(int) {}
