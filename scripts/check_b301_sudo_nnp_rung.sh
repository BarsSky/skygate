#!/usr/bin/env bash
# check_b301_sudo_nnp_rung.sh
#
# 2026-09-23 (B301) — a sudo refusal that can NEVER succeed must fall through to
# the privileged helper.
#
# Live on `aro`, with B300 already running (so the relay was correctly recognised
# as LOCAL and the ladder was actually walked):
#
#   staggeredSync(aggregated): exit-node-vps applied LOCALLY:
#     local=err=tailscale set (local, sudo): sudo: The "no new privileges" flag is set,
#     which prevents sudo from running as root. … — no SSH involved
#
# `deploy/install-common.sh` writes `NoNewPrivileges=yes` into the skygate unit, so
# sudo can NEVER become root from inside skygate — the kernel flag is checked before
# any sudoers rule. `isPrivilegeRefusal` did not know that phrase, so the refusal was
# treated as a REAL `tailscale set` failure and `ApplyRoutesLocally` returned one
# rung early: the root-owned helper sat installed and idle, `routes-apply.status`
# was never created, and every prefix stayed «нет маршрута». The v1.5.65 log line
# is the proof that the first two rungs were tried and the third was skipped.
#
# CONTRACTS
#   A. the systemd `NoNewPrivileges` refusal (and the other sudo-specific refusals
#      that cannot be fixed from inside the unit) is classified as a privilege
#      refusal, and the pre-existing needles survive
#   B. the ladder therefore REACHES the helper rung — proven behaviourally, not by
#      reading the source
#   C. a real `tailscale set` failure is still NOT a refusal (it must not be masked
#      by a retry on another rung)
#   D. the operator-facing hint names the trap, the tests pass, and this script is
#      tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B301: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

APPLY=internal/headscale/local_apply_b293.go
TEST=internal/headscale/local_apply_b301_test.go

hdr "B301 — a sudo refusal that can never succeed must fall through to the helper"

# --- A: the refusal class -----------------------------------------------------
if grep -q 'func isPrivilegeRefusal(' "$APPLY" && grep -q '"no new privileges"' "$APPLY"; then
  ok "A1: the systemd NoNewPrivileges refusal is a privilege refusal"
else
  bad "A1: 'no new privileges' is not recognised — the ladder stops one rung early on every systemd install"
fi
if grep -q '"prevents sudo from running as root"' "$APPLY"; then
  ok "A2: sudo's own wording of that refusal is recognised too"
else
  bad "A2: only one spelling of the systemd refusal is handled"
fi
missing=""
for needle in 'not allowed to execute' 'no tty present' 'sorry, user'; do
  grep -q "\"$needle\"" "$APPLY" || missing="$missing [$needle]"
done
if [ -z "$missing" ]; then
  ok "A3: the other sudo-only refusals are classified as privilege refusals"
else
  bad "A3: these sudo refusals are still treated as real failures:$missing"
fi
pre=""
for needle in 'a password is required' 'is not in the sudoers' 'permission denied' 'operation not permitted'; do
  grep -q "$needle" "$APPLY" || pre="$pre [$needle]"
done
if [ -z "$pre" ]; then
  ok "A4: the pre-existing needles (pinned by the B293 contracts) survive"
else
  bad "A4: a pre-existing needle disappeared:$pre"
fi

# --- B: the helper rung is reached --------------------------------------------
if grep -q 'if out, herr := applyViaHelper(routes, acceptRoutes); herr == nil {' "$APPLY"; then
  ok "B1: rung 3 is still the privileged helper"
else
  bad "B1: the helper rung changed shape"
fi
if [ -f "$TEST" ] && grep -q 'TestApplyRoutesLocally_NoNewPrivilegesFallsThroughToHelper_B301' "$TEST"; then
  ok "B2: the fall-through is pinned behaviourally (the live two-line refusal drives the ladder)"
else
  bad "B2: the fall-through has no regression test"
fi
if grep -q 'NoNewPrivileges' "$APPLY"; then
  ok "B3: the source names the flag that makes sudo impossible, so the next reader knows why"
else
  bad "B3: the reason this refusal must fall through is undocumented"
fi

# --- C/D: behaviour, hint, git ------------------------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ -run 'B301' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "C1: the B301 tests pass (including 'a real failure is NOT a refusal')"
  else
    bad "C1: the B301 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "C1: go not on PATH — run the B301 tests on the VM"
fi
if grep -q 'NoNewPrivileges=yes' "$APPLY" \
   && grep -q 'install-routes-helper.sh' "$APPLY" \
   && grep -q 'operator' "$APPLY" && grep -q 'sudoers' "$APPLY"; then
  ok "D1: the fallback hint warns that a NOPASSWD rule cannot work on a systemd install"
else
  bad "D1: the hint still offers only the sudoers route, which the kernel flag makes impossible"
fi
if git ls-files --error-unmatch scripts/check_b301_sudo_nnp_rung.sh >/dev/null 2>&1; then
  ok "D2: scripts/check_b301_sudo_nnp_rung.sh is tracked by git"
else
  bad "D2: scripts/check_b301_sudo_nnp_rung.sh is NOT tracked"
fi

printf '\n\033[1mB301 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
