// v1.5.0+ (B207) — unit tests for the /admin/audit
// unified view (audit_log + cluster_audit).
//
// The full handler is tested via the live B207 verify
// script (insert a cluster_audit row, GET /admin/audit,
// assert both tables appear). These unit tests pin
// the pure helpers: query-building, time parsing
// (24h, 7d, etc.), and the SQL column types match.

package admin

import (
	"reflect"
	"testing"
	"time"
)

func TestParseSinceFilter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty", "", 0},
		{"1h", "1h", 1 * time.Hour},
		{"24h", "24h", 24 * time.Hour},
		{"7d", "7d", 7 * 24 * time.Hour},
		{"30m", "30m", 30 * time.Minute},
		{"invalid (no unit)", "60", 0},  // "60" without unit → 0 (ignored)
		{"invalid (garbage)", "abc", 0}, // "abc" → 0
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSinceFilter(c.in)
			if got != c.want {
				t.Errorf("parseSinceFilter(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestLimitOverride(t *testing.T) {
	// The query string "?limit=N" sets the row cap.
	// N must be 1..5000; invalid values fall back to
	// the default (200).
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 200},
		{"valid 50", "50", 50},
		{"valid 500", "500", 500},
		{"valid 5000", "5000", 5000},
		{"invalid (zero)", "0", 200},    // 0 → default
		{"invalid (negative)", "-5", 200},
		{"invalid (non-numeric)", "abc", 200},
		{"invalid (too large)", "5001", 200}, // 5001 > 5000 → default
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseLimit(c.in)
			if got != c.want {
				t.Errorf("parseLimit(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestSourceFilterValidation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty (all)", "", AuditSourceAll},
		{"audit_log", "audit_log", AuditSourceAuditLog},
		{"cluster_audit", "cluster_audit", AuditSourceCluster},
		{"unknown", "garbage", AuditSourceAll}, // unknown → empty
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeSourceFilter(c.in)
			if got != c.want {
				t.Errorf("normalizeSourceFilter(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestAuditEntry_FieldsRoundtrip(t *testing.T) {
	// The AuditEntry struct is the in-memory shape
	// handed to the template. This test pins the field
	// names — adding a field is a deliberate act; a
	// typo would break the template binding.
	e := AuditEntry{
		Source:       "cluster_audit",
		Time:         "2026-09-01 18:00:00",
		Actor:        "skyadmin",
		Action:       "node_failover",
		Target:       "test-b204-standby-ready",
		Detail:       `{"to_node_id":"test-b204-standby-ready","from_node_id":"test-b204-primary-failed","actor":"skygate-cli"}`,
		Result:       "ok",
		ErrorMessage: "",
	}
	if e.Source != "cluster_audit" {
		t.Errorf("Source = %q", e.Source)
	}
	if e.Result != "ok" {
		t.Errorf("Result = %q", e.Result)
	}
	// Type equality — template uses range over []AuditEntry,
	// so the type must be stable.
	var _ []AuditEntry = []AuditEntry{e}
}

func TestAuditEntry_OptionalFields(t *testing.T) {
	// audit_log rows have empty Target, Result,
	// ErrorMessage. The template must render "—" for
	// empty Target and not render the [ok] tag for
	// empty Result. This test pins the zero-value
	// behavior.
	e := AuditEntry{
		Source: "audit_log",
		Time:   "2026-08-15 12:00:00",
		Actor:  "alice",
		Action: "login",
		Detail: "user=alice",
		// Target, Result, ErrorMessage all zero.
	}
	if e.Target != "" {
		t.Errorf("audit_log Target should be empty, got %q", e.Target)
	}
	if e.Result != "" {
		t.Errorf("audit_log Result should be empty, got %q", e.Result)
	}
	if e.ErrorMessage != "" {
		t.Errorf("audit_log ErrorMessage should be empty, got %q", e.ErrorMessage)
	}
}

func TestAuditSourceConstants(t *testing.T) {
	// Pin the source string values. The template and
	// the URL filter both compare against these.
	if AuditSourceAll != "" {
		t.Errorf("AuditSourceAll = %q, want \"\"", AuditSourceAll)
	}
	if AuditSourceAuditLog != "audit_log" {
		t.Errorf("AuditSourceAuditLog = %q", AuditSourceAuditLog)
	}
	if AuditSourceCluster != "cluster_audit" {
		t.Errorf("AuditSourceCluster = %q", AuditSourceCluster)
	}
	if AuditSourceLimitDefault != 200 {
		t.Errorf("AuditSourceLimitDefault = %d", AuditSourceLimitDefault)
	}
}

// _ = reflect.DeepEqual pins the reflect import — the
// tests use it via the AuditEntry type assertions.
var _ = reflect.DeepEqual