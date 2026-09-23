#!/usr/bin/env bash
# check_b294_headscale_read_truth.sh
#
# 2026-09-23 (B294) — the operator's prefix-assignment page on `aro`:
#
#   состояние политики неизвестно: read live policy: api: Get
#   "http://127.0.0.1:8081/api/v1/policy": dial tcp 127.0.0.1:8081: connect:
#   connection refused; cli: all variants failed
#
#   владелец не объявляет: 0   никто не объявляет: 19   объявляет несколько: 0
#
# …with every row «АНОНС: нет · нет маршрута» (19 «проблемных»), and the same page
# on the docker VM "not changing the table" either.
#
# THREE defects behind that one screen:
#
#  A. The live-policy READ had only two rungs — the API and a DOCKER-ONLY
#     `headscale policy get`. On a native install with an unreachable API it gave
#     up at once and answered "cli: all variants failed" about a CLI it never ran,
#     while the policy FILE headscale actually serves in `policy.mode: file` was
#     never read — although the WRITE path has used that file since B272.
#  B. The prefix table's advertisement map is filled from `ListAllNodes()` and the
#     loader DISCARDED the error, so an unreachable headscale rendered every prefix
#     as «нет / нет маршрута»: absence of evidence presented as a negative fact,
#     which no action on the page could change.
#  C. The unreachable API is a CONFIGURATION class (the skygate process must be able
#     to reach it; inside a container `127.0.0.1:<port>` is the container's own
#     loopback, not the host's) and nothing named HEADSCALE_URL as the thing to
#     check.
#
# CONTRACTS
#   A. the read chain is API → policy file → CLI (install-kind ladder) → named error
#   B. the error names every rung and the real blocker, with the HEADSCALE_URL hint
#   C. the page carries the live-read failure and the hint to the template
#   D. the template says «could not ask» instead of «nothing is advertised» (RU+EN)
#   E. the regression tests exist and pass; the script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B294: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ACL=internal/headscale/acl.go
EXIT=internal/feature/admin/exit_nodes.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go
TEST=internal/headscale/acl_read_b294_test.go
ATEST=internal/feature/admin/exit_nodes_b294_test.go

hdr "B294 — reading the live policy must work without docker, and a failed read must not read as «nothing is advertised»"

# --- A: the read chain --------------------------------------------------------
if grep -q '^func (c \*Client) readPolicyFileAsACL(' "$ACL" && grep -q 'DiscoverPolicyPath()' "$ACL"; then
  ok "A1: the policy FILE is a read rung (native file-mode hosts)"
else
  bad "A1: the file headscale serves can still not be read"
fi
if grep -q '^func (c \*Client) readPolicyViaCLIAsACL(' "$ACL" && grep -q 'c.runHeadscaleCLI(args...)' "$ACL"; then
  ok "A2: the CLI rung uses the install-kind ladder (docker exec OR the local binary)"
else
  bad "A2: the CLI rung is still docker-only"
fi
if grep -q 'c.readPolicyFileAsACL()' "$ACL" && grep -q 'c.readPolicyViaCLIAsACL()' "$ACL"; then
  ok "A3: GetACL walks file → CLI after the API fails"
else
  bad "A3: GetACL still gives up after the API"
fi
if ! grep -vE '^[[:space:]]*//' "$ACL" | grep -q 'cli: all variants failed'; then
  ok "A4: the misleading 'cli: all variants failed' wording is gone (comments excluded)"
else
  bad "A4: the error still blames a CLI that was never tried"
fi

# --- B: the error names the blocker -------------------------------------------
if grep -q 'policy file: %s; %s' "$ACL" && grep -q 'headscale CLI: ' "$ACL"; then
  ok "B1: the failure names every rung it tried"
else
  bad "B1: the failure does not say what was tried"
fi
if grep -q '^func aclReadHint(' "$ACL" && grep -q 'HEADSCALE_URL' "$ACL" && grep -q 'never 127.0.0.1' "$ACL"; then
  ok "B2: a connection refusal names HEADSCALE_URL and the container-loopback trap"
else
  bad "B2: an unreachable API is not actionable"
fi
if grep -q 'actively refused' "$ACL" && grep -q 'no connection could be made' "$ACL"; then
  ok "B3: the reachability test understands both platform spellings"
else
  bad "B3: the reachability test is Linux-only"
fi
if grep -q '^func ACLReadHintFor(' "$ACL"; then
  ok "B4: the hint is exported for the page"
else
  bad "B4: the page cannot render the hint"
fi
if grep -q 'headscale GET /api/v1/policy: 401\|StatusCode' "$ACL"; then
  ok "B5: an API error that is NOT a reachability failure gets no network hint"
else
  bad "B5: every API error would be blamed on the network"
fi

# --- C: the page carries the failure ------------------------------------------
if grep -q 'stats.LiveReadErr = herr.Error()' "$EXIT" && grep -q 'LiveReadErr string' "$EXIT"; then
  ok "C1: the loader keeps the ListAllNodes error instead of discarding it"
else
  bad "C1: a failed live read is still silently swallowed"
fi
if grep -q 'stats.LiveReadHint = headscale.ACLReadHintFor(' "$EXIT"; then
  ok "C2: the actionable hint travels with it"
else
  bad "C2: the page has the error but not the fix"
fi
if grep -q 'cannot read the advertised routes from headscale' "$EXIT"; then
  ok "C3: the journal names the cause of an empty АНОНС column"
else
  bad "C3: nothing in the journal explains it"
fi

# --- D: the template reads honestly -------------------------------------------
if grep -q '{{if .LiveReadErr}}' "$TMPL" && grep -q 'exit_nodes.prefix_owner.live_read_failed' "$TMPL"; then
  ok "D1: the prefix card states that headscale could not be read"
else
  bad "D1: the table still shows «нет» as if it were a fact"
fi
if grep -q '{{if .LiveReadHint}}' "$TMPL"; then
  ok "D2: the hint is rendered next to the error"
else
  bad "D2: the page shows the error without the fix"
fi
RU=$(grep -c '"exit_nodes.prefix_owner.live_read_failed"' "$I18N" || true)
EN=$(grep -c '"exit_nodes.prefix_owner.live_read_failed_help"' "$I18N" || true)
if [ "${RU:-0}" -ge 2 ] && [ "${EN:-0}" -ge 2 ]; then
  ok "D3: the new i18n keys exist in RU and EN"
else
  bad "D3: i18n keys missing (banner=${RU:-0}, help=${EN:-0}; want 2 each)"
fi

# --- E: tests + git ------------------------------------------------------------
for t in "$TEST" "$ATEST"; do
  if [ -f "$t" ]; then ok "E1: $t exists"; else bad "E1: $t is missing"; fi
done
if grep -q 'TestGetACLReadsThePolicyFileWhenTheAPIIsDown_B294' "$TEST" 2>/dev/null; then
  ok "E2: the file rung is pinned by a test (the live native case)"
else
  bad "E2: the file rung is untested"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ ./internal/feature/admin/ -run 'B294' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "E3: the B294 tests pass"
  else
    bad "E3: the B294 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "E3: go not on PATH — run the B294 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b294_headscale_read_truth.sh >/dev/null 2>&1; then
  ok "E4: scripts/check_b294_headscale_read_truth.sh is tracked by git"
else
  bad "E4: scripts/check_b294_headscale_read_truth.sh is NOT tracked"
fi

printf '\n\033[1mB294 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
