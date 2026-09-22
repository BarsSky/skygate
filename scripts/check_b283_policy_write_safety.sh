#!/usr/bin/env bash
# check_b283_policy_write_safety.sh
#
# 2026-09-22 (B283) — "a policy write must not be able to take the control
# plane down".
#
# LIVE OUTAGE (native host `aro`, headscale `policy.mode: file`). skygate wrote
# a policy file headscale could not load, and headscale crash-looped 248 times:
# the whole tailnet lost its control plane and every device disappeared from the
# portal. Two independent defects produced it:
#
#   1. THE HANDOFF RACE. RequestPolicyApply rewrote `policy.request.props` in
#      place (os.WriteFile = truncate + write) while the root applier read the
#      same file line by line. A second request landing mid-read made the reader
#      stitch the head of one document onto the tail of another: the 7869-byte
#      file on disk had exactly the size of the saved snapshot (valid JSON in
#      the database) and was unparseable on disk —
#      `hujson: line 302, column 23: invalid character ']' after object name`.
#
#   2. THE SILENT BAD WRITE + THE INVISIBLE CRASH-LOOP. Nothing validated the
#      document before writing it, and the applier treated `systemctl restart`
#      (which returns 0 as soon as the unit is STARTED) as success — it logged
#      `done` while headscale was already dying on the file it had just written,
#      and the rollback to `policy.prev` never fired.
#
# Plus two amplifiers found in the same audit:
#
#   3. THE UNDECLARED `via` TAG. The generated policy pinned per-CIDR grants to
#      the B275 assignment table's owner tag while that tag was absent from
#      `tagOwners` (the owner-tag set was never merged into `distinctVias`).
#      headscale refuses such a document as a whole — and on a file-mode host
#      that means it will not start at all.
#
#   4. THE 5-MINUTE REWRITE LOOP. The same policy was rewritten every ~5 minutes
#      with 7100/7109/7126/7135-byte variants, each write restarting headscale —
#      every extra write another chance to hit (1).
#
# CONTRACTS
#   A. the handoff is validated and atomic (temp + rename)
#   B. SetPolicy refuses unparseable policies and undeclared tag references
#   C. the generator declares every tag its grants reference
#   D. an unchanged policy is not rewritten (no needless headscale restart)
#   E. the applier validates before writing and verifies headscale came up
#   F. the B283 regression tests exist and pass
#   G. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B283: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

HELPER=internal/headscale/policy_helper.go
ACL=internal/headscale/acl.go
VALIDATE=internal/headscale/policy_validate.go
GEN=internal/acl/acl.go
APPLIER=deploy/skygate-apply-policy.sh

hdr "B283 — a policy write must not take the control plane down"

# --- A: the handoff is validated and atomic ---------------------------------
if grep -q 'os.CreateTemp(dir, "policy.request' "$HELPER" && grep -q 'os.Rename(tmpName, req)' "$HELPER"; then
  ok "A1: RequestPolicyApply hands the request over by temp + rename (a reader can never see a splice)"
else
  bad "A1: RequestPolicyApply does not rename — an in-place rewrite can splice two documents together"
fi
if grep -q 'os.WriteFile(req, \[\]byte(body)' "$HELPER"; then
  bad "A2: the in-place os.WriteFile on the request path is back (that is the live splice)"
else
  ok "A2: no in-place write to the watched request path"
fi
if grep -q 'PolicyJSON(policy)' "$HELPER"; then
  ok "A3: the handoff validates the document before writing anything"
else
  bad "A3: the handoff does not validate the policy"
fi

# --- B: SetPolicy refuses what headscale cannot load ------------------------
if [ -f "$VALIDATE" ] && grep -q '^func UndeclaredTags(' "$VALIDATE"; then
  ok "B1: headscale.UndeclaredTags exists (grants vs tagOwners)"
else
  bad "B1: $VALIDATE / UndeclaredTags is missing — an undeclared via tag stays a dead daemon"
fi
if grep -q 'UndeclaredTags(policy)' "$ACL"; then
  ok "B2: SetPolicy refuses a policy whose grants reference undeclared tags"
else
  bad "B2: SetPolicy does not check tag references"
fi
if grep -q 'refusing to set a policy that does not parse' "$ACL"; then
  ok "B3: SetPolicy refuses an unparseable policy by name"
else
  bad "B3: SetPolicy does not validate the document it is about to write"
fi

# --- C: the generator declares what it references ---------------------------
if grep -q 'for _, tag := range ownerTagByPrefix' "$GEN" && grep -q 'distinctVias\[tag\] = true' "$GEN"; then
  ok "C1: the B275 owner tags are merged into the tagOwners set (the live undeclared via)"
else
  bad "C1: the assignment table's owner tags are not declared — headscale will refuse the document"
fi
if grep -q 'TEMP-B283' "$GEN" 2>/dev/null; then
  bad "C2: a temporary revert marker is left in $GEN"
else
  ok "C2: no temporary markers in the generator"
fi

# --- D: no needless rewrite / restart ---------------------------------------
if grep -q 'PolicyEquivalent(string(prev), policy)' "$ACL"; then
  ok "D1: a file-mode write is skipped when the document already says the same thing"
else
  bad "D1: the file-mode path rewrites (and restarts headscale for) an unchanged document"
fi
if grep -q 'policy unchanged (semantically equal)' "$APPLIER"; then
  ok "D2: the applier skips an unchanged document instead of restarting headscale"
else
  bad "D2: the applier restarts headscale even when nothing changed"
fi

# --- E: the applier validates and verifies ----------------------------------
if grep -q 'json.loads(os.environ\["POLICY_CHECK"\])' "$APPLIER"; then
  ok "E1: the applier refuses to write a policy that does not parse"
else
  bad "E1: the applier writes whatever it is handed"
fi
if grep -q 'did not answer on' "$APPLIER" && grep -q 'previous policy was restored' "$APPLIER"; then
  ok "E2: the applier verifies headscale came up and rolls back when it did not"
else
  bad "E2: the applier still treats 'systemctl restart' as proof the daemon is alive"
fi

# --- F: the regression tests exist and pass ---------------------------------
for tf in internal/acl/acl_b283_test.go internal/headscale/policy_validate_b283_test.go internal/headscale/policy_helper_b283_test.go; do
  if [ -f "$tf" ]; then
    ok "F1: $tf exists"
  else
    bad "F1: $tf is missing"
  fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/acl/ -run 'B283' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F2: the B283 generator tests pass (a via pin from the assignment table is declared)"
  else
    bad "F2: the B283 generator tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/headscale/ -run 'B283' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F3: the B283 handoff/validation tests pass"
  else
    bad "F3: the B283 handoff/validation tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "F2-F3: go not on PATH — run the B283 tests on the VM"
fi

# --- G: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b283_policy_write_safety.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b283_policy_write_safety.sh is tracked by git"
else
  bad "G1: scripts/check_b283_policy_write_safety.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB283 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
