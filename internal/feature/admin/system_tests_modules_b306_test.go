// B306 (v1.5.71) — modules are first-class system tests.
//
// The operator asked to extend the system tests so the project's modules can be
// fully controlled. Before this, the test catalogue was static and the modules
// lived only on /admin/modules: nothing in the battery asked "is this module
// working?", and a module in StateError was a badge on another page.
//
// These tests pin the ladder (not installed → SKIP, running+healthy → PASS,
// running+unhealthy → FAIL, error → FAIL with the module's own LastError), the
// generated test names, the run filter, and the inbox routing that gives each
// module its own notification source.
package admin

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/module"
)

// b306ModuleStub implements module.Module with fixed status/health.
type b306ModuleStub struct {
	status module.ModuleStatus
	health module.HealthStatus
}

func (s b306ModuleStub) Name() string                                              { return "stub" }
func (s b306ModuleStub) Init(context.Context, module.ModuleConfig) error           { return nil }
func (s b306ModuleStub) Start(context.Context) error                               { return nil }
func (s b306ModuleStub) Stop(context.Context) error                                { return nil }
func (s b306ModuleStub) Status() module.ModuleStatus                               { return s.status }
func (s b306ModuleStub) Health() module.HealthStatus                               { return s.health }
func (s b306ModuleStub) SubFeatures() []module.SubFeature                          { return nil }
func (s b306ModuleStub) EnableSubFeature(context.Context, string) error            { return nil }
func (s b306ModuleStub) DisableSubFeature(context.Context, string) error           { return nil }
func (s b306ModuleStub) Install(context.Context, module.ModuleConfig) error        { return nil }

// TestModuleCheckLadder_B306 pins the four outcomes the operator will actually see.
func TestModuleCheckLadder_B306(t *testing.T) {
	cases := []struct {
		name       string
		status     module.ModuleStatus
		health     module.HealthStatus
		wantStatus SystemTestStatus
		wantInOut  string
	}{
		{
			name:       "not-installed-is-skip",
			status:     module.ModuleStatus{State: module.StateNotInstalled},
			wantStatus: SystemTestSkip,
			wantInOut:  "not installed",
		},
		{
			name:       "running-healthy-is-pass",
			status:     module.ModuleStatus{State: module.StateRunning},
			health:     module.HealthStatus{Healthy: true, Checks: map[string]bool{"interface_up": true}},
			wantStatus: SystemTestPass,
			wantInOut:  "interface_up:true",
		},
		{
			name:       "running-unhealthy-is-fail",
			status:     module.ModuleStatus{State: module.StateRunning},
			health:     module.HealthStatus{Healthy: false, Checks: map[string]bool{"auth_ok": false}},
			wantStatus: SystemTestFail,
			wantInOut:  "claims to run but reports unhealthy",
		},
		{
			name:       "error-state-is-fail-with-last-error",
			status:     module.ModuleStatus{State: module.StateError, LastError: "tailscaled refused to start"},
			wantStatus: SystemTestFail,
			wantInOut:  "tailscaled refused to start",
		},
		{
			name:       "stopped-is-skip",
			status:     module.ModuleStatus{State: module.StateStopped},
			wantStatus: SystemTestSkip,
			wantInOut:  "stopped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, out := runModuleCheck("demo", b306ModuleStub{status: tc.status, health: tc.health})
			if gotStatus != tc.wantStatus {
				t.Errorf("status = %q, want %q (output %q)", gotStatus, tc.wantStatus, out)
			}
			if !strings.Contains(out, tc.wantInOut) {
				t.Errorf("output %q does not contain %q", out, tc.wantInOut)
			}
			if !strings.Contains(out, "module demo:") {
				t.Errorf("output %q does not name the module", out)
			}
		})
	}

	// A nil module (unregistered) must SKIP, not panic.
	if st, out := runModuleCheck("ghost", nil); st != SystemTestSkip || !strings.Contains(out, "not registered") {
		t.Errorf("nil module: (%q, %q), want skip + 'not registered'", st, out)
	}
}

// TestModuleTestsAreGenerated_B306: one test per registered module, named
// module.<name>, and the per-module run returns exactly that module's test.
func TestModuleTestsAreGenerated_B306(t *testing.T) {
	mgr := module.NewManager(t.TempDir(), t.TempDir(), func(string, string) {})
	if err := mgr.Register(b306ModuleStub{status: module.ModuleStatus{State: module.StateRunning}, health: module.HealthStatus{Healthy: true}}); err != nil {
		t.Fatalf("register stub module: %v", err)
	}
	svc := &Service{Modules: mgr}

	tests := svc.ModuleTests()
	if len(tests) != 1 {
		t.Fatalf("ModuleTests() = %d, want 1", len(tests))
	}
	if tests[0].Name != "module.stub" || tests[0].Category != moduleTestCategory {
		t.Errorf("generated test = %q/%q, want module.stub/modules", tests[0].Name, tests[0].Category)
	}
	if st, _ := tests[0].Run(context.Background()); st != SystemTestPass {
		t.Errorf("generated test status = %q, want pass", st)
	}

	// The catalogue the page and "Run all" use contains the module test.
	all := svc.AllTests()
	found := false
	for _, d := range all {
		if d.Name == "module.stub" {
			found = true
			break
		}
	}
	if !found {
		t.Error("AllTests() does not include the generated module test")
	}

	// The per-module run returns that module only; an unknown module returns nil
	// (the handler turns that into "not registered" instead of a silent success).
	results, summary := svc.RunModuleTests(context.Background(), "stub")
	if len(results) != 1 || summary == nil || summary.Pass != 1 {
		t.Fatalf("RunModuleTests(stub) = %d results, summary %+v", len(results), summary)
	}
	if results2, s2 := svc.RunModuleTests(context.Background(), "ghost"); results2 != nil || s2 != nil {
		t.Errorf("RunModuleTests(ghost) = %v/%v, want nil/nil", results2, s2)
	}

	rows := svc.ModuleTestRows()
	if len(rows) != 1 || rows[0].Name != "stub" || !rows[0].Healthy || rows[0].TestName != "module.stub" {
		t.Errorf("ModuleTestRows = %+v", rows)
	}
}

// TestModuleFailuresGetTheirOwnInboxSource_B306: a failing module test is not
// buried among the in-process checks — it reports under module:<name>, and a
// healthy run RESOLVES that event.
func TestModuleFailuresGetTheirOwnInboxSource_B306(t *testing.T) {
	svc, _ := b305Service(t)
	svc.ReportRunToMonitor([]SystemTestResult{
		{Name: "module.tailscale", Category: "modules", Status: SystemTestFail, Output: "state=error last_error: tailscaled refused to start"},
		{Name: "net.dns", Category: "network", Status: SystemTestFail, Output: "no resolver"},
	})

	moduleEvents, err := db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "module:tailscale"})
	if err != nil {
		t.Fatalf("list module events: %v", err)
	}
	if len(moduleEvents) != 1 {
		t.Fatalf("module events = %d, want 1 (%+v)", len(moduleEvents), moduleEvents)
	}
	ev := moduleEvents[0]
	if ev.Fingerprint != "module:tailscale:health" || ev.Severity != "error" || ev.State != "open" {
		t.Errorf("module event = %+v, want an open error with the module fingerprint", ev)
	}
	if !strings.Contains(ev.Title, "tailscale") || ev.Link != "/admin/modules/tailscale" {
		t.Errorf("module event title/link = %q/%q", ev.Title, ev.Link)
	}
	// The in-process failure keeps its own source.
	plain, _ := db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "system_test"})
	if len(plain) != 1 || plain[0].Fingerprint != "system_test:net.dns" {
		t.Errorf("in-process event = %+v, want system_test:net.dns", plain)
	}

	// Same-module recurrence folds into one row; recovery resolves it.
	svc.ReportRunToMonitor([]SystemTestResult{{Name: "module.tailscale", Category: "modules", Status: SystemTestFail, Output: "again"}})
	moduleEvents, _ = db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "module:tailscale"})
	if len(moduleEvents) != 1 || moduleEvents[0].Repeats != 2 {
		t.Errorf("after a repeat: %+v, want one row with repeats=2", moduleEvents)
	}
	svc.ReportRunToMonitor([]SystemTestResult{{Name: "module.tailscale", Category: "modules", Status: SystemTestPass, Output: "ok"}})
	moduleEvents, _ = db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "module:tailscale"})
	if len(moduleEvents) != 1 || moduleEvents[0].State != "resolved" {
		t.Errorf("after a healthy run: %+v, want resolved", moduleEvents)
	}
	// A module that is NOT installed must not create or resolve anything.
	svc.ReportRunToMonitor([]SystemTestResult{{Name: "module.tailscale", Category: "modules", Status: SystemTestSkip, Output: "not installed"}})
	moduleEvents, _ = db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "module:tailscale"})
	if len(moduleEvents) != 1 || moduleEvents[0].State != "resolved" {
		t.Errorf("a skipped module run changed the event: %+v", moduleEvents)
	}
	_ = sql.ErrNoRows
}

// TestModuleNameFromTest_B306: only generated names map to a module.
func TestModuleNameFromTest_B306(t *testing.T) {
	if got := ModuleNameFromTest("module.tailscale"); got != "tailscale" {
		t.Errorf("ModuleNameFromTest(module.tailscale) = %q", got)
	}
	if got := ModuleNameFromTest("net.dns"); got != "" {
		t.Errorf("ModuleNameFromTest(net.dns) = %q, want empty", got)
	}
}
