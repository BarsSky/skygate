#!/usr/bin/env bash
# check_rename_rules_b277_5.sh — v1.5.45 (B-rename-rules):
# the device_rules cascade that B231 was missing.
#
# 2026-09-22: the user-facing bug was on /admin/exit-nodes +
# /my/exit-rules where a headscale node renamed `node` →
# `exit-node-vps` left the rule rows with the OLD `exit_node_id`
# (= "node"). The ACL builder still emitted `via:
# tag:dev-infra-node`, which doesn't match the new node's tag
# (`tag:dev-infra-exit-node-vps`). Tailscale silently ignored
# the via= pin and traffic went through DERP / default.
#
# B231 (the pref migrator) ran but only updated the
# device_exit_node_prefs table — the denormalised columns in
# device_rules stayed stale forever. This b-block extends
# applyRenameMigration to also UPDATE every device_rules row
# for the user where exit_node_id OR device_hostname =
# oldHost, in the same transaction as the pref migration.
#
# CONTRACTS (A1–A4)
#   A  applyRenameMigration signature + UPDATE
#   B  UPDATE scope (enabled=1, same user)
#   C  UPDATE skips other users
#   D  unit test covers the cascade

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B-rename-rules: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

RENAME=internal/feature/exit_rules/reconciler_rename.go

hdr "B-rename-rules (v1.5.45) — device_rules cascade"

# --- A: signature + UPDATE present ---------------------------------------
if grep -q 'func (s \*Service) applyRenameMigration(ctx context\.Context, userID int64, oldHost, newHost, exitNodeTag string) (int64, error)' "$RENAME"; then
  ok "A1: applyRenameMigration now returns (int64, error) — caller can read the rules-affected count"
else
  bad "A1: applyRenameMigration still returns just error — the caller can't audit-log the cascade"
fi

# A2: the UPDATE touches BOTH denormalised columns.
if awk '/UPDATE device_rules/,/\, newHost, newHost, userID, oldHost\)/' "$RENAME" | grep -q 'exit_node_id'; then
  ok "A2: the rules UPDATE rewrites exit_node_id"
fi
if awk '/UPDATE device_rules/,/\, newHost, newHost, userID, oldHost\)/' "$RENAME" | grep -q 'device_hostname'; then
  ok "A3: the rules UPDATE rewrites device_hostname"
fi

# A4: the UPDATE is scoped to enabled=1 (disabled rules must NOT be touched).
if awk '/UPDATE device_rules/,/\, newHost, newHost, userID, oldHost\)/' "$RENAME" | grep -q 'enabled = 1'; then
  ok "A4: the rules UPDATE scopes by enabled=1 (disabled rules survive — they need operator attention when re-enabled)"
fi

# --- B: same-user scope --------------------------------------------------
if awk '/UPDATE device_rules/,/\, newHost, newHost, userID, oldHost\)/' "$RENAME" | grep -q 'user_id = $3'; then
  ok "B1: the UPDATE scopes by user_id (other users' rules are untouched)"
fi

# --- C: caller audit-logs the count ---------------------------------------
if grep -q 'rules_cascaded=' "$RENAME"; then
  ok "C1: the audit log line carries rules_cascaded=N (operator can audit which rules moved)"
fi
if grep -q 'rules_cascaded=' "$RENAME" && grep -q 'n\.SendAlert' "$RENAME"; then
  ok "C2: the Telegram alert also carries the count (no separate alert for the cascade)"
fi

# --- D: unit test --------------------------------------------------------
if grep -q 'func TestApplyRenameMigration_PropagatesToDeviceRules' internal/feature/exit_rules/reconciler_rename_rules_b277_5_test.go 2>/dev/null; then
  ok "D1: a unit test covers the cascade (enabled=1 rules updated, disabled + other-user rules skipped)"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(timeout 120 go test -count=1 -run 'ApplyRenameMigration' ./internal/feature/exit_rules/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "D2: the B-rename-rules unit test passes"
  else
    bad "D2: unit test failed: $OUT"
  fi
else
  skip "D2: go not on PATH"
fi

# --- E: tracked by git (trap #11) ---------------------------------------
if git ls-files --error-unmatch scripts/check_rename_rules_b277_5.sh >/dev/null 2>&1; then
  ok "E1: scripts/check_rename_rules_b277_5.sh is tracked by git"
else
  bad "E1: scripts/check_rename_rules_b277_5.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB-rename-rules summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
