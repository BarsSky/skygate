// internal/feature/admin/system_tests_modules_b306.go — B306 (v1.5.71).
//
// The operator asked to «пройтись по тестам системы и расширить их давая
// возможность полностью контролировать модули проекта и получать уведомления по
// неисправности или некорректном поведении».
//
// Before this file the test catalogue was a static list of in-process checks
// (network/db/headscale/disk/…) and the project's MODULES lived on a separate page
// with their own state machine: the operator could install/start a module, but
// nothing in the test battery ever asked "is this module actually working?", and a
// module that entered StateError was visible only as a badge on /admin/modules.
//
// B306 makes every registered module a first-class test:
//
//   * MODULE tests are generated from the live Manager (one per module, named
//     "module.<name>", category "modules"), so a module added later is covered
//     without touching the catalogue;
//   * a module that is not installed reports SKIP — a fresh install must not show
//     a wall of red for features the operator never turned on;
//   * an installed module reports PASS/FAIL from its own Status()+Health(), with
//     the module's LastError and its failed health checks in the output, so the
//     failure is actionable instead of "something is wrong";
//   * the failures flow into the B305 monitoring inbox under the module's own
//     source ("module:<name>"), and a recovery RESOLVES that event — the operator
//     is notified once per module fault, not once per run;
//   * the per-module control the operator asked for is a single POST
//     (/admin/modules/{name}/test) that runs exactly that module's test, persists
//     the run, reports to the inbox and answers with a flash naming the result.
package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/module"
)

// moduleTestCategory is the category the generated tests carry; the page groups
// by it and the per-module run filters on it.
const moduleTestCategory = "modules"

// moduleTestNamePrefix makes a module test recognisable in the persisted results
// (the inbox producer routes "module.<name>" to the module's own source).
const moduleTestNamePrefix = "module."

// ModuleNameFromTest extracts the module name from a generated test name
// ("module.tailscale" → "tailscale", anything else → "").
func ModuleNameFromTest(testName string) string {
	if !strings.HasPrefix(testName, moduleTestNamePrefix) {
		return ""
	}
	return strings.TrimPrefix(testName, moduleTestNamePrefix)
}

// ModuleTestRow is the per-module line the tests page renders (B306): the module's
// current state and health next to the button that re-checks it, so "полностью
// контролировать модули проекта" is one click from the page that measures them.
type ModuleTestRow struct {
	Name        string
	TestName    string
	State       string
	InstallMode string
	Enabled     bool
	Healthy     bool
	LastError   string
	Link        string
}

// ModuleTestRows builds the module card's view model from the live Manager.
func (s *Service) ModuleTestRows() []ModuleTestRow {
	if s == nil || s.Modules == nil {
		return nil
	}
	infos := s.Modules.List()
	out := make([]ModuleTestRow, 0, len(infos))
	for _, info := range infos {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		row := ModuleTestRow{
			Name:        name,
			TestName:    moduleTestNamePrefix + name,
			State:       info.State,
			InstallMode: info.InstallMode,
			Enabled:     info.Enabled,
			LastError:   info.LastError,
			Link:        "/admin/modules/" + name,
		}
		if mod, ok := s.Modules.Get(name); ok && mod != nil {
			row.Healthy = mod.Health().Healthy
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ModuleTests returns one test per registered module (B306). It is a method (not
// a package variable) because the module set is a runtime property: the Manager
// holds whatever main.go registered, and a test listed for a module that is not
// registered would be a lie.
func (s *Service) ModuleTests() []SystemTestDef {
	if s == nil || s.Modules == nil {
		return nil
	}
	infos := s.Modules.List()
	out := make([]SystemTestDef, 0, len(infos))
	for _, info := range infos {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		mod, ok := s.Modules.Get(name)
		if !ok || mod == nil {
			continue
		}
		out = append(out, s.moduleTestFor(name, mod, info))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// moduleTestFor builds the test closure for one module. The description names the
// install mode and the enabled flag, which is what the operator needs to tell an
// installed-but-disabled module from one that is simply not there.
func (s *Service) moduleTestFor(name string, mod module.Module, info module.ModuleInfo) SystemTestDef {
	desc := fmt.Sprintf("module %s: state + health (enabled=%v", name, info.Enabled)
	if info.InstallMode != "" {
		desc += ", mode=" + info.InstallMode
	}
	desc += ")"
	return SystemTestDef{
		Name:        moduleTestNamePrefix + name,
		Category:    moduleTestCategory,
		Description: desc,
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			return runModuleCheck(name, mod)
		},
	}
}

// runModuleCheck is the pure part of a module test: it reads the module's status
// and health and turns them into (status, human output). Extracted so it can be
// unit-tested with a stub module (no Manager, no DB).
func runModuleCheck(name string, mod module.Module) (SystemTestStatus, string) {
	if mod == nil {
		return SystemTestSkip, "module " + name + " is not registered in this build"
	}
	st := mod.Status()
	health := mod.Health()

	// Not installed = nothing to verify. A module the operator never turned on
	// must not make the catalogue look broken (the same rule the live-state checks
	// follow: SKIP, never FAIL).
	if st.State == module.StateNotInstalled {
		return SystemTestSkip, fmt.Sprintf("module %s: not installed (state=%s)", name, st.State)
	}

	// Build the evidence first, so a PASS and a FAIL report the same fields.
	var b strings.Builder
	fmt.Fprintf(&b, "module %s: state=%s healthy=%v", name, st.State, health.Healthy)
	if !st.StartedAt.IsZero() {
		fmt.Fprintf(&b, " started=%s", st.StartedAt.UTC().Format(time.RFC3339))
	}
	if len(health.Checks) > 0 {
		keys := make([]string, 0, len(health.Checks))
		for k := range health.Checks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(" checks=")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "%s:%v", k, health.Checks[k])
		}
	}
	for _, k := range sortedInfoKeys(st.Info) {
		fmt.Fprintf(&b, " %s=%s", k, st.Info[k])
	}
	if st.LastError != "" {
		fmt.Fprintf(&b, " last_error=%s", st.LastError)
	}

	// The failure ladder: a module in StateError, or one that is unhealthy while
	// claiming to run, is a real problem the operator must see.
	switch {
	case st.State == module.StateError:
		return SystemTestFail, b.String() + " — module is in an error state"
	case st.State == module.StateRunning && !health.Healthy:
		return SystemTestFail, b.String() + " — module claims to run but reports unhealthy"
	case st.State == module.StateStopped:
		// Stopped on purpose (or by a failed start) is worth knowing about, but it
		// is a WARNING in the inbox rather than a red test: the module is not
		// supposed to serve traffic in that state.
		return SystemTestSkip, b.String() + " — module is stopped"
	default:
		return SystemTestPass, b.String()
	}
}

// sortedInfoKeys returns the module's Info keys in a stable order (the map is
// rendered into the test output, and a random order would make two identical runs
// look different).
func sortedInfoKeys(info map[string]string) []string {
	keys := make([]string, 0, len(info))
	for k := range info {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AllTests is the catalogue the page and the runner use: the static registry plus
// the module tests generated from the live Manager.
func (s *Service) AllTests() []SystemTestDef {
	out := make([]SystemTestDef, 0, len(TestRegistry)+4)
	out = append(out, TestRegistry...)
	out = append(out, s.ModuleTests()...)
	return out
}

// RunModuleTests runs exactly one module's tests (the per-module "Проверить"
// button). Returns nil when the module is not registered, so the handler can say
// so instead of reporting an empty success.
func (s *Service) RunModuleTests(ctx context.Context, name string) ([]SystemTestResult, *SystemRunSummary) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	var tests []SystemTestDef
	for _, t := range s.ModuleTests() {
		if ModuleNameFromTest(t.Name) == name {
			tests = append(tests, t)
		}
	}
	if len(tests) == 0 {
		return nil, nil
	}
	results := make([]SystemTestResult, 0, len(tests))
	summary := &SystemRunSummary{StartedAt: time.Now().UTC()}
	for _, t := range tests {
		testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		start := time.Now()
		status, output := t.Run(testCtx)
		cancel()
		results = append(results, SystemTestResult{
			Name:     t.Name,
			Category: t.Category,
			Status:   status,
			Output:   output,
			Duration: time.Since(start).String(),
		})
		switch status {
		case SystemTestPass:
			summary.Pass++
		case SystemTestFail:
			summary.Fail++
		case SystemTestSkip:
			summary.Skip++
		}
	}
	summary.FinishedAt = time.Now().UTC()
	summary.TotalCount = len(results)
	summary.Duration = summary.FinishedAt.Sub(summary.StartedAt).String()
	return results, summary
}

// RunModuleTestAction is the "test" action of the /admin/modules dispatcher
// (B306). It runs the module's generated test, persists the run (so it appears in
// the history strip) and redirects to the module's detail page with a flash
// naming the outcome; a failure reaches the monitoring inbox through the same
// ReportRunToMonitor path every run uses.
//
// It lives next to the test generator rather than in modules.go so the whole
// "module = test" idea is in one file.
func (s *Service) RunModuleTestAction(w http.ResponseWriter, r *http.Request, c *auth.Claims, name string) {
	results, summary := s.RunModuleTests(r.Context(), name)
	if results == nil {
		s.redirectWithModuleError(w, r, name, fmt.Errorf("module %s is not registered in this build", name))
		return
	}
	if _, err := s.PersistRun(r.Context(), results, summary, c.UserID); err != nil {
		s.redirectWithModuleError(w, r, name, fmt.Errorf("run could not be stored: %w", err))
		return
	}
	outcome := "проверка пройдена"
	switch {
	case summary.Fail > 0:
		outcome = "ОБНАРУЖЕНА НЕИСПРАВНОСТЬ"
	case summary.Pass == 0:
		outcome = "проверять нечего (модуль не установлен)"
	}
	for _, res := range results {
		if res.Status == SystemTestFail {
			outcome += " — " + truncateForEvent(res.Output, 160)
			break
		}
	}
	msg := fmt.Sprintf("Модуль %s: %s (pass=%d fail=%d skip=%d). Событие — на /admin/monitor.",
		name, outcome, summary.Pass, summary.Fail, summary.Skip)
	http.Redirect(w, r, fmt.Sprintf("/admin/modules/%s?ok=%s", name, url.QueryEscape(msg)), http.StatusSeeOther)
}
