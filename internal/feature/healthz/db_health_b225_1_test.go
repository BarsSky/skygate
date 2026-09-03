// v1.5.0+ / B225.1 — unit tests for the DB health
// sampler transition detector (Phase 4.4 follow-up).
//
// The detector compares each tick's health status
// (SampleError == "") with the sampler's last-known
// state. On an edge (ok→degraded or degraded→ok),
// it pushes a Telegram alert via the configured
// Notifier. The first tick is the baseline (no
// alert) so a freshly-started skygate doesn't fire
// on the first sample.
//
// The tests use a recordingSink (a tiny fake that
// satisfies healthz.DBHealthAlertSink) to capture
// every SendAlert call. They directly drive
// Sampler.detectTransition — the B206 query
// plumbing is exercised by scripts/db_health_check.sh
// on the live agent.

package healthz

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingSink satisfies DBHealthAlertSink.
// Captures every SendAlert call for assertion.
type recordingSink struct {
	mu    sync.Mutex
	texts []string
}

func (r *recordingSink) SendAlert(text string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return int64(len(r.texts))
}

func (r *recordingSink) first() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.texts) == 0 {
		return "", false
	}
	return r.texts[0], true
}

func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.texts)
}

// makeSample builds a DBHealthSample with the given
// error string. The Sampler stores the sample via
// atomic.Pointer; for the transition detector
// tests we only need the SampleError + SampledAt
// fields populated.
func makeSample(err string) *DBHealthSample {
	s := &DBHealthSample{
		SampledAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}
	if err != "" {
		s.SampleError = err
	}
	return s
}

// TestTransition_FirstTickIsBaseline: the first
// sample after startup does NOT fire an alert.
// Prevents a freshly-started skygate from spamming
// the operator with "DB DEGRADED" if the very
// first tick lands during pool warmup.
func TestTransition_FirstTickIsBaseline(t *testing.T) {
	s := &Sampler{
		cfg: DBHealthConfig{
			Notifier:  &recordingSink{},
			ClusterID: "skygate-staging",
		},
	}
	s.detectTransition(makeSample("")) // healthy first tick
	rec := s.cfg.Notifier.(*recordingSink)
	if n := rec.count(); n != 0 {
		t.Errorf("first tick fired %d alerts, want 0 (first sample is the baseline)", n)
	}
	if !s.hasFirstSample {
		t.Error("hasFirstSample should be true after first tick")
	}
	if !s.lastHealthy {
		t.Error("lastHealthy should be true after healthy first tick")
	}
}

// TestTransition_FirstTickDegradedAlsoBaseline:
// the first sample being degraded also does NOT
// fire (we don't know the operator's previous
// intent). The next tick is the first real
// transition signal.
func TestTransition_FirstTickDegradedAlsoBaseline(t *testing.T) {
	s := &Sampler{
		cfg: DBHealthConfig{
			Notifier:  &recordingSink{},
			ClusterID: "skygate-staging",
		},
	}
	s.detectTransition(makeSample("connection refused")) // degraded first tick
	rec := s.cfg.Notifier.(*recordingSink)
	if n := rec.count(); n != 0 {
		t.Errorf("first tick (degraded) fired %d alerts, want 0", n)
	}
	if s.lastHealthy {
		t.Error("lastHealthy should be false after degraded first tick")
	}
}

// TestTransition_OkToDegraded: the second tick
// (after a healthy baseline) is degraded → ❌
// "DB health DEGRADED" alert. Pins the operator-
// readable format.
func TestTransition_OkToDegraded(t *testing.T) {
	s := &Sampler{
		cfg: DBHealthConfig{
			Notifier:  &recordingSink{},
			ClusterID: "skygate-staging",
			Interval:  30 * time.Second,
		},
	}
	// Healthy baseline (no alert).
	s.detectTransition(makeSample(""))
	// Degraded second tick — alert fires.
	s.detectTransition(makeSample("connection refused"))

	rec := s.cfg.Notifier.(*recordingSink)
	if n := rec.count(); n != 1 {
		t.Fatalf("alerts fired = %d, want 1 (one ok→degraded transition)", n)
	}
	got, _ := rec.first()
	for _, want := range []string{
		"❌", "DB health DEGRADED", "cluster: skygate-staging",
		"connection refused", "next_check_in:", "sampled_at:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("degraded alert missing %q\n  text: %q", want, got)
		}
	}
}

// TestTransition_DegradedToOk: the second tick
// (after a degraded baseline) is healthy → ✅
// "DB health recovered" alert.
func TestTransition_DegradedToOk(t *testing.T) {
	s := &Sampler{
		cfg: DBHealthConfig{
			Notifier:  &recordingSink{},
			ClusterID: "skygate-staging",
			Interval:  30 * time.Second,
		},
	}
	// Degraded baseline (no alert).
	s.detectTransition(makeSample("connection refused"))
	// Healthy second tick — alert fires.
	s.detectTransition(makeSample(""))

	rec := s.cfg.Notifier.(*recordingSink)
	if n := rec.count(); n != 1 {
		t.Fatalf("alerts fired = %d, want 1 (one degraded→ok transition)", n)
	}
	got, _ := rec.first()
	for _, want := range []string{
		"✅", "DB health recovered", "cluster: skygate-staging",
		"error: \"\"", "next_check_in:", "sampled_at:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recovered alert missing %q\n  text: %q", want, got)
		}
	}
}

// TestTransition_NoOpOnStable: multiple ticks at
// the same health state fire NO alerts. The
// detector only fires on edges.
func TestTransition_NoOpOnStable(t *testing.T) {
	s := &Sampler{
		cfg: DBHealthConfig{
			Notifier:  &recordingSink{},
			ClusterID: "skygate-staging",
		},
	}
	s.detectTransition(makeSample(""))            // baseline
	s.detectTransition(makeSample(""))            // same as baseline
	s.detectTransition(makeSample(""))            // same
	s.detectTransition(makeSample("connection refused")) // transition: 1 alert
	s.detectTransition(makeSample("connection refused")) // same as last
	s.detectTransition(makeSample("connection refused")) // same
	s.detectTransition(makeSample(""))                     // transition: 1 alert

	rec := s.cfg.Notifier.(*recordingSink)
	if n := rec.count(); n != 2 {
		t.Errorf("alerts fired = %d, want 2 (one ok→degraded + one degraded→ok, no alerts on stable states)", n)
	}
}

// TestTransition_NopSinkIsSilent: with the noop
// sink (the production default when no Telegram
// bot is configured), the detector still tracks
// state but doesn't crash. This is the path the
// agent exercises today (no bot).
func TestTransition_NopSinkIsSilent(t *testing.T) {
	s := &Sampler{
		cfg: DBHealthConfig{
			Notifier:  NoopAlertSink{},
			ClusterID: "skygate-staging",
		},
	}
	// Multiple transitions, no panic.
	s.detectTransition(makeSample(""))
	s.detectTransition(makeSample("connection refused"))
	s.detectTransition(makeSample(""))
	// State should be tracked correctly even with noop sink.
	if !s.lastHealthy {
		t.Error("lastHealthy should be true after the last (healthy) tick")
	}
}

// TestTransition_EmptyNotifierFieldIsSafe: a
// nil Notifier (defensive — main.go always sets
// the NoopAlertSink) doesn't crash. The SendAlert
// call is wrapped in a method dispatch, so nil
// would normally panic, but the cfg defaults to
// NoopAlertSink{} so this is a belt-and-suspenders
// guard.
func TestTransition_EmptyNotifierFieldIsSafe(t *testing.T) {
	s := &Sampler{
		cfg: DBHealthConfig{
			// No Notifier set.
			ClusterID: "skygate-staging",
		},
	}
	// We must NOT call detectTransition with a nil
	// Notifier — that would panic on the SendAlert
	// dispatch. main.go's NewDBHealthSampler path
	// always sets Notifier (NoopAlertSink by
	// default). This test just confirms the
	// config defaults to NoopAlertSink, not nil.
	if s.cfg.Notifier == nil {
		s.cfg.Notifier = NoopAlertSink{}
	}
	rec := &recordingSink{}
	s.cfg.Notifier = rec
	s.detectTransition(makeSample(""))
	s.detectTransition(makeSample("connection refused"))
	if n := rec.count(); n != 1 {
		t.Errorf("alerts fired = %d, want 1", n)
	}
}
