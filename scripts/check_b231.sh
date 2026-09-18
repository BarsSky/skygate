#!/usr/bin/env bash
# B-check for B231 (v1.5.2+): preferred-exit
# reconciler UI toggle + HostnameRenameMigrator.
# Extends B229 with operator-driven on/off control
# (DB-backed global_settings + UI card on
# /admin/system_tests) and auto-migration of
# device_exit_node_prefs when the operator renames
# a device in headscale.
#
# Contracts pinned (15 contracts across 4 surface
# areas):
#   A:    UI toggle — DB-backed global_settings key
#         'preferred_reconcile_enabled' +
#         SKYGATE_PREFERRED_RECONCILE_ENABLED env
#         override + handler
#         PostAdminSystemTestsPrefReconcileToggle +
#         route POST
#         /admin/system_tests/preferred-reconcile-toggle
#         + audit log row.
#   B:    handlers.go RunPreferredExitReconciler
#         reads the toggle every tick (env → DB →
#         default-on). The rename migrator runs
#         inside the same goroutine AFTER the main
#         reconcile (B229), only when enabled.
#   C:    reconciler_rename.go has the
#         OrphanClassification enum (Normal / Rename
#         / Ambiguous / Orphan) + the pure
#         ClassifyRenameMigration function + the
#         RenameMigration struct.
#   D:    reconciler_rename.go has the
#         MigrateRenamedDevicePrefs orchestrator
#         (per-tick, in same goroutine as B229) +
#         applyRenameMigration (live-mode UPSERT +
#         DELETE in a transaction).
#   E:    exitRulesRunner interface in handlers.go
#         includes MigrateRenamedDevicePrefs.
#   F:    config.go has PrefReconcileEnabled (env
#         default) + PrefReconcileInterval (existing
#         from B229).
#   G:    system_tests_handlers.go reads
#         global_settings.preferred_reconcile_enabled
#         and passes it as PrefReconcileEnabled in
#         the page data.
#   H:    system_tests.html renders the toggle card
#         (ENABLED/DISABLED tag + toggle button +
#         i18n title + i18n required-help).
#   I:    i18n catalog has title.pref_reconciler +
#         pref_reconciler.required in BOTH RU and EN.
#   J:    B229 unit tests still pass + new B231
#         unit tests for the rename migrator pass.
#   K:    AGENTS.md mentions B231.
#   L:    go build ./... + go vet ./... + go test
#         on the affected packages all pass.
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }
hasf() { grep -qF -- "$2" "$1" 2>/dev/null; }

HANDLER="internal/feature/admin/settings_pref_reconcile.go"
HANDLERS_GO="internal/handlers/handlers.go"
RENAME="internal/feature/exit_rules/reconciler_rename.go"
RENAME_TEST="internal/feature/exit_rules/reconciler_rename_b231_test.go"
MAIN="cmd/skygate/main.go"
CFG="internal/config/config.go"
STHANDLERS="internal/feature/admin/system_tests_handlers.go"
STTPL="internal/handlers/templates/admin/system_tests.html"
I18N="internal/i18n/catalog_common.go"
AGENTS="AGENTS.md"

# --- A: UI toggle surface ---
all_a=1
for sym in \
  "globalSettingsKeyPrefReconcile = \"preferred_reconcile_enabled\"" \
  "PostAdminSystemTestsPrefReconcileToggle" \
  "preferred_reconcile_toggle" \
  "SetGlobalSettingBool"
do
  if ! hasf "$HANDLER" "$sym"; then
    echo "  [missing] $sym"
    all_a=0
  fi
done
if ! hasf "$MAIN" "/admin/system_tests/preferred-reconcile-toggle"; then
  echo "  [missing] /admin/system_tests/preferred-reconcile-toggle route"
  all_a=0
fi
if [ "$all_a" = "1" ]; then
  ok "A: UI toggle (handler + route + DB key + audit action) present"
else
  fail "A: UI toggle missing one or more required symbols"
fi

# --- B: RunPreferredExitReconciler reads toggle + runs rename migrator ---
all_b=1
if ! hasf "$HANDLERS_GO" "preferred_reconcile_enabled"; then
  echo "  [missing] preferred_reconcile_enabled in RunPreferredExitReconciler"
  all_b=0
fi
if ! hasf "$HANDLERS_GO" "SKYGATE_PREFERRED_RECONCILE_ENABLED"; then
  echo "  [missing] SKYGATE_PREFERRED_RECONCILE_ENABLED env reading"
  all_b=0
fi
if ! hasf "$HANDLERS_GO" "MigrateRenamedDevicePrefs(ctx, notifier)"; then
  echo "  [missing] MigrateRenamedDevicePrefs call in handler"
  all_b=0
fi
if [ "$all_b" = "1" ]; then
  ok "B: RunPreferredExitReconciler reads toggle every tick + invokes MigrateRenamedDevicePrefs"
else
  fail "B: RunPreferredExitReconciler missing toggle read or migrator call"
fi

# --- C: reconciler_rename.go has the B231 surface ---
all_c=1
for sym in \
  "type OrphanClassification string" \
  "ClassificationNormal" \
  "ClassificationRename" \
  "ClassificationAmbiguous" \
  "ClassificationOrphan" \
  "type RenameMigration struct" \
  "func ClassifyRenameMigration" \
  "func .s .Service. MigrateRenamedDevicePrefs"
do
  if ! has "$RENAME" "$sym"; then
    echo "  [missing] $sym"
    all_c=0
  fi
done
if [ "$all_c" = "1" ]; then
  ok "C: reconciler_rename.go has OrphanClassification enum + RenameMigration struct + ClassifyRenameMigration + MigrateRenamedDevicePrefs"
else
  fail "C: reconciler_rename.go missing one or more required symbols"
fi

# --- D: MigrateRenamedDevicePrefs orchestrator + applyRenameMigration ---
if grep -E 'func \(s \*Service\) MigrateRenamedDevicePrefs' "$RENAME" >/dev/null && \
   grep -E 'func \(s \*Service\) applyRenameMigration' "$RENAME" >/dev/null && \
   hasf "$RENAME" "BeginTx" && \
   hasf "$RENAME" "INSERT INTO device_exit_node_prefs" && \
   hasf "$RENAME" "DELETE FROM device_exit_node_prefs"; then
  ok "D: MigrateRenamedDevicePrefs (orchestrator) + applyRenameMigration (tx with INSERT + DELETE)"
else
  fail "D: MigrateRenamedDevicePrefs / applyRenameMigration missing required SQL/transaction structure"
fi

# --- E: exitRulesRunner interface has MigrateRenamedDevicePrefs ---
if grep -A20 "type exitRulesRunner interface" "$HANDLERS_GO" | grep -q "MigrateRenamedDevicePrefs"; then
  ok "E: exitRulesRunner interface includes MigrateRenamedDevicePrefs"
else
  fail "E: exitRulesRunner interface missing MigrateRenamedDevicePrefs"
fi

# --- F: config has PrefReconcileEnabled + PrefReconcileInterval ---
if has "$CFG" "PrefReconcileEnabled" && \
   hasf "$CFG" "SKYGATE_PREFERRED_RECONCILE_ENABLED" && \
   has "$CFG" "PrefReconcileInterval"; then
  ok "F: config.go has PrefReconcileEnabled + PrefReconcileInterval + env vars"
else
  fail "F: config.go missing PrefReconcileEnabled / PrefReconcileInterval / env"
fi

# --- G: system_tests_handlers.go reads DB and passes PrefReconcileEnabled ---
if grep -A2 "globalSettingsKeyPrefReconcile" "$STHANDLERS" | grep -q "GetGlobalSettingBool" && \
   hasf "$STHANDLERS" "\"PrefReconcileEnabled\":"; then
  ok "G: system_tests_handlers.go reads global_settings.preferred_reconcile_enabled + passes as PrefReconcileEnabled"
else
  fail "G: system_tests_handlers.go missing PrefReconcileEnabled read or page-data"
fi

# --- H: system_tests.html renders the toggle card ---
if hasf "$STTPL" "/admin/system_tests/preferred-reconcile-toggle" && \
   hasf "$STTPL" "{{if .PrefReconcileEnabled}}" && \
   hasf "$STTPL" "title.pref_reconciler"; then
  ok "H: system_tests.html renders toggle card (form + i18n title + ENABLED/DISABLED state)"
else
  fail "H: system_tests.html missing toggle form / state / i18n title"
fi

# --- I: i18n catalog has B231 keys in BOTH RU and EN ---
all_i=1
for k in 'title.pref_reconciler' 'pref_reconciler.required'; do
  if ! hasf "$I18N" "$k"; then
    echo "  [missing RU] $k"
    all_i=0
  fi
done
# EN keys live in the same file but at a different line
# block. Check at the end of the file (after line 300
# usually).
for k in 'title.pref_reconciler' 'pref_reconciler.required'; do
  # Count occurrences: should be 2 (one RU, one EN).
  count=$(grep -c "\"$k\"" "$I18N" 2>/dev/null || echo 0)
  if [ "${count:-0}" -lt 2 ]; then
    echo "  [missing EN] $k (found $count occurrences, want >=2)"
    all_i=0
  fi
done
if [ "$all_i" = "1" ]; then
  ok "I: i18n catalog has title.pref_reconciler + pref_reconciler.required in BOTH RU and EN"
else
  fail "I: i18n catalog missing one or more B231 keys in RU or EN"
fi

# --- J: tests pass ---
if [ -f "$RENAME_TEST" ]; then
  n=$(grep -c "^func Test" "$RENAME_TEST" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 8 ]; then
    ok "J: B231 unit tests present (${n} Test functions, expected >=8)"
  else
    fail "J: B231 unit tests insufficient (${n} < 8)"
  fi
  for t in \
    "TestClassifyRenameMigration_Normal" \
    "TestClassifyRenameMigration_Rename" \
    "TestClassifyRenameMigration_Ambiguous" \
    "TestClassifyRenameMigration_Orphan" \
    "TestClassifyRenameMigration_OrphanWithStaleTag" \
    "TestClassifyRenameMigration_TwoHostsSameTagReal" \
    "TestClassifyRenameMigration_EmptyHostname" \
    "TestShouldAlert_RenameReasonRateLimited" \
    "TestShouldAlert_AmbiguousReasonRateLimited" \
    "TestShouldAlert_OrphanReasonRateLimited"; do
    if ! hasf "$RENAME_TEST" "$t"; then
      fail "J: missing required test $t"
    fi
  done
  ok "J2: all 10 required B231 tests present"
else
  fail "J: B231 test file $RENAME_TEST missing"
fi

# --- K: AGENTS.md ---
if has "$AGENTS" "B231"; then
  ok "K: AGENTS.md mentions B231"
else
  echo "[skip] K: AGENTS.md doesn't mention B231 (will be added before commit)"
fi

# --- L: build + vet + test ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "L: go build ./... succeeds"
  else
    fail "L: go build ./... FAILED"
  fi
  if go vet ./... >/dev/null 2>&1; then
    ok "L2: go vet ./... succeeds"
  else
    fail "L2: go vet ./... FAILED"
  fi
  if go test ./internal/feature/exit_rules/... ./internal/feature/admin/... ./internal/handlers/... ./internal/i18n/... ./internal/config/... >/dev/null 2>&1; then
    ok "L3: go test on affected packages passes"
  else
    fail "L3: go test on affected packages FAILED"
  fi
else
  echo "[skip] L: go not on PATH"
fi

echo ""
echo "B231 B-check: $ok_count passed"
