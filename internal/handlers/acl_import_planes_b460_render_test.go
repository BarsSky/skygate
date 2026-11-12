package handlers

// acl_import_planes_b460_render_test.go — B460: the ACL import page must actually
// RENDER the planes it will push to, and must render EVERY plane of a failed
// apply.
//
// Why a render test and not just the pure helpers (which live in the admin
// package and have their own file): the defect this block fixes was a page that
// showed the wrong story, and a template typo or an action the funcmap cannot
// resolve fails only at EXECUTE time — TestLoadTemplates parses and would not
// catch it. The partial-apply fix is likewise only real if the page prints the
// second plane; asserting it on the rendered HTML is the whole contract.

import (
	"strings"
	"testing"

	"skygate/internal/feature/admin"
	"skygate/internal/i18n"
)

const b460Imported = `{"acls":[],"tagOwners":{},"groups":{},"ssh":{}}`

// b460Data is the dry-run shape the handler produces after B460.
func b460Data() map[string]any {
	return map[string]any{
		"Page":           "admin/acls",
		"Title":          "ACL",
		"CSRF":           "b460-test-token",
		"FlashSuccess":   "",
		"FlashError":     "",
		"CurrentPolicy":  `{"acls":[]}`,
		"ImportedPolicy": b460Imported,
		"SameAsCurrent":  false,
		"CurrentHash":    "aaaa1111",
		"ImportedHash":   "bbbb2222",
		"PlaneVersions": []admin.PlaneVersion{
			{URL: "http://primary.example.com:50444", Version: "0.29.2", Via: "GET /api/v1/version"},
			{URL: "http://second.example.com:50444", Reason: "the version monitor samples only the primary headscale"},
		},
		"PlaneMismatch": "0.29.0 (http://second.example.com:50444); 0.29.2 (http://primary.example.com:50444)",
		"PlaneUnknown":  1,
	}
}

func b460Render(t *testing.T, data map[string]any, lang string) string {
	t.Helper()
	tpl := LoadTemplates()
	if tpl == nil {
		t.Fatal("LoadTemplates returned nil")
	}
	i18n.SetGlobal(i18n.New())
	i18n.SetLang(lang)
	var sb strings.Builder
	if err := tpl.ExecuteTemplate(&sb, "body-admin-acls_import", data); err != nil {
		t.Fatalf("%s: execute body-admin-acls_import: %v", lang, err)
	}
	out := sb.String()
	if strings.Contains(out, "{{") {
		t.Errorf("%s: rendered page still has unexpanded template actions", lang)
	}
	return out
}

// TestB460_PlaneTableRenders — every plane, its version and the reason for an
// undetermined one reach the page, in both languages.
func TestB460_PlaneTableRenders(t *testing.T) {
	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		out := b460Render(t, b460Data(), lang)

		for _, want := range []string{
			"http://primary.example.com:50444",
			"http://second.example.com:50444",
			"0.29.2",
			"the version monitor samples only the primary headscale",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: rendered page is missing %q", lang, want)
			}
		}
		// The mismatch warning must carry the actual disagreement, not just a label.
		if !strings.Contains(out, "0.29.0") {
			t.Errorf("%s: the mismatch warning does not name the other version", lang)
		}
		// Mobile: a table outside .table-wrap scrolls the whole page (B105).
		if !strings.Contains(out, `class="table-wrap"`) {
			t.Errorf("%s: no .table-wrap around the plane table", lang)
		}
	}
}

// TestB460_ApplyReportListsEveryPlane — THE fix. A partially failed apply must
// show the plane that succeeded AND the plane that failed; pre-B460 the page
// answered with the first error and said nothing about the rest.
func TestB460_ApplyReportListsEveryPlane(t *testing.T) {
	data := b460Data()
	data["ApplyReport"] = []admin.ApplyReportRow{
		{URL: "http://primary.example.com:50444", OK: true, Version: 11},
		{URL: "http://second.example.com:50444", OK: false, Version: 12, Detail: `headscale: 400 {"message":"src=tag not found"}`},
	}
	data["ApplyOK"] = 1
	data["ApplyFailed"] = 1
	data["ApplyAttempted"] = 2

	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		out := b460Render(t, data, lang)

		if !strings.Contains(out, i18n.T(lang, "acls.import_report_banner")) {
			t.Errorf("%s: the partial-apply banner is absent", lang)
		}
		if !strings.Contains(out, i18n.T(lang, "acls.import_report_ok")) {
			t.Errorf("%s: the succeeded plane is not marked applied", lang)
		}
		if !strings.Contains(out, i18n.T(lang, "acls.import_report_fail")) {
			t.Errorf("%s: the failed plane is not marked failed", lang)
		}
		// Both planes must be listed — this is the assertion the old code fails.
		for _, url := range []string{"http://primary.example.com:50444", "http://second.example.com:50444"} {
			if !strings.Contains(out, url) {
				t.Errorf("%s: report does not list %q", lang, url)
			}
		}
		if !strings.Contains(out, "src=tag not found") {
			t.Errorf("%s: the failure detail is not shown", lang)
		}
		// The operator must be told that a partial apply is not rolled back.
		if !strings.Contains(out, i18n.T(lang, "acls.import_report_hint")) {
			t.Errorf("%s: the 'not rolled back' hint is absent", lang)
		}
	}
}

// TestB460_NoReportWithoutApply — the plain dry-run must not claim an apply
// happened.
func TestB460_NoReportWithoutApply(t *testing.T) {
	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		out := b460Render(t, b460Data(), lang)
		if strings.Contains(out, i18n.T(lang, "acls.import_report_banner")) {
			t.Errorf("%s: a dry-run without ApplyReport rendered the apply banner", lang)
		}
		if !strings.Contains(out, "http://primary.example.com:50444") {
			t.Errorf("%s: the dry-run lost its plane table", lang)
		}
	}
}

// TestB460_UnknownVersionIsNamedNotBlank — a plane whose version is unknown must
// say WHY. An empty version cell reading as "fine" is the defect class this page
// exists to remove.
func TestB460_UnknownVersionIsNamedNotBlank(t *testing.T) {
	data := b460Data()
	data["PlaneVersions"] = []admin.PlaneVersion{
		{URL: "http://only.example.com:50444", Reason: "no version source is wired in this build"},
	}
	data["PlaneUnknown"] = 1
	data["PlaneMismatch"] = ""

	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		out := b460Render(t, data, lang)
		if !strings.Contains(out, "no version source is wired in this build") {
			t.Errorf("%s: the unknown-version reason is not rendered", lang)
		}
		if !strings.Contains(out, i18n.T(lang, "acls.import_planes_unknown_hint")) {
			t.Errorf("%s: the unknown-version hint is absent", lang)
		}
	}
}
