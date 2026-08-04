package admin

// system_tests_handlers.go — HTTP handlers for
// /admin/system_tests (v0.33.0). Kept in a separate file
// from system_tests.go so the registry + persistence code
// can be unit-tested without the http package import.

import (
	"fmt"
	"net/http"

	"skygate/internal/i18n"
)

// GetAdminSystemTests renders /admin/system_tests.
//
// Shows the test registry as a grid (category, name,
// description, last-run status), a "Run all" button, and
// the last 20 runs from system_tests_runs as a history
// strip. The /admin/system_tests page is the operator's
// single-pane "is everything OK?" view.
func (s *Service) GetAdminSystemTests(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	recent, _ := s.ListRecentRuns(r.Context(), 20)
	s.Backend.RenderWithLayout(w, r, "admin/system_tests.html", c, map[string]any{
		"Page":        "admin/system_tests",
		"Title":       i18n.T(lang, "title.admin_system_tests"),
		"Tests":       TestRegistry,
		"RecentRuns":  recent,
		"FlashError":  r.URL.Query().Get("err"),
	})
}

// PostAdminSystemTestsRun runs the test suite, persists the
// result, and re-renders the page with the live result +
// history. Admin-only.
func (s *Service) PostAdminSystemTestsRun(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	results, summary := s.RunAllTests(r.Context())
	if _, err := s.PersistRun(r.Context(), results, summary, c.UserID); err != nil {
		http.Redirect(w, r, "/admin/system_tests?err="+err.Error(),
			http.StatusSeeOther)
		return
	}
	// Audit log: a concise summary + names of failing tests.
	detail := fmt.Sprintf("pass=%d fail=%d skip=%d",
		summary.Pass, summary.Fail, summary.Skip)
	for _, res := range results {
		if res.Status == SystemTestFail {
			detail += " failed:" + res.Name
		}
	}
	if s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "system_tests_run", detail)
	}
	// Re-render with the live result + updated history.
	recent, _ := s.ListRecentRuns(r.Context(), 20)
	lang := s.I18n.LangFromRequest(r)
	s.Backend.RenderWithLayout(w, r, "admin/system_tests.html", c, map[string]any{
		"Page":         "admin/system_tests",
		"Title":        i18n.T(lang, "title.admin_system_tests"),
		"Tests":        TestRegistry,
		"RecentRuns":   recent,
		"LiveResults":  results,
		"LiveSummary":  summary,
		"FlashSuccess": fmt.Sprintf("pass=%d fail=%d", summary.Pass, summary.Fail),
	})
}
