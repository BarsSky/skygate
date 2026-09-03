package admin

// system_tests_handlers.go — HTTP handlers for
// /admin/system_tests (v0.33.0). Kept in a separate file
// from system_tests.go so the registry + persistence code
// can be unit-tested without the http package import.

import (
	"fmt"
	"net/http"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// GetAdminSystemTests renders /admin/system_tests.
//
// Shows the test registry as a grid (category, name,
// description, last-run status), a "Run all" button, and
// the last 20 runs from system_tests_runs as a history
// strip. The /admin/system_tests page is the operator's
// single-pane "is everything OK?" view.
//
// 2026-08-06 v0.33.1.18 — also renders the DNS-autoupdater
// toggle so the operator can enable/disable domain→/32
// refresh from the web UI without editing .env or
// restarting skygate. The DB-backed state overrides the
// env var on the next autoupdate tick (5m default).
//
// 2026-08-18 (TD-8, v1.4.4) — added a "History" tab
// (?tab=history) that aggregates per-test pass/fail/skip
// counts across the last 7/30/all days. The Tests tab
// (?tab=tests, default) is the original grid view. The
// tab UI is just a CSS link bar; the actual data switch
// happens in the template.
func (s *Service) GetAdminSystemTests(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	recent, _ := s.ListRecentRuns(r.Context(), 20)
	// 2026-08-09 v0.33.1.26 — load the most recent run's
	// per-test results so the page shows persistent
	// PASS/FAIL/SKIP icons on initial load, not just after
	// "Run all" was clicked. If the parse fails (malformed
	// JSON), the summary counts are still rendered; the
	// error is logged to the audit log so the operator can
	// see "the last run's results_json was corrupt".
	last, lastErr := s.ListLastRunWithResults(r.Context())
	if lastErr != nil && s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "system_tests_last_parse_error", lastErr.Error())
	}
	// 2026-08-06 v0.33.1.18 — read the effective DNS-autoupdate
	// state. Order of precedence (highest first):
	//   1. global_settings row 'dns_autoupdate_enabled' (after
	//      the first UI toggle the DB value wins)
	//   2. config.DNSAutoUpdateEnabled (env var
	//      SKYGATE_DNS_AUTOUPDATE_ENABLED; default true)
	// 2026-08-06 note: GetGlobalSettingBool needs a cfg to
	// know what the fallback env value is. We pass
	// s.Cfg.DNSAutoUpdateEnabled so the operator sees the
	// SAME effective state the goroutine would use.
	dnsAutoEnabled := db.GetGlobalSettingBool(s.dbc(), globalSettingsKeyDNSAutoUpdate, s.Cfg.DNSAutoUpdateEnabled)
	// 2026-09-03: v1.5.2 (B231) — read the effective
	// preferred-exit auto-reconciler state. Same
	// precedence model as the DNS-autoupdater: DB row
	// wins after the first UI toggle; otherwise the env
	// (default true). The /admin/system_tests page
	// uses this to render the toggle's current state.
	prefReconcileEnabled := db.GetGlobalSettingBool(s.dbc(), globalSettingsKeyPrefReconcile, s.Cfg.PrefReconcileEnabled)
	// TD-8 (2026-08-18): read the ?tab= and ?window= query
	// params. Default tab is "tests" (the original grid);
	// default window is "7d". Unknown values fall back to
	// the defaults (no error to the operator — a typo'd
	// URL shouldn't 500).
	tab := r.URL.Query().Get("tab")
	if tab != "history" {
		tab = "tests"
	}
	windowStr := r.URL.Query().Get("window")
	now := time.Now().UTC()
	hw := ParseHistoryWindow(windowStr, now)
	// 2026-08-18 (TD-8): always compute the history so the
	// operator can switch tabs without a second round-trip
	// (the data is small — at most a few hundred runs even
	// on busy deployments). The cost is dominated by the
	// JSON parse inside ComputeTestHistory; the page render
	// is the next bottleneck.
	history, _ := s.ComputeTestHistory(r.Context(), hw.Since, hw.Until)
	data := map[string]any{
		"Page":                  "admin/system_tests",
		"Title":                 i18n.T(lang, "title.admin_system_tests"),
		"Tests":                 TestRegistry,
		"RecentRuns":            recent,
		"FlashError":            r.URL.Query().Get("err"),
		"DNSAutoUpdateEnabled":  dnsAutoEnabled,
		"PrefReconcileEnabled": prefReconcileEnabled,
		"FlashDNSAutoToggled":   r.URL.Query().Get("dns_auto_toggled"),
		"FlashPrefReconcileToggled": r.URL.Query().Get("pref_reconcile_toggled"),
		"Now":                   now,
		"Tab":                   tab,
		"Window":                windowStr,
		"WindowLabel":           hw.Label,
		"History":               history,
	}
	if last != nil {
		data["LastRunID"] = last.RunID
		data["LastResults"] = last.Results
		data["LastSummary"] = last.Summary
		data["LastRunStartedAt"] = last.StartedAt
		data["LastRunFinishedAt"] = last.FinishedAt
		data["LastRunAgeSec"] = int64(time.Since(last.StartedAt).Seconds())
	}
	s.Backend.RenderWithLayout(w, r, "admin/system_tests.html", c, data)
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
