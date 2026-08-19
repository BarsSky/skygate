#!/usr/bin/env bash
# check_b149.sh — v1.5.0 / B149 contracts.
#
# This is the B-check that pins the /admin/ha page
# (Phase 5 of the v1.5.0 BL-2 plan). It verifies five things:
#
#   1. internal/feature/admin/ha.go exists with the 8
#      documented handlers (GetAdminHA + 7 POSTs).
#   2. internal/feature/admin/ha_test.go exists with pure-Go
#      unit tests for the form-parsing / confirmation helpers
#      (no DB needed).
#   3. internal/handlers/templates/admin/ha.html exists and
#      renders all 6 sections per the BL-2 plan §5.1.
#   4. The i18n catalog has the new ha.* keys in BOTH ruAdmin
#      and enAdmin maps (parity is checked separately by
#      TestCatalogsParity in the i18n package).
#   5. cmd/skygate/main.go wires the 10 new routes (1 GET
#      + 9 POSTs) + the Service struct carries the two new
#      fields (RegapiStore, SelfHostname) per the B149
#      plan.
#
# The script is intentionally read-only — it does not touch
# the database or run the live VM. The unit tests
# (`go test ./internal/feature/admin/ ./internal/i18n/`)
# cover the runtime contract; this script is the "is the
# code even there?" check.

set -euo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$REPO_ROOT"

# When the script is run from git-bash on Windows, the
# `go` binary is at C:\Program Files\Go\bin\go.exe and may
# not be on the bash PATH inherited from PowerShell. Add
# common locations.
if ! command -v go >/dev/null 2>&1; then
    for cand in \
        "/c/Program Files/Go/bin/go.exe" \
        "/c/Program Files (x86)/Go/bin/go.exe" \
        "/c/Go/bin/go.exe" \
        "/usr/local/go/bin/go" \
        "/opt/go/bin/go" \
        "/usr/bin/go"; do
        if [ -x "$cand" ]; then
            export PATH="$(dirname "$cand"):$PATH"
            break
        fi
    done
fi
# Last resort: use the Windows-style full path via cmd.exe
if ! command -v go >/dev/null 2>&1; then
    if command -v cmd.exe >/dev/null 2>&1; then
        # Define a shim function that runs go via cmd
        go() { cmd.exe //c "go $*"; }
        export -f go 2>/dev/null || true
    fi
fi
if ! command -v go >/dev/null 2>&1; then
    echo "FATAL: go binary not found in PATH (looked in /c/Program Files/Go/bin and friends)"
    echo "Current PATH: $PATH"
    exit 1
fi

PASS=0
FAIL=0

ok()   { echo "PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "FAIL: $1"; FAIL=$((FAIL+1)); }

# --- contract A: ha.go with 8 handlers -----------------------------
echo
echo "=== contract A: internal/feature/admin/ha.go ==="
if [ -f internal/feature/admin/ha.go ]; then
    ok "internal/feature/admin/ha.go exists"
else
    bad "internal/feature/admin/ha.go missing"
fi
for h in \
    "func (s *Service) GetAdminHA" \
    "func (s *Service) PostAdminHAChainEdit" \
    "func (s *Service) PostAdminHAAutoReclaimToggle" \
    "func (s *Service) PostAdminHAAddNode" \
    "func (s *Service) PostAdminHARemoveNode" \
    "func (s *Service) PostAdminHAForcePromote" \
    "func (s *Service) PostAdminHAForceDemote" \
    "func (s *Service) PostAdminHAReclaim" \
    "func (s *Service) PostAdminHARegapiCreds" \
    "func (s *Service) PostAdminHARegapiTest"; do
    if grep -q -F "$h" internal/feature/admin/ha.go; then
        ok "ha.go has handler: ${h##* }"
    else
        bad "ha.go missing handler: $h"
    fi
done

# --- contract B: ha_test.go with pure-Go unit tests ---------------
echo
echo "=== contract B: internal/feature/admin/ha_test.go ==="
if [ -f internal/feature/admin/ha_test.go ]; then
    ok "internal/feature/admin/ha_test.go exists"
else
    bad "internal/feature/admin/ha_test.go missing"
fi
for t in \
    "TestParseHAAddNodeForm_OK" \
    "TestParseHAAddNodeForm_Errors" \
    "TestParseHAChainEditForm_UpdatesPriorities" \
    "TestParseHAChainEditForm_MissingOldHostnames" \
    "TestParseHAChainEditForm_DuplicatePriorities" \
    "TestParseHARegapiCredsForm_OK" \
    "TestParseHARegapiCredsForm_TrimsWhitespace" \
    "TestParseHARegapiCredsForm_DelegatesToCredentialsValidate" \
    "TestIsHAForceActionConfirmationCorrect" \
    "TestFormatHAChainForTemplate_EmptyChain" \
    "TestFormatHAChainForTemplate_ListsMembers" \
    "TestRegapiCredentialsValidate_FormAndLibraryAgree"; do
    if grep -q "$t" internal/feature/admin/ha_test.go; then
        ok "ha_test.go has test: $t"
    else
        bad "ha_test.go missing test: $t"
    fi
done

# --- contract C: template renders all 6 sections -----------------
echo
echo "=== contract C: internal/handlers/templates/admin/ha.html ==="
if [ -f internal/handlers/templates/admin/ha.html ]; then
    ok "internal/handlers/templates/admin/ha.html exists"
else
    bad "internal/handlers/templates/admin/ha.html missing"
fi
for marker in \
    "body-admin-ha" \
    "ha.section_topology" \
    "ha.section_policy" \
    "ha.add_node" \
    "ha.section_regapi" \
    "ha.section_force" \
    "ha.section_audit"; do
    if grep -q "$marker" internal/handlers/templates/admin/ha.html; then
        ok "ha.html has section marker: $marker"
    else
        bad "ha.html missing section marker: $marker"
    fi
done

# --- contract D: i18n keys (ru + en parity) -------------------------
echo
echo "=== contract D: i18n catalog parity for ha.* keys ==="
# The TestCatalogsParity unit test in the i18n package
# catches the RU+EN mismatch. This contract is a "is the
# TestCatalogsParity test still PASSING" check — i.e. we
# run the test, see it green.
if go test -count=1 -run "TestCatalogsParity" ./internal/i18n/ 2>&1 | tee /tmp/b149_parity.log >/dev/null; then
    ok "TestCatalogsParity PASS (RU+EN ha.* keys in lock-step)"
else
    bad "TestCatalogsParity FAIL (RU+EN ha.* keys mismatch) — see /tmp/b149_parity.log"
    head -10 /tmp/b149_parity.log
fi
# Spot-check a few specific keys exist in BOTH maps.
for key in \
    "ha.title" \
    "ha.subtitle" \
    "ha.col_hostname" \
    "ha.add_node" \
    "ha.regapi_save" \
    "ha.force_promote" \
    "ha.section_audit"; do
    if grep -q "\"$key\"" internal/i18n/catalog_admin.go; then
        count=$(grep -c "\"$key\"" internal/i18n/catalog_admin.go)
        if [ "$count" -ge 2 ]; then
            ok "ha key present in both maps: $key (count=$count)"
        else
            bad "ha key only in one map: $key (count=$count, need 2)"
        fi
    else
        bad "ha key missing entirely: $key"
    fi
done

# --- contract E: main.go routes + Service fields ------------------
echo
echo "=== contract E: cmd/skygate/main.go routes + Service struct ==="
for route in \
    "GET /admin/ha" \
    "POST /admin/ha/chain/edit" \
    "POST /admin/ha/auto-reclaim-toggle" \
    "POST /admin/ha/node/add" \
    "POST /admin/ha/node/remove" \
    "POST /admin/ha/force-promote" \
    "POST /admin/ha/force-demote" \
    "POST /admin/ha/reclaim" \
    "POST /admin/ha/regapi/save" \
    "POST /admin/ha/regapi/test"; do
    if grep -q "mux.Handle(\"$route\"" cmd/skygate/main.go; then
        ok "main.go registers route: $route"
    else
        bad "main.go missing route: $route"
    fi
done
# Service struct carries the two new fields.
if grep -q "RegapiStore" internal/feature/admin/service.go; then
    ok "service.go has RegapiStore field"
else
    bad "service.go missing RegapiStore field"
fi
if grep -q "SelfHostname" internal/feature/admin/service.go; then
    ok "service.go has SelfHostname field"
else
    bad "service.go missing SelfHostname field"
fi
# main.go actually wires the RegapiStore from the config.
if grep -q "regapi.NewStore" cmd/skygate/main.go; then
    ok "main.go wires regapi.NewStore"
else
    bad "main.go does not wire regapi.NewStore"
fi
# main.go wires SelfHostname too.
if grep -q "SelfHostname:" cmd/skygate/main.go; then
    ok "main.go wires SelfHostname"
else
    bad "main.go does not wire SelfHostname"
fi
# Layout has the menu link.
if grep -q 'href="/admin/ha"' internal/handlers/templates/layout.html; then
    ok "layout.html has /admin/ha menu link"
else
    bad "layout.html missing /admin/ha menu link"
fi
# sectionPageSet includes admin/ha so the sidebar auto-opens.
if grep -q '"admin/ha"' internal/handlers/handlers.go; then
    ok "handlers.go sectionPageSet includes admin/ha"
else
    bad "handlers.go sectionPageSet missing admin/ha"
fi

# --- summary ----------------------------------------------------------
echo
echo "=== summary ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
if [ "$FAIL" -eq 0 ]; then
    echo "all B149 contracts satisfied"
    exit 0
else
    echo "B149 contracts NOT satisfied"
    exit 1
fi
