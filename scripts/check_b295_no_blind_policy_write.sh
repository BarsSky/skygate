#!/usr/bin/env bash
# check_b295_no_blind_policy_write.sh
#
# 2026-09-23 (B295) — a failed LIVE READ must not become a WRITE (and a restart).
#
# On `aro` the prefix-assignment card answered «состояние политики неизвестно:
# read live policy: api: Get "http://127.0.0.1:8081/api/v1/policy": dial tcp
# 127.0.0.1:8081: connect: connection refused» while headscale itself was listening
# on exactly that address (pid visible in `ss -ltnp`, `systemctl is-active` =
# active) and the API answered when probed. The refusal was TRANSIENT — and the
# code made it permanent:
#
#	acl-drift: cannot read the live policy to decide whether a re-apply is needed
#	(…) — applying unconditionally
#
# On a `policy.mode: file` host a policy write means `systemctl restart headscale`
# (0.29 re-reads the policy file only at startup), so answering a read failure with
# a write is answering a restart with another restart. The observation fed the
# outage; the page could never converge and every prefix stayed «нет маршрута».
#
# CONTRACTS
#   A. a TRANSIENT read failure is retried before any fallback (restart window)
#   B. an HTTP error is NOT retried (the daemon is up — the file/CLI rungs matter)
#   C. when the read still fails, the decision comes from the last APPLIED snapshot
#      in acl_snapshots: equal → no write (and no restart), different → write
#   D. without a usable snapshot the old blind behaviour survives, but says so
#   E. the page reports an in-sync verdict from that snapshot AND names the source
#   F. the regression tests exist and pass; the script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B295: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ACL=internal/headscale/acl.go
SNAP=internal/headscale/policy_snapshot_b295.go
SYNC=internal/feature/exit_rules/sync.go
EXIT=internal/feature/admin/exit_nodes.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
TEST=internal/headscale/policy_snapshot_b295_test.go
ATEST=internal/feature/admin/exit_nodes_b295_test.go

hdr "B295 — a failed read must not become a blind write (and a headscale restart)"

# --- A: retry the restart window ----------------------------------------------
if grep -q '^func isTransientReadError(' "$ACL" && grep -q 'aclReadRetries' "$ACL" && grep -q 'aclReadRetryDelay' "$ACL"; then
  ok "A1: a transient read failure has a bounded retry (attempts + delay are injectable)"
else
  bad "A1: a restart window still fails the read outright"
fi
if grep -q 'for attempt := 1; attempt <= aclReadRetries; attempt++' "$ACL"; then
  ok "A2: the retry loop is bounded"
else
  bad "A2: no bounded retry loop"
fi
if grep -q 'connectex' "$ACL" && grep -q 'actively refused' "$ACL"; then
  ok "A3: the transient test understands both platform spellings"
else
  bad "A3: the transient test is Linux-only (a Windows run would not retry)"
fi

# --- B: never retry an HTTP answer -------------------------------------------
if grep -q 'errors.As(err, &apiErr)' "$ACL" && grep -q 'return false' "$ACL"; then
  ok "B1: an HTTP error (401/404/500) is not retried — the daemon is UP"
else
  bad "B1: the retry cannot tell an HTTP answer from a dead socket"
fi

# --- C: decide from the applied snapshot -------------------------------------
if [ -f "$SNAP" ] && grep -q '^func CompareWithSnapshot(' "$SNAP" && grep -q '^func SnapshotInSyncHint(' "$SNAP"; then
  ok "C1: the snapshot verdict + its operator-facing hint exist"
else
  bad "C1: $SNAP is missing the decision helper"
fi
if grep -q 'PolicyEquivalent(generated, snapshot)' "$SNAP"; then
  ok "C2: the comparison reuses the set-semantics equivalence (B288), not DeepEqual"
else
  bad "C2: the snapshot comparison can report phantom drift"
fi
if grep -q 'last APPLIED snapshot' "$SNAP" && grep -q 'not restarted' "$SNAP"; then
  ok "C3: the in-sync reason says headscale is not restarted"
else
  bad "C3: the in-sync reason does not explain the consequence"
fi
if grep -q '^func (s \*Service) decideWithoutLivePolicy(' "$SYNC" && \
   grep -q 'db.LastAppliedACLVersion(s.dbc())' "$SYNC" && grep -q 'db.GetACLConfig(s.dbc(), version)' "$SYNC"; then
  ok "C4: the sync decides from acl_snapshots when the live read fails"
else
  bad "C4: the sync still writes blind"
fi
if grep -q 'if !verdict.Apply {' "$SYNC"; then
  ok "C5: an in-sync snapshot verdict returns WITHOUT writing"
else
  bad "C5: the verdict is computed but not honoured"
fi
if ! grep -q 'applying unconditionally' "$SYNC"; then
  ok "C6: the pre-B295 'applying unconditionally' branch is gone"
else
  bad "C6: the blind apply is still in place"
fi

# --- D: the blind path stays, and admits it ----------------------------------
if grep -q 'applying blind' "$SNAP"; then
  ok "D1: a fresh install with no snapshot still applies, and says the decision was blind"
else
  bad "D1: the no-snapshot case is silent"
fi
if grep -q 'could not be compared' "$SNAP"; then
  ok "D2: an unparseable snapshot is reported instead of being read as 'in sync'"
else
  bad "D2: an unparseable snapshot could silently skip a needed write"
fi

# --- E: the page names the source --------------------------------------------
if grep -q 'stats.PolicyVia = headscale.SnapshotInSyncHint(version)' "$EXIT" && grep -q 'PolicyVia string' "$EXIT"; then
  ok "E1: the page reports the snapshot-based verdict and its source"
else
  bad "E1: the page cannot say where an in-sync verdict came from"
fi
if grep -q '{{if .PolicyVia}}' "$TMPL"; then
  ok "E2: the template renders the source"
else
  bad "E2: the source is computed but not shown"
fi
if grep -q 'read live policy: ' "$EXIT"; then
  ok "E3: a read failure WITHOUT a usable snapshot is still an error (never silent)"
else
  bad "E3: a real read failure can be swallowed"
fi

# --- F: tests + git -----------------------------------------------------------
for t in "$TEST" "$ATEST"; do
  if [ -f "$t" ]; then ok "F1: $t exists"; else bad "F1: $t is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ ./internal/feature/admin/ -run 'B295' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F2: the B295 tests pass"
  else
    bad "F2: the B295 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "F2: go not on PATH — run the B295 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b295_no_blind_policy_write.sh >/dev/null 2>&1; then
  ok "F3: scripts/check_b295_no_blind_policy_write.sh is tracked by git"
else
  bad "F3: scripts/check_b295_no_blind_policy_write.sh is NOT tracked"
fi

printf '\n\033[1mB295 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
