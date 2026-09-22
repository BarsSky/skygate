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
#   E  the applier writes the request VERBATIM and records its verdict (B288.1,
#      renegotiated from B272.4's tagOwners union)
#   F  a headscale restart must not cost a whole tick (bounded retry)

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

# --- E: B288.1 — the applier writes the requested document VERBATIM ----------
# RENEGOTIATED for B288.1 (2026-09-22). B272.4 taught the applier to UNION the
# incoming tagOwners with the file on disk, to survive the lost-update race of
# the per-device EnsureTagOwner loop (section F pins the retry that now absorbs
# the restart window). That union made a key the incoming document does NOT
# mention impossible to REMOVE — so a stale declaration (live on `aro`:
# `tag:dev-daniil-homepc`, a tag no node wears) kept the served policy
# permanently different from the generated one: an apply plus a headscale
# restart every five minutes for days, and a drift banner that could never
# clear. Both writers emit COMPLETE documents now (B272.7 batches the tag path
# into one read-modify-write; B288 makes the generator declare every per-device
# tag `node_owner_map` records), so the applier is no longer a second author of
# the policy. E.1/E.2 pin the removal plus the safeguards that stay; E.3 is the
# behavioural half.
APPLIER=deploy/skygate-apply-policy.sh
if grep -q 'POLICY_OLD_FILE' "$APPLIER"; then
  bad "E.1 the tagOwners union is back — a stale declaration can never be removed, so the generated and live policies can never converge"
else
  ok "E.1 the applier writes the requested document verbatim (no tagOwners union)"
fi
if grep -q 'THE UNION IS REMOVED' "$APPLIER" \
   && grep -q 'refusing to write a policy that does not parse' "$APPLIER" \
   && grep -q 'policy unchanged (semantically equal)' "$APPLIER" \
   && grep -q 'policy.prev' "$APPLIER"; then
  ok "E.2 the removal is documented and the safeguards stay (parse check, no-op skip, rollback)"
else
  bad "E.2 the union removal is undocumented, or a safeguard disappeared with it"
fi
PY_OK=0
if command -v python3 >/dev/null 2>&1 && python3 -c 'import json' >/dev/null 2>&1; then PY_OK=1; fi

# --- F: B272.4 — a headscale restart must not cost a whole tick ---------------
AUTO=internal/nodeownership/auto.go
RETRYTEST=internal/nodeownership/auto_b272_4_test.go
grep -q 'func ensureTagIsPermittedRetry(' "$AUTO" && ok "F.1 the bounded retry wrapper exists" || bad "F.1 ensureTagIsPermittedRetry missing"
grep -q 'ensureTagIsPermittedRetry(hs, r, baseDomain, ensured)' "$AUTO" && ok "F.2 the reconciler uses it" || bad "F.2 the reconciler must call the retry variant"
grep -q 'func isTransientHeadscaleDown(err error) bool' "$AUTO" && ok "F.3 the transient classifier exists" || bad "F.3 isTransientHeadscaleDown missing"
grep -q 'connection refused' "$AUTO" && ok "F.4 it matches the refused connection the applier's restart causes" || bad "F.4 match connection refused"
grep -q 'attempt < 3' "$AUTO" && ok "F.5 the retry is bounded (no endless loop)" || bad "F.5 bound the retry"
grep -q 'TestB2724_TransientHeadscaleDownIsRetried' "$RETRYTEST" && ok "F.6 the behavioural retry contract is present" || bad "F.6 add the retry test"
grep -q 'TestB2724_PermissionRefusalIsNotRetried' "$RETRYTEST" && ok "F.7 an operator problem must NOT be retried — pinned" || bad "F.7 pin the no-retry-on-refusal contract"
if [ "$PY_OK" = 1 ]; then
  # The behavioural half: the applier must write EXACTLY the body it was handed,
  # and must contain no merge step (that is what lets a stale tagOwners key
  # disappear and the two documents converge).
  if python3 - "$APPLIER" <<'PY'
import sys
src = open(sys.argv[1], encoding="utf-8").read()
verbatim = '''printf '%s\\n' "$POLICY_BODY" > "$tmp"'''
if verbatim not in src:
    print("the applier does not write the incoming body verbatim")
    sys.exit(1)
for banned in ("merged = dict", "POLICY_OLD_FILE", 'new["tagOwners"] = merged'):
    if banned in src:
        print("merge step still present: " + banned)
        sys.exit(1)
# ... and it must still record its verdict (B288.1), otherwise a write that
# never lands is invisible again.
if "status_write failed" not in src or "policy-apply.status" not in src:
    print("the applier stopped recording its verdict")
    sys.exit(1)
print("ok")
PY
  then
    ok "E.3 behavioural: the write is the incoming document, no merge step, verdict recorded"
  else
    bad "E.3 the applier still merges the on-disk policy into the request"
  fi
else
  skip "E.3 python3 not available — behavioural applier test skipped"
fi

if command -v go >/dev/null 2>&1; then
  out="$(go test ./internal/nodeownership/ 2>&1)"
  if grep -q '^ok' <<< "$out"; then ok "D.2 nodeownership tests pass"; else bad "D.2 go test failed: $(printf '%s' "$out" | tail -n 4)"; fi
else
  skip "D go toolchain not on PATH"
fi

printf '\n\033[1mB272.3.1: %d PASS, %d FAIL, %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
