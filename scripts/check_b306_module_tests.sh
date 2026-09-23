#!/usr/bin/env bash
# check_b306_module_tests.sh
#
# 2026-09-23 (B306, v1.5.71) — the project's modules are first-class system tests.
#
# Operator ask: «Отдельно следует пройтись по тестам системы и расширить их давая
# возможность полностью контролировать модули проекта и получать уведомления по
# неисправности или некорректном поведении.»
#
# Before this block the test catalogue was a static list of in-process checks and
# the modules lived on their own page: nothing in the battery asked "is this module
# working?", and a module in StateError was a badge on /admin/modules. B306
# generates one test per registered module, gives each module its own action to run
# it, and routes failures into the B305 monitoring inbox under the module's own
# source.
#
# CONTRACTS
#   A. one generated test per registered module, in the shared catalogue
#   B. the outcome ladder (not installed → SKIP, running+healthy → PASS,
#      running+unhealthy → FAIL, error → FAIL with LastError, stopped → SKIP)
#   C. per-module control: the action button, the dispatcher case, the run filter
#   D. module faults reach the inbox under module:<name> and recovery resolves them
#   E. the tests exist and pass
#   F. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B306: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SRC=internal/feature/admin/system_tests_modules_b306.go
TESTS=internal/feature/admin/system_tests.go
HANDLERS=internal/feature/admin/system_tests_handlers.go
MODULES=internal/feature/admin/modules.go
TPL=internal/handlers/templates/admin/system_tests.html
I18N=internal/i18n/catalog_modules.go
TESTFILE=internal/feature/admin/system_tests_modules_b306_test.go

hdr "B306 — modules are part of the system-test battery"

# --- A: generated tests --------------------------------------------------------
if grep -q 'func (s \*Service) ModuleTests() \[\]SystemTestDef' "$SRC" \
   && grep -q 'moduleTestNamePrefix + name' "$SRC"; then
  ok "A1: one test is generated per registered module"
else
  bad "A1: module tests are not generated from the live Manager"
fi
if grep -q 'func (s \*Service) AllTests() \[\]SystemTestDef' "$SRC" \
   && grep -q 'out = append(out, s.ModuleTests()...)' "$SRC"; then
  ok "A2: the shared catalogue is registry + module tests"
else
  bad "A2: AllTests does not include the module tests"
fi
if grep -q 'for _, t := range s.AllTests()' "$TESTS"; then
  ok "A3: RunAllTests runs the module tests too"
else
  bad "A3: Run all still ignores the modules"
fi
if grep -q '"Tests":                 s.AllTests()' "$HANDLERS" && grep -q '"ModuleRows":' "$HANDLERS"; then
  ok "A4: the page renders the shared catalogue + the module rows"
else
  bad "A4: the page still renders the static registry only"
fi

# --- B: the ladder -------------------------------------------------------------
if grep -q 'st.State == module.StateNotInstalled' "$SRC" && grep -q 'return SystemTestSkip, fmt.Sprintf("module %s: not installed' "$SRC"; then
  ok "B1: a module that was never installed reports SKIP (a fresh install is not red)"
else
  bad "B1: a not-installed module does not SKIP"
fi
if grep -q 'st.State == module.StateError' "$SRC" && grep -q 'module is in an error state' "$SRC"; then
  ok "B2: StateError is a FAIL that names the state"
else
  bad "B2: an errored module is not reported as a failure"
fi
if grep -q 'st.State == module.StateRunning && !health.Healthy' "$SRC"; then
  ok "B3: a module that claims to run but is unhealthy FAILS"
else
  bad "B3: an unhealthy running module would pass"
fi
if grep -q 'last_error=%s' "$SRC" && grep -q 'checks=' "$SRC"; then
  ok "B4: the output carries the module's LastError and its health checks"
else
  bad "B4: the failure output is not actionable"
fi
if grep -q 'if mod == nil {' "$SRC" && grep -q 'is not registered in this build' "$SRC"; then
  ok "B5: an unregistered module SKIPs instead of panicking"
else
  bad "B5: a missing module is not handled"
fi

# --- C: per-module control -----------------------------------------------------
if grep -q 'func (s \*Service) RunModuleTests(ctx context.Context, name string)' "$SRC"; then
  ok "C1: a single module can be run on its own"
else
  bad "C1: there is no per-module run"
fi
if grep -q 'case "test":' "$MODULES" && grep -q 's.RunModuleTestAction(w, r, c, name)' "$MODULES"; then
  ok "C2: the /admin/modules dispatcher handles the test action (CSRF + admin gate reused)"
else
  bad "C2: the modules page cannot run a test"
fi
if grep -q 'build("test", "modules.test", "")' "$MODULES"; then
  ok "C3: the action button is offered in every module state"
else
  bad "C3: the button is missing for some module states"
fi
if grep -q 'PersistRun' "$SRC"; then
  ok "C4: a module run is persisted (it shows up in the history strip)"
else
  bad "C4: module runs are not stored"
fi
if grep -q '{{if .ModuleRows}}' "$TPL" && grep -q 'modules.test_section' "$TPL"; then
  ok "C5: the tests page shows the module section"
else
  bad "C5: the module section is not rendered"
fi
for k in test test_help test_section; do
  cnt="$(grep -cF "\"modules.$k\"" "$I18N" 2>/dev/null || echo 0)"
  if [ "$cnt" -ge 2 ]; then
    ok "C6: i18n key present (RU+EN): modules.$k"
  else
    bad "C6: i18n key modules.$k only in $cnt/2 maps"
  fi
done

# --- D: the inbox --------------------------------------------------------------
if grep -q 'source = "module:" + modName' "$TESTS" \
   && grep -q 'fingerprint = "module:" + modName + ":health"' "$TESTS"; then
  ok "D1: a module fault reports under module:<name> with a per-module fingerprint"
else
  bad "D1: module faults are buried in the generic system_test source"
fi
if grep -q 'ev.Link = "/admin/modules/" + modName' "$TESTS"; then
  ok "D2: the event links to the module's own page"
else
  bad "D2: the event does not link to the failing module"
fi
if grep -q 'case SystemTestPass:' "$TESTS" && grep -q 'in.Resolve(ev)' "$TESTS"; then
  ok "D3: recovery resolves the event (one notification per fault, not per run)"
else
  bad "D3: a recovered module leaves its event open"
fi

# --- E: tests ------------------------------------------------------------------
if [ -f "$TESTFILE" ]; then ok "E1: $TESTFILE exists"; else bad "E1: $TESTFILE is missing"; fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B306' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "E2: the B306 Go tests pass"
  else
    bad "E2: the B306 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "E2: go not on PATH — run the B306 tests on the VM"
fi

# --- F: git --------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b306_module_tests.sh >/dev/null 2>&1; then
  ok "F1: scripts/check_b306_module_tests.sh is tracked by git"
else
  bad "F1: scripts/check_b306_module_tests.sh is NOT tracked"
fi

printf '\n\033[1mB306 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
