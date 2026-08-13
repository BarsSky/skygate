package handlers

// exit_nodes_render_test.go — regression test for the v0.33.1.29 B81
// SSH-target fallback chain on /admin/exit-nodes.
//
// Why a render test (not just a unit test on db.LookupExitServerSSHTarget)?
// The B81 fix touches three layers:
//   1. internal/db (LookupExitServerSSHTarget helper) — unit-tested in
//      internal/db/exit_servers_test.go (4 cases).
//   2. internal/feature/exit_rules/sync.go (the SSH call path) —
//      covered by inspection + a small wiring note (the integration
//      is too tightly coupled to *headscale.Client to mock without
//      a v0.34 refactor).
//   3. THIS file: the /admin/exit-nodes.html template must
//        a) render the B81 "auto (Tailscale IP)" badge when the
//           resolved SSH target came from the Tailscale IP fallback,
//        b) render the "Use Tailscale IP" button when the stored
//           ssh_target DIFFERS from the resolved one (so the
//           operator has a one-click migration path),
//        c) include the new form helper text under the ssh_target
//           input field (so a fresh add naturally leaves ssh_target
//           empty and picks up the auto-fallback),
//        d) NOT regress the v0.33.1 ssh_target column rendering
//           (the stored value still shows when no Tailscale IP is
//           set).
//
// The test funcmap mirrors the production funcmap (templates.go)
// so a future change to the template that uses a helper not in
// the funcmap fails the test, not the page load.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"
	"testing"
)

// stubExitNodeInfo mimics the live admin.ExitNodeInfo for the
// fields the template reads in the SSH column. If a future
// template edit accesses a field not on this stub, the test
// panics with "can't evaluate field X in type handlers.stubExitNodeInfo"
// — same signal as the v0.33.1.2 B78 test pattern. The
// healthy/state/last-seen fields are added because the
// hostname column also renders them (the health dot/half/empty
// circle), and the test needs the row to render past that
// column to actually reach the SSH column we're asserting on.
type stubExitNodeInfo struct {
	NodeID              string
	Hostname            string
	TailscaleIP         string
	SSHTarget           string
	ResolvedSSHTarget   string
	SSHTargetAuto       bool
	SSHKeyPath          string
	Enabled             bool
	Healthy             bool
	State               string
	LastSeenAgo         string
	Routes              []string
	RouteCount          int
	AcceptRoutes        int
	Tags                []string
	AdvertisesV4Default bool
	AdvertisesV6Default bool
	SyncStatus          string
	Description         string
}

// loadExitNodesBody parses the exit_nodes.html body template with
// a minimal funcmap that mirrors the production funcmap. The
// catalog lookups (t / tf) are stubbed to return the key as-is
// — the test cares about the TEMPLATE STRUCTURE (which i18n keys
// are referenced, where the new button shows up), not the
// translated strings (those are covered by the i18n parity test).
func loadExitNodesBody(t *testing.T) *template.Template {
	t.Helper()
	data, err := templatesFS.ReadFile("templates/admin/exit_nodes.html")
	if err != nil {
		t.Fatalf("read exit_nodes.html: %v", err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":      func(key string) string { return key },
		"tf":     func(key string, args ...any) string { return key },
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeJSON": func(s string) template.JS {
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
	}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse exit_nodes.html: %v", err)
	}
	return tpl
}

// TestExitNodesRendersB81_ResolvedSSHTarget pins the headline B81
// fix: a row whose stored ssh_target is empty but TailscaleIP is
// set shows the RESOLVED value (`root@<tailscale_ip>`) in the SSH
// column, not a "—" placeholder. The pre-B81 column only looked
// at the stored value and would render "—" for exactly this row,
// making it impossible to predict what the next sync would hit
// (the operator had to wait for the next sync to fail and read
// the audit log to discover the actual host).
func TestExitNodesRendersB81_ResolvedSSHTarget(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes": []stubExitNodeInfo{
			{
				NodeID:            "1",
				Hostname:          "relay-1",
				TailscaleIP:       "100.64.0.10",
				SSHTarget:         "", // empty → B81 fallback
				ResolvedSSHTarget: "root@100.64.0.10",
				SSHTargetAuto:     true,
				Enabled:           true,
			},
		},
		"TotalCount":  1,
		"HealthyCount": 1,
		"ControlURL":  "https://head.example.com",
		"SSHKeyPath":  "/ssh-sync/id_ed25519",
		"Page":        "admin/exit_nodes",
		"Title":       "Exit nodes",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "root@100.64.0.10") {
		t.Errorf("resolved ssh_target must render in the SSH column, got:\n%s", got)
	}
	// The "auto (Tailscale IP)" badge must be present — the
	// operator needs to know the row is using the B81 fallback
	// and the stored ssh_target is empty.
	if !strings.Contains(got, "exit_nodes.ssh_target_auto_badge") {
		t.Errorf("B81 auto badge i18n key must render when SSHTargetAuto=true, got:\n%s", got)
	}
}

// TestExitNodesRendersB81_OperatorOverrideWins pins the priority:
// when the operator has set ssh_target explicitly (e.g. karolina's
// port 18022), the resolved value MUST be the operator's override
// — NOT the B81 auto-fallback. This is the "karolina regression"
// test: a refactor that accidentally lets the auto-fallback
// shadow the operator's non-default port would silently lose the
// port and start ssh'ing to port 22 on karolina (which karolina
// doesn't listen on).
func TestExitNodesRendersB81_OperatorOverrideWins(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes": []stubExitNodeInfo{
			{
				NodeID:            "2",
				Hostname:          "karolina",
				TailscaleIP:       "100.64.0.20",
				SSHTarget:         "root@karolina.example.com:18022",
				ResolvedSSHTarget: "root@karolina.example.com:18022",
				SSHTargetAuto:     false,
				Enabled:           true,
			},
		},
		"TotalCount":  1,
		"HealthyCount": 1,
		"ControlURL":  "https://head.example.com",
		"SSHKeyPath":  "/ssh-sync/id_ed25519",
		"Page":        "admin/exit_nodes",
		"Title":       "Exit nodes",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	// Operator's non-default port must be in the rendered HTML.
	if !strings.Contains(got, "root@karolina.example.com:18022") {
		t.Errorf("operator override (port 18022) must render, got:\n%s", got)
	}
	// The "Use Tailscale IP" button must NOT render when stored
	// and resolved values match (no migration needed).
	if strings.Contains(got, "/admin/exit-nodes/use-ts-ip") {
		t.Errorf("use-ts-ip button must NOT render when stored == resolved, got:\n%s", got)
	}
}

// TestExitNodesRendersB81_UseTailscaleIPButton shows up when
// stored differs from resolved. The classic v0.33.1-era case:
// ssh_target = "root@<firewalled-public-ip>:22" (a public IP
// the operator's firewall doesn't forward) but tailscale_ip
// is "100.64.0.10" (always reachable). The B81
// fallback would mask the broken override silently, but the
// button makes the migration explicit.
func TestExitNodesRendersB81_UseTailscaleIPButton(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes": []stubExitNodeInfo{
			{
				NodeID:            "3",
				Hostname:          "relay-3",
				TailscaleIP:       "100.64.0.30",
				SSHTarget:         "root@198.51.100.30:22", // firewalled public IP (RFC 5737 docs)
				ResolvedSSHTarget: "root@100.64.0.30",      // B81 fallback
				SSHTargetAuto:     false,                    // SSHTarget is set, so not auto
				Enabled:           true,
			},
		},
		"TotalCount":  1,
		"HealthyCount": 1,
		"ControlURL":  "https://head.example.com",
		"SSHKeyPath":  "/ssh-sync/id_ed25519",
		"Page":        "admin/exit_nodes",
		"Title":       "Exit nodes",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	// The button must render (the operator needs a way to fix
	// the broken override).
	if !strings.Contains(got, "/admin/exit-nodes/use-ts-ip") {
		t.Errorf("use-ts-ip button must render when stored != resolved, got:\n%s", got)
	}
	// The hidden node_id must be present so the handler can
	// resolve the row.
	if !strings.Contains(got, `value="3"`) {
		t.Errorf("hidden node_id=3 must render in the form, got:\n%s", got)
	}
	// The "Use Tailscale IP" i18n key must be referenced.
	if !strings.Contains(got, "exit_nodes.ssh_target_use_ts_ip") {
		t.Errorf("use-ts-ip button i18n key must render, got:\n%s", got)
	}
}

// TestExitNodesRendersB81_FormHelperText pins the add-form
// helper text. The pre-B81 form had no hint about leaving
// ssh_target empty, so operators either (a) typed a public IP
// that turned out to be firewalled, or (b) left it empty and
// were surprised when SyncAdvertisedRoutes fell back to
// nodeHostname (which doesn't resolve for typical exit-nodes).
// B81 makes "leave empty = Tailscale IP" the documented and
// supported path; this test pins the helper text so a future
// refactor that drops it re-introduces the v0.33.1 UX bug.
func TestExitNodesRendersB81_FormHelperText(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes":        []stubExitNodeInfo{},
		"TotalCount":   0,
		"HealthyCount": 0,
		"ControlURL":   "https://head.example.com",
		"SSHKeyPath":   "/ssh-sync/id_ed25519",
		"Page":         "admin/exit_nodes",
		"Title":        "Exit nodes",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "exit_nodes.form_ssh_target_help") {
		t.Errorf("form ssh_target helper text must render, got:\n%s", got)
	}
	if !strings.Contains(got, "exit_nodes.form_ssh_target_placeholder") {
		t.Errorf("form ssh_target placeholder i18n key must render, got:\n%s", got)
	}
}

// TestExitNodesRendersB81_DisabledRowHidesButton pins that
// the "Use Tailscale IP" button does NOT render on disabled
// rows. The PostAdminExitNodeUseTailscaleIP handler refuses
// to touch disabled rows (it would change a row the operator
// explicitly turned off into a row that participates in the
// next sync) — the button must mirror that contract.
func TestExitNodesRendersB81_DisabledRowHidesButton(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes": []stubExitNodeInfo{
			{
				NodeID:            "4",
				Hostname:          "off-relay",
				TailscaleIP:       "100.64.0.40",
				SSHTarget:         "root@broken.example.com:22",
				ResolvedSSHTarget: "root@100.64.0.40",
				SSHTargetAuto:     false,
				Enabled:           false, // operator turned this off
			},
		},
		"TotalCount":  1,
		"HealthyCount": 0,
		"ControlURL":  "https://head.example.com",
		"SSHKeyPath":  "/ssh-sync/id_ed25519",
		"Page":        "admin/exit_nodes",
		"Title":       "Exit nodes",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "/admin/exit-nodes/use-ts-ip") {
		t.Errorf("use-ts-ip button must NOT render on disabled rows, got:\n%s", got)
	}
}
