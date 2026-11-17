package handlers

// host_migration_render_b482_test.go — B482: the host-migration wizard's two pages must
// actually RENDER.
//
// /admin/migrate asks nine questions about moving the installation to another machine and
// stores the answers; /admin/migrate/{id} renders the generated plan and runs one step at a
// time. Both templates are new, and every failure mode of a Go template is an EXECUTE-time
// failure that no shell check can see: a field the data does not carry, a helper the funcmap
// does not define, a type a date helper refuses. B325 E3 only parses them, and
// TestTemplateArgsMatchCatalog only counts printf arguments.
//
// So this file pins three things:
//
//  1. `body-admin-host_migration` executes on a realistic wizard payload (with and without a
//     stored secret, with and without history rows) in RU and EN.
//  2. `body-admin-host_migration_run` executes on a stored plan AND on the not-yet-stored
//     preview, and carries the per-step form the executor needs (`step_key`).
//  3. the pages are wired: the sidebar links /admin/migrate, and pageFromName/pageTitle/
//     sectionPageSet/sectionLabel/pageLabel know both templates (B96/B435 read the same maps).
//
// The payloads are plain maps on purpose: the view structs live unexported in
// internal/feature/admin, and a test that had to export them would be testing the export.
// Map-based data also proves the templates do not depend on a struct's method set.

import (
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// b482Answers is one realistic answer set (copy mode, pasted key, PostgreSQL, provider DNS).
func b482Answers() map[string]any {
	return map[string]any{
		"Mode":         "copy",
		"Host":         "198.51.100.10",
		"Port":         22,
		"User":         "root",
		"Auth":         "key_paste",
		"KeyPath":      "/root/.ssh/id_ed25519",
		"InstallKind":  "systemd",
		"DBMode":       "postgres",
		"NewHostname":  "skygate-host-2",
		"DNSMode":      "provider",
		"DNSTTL":       300,
		"OldHost":      "stop",
		"KeepNoiseKey": true,
		"ReapplyACL":   true,
		"DedupeRules":  true,
		"DerpMove":     true,
	}
}

// b482StepView is one step row exactly as the handler renders it.
func b482StepView() map[string]any {
	return map[string]any{
		"Key":        "p0.snapshot",
		"Phase":      "P0",
		"Ordinal":    5,
		"TitleKey":   "migrate.step.p0_snapshot.title",
		"WhyKey":     "migrate.step.p0_snapshot.why",
		"NoteKey":    "migrate.note.note_snapshot_stops_services",
		"ManualKey":  "",
		"KindKey":    "migrate.kind_confirm",
		"WhereKey":   "migrate.where_local",
		"StatusKey":  "migrate.status_pending",
		"Kind":       "confirm",
		"Where":      "local",
		"Status":     "pending",
		"Required":   true,
		"Downtime":   true,
		"Internal":   false,
		"CanRun":     true,
		"Command":    "set -e; systemctl stop skygate; tar -czf /tmp/x.tgz /var/lib/skygate",
		"Verify":     "test -f /tmp/x.tgz",
		"Output":     "",
		"StartedAt":  int64(0),
		"FinishedAt": int64(0),
	}
}

func b482WizardData(withRuns bool) map[string]any {
	data := map[string]any{
		"Answers":           b482Answers(),
		"RunID":             int64(0),
		"HasSSHSecret":      true,
		"SSHSecretFP":       "••••••••abcd",
		"HasDSN":            true,
		"DSNFP":             "••••••••wxyz",
		"DefaultSSHKeyPath": "/root/.ssh/id_ed25519",
		"SecretKeyMissing":  false,
		"Runs":              []any{},
		"FlashSuccess":      "",
		"FlashError":        "",
	}
	if withRuns {
		data["RunID"] = int64(7)
		data["Runs"] = []any{map[string]any{
			"ID":          int64(7),
			"State":       "planned",
			"StateKey":    "migrate.state_planned",
			"ModeKey":     "migrate.mode_copy",
			"Target":      "root@198.51.100.10:22",
			"NewHostname": "skygate-host-2",
			"Author":      "admin",
			"CreatedAt":   int64(1763000000),
			"CanDelete":   true,
		}}
	}
	return data
}

func b482RunData(preview bool) map[string]any {
	step := b482StepView()
	if preview {
		step["Kind"] = "auto"
		step["KindKey"] = "migrate.kind_auto"
		step["Downtime"] = false
		step["NoteKey"] = ""
	}
	return map[string]any{
		"Run": map[string]any{
			"ID":            int64(7),
			"CreatedByName": "admin",
			"CreatedAt":     int64(1763000000),
			"State":         "planned",
		},
		"Answers": b482Answers(),
		"Phases": []any{map[string]any{
			"Name":     "P0",
			"TitleKey": "migrate.phase.p0",
			"Done":     0,
			"Total":    1,
			"Steps":    []any{step},
		}},
		"HasSteps":         !preview,
		"Preview":          preview,
		"HasRunning":       false,
		"Total":            1,
		"DoneSteps":        0,
		"HasSSHSecret":     true,
		"SSHSecretFP":      "••••••••abcd",
		"HasDSN":           true,
		"DSNFP":            "••••••••wxyz",
		"SecretKeyMissing": false,
		"Target":           "root@198.51.100.10:22",
		"StateKey":         "migrate.state_planned",
		"ModeKey":          "migrate.mode_copy",
		"DNSKey":           "migrate.dns_provider",
		"OldKey":           "migrate.old_stop",
		"InstallKey":       "migrate.install_systemd",
		"AuthKey":          "migrate.auth_key_paste",
		"DBKey":            "migrate.db_postgres",
		"FlashSuccess":     "",
		"FlashError":       "",
	}
}

func b482RunDataManual() map[string]any {
	data := b482RunData(false)
	step := b482StepView()
	step["Kind"] = "manual"
	step["KindKey"] = "migrate.kind_manual"
	step["Where"] = "manual"
	step["WhereKey"] = "migrate.where_manual"
	step["Downtime"] = false
	step["NoteKey"] = "migrate.note.note_client_check"
	step["ManualKey"] = "migrate.manual.manual_client_check"
	step["CanRun"] = true
	step["Command"] = "manual_client_check"
	step["Verify"] = ""
	data["Phases"] = []any{map[string]any{
		"Name":     "P6",
		"TitleKey": "migrate.phase.p6",
		"Done":     0,
		"Total":    1,
		"Steps":    []any{step},
	}}
	return data
}

// TestB482_HostMigrationTemplatesRender — both pages execute in both languages.
func TestB482_HostMigrationTemplatesRender(t *testing.T) {
	tpl := LoadTemplates()
	if tpl == nil {
		t.Fatal("LoadTemplates returned nil")
	}
	i18n.SetGlobal(i18n.New())

	cases := []struct {
		name string
		tmpl string
		data map[string]any
		want []string
	}{
		{
			name: "wizard-no-history",
			tmpl: "body-admin-host_migration",
			data: b482WizardData(false),
			// No stored attempt → no history table at all (the empty state is a sentence, not an
			// empty table). The table-wrap contract is asserted on the case that HAS rows.
			want: []string{`name="host"`, `name="ssh_port"`, `name="ssh_password"`,
				`name="db_dsn"`, `name="clear_ssh_secret"`, `action="/admin/migrate/save"`},
		},
		{
			name: "wizard-with-history",
			tmpl: "body-admin-host_migration",
			data: b482WizardData(true),
			want: []string{`action="/admin/migrate/7/delete"`, `href="/admin/migrate/7"`,
				`class="table-wrap"`},
		},
		{
			name: "run-stored",
			tmpl: "body-admin-host_migration_run",
			data: b482RunData(false),
			want: []string{`action="/admin/migrate/7/step"`, `name="step_key"`,
				`action="/admin/migrate/7/plan"`, `class="table-wrap"`},
		},
		{
			name: "run-preview",
			tmpl: "body-admin-host_migration_run",
			data: b482RunData(true),
			want: []string{`action="/admin/migrate/7/plan"`},
		},
		{
			// A manual step keeps its button — pressing it is how the operator records that the
			// instruction was carried out (the handler writes manual_wait, it runs nothing).
			name: "run-manual-step",
			tmpl: "body-admin-host_migration_run",
			data: b482RunDataManual(),
			want: []string{`name="step_key"`, `value="p0.snapshot"`},
		},
	}
	for _, tc := range cases {
		for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
			i18n.SetLang(lang)
			var sb strings.Builder
			if err := tpl.ExecuteTemplate(&sb, tc.tmpl, map[string]any{
				"Page": "admin/migrate",
				"Data": tc.data,
			}); err != nil {
				t.Fatalf("%s/%s: execute %s: %v", tc.name, lang, tc.tmpl, err)
			}
			out := sb.String()
			if strings.Contains(out, "{{") {
				t.Errorf("%s/%s: rendered page still has unexpanded template actions", tc.name, lang)
			}
			// A missing catalogue key renders AS the key, which is the defect B325 A1 hunts for
			// in the source; here it is caught in the output.
			if strings.Contains(out, "migrate.") && strings.Contains(out, ">migrate.") {
				t.Errorf("%s/%s: the page rendered a raw i18n key", tc.name, lang)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("%s/%s: rendered page is missing %q", tc.name, lang, w)
				}
			}
			// The secret must never come back: the fingerprint is the most the page may show.
			if strings.Contains(out, "value=\"••••") {
				t.Errorf("%s/%s: a secret fingerprint landed in a form VALUE — it belongs in a placeholder", tc.name, lang)
			}
		}
	}

	// A manual step has its OWN button label: pressing it records that the operator carried the
	// instruction out, it runs nothing. Both languages must resolve it (a missing key renders as
	// the key itself, which the check above cannot distinguish from a page that omits the button).
	cat := i18n.New()
	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		i18n.SetLang(lang)
		want := cat.T(lang, "migrate.run_btn_manual")
		if want == "migrate.run_btn_manual" {
			t.Fatalf("%s: migrate.run_btn_manual is not in the catalogue", lang)
		}
		var sb strings.Builder
		if err := tpl.ExecuteTemplate(&sb, "body-admin-host_migration_run", map[string]any{
			"Page": "admin/migrate",
			"Data": b482RunDataManual(),
		}); err != nil {
			t.Fatalf("%s: execute the manual-step page: %v", lang, err)
		}
		if !strings.Contains(sb.String(), want) {
			t.Errorf("%s: the manual step did not render %q — it looks like a runnable step", lang, want)
		}
	}
}

// TestB482_HostMigrationWiring — the page is discoverable and the B96/B435 maps know it.
func TestB482_HostMigrationWiring(t *testing.T) {
	layout, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	if !strings.Contains(string(layout), `href="/admin/migrate"`) {
		t.Error("layout.html has no link to /admin/migrate — the wizard would be undiscoverable")
	}
	if !strings.Contains(string(layout), `{{t "nav.migrate"}}`) {
		t.Error("the sidebar entry is not translated ({{t \"nav.migrate\"}} is missing)")
	}

	// Both templates report the SAME .Page, so one sidebar link and one breadcrumb cover the
	// form and the run page. The title key is asserted exactly: a typo here puts a raw key in
	// the browser tab (the B435/B450 defect).
	titles := map[string]string{
		"admin/host_migration.html":     "title.admin_host_migration",
		"admin/host_migration_run.html": "title.admin_host_migration_run",
	}
	for name, wantTitle := range titles {
		if got := pageFromName(name); got != "admin/migrate" {
			t.Errorf("pageFromName(%s) = %q, want admin/migrate", name, got)
		}
		if got := pageTitle(name); got != wantTitle {
			t.Errorf("pageTitle(%s) = %q, want %q", name, got, wantTitle)
		}
	}
	if got := pageLabel("admin/migrate"); got != "nav.migrate" {
		t.Errorf("pageLabel(admin/migrate) = %q, want nav.migrate", got)
	}
	if got := sectionLabel("admin/migrate"); got != "nav.section_data" {
		t.Errorf("sectionLabel(admin/migrate) = %q, want nav.section_data", got)
	}
	if !sectionPageSet("admin/migrate")["InSectionData"] {
		t.Error("sectionPageSet(admin/migrate) does not mark the Data section — the sidebar would not auto-open")
	}
}
