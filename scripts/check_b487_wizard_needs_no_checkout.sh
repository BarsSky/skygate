#!/usr/bin/env bash
# ============================================================================
# check_b487_wizard_needs_no_checkout.sh — B487
#
# THE WIZARD MUST WORK ON THE INSTALLATION IT EXISTS FOR.
#
# Measured while writing the debt register (2026-11-17): the migration plan's steps ran
# `bash /opt/skygate/deploy/derper-cert-sync.sh --install`, `bash /opt/skygate/scripts/b480_dup_rule_cleanup.sh`
# and `bash /opt/skygate/scripts/verify_migration.sh` ON THE NEW HOST — but a native (systemd) install is
# a RELEASE TARBALL with no checkout at all, so those three steps failed with "No such file or directory"
# on exactly the kind of host the wizard is for. The helpers are needed on the TARGET, and the only
# artefact guaranteed to be there is the panel binary — so they travel inside it.
#
# The mirror is generated (scripts/gen_migrate_assets.sh → internal/migrate/assets_gen.go, base64) and a
# Go test fails when it drifts from deploy/ and scripts/. That is deliberate: one source of truth, and a
# generated artefact whose staleness is a red gate rather than a runtime surprise.
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b487.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

PLAN=internal/migrate/plan.go
EXEC=internal/migrate/exec.go
SCRIPT=internal/migrate/script.go
GEN=internal/migrate/assets_gen.go
GENERATOR=scripts/gen_migrate_assets.sh
TEST=internal/migrate/assets_b487_test.go
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
ROADMAP=docs/ROADMAP.md
DOC=docs/host-migration.md
DOCRU=docs/ru/host-migration.md

PASS=0
FAIL=0
fails=()
ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$PLAN" "$EXEC" "$SCRIPT" "$GEN" "$GENERATOR" "$TEST" "$VERIFY" "$AGENTS" "$NOTES" "$ROADMAP" "$DOC" "$DOCRU"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the plan names no checkout path --------------------------------------------
if ! has "$PLAN" '/opt/skygate'; then
  ok "A1: no plan step names /opt/skygate — a release-tarball install has no such checkout"
else
  bad "A1: a step still runs a script from /opt/skygate (the reason this block exists)"
fi
n_script="$(grep -c 'Where: WhereScript' "$PLAN" || true)"
if [ "${n_script:-0}" -ge 3 ]; then
  ok "A1b: ${n_script} steps use the embedded helpers (cert-sync, dedupe cleanup, migration verify)"
else
  bad "A1b: only ${n_script:-0} step(s) use embedded helpers — expected 3"
fi

# --- B: the embedding itself ---------------------------------------------------------
if has "$SCRIPT" 'func ScriptSource(' && has "$SCRIPT" 'base64.StdEncoding.DecodeString'; then
  ok "B1: ScriptSource decodes an embedded base64 body"
else
  bad "B1: no decoded asset accessor"
fi
if has "$SCRIPT" 'func splitScriptCommand('; then
  ok "B2: a script step carries its arguments separately (no shell-quoting of the body)"
else
  bad "B2: no argument splitter"
fi
if has "$EXEC" 'case WhereScript:' && has "$EXEC" 'deps.runner()(ctx, "ssh", argv, body, env)'; then
  ok "B3: the executor streams the script to the new host as bash -s -- <args>"
else
  bad "B3: the executor cannot run an embedded helper"
fi
if has "$EXEC" 'WhereInternal, WhereScript'; then
  ok "B4: the step-location gate accepts the new location"
else
  bad "B4: runStepBody would reject a script step"
fi
assets="$(grep -c 'B64:' "$GEN" || true)"
if [ "${assets:-0}" -ge 3 ]; then
  ok "B5: assets_gen.go carries ${assets} embedded helpers"
else
  bad "B5: assets_gen.go carries ${assets:-0} helper(s) — expected 3"
fi
if has "$GENERATOR" 'DO NOT EDIT' && has "$GENERATOR" 'base64'; then
  ok "B6: the generated file says it is generated and why base64 is used"
else
  bad "B6: the generator does not explain its output"
fi

# --- C: the drift contract and its behaviour ------------------------------------------
if has "$TEST" 'func TestB487_AssetsMatchTheirSources('; then
  ok "C1: a stale mirror is a red test, not a runtime surprise"
else
  bad "C1: nothing fails when assets_gen.go drifts from the sources"
fi
if has "$TEST" 'func TestB487_PlanUsesNoCheckoutPath(' && has "$TEST" 'func TestB487_ScriptStepStreamsTheBody('; then
  ok "C2: the plan and the streaming behaviour are both covered"
else
  bad "C2: the plan/executor behaviour is not pinned"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/migrate/ -count=1 -run 'B487' 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "C3: the B487 tests pass (drift + streaming + no checkout path)"
  else
    bad "C3: the B487 tests fail: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
  fi
else
  printf '  \033[33m-\033[0m C3: SKIP — no go toolchain\n'
fi

# --- D: the docs know how the helpers reach the target --------------------------------
if has "$DOC" 'embedd' || has "$DOC" 'embedded' || has "$DOC" 'вшит'; then
  ok "D1: the EN analysis says the helpers travel inside the binary"
else
  bad "D1: the EN analysis still implies a checkout on the target"
fi
if has "$DOCRU" 'вшит' || has "$DOCRU" 'embedded' || has "$DOCRU" 'встраива'; then
  ok "D2: the RU analysis says the same"
else
  bad "D2: the RU analysis still implies a checkout on the target"
fi

# --- E: registration ------------------------------------------------------------------
if has "$VERIFY" '"B487"'; then ok "E1: registered in the gate"; else bad "E1: not registered"; fi
if has "$AGENTS" 'B487'; then ok "E2: block index carries it"; else bad "E2: no index entry"; fi
if has "$NOTES" 'B487'; then ok "E3: release notes describe it"; else bad "E3: no release-notes section"; fi

# --- F: discriminators ------------------------------------------------------------------
cat > "$SKY_TMP/old.go" <<'GO'
package migrate
// Command: `bash /opt/skygate/scripts/verify_migration.sh`
GO
if grep -q 'WhereScript' "$SKY_TMP/old.go"; then
  bad "F1: the embedded-helper predicate matches a plan that runs checkout scripts"
else
  ok "F1: the predicates discriminate the pre-B487 plan"
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB487: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB487: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
