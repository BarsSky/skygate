package handlers

// handlers_active_tree_test.go — regression tests for the
// admin-sidebar "active in tree" feature (v1.1.0 TD-1 + v1.1.5
// breadcrumb). The sidebar highlights the current page +
// auto-opens the parent <details> block, AND a breadcrumb
// ("Админ › Devices & Nodes › Devices") shows the section +
// page name. Both depend on the .Page field set by
// pageFromName() in handlers.go matching the URL path that
// sectionPageSet() + sectionLabel() + pageLabel() + the
// layout.html's `{{if eq .Page "admin/..."}}` lookups all
// use.
//
// Bug fixed 2026-08-13: pageFromName used to return the
// template name verbatim ("admin/exit_nodes"), but the URL
// + sidebar link + section map all use the canonical URL
// form ("admin/exit-nodes", hyphen). Result: exit-nodes,
// exit-rules, control-planes pages had no active-link
// class, no auto-open section, no breadcrumb. The fix is
// in pageFromName — translate underscores to hyphens in
// the admin/ segment. These tests pin that fix.

import "testing"

// TestPageFromName_AdminSegmentsTranslateUnderscoreToHyphen
// pins the 3 admin pages whose template filenames use
// underscores but whose URLs use hyphens. Each must
// return the hyphen form so the runtime .Page value
// matches what sectionPageSet() + layout.html's
// `{{if eq .Page "admin/exit-nodes"}}` lookups all expect.
func TestPageFromName_AdminSegmentsTranslateUnderscoreToHyphen(t *testing.T) {
	cases := []struct {
		templateName string
		want         string
	}{
		// Top-level pages with the URL-vs-template mismatch
		// (the v1.1.0-era bug — section auto-open + active
		// link + breadcrumb all silently broken until the
		// 2026-08-13 fix).
		{"admin/exit_nodes.html", "admin/exit-nodes"},
		{"admin/exit_rules.html", "admin/exit-rules"},
		{"admin/control_planes.html", "admin/control-planes"},
		// Sub-pages with underscore — also translated, even
		// though they don't appear in sectionPageSet (they
		// don't need active-link / section / breadcrumb).
		{"admin/exit_rules_cleanup.html", "admin/exit_rules_cleanup"},
		{"admin/exit_rules_nodes.html", "admin/exit_rules_nodes"},
		{"admin/acls_import.html", "admin/acls_import"},
		{"admin/derp_config.html", "admin/derp_config"},
		{"admin/user_subnet.html", "admin/user_subnet"},
		{"admin/user_control_plane.html", "admin/user_control_plane"},
		// Top-level pages WITHOUT underscore — must pass
		// through unchanged (the regex covers the no-op case).
		{"admin/devices.html", "admin/devices"},
		{"admin/acls.html", "admin/acls"},
		{"admin/audit.html", "admin/audit"},
		{"admin/backup.html", "admin/backup"},
		{"admin/integrations.html", "admin/integrations"},
		{"admin/headscale.html", "admin/headscale"},
		{"admin/headplane.html", "admin/headplane"},
		{"admin/telegram.html", "admin/telegram"},
		{"admin/tailscale.html", "admin/tailscale"},
		{"admin/services.html", "admin/services"},
		{"admin/meshes.html", "admin/meshes"},
		{"admin/subnets.html", "admin/subnets"},
		{"admin/update.html", "admin/update"},
		{"admin/users.html", "admin/users"},
		{"admin/invites.html", "admin/invites"},
		{"admin/settings.html", "admin/settings"},
		// Pages that DO use underscore in BOTH URL and template
		// (kept as-is for backward-compat with the v0.32.x era).
		{"admin/system_tests.html", "admin/system_tests"},
		{"admin/headscale_acl.html", "admin/headscale_acl"},
	}
	for _, tc := range cases {
		got := pageFromName(tc.templateName)
		if got != tc.want {
			t.Errorf("pageFromName(%q) = %q, want %q", tc.templateName, got, tc.want)
		}
	}
}

// TestPageFromName_NonAdminPagesUnchanged pins the non-admin
// paths so the underscore-to-hyphen translation doesn't leak
// into dashboard / user / my / help pages.
func TestPageFromName_NonAdminPagesUnchanged(t *testing.T) {
	cases := []struct {
		templateName string
		want         string
	}{
		{"dashboard.html", "dashboard"},
		{"help.html", "help"},
		{"user/devices.html", "my/devices"},
		{"user/preauth_result.html", "my/devices"},
		{"user/exit_nodes.html", "my/exit-nodes"},
		{"user/account.html", "user/account"},
		{"user/keys.html", "user/keys"},
		{"user/exit_rules_help.html", "user/exit_rules_help"},
		{"my_tokens.html", "my_tokens"},
	}
	for _, tc := range cases {
		got := pageFromName(tc.templateName)
		if got != tc.want {
			t.Errorf("pageFromName(%q) = %q, want %q", tc.templateName, got, tc.want)
		}
	}
}

// TestSectionPageSet_HyphenPageIDs is the upstream pin: the
// 3 top-level pages with URL-vs-template mismatch must be
// recognised by sectionPageSet() as belonging to their
// section (and only their section — no leakage to others).
// If a future refactor changes sectionPageSet() to use
// underscore form by mistake, this test catches it before
// the page goes live and the active-link silently breaks.
func TestSectionPageSet_HyphenPageIDs(t *testing.T) {
	cases := []struct {
		page         string
		wantSections map[string]bool
	}{
		// exit-nodes → Devices & Nodes only
		{"admin/exit-nodes", map[string]bool{
			"InSectionDevices":      true,
			"InSectionAccess":       false,
			"InSectionHealth":       false,
			"InSectionIntegrations": false,
			"InSectionData":         false,
			"InSectionSettings":     false,
		}},
		// exit-rules → Access Control only
		{"admin/exit-rules", map[string]bool{
			"InSectionDevices":      false,
			"InSectionAccess":       true,
			"InSectionHealth":       false,
			"InSectionIntegrations": false,
			"InSectionData":         false,
			"InSectionSettings":     false,
		}},
		// control-planes → Data only
		{"admin/control-planes", map[string]bool{
			"InSectionDevices":      false,
			"InSectionAccess":       false,
			"InSectionHealth":       false,
			"InSectionIntegrations": false,
			"InSectionData":         true,
			"InSectionSettings":     false,
		}},
	}
	for _, tc := range cases {
		got := sectionPageSet(tc.page)
		for section, want := range tc.wantSections {
			if got[section] != want {
				t.Errorf("sectionPageSet(%q)[%q] = %v, want %v (full: %+v)",
					tc.page, section, got[section], want, got)
			}
		}
	}
}

// TestSectionLabel_HyphenPageIDs pins that sectionLabel
// returns the i18n key for each of the 3 hyphen-form pages.
// Without this, the breadcrumb is empty for exit-nodes /
// exit-rules / control-planes — the operator can't tell
// which section they're in.
func TestSectionLabel_HyphenPageIDs(t *testing.T) {
	cases := []struct {
		page string
		want string
	}{
		{"admin/exit-nodes", "nav.section_devices"},
		{"admin/exit-rules", "nav.section_access"},
		{"admin/control-planes", "nav.section_data"},
	}
	for _, tc := range cases {
		got := sectionLabel(tc.page)
		if got != tc.want {
			t.Errorf("sectionLabel(%q) = %q, want %q", tc.page, got, tc.want)
		}
	}
}

// TestPageLabel_HyphenPageIDs is the breadcrumb's "page"
// segment pin. Without this, the breadcrumb's last segment
// is empty for the 3 hyphen-form pages.
func TestPageLabel_HyphenPageIDs(t *testing.T) {
	cases := []struct {
		page string
		want string
	}{
		{"admin/exit-nodes", "nav.exit_nodes_admin"},
		{"admin/exit-rules", "nav.exit_rules_all"},
		{"admin/control-planes", "nav.control_planes"},
	}
	for _, tc := range cases {
		got := pageLabel(tc.page)
		if got != tc.want {
			t.Errorf("pageLabel(%q) = %q, want %q", tc.page, got, tc.want)
		}
	}
}
