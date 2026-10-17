#!/usr/bin/env bash
# check_b394_system_tests_diagnostics.sh
#
# 2026-10-17 (B394) — /admin/system_tests MUST BE A DIAGNOSTIC TOOL, NOT A GRID OF
# PASS/FAIL CELLS.
#
# OPERATOR ASK (verbatim): «превратить /admin/system_tests в диагностический
# инструмент — важность, "что делать", сбор контекста, прогресс прогона,
# исправление ложного "ОТКЛЮЧЕНО" после POST, фильтры/якоря/копирование упавших,
# 11 новых тестов …, retention истории».
#
# THE FALSE «ОТКЛЮЧЕНО» (measured in the code, not guessed)
#   GetAdminSystemTests built a map with DNSAutoUpdateEnabled / PrefReconcileEnabled /
#   DNSResolveInterval* / Tab / Window / History / LastRun*. PostAdminSystemTestsRun
#   built its OWN, much smaller map and rendered the same template with it — so after
#   every run the template's {{if .DNSAutoUpdateEnabled}} read a MISSING key (nil ->
#   falsy) and the card printed «ОТКЛЮЧЕНО» while the setting was ON.
#
# WHY THIS CONTRACT READS A SURFACE
#   The admin feature is split across system_tests.go (registry), _handlers.go (HTTP),
#   _runtime.go (runner), _runs.go (history) and _run_state_b394.go (the async
#   snapshot). Pinning one file would turn a pure code move red (B339's rule) and
#   would miss the behaviour being re-introduced in a sibling file.
#
# CONTRACTS
#   A. one page-data builder, used by both handlers
#   B. severity + remediation + doc link on every registry entry
#   C. async run + progress: snapshot, json endpoint, poller
#   D. retention exists AND has a caller
#   E. the 11 new diagnostic tests are registered and appended
#   F. filters / anchors / copy-failed / per-test run / remediation in the template
#   G. i18n: every new user-visible string exists in RU and EN
#   H. registered, tracked, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9/B386).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/admin/system_tests.go ] || {
  printf 'B394: cannot locate the admin feature from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b394.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

TPL=internal/handlers/templates/admin/system_tests.html
CAT=internal/i18n/catalog_common.go
ROUTES=cmd/skygate/routes.go
SURF="$SKY_TMP/admin_surface.go"

# The admin surface: every NON-TEST file of the system-tests family, one blob.
for f in internal/feature/admin/system_tests*.go; do
  case "$f" in *_test.go) continue ;; esac
  cat "$f" >> "$SURF" 2>/dev/null || true
done

count_of() { grep -cF -- "$1" "$2" 2>/dev/null || true; }
has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }

hdr "B394 — /admin/system_tests is a diagnostic tool"

SURF_SRC="$(cat "$SURF" 2>/dev/null || true)"
TPL_SRC_TXT="$(cat "$TPL" 2>/dev/null || true)"
ROUTES_SRC="$(cat "$ROUTES" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# A. one builder, both handlers
# ---------------------------------------------------------------------------
hdr "A — one page-data builder feeds GET and POST (the false «ОТКЛЮЧЕНО»)"

if has 'func (s *Service) systemTestsPageData(' "$SURF_SRC"; then
  ok "A1: systemTestsPageData exists — one description of the page"
else
  bad "A1: no shared page-data builder; the handlers build their own maps"
fi

n="$(count_of 's.systemTestsPageData(' "$SURF")"
if [ "${n:-0}" -ge 2 ]; then
  ok "A2: both handlers call it (GET and the POST path)"
else
  bad "A2: only ${n:-0} call site(s) — a handler still renders a hand-built map"
fi

n="$(count_of '"DNSAutoUpdateEnabled"' "$SURF")"
if [ "${n:-0}" -eq 1 ]; then
  ok "A3: DNSAutoUpdateEnabled is set in exactly one place (the builder)"
else
  bad "A3: DNSAutoUpdateEnabled appears ${n:-0} times — a second map is how the toggle lied"
fi

n="$(count_of 'data := map[string]any{' "$SURF")"
if [ "${n:-0}" -eq 1 ]; then
  ok "A4: exactly one inline page map (the builder's) — the POST path has none"
else
  bad "A4: ${n:-0} inline page maps — a handler still builds its own"
fi

# ---------------------------------------------------------------------------
# B. severity + remediation + doc link
# ---------------------------------------------------------------------------
hdr "B — every test carries a severity and 'what to do'"

if has 'Severity TestSeverity' "$SURF_SRC"; then
  ok "B1: SystemTestDef has a typed Severity field"
else
  bad "B1: SystemTestDef has no typed Severity field"
fi
if has 'Remediation string' "$SURF_SRC"; then
  ok "B2: SystemTestDef has a Remediation field (an i18n key)"
else
  bad "B2: SystemTestDef has no Remediation field"
fi
if has 'DocLink string' "$SURF_SRC"; then
  ok "B3: SystemTestDef has a DocLink field"
else
  bad "B3: SystemTestDef has no DocLink"
fi
if has 'annotateTest(' "$SURF_SRC"; then
  ok "B4: one helper applies the annotations (so coverage is checkable)"
else
  bad "B4: nothing applies the annotations — the fields would render empty"
fi
if has 'SeverityCritical' "$SURF_SRC" && has 'SeverityWarning' "$SURF_SRC" && has 'SeverityInfo' "$SURF_SRC"; then
  ok "B5: the three severities exist"
else
  bad "B5: the severity vocabulary is incomplete"
fi

# ---------------------------------------------------------------------------
# C. async run + progress
# ---------------------------------------------------------------------------
hdr "C — a run reports progress instead of blocking the request"

if has 'SystemRunState' "$SURF_SRC"; then
  ok "C1: a run snapshot type exists"
else
  bad "C1: no run snapshot — the page can only show a finished run"
fi
if has 'func (s *Service) StartSystemRun(' "$SURF_SRC"; then
  ok "C2: StartSystemRun launches the catalogue in the background"
else
  bad "C2: no background runner"
fi
if has 'go s.runTestsAsync(' "$SURF_SRC"; then
  ok "C3: the run really is asynchronous (a goroutine starts it)"
else
  bad "C3: the run still blocks the POST request"
fi
if has '/admin/system_tests/run.json' "$ROUTES_SRC"; then
  ok "C4: the progress endpoint is registered"
else
  bad "C4: the progress endpoint is not registered in the routes"
fi
if has 'system_tests/run.json' "$TPL_SRC_TXT"; then
  ok "C5: the template polls the progress endpoint"
else
  bad "C5: the template does not poll anything"
fi
if has 'name="test"' "$TPL_SRC_TXT"; then
  ok "C6: a single test can be run from its row"
else
  bad "C6: no per-test run control"
fi

# ---------------------------------------------------------------------------
# D. retention
# ---------------------------------------------------------------------------
hdr "D — the history is pruned, not accumulated"

if has 'func (s *Service) PruneSystemTestRuns(' "$SURF_SRC"; then
  ok "D1: PruneSystemTestRuns exists"
else
  bad "D1: nothing prunes system_tests_runs"
fi
n="$(count_of 's.PruneSystemTestRuns(' "$SURF")"
if [ "${n:-0}" -ge 1 ]; then
  ok "D2: the pruner has a caller (declared-but-never-called is how a retention story stays fiction)"
else
  bad "D2: the pruner has no caller"
fi
if has 'SystemTestRunsKeep' "$SURF_SRC" && has 'SystemTestRunsMaxAge' "$SURF_SRC"; then
  ok "D3: the retention policy is named constants, not magic numbers"
else
  bad "D3: the retention policy has no named bounds"
fi

# ---------------------------------------------------------------------------
# E. the 11 new tests
# ---------------------------------------------------------------------------
hdr "E — the 11 diagnostic tests are registered"

MISSING=""
for t in headscale.exit_routes_approved headscale.tags_drift headscale.policy_current \
         prefix_owner.consistency db.writable_primary db.schema_tables disk.free_space \
         derp.reachable build.ready exit_rules.domain_prefix backup.verify; do
  if has "\"$t\"" "$SURF_SRC"; then : ; else MISSING="${MISSING}${t} "; fi
done
if [ -z "$MISSING" ]; then
  ok "E1: all 11 new tests are in the registry"
else
  bad "E1: missing from the registry: $MISSING"
fi
if has 'append(TestRegistry, b394Tests...)' "$SURF_SRC"; then
  ok "E2: the batch is appended to TestRegistry (a declared slice nobody appends is dead code)"
else
  bad "E2: the new tests are not appended to TestRegistry"
fi
if has 'tx.Rollback()' "$SURF_SRC"; then
  ok "E3: the write probe rolls back (a system test must not leave state behind)"
else
  bad "E3: the write probe has no rollback"
fi

# ---------------------------------------------------------------------------
# F. filters, anchors, copy, remediation rendering
# ---------------------------------------------------------------------------
hdr "F — the page is operable when something fails"

if has 'id="t-' "$TPL_SRC_TXT"; then
  ok "F1: every row has an anchor id (t-<name>)"
else
  bad "F1: no per-row anchors"
fi
if has 'st-only-failed' "$TPL_SRC_TXT"; then
  ok "F2: a 'failed only' filter exists"
else
  bad "F2: no failure filter"
fi
if has 'st-category' "$TPL_SRC_TXT"; then
  ok "F3: a category filter exists"
else
  bad "F3: no category filter"
fi
if has 'st-copy-failed' "$TPL_SRC_TXT" && has 'clipboard' "$TPL_SRC_TXT"; then
  ok "F4: 'copy the failed ones' is wired to the clipboard"
else
  bad "F4: no copy-failed action"
fi
if has '{{t .Remediation}}' "$TPL_SRC_TXT"; then
  ok "F5: the page renders the remediation text"
else
  bad "F5: the page never renders Remediation"
fi
if has 'st-sev-' "$TPL_SRC_TXT"; then
  ok "F6: the severity is visible as its own badge"
else
  bad "F6: the severity is not rendered"
fi

# ---------------------------------------------------------------------------
# G. i18n
# ---------------------------------------------------------------------------
hdr "G — every new user-visible string is localised (RU+EN)"

MISSING_KEYS=""
for k in system_tests.severity_label system_tests.remediation_label system_tests.remediation_link \
         system_tests.run_one system_tests.only_failed system_tests.filter_category \
         system_tests.filter_category_all system_tests.copy_failed system_tests.copy_failed_none \
         system_tests.progress_starting system_tests.progress_running system_tests.progress_done \
         system_tests.progress_lost st.fix.generic st.fix.db_integrity st.fix.tag_drift; do
  r="$(count_of "\"$k\"" "$CAT")"
  [ "${r:-0}" -ge 2 ] || MISSING_KEYS="${MISSING_KEYS}${k}(${r:-0}) "
done
if [ -z "$MISSING_KEYS" ]; then
  ok "G1: all new keys exist in RU and EN"
else
  bad "G1: keys missing the RU+EN pair: $MISSING_KEYS"
fi

# ---------------------------------------------------------------------------
# H. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "H — registered, tracked, documented"

if git ls-files --error-unmatch scripts/check_b394_system_tests_diagnostics.sh >/dev/null 2>&1; then
  ok "H1: this script is tracked by git (trap #11)"
else
  bad "H1: this script is not tracked"
fi
if grep -q 'check_b394_system_tests_diagnostics' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "H2: registered in scripts/verify_pre_deploy.sh"
else
  bad "H2: not registered"
fi
if grep -q '^\- \*\*B394\*\*' AGENTS.md 2>/dev/null; then
  ok "H3: one-line entry in the AGENTS.md block index"
else
  bad "H3: no B394 entry in the AGENTS block index"
fi
if grep -q 'B394' docs/operations.md 2>/dev/null; then
  ok "H4: docs/operations.md explains what a failure means and what to do"
else
  bad "H4: docs/operations.md does not document the diagnostics"
fi
# There is no docs/ru/operations.md (the RU set is INSTALL/README/ROADMAP/UPDATE), so
# the RU half of rule 12 is satisfied by the RU roadmap recording the block.
if grep -q 'B394' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "H5: the RU roadmap records the block (rule 12)"
else
  bad "H5: docs/ru/ROADMAP.md does not record B394"
fi

printf '\n\033[1mB394: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
