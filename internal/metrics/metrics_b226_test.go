// v1.5.0+ / B226 — unit tests for the in-house
// Prometheus text-format exporter. Tests pin:
//   1. Counter / Gauge monotonic semantics
//      (Counter rejects negative Add, Gauge accepts
//      it; Counter is monotonic, Gauge is not)
//   2. WithLabelValues returns the same series for
//      the same labels + creates new series for new
//      labels
//   3. Registry de-duplicates metric names
//      (Register returns an error on duplicate)
//   4. textfmt output matches the expected format
//      (# HELP, # TYPE, metric{labels} value)
//   5. Label values are properly escaped
//      (\\, \", newline)
//   6. /metrics handler returns text/plain;
//      version=0.0.4

package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCounter_Basic: Counter is monotonically
// increasing; Add with negative is silently ignored
// (Prometheus convention).
func TestCounter_Basic(t *testing.T) {
	c := NewCounter("test_counter_basic", "test counter")
	c.Inc()
	c.Inc()
	c.Add(5)
	if v := c.value; v != 7 {
		t.Errorf("value = %v, want 7", v)
	}
	c.Add(-1) // ignored (counter is monotonic)
	if v := c.value; v != 7 {
		t.Errorf("after negative Add: value = %v, want 7 (counter is monotonic)", v)
	}
}

// TestGauge_Basic: Gauge is settable, can go up
// or down.
func TestGauge_Basic(t *testing.T) {
	g := NewGauge("test_gauge_basic", "test gauge")
	g.Set(10)
	g.Inc()
	g.Dec()
	g.Add(-3)
	if v := g.Value(); v != 7 {
		t.Errorf("Value = %v, want 7", v)
	}
}

// TestGaugeVec_WithLabelValues: WithLabelValues
// returns the same series for the same labels.
func TestGaugeVec_WithLabelValues(t *testing.T) {
	gv := NewGaugeVec("test_gauge_vec", "test vec", []string{"cluster", "state"})
	s1 := gv.WithLabelValues("prod-1", "ready")
	s2 := gv.WithLabelValues("prod-1", "ready")
	if s1 != s2 {
		t.Errorf("WithLabelValues returned different series for the same labels")
	}
	s1.Set(3)
	if v := s2.Value(); v != 3 {
		t.Errorf("shared series: s2.Value = %v, want 3", v)
	}
	// Different labels = different series.
	s3 := gv.WithLabelValues("prod-1", "draining")
	if s3 == s1 {
		t.Error("WithLabelValues returned the same series for different labels")
	}
	if v := s3.Value(); v != 0 {
		t.Errorf("new series: s3.Value = %v, want 0 (default)", v)
	}
}

// TestRegistry_DuplicateName: Register returns
// an error on duplicate metric name (Prometheus
// convention — names are unique per process).
func TestRegistry_DuplicateName(t *testing.T) {
	r := &Registry{metrics: make(map[string]Metric)}
	// Construct two metrics DIRECTLY (bypassing
	// NewCounter, which would auto-register and
	// panic on the second call).
	c1 := &Counter{name: "test_dup", help: "first"}
	c2 := &Counter{name: "test_dup", help: "second"}
	if err := r.Register(c1); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(c2); err == nil {
		t.Error("expected error on duplicate Register, got nil")
	}
}

// TestTextFormat: pins the Prometheus text format
// output for Counter + Gauge + GaugeVec.
func TestTextFormat(t *testing.T) {
	c := NewCounter("skygate_test_counter", "test counter help")
	c.Inc()
	c.Add(2)

	g := NewGauge("skygate_test_gauge", "test gauge help")
	g.Set(42)

	gv := NewGaugeVec("skygate_test_gauge_vec", "test vec help", []string{"cluster", "state"})
	gv.WithLabelValues("prod-1", "ready").Set(3)
	gv.WithLabelValues("prod-1", "draining").Set(1)

	var buf strings.Builder
	c.WriteText(&buf)
	g.WriteText(&buf)
	gv.WriteText(&buf)
	out := buf.String()

	for _, want := range []string{
		"# HELP skygate_test_counter test counter help\n",
		"# TYPE skygate_test_counter counter\n",
		"skygate_test_counter 3\n",

		"# HELP skygate_test_gauge test gauge help\n",
		"# TYPE skygate_test_gauge gauge\n",
		"skygate_test_gauge 42\n",

		"# HELP skygate_test_gauge_vec test vec help\n",
		"# TYPE skygate_test_gauge_vec gauge\n",
		`skygate_test_gauge_vec{cluster="prod-1",state="ready"} 3` + "\n",
		`skygate_test_gauge_vec{cluster="prod-1",state="draining"} 1` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("textfmt output missing %q\n  full output:\n%s", want, out)
		}
	}
}

// TestTextFormat_LabelEscaping: backslash, quote,
// and newline are escaped per the Prometheus
// spec (\ → \\, " → \", newline → \n).
func TestTextFormat_LabelEscaping(t *testing.T) {
	got := formatLabels([]string{"k"}, []string{`a"b\c` + "\n" + "d"})
	want := `{k="a\"b\\c\nd"}`
	if got != want {
		t.Errorf("escape = %q, want %q", got, want)
	}
}

// TestHandler_HTTP: the /metrics handler returns
// text/plain; version=0.0.4 (the canonical
// Prometheus content-type).
func TestHandler_HTTP(t *testing.T) {
	// Register a metric in a fresh registry for
	// isolation.
	g := NewGauge("skygate_test_handler_gauge", "test")
	g.Set(7)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Default().Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/plain; version=0.0.4; charset=utf-8")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "skygate_test_handler_gauge 7") {
		t.Errorf("handler body missing metric value:\n%s", string(body))
	}
}

// TestHandler_StableOrder: WriteText sorts metric
// names alphabetically (helps test snapshot +
// reduces diff churn in Prometheus scrapes).
func TestHandler_StableOrder(t *testing.T) {
	// Register in non-alphabetical order.
	NewGauge("zzz_last", "")
	NewGauge("aaa_first", "")
	NewGauge("mmm_middle", "")
	var buf strings.Builder
	Default().WriteText(&buf)
	out := buf.String()
	iAaa := strings.Index(out, "aaa_first")
	iMmm := strings.Index(out, "mmm_middle")
	iZzz := strings.Index(out, "zzz_last")
	if !(iAaa < iMmm && iMmm < iZzz) {
		t.Errorf("metrics NOT in alphabetical order: aaa=%d, mmm=%d, zzz=%d\n%s", iAaa, iMmm, iZzz, out)
	}
}
