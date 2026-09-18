#!/usr/bin/env bash
# check_b150.sh — v1.5.0 / B150 contracts.
#
# This is the B-check that pins the /admin/deploy page +
# `skygate deploy {push,pull,sync,status}` and
# `skygate ha {promote,demote,reclaim}` CLI subcommands
# (Phase 6 of the v1.5.0 BL-2 plan). It verifies five
# things:
#
#   A. internal/deploy/{subcommand,push,pull,ha}.go exist
#      with the 7 CLI functions documented in the plan
#      (Run, RunPush, RunPull, RunStatus, HAPromote,
#      HADemote, HAReclaim).
#   B. internal/feature/admin/deploy.go exists with the
#      3 documented handlers (GetAdminDeploy,
#      PostAdminDeployPush, PostAdminDeployTestFailover).
#   C. internal/handlers/templates/admin/deploy.html
#      exists and renders the 4 page sections per the
#      BL-2 plan §5.1 / Phase 6 (topology, controls,
#      HA actions, audit).
#   D. The i18n catalog has the 10 new deploy.* keys
#      in BOTH ruAdmin and enAdmin maps (parity is
#      checked separately by TestCatalogsParity in the
#      i18n package).
#   E. cmd/skygate/main.go wires the 7 subcommand cases
#      + 3 admin routes + the layout has the /admin/deploy
#      sidebar link + sectionPageSet includes admin/deploy.
#
# The script is intentionally read-only — it does not
# touch the database or run the live VM. The unit tests
# (`go test ./...`) cover the runtime contract; this
# script is the "is the code even there?" check.

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

# --- contract A: internal/deploy/* with 7 CLI functions -----------------
echo
echo "=== contract A: internal/deploy/{subcommand,push,pull,ha}.go ==="
for f in subcommand.go push.go pull.go ha.go; do
    if [ -f "internal/deploy/$f" ]; then
        ok "internal/deploy/$f exists"
    else
        bad "internal/deploy/$f missing"
    fi
done
for sig in \
    "func Run(ctx context.Context, args []string) error" \
    "func RunPush(ctx context.Context, d *Deps, target string) error" \
    "func RunPull(ctx context.Context, d *Deps, target string) error" \
    "func RunStatus(ctx context.Context, d *Deps, target string) error" \
    "func HAPromote(ctx context.Context, d *Deps, target string) error" \
    "func HADemote(ctx context.Context, d *Deps, target string) error" \
    "func HAReclaim(ctx context.Context, d *Deps) error" \
    "func OpenDepsFromEnv(ctx context.Context, dsn, bucket, selfHost, binPath string, buildInfo BuildInfo) (*Deps, error)"; do
    name=$(echo "$sig" | sed -E 's/^func ([A-Za-z]+).*/\1/')
    found=0
    for f in internal/deploy/subcommand.go internal/deploy/push.go internal/deploy/pull.go internal/deploy/ha.go; do
        if [ -f "$f" ] && grep -q -F "$sig" "$f"; then
            found=1
            break
        fi
    done
    if [ "$found" = "1" ]; then
        ok "deploy package has function: $name"
    else
        bad "deploy package missing function: $name"
    fi
done
# OpenDepsFromEnv must be exported (capitalized) so the
# admin package can call it from the /admin/deploy
# handlers.
if grep -q "^func OpenDepsFromEnv" internal/deploy/subcommand.go; then
    ok "OpenDepsFromEnv is exported (visible to admin package)"
else
    bad "OpenDepsFromEnv is not exported (lowercase or missing)"
fi

# --- contract B: internal/feature/admin/deploy.go with 3 handlers -----
echo
echo "=== contract B: internal/feature/admin/deploy.go ==="
if [ -f internal/feature/admin/deploy.go ]; then
    ok "internal/feature/admin/deploy.go exists"
else
    bad "internal/feature/admin/deploy.go missing"
fi
for h in \
    "func (s *Service) GetAdminDeploy" \
    "func (s *Service) PostAdminDeployPush" \
    "func (s *Service) PostAdminDeployTestFailover"; do
    if grep -q -F "$h" internal/feature/admin/deploy.go; then
        ok "deploy.go has handler: ${h##* }"
    else
        bad "deploy.go missing handler: $h"
    fi
done
# The handlers must import the internal/deploy package
# (the B150 contract is that the /admin/deploy page
# delegates to the same internal/deploy primitives the
# CLI uses).
if grep -q '"skygate/internal/deploy"' internal/feature/admin/deploy.go; then
    ok "deploy.go imports internal/deploy"
else
    bad "deploy.go missing internal/deploy import"
fi

# --- contract C: deploy.html template renders 4 sections --------------
echo
echo "=== contract C: internal/handlers/templates/admin/deploy.html ==="
if [ -f internal/handlers/templates/admin/deploy.html ]; then
    ok "internal/handlers/templates/admin/deploy.html exists"
else
    bad "internal/handlers/templates/admin/deploy.html missing"
fi
for marker in \
    "body-admin-deploy" \
    "deploy.title" \
    "deploy.section_controls" \
    "deploy.push_button" \
    "deploy.test_failover_button" \
    "deploy.dry_run_label" \
    "ha.section_force" \
    "ha.section_audit"; do
    if grep -q "$marker" internal/handlers/templates/admin/deploy.html; then
        ok "deploy.html has section marker: $marker"
    else
        bad "deploy.html missing section marker: $marker"
    fi
done

# --- contract D: i18n catalog parity for deploy.* keys ----------------
echo
echo "=== contract D: i18n catalog parity for deploy.* keys ==="
# The TestCatalogsParity unit test in the i18n package
# catches the RU+EN mismatch. This contract is a "is the
# TestCatalogsParity test still PASSING" check — i.e. we
# run the test, see it green.
if go test -count=1 -run "TestCatalogsParity" ./internal/i18n/ 2>&1 | tee /tmp/b150_parity.log >/dev/null; then
    ok "TestCatalogsParity PASS (RU+EN deploy.* keys in lock-step)"
else
    bad "TestCatalogsParity FAIL (RU+EN deploy.* keys mismatch) — see /tmp/b150_parity.log"
    head -10 /tmp/b150_parity.log
fi
# Spot-check the 10 specific deploy.* keys exist in BOTH maps.
for key in \
    "deploy.title" \
    "deploy.subtitle" \
    "deploy.section_controls" \
    "deploy.controls_help" \
    "deploy.target_label" \
    "deploy.push_button" \
    "deploy.test_failover_title" \
    "deploy.test_failover_help" \
    "deploy.test_failover_button" \
    "deploy.dry_run_label"; do
    if grep -q "\"$key\"" internal/i18n/catalog_admin.go; then
        count=$(grep -c "\"$key\"" internal/i18n/catalog_admin.go)
        if [ "$count" -ge 2 ]; then
            ok "deploy key present in both maps: $key (count=$count)"
        else
            bad "deploy key only in one map: $key (count=$count, need 2)"
        fi
    else
        bad "deploy key missing entirely: $key"
    fi
done
# nav.deploy (in catalog_common.go, the sidebar label).
if grep -q '"nav.deploy"' internal/i18n/catalog_common.go; then
    count=$(grep -c '"nav.deploy"' internal/i18n/catalog_common.go)
    if [ "$count" -ge 2 ]; then
        ok "nav.deploy present in both maps (count=$count)"
    else
        bad "nav.deploy only in one map (count=$count, need 2)"
    fi
else
    bad "nav.deploy missing entirely from catalog_common.go"
fi

# --- contract E: main.go routes + subcommands + layout link ---------
echo
echo "=== contract E: cmd/skygate/main.go routes + subcommands ==="
# 3 admin routes (page + push + test-failover)
for route in \
    "GET /admin/deploy" \
    "POST /admin/deploy/push" \
    "POST /admin/deploy/test-failover"; do
    if grep -q "mux.Handle(\"$route\"" cmd/skygate/main.go; then
        ok "main.go registers route: $route"
    else
        bad "main.go missing route: $route"
    fi
done
# 7 subcommand dispatch cases (deploy-{push,pull,sync,status} + ha-{promote,demote,reclaim})
#
# Match the token `"deploy-push"` anywhere on a line —
# the dispatch is written as one
# `case "deploy-push", "deploy-pull", ...:` line, so a
# stricter `case "$sub"` prefix match would miss the
# tokens after the first comma.
for sub in \
    "deploy-push" \
    "deploy-pull" \
    "deploy-sync" \
    "deploy-status" \
    "ha-promote" \
    "ha-demote" \
    "ha-reclaim"; do
    if grep -q "\"$sub\"" cmd/skygate/main.go; then
        ok "main.go handles subcommand: $sub"
    else
        bad "main.go missing subcommand case: $sub"
    fi
done
# main.go imports the internal/deploy package.
if grep -q '"skygate/internal/deploy"' cmd/skygate/main.go; then
    ok "main.go imports internal/deploy"
else
    bad "main.go missing internal/deploy import"
fi
# runDeploySubcommand and runHASubcommand helper functions exist.
for helper in "func runDeploySubcommand" "func runHASubcommand"; do
    if grep -q -F "$helper" cmd/skygate/main.go; then
        ok "main.go has helper: ${helper##* }"
    else
        bad "main.go missing helper: $helper"
    fi
done
# Layout has the /admin/deploy menu link.
if grep -q 'href="/admin/deploy"' internal/handlers/templates/layout.html; then
    ok "layout.html has /admin/deploy menu link"
else
    bad "layout.html missing /admin/deploy menu link"
fi
# sectionPageSet includes admin/deploy so the sidebar auto-opens.
if grep -q '"admin/deploy"' internal/handlers/handlers.go; then
    ok "handlers.go sectionPageSet includes admin/deploy"
else
    bad "handlers.go sectionPageSet missing admin/deploy"
fi

# --- summary ----------------------------------------------------------
echo
echo "=== summary ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
if [ "$FAIL" -eq 0 ]; then
    echo "all B150 contracts satisfied"
    exit 0
else
    echo "B150 contracts NOT satisfied"
    exit 1
fi
