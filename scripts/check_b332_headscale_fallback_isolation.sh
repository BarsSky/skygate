#!/usr/bin/env bash
# check_b332_headscale_fallback_isolation.sh
#
# 2026-10-01 (B332) — two headscale tests failed on the reference VM and both
# were environment failures, not regressions.
#
# THE MEASUREMENT
# ---------------
# A full catalog run on the reference VM reported (baseline, before this fix):
#
#	--- FAIL: TestGetACLNamesEveryRungInTheError_B294 (2.79s)
#	    acl_read_b294_test.go:76: with no API, no file and no CLI the read must fail loudly
#	--- FAIL: TestGetACLAPIFailsNoContainer (0.41s)
#	    headscale_test.go:576: expected error for 500 + no CLI, got nil
#
# Both tests asserted a FAILURE path while assuming the other two rungs of
# `GetACL()` were unreachable. On the VM they are reachable: the `headscale`
# container is running and docker is on PATH, and the host's policy file is
# discoverable — so `GetACL()` returned the LIVE policy, and the tests read as a
# product regression when the product was fine.
#
# Two defects, not one:
#
#   1. THE CODE made the CLI rung impossible to disable. `runHeadscaleCLI`
#      replaced an empty `ExecContainer` with the literal container name
#      `headscale`, while `tags.go`, `preauth.go` and `nodes.go` all read
#      `ExecContainer == ""` as "the CLI path is not configured", and
#      headscale_test.go documented `c.ExecContainer = ""` as "disable CLI
#      fallback". The field now means what everyone already assumed: empty =
#      no container (the local binary is still tried, which is the native
#      install path). `New()` always sets a non-empty value, so production
#      behaviour is unchanged.
#
#   2. THE TESTS were not hermetic. Removing the wrong assumption is not the
#      same as skipping: a skipped test proves nothing on the host that
#      matters. `isolateHeadscaleFallbacks` now makes the premise TRUE on any
#      host — PATH is an empty temp dir (so neither `docker` nor `headscale`
#      resolves) and the two discovery env vars point at absent files.
#
# CONTRACTS
#   A. the empty container no longer silently becomes `headscale`
#   B. the isolation helper exists and isolates BOTH non-API rungs
#   C. both previously-failing tests use it (no t.Skip substitution)
#   D. the new semantics are pinned by their own tests, and the filter matches
#   E. tracked by git (AGENTS trap #11) and registered in the catalog

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B332: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ROUTES=internal/headscale/routes.go
ISO=internal/headscale/fallback_isolation_b332_test.go
B294=internal/headscale/acl_read_b294_test.go
HS=internal/headscale/headscale_test.go

hdr "B332 — a headscale test must not depend on the host running headscale"

# --- A: the empty container no longer becomes the default name ---------------
if [ -f "$ROUTES" ]; then
  ok "A1: $ROUTES exists"
else
  bad "A1: $ROUTES is missing"
fi
# The exact defect: `if container == "" { container = "headscale" }`.
if grep -nE 'container[[:space:]]*=[[:space:]]*"headscale"' "$ROUTES" | grep -v ':[[:space:]]*//' | grep -q .; then
  bad "A2: runHeadscaleCLI still replaces an empty ExecContainer with the literal container name 'headscale' — the field cannot be disabled, and two tests depend on being able to disable it"
else
  ok "A2: an empty ExecContainer is no longer silently replaced by the 'headscale' container name"
fi
if grep -qF 'dockerUsable := lookErr == nil && container != ""' "$ROUTES"; then
  ok "A3: the docker rung is gated on a NON-empty container (empty = try the local binary, the native-install path)"
else
  bad "A3: the docker rung is not gated on a non-empty container"
fi
if grep -qF 'container := c.ExecContainer' "$ROUTES"; then
  ok "A4: the container name still comes from the field, with no fallback literal"
else
  bad "A4: the container name no longer comes from ExecContainer — this contract is checking the wrong thing"
fi

# --- B: the isolation helper -------------------------------------------------
if [ -f "$ISO" ]; then
  ok "B1: $ISO exists"
else
  bad "B1: $ISO is missing — the isolation helper is what makes the premise true"
fi
if grep -q '^func isolateHeadscaleFallbacks' "$ISO"; then
  ok "B2: isolateHeadscaleFallbacks exists"
else
  bad "B2: isolateHeadscaleFallbacks is missing"
fi
if grep -qF 't.Setenv("PATH", t.TempDir())' "$ISO"; then
  ok "B3: it empties PATH (neither docker nor headscale resolves)"
else
  bad "B3: the helper does not neutralise PATH — a host with docker on PATH can still reach the CLI rung"
fi
if grep -qF 'SKYGATE_HEADSCALE_POLICY_PATH' "$ISO" && grep -qF 'SKYGATE_HEADSCALE_CONFIG' "$ISO"; then
  ok "B4: it points the policy-file discovery at absent paths (both env knobs)"
else
  bad "B4: the helper does not isolate the FILE rung — a host with a readable policy file would still succeed"
fi

# --- C: both previously-failing tests use it, and do not skip ----------------
for pair in "$B294:TestGetACLNamesEveryRungInTheError_B294" "$HS:TestGetACLAPIFailsNoContainer"; do
  f="${pair%%:*}"
  fn="${pair##*:}"
  if [ -f "$f" ] && grep -q "^func $fn" "$f"; then
    ok "C1: $fn still exists in $(basename "$f") (it was renegotiated, not deleted)"
  else
    bad "C1: $fn is gone from $f — removing the contract is not fixing it"
  fi
done
# The call must sit inside the test body, not merely be mentioned.
body_b294="$(awk '/^func TestGetACLNamesEveryRungInTheError_B294/,/^}/' "$B294")"
body_nocon="$(awk '/^func TestGetACLAPIFailsNoContainer/,/^}/' "$HS")"
if grep -q 'isolateHeadscaleFallbacks(t)' <<< "$body_b294"; then
  ok "C2: TestGetACLNamesEveryRungInTheError_B294 isolates the fallbacks"
else
  bad "C2: TestGetACLNamesEveryRungInTheError_B294 does not isolate them — it fails on any host that runs headscale"
fi
if grep -q 'isolateHeadscaleFallbacks(t)' <<< "$body_nocon"; then
  ok "C3: TestGetACLAPIFailsNoContainer isolates the fallbacks"
else
  bad "C3: TestGetACLAPIFailsNoContainer does not isolate them — it fails on any host that runs headscale"
fi
# A t.Skip would satisfy the gate while proving nothing on the VM.
if grep -q 't\.Skip' <<< "$body_nocon"; then
  bad "C4: TestGetACLAPIFailsNoContainer skips instead of isolating the environment — a skipped test proves nothing on the host that matters"
else
  ok "C4: the assertions were kept (no t.Skip substituted for the isolation)"
fi
if grep -q 'isolateHeadscaleFallbacks(t)' internal/headscale/headscale_test.go && \
   ! awk '/^func TestGetACLAPIFailsNoContainer/,/^}/' internal/headscale/headscale_test.go | grep -q 't\.Skip'; then
  ok "C5: the no-container test asserts on every platform, including Windows"
else
  bad "C5: the no-container test does not assert on every platform"
fi

# --- D: the semantics have their own tests -----------------------------------
if [ -f "$ISO" ] && grep -q '^func TestB332_EmptyExecContainerNeverUsesDocker' "$ISO"; then
  ok "D1: TestB332_EmptyExecContainerNeverUsesDocker pins the new semantics"
else
  bad "D1: the new semantics have no test of their own"
fi
if [ -f "$ISO" ] && grep -q '^func TestB332_ConfiguredContainerDoesUseDocker' "$ISO"; then
  ok "D2: TestB332_ConfiguredContainerDoesUseDocker pins the positive half (the fallback is not simply disabled)"
else
  bad "D2: nothing pins that a CONFIGURED container still reaches docker"
fi

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ -run 'TestGetACLNamesEveryRungInTheError_B294|TestGetACLAPIFailsNoContainer|TestB332_' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "D3: the four tests pass here"
  else
    bad "D3: the four tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  LIST="$(go test ./internal/headscale/ -list 'TestB332_' 2>&1)"
  if grep -q 'TestB332_EmptyExecContainerNeverUsesDocker' <<< "$LIST"; then
    ok "D4: the -list filter matches the new tests (no vacuous 'no tests to run')"
  else
    bad "D4: the -list filter matches nothing"
  fi
  # The whole package must stay green: this change touches a shared ladder.
  OUT="$(go test ./internal/headscale/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "D5: the whole internal/headscale package passes"
  else
    bad "D5: internal/headscale is not green after the change:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "D3-D5: go not on PATH — run the headscale tests on the VM"
fi

# --- E: tracked + registered -------------------------------------------------
if git ls-files --error-unmatch scripts/check_b332_headscale_fallback_isolation.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'check_b332_headscale_fallback_isolation.sh' scripts/verify_pre_deploy.sh; then
  ok "E2: verify_pre_deploy.sh registers B332"
else
  bad "E2: verify_pre_deploy.sh does not register B332"
fi
if grep -q 'B332' AGENTS.md; then
  ok "E3: AGENTS.md's block index carries B332"
else
  bad "E3: AGENTS.md has no B332 entry (AGENTS rule 2)"
fi

printf '\n\033[1mB332 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
