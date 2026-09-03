// v1.5.0+ / B225.2 — unit tests for the watchdog's
// PG health transition detector (Phase 4.4
// follow-up). The detector tracks consecutive
// cluster_database read failures and fires a
// Telegram alert on the
// "healthy → threshold-failures" edge
// (crossing ReadFailureThreshold) + a
// "threshold-failures → healthy" edge (recovery).
//
// The tests directly drive detectReadFailureTransition
// and detectReadSuccessTransition (the production
// tick() method is exercised by scripts/dbswap_check.sh
// on the live agent).

package watchdog

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// recordingSink satisfies NotifierSink. Captures
// every SendAlert call for assertion.
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

// makeWatchdog builds a DBSwap with a recording
// notifier + a low ReadFailureThreshold (3) so
// the test doesn't need to spam many ticks.
func makeWatchdog() (*DBSwap, *recordingSink) {
	rec := &recordingSink{}
	w := &DBSwap{
		cfg: Config{
			Interval:             5 * 1e9, // 5s (1e9 ns)
			PingTimeout:          3 * 1e9, // 3s
			Logger:               func(string, ...any) {},
			Notifier:             rec,
			ReadFailureThreshold: 3,
			ClusterID:            "skygate-staging",
		},
	}
	return w, rec
}

// TestReadFailureTransition_FirstTickIsBaseline: the
// first tick (whether success or failure) is the
// baseline. A freshly-started skygate shouldn't
// fire a spurious "PG DEGRADED" alert on the first
// sample, even if the first read fails.
func TestReadFailureTransition_FirstTickIsBaseline(t *testing.T) {
	w, rec := makeWatchdog()
	w.detectReadFailureTransition(errors.New("connection refused"))
	if n := rec.count(); n != 0 {
		t.Errorf("first tick fired %d alerts, want 0 (first observation is the baseline)", n)
	}
	if !w.hasFirstTick {
		t.Error("hasFirstTick should be true after first tick")
	}
	if c := w.consecutiveReadFailures; c != 1 {
		t.Errorf("consecutiveReadFailures = %d, want 1", c)
	}
}

// TestReadFailureTransition_ThresholdCross: the
// alert fires when the counter CROSSES the threshold
// (i.e. becomes 3 with threshold=3, not on the 2nd
// failure, not on the 4th).
func TestReadFailureTransition_ThresholdCross(t *testing.T) {
	w, rec := makeWatchdog()
	// Tick 1: failure (counter 1) — baseline, no alert.
	w.detectReadFailureTransition(errors.New("e1"))
	if n := rec.count(); n != 0 {
		t.Errorf("tick 1: alerts = %d, want 0 (baseline)", n)
	}
	// Tick 2: failure (counter 2) — no alert (below threshold).
	w.detectReadFailureTransition(errors.New("e2"))
	if n := rec.count(); n != 0 {
		t.Errorf("tick 2: alerts = %d, want 0 (counter=2 < threshold=3)", n)
	}
	// Tick 3: failure (counter 3) — ALERT (crosses threshold).
	w.detectReadFailureTransition(errors.New("e3"))
	if n := rec.count(); n != 1 {
		t.Fatalf("tick 3: alerts = %d, want 1 (counter crossed threshold)", n)
	}
	// Tick 4: failure (counter 4) — no alert (already
	// past the edge; one alert per degraded state, not
	// per tick).
	w.detectReadFailureTransition(errors.New("e4"))
	if n := rec.count(); n != 1 {
		t.Errorf("tick 4: alerts = %d, want 1 (no re-alert while degraded)", n)
	}
}

// TestReadFailureTransition_AlertText: pins the
// ❌ "PG health DEGRADED" alert text format.
func TestReadFailureTransition_AlertText(t *testing.T) {
	w, rec := makeWatchdog()
	w.detectReadFailureTransition(errors.New("first"))
	w.detectReadFailureTransition(errors.New("second"))
	w.detectReadFailureTransition(errors.New("third crosses threshold"))

	got, _ := rec.first()
	for _, want := range []string{
		"❌", "PG health DEGRADED", "cluster: skygate-staging",
		"read failures: 3", "last error: third crosses threshold",
		"threshold: 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("degraded alert missing %q\n  text: %q", want, got)
		}
	}
}

// TestReadSuccessTransition_Recovery: after
// consecutive failures crosses the threshold,
// the next successful read fires the
// ✅ "PG health recovered" alert.
func TestReadSuccessTransition_Recovery(t *testing.T) {
	w, rec := makeWatchdog()
	// Drive 3 failures (alert fires on tick 3).
	w.detectReadFailureTransition(errors.New("e1"))
	w.detectReadFailureTransition(errors.New("e2"))
	w.detectReadFailureTransition(errors.New("e3"))
	// 1 alert so far.
	if n := rec.count(); n != 1 {
		t.Fatalf("after 3 failures: alerts = %d, want 1", n)
	}
	// Recovery tick.
	w.detectReadSuccessTransition()
	if n := rec.count(); n != 2 {
		t.Fatalf("after recovery: alerts = %d, want 2", n)
	}
	got, _ := rec.last() // see helper below
	for _, want := range []string{
		"✅", "PG health recovered", "cluster: skygate-staging",
		"previous consecutive failures: 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recovered alert missing %q\n  text: %q", want, got)
		}
	}
	// Counter reset to 0.
	if c := w.consecutiveReadFailures; c != 0 {
		t.Errorf("consecutiveReadFailures after recovery = %d, want 0", c)
	}
}

// TestReadSuccessTransition_NoAlertOnStableSuccess:
// when consecutive failures is 0 (the normal
// state), a successful read is a no-op (no
// false "recovered" alerts).
func TestReadSuccessTransition_NoAlertOnStableSuccess(t *testing.T) {
	w, rec := makeWatchdog()
	// Bootstrap with one success (baseline).
	w.detectReadSuccessTransition()
	// Many more successes.
	for i := 0; i < 10; i++ {
		w.detectReadSuccessTransition()
	}
	if n := rec.count(); n != 0 {
		t.Errorf("alerts = %d after 11 successes (1 baseline + 10 normal), want 0", n)
	}
}

// TestTransition_BelowThresholdNoAlert: a few
// failures below the threshold should NOT fire.
// Pins the "3 consecutive" rule.
func TestTransition_BelowThresholdNoAlert(t *testing.T) {
	w, rec := makeWatchdog()
	w.detectReadFailureTransition(errors.New("e1"))
	w.detectReadFailureTransition(errors.New("e2"))
	// Below threshold (3): no alert.
	if n := rec.count(); n != 0 {
		t.Errorf("after 2 failures (below threshold=3): alerts = %d, want 0", n)
	}
	w.detectReadSuccessTransition() // 1 successful read, counter resets
	if c := w.consecutiveReadFailures; c != 0 {
		t.Errorf("counter = %d, want 0 after success", c)
	}
}

// TestTransition_FlapDoesNotSpam: 3 failures
// → 1 success → 3 failures → 1 success fires
// exactly 2 alerts (one per crossing). This is
// the "flap" scenario — a real network
// instability might cause this; the detector
// must not spam the operator.
func TestTransition_FlapDoesNotSpam(t *testing.T) {
	w, rec := makeWatchdog()
	// Round 1: 3 failures (1 alert).
	w.detectReadFailureTransition(errors.New("e1"))
	w.detectReadFailureTransition(errors.New("e2"))
	w.detectReadFailureTransition(errors.New("e3"))
	// Round 1: 1 success (recovery alert, 2 total).
	w.detectReadSuccessTransition()
	// Round 2: 3 failures (degraded alert, 3 total).
	w.detectReadFailureTransition(errors.New("e1"))
	w.detectReadFailureTransition(errors.New("e2"))
	w.detectReadFailureTransition(errors.New("e3"))
	// Round 2: 1 success (recovery alert, 4 total).
	w.detectReadSuccessTransition()
	if n := rec.count(); n != 4 {
		t.Errorf("flap alerts = %d, want 4 (1 degraded + 1 recovered per round, 2 rounds)", n)
	}
}

// TestTransition_NopSinkIsSilent: with the
// noop sink, the detector tracks state but
// doesn't crash.
func TestTransition_NopSinkIsSilent(t *testing.T) {
	w := &DBSwap{
		cfg: Config{
			Interval:             5 * 1e9,
			PingTimeout:          3 * 1e9,
			Logger:               func(string, ...any) {},
			Notifier:             NoopNotifierSink{},
			ReadFailureThreshold: 3,
			ClusterID:            "skygate-staging",
		},
	}
	w.detectReadFailureTransition(errors.New("e1"))
	w.detectReadFailureTransition(errors.New("e2"))
	w.detectReadFailureTransition(errors.New("e3"))
	// Counter at 3, no alert fired (noop sink).
	if c := w.consecutiveReadFailures; c != 3 {
		t.Errorf("counter = %d, want 3", c)
	}
	w.detectReadSuccessTransition()
	if c := w.consecutiveReadFailures; c != 0 {
		t.Errorf("counter after success = %d, want 0", c)
	}
}

// last returns the last recorded alert text (the
// recovery alert in the "recovery" test).
func (r *recordingSink) last() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.texts) == 0 {
		return "", false
	}
	return r.texts[len(r.texts)-1], true
}
