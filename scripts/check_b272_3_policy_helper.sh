#!/usr/bin/env bash
# check_b272_3_policy_helper.sh
#
# 2026-09-20 (B272.3.1) — the two follow-ups the live `aro` diagnostic forced:
#
#   1. `write_policy_units()` runs only during a FRESH install, so a host
#      installed before v1.5.16 never gets `skygate-policy.path` /
#      `skygate-policy.service`. With ProtectSystem=strict the policy file is
#      read-only for skygate, `ensureTagIsPermitted` can never add the tagOwners
#      entry, headscale keeps answering `400 requested tags [...] are invalid or
#      not permitted`, and no device tag ever lands. The aro box ran 13 h in that
#      state: `skygate-policy.path: inactive/missing`, `tag:dev-* entries: 0`,
#      `tag-reconcile: checked=4 applied=0 failed=3` every five minutes.
#      → deploy/install-policy-helper.sh re-installs the helper idempotently on
#        an existing install and re-arms the path unit.
#
#   2. That same failure was classified `unknown`, so the METRIC said
#      `skygate_tag_autoupdate_failures_total{reason="unknown"} 157` per host
#      while the explanation lived only in the journal — no alert or dashboard
#      named the fix.
#      → FailureReason gains `policy_write_refused`, checked BEFORE the gRPC
#        codes because the wrapped error carries both the 500 body and the
#        write failure.
#
# CONTRACTS
#   A  the installer exists, parses, is idempotent and reuses write_policy_units
#   B  it resolves SKYGATE_UPDATE_DIR from the service (a mismatch is silent)
#   C  the classification exists and is wired before the gRPC codes
#   D  the renegotiated Go contract + the package tests

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || { printf 'B272.3.1: cannot locate cmd/skygate/main.go\n' >&2; exit 2; }

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

INST=deploy/install-policy-helper.sh
ALERT=internal/nodeownership/auto_alert.go
TEST=internal/nodeownership/auto_b272_test.go

hdr "B272.3.1 — the helper must be installable on an existing host, and the refusal must be classified"

if [ -f "$INST" ]; then ok "A.1 $INST exists"; else bad "A.1 $INST missing"; fi
if bash -n "$INST" 2>/dev/null; then ok "A.2 the installer parses"; else bad "A.2 $INST does not parse"; fi
if git ls-files --error-unmatch "$INST" >/dev/null 2>&1; then ok "A.3 git tracks the installer (not silently ignored)"; else bad "A.3 $INST is not tracked by git — check .gitignore"; fi
grep -q 'write_policy_units "\$UPDATE_DIR"' "$INST" && ok "A.4 it reuses write_policy_units (single source of truth for the units)" || bad "A.4 the installer must call write_policy_units"
grep -q 'systemctl enable --now skygate-policy.path' "$INST" && ok "A.5 it arms the path unit" || bad "A.5 the installer must enable+start skygate-policy.path"
grep -q 'systemctl daemon-reload' "$INST" && ok "A.6 it reloads systemd" || bad "A.6 the installer must run systemctl daemon-reload"
if grep -q 'id -u' "$INST" && grep -q 'root' "$INST"; then ok "A.7 it refuses to run unprivileged"; else bad "A.7 add a root check"; fi
grep -q 'systemctl show skygate -p Environment' "$INST" && ok "B.1 it reads SKYGATE_UPDATE_DIR from the running service" || bad "B.1 resolve SKYGATE_UPDATE_DIR from the service — a mismatch is silent"
grep -q 'policy.request.props' "$INST" && ok "B.2 it reports a queued request" || bad "B.2 report a queued policy.request.props"
if grep -qi 'skygate-policy.path: inactive/missing\|before v1.5.16\|ProtectSystem' "$INST"; then ok "B.3 the header explains the failure mode it fixes"; else bad "B.3 explain why the host is broken"; fi

grep -q 'ReasonPolicyWriteRefused FailureReason = "policy_write_refused"' "$ALERT" && ok "C.1 the new reason exists" || bad "C.1 ReasonPolicyWriteRefused missing"
grep -q 'strings.Contains(s, "update is disabled for modes other than")' "$ALERT" && ok "C.2 it matches the file-mode 500 body" || bad "C.2 match the file-mode 500 body"
grep -q 'strings.Contains(s, "read-only file system")\|strings.Contains(s, "write policy file")' "$ALERT" && ok "C.3 it matches the read-only write failure" || bad "C.3 match the write failure"
if [ "$(grep -n 'ReasonPolicyWriteRefused' "$ALERT" | head -1 | cut -d: -f1)" -lt "$(grep -n 'stringContainsACLReject\|Contains(s, "InvalidArgument")' "$ALERT" | head -1 | cut -d: -f1)" ] 2>/dev/null; then
  ok "C.4 the policy check runs BEFORE the gRPC-code checks"
else
  bad "C.4 the policy refusal must be classified before the gRPC codes"
fi
grep -q 'ReasonPolicyWriteRefused' "$TEST" && ok "D.1 the renegotiated Go contract is present" || bad "D.1 update TestB272_ReconcileReportsOwnerFailure"

if command -v go >/dev/null 2>&1; then
  out="$(go test ./internal/nodeownership/ 2>&1)"
  if grep -q '^ok' <<< "$out"; then ok "D.2 nodeownership tests pass"; else bad "D.2 go test failed: $(printf '%s' "$out" | tail -n 4)"; fi
else
  skip "D go toolchain not on PATH"
fi

printf '\n\033[1mB272.3.1: %d PASS, %d FAIL, %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
