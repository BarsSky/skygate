#!/usr/bin/env bash
# check_b382_update_path_truth.sh
#
# 2026-10-13 (B382, v1.5.121) — "the update path must not name things that do not
# exist, and it must not call a crashed unit a broken install".
#
# THREE DEFECTS, ONE THEME: everything here is a message or a path the OPERATOR
# is told to trust, and each one was wrong in a way that costs real time.
#
# RR-2 — the in-app manual steps downloaded files the release never published.
#   `GenerateBareSteps` (internal/update/manual.go) told a bare-install operator to
#   fetch `…/releases/download/<tag>/skygate-linux-amd64` and
#   `…/skygate-linux-amd64.sha256`. Neither has EVER been a release asset: the
#   pipeline publishes `skygate-<tag>-<os>-<arch>.tar.gz` (the `skygate` binary at
#   the archive root) plus ONE `SHA256SUMS`, and that file lists the ARCHIVES — not
#   the binaries inside them. So step 3 of a documented procedure answered 404, and
#   even a corrected `curl` would have failed the documented `sha256sum -c` because
#   the other four archives are not on the operator's disk.
#
# RR-3 — the applier turned our own crash into "your install is broken".
#   `restart_service` ran `systemctl restart` directly. A unit that crashed is
#   LATCHED in the `failed` state and systemd refuses to act on it (AGENTS trap #6,
#   seen live during the native-update rollback drill), so the applier reported only
#   "the service did not come up" and sent the operator to the journal for a product
#   problem that does not exist.
#
# TD-30 (the cheap half) — a hang looked like a failed assertion.
#   B132 killed its `go test` at Go's own implicit 10-minute-per-package default
#   (`601.705s`) and the message named neither a budget nor a test (B378 fixed the
#   MESSAGE). The BUDGET was still implicit: "slow" and "hung" could not be told
#   apart by a number. B132 now passes an explicit `-timeout`, takes it from
#   `SKYGATE_GO_TEST_TIMEOUT` (default 10m), and prints it in BOTH verdicts.
#
# CONTRACTS
#   A. RR-2: the steps name publishable assets, and manual.go cannot drift from the
#      pipeline that produces them (behavioural: the Go test renders the real steps)
#   B. RR-2: the two operator documents stop teaching the dead names
#   C. RR-3: the failed-state latch is cleared BEFORE the restart, only in the
#      systemd branch, only when the unit is really failed, and never fatally
#   D. TD-30: the budget is explicit, overridable and printed in both verdicts
#   E. registered, tracked, documented
#
# NOTE: scratch files live under `mktemp -d` (trap #13), and nothing here touches
# the network — the cross-check against the release pipeline reads the workflow
# that builds the assets.

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B382: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

export LC_ALL=C

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MANUAL=internal/update/manual.go
APPSH=deploy/skygate-apply-update.sh
B132=scripts/check_b132.sh
GTV=scripts/go_test_verdict.sh
RELWF=.github/workflows/release.yml
CATALOG=scripts/verify_pre_deploy.sh
SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b382.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

hdr "B382 (v1.5.121) — RR-2 + RR-3 + the explicit go test budget (TD-30)"

# ---------------------------------------------------------------------------
hdr "A. RR-2 — the manual steps must name assets the release really publishes"
# ---------------------------------------------------------------------------
if [ -f "$MANUAL" ]; then
  ok "A1: $MANUAL exists"
else
  bad "A1: $MANUAL is missing"
fi

if grep -Fq '"/skygate-" + target + "-linux-amd64.tar.gz"' "$MANUAL"; then
  ok "A2: the download URL is built from the tag as skygate-<tag>-linux-amd64.tar.gz"
else
  bad "A2: $MANUAL does not build the published tarball name from the tag"
fi

if grep -Fq '/SHA256SUMS"' "$MANUAL"; then
  ok "A3: the steps download the release's single SHA256SUMS"
else
  bad "A3: $MANUAL does not download SHA256SUMS"
fi

if grep -Fq '| sha256sum -c -' "$MANUAL"; then
  ok "A4: the checksum check is filtered to the ONE archive downloaded (SHA256SUMS lists all five)"
else
  bad "A4: the steps still verify an unfiltered checksum list — four of the five listed archives are not on the operator's disk, so the documented check cannot pass"
fi

# The unpublished name may survive ONLY in a Go comment that explains the defect.
stale_hits="$(grep -n 'skygate-linux-amd64' "$MANUAL" 2>/dev/null || true)"
stale_in_steps="$(printf '%s\n' "$stale_hits" | grep -v ':[[:space:]]*//' | grep -v '^$' || true)"
if [ -z "$stale_in_steps" ]; then
  if [ -n "$stale_hits" ]; then
    ok "A5: the unpublished asset name survives only in the Go comment that explains RR-2, never in a step string"
  else
    ok "A5: the unpublished asset name is gone from $MANUAL entirely"
  fi
else
  bad "A5: the unpublished asset name is still in a copied command: $(printf '%s' "$stale_in_steps" | tr '\n' ' ')"
fi

# The pipeline is the ground truth for the asset shape: if it is ever renamed,
# this contract fails HERE instead of in an operator's terminal.
if grep -q 'skygate-v\${{ steps.meta.outputs.version }}-\${{ matrix.archive_dir }}.\${{ matrix.ext }}' "$RELWF" \
   && grep -q 'archive_dir: linux-amd64' "$RELWF" \
   && grep -q 'ext: tar.gz' "$RELWF"; then
  ok "A6: $RELWF still publishes skygate-v<version>-linux-amd64.tar.gz — the name manual.go builds from the tag"
else
  bad "A6: the release workflow no longer publishes the asset shape manual.go points at (they have drifted apart)"
fi

# Behavioural: render the REAL steps and assert on them. This is the half that
# cannot be satisfied by a comment, because it calls GenerateBareSteps.
GO=""
if command -v go >/dev/null 2>&1; then
  GO="go"
else
  # The candidate list keeps `command -v go` INLINE and FIRST (the B132/B281 G2
  # reference order): a hardcoded /usr/local/go/bin/go consulted before the probe
  # resolves to whatever toolchain the runner image happens to ship — on the CI
  # runner that was Go 1.24.13 while go.mod wanted 1.25.
  for cand in "$(command -v go 2>/dev/null)" "/c/Program Files/Go/bin/go.exe" "/usr/local/go/bin/go" "/snap/bin/go"; do
    if [ -x "${cand}" ]; then GO="${cand}"; break; fi
  done
fi
if [ -z "$GO" ]; then
  skip "A7: 'go' not found — the behavioural half (rendered steps) could not run"
else
  a7_out="$("$GO" test -count=1 -timeout 3m -run 'TestGenerateBareSteps' ./internal/update/ 2>&1)"
  a7_rc=$?
  if [ "$a7_rc" -eq 0 ]; then
    ok "A7: TestGenerateBareSteps passes — the rendered steps carry the published tarball + SHA256SUMS and none of the dead names"
  else
    bad "A7: TestGenerateBareSteps fails (rc=$a7_rc): $(printf '%s' "$a7_out" | tail -4 | tr '\n' ' ')"
  fi
fi

# ---------------------------------------------------------------------------
hdr "B. RR-2 — the operator documents must stop teaching the dead names"
# ---------------------------------------------------------------------------
for pair in "docs/UPDATE.md:B1" "docs/ru/UPDATE.md:B2"; do
  f="${pair%%:*}"; id="${pair##*:}"
  if [ ! -f "$f" ]; then
    bad "$id: $f is missing"
    continue
  fi
  if grep -q 'RR-2' "$f" && grep -q 'v1.5.121' "$f"; then
    ok "$id: $f records the RR-2 fix (v1.5.121)"
  else
    bad "$id: $f still describes the dead asset names as a live gap"
  fi
  if grep -q 'still reference' "$f"; then
    bad "$id: $f still says the in-app lists «still reference» the dead names"
  else
    ok "$id: $f no longer claims the lists point at unpublished assets"
  fi
done

# ---------------------------------------------------------------------------
hdr "C. RR-3 — a latched failed unit must be cleared before the restart"
# ---------------------------------------------------------------------------
if grep -q 'systemctl reset-failed "\$SERVICE"' "$APPSH"; then
  ok "C1: the applier calls systemctl reset-failed on the unit it is about to restart"
else
  bad "C1: the applier never clears the failed-state latch — a crashed unit still refuses the restart (trap #6)"
fi

c_reset="$(grep -n 'systemctl reset-failed "\$SERVICE"' "$APPSH" | head -1 | cut -d: -f1)"
c_restart="$(grep -n 'systemctl restart "\$SERVICE"' "$APPSH" | head -1 | cut -d: -f1)"
c_branch="$(grep -n 'if \[ "\$MODE" = "systemd" \]' "$APPSH" | head -1 | cut -d: -f1)"
if [ -n "$c_reset" ] && [ -n "$c_restart" ] && [ -n "$c_branch" ] \
   && [ "$c_reset" -gt "$c_branch" ] && [ "$c_reset" -lt "$c_restart" ]; then
  ok "C2: it runs inside the systemd branch ($c_branch) and BEFORE the restart ($c_reset < $c_restart)"
else
  bad "C2: wrong order or wrong branch (branch=$c_branch reset=$c_reset restart=$c_restart) — resetting after the restart would fix nothing"
fi

if grep -q 'systemctl is-failed --quiet "\$SERVICE"' "$APPSH"; then
  ok "C3: the reset is asked for only when the unit really is failed, so a healthy unit is untouched and the log line is evidence"
else
  bad "C3: the reset is unconditional — it would also run on every healthy update and its log line would mean nothing"
fi

if grep -q 'reset-failed \$SERVICE returned non-zero; continuing' "$APPSH"; then
  ok "C4: a non-zero reset is a warning, not a fatal error — the restart still gets its chance"
else
  bad "C4: a failing reset-failed would abort the update"
fi

if grep -q 'rc-service "\$SERVICE" start' "$APPSH"; then
  ok "C5: the OpenRC branch keeps its start fallback (no latch there, and B262 behaviour preserved)"
else
  bad "C5: the OpenRC restart path lost its start fallback"
fi

# ---------------------------------------------------------------------------
hdr "D. TD-30 (cheap half) — the go test budget must be an explicit number"
# ---------------------------------------------------------------------------
if grep -q -- '-timeout "\${GO_TEST_TIMEOUT}"' "$B132"; then
  ok "D1: B132 passes an explicit -timeout to go test instead of relying on Go's implicit default"
else
  bad "D1: B132 still runs go test with no explicit budget — 'slow' and 'hung' cannot be told apart by a number"
fi

if grep -q 'SKYGATE_GO_TEST_TIMEOUT' "$B132" && grep -q 'GO_TEST_TIMEOUT:-10m' "$B132"; then
  ok "D2: the budget is overridable without editing a gate script (SKYGATE_GO_TEST_TIMEOUT, default 10m per package)"
else
  bad "D2: the budget is not overridable from the environment"
fi

if [ "$(grep -c 'explicit -timeout \${GO_TEST_TIMEOUT}' "$B132")" -ge 1 ] \
   && [ "$(grep -c 'the budget was -timeout \${GO_TEST_TIMEOUT}' "$B132")" -ge 1 ]; then
  ok "D3: the budget is printed in BOTH verdicts (the pass line and the hang line)"
else
  bad "D3: the budget appears in only one verdict — the operator still has to guess it"
fi

if grep -q 'caller-supplied -timeout' "$GTV"; then
  ok "D4: the hang reader says the budget is the caller's own -timeout, and names the knob"
else
  bad "D4: the hang reader does not say where the budget came from"
fi

# Behavioural: the shape-exact hang dump must still be read as a HANG (B378 owns
# the verdict itself; this asserts the new budget line did not break the reader).
{
  printf 'panic: test timed out after 10m0s\n'
  printf '\trunning tests:\n'
  printf '\t\tTestB2761_MarkGroupKeepsTheIntent (7s)\n\n'
  printf 'goroutine 22 [syscall]:\n'
  printf 'syscall.FlushFileBuffers(0x1c0)\n'
  printf 'modernc.org/sqlite/lib._winSync(0xc00014a180, 0x1ecfe3f05c0)\n'
  printf 'skygate/internal/db.addColumnIfMissingSQLite(0xc000116f70)\n'
  printf 'FAIL\tskygate/internal/feature/exit_rules\t601.705s\n'
} > "$SKY_TMP/hang.txt"
d5_out="$(bash "$GTV" < "$SKY_TMP/hang.txt" 2>&1)"; d5_rc=$?
if [ "$d5_rc" -eq 2 ] \
   && [[ $d5_out == *'caller-supplied -timeout'* ]] \
   && [[ $d5_out == *'TestB2761_MarkGroupKeepsTheIntent'* ]]; then
  ok "D5: the hang dump is still read as HUNG (rc=2), still names the test, and now names the budget's origin"
else
  bad "D5: the reader regressed on the hang shape (rc=$d5_rc): $(printf '%s' "$d5_out" | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------
hdr "E. registered, tracked, documented"
# ---------------------------------------------------------------------------
for f in scripts/check_b382_update_path_truth.sh; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    ok "E1: $f is tracked by git (trap #11)"
  else
    bad "E1: $f is NOT tracked by git"
  fi
done
if grep -q 'check_b382_update_path_truth.sh' "$CATALOG"; then
  ok "E2: registered in $CATALOG"
else
  bad "E2: not registered in $CATALOG (rule 2)"
fi
if grep -q 'B382' RELEASE-NOTES.md 2>/dev/null; then
  ok "E3: RELEASE-NOTES.md carries the B382 entry"
else
  bad "E3: RELEASE-NOTES.md has no B382 entry"
fi
if grep -q 'B382' docs/ROADMAP.md 2>/dev/null; then
  ok "E4: docs/ROADMAP.md records B382"
else
  bad "E4: docs/ROADMAP.md does not mention B382"
fi

# ---------------------------------------------------------------------------
hdr "F. a terminal PHASE is not the end of the WRITER (the CI flake this block hit)"
# ---------------------------------------------------------------------------
# The first CI run of this block failed with
#   --- FAIL: TestB375_YesterdaysRunDoesNotBlockToday (0.62s)
#       testing.go:1369: TempDir RemoveAll cleanup: unlinkat /tmp/…/002: directory not empty
#   FAIL skygate/internal/update
# — a fixture that returns as soon as the state reaches a terminal PHASE, while
# the goroutine runScheduled spawned is still persisting transitions, so
# testing.TempDir's RemoveAll races the writer. It passed on re-run, which is what
# a race looks like; the join below removes the window instead of tolerating it.
SCHED=internal/update/scheduler.go
B375T=internal/update/scheduler_b375_test.go

if grep -q 'scheduledRunWG sync.WaitGroup' "$SCHED"; then
  ok "F1: $SCHED tracks the scheduled-run goroutine in a WaitGroup"
else
  bad "F1: nothing lets a caller wait for the scheduled-run goroutine to RETURN — only for a terminal phase"
fi

f_add="$(grep -n 'scheduledRunWG.Add(1)' "$SCHED" | head -1 | cut -d: -f1)"
f_go="$(grep -n 'go func() {' "$SCHED" | tail -1 | cut -d: -f1)"
f_done="$(grep -c 'defer scheduledRunWG.Done()' "$SCHED")"
if [ -n "$f_add" ] && [ -n "$f_go" ] && [ "$f_add" -lt "$f_go" ] && [ "$f_done" -ge 1 ]; then
  ok "F2: the counter is incremented BEFORE the goroutine starts ($f_add < $f_go) and released by a defer inside it"
else
  bad "F2: the WaitGroup is wired wrong (add=$f_add go=$f_go done=$f_done) — an Add inside the goroutine would race the join"
fi

if grep -q 'b375JoinScheduledRun(t)' "$B375T" && grep -q 'scheduledRunWG.Wait()' "$B375T"; then
  ok "F3: the B375 fixture JOINS the writer before it returns the terminal state"
else
  bad "F3: the fixture still returns at the phase, leaving a writer alive while t.TempDir is removed"
fi

if grep -q 'TempDir RemoveAll cleanup' "$B375T"; then
  ok "F4: the fixture records the measured failure it exists to prevent, so the reason survives a refactor"
else
  bad "F4: the fixture does not record why the join is there"
fi

if [ -z "$GO" ]; then
  skip "F5: 'go' not found — the repeated fixture run could not be executed"
else
  f5_out="$("$GO" test -count=3 -timeout 5m -run 'TestB375_YesterdaysRunDoesNotBlockToday' ./internal/update/ 2>&1)"
  f5_rc=$?
  if [ "$f5_rc" -eq 0 ]; then
    ok "F5: the fixture that failed CI passes three times in a row in the gate itself"
  else
    bad "F5: the failing fixture did not pass 3x (rc=$f5_rc): $(printf '%s' "$f5_out" | tail -4 | tr '\n' ' ')"
  fi
fi

printf '\n\033[1mB382 summary: %d passed, %d failed, %d skipped\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
