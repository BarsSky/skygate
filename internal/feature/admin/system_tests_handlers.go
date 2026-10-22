package admin

// system_tests_handlers.go — HTTP handlers for
// /admin/system_tests (v0.33.0). Kept in a separate file
// from system_tests.go so the registry + persistence code
// can be unit-tested without the http package import.
//
// B394 — the page is a DIAGNOSTIC view, and it has ONE data builder.
// GET and POST used to build separate maps: the POST one was much smaller, so the
// template's `{{if .DNSAutoUpdateEnabled}}` read a missing key (nil → falsy) and
// every run reported the DNS auto-updater as «ОТКЛЮЧЕНО» while it was enabled.
// Both handlers now go through systemTestsPageData, so a second map cannot drift
// back in.

import (
	"net/http"
	"time"

	"encoding/json"
	"errors"
	"strings"

	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/i18n"
)

// GetAdminSystemTests renders /admin/system_tests.
//
// Shows the test registry as a grid (severity, category, name, description,
// last-run status, remediation), a "Run all" button, and the last 20 runs from
// system_tests_runs as a history strip.
//
// 2026-08-06 v0.33.1.18 — also renders the DNS-autoupdater toggle so the operator
// can enable/disable domain→/32 refresh from the web UI without editing .env or
// restarting skygate.
//
// 2026-08-18 (TD-8, v1.4.4) — added a "History" tab (?tab=history) that
// aggregates per-test pass/fail/skip counts across the last 7/30/all days.
func (s *Service) GetAdminSystemTests(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	recent, _ := s.ListRecentRuns(r.Context(), 20)
	// 2026-08-09 v0.33.1.26 — load the most recent run's per-test results so the
	// page shows persistent PASS/FAIL/SKIP icons on initial load, not just after
	// "Run all" was clicked.
	last, lastErr := s.ListLastRunWithResults(r.Context())
	if lastErr != nil && s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "system_tests_last_parse_error", lastErr.Error())
	}
	// TD-8 (2026-08-18): ?tab= and ?window=. Defaults: tab=tests, window=7d. An
	// unknown value falls back to the default — a typo'd URL must not 500.
	tab := r.URL.Query().Get("tab")
	if tab != "history" {
		tab = "tests"
	}
	windowStr := r.URL.Query().Get("window")
	now := time.Now().UTC()
	hw := ParseHistoryWindow(windowStr, now)
	// Always compute the history so the operator can switch tabs without a second
	// round-trip (the data is small — at most a few hundred runs even on busy
	// deployments).
	history, _ := s.ComputeTestHistory(r.Context(), hw.Since, hw.Until)
	s.Backend.RenderWithLayout(w, r, "admin/system_tests.html", c,
		s.systemTestsPageData(r, recent, last, history, hw, tab, windowStr, nil, nil, ""))
}

// systemTestsPageData is the ONE description of the /admin/system_tests page.
//
// live/liveSummary are non-nil only on the POST path (the run that just
// finished); every other key is computed identically for both paths, which is
// what makes the false «ОТКЛЮЧЕНО» impossible to reintroduce.
func (s *Service) systemTestsPageData(
	r *http.Request,
	recent []SystemRunSummary,
	last *LastRunWithResults,
	history TestHistory,
	hw HistoryWindow,
	tab, windowStr string,
	live []SystemTestResult,
	liveSummary *SystemRunSummary,
	flashSuccess string,
) map[string]any {
	lang := s.I18n.LangFromRequest(r)
	now := time.Now().UTC()
	// Effective DNS-autoupdate state. Precedence (highest first):
	//   1. global_settings row 'dns_autoupdate_enabled' (after the first UI toggle)
	//   2. config.DNSAutoUpdateEnabled (env SKYGATE_DNS_AUTOUPDATE_ENABLED)
	dnsAutoEnabled := db.GetGlobalSettingBool(s.dbc(), globalSettingsKeyDNSAutoUpdate, s.Cfg.DNSAutoUpdateEnabled)
	// B308 (v1.5.73): the effective per-domain re-resolve interval, parsed with the
	// updater's OWN policy so the number on the page is the number the goroutine uses.
	dnsResolveRaw, _ := db.GetGlobalSetting(s.dbc(), exit_rules.SettingDomainResolveIntervalSec, "")
	dnsResolveInterval := exit_rules.ParseDomainResolveInterval(dnsResolveRaw)
	// B231: the preferred-exit auto-reconciler, same precedence model.
	prefReconcileEnabled := db.GetGlobalSettingBool(s.dbc(), globalSettingsKeyPrefReconcile, s.Cfg.PrefReconcileEnabled)

	data := map[string]any{
		"Page":  "admin/system_tests",
		"Title": i18n.T(lang, "title.admin_system_tests"),
		// B306 (v1.5.71): the catalogue is the static registry plus one generated
		// test per registered module (status + health), so the page, "Run all" and
		// the per-module button all describe the same set. B394: every entry carries
		// severity + remediation + doc link (annotateTest).
		"Tests":      s.AllTests(),
		"ModuleRows": s.ModuleTestRows(),
		"RecentRuns": recent,
		"FlashError": r.URL.Query().Get("err"),
		// B394: the id of the run the page should follow (the POST redirects to
		// ?run=<id>). Empty on a plain GET, which means "show the persisted run".
		"RunID": r.URL.Query().Get("run"),
		// B394 filters, server-side only for the counter line; the table itself is
		// filtered in the browser so a click costs no round trip.
		"FilterStatus":              r.URL.Query().Get("status"),
		"FilterCategory":            r.URL.Query().Get("category"),
		"FlashSuccess":              flashSuccess,
		"DNSAutoUpdateEnabled":      dnsAutoEnabled,
		"DNSResolveIntervalSec":     int64(dnsResolveInterval.Seconds()),
		"DNSResolveIntervalHuman":   exit_rules.DomainResolveIntervalLabel(dnsResolveInterval),
		"FlashDNSInterval":          r.URL.Query().Get("dns_interval"),
		"PrefReconcileEnabled":      prefReconcileEnabled,
		"FlashDNSAutoToggled":       r.URL.Query().Get("dns_auto_toggled"),
		"FlashPrefReconcileToggled": r.URL.Query().Get("pref_reconcile_toggled"),
		"Now":                       now,
		"Tab":                       tab,
		"Window":                    windowStr,
		"WindowLabel":               hw.Label,
		"History":                   history,
	}
	if last != nil {
		data["LastRunID"] = last.RunID
		// B408.1: the row's verdict comes from the NEWEST run that contains it, not
		// from the newest run overall — a single-test re-run is persisted now, and
		// reading only that row would blank the other rows.
		if perTest, err := s.ListLastResultsPerTest(r.Context(), 50); err == nil && len(perTest) > 0 {
			data["LastResults"] = perTest
			data["LastResultsPerTest"] = true
		} else {
			data["LastResults"] = last.Results
		}
		data["LastSummary"] = last.Summary
		data["LastRunStartedAt"] = last.StartedAt
		data["LastRunFinishedAt"] = last.FinishedAt
		data["LastRunAgeSec"] = int64(time.Since(last.StartedAt).Seconds())
	}
	if live != nil {
		data["LiveResults"] = live
	}
	if liveSummary != nil {
		data["LiveSummary"] = liveSummary
	}
	return data
}

// PostAdminSystemTestsRun runs the test suite, persists the result, and
// re-renders the page with the live result + history. Admin-only.
// PostAdminSystemTestsRun starts the catalogue in the background and redirects to
// the page, which then polls the progress endpoint.
//
// B394 — this used to run everything INLINE: the browser waited for the slowest
// test with no progress, no partial results and no way to run a single test. The
// run is now asynchronous (?run=<id> carries the id so the page knows what to
// follow) and one test can be re-run on its own with ?test=<name>.
func (s *Service) PostAdminSystemTestsRun(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	only := strings.TrimSpace(r.FormValue("test"))
	state, err := s.StartSystemRun(c.UserID, c.Username, only)
	if err != nil {
		code := http.StatusInternalServerError
		msg := err.Error()
		if errors.Is(err, ErrSystemRunInFlight) {
			code = http.StatusConflict
			msg = "another system-test run is already in progress — wait for it to finish (" + msg + ")"
		}
		data := s.systemTestsPageData(r, mustRecentRuns(r, s), mustLastRun(r, s),
			TestHistory{}, ParseHistoryWindow("", time.Now().UTC()), "tests", "", nil, nil, "")
		data["FlashError"] = msg
		w.WriteHeader(code)
		s.Backend.RenderWithLayout(w, r, "admin/system_tests.html", c, data)
		return
	}
	if s.Backend != nil {
		target := "all tests"
		if only != "" {
			target = "test " + only
		}
		s.Backend.Audit(c.UserID, c.Username, "system_tests_run", "started "+target+" (run "+state.ID+")")
	}
	http.Redirect(w, r, "/admin/system_tests?run="+state.ID, http.StatusSeeOther)
}

// GetAdminSystemTestsRunJSON answers the poller. It is deliberately tiny: the page
// only needs done/current/total/results, and a JSON body keeps the template dumb.
func (s *Service) GetAdminSystemTestsRunJSON(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	want := strings.TrimSpace(r.URL.Query().Get("run"))
	state := s.CurrentSystemRun()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if state == nil || (want != "" && state.ID != want) {
		// Unknown or expired id: say so instead of returning an empty success —
		// the poller stops and the page falls back to the persisted last run.
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "no such run", "run": want})
		return
	}
	_ = json.NewEncoder(w).Encode(state)
}

// mustRecentRuns / mustLastRun keep the error paths short: a failed read means the
// page renders without that section, which is what the GET handler does too.
func mustRecentRuns(r *http.Request, s *Service) []SystemRunSummary {
	out, _ := s.ListRecentRuns(r.Context(), 20)
	return out
}

func mustLastRun(r *http.Request, s *Service) *LastRunWithResults {
	out, _ := s.ListLastRunWithResults(r.Context())
	return out
}
