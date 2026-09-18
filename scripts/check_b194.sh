#!/usr/bin/env bash
# ============================================================================
# check_b194.sh — B194 (v1.5.0) auto-deploy framework
#
# Verifies:
#   A) internal/deployrun/ package exists with 4+ core files
#      (types, framework, registry, sse, s3client, handlers)
#   B) internal/deployrun/steps/ has 6 step files (Phase 1)
#   C) Each step file implements the DeployStep interface
#      (Name, Description, Run, Rollback, IsOptional, DependsOn)
#   D) The framework has a Run() entry point that takes
#      (context.Context, *DeployRun, []DeployStep) error
#   E) DB migration adds deploy_runs + deploy_run_steps tables
#   F) The SSE broker has Publish + Subscribe + Close methods
#   G) AGENTS.md mentions B194
#   H) verify_pre_deploy.sh runs check_b194.sh
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PASS=0
FAIL=0

ok() { echo "  ✓ $*"; PASS=$((PASS+1)); }
no() { echo "  ✗ $*"; FAIL=$((FAIL+1)); }

# ----- A) package + core files -----------------------------------------
echo "A) internal/deployrun/ package + core files"
if [ -d "$PROJECT_DIR/internal/deployrun" ]; then
  ok "internal/deployrun/ directory exists"
else
  no "internal/deployrun/ directory missing"
fi
for f in types.go framework.go registry.go sse.go s3client.go handlers.go; do
  if [ -f "$PROJECT_DIR/internal/deployrun/$f" ]; then
    ok "internal/deployrun/$f exists"
  else
    no "internal/deployrun/$f missing"
  fi
done

# ----- B) step files -----------------------------------------------------
echo "B) internal/deployrun/steps/ step files"
if [ -d "$PROJECT_DIR/internal/deployrun/steps" ]; then
  ok "internal/deployrun/steps/ directory exists"
else
  no "internal/deployrun/steps/ directory missing"
fi
for step in validate_input.go generate_preauth_key.go update_ha_chain.go push_env_s3.go tag_node.go audit_log.go; do
  if [ -f "$PROJECT_DIR/internal/deployrun/steps/$step" ]; then
    ok "step file: $step"
  else
    no "step file missing: $step"
  fi
done

# ----- C) DeployStep interface implementation -------------------------
echo "C) Each step file implements the DeployStep interface"
for step in validate_input generate_preauth_key update_ha_chain push_env_s3 tag_node audit_log; do
  f="$PROJECT_DIR/internal/deployrun/steps/${step}.go"
  if [ ! -f "$f" ]; then
    no "step file $f missing — skipping interface check"
    continue
  fi
  has_name=$(grep -c 'func .* Name() string' "$f" || true)
  has_desc=$(grep -c 'func .* Description() string' "$f" || true)
  has_run=$(grep -c 'func .* Run(ctx \*deployrun' "$f" || true)
  has_rollback=$(grep -c 'func .* Rollback(ctx \*deployrun' "$f" || true)
  has_opt=$(grep -c 'func .* IsOptional() bool' "$f" || true)
  has_dep=$(grep -c 'func .* DependsOn() \[\]string' "$f" || true)
  if [ "$has_name" -ge 1 ] && [ "$has_desc" -ge 1 ] && [ "$has_run" -ge 1 ] \
    && [ "$has_rollback" -ge 1 ] && [ "$has_opt" -ge 1 ] && [ "$has_dep" -ge 1 ]; then
    ok "step $step implements DeployStep"
  else
    no "step $step missing interface methods (name=$has_name desc=$has_desc run=$has_run rollback=$has_rollback opt=$has_opt dep=$has_dep)"
  fi
done

# ----- D) Framework.Run() entry point -----------------------------------
echo "D) Framework.Run() signature"
f="$PROJECT_DIR/internal/deployrun/framework.go"
if grep -qE 'func \(f \*Framework\) Run\(.*context\.Context, .*DeployRun, .*\[\]DeployStep.*\) error' "$f"; then
  ok "Framework.Run signature matches (ctx, *DeployRun, []DeployStep) error"
else
  no "Framework.Run signature does not match expected pattern"
fi

# ----- E) DB migration -------------------------------------------------
echo "E) DB migration for deploy_runs + deploy_run_steps"
f="$PROJECT_DIR/internal/db/migrations_v0_63_b194.go"
if [ -f "$f" ]; then
  ok "migration file $f exists"
else
  no "migration file $f missing"
fi
if grep -qE 'CREATE TABLE IF NOT EXISTS deploy_runs' "$f" 2>/dev/null; then
  ok "migration creates deploy_runs table"
else
  no "migration missing deploy_runs table"
fi
if grep -qE 'CREATE TABLE IF NOT EXISTS deploy_run_steps' "$f" 2>/dev/null; then
  ok "migration creates deploy_run_steps table"
else
  no "migration missing deploy_run_steps table"
fi
if grep -qE 'run_id.*BIGINT.*REFERENCES.*deploy_runs' "$f" 2>/dev/null; then
  ok "deploy_run_steps has FK to deploy_runs"
else
  no "deploy_run_steps missing FK to deploy_runs"
fi

# ----- F) SSE broker ----------------------------------------------------
echo "F) SSE broker (Subscribe/Publish/Close)"
f="$PROJECT_DIR/internal/deployrun/sse.go"
if [ -f "$f" ]; then
  for method in Subscribe Publish Close; do
    if grep -qE "func \(b \*SSEBroker\) $method\(" "$f"; then
      ok "SSEBroker.$method exists"
    else
      no "SSEBroker.$method missing"
    fi
  done
else
  no "sse.go missing"
fi

# ----- G) AGENTS.md -----------------------------------------------------
echo "G) AGENTS.md mentions B194"
f="$PROJECT_DIR/AGENTS.md"
if [ -f "$f" ] && grep -q "B194" "$f"; then
  ok "AGENTS.md mentions B194"
else
  no "AGENTS.md does NOT mention B194"
fi

# ----- H) verify_pre_deploy.sh integration -----------------------------
echo "H) verify_pre_deploy.sh runs check_b194.sh"
f="$PROJECT_DIR/scripts/verify_pre_deploy.sh"
if [ -f "$f" ] && grep -q "check_b194" "$f"; then
  ok "verify_pre_deploy.sh references check_b194"
else
  no "verify_pre_deploy.sh missing check_b194 reference"
fi

# ----- I) main.go wiring (B194.1) --------------------------------------
echo "I) main.go wiring (B194.1)"
f="$PROJECT_DIR/cmd/skygate/main.go"
if grep -q 'deployrun.NewService' "$f"; then
  ok "main.go constructs deployrun.NewService"
else
  no "main.go missing deployrun.NewService"
fi
if grep -q '"skygate/internal/deployrun"' "$f"; then
  ok "main.go imports internal/deployrun"
else
  no "main.go missing internal/deployrun import"
fi
if grep -q '_ "skygate/internal/deployrun/steps"' "$f"; then
  ok "main.go imports steps/ for init() side effects"
else
  no "main.go missing _ import of steps/"
fi
# Routes registered
for route in "GET /admin/deploys" "GET /admin/deploys/new" "POST /admin/deploys" "GET /admin/deploys/"; do
  if grep -q "mux.Handle.*\"$route\"" "$f"; then
    ok "route registered: $route"
  else
    no "route NOT registered: $route"
  fi
done

# ----- J) /admin/ha has "Add + auto-deploy" button (B194.1) -----------
echo "J) /admin/ha template has Add + auto-deploy button"
f="$PROJECT_DIR/internal/handlers/templates/admin/ha.html"
if grep -q '/admin/deploys/new' "$f"; then
  ok "ha.html links to /admin/deploys/new"
else
  no "ha.html missing /admin/deploys/new link"
fi

# ----- K) i18n keys for the button (B194.1) ----------------------------
echo "K) i18n keys for the auto-deploy button"
f="$PROJECT_DIR/internal/i18n/catalog_admin.go"
for key in "ha.add_node_auto_deploy_help" "ha.add_node_auto_deploy_btn"; do
  ru_count=$(grep -c "\"$key\"" "$f" || true)
  if [ "$ru_count" -ge 2 ]; then
    ok "i18n key $key has both RU + EN translations"
  else
    no "i18n key $key has only $ru_count translation(s) (need 2: RU + EN)"
  fi
done

# ----- L) HSClient adapter bridges headscale.Client → deployrun.HSClient
echo "L) HSClient adapter"
f="$PROJECT_DIR/internal/deployrun/adapter.go"
if [ -f "$f" ] && grep -q 'HSFactoryFromFunc' "$f"; then
  ok "adapter.go has HSFactoryFromFunc"
else
  no "adapter.go missing HSFactoryFromFunc"
fi
if grep -q 'hsClientAdapter' "$f" && grep -q 'CreatePreauthKeyWithTags' "$f"; then
  ok "hsClientAdapter wraps CreatePreauthKeyWithTags"
else
  no "hsClientAdapter does not wrap CreatePreauthKeyWithTags"
fi

# ----- M) handlers.go has real SSE streaming -------------------------
echo "M) Real SSE streaming in handlers.go"
f="$PROJECT_DIR/internal/deployrun/handlers.go"
if grep -q 'SSEBroker' "$f" && grep -q 'Subscribe' "$f" && grep -q 'MarshalEvent' "$f"; then
  ok "handlers.go wires real SSE (Subscribe + MarshalEvent)"
else
  no "handlers.go missing real SSE wiring"
fi
if grep -q 'setBroker' "$f" && grep -q 'clearBroker' "$f"; then
  ok "handlers.go has broker lifecycle (setBroker/clearBroker)"
else
  no "handlers.go missing broker lifecycle"
fi

# ----- summary ---------------------------------------------------------
echo ""
echo "B194 summary: $PASS PASS / $FAIL FAIL"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
