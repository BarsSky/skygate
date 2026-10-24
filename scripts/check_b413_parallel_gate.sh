#!/usr/bin/env bash
# check_b413_parallel_gate.sh
#
# 2026-10-23 (B413) — the pre-deploy catalog must be parallelisable WITHOUT
# becoming skippable.
#
# OPERATOR REQUEST (verbatim):
#
#   «очень долго осуществляется прогон гейт есть ли способ распараллелить
#    выполнение его и тем самым ускорить?»
#
# MEASURED before this block: the reference Windows workstation spends ~50 minutes
# on 459 checks; the SAME catalog on GitHub's Linux runner finishes in ~24. The
# catalog was sequential because `run_check` executed every contract inline, and
# most of the wall clock is hundreds of independent `go test` invocations that share
# a build cache.
#
# CONTRACTS
#   A  the library exists, is tracked, is syntax-valid and is sourced by the catalog
#   B  DEFAULT IS SEQUENTIAL — CI and every documented procedure keep the old
#      behaviour; the mode is opt-in and clamped
#   C  the classification is conservative: read-only Go/text/git commands may run
#      concurrently, everything else (docker/psql/ssh/contract scripts/`go build
#      ./cmd`) stays serial, and `!`-negated reads are still reads
#   D  BEHAVIOURAL: the pool really overlaps work, prints rows in registry order,
#      and folds PASS/FAIL/TIMEOUT/SKIP-row results into the SAME counters the
#      verdict reads (a parallel run must not be able to report differently)
#   E  the catalog wires it: enqueue in run_check, one gate_exec/gate_report
#      implementation, drain BEFORE the summary, cleanup trap
#   F  registered in the catalog, in the AGENTS block index, documented, and the
#      timings surface exists (the data that justifies the mode)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE="scripts/verify_pre_deploy.sh"
LIB="scripts/lib/gate_parallel.sh"
DOC="docs/operations.md"

hdr "B413 — the gate must be parallelisable without becoming skippable"

# --- A: the library -----------------------------------------------------------
if [ -f "$LIB" ]; then
  ok "A1: $LIB exists"
else
  bad "A1: $LIB is missing — the catalog has no runner to opt into"
fi
if git ls-files --error-unmatch "$LIB" >/dev/null 2>&1; then
  ok "A2: the library is TRACKED by git (AGENTS trap #11)"
else
  bad "A2: $LIB is not tracked by git — it would be absent from the pushed tag"
fi
if bash -n "$LIB" 2>/dev/null; then
  ok "A3: the library is valid bash"
else
  bad "A3: the library does not parse"
fi
if grep -qF 'scripts/lib/gate_parallel.sh' "$GATE"; then
  ok "A4: the catalog sources the library"
else
  bad "A4: the catalog does not source the library — the runner is dead code"
fi
for fn in gate_jobs_count gate_jobs_mode gate_cmd_is_parallel_safe gate_exec gate_report \
          gate_enqueue gate_drain gate_worker gate_now_ms gate_record_timing gate_slowest; do
  if grep -qE "^${fn}\(\)" "$LIB"; then
    ok "A5: $fn is defined"
  else
    bad "A5: $fn is missing from the library"
  fi
done

# --- B: the default is sequential --------------------------------------------
env -u SKYGATE_GATE_JOBS bash -c ". $LIB; gate_jobs_mode" >/dev/null 2>&1
if [ "$?" -ne 0 ]; then
  ok "B1: with SKYGATE_GATE_JOBS unset the runner is OFF (the historical behaviour CI keeps)"
else
  bad "B1: the runner is ON by default — CI and every documented procedure would change behaviour silently"
fi
out="$(SKYGATE_GATE_JOBS=6 bash -c ". $LIB; gate_jobs_mode && echo on")"
if [ "$out" = "on" ]; then
  ok "B2: SKYGATE_GATE_JOBS=6 turns it on"
else
  bad "B2: SKYGATE_GATE_JOBS=6 did not activate the runner"
fi
out="$(SKYGATE_GATE_JOBS=999 bash -c ". $LIB; echo \$GATE_JOBS")"
if [ "$out" = "16" ]; then
  ok "B3: the worker count is clamped (999 → 16) — an unbounded pool would fork-bomb the host"
else
  bad "B3: the worker count is not clamped (got '$out')"
fi
out="$(SKYGATE_GATE_JOBS=notanumber bash -c ". $LIB; echo \$GATE_JOBS")"
if [ "$out" = "1" ]; then
  ok "B4: garbage in the knob falls back to sequential"
else
  bad "B4: a non-numeric knob produced '$out'"
fi

# --- C: classification --------------------------------------------------------
classify() { # $1 = command → prints SAFE or SERIAL
  bash -c ". $LIB; if gate_cmd_is_parallel_safe \"\$1\"; then echo SAFE; else echo SERIAL; fi" _ "$1"
}
expect_safe() {
  local got; got="$(classify "$1")"
  if [ "$got" = "SAFE" ]; then ok "C: '$1' may run in the pool"; else bad "C: '$1' was classified $got, expected SAFE"; fi
}
expect_serial() {
  local got; got="$(classify "$1")"
  if [ "$got" = "SERIAL" ]; then ok "C: '$1' stays serial (correct)"; else bad "C: '$1' was classified $got — it must NOT run concurrently"; fi
}
expect_safe "go test ./... 2>&1"
expect_safe "go vet ./... 2>&1"
expect_safe "go build ./... 2>&1"
expect_safe "grep -qF foo bar"
expect_safe "! git ls-files | grep -E x"
expect_safe "test -f scripts/verify_pre_deploy.sh"
expect_serial "go build ./cmd/skygate 2>&1"
expect_serial "bash scripts/check_b1.sh"
expect_serial "bash -n scripts/x.sh"
expect_serial "docker ps"
expect_serial "psql -c 'select 1'"
expect_serial "git pull"
expect_serial "systemctl restart skygate"

# --- D: behaviour of the pool -------------------------------------------------
hdr "D. behavioural — the pool overlaps, keeps order and counts correctly"

TMPD="$(mktemp -d "${TMPDIR:-/tmp}/b413.XXXXXX")" || exit 2
trap 'rm -rf "$TMPD"' EXIT
cat >"$TMPD/pool.sh" <<'POOL'
#!/usr/bin/env bash
# Exercise the REAL library functions: enqueue five jobs directly (classification
# is covered by section C), drain, and report what the counters say.
set -u
# The worker count is read by the library AT SOURCE TIME, so it must be exported
# before sourcing — setting GATE_JOBS afterwards would leave a one-worker pool.
export SKYGATE_GATE_JOBS=3
. scripts/lib/gate_parallel.sh
RED=""; GRN=""; NC=""; YLW=""
RESULTS_PASS=0; RESULTS_FAIL=0; RESULTS_TIMEOUT=0; RESULTS_SKIP=0
RESULTS_SKIP_ROWS=0; RESULTS_FAILNAMES=""
trap 'gate_cleanup' EXIT
# Each slow job records its own start/end into a shared timeline. The overlap
# verdict must come from the JOBS' own wall time, not from the harness's: Git Bash
# forks cost ~50-80 ms each, so drain's per-row bookkeeping alone can hide a real
# overlap (measured: 5 jobs overlapped for 1.1 s while the harness spent ~3 s).
# The jobs run in a fresh `bash -c`, so they use ${EPOCHREALTIME} directly (a helper
# function would not be defined there) and the path comes from the environment.
export POOL_TIMELINE="${POOL_TIMELINE:?}"
gate_enqueue "J1" "first (slow)"   "j1" "echo \"S J1 \${EPOCHREALTIME}\" >>\"\$POOL_TIMELINE\"; sleep 0.5; echo \"E J1 \${EPOCHREALTIME}\" >>\"\$POOL_TIMELINE\"; echo 'SKIP nothing live to inspect'"
gate_enqueue "J2" "second (slow)"  "j2" "echo \"S J2 \${EPOCHREALTIME}\" >>\"\$POOL_TIMELINE\"; sleep 0.5; echo \"E J2 \${EPOCHREALTIME}\" >>\"\$POOL_TIMELINE\""
gate_enqueue "J3" "third (fails)"  "j3" "echo 'contract FAIL row'; exit 1"
gate_enqueue "J4" "fourth (slow)"  "j4" "echo \"S J4 \${EPOCHREALTIME}\" >>\"\$POOL_TIMELINE\"; sleep 0.5; echo \"E J4 \${EPOCHREALTIME}\" >>\"\$POOL_TIMELINE\""
gate_enqueue "J5" "fifth (killed)" "j5" "exit 124"
gate_drain
echo "COUNTS pass=$RESULTS_PASS fail=$RESULTS_FAIL timeout=$RESULTS_TIMEOUT skip_rows=$RESULTS_SKIP_ROWS"
POOL
POOLOUT="$(POOL_TIMELINE="$TMPD/timeline" bash "$TMPD/pool.sh" 2>&1)"
printf '%s\n' "$POOLOUT" | sed 's/^/        /'

case "$POOLOUT" in
  *"COUNTS pass=3 fail=1 timeout=1 skip_rows=1"*)
    ok "D1: PASS/FAIL/TIMEOUT/SKIP-row are counted exactly as a serial run would count them" ;;
  *)
    bad "D1: the counters disagree with the serial semantics (a parallel run could report differently)" ;;
esac
# The ROW ORDER the operator reads: extract the names from the printed rows.
ORDER="$(printf '%s\n' "$POOLOUT" | sed -nE 's/^[[:space:]]*(PASS|FAIL|TIMEOUT)[[:space:]]+(J[0-9]).*/\2/p' | tr '\n' ' ')"
case "$ORDER" in
  "J1 J2 J3 J4 J5 "*) ok "D2: rows are printed in REGISTRY order regardless of completion order ($ORDER)" ;;
  *) bad "D2: rows are not in registry order (got: '$ORDER')" ;;
esac
# Overlap, measured on the jobs' own clocks: the maximum number of slow jobs that
# were running at the same time must be > 1 (with 3 workers and three 0.5 s jobs it
# is 2-3). A serial runner would report 1 no matter how long it took.
OVERLAP=0
if [ -f "$TMPD/timeline" ]; then
  OVERLAP="$(awk '
    { ev[NR] = $1; id[NR] = $2; t[NR] = $3 + 0 }
    END {
      max = 0
      for (i = 1; i <= NR; i++) {
        if (ev[i] != "S") continue
        n = 0
        for (j = 1; j <= NR; j++) {
          if (id[j] != id[i]) continue
          if (ev[j] == "S" && t[j] <= t[i]) n++
        }
        # count how many other jobs were inside this one job at its start
        inside = 1
        for (j = 1; j <= NR; j++) {
          if (id[j] == id[i] || ev[j] != "S") continue
          # find the matching end for job id[j]
          endt = -1
          for (k = 1; k <= NR; k++) if (id[k] == id[j] && ev[k] == "E") endt = t[k]
          if (endt >= t[i]) inside++
        }
        if (inside > max) max = inside
      }
      printf "%d", max
    }' "$TMPD/timeline" 2>/dev/null)"
fi
case "${OVERLAP:-0}" in
  ''|0|1) bad "D3: no job ever overlapped another (max concurrency ${OVERLAP:-?}) — the pool would serialise the catalog" ;;
  *)      ok "D3: jobs really overlap (max concurrency ${OVERLAP} of 3 workers, measured on the jobs' own clock)" ;;
esac
# The verdict must see the pool's failure: a run whose only FAIL came from the pool
# still has RESULTS_FAIL > 0, so the summary (and the exit status) stays red.
case "$POOLOUT" in
  *"fail=1"*) ok "D4: a failing queued check reaches the counters the verdict reads" ;;
  *) bad "D4: a failing queued check never reached the counters — the gate would go green while a check failed" ;;
esac

# --- E: wiring in the catalog -------------------------------------------------
if grep -qF 'gate_enqueue "$name" "$desc" "$label" "$cmd"' "$GATE"; then
  ok "E1: run_check enqueues a parallel-safe check instead of running it"
else
  bad "E1: run_check never enqueues — the library would be unused"
fi
if grep -qF 'gate_exec "$cmd" "$@"' "$GATE" && grep -qF 'gate_report "$name" "$desc" "$label" "$rc" "$out"' "$GATE"; then
  ok "E2: the serial path uses the SAME gate_exec/gate_report implementation as the pool"
else
  bad "E2: the serial path does not share the implementation — two report paths would drift"
fi
# Order matters: the drain must happen BEFORE the summary prints, or the counters
# would be printed before the pool's results reach them.
DRAIN_LINE=$(grep -n '^gate_drain$' "$GATE" | head -n 1 | cut -d: -f1)
SUM_LINE=$(grep -n '=== summary ===' "$GATE" | head -n 1 | cut -d: -f1)
if [ -n "$DRAIN_LINE" ] && [ -n "$SUM_LINE" ] && [ "$DRAIN_LINE" -lt "$SUM_LINE" ]; then
  ok "E3: gate_drain runs BEFORE the summary (line $DRAIN_LINE < $SUM_LINE)"
else
  bad "E3: the drain is missing or runs after the summary (drain=$DRAIN_LINE summary=$SUM_LINE) — the counters would be stale"
fi
if grep -qF "trap 'gate_cleanup' EXIT" "$GATE"; then
  ok "E4: the queue/timing scratch dirs are removed on exit"
else
  bad "E4: no cleanup trap — a run would leave /tmp scratch directories behind"
fi
if grep -qF 'gate_timing_total_ms' "$GATE" && grep -qF 'gate_slowest 8' "$GATE"; then
  ok "E5: the summary reports wall clock and the slowest checks (the data that justifies the mode)"
else
  bad "E5: the summary has no timing surface — nobody could tell whether the knob helped"
fi

# --- F: registered, indexed, documented --------------------------------------
if grep -q 'run_check "B413"' "$GATE"; then
  ok "F1: B413 is registered in the catalog"
else
  bad "F1: B413 is not registered — this contract would never run in the gate"
fi
if grep -q '\*\*B413\*\*' AGENTS.md; then
  ok "F2: AGENTS.md block index carries B413 (rule 2)"
else
  bad "F2: AGENTS.md has no B413 entry"
fi
if grep -q 'SKYGATE_GATE_JOBS' "$DOC" && grep -q 'scripts/lib/gate_parallel.sh' "$DOC"; then
  ok "F3: $DOC documents the knob and points at the library"
else
  bad "F3: $DOC does not document SKYGATE_GATE_JOBS — the operator would not find the speed-up"
fi
if grep -q 'SKYGATE_GATE_JOBS' "$GATE"; then
  ok "F4: the catalog itself names the knob (so 'how do I speed this up' is answered in the file)"
else
  bad "F4: the catalog never mentions SKYGATE_GATE_JOBS"
fi

printf '\n\033[1mB413 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
