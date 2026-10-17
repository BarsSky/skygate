package handlers

// dashboard_wizard_render_b395_test.go — B395: the dashboard wizard must render the
// RIGHT branch for the RIGHT user, and must never print a zero it does not know.
//
// Why rendering and not source: the two defects this block exists to prevent are
// both invisible in a grep — "0 devices" rendered for a headscale that is DOWN (the
// operator is told everything is empty and starts creating users that go nowhere),
// and an admin-only import table leaking onto a normal user's dashboard. Both are
// properties of the rendered HTML with a given set of page data, so both are tested
// here.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"testing"

	"skygate/internal/setupstate"
)

// UserWizardStub mirrors my.UserWizard's field names. html/template resolves by
// name, so the handlers test does not import the feature package.
type UserWizardStub struct {
	HasDevice         bool
	DeviceCount       int
	ExitNodeAvailable bool
	Steps             []UserWizardStepStub
}

type UserWizardStepStub struct {
	ID    string
	Title string
	Done  bool
	Link  string
}

// loadDashboardBody parses dashboard.html with the same funcmap shape the render
// tests use: the template functions echo their keys, so a marker assertion is about
// which KEY reached the page, not about the Russian text.
func loadDashboardBody(t *testing.T) *template.Template {
	t.Helper()
	data, err := templatesFS.ReadFile("templates/dashboard.html")
	if err != nil {
		t.Fatalf("read dashboard.html: %v", err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":                  func(key string) string { return key },
		"tf":                 func(key string, args ...any) string { return fmt.Sprint(key, " ", args) },
		"safeHTML":           func(s string) template.HTML { return template.HTML(s) },
		"safeJS":             func(s string) template.JS { return template.JS(s) },
		"safeJSON":           func(v any) template.JS { b, _ := json.Marshal(v); return template.JS(b) },
		"humanizeAgeSeconds": func(int64) string { return "1m" },
		"add":                func(a, b int) int { return a + b },
		"sub":                func(a, b int) int { return a - b },
	}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse dashboard.html: %v", err)
	}
	return tpl
}

func renderDashboard(t *testing.T, data map[string]any) string {
	t.Helper()
	tpl := loadDashboardBody(t)
	var sb strings.Builder
	if err := tpl.ExecuteTemplate(&sb, "body-dashboard", data); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	return sb.String()
}

func unknownWizard() *setupstate.Report {
	return &setupstate.Report{
		State:  setupstate.StateUnreachable,
		Reason: "dial tcp 100.64.0.1:8080: connection refused",
		// UNKNOWN on purpose: an unreachable headscale must not produce zeros.
		InfraNodes:    setupstate.Count{Value: 0, Known: false},
		PendingReview: setupstate.Count{Value: 0, Known: false},
		Steps: []setupstate.Step{
			{ID: "diagnose", Title: "wizard.step_diagnose_headscale", State: setupstate.StepTodo,
				Why: "wizard.why_waiting_for_install", Link: "/admin/headscale"},
		},
	}
}

// TestDashboardWizard_NeverPrintsAnUnknownZero is the regression for the whole
// wizard: with an unreachable headscale the page must say «unknown», not 0.
func TestDashboardWizard_NeverPrintsAnUnknownZero(t *testing.T) {
	body := renderDashboard(t, map[string]any{
		"Username": "admin", "IsAdmin": true, "Wizard": unknownWizard(),
	})
	if !strings.Contains(body, "wizard.unknown") {
		t.Error("an unknown count was not rendered as the «unknown» marker — the page would show a zero")
	}
	if !strings.Contains(body, "wizard.state_unreachable") {
		t.Error("the unreachable state is not named on the page")
	}
	if !strings.Contains(body, "connection refused") {
		t.Error("the raw reason is not rendered — the operator cannot act on «does not answer» alone")
	}
}

// TestDashboardWizard_AdminBranchOnlyForAdmins pins the permission split: the admin
// wizard is rendered only when it is passed, and the user branch is a different card.
func TestDashboardWizard_AdminBranchOnlyForAdmins(t *testing.T) {
	admin := renderDashboard(t, map[string]any{
		"Username": "admin", "IsAdmin": true, "Wizard": unknownWizard(),
	})
	if !strings.Contains(admin, "wizard.title") {
		t.Error("the admin branch is missing on an admin dashboard")
	}

	user := renderDashboard(t, map[string]any{
		"Username": "alice", "IsAdmin": false,
		"UserWizard": &UserWizardStub{Steps: []UserWizardStepStub{
			{ID: "connect", Title: "wizard.user_step_connect", Done: true},
		}},
	})
	if strings.Contains(user, "wizard.state_label") {
		t.Error("the admin branch (headscale state) leaked onto a user dashboard")
	}
	if !strings.Contains(user, "wizard.user_title") {
		t.Error("the user branch is missing on a user dashboard")
	}
}

// TestDashboardWizard_ImportTableClassifiesNodes pins the three-way classification
// the import table exists for: infrastructure, awaiting review, owned.
func TestDashboardWizard_ImportTableClassifiesNodes(t *testing.T) {
	rep := &setupstate.Report{
		State:         setupstate.StatePopulated,
		InfraNodes:    setupstate.Count{Value: 1, Known: true},
		PendingReview: setupstate.Count{Value: 1, Known: true},
		Nodes: []setupstate.NodeRow{
			{NodeID: "1", Hostname: "relay", Username: "infra", Infra: true},
			{NodeID: "2", Hostname: "laptop", Username: "alice"},
			{NodeID: "3", Hostname: "guest-phone", Username: "stranger", NeedsReview: true},
		},
	}
	body := renderDashboard(t, map[string]any{"Username": "admin", "IsAdmin": true, "Wizard": rep})
	for _, marker := range []string{"wizard.node_infra", "wizard.node_review", "wizard.node_owned", "relay", "guest-phone", "wizard.import_show"} {
		if !strings.Contains(body, marker) {
			t.Errorf("the import table is missing %q", marker)
		}
	}
}

// TestDashboardWizard_AclWarningAndAiLink pins the two hints the operator asked for
// by name: the ACL rewrite warning (with the snapshot) and the AI-helper link.
func TestDashboardWizard_AclWarningAndAiLink(t *testing.T) {
	rep := &setupstate.Report{
		State:         setupstate.StatePopulated,
		InfraNodes:    setupstate.Count{Known: true},
		PendingReview: setupstate.Count{Known: true},
		ACLLocked:     true,
		SnapshotID:    42,
	}
	body := renderDashboard(t, map[string]any{"Username": "admin", "IsAdmin": true, "Wizard": rep})
	if !strings.Contains(body, "wizard.acl_warning") {
		t.Error("the ACL rewrite warning is not rendered")
	}
	if !strings.Contains(body, "42") {
		t.Error("the snapshot id is not rendered — the warning would be unrecoverable")
	}
	if !strings.Contains(body, "/my/exit-rules/agent") {
		t.Error("the AI-helper link is missing")
	}
}

// TestDashboardWizard_AbsentShowsInstallScripts pins that the "no headscale" state
// ships ready-made commands as TEXT — the wizard never rewrites the operator's
// docker-compose.yml, so the script is the deliverable.
func TestDashboardWizard_AbsentShowsInstallScripts(t *testing.T) {
	rep := &setupstate.Report{
		State:         setupstate.StateAbsent,
		Reason:        "no headscale client is configured",
		InfraNodes:    setupstate.Count{Known: false},
		PendingReview: setupstate.Count{Known: false},
	}
	body := renderDashboard(t, map[string]any{"Username": "admin", "IsAdmin": true, "Wizard": rep})
	for _, marker := range []string{"wizard.install_docker_script", "wizard.install_systemd_script", "wizard.install_hint"} {
		if !strings.Contains(body, marker) {
			t.Errorf("the absent state is missing %q", marker)
		}
	}
}
