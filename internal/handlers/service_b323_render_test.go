package handlers

// service_b323_render_test.go — B323 (2026-09-25): the SERVICE CONTROL page
// (/admin/service) must actually RENDER.
//
// The page is the operator's answer to «вынести перезапуск сервиса в настройки и в
// отдельный блок управления состоянием skygate, чтобы не искать где он есть
// сейчас». Its state struct comes from internal/feature/admin (types only — the
// render test needs no handler, no DB and no container), so this file lives in the
// handlers package where the real template set and funcmap are available.
//
// Two contracts:
//
//  1. body-admin-service executes against a binary-host / docker / kubernetes
//     state without an error. A typo in a template action or a call to a helper
//     the funcmap does not define (TestLoadTemplates does not catch the latter,
//     because parse-time the helper may simply be unknown only at execute time)
//     fails here instead of on the operator's box.
//  2. every `service_ctl.*` key the template asks for exists in BOTH catalogues
//     (rule 10) — a bare key rendered into the page is the same defect as an
//     English literal.

import (
	"strings"
	"testing"

	"skygate/internal/feature/admin"
	"skygate/internal/i18n"
)

// b323State builds the three interesting state shapes. The fields are exported,
// so the handlers-side test can assemble them without the admin package's
// injectable probe (which is unexported by design).
func b323States() map[string]admin.ServiceControlState {
	return map[string]admin.ServiceControlState{
		"docker": {
			Kind:            admin.KindDocker,
			KindWhy:         "container marker /.dockerenv",
			InContainer:     true,
			EnvFilePath:     "/home/operator/skygate/.env",
			EnvFileSource:   "service_ctl.envsrc_docker_repo",
			EnvFileExists:   true,
			UnitName:        "skygate",
			ContainerName:   "skygate",
			ComposeProject:  "skygate",
			ComposeFile:     "/home/operator/skygate/docker-compose.yml",
			Build:           "v1.5.99+deadbee",
			Uptime:          "1h30m",
			CanRestart:      true,
			CanRecreate:     true,
			RestartCommand:  "docker compose -p skygate -f /home/operator/skygate/docker-compose.yml restart skygate",
			RecreateCommand: "docker compose -p skygate -f /home/operator/skygate/docker-compose.yml up -d --force-recreate skygate",
			RestartArgs:     []string{"docker", "compose", "restart", "skygate"},
			RecreateArgs:    []string{"docker", "compose", "up", "-d", "--force-recreate", "skygate"},
			LastRestartLog:  "[2026-09-25T00:00:00Z] docker compose restart\n  exit=0",
			Notes:           []string{"service_ctl.note_docker_env_frozen", "service_ctl.note_docker_cwd"},
		},
		"kubernetes": {
			Kind:            admin.KindKubernetes,
			KindWhy:         "KUBERNETES_SERVICE_HOST is set",
			InContainer:     true,
			EnvFileSource:   "service_ctl.envsrc_k8s_deployment",
			UnitName:        "skygate",
			ContainerName:   "skygate",
			Build:           "v1.5.99+deadbee",
			RestartRefusal:  "service_ctl.k8s_refuse",
			RecreateRefusal: "service_ctl.recreate_docker_only",
			Notes:           []string{"service_ctl.note_k8s"},
		},
		"binary": {
			Kind:            admin.KindBinary,
			KindWhy:         "no service manager found (started by hand)",
			EnvFileSource:   "service_ctl.envsrc_none",
			UnitName:        "skygate",
			ContainerName:   "skygate",
			RestartRefusal:  "service_ctl.binary_refuse",
			RecreateRefusal: "service_ctl.recreate_docker_only",
			Notes:           []string{"service_ctl.note_binary"},
		},
	}
}

// TestB323_ServiceTemplateRenders — the page executes for every install-kind
// shape, in both languages.
func TestB323_ServiceTemplateRenders(t *testing.T) {
	tpl := LoadTemplates()
	if tpl == nil {
		t.Fatal("LoadTemplates returned nil")
	}
	i18n.SetGlobal(i18n.New())
	for name, state := range b323States() {
		for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
			i18n.SetLang(lang)
			var sb strings.Builder
			err := tpl.ExecuteTemplate(&sb, "body-admin-service", map[string]any{
				"Page":         "admin/service",
				"Title":        "Service control",
				"State":        state,
				"CSRF":         "b323-test-token",
				"FlashSuccess": "",
				"FlashError":   "",
			})
			if err != nil {
				t.Fatalf("%s/%s: execute body-admin-service: %v", name, lang, err)
			}
			out := sb.String()
			if strings.Contains(out, "{{") {
				t.Errorf("%s/%s: rendered page still has unexpanded template actions", name, lang)
			}
			if !strings.Contains(out, `class="table-wrap"`) {
				t.Errorf("%s/%s: no .table-wrap in the rendered page (mobile horizontal scroll)", name, lang)
			}
		}
	}
}

// TestB323_ServiceTemplateKeysExist — every literal service_ctl.* key in the
// template resolves in RU and EN. The dynamic kind keys are checked too, because
// the template builds them with printf.
func TestB323_ServiceTemplateKeysExist(t *testing.T) {
	data, err := templatesFS.ReadFile("templates/admin/service.html")
	if err != nil {
		t.Fatalf("read service.html: %v", err)
	}
	html := string(data)
	cat := i18n.New()

	// Static literal keys.
	for _, m := range tCallRe.FindAllStringSubmatch(html, -1) {
		key := m[2]
		if !strings.HasPrefix(key, "service_ctl.") {
			continue
		}
		for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
			if got := cat.T(lang, key); got == key {
				t.Errorf("service.html: key %q missing from the %s catalogue", key, lang)
			}
		}
	}

	// The keys the template assembles at run time, plus the ones the handler
	// renders through {{t .State.*}}.
	dynamic := []string{
		"service_ctl.kind_docker", "service_ctl.kind_systemd", "service_ctl.kind_openrc",
		"service_ctl.kind_kubernetes", "service_ctl.kind_binary", "service_ctl.kind_unknown",
		"service_ctl.envsrc_systemd_unit", "service_ctl.envsrc_systemd_default",
		"service_ctl.envsrc_docker_repo", "service_ctl.envsrc_docker_default",
		"service_ctl.envsrc_openrc", "service_ctl.envsrc_k8s_deployment",
		"service_ctl.envsrc_binary_explicit", "service_ctl.envsrc_none",
		"service_ctl.k8s_refuse", "service_ctl.binary_refuse", "service_ctl.unknown_refuse",
		"service_ctl.recreate_docker_only", "service_ctl.docker_nocompose",
		"service_ctl.restart_unavailable", "service_ctl.recreate_unavailable",
		"service_ctl.unknown_action", "service_ctl.flash_launched",
		"service_ctl.note_k8s", "service_ctl.note_binary", "service_ctl.note_unknown",
		"service_ctl.note_docker_env_frozen", "service_ctl.note_systemd_native",
		"service_ctl.note_openrc_native", "service_ctl.note_docker_cwd",
		// The three function keys (the labels the page renders as its own text):
		// they belong to this page, so they were moved out of the global common.*
		// namespace and must exist in both catalogues here.
		"service_ctl.back", "service_ctl.open_page", "service_ctl.tailscale_moved",
	}
	for _, key := range dynamic {
		for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
			if got := cat.T(lang, key); got == key {
				t.Errorf("key %q (referenced from the template or the state) missing from the %s catalogue", key, lang)
			}
		}
	}
}

// TestB323_LayoutLinksServicePage — the page is discoverable: the sidebar has a
// link (the whole point of the operator's request) and the B96 section maps in
// internal/handlers/handlers.go know about the page.
func TestB323_LayoutLinksServicePage(t *testing.T) {
	layout, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	if !strings.Contains(string(layout), `href="/admin/service"`) {
		t.Error("layout.html has no link to /admin/service — the control would be undiscoverable again")
	}
	if !strings.Contains(string(layout), `{{t "service_ctl.title"}}`) {
		t.Error("layout.html sidebar entry does not use the service_ctl.title key")
	}
	if sectionLabel("admin/service") != "nav.section_settings" {
		t.Errorf("sectionLabel(admin/service) = %q, want nav.section_settings", sectionLabel("admin/service"))
	}
	if pageLabel("admin/service") == "" {
		t.Error("pageLabel(admin/service) is empty — the breadcrumb would skip the page")
	}
	if !sectionPageSet("admin/service")["InSectionSettings"] {
		t.Error("sectionPageSet(admin/service) does not mark the Settings section — the sidebar would not auto-open")
	}
}
