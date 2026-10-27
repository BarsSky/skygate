#!/usr/bin/env bash
# check_b431_update_check_freshness.sh — B431 (2026-10-27)
#
# «ОПЯТЬ НЕ ВИДИТ НОВОЕ ОБНОВЛЕНИЕ СТРАНИЦА ОБНОВЛЕНИЙ В SKYGATE».
#
# Measured on the operator's live install the same evening: the running build was
# v1.5.178, the process-wide checker had cached that answer at 14:46, v1.5.179 was
# published at 15:51, and /admin/update still answered «последняя: v1.5.178» at 16:10
# and 16:20. GitHub was reachable from the container (rate_limit: 56 of 60 left) and
# the release monitor had announced v1.5.178 normally — the page alone was blind.
#
# Two defects held it up:
#   1. B424 made the checker process-wide (correct: a per-render checker burned the
#      anonymous 60/hour quota) but kept its 6-HOUR success cache, so after any release
#      the page told the truth only after the cache expired — up to six hours of a
#      published release being invisible.
#   2. «Проверить сейчас» — the operator's only remedy — was wired to
#      PostAdminUpdateCheck, which called the CACHED render path. Before B424 that was
#      accidentally a bypass (every render built its own Checker, so no cache survived a
#      request); B424 removed the accident and left a button that could not remedy
#      anything, with a comment claiming it "bypasses the 6h success / 15m failure
#      cache". The click also said nothing and audited nothing, so it was
#      indistinguishable from a dead button.
#
# The fix: success cache 10 minutes (at most 6 calls/hour from a 6-second-refresh tab —
# a tenth of the anonymous limit), a CheckFresh path that bypasses BOTH windows and
# seeds the cache for the render that follows, the mutex the Checker doc had promised
# since the instance became process-wide, a flash that says what the check found, and an
# audit row that proves the button was pressed.
#
# CONTRACTS (16)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

CHECKER=internal/update/checker.go
CHECKER_TEST=internal/update/checker_test.go
FRESH_TEST=internal/update/checker_b431_test.go
PAGE=internal/feature/admin/update.go
CAT=internal/i18n/catalog_update.go
GATE=scripts/verify_pre_deploy.sh
for f in "$CHECKER" "$CHECKER_TEST" "$FRESH_TEST" "$PAGE" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the success window is short enough to tell the truth"
if grep -qE '^const checkSuccessCache = 10 \* time\.Minute' "$CHECKER"; then
  ok "A1: a successful GitHub answer is cached for 10 minutes, not 6 hours"
else
  bad "A1: checkSuccessCache is not 10 minutes — a published release can stay invisible for hours again"
fi
if grep -qF 'a tenth of the 60/hour' "$CHECKER"; then
  ok "A2: the constant records the quota arithmetic that makes 10 minutes safe"
else
  bad "A2: the constant carries no measurement — the next reader cannot tell why 10 minutes and not 6 hours"
fi
if grep -q 'caches a success for 6 h' "$CHECKER" "$PAGE" 2>/dev/null; then
  bad "A3: a comment still promises the 6-hour success cache — the doc lies about the code"
else
  ok "A3: no comment still claims the page caches a success for 6 hours"
fi

hdr "B. the forced path exists and the lock covers it"
if grep -qF 'func (c *Checker) CheckFresh(ctx context.Context) (*Result, error)' "$CHECKER"; then
  ok "B1: Checker.CheckFresh exists"
else
  bad "B1: Checker.CheckFresh is missing — «Проверить сейчас» has nothing forced to call"
fi
if grep -qF 'return c.check(ctx, true)' "$CHECKER" && grep -qF 'return c.check(ctx, false)' "$CHECKER"; then
  ok "B2: Check and CheckFresh share one implementation, differing only in the force flag"
else
  bad "B2: Check/CheckFresh do not both delegate to c.check — the cache guard cannot be reasoned about"
fi
if grep -qF 'if !force {' "$CHECKER"; then
  ok "B3: both cache windows sit behind 'if !force' (a forced check consults neither)"
else
  bad "B3: the cache guards are not gated on 'force' — a forced check may still answer from the cache"
fi
if grep -qF 'mu sync.Mutex' "$CHECKER" && grep -qF 'c.mu.Lock()' "$CHECKER"; then
  ok "B4: the process-wide checker has the mutex its own doc has promised since B424"
else
  bad "B4: no mutex — the process-wide checker is read and written from concurrent renders (a data race)"
fi

hdr "C. the page and the button do different things"
if grep -qF 'updateCheckerFor(s).CheckFresh(ctx)' "$PAGE"; then
  ok "C1: PostAdminUpdateCheck asks GitHub explicitly (CheckFresh)"
else
  bad "C1: the «Проверить сейчас» handler does not call CheckFresh — it renders the cached page and changes nothing"
fi
if grep -qF 'updateCheckerFor(s).Check(ctx)' "$PAGE"; then
  ok "C2: the render path stays CACHED (a re-check per render is the B424 quota bug)"
else
  bad "C2: the render path no longer uses the cached Check — every page load would spend a GitHub call"
fi
if grep -qF '"update_check_now"' "$PAGE"; then
  ok "C3: the click leaves an audit row (a press nobody can see is indistinguishable from a dead button)"
else
  bad "C3: the «Проверить сейчас» click writes no audit row"
fi
if grep -qF 'update.check_now_ok' "$PAGE" && grep -qF 'update.check_now_failed' "$PAGE"; then
  ok "C4: the handler reports the outcome in a flash instead of rendering silently"
else
  bad "C4: the handler does not use the check_now_* flashes — a successful press and a dead button still look the same"
fi

hdr "D. the new strings exist in both catalogues"
missing=""
for k in update.check_now_ok update.check_now_none update.check_now_failed update.check_now_failed_unknown; do
  n=$(grep -cF "\"$k\":" "$CAT" 2>/dev/null || true)
  [ "$n" = "2" ] || missing="$missing $k($n)"
done
if [ -z "$missing" ]; then
  ok "D1: all four check_now keys are defined in RU and EN"
else
  bad "D1: key(s) missing from one catalogue:$missing"
fi

hdr "E. the behaviour is tested, not assumed"
for fn in TestB431_SuccessCacheIsShortEnough TestB431_FreshCheckBypassesTheSuccessCache TestB431_FreshCheckOverridesTheFailureBackoff TestB431_ConcurrentCheckAndFreshAreSafe; do
  if grep -qF "func $fn(" "$FRESH_TEST"; then
    ok "E1: $fn"
  else
    bad "E1: $fn is missing from $FRESH_TEST"
  fi
done
if OUT="$(go test ./internal/update/ -run B431 -count=1 2>&1)"; then
  ok "E2: go test -run B431 passes"
else
  bad "E2: go test -run B431 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi
# The pre-B431 sibling test was vacuous: it redirected through a broken transport and
# then asserted nothing. It must keep asserting the parsed release.
if grep -qF 'result.Latest != "v0.29.0"' "$CHECKER_TEST"; then
  ok "E3: the redirect test asserts the parsed release (it used to log a failure and assert nothing)"
else
  bad "E3: TestChecker_Fetch_Success asserts nothing again — the transport test is vacuous"
fi

hdr "F. registration"
if grep -q 'run_check "B431"' "$GATE"; then
  ok "F1: B431 is registered in the catalog"
else
  bad "F1: B431 is not registered — this contract would never run"
fi
if grep -q '\*\*B431\*\*' AGENTS.md; then
  ok "F2: AGENTS.md block index carries B431"
else
  bad "F2: AGENTS.md has no B431 entry"
fi
if git ls-files --error-unmatch scripts/check_b431_update_check_freshness.sh >/dev/null 2>&1; then
  ok "F3: this script is tracked by git (trap 11)"
else
  bad "F3: this script is not tracked by git"
fi

printf '\n\033[1mB431 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
