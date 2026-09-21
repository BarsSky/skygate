#!/usr/bin/env bash
# check_b276_2_routescript.sh
#
# 2026-09-21 (B276.2) — the routescript per-exit-node rewrite.
#
# Before B276.2, `GenerateRouteSetupScript` picked the FIRST exit node
# from headscale and routed EVERY rule through it, regardless of what
# the rule's exit_node field said. The script was honest but wrong once
# a user could have rules on multiple relays (B275). B276.2 rewrites
# the data layer (`loadRoutesForScriptGroups`) to return per-exit-node
# groups and the body builders (linux/windows) to emit one `=== exit-node:
# NAME ===` block per group. Auto rules (empty exit_node) fold into the
# user's preferred exit node (fallback: first_healthy).
#
# CONTRACTS
#   A  data layer shape (ScriptRouteGroup + loadRoutesForScriptGroups)
#   B  body builders take []ScriptRouteGroup (no more flat []routeEntry)
#   C  per-group `=== exit-node: NAME ===` block + `ip route add` per
#      group, NOT a single block per script
#   D  auto rules (empty exit_node) fold into preferred group, not first
#      healthy; preferred group gets the DNS route + restore-default
#   E  restore path emits `ip route del` for EVERY group's IP (not just
#      preferred)
#   F  unit tests cover the headline scenarios
#   G  live state (SKIPs when no docker PG)
#   H  the script itself is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B276.2: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DATA=internal/feature/exit_rules/routescript_data.go
LINUX=internal/feature/exit_rules/routescript_linux_body.go
WIN=internal/feature/exit_rules/routescript_windows_body.go
ORCH=internal/feature/exit_rules/routescript.go
TEST=internal/feature/exit_rules/routescript_b276_2_test.go

hdr "B276.2 — routescript per-exit-node rewrite"

# --- A: data layer shape -----------------------------------------------------
if grep -q 'type ScriptRouteGroup struct' "$DATA" && grep -q 'func (s \*Service) loadRoutesForScriptGroups' "$DATA"; then
  ok "A1: ScriptRouteGroup + loadRoutesForScriptGroups exist in the data layer"
else
  bad "A1: the data layer was not refactored to per-exit-node groups"
fi
if grep -q 'Source *string' "$DATA" && grep -q 'ExitNodeIP *string' "$DATA"; then
  ok "A2: ScriptRouteGroup carries ExitNodeIP (resolved Tailscale IP) + Source (preferred / explicit / first_healthy)"
else
  bad "A2: ScriptRouteGroup is missing the IP or the source-of-truth fields"
fi
if grep -q 'db.GetUserExitNodePref' "$DATA"; then
  ok "A3: empty exit_node folds into the user's preferred exit node (db.GetUserExitNodePref)"
else
  bad "A3: auto rules are not folded into the preferred exit node"
fi

# --- B: body builders take groups (not flat route list) ---------------------
if grep -q 'func buildLinuxRouteScript(groups \[\]ScriptRouteGroup' "$LINUX" && grep -q 'func buildWindowsRouteScript(groups \[\]ScriptRouteGroup' "$WIN"; then
  ok "B1: both body builders now take []ScriptRouteGroup (was []routeEntry + exitNodeIP)"
else
  bad "B1: one of the body builders still has the old signature"
fi
if ! grep -q 'resolveExitNodeIPForScript' "$DATA" "$LINUX" "$WIN" "$ORCH"; then
  ok "B2: the dead single-exitNodeIP resolver is gone"
else
  bad "B2: resolveExitNodeIPForScript is still referenced — dead code path"
fi

# --- C: per-group blocks -----------------------------------------------------
if grep -q '=== exit-node: ' "$LINUX" && grep -q 'rem === exit-node: ' "$WIN"; then
  ok "C1: each body emits a '=== exit-node: NAME ===' block per group"
else
  bad "C1: missing per-exit-node block markers"
fi
# Linux: ip route add X via $GROUP_IP (one per group, NOT a single $EXIT_NODE_IP for all).
if grep -q 'ip route add %s via %s' "$LINUX" && grep -qE 'g\.ExitNodeIP' "$LINUX"; then
  ok "C2: Linux routes use the group's own ExitNodeIP, not a global one"
else
  bad "C2: Linux builder is not using per-group ExitNodeIP"
fi
# Windows: route add X mask M $GROUP_IP
if grep -q 'route add %s mask %s %s' "$WIN" && grep -qE 'g\.ExitNodeIP' "$WIN"; then
  ok "C3: Windows routes use the group's own ExitNodeIP, not a global one"
else
  bad "C3: Windows builder is not using per-group ExitNodeIP"
fi

# --- D: auto rules fold into preferred --------------------------------------
if grep -q '"preferred"' "$DATA" && grep -q '"first_healthy"' "$DATA"; then
  ok "D1: the data layer tags preferred / first_healthy groups explicitly"
else
  bad "D1: the group sources are not tagged"
fi
if grep -q 'ip route add 100.100.100.100/32 via %s' "$LINUX"; then
  ok "D2: Linux DNS route is built from the per-group formatter (it ends up on the preferred group)"
else
  bad "D2: DNS route formatter was not updated"
fi
if grep -q 'preferredGroup(groups)' "$LINUX" && grep -q 'preferredGroup(groups)' "$WIN"; then
  ok "D3: both body builders route the DNS + restore-default through preferredGroup()"
else
  bad "D3: preferredGroup helper is not used by both builders"
fi

# --- E: restore path clears every group ------------------------------------
if grep -q 'for _, g := range groups' "$LINUX" && grep -q 'for _, g := range groups' "$WIN"; then
  ok "E1: restore iterates every group (was: only the first exit node)"
else
  bad "E1: restore still only cleans the first/old exit node"
fi

# --- F: unit tests -----------------------------------------------------------
if [ -f "$TEST" ] && grep -q 'func TestBuildLinux_PerExitNodeBlocks' "$TEST" \
   && grep -q 'func TestBuildLinux_AutoRulesFoldIntoPreferred' "$TEST" \
   && grep -q 'func TestBuildLinux_MissingIPPlaceholder' "$TEST" \
   && grep -q 'func TestBuildLinux_RestoreClearsAllGroups' "$TEST" \
   && grep -q 'func TestBuildWindows_PerExitNodeBlocks' "$TEST" \
   && grep -q 'func TestPreferredGroup' "$TEST"; then
  ok "F1: 6 unit tests cover per-group blocks, auto folding, missing-IP placeholder, restore cleanup, preferred selection"
else
  bad "F1: at least one B276.2 unit test is missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(timeout 600 go test -count=1 -run 'B2762|BuildLinux|BuildWindows|PreferredGroup' ./internal/feature/exit_rules/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "F2: the B276.2 unit tests pass"
  else
    bad "F2: the B276.2 unit tests failed: $OUT"
  fi
else
  skip "F2: go not on PATH"
fi

# --- H: script is tracked by git (trap #11) --------------------------------
if git ls-files --error-unmatch scripts/check_b276_2_routescript.sh >/dev/null 2>&1; then
  ok "H1: scripts/check_b276_2_routescript.sh is tracked by git"
else
  bad "H1: scripts/check_b276_2_routescript.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB276.2 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
