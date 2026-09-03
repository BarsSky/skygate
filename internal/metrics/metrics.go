// Package metrics is the v1.5.0+ / B226 in-house
// Prometheus text-format exporter for skygate.
//
// Why in-house and not prometheus/client_golang?
//   - skygate is a focused, minimal-deps project.
//     Adding the client_golang transitive closure
//     (~10 modules) just for a /metrics endpoint
//     is overkill.
//   - The text exposition format is well-defined
//     and the encoding is ~50 lines (see
//     WriteText below).
//   - We expose ~12 metrics, all gauges or
//     counters, no histograms, summaries, or
//     exemplars — the full client_golang surface
//     is unnecessary.
//
// Surface
// -------
// - Registry (singleton via Default()) — holds all
//   registered metrics
// - Counter / CounterVec — monotonic, can increment
// - Gauge / GaugeVec — settable, can go up or down
// - Handler() http.Handler — returns the renderer
//   for /metrics (text/plain; version=0.0.4)
// - All metrics live in memory (no disk persistence,
//   no push gateway).
//
// Concurrency
// -----------
// - All metric mutations (Inc, Set, Add) are guarded
//   by a per-metric sync.RWMutex.
// - The renderer takes a read lock per metric.
// - No global lock — each metric is independent.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// MetricType is the Prometheus metric type. B226
// ships only counter + gauge. Histogram / summary
// are intentionally out of scope.
type MetricType string

const (
	TypeCounter MetricType = "counter"
	TypeGauge   MetricType = "gauge"
)

// Metric is the common interface implemented by
// Counter, Gauge, CounterVec, GaugeVec. The
// registry iterates registered metrics and calls
// WriteText on each.
type Metric interface {
	// Name is the Prometheus metric name (snake_case,
	// e.g. "skygate_cluster_nodes_total").
	Name() string
	// Help is the human-readable description (the
	// "# HELP <name> <help>" line in textfmt).
	Help() string
	// Type returns the metric type (counter, gauge).
	Type() MetricType
	// WriteText writes the metric's series in
	// text-format to w. One metric can produce
	// multiple series (the *_vec variants).
	WriteText(w io.Writer)
}

// Registry holds all the metrics. The default
// registry is a process-wide singleton (so
// anywhere in the codebase can call
// `metrics.NewCounter("foo", "help")` without
// having to thread a *Registry through every
// signature).
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]Metric
}

var (
	defaultRegistry     *Registry
	defaultRegistryOnce sync.Once
)

// Default returns the process-wide registry. Safe
// to call from any goroutine; the registry is
// created on the first call.
func Default() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = &Registry{
			metrics: make(map[string]Metric),
		}
	})
	return defaultRegistry
}

// Register adds a metric to the registry. Returns
// an error if a metric with the same name is
// already registered (Prometheus convention —
// metric names are unique per process).
func (r *Registry) Register(m Metric) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.metrics[m.Name()]; ok {
		return fmt.Errorf("metrics: %q already registered", m.Name())
	}
	r.metrics[m.Name()] = m
	return nil
}

// MustRegister is Register + panic on error. Use
// for top-level metric declarations where the
// name is a compile-time constant.
func (r *Registry) MustRegister(m Metric) {
	if err := r.Register(m); err != nil {
		panic(err)
	}
}

// MustDefault is shorthand for Default().MustRegister.
func MustDefault(m Metric) { Default().MustRegister(m) }

// Handler returns an http.Handler that serves the
// registry in Prometheus text-format (text/plain;
// version=0.0.4). Use:
//   mux.Handle("/metrics", metrics.Default().Handler())
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.WriteText(w)
	})
}

// WriteText renders the registry in text-format
// to w. Metrics are sorted by name for stable
// output (helps the test snapshot + reduces diff
// churn in Prometheus scrapes).
func (r *Registry) WriteText(w io.Writer) {
	r.mu.RLock()
	names := make([]string, 0, len(r.metrics))
	for n := range r.metrics {
		names = append(names, n)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	for _, n := range names {
		r.mu.RLock()
		m := r.metrics[n]
		r.mu.RUnlock()
		m.WriteText(w)
	}
}

// ----- Counter / Gauge (no labels) -----

// Counter is a monotonically-increasing scalar.
// Use for "total X" metrics (e.g. audit_log
// rows by action).
type Counter struct {
	mu    sync.RWMutex
	name  string
	help  string
	value float64
}

// NewCounter creates + registers a Counter with the
// default registry. Panics on duplicate name.
func NewCounter(name, help string) *Counter {
	c := &Counter{name: name, help: help}
	MustDefault(c)
	return c
}

// Inc adds 1 to the counter.
func (c *Counter) Inc() { c.Add(1) }

// Add adds v to the counter (negative values are
// silently ignored — counters are monotonic).
func (c *Counter) Add(v float64) {
	if v < 0 {
		return
	}
	c.mu.Lock()
	c.value += v
	c.mu.Unlock()
}

// Name / Help / Type / WriteText satisfy the
// Metric interface.
func (c *Counter) Name() string       { return c.name }
func (c *Counter) Help() string       { return c.help }
func (c *Counter) Type() MetricType   { return TypeCounter }
func (c *Counter) WriteText(w io.Writer) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	fmt.Fprintf(w, "# HELP %s %s\n", c.name, c.help)
	fmt.Fprintf(w, "# TYPE %s %s\n", c.name, c.Type())
	fmt.Fprintf(w, "%s %s\n", c.name, formatFloat(c.value))
}

// ----- CounterVec / GaugeVec (labels) -----

// Gauge is a settable scalar (can go up or down).
// Use for "current state" metrics (e.g. node count
// by state, DB pool connections, primary flag).
type Gauge struct {
	mu    sync.RWMutex
	name  string
	help  string
	value float64
}

// NewGauge creates + registers a Gauge.
func NewGauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	MustDefault(g)
	return g
}

// Set replaces the gauge value.
func (g *Gauge) Set(v float64) {
	g.mu.Lock()
	g.value = v
	g.mu.Unlock()
}

// Inc / Dec add / subtract 1.
func (g *Gauge) Inc() { g.Add(1) }
func (g *Gauge) Dec() { g.Add(-1) }

// Add adds v (can be negative).
func (g *Gauge) Add(v float64) {
	g.mu.Lock()
	g.value += v
	g.mu.Unlock()
}

// Value returns the current value (atomic snapshot).
func (g *Gauge) Value() float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.value
}

// Name / Help / Type / WriteText satisfy Metric.
func (g *Gauge) Name() string     { return g.name }
func (g *Gauge) Help() string     { return g.help }
func (g *Gauge) Type() MetricType { return TypeGauge }
func (g *Gauge) WriteText(w io.Writer) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	fmt.Fprintf(w, "# HELP %s %s\n", g.name, g.help)
	fmt.Fprintf(w, "# TYPE %s %s\n", g.name, g.Type())
	fmt.Fprintf(w, "%s %s\n", g.name, formatFloat(g.value))
}

// GaugeVec is a labelled gauge (e.g. cluster node
// count per state). Use for cardinality-bounded
// label sets (cluster="X", state="ready|draining|...").
type GaugeVec struct {
	mu      sync.RWMutex
	name    string
	help    string
	labels  []string
	series  map[string]*GaugeVecEntry // labelValues joined by "\x00" → entry
}

// NewGaugeVec creates + registers a labelled gauge.
// `labels` is the label names (in deterministic
// order — the same order must be used for all
// Inc/Set calls on this metric).
func NewGaugeVec(name, help string, labels []string) *GaugeVec {
	gv := &GaugeVec{
		name:   name,
		help:   help,
		labels: labels,
		series: make(map[string]*GaugeVecEntry),
	}
	MustDefault(gv)
	return gv
}

// WithLabelValues returns the series for the given
// label values. The series is created on first
// access. The label values are joined by NUL bytes
// to form the map key (a label value containing a
// comma or quote is still safe).
func (gv *GaugeVec) WithLabelValues(vals ...string) *GaugeVecEntry {
	if len(vals) != len(gv.labels) {
		panic(fmt.Sprintf("metrics: %s WithLabelValues got %d vals, want %d", gv.name, len(vals), len(gv.labels)))
	}
	key := strings.Join(vals, "\x00")
	gv.mu.Lock()
	defer gv.mu.Unlock()
	e, ok := gv.series[key]
	if !ok {
		e = &GaugeVecEntry{labelValues: vals}
		gv.series[key] = e
	}
	return e
}

// Reset removes all series from the GaugeVec.
// Used by the B226 collector at the start of each
// tick so a state that disappeared in the previous
// tick (e.g. a node transitioned pending → ready)
// doesn't carry over a stale non-zero value.
func (gv *GaugeVec) Reset() {
	gv.mu.Lock()
	defer gv.mu.Unlock()
	gv.series = make(map[string]*GaugeVecEntry)
}

// GaugeVecEntry is a single series inside a
// GaugeVec (one per label-value tuple). The same
// pointer is returned for repeated calls with the
// same labels (so updates from multiple sites land
// in the same series).
type GaugeVecEntry struct {
	labelValues []string
	value        float64
}

func (e *GaugeVecEntry) Set(v float64)  { e.value = v }
func (e *GaugeVecEntry) Add(v float64)  { e.value += v }
func (e *GaugeVecEntry) Inc()           { e.Add(1) }
func (e *GaugeVecEntry) Dec()           { e.Add(-1) }
func (e *GaugeVecEntry) Value() float64 { return e.value }

// Name / Help / Type / WriteText satisfy Metric.
func (gv *GaugeVec) Name() string     { return gv.name }
func (gv *GaugeVec) Help() string     { return gv.help }
func (gv *GaugeVec) Type() MetricType { return TypeGauge }
func (gv *GaugeVec) WriteText(w io.Writer) {
	gv.mu.RLock()
	defer gv.mu.RUnlock()
	fmt.Fprintf(w, "# HELP %s %s\n", gv.name, gv.help)
	fmt.Fprintf(w, "# TYPE %s %s\n", gv.name, gv.Type())
	// Sort series by label values for stable output.
	keys := make([]string, 0, len(gv.series))
	for k := range gv.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := gv.series[k]
		fmt.Fprintf(w, "%s%s %s\n", gv.name, formatLabels(gv.labels, s.labelValues), formatFloat(s.value))
	}
}

// CounterVec is a labelled counter (e.g. audit_log
// rows by action + cluster).
type CounterVec struct {
	mu      sync.RWMutex
	name    string
	help    string
	labels  []string
	series  map[string]*CounterVecEntry
}

func NewCounterVec(name, help string, labels []string) *CounterVec {
	cv := &CounterVec{
		name:   name,
		help:   help,
		labels: labels,
		series: make(map[string]*CounterVecEntry),
	}
	MustDefault(cv)
	return cv
}

func (cv *CounterVec) WithLabelValues(vals ...string) *CounterVecEntry {
	if len(vals) != len(cv.labels) {
		panic(fmt.Sprintf("metrics: %s WithLabelValues got %d vals, want %d", cv.name, len(vals), len(cv.labels)))
	}
	key := strings.Join(vals, "\x00")
	cv.mu.Lock()
	defer cv.mu.Unlock()
	e, ok := cv.series[key]
	if !ok {
		e = &CounterVecEntry{labelValues: vals}
		cv.series[key] = e
	}
	return e
}

// CounterVecEntry is a single series in a
// CounterVec (one per label-value tuple).
type CounterVecEntry struct {
	labelValues []string
	value        float64
}

func (e *CounterVecEntry) Inc() { e.value++ }
func (e *CounterVecEntry) Add(v float64) {
	if v < 0 {
		return
	}
	e.value += v
}
func (e *CounterVecEntry) Value() float64 { return e.value }

func (cv *CounterVec) Name() string     { return cv.name }
func (cv *CounterVec) Help() string     { return cv.help }
func (cv *CounterVec) Type() MetricType { return TypeCounter }
func (cv *CounterVec) WriteText(w io.Writer) {
	cv.mu.RLock()
	defer cv.mu.RUnlock()
	fmt.Fprintf(w, "# HELP %s %s\n", cv.name, cv.help)
	fmt.Fprintf(w, "# TYPE %s %s\n", cv.name, cv.Type())
	keys := make([]string, 0, len(cv.series))
	for k := range cv.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := cv.series[k]
		fmt.Fprintf(w, "%s%s %s\n", cv.name, formatLabels(cv.labels, s.labelValues), formatFloat(s.value))
	}
}

// formatLabels renders a Prometheus label set:
//   {key1="val1",key2="val2"}
// Empty list → empty string (no braces).
// Label values are escaped: \ → \\, " → \", newline → \n.
func formatLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf(`%s="%s"`, n, escapeLabelValue(values[i]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabelValue(v string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
	)
	return r.Replace(v)
}

// formatFloat renders a float in Prometheus format:
//   - "1", "1.5", "1e9" are valid
//   - "NaN", "+Inf", "-Inf" are valid (B226 doesn't
//     produce these — division-by-zero is not a
//     concern in any of the current samplers)
//   - integers are rendered without decimals
//   - 0.0 must render as "0" (not "")
func formatFloat(v float64) string {
	if v == 0 {
		return "0"
	}
	return strings.TrimRight(
		strings.TrimRight(
			fmt.Sprintf("%g", v),
			"0"), ".")
}
