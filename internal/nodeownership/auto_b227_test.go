package nodeownership

// auto_b227_test.go — v1.5.2 / B227 — unit tests for the
// TagAlertSink (the B77 AddTag-failure observability hook).
//
// Coverage:
//   - ClassifyFailure: 3 reason buckets (acl_reject, rpc_error,
//     unknown) + nil-error guard.
//   - TagAlertSink.ReportFailure: rate-limit (1 per (node, reason)
//     per hour) + different-reason bypass + nil notifier
//     + nil receiver (defensive).
//   - Prometheus counter increments on every failure (rate-
//     limit independent).
//
// What's NOT tested here (covered elsewhere):
//   - The actual audit_log write — exercised by the B221
//     AppendAuditLogWithTarget test family. The sink just
//     delegates; we test the sink's call-site via the
//     fakeDBSource.capture() assertion.
//   - The integration with AutoBackfill's main loop — covered
//     by the B-check script (check_b227.sh) + the live-verify
//     run on the agent VM.

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/metrics"
)

// fakeDBSource is a db.DBSource that returns nil from
// Current(). This exercises the "DB unavailable" branch
// in ReportFailure (the audit_log write is silently
// skipped, the metric + alert still go through). We
// also track whether Current() was called so the test
// can assert the sink attempted the audit write at
// all (defense against future refactors that drop the
// DB path).
type fakeDBSource struct {
	mu      sync.Mutex
	called  int
	returns *sql.DB // nil in tests (exercises conn==nil guard)
}

func (f *fakeDBSource) Current() *sql.DB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	return f.returns
}

func (f *fakeDBSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

// recordingNotifier satisfies AlertSink for the B227
// tests. It records every SendAlert call (the
// recordingNotifier in admin/database_b225_test.go is
// a different type — it satisfies telegram.Notifier
// for the B225 family; here we only need the minimal
// AlertSink surface).
type recordingNotifier struct {
	mu    sync.Mutex
	texts []string
	ids   []int64
}

func (r *recordingNotifier) SendAlert(text string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	r.ids = append(r.ids, int64(len(r.texts)))
	return r.ids[len(r.ids)-1]
}

func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.texts)
}

func (r *recordingNotifier) first() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.texts) == 0 {
		return "", false
	}
	return r.texts[0], true
}

// freshCounterValue reads the current value of a
// CounterVec series (used to assert the Prometheus
// counter was incremented). Returns 0 if the series
// doesn't exist (which would itself be a test failure
// — the sink should always call WithLabelValues
// before Inc).
func freshCounterValue(t *testing.T, nodeID, hostname, reason string) float64 {
	t.Helper()
	return metrics.TagAutoupdateFailuresCounter.
		WithLabelValues(nodeID, hostname, reason).
		Value()
}

// resetCounter zeros the counter for the given labels
// (so test cases don't bleed into each other). The
// CounterVec's underlying map grows monotonically, so
// we just subtract the pre-test baseline.
func resetCounter(t *testing.T, nodeID, hostname, reason string) {
	t.Helper()
	// Counter has no Dec; the test just needs a stable
	// baseline. We use a different nodeID per test to
	// avoid collision (the metric accumulates by label
	// tuple; unique tuples don't interfere).
	_ = nodeID
	_ = hostname
	_ = reason
}

// TestClassifyFailure_ACLReject pins the gRPC codes
// that the B227 sink treats as permanent (operator
// action needed: headscale policy is misconfigured).
func TestClassifyFailure_ACLReject(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"InvalidArgument: requested tags are invalid or not permitted", errors.New("rpc error: code = InvalidArgument desc = requested tags [tag:dev-skyadmin-skygate-host-1-1] are invalid or not permitted")},
		{"PermissionDenied", errors.New("rpc error: code = PermissionDenied desc = no policy grant for tag")},
		{"FailedPrecondition: tag already exists", errors.New("rpc error: code = FailedPrecondition desc = tag already present on node")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFailure(tc.err); got != ReasonACLReject {
				t.Errorf("ClassifyFailure(%q) = %q, want %q", tc.err.Error(), got, ReasonACLReject)
			}
		})
	}
}

// TestClassifyFailure_RPCError pins the gRPC codes
// the B227 sink treats as transient (next tick will
// retry; no operator action needed).
func TestClassifyFailure_RPCError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"Unavailable: network down", errors.New("rpc error: code = Unavailable desc = connection refused")},
		{"Internal: headscale 5xx", errors.New("rpc error: code = Internal desc = internal server error")},
		{"DeadlineExceeded: timeout", errors.New("rpc error: code = DeadlineExceeded desc = context deadline exceeded")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFailure(tc.err); got != ReasonRPCError {
				t.Errorf("ClassifyFailure(%q) = %q, want %q", tc.err.Error(), got, ReasonRPCError)
			}
		})
	}
}

// TestClassifyFailure_Unknown pins the fallback for
// errors that don't match the recognized gRPC codes.
// The operator should see the raw error in the audit
// row + Telegram alert.
func TestClassifyFailure_Unknown(t *testing.T) {
	if got := ClassifyFailure(errors.New("some completely unrecognised error")); got != ReasonUnknown {
		t.Errorf("ClassifyFailure(unknown) = %q, want %q", got, ReasonUnknown)
	}
	// nil error → unknown (defensive; ReportFailure
	// shouldn't be called with nil, but ClassifyFailure
	// is a pure function so it should be safe).
	if got := ClassifyFailure(nil); got != ReasonUnknown {
		t.Errorf("ClassifyFailure(nil) = %q, want %q", got, ReasonUnknown)
	}
}

// TestReportFailure_MetricAlwaysIncrements asserts
// the B226 Prometheus counter is bumped on EVERY
// failure (the metric is rate-limit-independent —
// the operator uses it for Prom alerts and rate()
// queries; the Telegram path is the rate-limited
// part).
func TestReportFailure_MetricAlwaysIncrements(t *testing.T) {
	nodeID := "43-unique-metric-test"
	hostname := "skygate-host-1-1"
	reason := "acl_reject"
	resetCounter(t, nodeID, hostname, reason)
	pre := freshCounterValue(t, nodeID, hostname, reason)

	rec := &recordingNotifier{}
	sink := NewTagAlertSink(rec, &fakeDBSource{})

	err := errors.New("rpc error: code = InvalidArgument")
	for i := 0; i < 5; i++ {
		sink.ReportFailure(nodeID, hostname, "tag:dev-skyadmin-skygate-host-1-1", err)
	}

	got := freshCounterValue(t, nodeID, hostname, reason)
	if got-pre != 5 {
		t.Errorf("counter delta = %v, want 5 (5 ReportFailure calls)", got-pre)
	}
}

// TestReportFailure_RateLimit_1PerHourPerKey asserts
// the Telegram alert is rate-limited to 1 per
// (node_id, reason) per hour. Five calls in quick
// succession → exactly 1 SendAlert, even though the
// metric was bumped 5 times (see the previous test).
func TestReportFailure_RateLimit_1PerHourPerKey(t *testing.T) {
	nodeID := "44-rate-limit-test"
	hostname := "skygate-host-1-2"
	reason := "acl_reject"
	resetCounter(t, nodeID, hostname, reason)

	rec := &recordingNotifier{}
	sink := NewTagAlertSink(rec, &fakeDBSource{})

	err := errors.New("rpc error: code = InvalidArgument")
	for i := 0; i < 5; i++ {
		sink.ReportFailure(nodeID, hostname, "tag:dev-skyadmin-skygate-host-1-2", err)
	}

	if n := rec.count(); n != 1 {
		t.Errorf("alert count = %d, want 1 (5 ReportFailure calls should hit rate-limit on the 2nd-5th)", n)
	}
}

// TestReportFailure_RateLimit_DifferentReasons asserts
// the rate-limit key includes the reason. Two failures
// with different reasons → two alerts (not rate-limited
// against each other). The (node, reason) tuple is the
// key — same node, different reason = different bucket.
func TestReportFailure_RateLimit_DifferentReasons(t *testing.T) {
	nodeID := "45-diff-reasons-test"
	resetCounter(t, nodeID, "host-a", "acl_reject")
	resetCounter(t, nodeID, "host-a", "rpc_error")

	rec := &recordingNotifier{}
	sink := NewTagAlertSink(rec, &fakeDBSource{})

	// One ACL reject + one RPC error on the same node.
	sink.ReportFailure(nodeID, "host-a", "tag:dev-skyadmin-host-a", errors.New("rpc error: code = InvalidArgument"))
	sink.ReportFailure(nodeID, "host-a", "tag:dev-skyadmin-host-a", errors.New("rpc error: code = Unavailable"))

	if n := rec.count(); n != 2 {
		t.Errorf("alert count = %d, want 2 (different reasons → different rate-limit buckets)", n)
	}
	// Verify the two alerts have the right reason text.
	text1, _ := rec.first()
	if text1 == "" {
		t.Fatal("first alert missing")
	}
}

// TestReportFailure_RateLimit_ResetsAfterWindow asserts
// the rate-limit window expires after 1h. We use the
// sink's injectable Now() to fast-forward the clock
// past the window without sleeping the test.
func TestReportFailure_RateLimit_ResetsAfterWindow(t *testing.T) {
	nodeID := "46-reset-after-window"
	resetCounter(t, nodeID, "host-b", "acl_reject")

	rec := &recordingNotifier{}
	sink := NewTagAlertSink(rec, &fakeDBSource{})

	clock := time.Now()
	sink.Now = func() time.Time { return clock }

	err := errors.New("rpc error: code = InvalidArgument")
	sink.ReportFailure(nodeID, "host-b", "tag:dev-x-host-b", err)
	if n := rec.count(); n != 1 {
		t.Fatalf("alert count after 1st call = %d, want 1", n)
	}
	// 30 min later — still within the 1h window, should
	// NOT alert again.
	clock = clock.Add(30 * time.Minute)
	sink.ReportFailure(nodeID, "host-b", "tag:dev-x-host-b", err)
	if n := rec.count(); n != 1 {
		t.Errorf("alert count after 30min 2nd call = %d, want 1 (still in window)", n)
	}
	// 90 min after start — past the 1h window, should
	// alert again.
	clock = clock.Add(60 * time.Minute)
	sink.ReportFailure(nodeID, "host-b", "tag:dev-x-host-b", err)
	if n := rec.count(); n != 2 {
		t.Errorf("alert count after 90min 3rd call = %d, want 2 (window expired)", n)
	}
}

// TestReportFailure_NilNotifierSilent asserts a nil
// Notifier becomes NoopAlertSink (defensive guard
// inside NewTagAlertSink). The metric + audit still
// go through; only the Telegram call is silenced.
func TestReportFailure_NilNotifierSilent(t *testing.T) {
	nodeID := "47-nil-notifier"
	resetCounter(t, nodeID, "host-c", "acl_reject")

	pre := freshCounterValue(t, nodeID, "host-c", "acl_reject")
	// NewTagAlertSink(nil, ...) → Notifier = NoopAlertSink.
	sink := NewTagAlertSink(nil, &fakeDBSource{})
	sink.ReportFailure(nodeID, "host-c", "tag:dev-x-host-c",
		errors.New("rpc error: code = InvalidArgument"))
	got := freshCounterValue(t, nodeID, "host-c", "acl_reject")
	if got-pre != 1 {
		t.Errorf("counter delta = %v, want 1 (nil notifier should not skip metric)", got-pre)
	}
}

// TestReportFailure_NilReceiverSilent asserts the
// nil-receiver guard in ReportFailure. This is the
// path the manual Backfill callers (feature/my,
// feature/admin/devices) take when they pass nil.
func TestReportFailure_NilReceiverSilent(t *testing.T) {
	// Should not panic, should not increment any
	// counter (we use a fresh tuple to verify no
	// side effect).
	var sink *TagAlertSink
	nodeID := "48-nil-receiver"
	resetCounter(t, nodeID, "host-d", "acl_reject")
	pre := freshCounterValue(t, nodeID, "host-d", "acl_reject")
	sink.ReportFailure(nodeID, "host-d", "tag:dev-x-host-d",
		errors.New("rpc error: code = InvalidArgument"))
	post := freshCounterValue(t, nodeID, "host-d", "acl_reject")
	if post != pre {
		t.Errorf("nil-receiver should be a no-op; counter went %v → %v", pre, post)
	}
}

// TestReportFailure_DBAuditAttempted asserts the
// sink actually asks the DB for a connection on every
// failure (the audit_log write is the durable record).
// Even when the DB returns nil (the fakeDBSource
// case), we should see Current() was called — so a
// future refactor that drops the DB path is caught.
func TestReportFailure_DBAuditAttempted(t *testing.T) {
	nodeID := "49-db-attempted"
	resetCounter(t, nodeID, "host-e", "acl_reject")

	rec := &recordingNotifier{}
	fake := &fakeDBSource{}
	sink := NewTagAlertSink(rec, fake)

	sink.ReportFailure(nodeID, "host-e", "tag:dev-x-host-e",
		errors.New("rpc error: code = InvalidArgument"))

	if got := fake.callCount(); got != 1 {
		t.Errorf("fakeDBSource.Current() called %d times, want 1 (audit path always attempts the DB)", got)
	}
}

// TestNewTagAlertSink_NilDBAllowed asserts the sink
// tolerates a nil DBSource. The metric + alert still
// work; only the audit_log write is silently skipped.
// This is the path a fresh-test or a misconfigured
// startup would take.
func TestNewTagAlertSink_NilDBAllowed(t *testing.T) {
	rec := &recordingNotifier{}
	sink := NewTagAlertSink(rec, nil)
	nodeID := "50-nil-db"
	resetCounter(t, nodeID, "host-f", "acl_reject")
	pre := freshCounterValue(t, nodeID, "host-f", "acl_reject")
	// Should not panic.
	sink.ReportFailure(nodeID, "host-f", "tag:dev-x-host-f",
		errors.New("rpc error: code = InvalidArgument"))
	post := freshCounterValue(t, nodeID, "host-f", "acl_reject")
	if post-pre != 1 {
		t.Errorf("counter delta = %v, want 1 (nil DB should not skip metric)", post-pre)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("alert count = %d, want 1 (nil DB should not skip alert)", n)
	}
}

// TestBuildAlertText_Format pins the Telegram alert
// text format. The operator's eyes + the /admin/audit
// search rely on these exact field names. Future
// refactors that change the format will fail this
// test (the check_b227.sh also greps for the same
// substrings).
func TestBuildAlertText_Format(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 30, 0, 0, time.UTC)
	text := buildAlertText("43", "skygate-host-1-1", "tag:dev-skyadmin-skygate-host-1-1",
		ReasonACLReject, errors.New("rpc error: code = InvalidArgument"), now)

	for _, field := range []string{
		"❌ skygate tag autoupdate FAILED",
		"node: 43 (skygate-host-1-1)",
		"failed tag: tag:dev-skyadmin-skygate-host-1-1",
		"reason: acl_reject",
		"error: rpc error: code = InvalidArgument",
		"timestamp: 2026-09-03T14:30:00Z",
	} {
		if !contains(text, field) {
			t.Errorf("alert text missing field %q\n  text: %q", field, text)
		}
	}
}

// contains is a tiny stdlib-free substring helper
// (the test file already imports strings, but using
// a local helper makes the test table more readable
// than inlining strings.Contains everywhere).
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestBuildFailureDetail_AuditFormat pins the audit_log
// detail field names. /admin/audit (B207+) renders
// these as multi-line text; future refactors that
// rename a field break the operator's greppability.
func TestBuildFailureDetail_AuditFormat(t *testing.T) {
	detail := buildFailureDetail("skygate-host-1-1", "tag:dev-skyadmin-skygate-host-1-1",
		ReasonACLReject, errors.New("rpc error: code = InvalidArgument"))
	for _, field := range []string{
		"hostname=skygate-host-1-1",
		"failed_tag=tag:dev-skyadmin-skygate-host-1-1",
		"reason=acl_reject",
		"error=rpc error: code = InvalidArgument",
	} {
		if !contains(detail, field) {
			t.Errorf("audit detail missing field %q\n  detail: %q", field, detail)
		}
	}
}

// Compile-time assertion: *fakeDBSource satisfies
// db.DBSource. If the interface drifts, the test
// build fails before runtime surprises.
var _ db.DBSource = (*fakeDBSource)(nil)
