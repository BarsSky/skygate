#!/usr/bin/env bash
# check_b434_scheduled_context.sh — B434 (2026-10-28)
#
# «НЕ ОТРАБОТАЛО ПЛАНОВОЕ ОБНОВЛЕНИЕ» — the operator's Telegram pair, 08:00 MSK:
#
#   ⏰ Плановое обновление skygate начинается: v1.5.181+6aea3673 → v1.5.183 (job 150c20bc)
#   ❌ Плановое обновление skygate v1.5.181+6aea3673 → v1.5.183 НЕ УДАЛОСЬ:
#      backup tag: context canceled
#
# runScheduled built a 10-minute context, deferred its cancel(), and THEN spawned the
# goroutine that uses it. `defer` runs when runScheduled returns — one instruction after the
# spawn — so the upgrader began with a DEAD context, and the first cancellable call is
# `git tag -f skygate-pre-update-<sha>`: «backup tag: context canceled». The manual
# «Обновить» path worked on the same tree because its cancel() has always been deferred
# INSIDE its goroutine.
#
# WHY NO TEST CAUGHT IT: B375's tests assert that a job was CREATED (last_run written, a
# state row carrying the offered target). A job that fails instantly satisfies both. The
# failing assertion has to be about the CONTEXT the goroutine was handed, which is why
# SchedulerDeps gained NewUpgrader — a seam that exists to be observed, not to be flexible.
#
# CONTRACTS (12)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SCHED=internal/update/scheduler.go
TESTF=internal/update/scheduler_b434_test.go
GATE=scripts/verify_pre_deploy.sh
for f in "$SCHED" "$TESTF" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# The ordering predicate: within runScheduled, the first `defer cancel()` must come AFTER
# the `go func() {` that spawns the run.
ordering_verdict() {
  awk '
    /^func runScheduled\(/ { f = 1 }
    f && /go func\(\)/ { spawn = NR }
    f && /defer cancel\(\)/ { if (!cancel) cancel = NR }
    f && /^}/ { if (f) { f = 0; done = 1 } }
    done { if (!printed) { printf "%s\t%s\n", spawn, cancel; printed = 1; done = 0 } }
  ' "$1"
}

hdr "A. the context outlives the function that created it"
V="$(ordering_verdict "$SCHED")"
SPAWN="${V%%	*}"; CANCEL="${V##*	}"
if [ -z "${SPAWN:-}" ] || [ -z "${CANCEL:-}" ]; then
  bad "A1: could not find both the goroutine spawn and the deferred cancel in runScheduled (spawn='${SPAWN:-}', cancel='${CANCEL:-}')"
else
  if [ "$CANCEL" -gt "$SPAWN" ]; then
    ok "A1: runScheduled defers cancel() INSIDE the goroutine (line $CANCEL after the spawn on line $SPAWN)"
  else
    bad "A1: runScheduled defers cancel() BEFORE its goroutine (cancel line $CANCEL, spawn line $SPAWN) — the upgrader starts with a dead context and every scheduled update fails on its first cancellable step"
  fi
fi
# The detector proof: the same predicate must flag a snippet with the buggy ordering, or a
# future rewrite could satisfy A1 by accident.
TMPD="$(mktemp -d "${TMPDIR:-/tmp}/b434.XXXXXX")" || exit 2
trap 'rm -rf "$TMPD"' EXIT
cat > "$TMPD/buggy.go" <<'EOF'
func runScheduled(ctx context.Context, deps SchedulerDeps, target string, t time.Time) {
	ctxRun, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	go func() {
		u.Run(ctxRun, target)
	}()
}
EOF
V="$(ordering_verdict "$TMPD/buggy.go")"
S2="${V%%	*}"; C2="${V##*	}"
if [ -n "${S2:-}" ] && [ -n "${C2:-}" ] && [ "$C2" -lt "$S2" ]; then
  ok "A2: the ordering detector flags a planted pre-spawn cancel()"
else
  bad "A2: the detector did NOT flag a snippet with the buggy ordering — A1 cannot be trusted"
fi
if grep -q 'defer cancel()' "$SCHED"; then
  ok "A3: the deferred cancel still EXISTS (removing it would leak the 10-minute timer, not fix the bug)"
else
  bad "A3: the deferred cancel is gone — the context is now never released"
fi

hdr "B. the seam and the failing assertion"
if grep -q 'NewUpgrader func(repoPath string, st \*StateStore, current string) ScheduledUpgrader' "$SCHED" &&
   grep -q 'type ScheduledUpgrader interface' "$SCHED"; then
  ok "B1: SchedulerDeps.NewUpgrader lets a test observe the context the goroutine is handed"
else
  bad "B1: no injectable upgrader — the context bug can only be found live again"
fi
if grep -q 'deps.newUpgrader(deps.RepoPath, current)' "$SCHED"; then
  ok "B2: runScheduled goes through newUpgrader (nil = the real Docker upgrader)"
else
  bad "B2: runScheduled still constructs the Docker upgrader directly, bypassing the seam"
fi
if grep -q 'func TestB434_ScheduledRunHandsALiveContextToTheUpgrader' "$TESTF"; then
  ok "B3: the regression test exists"
else
  bad "B3: the regression test is missing"
fi
if grep -q 'ctxErr != nil' "$TESTF"; then
  ok "B4: the test asserts on the CONTEXT's state, not on the job merely existing"
else
  bad "B4: the test does not assert the context state — that is exactly the blind spot B375 had"
fi
if OUT="$(go test ./internal/update/ -run B434 -count=1 2>&1)"; then
  ok "B5: go test -run B434 passes"
else
  bad "B5: go test -run B434 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi
# The other half of the live lesson: the manual apply path must keep its cancel INSIDE the
# goroutine (it always has). Assert it here so a "cleanup" cannot import the bug there.
# The function ALSO has a request-scoped ctx (WithTimeout(r.Context(), 8s)) whose cancel is
# legitimately deferred at function scope, so the predicate has to track the cancel that
# belongs to the 10-minute background context the goroutine uses — comparing against the
# first `defer cancel()` in the function would read that other one (measured while writing
# this contract: it did, and reported a false failure).
if awk '
  /^func \(s \*Service\) PostAdminUpdateApply\(/ { f = 1 }
  f && /context\.WithTimeout\(context\.Background\(\), 10\*time\.Minute\)/ { ctxline = NR }
  f && ctxline && /go func\(\)/ { spawn = NR }
  f && ctxline && NR > ctxline && /defer cancel\(\)/ { if (!cancel) cancel = NR }
  f && /^}/ { f = 0 }
  END { if (spawn && cancel && cancel > spawn) exit 0; exit 1 }
' internal/feature/admin/update.go; then
  ok "B6: the manual «Обновить» path still defers cancel() inside its goroutine"
else
  bad "B6: the manual apply path no longer defers cancel() inside its goroutine"
fi

hdr "C. registration"
if grep -q 'run_check "B434"' "$GATE"; then
  ok "C1: B434 is registered in the catalog"
else
  bad "C1: B434 is not registered — this contract would never run"
fi
if grep -q '\*\*B434\*\*' AGENTS.md; then
  ok "C2: AGENTS.md block index carries B434"
else
  bad "C2: AGENTS.md has no B434 entry"
fi
if git ls-files --error-unmatch scripts/check_b434_scheduled_context.sh >/dev/null 2>&1; then
  ok "C3: this script is tracked by git (trap 11)"
else
  bad "C3: this script is not tracked by git"
fi

printf '\n\033[1mB434 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
