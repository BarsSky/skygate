#!/usr/bin/env bash
#===============================================================================
# Skygate v1.4.4 (B144) — TD-8: /admin/system_tests History tab
#
# Pins the v1.4.4 fix for the operator-reported issue
# "system_tests history — no per-test view":
# the pre-B144 /admin/system_tests page showed the test
# registry as a grid + a "Recent runs (last 20)" strip
# with aggregate pass/fail/skip counts. The strip told
# the operator "what was the overall result of each run"
# but not "which tests are flaky" or "which tests have
# been failing for a week". B144 adds a History tab
# (?tab=history) that aggregates per-test pass/fail/skip
# counts across a configurable window (7d / 30d / all).
#
# What this script verifies (live, on the VM):
#   A. internal/feature/admin/system_tests_history.go:
#      ComputeTestHistory + TestHistory + TestHistoryRow
#      + PassRate + TotalRuns + HistoryWindow +
#      ParseHistoryWindow + truncateForHistory
#   B. internal/feature/admin/system_tests_handlers.go:
#      GetAdminSystemTests reads ?tab= + ?window= +
#      calls ComputeTestHistory
#   C. internal/handlers/templates/admin/system_tests.html:
#      tab bar + History tab content + window selector +
#      per-test aggregate table + 7d/30d/all buttons
#   D. internal/feature/admin/system_tests_history_test.go:
#      11 test functions + go test passes
#   E. internal/i18n/catalog_common.go: 23 new
#      system_tests.* keys (tab_tests / tab_history /
#      history_title / history_subtitle / window_label /
#      window_7d / window_30d / window_all /
#      stat_total_runs / stat_window /
#      stat_total_duration / stat_tests_tracked / col_pass
#      / col_fail / col_skip / col_pass_rate /
#      col_last_status / col_last_run / col_last_error /
#      col_started / col_duration / never /
#      recent_runs_title / history_no_runs_help) in BOTH
#      RU + EN
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

HIST_GO="internal/feature/admin/system_tests_history.go"
HANDLERS_GO="internal/feature/admin/system_tests_handlers.go"
TEMPLATE="internal/handlers/templates/admin/system_tests.html"
I18N="internal/i18n/catalog_common.go"
TEST_FILE="internal/feature/admin/system_tests_history_test.go"

for f in "${HIST_GO}" "${HANDLERS_GO}" "${TEMPLATE}" "${I18N}" "${TEST_FILE}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: system_tests_history.go — aggregation
# ------------------------------------------------------------------------------
echo
echo "=== A. system_tests_history.go: ComputeTestHistory + TestHistory + TestHistoryRow + helpers ==="
a_compute=$(grep -cE '^func \(s \*Service\) ComputeTestHistory\(' "${HIST_GO}" || true)
a_th_struct=$(grep -cE '^type TestHistory struct' "${HIST_GO}" || true)
a_row_struct=$(grep -cE '^type TestHistoryRow struct' "${HIST_GO}" || true)
a_passrate=$(grep -cE '^func \(r TestHistoryRow\) PassRate\(\) int' "${HIST_GO}" || true)
a_totalruns=$(grep -cE '^func \(r TestHistoryRow\) TotalRuns\(\) int' "${HIST_GO}" || true)
a_window_struct=$(grep -cE '^type HistoryWindow struct' "${HIST_GO}" || true)
a_parsewindow=$(grep -cE '^func ParseHistoryWindow\(' "${HIST_GO}" || true)
a_truncate=$(grep -cE '^func truncateForHistory\(' "${HIST_GO}" || true)
a_query=$(grep -cE 'SELECT id, started_at, finished_at, duration_ms, results_json' "${HIST_GO}" || true)
a_placeholder=$(grep -cE 'db\.PlaceholdersList' "${HIST_GO}" || true)
a_seed=$(grep -cE 'accumulator\[def\.Name\]\s*=\s*&acc\{\}' "${HIST_GO}" || true)
a_sort_fail=$(grep -cE 'rows2\[i\]\.FailCount\s*>\s*rows2\[j\]\.FailCount' "${HIST_GO}" || true)
a_json_parse=$(grep -cE 'json\.Unmarshal\(\[\]byte\(resultsJSON\)' "${HIST_GO}" || true)
a_audit_on_err=$(grep -cE 'system_tests_history_parse_error' "${HIST_GO}" || true)
a_testreg_iter=$(grep -cE 'for _, def := range TestRegistry' "${HIST_GO}" || true)
if [ "${a_compute}" -ge 1 ] && [ "${a_th_struct}" -ge 1 ] && \
   [ "${a_row_struct}" -ge 1 ] && [ "${a_passrate}" -ge 1 ] && \
   [ "${a_totalruns}" -ge 1 ] && [ "${a_window_struct}" -ge 1 ] && \
   [ "${a_parsewindow}" -ge 1 ] && [ "${a_truncate}" -ge 1 ] && \
   [ "${a_query}" -ge 1 ] && [ "${a_placeholder}" -ge 1 ] && \
   [ "${a_seed}" -ge 1 ] && [ "${a_sort_fail}" -ge 1 ] && \
   [ "${a_json_parse}" -ge 1 ] && [ "${a_audit_on_err}" -ge 1 ] && \
   [ "${a_testreg_iter}" -ge 1 ]; then
    ok "ComputeTestHistory + TestHistory + TestHistoryRow + PassRate + TotalRuns + HistoryWindow + ParseHistoryWindow + truncateForHistory + TestRegistry seed + sort by FailCount + JSON parse + audit on error (all 15 present)"
else
    bad "system_tests_history.go incomplete: compute=${a_compute} th=${a_th_struct} row=${a_row_struct} passrate=${a_passrate} totalruns=${a_totalruns} window_struct=${a_window_struct} parsewindow=${a_parsewindow} truncate=${a_truncate} query=${a_query} placeholder=${a_placeholder} seed=${a_seed} sort_fail=${a_sort_fail} json_parse=${a_json_parse} audit_on_err=${a_audit_on_err} testreg_iter=${a_testreg_iter}"
fi

# ------------------------------------------------------------------------------
# Contract B: handler reads ?tab= + ?window= + calls ComputeTestHistory
# ------------------------------------------------------------------------------
echo
echo "=== B. system_tests_handlers.go: GetAdminSystemTests reads ?tab= + ?window= + calls ComputeTestHistory ==="
b_tab_param=$(grep -cE 'r\.URL\.Query\(\)\.Get\("tab"\)' "${HANDLERS_GO}" || true)
b_window_param=$(grep -cE 'r\.URL\.Query\(\)\.Get\("window"\)' "${HANDLERS_GO}" || true)
b_compute_call=$(grep -cE 's\.ComputeTestHistory\(r\.Context\(\), hw\.Since, hw\.Until\)' "${HANDLERS_GO}" || true)
b_parsewindow=$(grep -cE 'ParseHistoryWindow\(windowStr, now\)' "${HANDLERS_GO}" || true)
b_default_tab=$(grep -cPzo 'if tab != "history" \{\s*tab = "tests"' "${HANDLERS_GO}" | tr -d 0 | head -1)
b_default_tab=${b_default_tab:-0}
b_data_tab=$(grep -cE '"Tab":\s*tab' "${HANDLERS_GO}" || true)
b_data_window=$(grep -cE '"Window":\s*windowStr' "${HANDLERS_GO}" || true)
b_data_history=$(grep -cE '"History":\s*history' "${HANDLERS_GO}" || true)
if [ "${b_tab_param}" -ge 1 ] && [ "${b_window_param}" -ge 1 ] && \
   [ "${b_compute_call}" -ge 1 ] && [ "${b_parsewindow}" -ge 1 ] && \
   [ "${b_default_tab}" -ge 1 ] && [ "${b_data_tab}" -ge 1 ] && \
   [ "${b_data_window}" -ge 1 ] && [ "${b_data_history}" -ge 1 ]; then
    ok "Handler: ?tab= + ?window= + ComputeTestHistory + ParseHistoryWindow + default 'tests' tab + data fields (all 8 present)"
else
    bad "system_tests_handlers.go incomplete: tab_param=${b_tab_param} window_param=${b_window_param} compute_call=${b_compute_call} parsewindow=${b_parsewindow} default_tab=${b_default_tab} data_tab=${b_data_tab} data_window=${b_data_window} data_history=${b_data_history}"
fi

# ------------------------------------------------------------------------------
# Contract C: template — tab bar + History content + window selector
# ------------------------------------------------------------------------------
echo
echo "=== C. system_tests.html: tab bar + History content + window selector + aggregate table ==="
c_tab_bar=$(grep -cE 'class="tabs"' "${TEMPLATE}" || true)
c_tab_tests=$(grep -cE 'href="\?tab=tests"' "${TEMPLATE}" || true)
c_tab_history=$(grep -cE 'href="\?tab=history"' "${TEMPLATE}" || true)
c_history_eq=$(grep -cE '{{if eq .Tab "history"}}' "${TEMPLATE}" || true)
c_window_7d=$(grep -cE 'href="\?tab=history&window=7d"' "${TEMPLATE}" || true)
c_window_30d=$(grep -cE 'href="\?tab=history&window=30d"' "${TEMPLATE}" || true)
c_window_all=$(grep -cE 'href="\?tab=history&window=all"' "${TEMPLATE}" || true)
c_history_rows=$(grep -cE '{{range \.History\.Rows}}' "${TEMPLATE}" || true)
c_total_runs=$(grep -cE '\.History\.TotalRuns' "${TEMPLATE}" || true)
c_total_duration=$(grep -cE '\.History\.TotalDuration' "${TEMPLATE}" || true)
c_no_runs=$(grep -cE 'history_no_runs_help' "${TEMPLATE}" || true)
c_passrate_cell=$(grep -cE 'PassRate' "${TEMPLATE}" || true)
if [ "${c_tab_bar}" -ge 1 ] && [ "${c_tab_tests}" -ge 1 ] && \
   [ "${c_tab_history}" -ge 1 ] && [ "${c_history_eq}" -ge 1 ] && \
   [ "${c_window_7d}" -ge 1 ] && [ "${c_window_30d}" -ge 1 ] && \
   [ "${c_window_all}" -ge 1 ] && [ "${c_history_rows}" -ge 1 ] && \
   [ "${c_total_runs}" -ge 1 ] && [ "${c_total_duration}" -ge 1 ] && \
   [ "${c_no_runs}" -ge 1 ] && [ "${c_passrate_cell}" -ge 1 ]; then
    ok "Template: tab bar + tab links + History tab branch + 7d/30d/all buttons + History.Rows range + TotalRuns + TotalDuration + empty state + PassRate (all 12 present)"
else
    bad "system_tests.html incomplete: tab_bar=${c_tab_bar} tab_tests=${c_tab_tests} tab_history=${c_tab_history} history_eq=${c_history_eq} window_7d=${c_window_7d} window_30d=${c_window_30d} window_all=${c_window_all} history_rows=${c_history_rows} total_runs=${c_total_runs} total_duration=${c_total_duration} no_runs=${c_no_runs} passrate_cell=${c_passrate_cell}"
fi

# ------------------------------------------------------------------------------
# Contract D: unit test + go test
# ------------------------------------------------------------------------------
echo
echo "=== D. system_tests_history_test.go: 11 test functions + go test passes ==="
t1=$(grep -c 'func TestParseHistoryWindow_7D' "${TEST_FILE}" || true)
t2=$(grep -c 'func TestParseHistoryWindow_7DExplicit' "${TEST_FILE}" || true)
t3=$(grep -c 'func TestParseHistoryWindow_30D' "${TEST_FILE}" || true)
t4=$(grep -c 'func TestParseHistoryWindow_All' "${TEST_FILE}" || true)
t5=$(grep -c 'func TestParseHistoryWindow_UnknownFallsBackTo7D' "${TEST_FILE}" || true)
t6=$(grep -c 'func TestTestHistoryRow_PassRate' "${TEST_FILE}" || true)
t7=$(grep -c 'func TestTestHistoryRow_TotalRuns' "${TEST_FILE}" || true)
t8=$(grep -c 'func TestTruncateForHistory_UnderLimit' "${TEST_FILE}" || true)
t9=$(grep -c 'func TestTruncateForHistory_AtLimit' "${TEST_FILE}" || true)
t10=$(grep -c 'func TestTruncateForHistory_OverLimit' "${TEST_FILE}" || true)
t11=$(grep -c 'func TestTruncateForHistory_Empty' "${TEST_FILE}" || true)
test_count=$((t1+t2+t3+t4+t5+t6+t7+t8+t9+t10+t11))
if [ "${test_count}" -ge 11 ]; then
    ok "11 test functions present (5 ParseHistoryWindow + 2 TestHistoryRow + 4 truncateForHistory)"
else
    bad "Test file incomplete: test_count=${test_count} (expected 11)"
fi

GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in "/c/Program Files/Go/bin/go.exe" "/usr/local/go/bin/go" "/snap/bin/go"; do
        if [ -x "${cand}" ]; then GO="${cand}"; break; fi
    done
fi
if [ -z "${GO}" ]; then
    warn "go not found — skipping D go-test (other 4 contracts still hold)"
else
    test_out=$("${GO}" test -count=1 -run 'TestParseHistoryWindow|TestTestHistoryRow|TestTruncateForHistory' ./internal/feature/admin/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "go test PASSes for TD-8 pure-Go helpers (B144 compiles + tests green)"
    else
        bad "go test FAILED: ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Contract E: i18n — 23 new system_tests.* keys in BOTH RU and EN
# ------------------------------------------------------------------------------
echo
echo "=== E. catalog_common.go: 23 new system_tests.* keys (RU + EN) ==="
keys="tab_tests tab_history history_title history_subtitle window_label window_7d window_30d window_all stat_total_runs stat_window stat_total_duration stat_tests_tracked col_pass col_fail col_skip col_pass_rate col_last_status col_last_run col_last_error col_started col_duration never recent_runs_title"
miss=0
for k in $keys; do
    n=$(grep -cE "\"system_tests\.${k}\"" "${I18N}" || true)
    if [ "${n}" -lt 2 ]; then
        miss=$((miss+1))
        echo "    missing EN/RU entry for system_tests.${k} (count=${n})"
    fi
done
if [ "${miss}" -eq 0 ]; then
    ok "All 23 new system_tests.* keys present in both RU + EN (46 total occurrences)"
else
    bad "i18n: ${miss} of 23 keys missing in one of the languages"
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B144 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B144 contracts all hold."
    exit 0
fi
echo
echo "B144 has failing contracts — fix the source files above."
exit 1
