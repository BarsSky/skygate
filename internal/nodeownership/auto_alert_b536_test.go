// auto_alert_b536_test.go — B536 (2026-11-28)
//
// The live screen these tests pin (Telegram, 2026-11-28 08:05):
//
//	❌ skygate tag autoupdate FAILED
//	node: 198 (skybars-1)
//	failed tag: tag:dev-skyadmin-skybars-1
//	reason: unknown
//	error: duplicate name: "skybars-1" … shares the base name of "skybars" …
//
// A duplicate-node REPORT must never be rendered as a tag failure again: no "failed tag",
// no "reason: unknown", no increment of the tag-failure metric.
package nodeownership

import (
	"strings"
	"testing"
	"time"

	"skygate/internal/headscale"
	"skygate/internal/metrics"
)

// b536Pair is the live pair: the live suffixed node and the stale base-name node.
func b536Pair() []headscale.NodeView {
	return []headscale.NodeView{
		{
			ID: "55", GivenName: "skybars", Hostname: "skybars", UserName: "daniil",
			Tags: []string{"tag:dev-daniil-skybars"}, Online: false, LastSeen: "2026-11-27T12:16:00Z",
		},
		{
			ID: "198", GivenName: "skybars-1", Hostname: "skybars", UserName: "skyadmin",
			Tags: []string{"tag:dev-skyadmin-skybars-1"}, Online: true, CreatedAt: "2026-11-27T14:41:00Z",
		},
	}
}

func b536Duplicate(t *testing.T) NodeDuplicate {
	t.Helper()
	dups := DetectDuplicates(b536Pair())
	if len(dups) != 1 {
		t.Fatalf("DetectDuplicates = %d pair(s), want exactly 1", len(dups))
	}
	return dups[0]
}

// TestB536_ReportDoesNotWearTheFailureCostume is the whole block: the words the operator
// reads must describe a report.
func TestB536_ReportDoesNotWearTheFailureCostume(t *testing.T) {
	n := &recordingNotifier{}
	sink := NewTagAlertSink(n, nil)
	sink.ReportDuplicate(b536Duplicate(t))

	if n.count() != 1 {
		t.Fatalf("SendAlert calls = %d, want 1", n.count())
	}
	text, _ := n.first()
	for _, forbidden := range []string{
		"tag autoupdate FAILED",
		"failed tag",
		"reason: unknown",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the report still contains %q — that is the failure's costume:\n%s", forbidden, text)
		}
	}
	for _, required := range []string{
		"Дубликат узла",
		"198",
		"55",
		"skybars",
		"пере-регистрация",
		"НЕ ошибка тегирования",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("the report does not mention %q:\n%s", required, text)
		}
	}
}

// TestB536_ReportDoesNotCountAsATagFailure: the metric that means "a tag write was
// rejected" must stay untouched, and the report gets its own series instead. On the live
// host the polluted counter read reason="unknown" for events where no tag write happened.
func TestB536_ReportDoesNotCountAsATagFailure(t *testing.T) {
	d := b536Duplicate(t)
	before := metrics.TagAutoupdateFailuresCounter.
		WithLabelValues(d.NewID, d.NewHost, "unknown").Value()
	dupBefore := metrics.DuplicateNodesCounter.
		WithLabelValues(d.NewID, string(d.Kind)).Value()

	sink := NewTagAlertSink(&recordingNotifier{}, nil)
	sink.ReportDuplicate(d)

	after := metrics.TagAutoupdateFailuresCounter.
		WithLabelValues(d.NewID, d.NewHost, "unknown").Value()
	if after != before {
		t.Errorf("skygate_tag_autoupdate_failures_total moved by %v for a REPORT — the failure metric now lies", after-before)
	}
	dupAfter := metrics.DuplicateNodesCounter.
		WithLabelValues(d.NewID, string(d.Kind)).Value()
	if dupAfter-dupBefore != 1 {
		t.Errorf("skygate_duplicate_nodes_total delta = %v, want 1", dupAfter-dupBefore)
	}
}

// TestB536_ReportIsRateLimitedPerNodeAndKind: the detector runs every 5 minutes, so the
// same sentence must arrive once an hour at most — while a DIFFERENT kind or node still
// gets through (it is different information).
func TestB536_ReportIsRateLimitedPerNodeAndKind(t *testing.T) {
	n := &recordingNotifier{}
	sink := NewTagAlertSink(n, nil)
	now := time.Date(2026, 11, 28, 8, 5, 0, 0, time.UTC)
	sink.Now = func() time.Time { return now }

	d := b536Duplicate(t)
	for i := 0; i < 5; i++ {
		sink.ReportDuplicate(d)
	}
	if n.count() != 1 {
		t.Fatalf("SendAlert calls = %d, want 1 (5 ticks within the hour)", n.count())
	}

	// The window passes → the report repeats (it is still true).
	now = now.Add(rateLimitWindow + time.Minute)
	sink.ReportDuplicate(d)
	if n.count() != 2 {
		t.Fatalf("SendAlert calls = %d after the window, want 2", n.count())
	}

	// A different node is different information and must not be suppressed.
	other := d
	other.NewID = "199"
	other.NewGiven = "laptop-1"
	sink.ReportDuplicate(other)
	if n.count() != 3 {
		t.Fatalf("SendAlert calls = %d, want 3 (another node must not be rate-limited away)", n.count())
	}
}

// TestB536_ReportTextNamesTheEvidence: the "kind" decides what the operator is told to do,
// and the three kinds must not read the same.
func TestB536_ReportTextNamesTheEvidence(t *testing.T) {
	now := time.Date(2026, 11, 28, 8, 5, 0, 0, time.UTC)
	texts := map[DuplicateKind]string{}
	for _, kind := range []DuplicateKind{
		DuplicateSameHostStale, DuplicateSameHostLive, DuplicateDifferentHost,
	} {
		d := b536Duplicate(t)
		d.Kind = kind
		texts[kind] = buildDuplicateText(d, now)
		if !strings.Contains(texts[kind], "признак: ") {
			t.Errorf("%s: the report does not state the evidence:\n%s", kind, texts[kind])
		}
	}
	if texts[DuplicateSameHostStale] == texts[DuplicateDifferentHost] {
		t.Error("a re-registration and a name collision read identically — the kind carries the operator's next action")
	}
	if !strings.Contains(texts[DuplicateSameHostLive], "ОБЕ ноды online") {
		t.Errorf("same_host_live must say both nodes are online:\n%s", texts[DuplicateSameHostLive])
	}
}
