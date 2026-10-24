#!/usr/bin/env bash
# scripts/lib/gate_parallel.sh — optional parallel runner for the pre-deploy
# guarantee catalog (B413, 2026-10-23).
#
# WHY THIS EXISTS
#
# The operator's question, verbatim:
#
#   «очень долго осуществляется прогон гейт есть ли способ распараллелить
#    выполнение его и тем самым ускорить?»
#
# Measured on the reference Windows workstation: `bash scripts/verify_pre_deploy.sh`
# takes ~50 minutes for 459 checks, while the SAME catalog on GitHub's Linux runner
# finishes in ~24. The catalog is strictly sequential because `run_check` executes
# each contract inline, and most of the wall clock is spent inside `go test` — hundreds
# of independent invocations that share a build cache and therefore do not need to be
# serialised.
#
# WHAT IS PARALLEL AND WHAT IS NOT (the whole safety argument)
#
# Only checks whose command is provably read-only are queued: the Go toolchain
# (`go test`, `go vet`, `go version`, `go list`, `go env`), plain text tools
# (`grep`, `awk`, `sed`, `test -f`), and read-only `git ls-files`/`git grep`/`git log`.
# Everything else — anything naming docker/psql/ssh/systemctl/kubectl/curl/apt,
# anything that builds a BINARY into the tree, and every `bash scripts/check_*.sh`
# contract (a script can do anything) — runs SERIAL, exactly where it runs today,
# with the same ordering relative to earlier checks.
#
# `go build` is deliberately NOT parallel-safe even though it looks like the rest of
# the toolchain: `go build ./cmd/skygate` writes `skygate`/`skygate.exe` into the
# repo root, which is shared state that another check may read.
#
# The verdict is unchanged: a queued check still runs under the same budget
# (`SKYGATE_CHECK_TIMEOUT`), with the same `setsid`/`timeout` isolation, and its
# result is folded into the SAME summary counters before the catalog prints the
# summary block. `push_gate_verdict.sh` and CI keep parsing the same rows (B335,
# B378, TD-22), so an opt-in speed-up cannot become an opt-out from the contract.
#
# USAGE
#   SKYGATE_GATE_JOBS=6 bash scripts/verify_pre_deploy.sh
#
#   default 1 = the historical sequential behaviour (and what CI runs);
#   the value is clamped to 1..16;
#   `SKYGATE_GATE_TIMINGS_OUT=<file>` dumps every check's wall-clock time as TSV.
#
# The runner is intentionally a LIBRARY (sourced by verify_pre_deploy.sh) so a
# contract can test it without running the catalog: see
# scripts/check_b413_parallel_gate.sh.

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

# GATE_JOBS: effective worker count (1 = sequential mode, no queue at all).
gate_jobs_count() {
  local n="${SKYGATE_GATE_JOBS:-1}"
  case "$n" in
    ''|*[!0-9]*) n=1 ;;
  esac
  [ "$n" -lt 1 ] && n=1
  [ "$n" -gt 16 ] && n=16
  printf '%s' "$n"
}

GATE_JOBS="${GATE_JOBS:-$(gate_jobs_count)}"

# True when the parallel runner is active.
gate_jobs_mode() {
  [ "${GATE_JOBS:-1}" -gt 1 ]
}

# ---------------------------------------------------------------------------
# Timing (per check, always recorded — the data is what justifies the mode)
# ---------------------------------------------------------------------------

gate_now_ms() {
  if [ -n "${EPOCHREALTIME:-}" ]; then
    # EPOCHREALTIME is "1750000000.123456"; the fractional part is locale-free.
    local t="${EPOCHREALTIME}"
    local secs="${t%%[.,]*}"
    local frac="${t#*[.,]}"
    frac="${frac}000"
    printf '%s' "$(( secs * 1000 + 10#${frac:0:3} ))"
  else
    printf '%s000' "$(date +%s)"
  fi
}

# gate_timing_init — lazily create the per-run timing directory.
gate_timing_init() {
  [ -n "${GATE_TIMING:-}" ] && return 0
  GATE_TIMING="$(mktemp -d "${TMPDIR:-/tmp}/skygate-gate-timing.XXXXXX" 2>/dev/null)" || {
    GATE_TIMING=""
    return 1
  }
}

# gate_record_timing <name> <ms> — one TSV row per executed check.
gate_record_timing() {
  local name="$1" ms="$2"
  gate_timing_init || return 0
  printf '%s\t%s\n' "$ms" "$name" >>"$GATE_TIMING/rows.tsv" 2>/dev/null || true
}

# gate_slowest <n> — the N slowest checks, as "ms<TAB>name" rows.
gate_slowest() {
  local n="${1:-8}"
  [ -n "${GATE_TIMING:-}" ] && [ -f "$GATE_TIMING/rows.tsv" ] || return 0
  sort -t "$(printf '\t')" -k1,1nr "$GATE_TIMING/rows.tsv" | head -n "$n"
}

# gate_timing_total_ms — the sum of every recorded check (the serial wall-clock
# estimate the parallel run has to beat).
gate_timing_total_ms() {
  [ -n "${GATE_TIMING:-}" ] && [ -f "$GATE_TIMING/rows.tsv" ] || { printf '0'; return 0; }
  awk -F'\t' '{ s += $1 } END { printf "%d", s }' "$GATE_TIMING/rows.tsv"
}

# gate_timing_dump — honour SKYGATE_GATE_TIMINGS_OUT.
gate_timing_dump() {
  local out="${SKYGATE_GATE_TIMINGS_OUT:-}"
  [ -n "$out" ] && [ -n "${GATE_TIMING:-}" ] && [ -f "$GATE_TIMING/rows.tsv" ] || return 0
  cp "$GATE_TIMING/rows.tsv" "$out" 2>/dev/null && echo "  timings: $out (TSV: ms, check)"
}

# ---------------------------------------------------------------------------
# Classification — the ONLY place that decides what may run concurrently
# ---------------------------------------------------------------------------

# gate_cmd_is_parallel_safe <command-string>
#
# Conservative by construction: an unrecognised command is NOT safe. Every rule
# below is a read-only class; the deny list wins over the allow list, so a command
# that both looks like `go test` and names docker stays serial.
gate_cmd_is_parallel_safe() {
  local cmd="$1"
  # A leading `!` is a negation of the command's status, not part of it
  # (`! git ls-files | grep -E …` is a read-only grep).
  local cmd_eff="$cmd"
  case "$cmd_eff" in
    "! "*) cmd_eff="${cmd_eff#! }" ;;
  esac

  # 1. Deny list first: machine state, network, installers, tree writes, and any
  #    contract script (a check_*.sh may do all of the above internally). The
  #    patterns are COMMAND-shaped on purpose: a bare `*deploy*`/`*install*` would
  #    also match `verify_pre_deploy.sh` and `docs/INSTALL.md` in a read-only grep,
  #    turning safe checks serial and making the mode pointless.
  case "$cmd" in
    *docker*|*psql*|*pg_dump*|*pg_restore*|*systemctl*|*rc-service*|*kubectl*|*helm*|\
    *"ssh "*|*scp\ *|*curl*|*wget*|*apt-get*|*apt\ *|*apk\ *|*yum\ *|*dnf\ *|\
    *"install.sh"*|*"install-common.sh"*|*" install "*|*" deploy "*|*"/deploy.sh"*|*"skygate deploy"*|\
    *"make "*|*"update.sh"*|*"skygate update"*|*"cleanup_*.sh"*|*"rotate_"*|\
    *"git checkout"*|*"git reset"*|*"git pull"*|*"git fetch"*|*"git commit"*|*"git tag"*|*"git push"*|*"git add"*|\
    *"bash scripts/"*|*"sh scripts/"*|*" -o "*|*"build ./cmd"*)
      return 1
      ;;
  esac

  # 2. The Go toolchain: independent processes, shared build cache (cache-safe by
  #    design). `build` is excluded unless it is a multi-package build (which writes
  #    no artifact) — the deny list above already removed `build ./cmd`.
  # The catalog quotes the toolchain as '$GO' — a bare `go`, or an absolute path
  # that may contain spaces ('C:/Program Files/Go/bin/go.exe'). Take the first
  # WORD, honouring the quoting, then its basename.
  local first=""
  case "$cmd_eff" in
    \'*|\"*)
      local q="${cmd_eff:0:1}" rest="${cmd_eff:1}"
      first="${rest%%"$q"*}"
      ;;
    *)
      first="${cmd_eff%% *}"
      ;;
  esac
  first="${first##*/}"
  case "$first" in
    go|go.exe)
      case "$cmd_eff" in
        *" test "*|*" vet "*|*" version "*|*" list "*|*" env "*|*" build ./..."*)
          return 0
          ;;
      esac
      return 1
      ;;
  esac

  # 3. Read-only text tools and git reads.
  case "$cmd_eff" in
    grep*|test\ -f*|test\ -d*|test\ -x*|awk*|sed*|sort*|comm*|diff*|\
    "git ls-files"*|"git grep"*|"git log"*|"git show"*|"git diff"*|"git rev-parse"*)
      return 0
      ;;
  esac

  return 1
}

# ---------------------------------------------------------------------------
# Execution (shared by the serial path and the workers)
# ---------------------------------------------------------------------------

# gate_exec <command> [extra bash -c args...]
#
# Sets OUT (combined output) and RC. This is the ONE implementation of the
# isolation the catalog paid for: a per-check budget (SKYGATE_CHECK_TIMEOUT), no
# controlling terminal (B299: a password prompt must fail fast, not stop the
# group), a killable deadline (-k), and stdin from /dev/null (a bare `grep` must
# not block on the caller's pipe).
gate_exec() {
  local cmd="$1"
  shift || true
  local budget="${SKYGATE_CHECK_TIMEOUT:-900}"
  if command -v timeout >/dev/null 2>&1; then
    if command -v setsid >/dev/null 2>&1 && setsid --help >/dev/null 2>&1; then
      OUT=$(setsid --wait timeout -k 10 "$budget" bash -c "$cmd" "$@" </dev/null 2>&1)
      RC=$?
    else
      OUT=$(timeout -k 10 "$budget" bash -c "$cmd" "$@" </dev/null 2>&1)
      RC=$?
    fi
  else
    OUT=$(bash -c "$cmd" "$@" </dev/null 2>&1)
    RC=$?
  fi
}

# gate_report <name> <desc> <label> <rc> <out> <ms> <budget>
#
# Prints the row and folds the result into the counters. ONE implementation for
# both modes, so a parallel run cannot report differently from a serial one.
gate_report() {
  local name="$1" desc="$2" label="$3" rc="$4" out="$5" ms="$6" budget="$7"

  gate_record_timing "$name" "$ms"

  if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
    echo "  ${RED}TIMEOUT${NC}  $name  $desc  (no result within ${budget}s)"
    [ -n "$out" ] && printf '%s\n' "$out" | sed 's/^/        /' | head -20
    RESULTS_TIMEOUT=$((RESULTS_TIMEOUT + 1))
    RESULTS_FAILNAMES="$RESULTS_FAILNAMES $name"
    return 0
  fi
  if [ "$rc" -eq 0 ]; then
    echo "  ${GRN}PASS${NC}  $name  $label"
    RESULTS_PASS=$((RESULTS_PASS + 1))
  else
    echo "  ${RED}FAIL${NC}  $name  $desc"
    if [ -n "$out" ]; then
      # B322: the check's OWN failing lines come first (see run_check).
      printf '%s\n' "$out" | grep -aE 'FAIL|SKY-FAIL|TIMEOUT' | head -12 | sed 's/^/        /'
      printf '%s\n' "$out" | sed 's/^/        /' | head -20
    fi
    RESULTS_FAIL=$((RESULTS_FAIL + 1))
    RESULTS_FAILNAMES="$RESULTS_FAILNAMES $name"
  fi
  # TD-22: informational sub-contract SKIP rows, never part of the verdict.
  RESULTS_SKIP_ROWS=$((RESULTS_SKIP_ROWS + $(printf '%s\n' "$out" | grep -acE '(^|[[:space:]])SKIP([[:space:]]|$)')))
}

# ---------------------------------------------------------------------------
# The queue (only initialised when a safe check is actually enqueued)
# ---------------------------------------------------------------------------

# gate_queue_init — create the queue dirs and start the workers.
gate_queue_init() {
  [ -n "${GATE_QUEUE:-}" ] && return 0
  GATE_QUEUE="$(mktemp -d "${TMPDIR:-/tmp}/skygate-gate-queue.XXXXXX" 2>/dev/null)" || {
    GATE_JOBS=1
    return 1
  }
  mkdir -p "$GATE_QUEUE/new" "$GATE_QUEUE/work" "$GATE_QUEUE/done"
  GATE_SEQ=0
  local w
  for w in $(seq 1 "$GATE_JOBS"); do
    ( gate_worker "$w" ) &
  done
  GATE_WORKERS=1
  trap 'gate_cleanup' EXIT
  return 0
}

gate_cleanup() {
  if [ -n "${GATE_QUEUE:-}" ] && [ -d "$GATE_QUEUE" ]; then
    rm -rf "$GATE_QUEUE" 2>/dev/null || true
  fi
  if [ -n "${GATE_TIMING:-}" ] && [ -d "$GATE_TIMING" ]; then
    rm -rf "$GATE_TIMING" 2>/dev/null || true
  fi
}

# gate_worker <n> — pulls jobs until the queue is closed and empty.
#
# Picking is atomic (`mv` of a file: exactly one worker wins), so two workers can
# never execute the same check. Results go to files because a subshell cannot touch
# the parent's counters.
gate_worker() {
  local w="$1" cand claimed srcbase job
  while :; do
    claimed=""
    srcbase=""
    for cand in "$GATE_QUEUE"/new/*.job; do
      [ -e "$cand" ] || continue
      # The index must be read from the QUEUE name, not from the work name: after
      # the mv the basename is the worker number, and drain() looks jobs up by index.
      srcbase="$(basename "$cand" .job)"
      job="$GATE_QUEUE/work/$w.job"
      if mv "$cand" "$job" 2>/dev/null; then
        claimed="$job"
        break
      fi
      srcbase=""
    done
    if [ -z "$claimed" ]; then
      # Nothing claimable. Exit only after the producer closed the queue; the
      # second test closes the race where a job is written just before `stop`.
      if [ -f "$GATE_QUEUE/stop" ]; then
        local rest
        rest=$(ls -1 "$GATE_QUEUE"/new/*.job 2>/dev/null | head -n 1)
        [ -z "$rest" ] && return 0
      fi
      sleep 0.2
      continue
    fi
    local idx name desc label cmd t0 t1
    idx="$srcbase"
    name="$(sed -n '1p' "$claimed")"
    desc="$(sed -n '2p' "$claimed")"
    label="$(sed -n '3p' "$claimed")"
    cmd="$(tail -n +4 "$claimed")"
    [ "${GATE_DEBUG:-0}" = "1" ] && printf 'DEBUG worker %s start %s at %s\n' "$w" "$idx" "$(gate_now_ms)" >>"${GATE_DEBUG_FILE:-/dev/null}"
    t0="$(gate_now_ms)"
    gate_exec "$cmd"
    t1="$(gate_now_ms)"
    [ "${GATE_DEBUG:-0}" = "1" ] && printf 'DEBUG worker %s end   %s at %s rc=%s\n' "$w" "$idx" "$(gate_now_ms)" "$RC" >>"${GATE_DEBUG_FILE:-/dev/null}"
    { sed -n '1,3p' "$claimed"; } >"$GATE_QUEUE/done/$idx.head" 2>/dev/null || true
    printf '%s\n' "$RC" >"$GATE_QUEUE/done/$idx.rc"
    printf '%s\n' "$((t1 - t0))" >"$GATE_QUEUE/done/$idx.ms"
    printf '%s\n' "$OUT" >"$GATE_QUEUE/done/$idx.out"
  done
}

# gate_enqueue <name> <desc> <label> <cmd> — hand a SAFE check to the pool.
gate_enqueue() {
  gate_queue_init || return 1
  GATE_SEQ=$((GATE_SEQ + 1))
  local idx
  idx="$(printf '%06d' "$GATE_SEQ")"
  {
    printf '%s\n' "$1"
    printf '%s\n' "$2"
    printf '%s\n' "$3"
    printf '%s\n' "$4"
  } >"$GATE_QUEUE/new/$idx.job"
}

# gate_drain — close the queue, wait for the workers, print the queued rows IN
# REGISTRY ORDER and fold them into the counters. Called ONCE, before the summary.
gate_drain() {
  gate_jobs_mode || return 0
  [ -n "${GATE_QUEUE:-}" ] && [ -d "$GATE_QUEUE" ] || return 0
  : >"$GATE_QUEUE/stop"
  wait
  local idx name desc label rc ms out budget
  budget="${SKYGATE_CHECK_TIMEOUT:-900}"
  for idx in $(seq 1 "${GATE_SEQ:-0}"); do
    idx="$(printf '%06d' "$idx")"
    [ -f "$GATE_QUEUE/done/$idx.rc" ] || continue
    name="$(sed -n '1p' "$GATE_QUEUE/done/$idx.head")"
    desc="$(sed -n '2p' "$GATE_QUEUE/done/$idx.head")"
    label="$(sed -n '3p' "$GATE_QUEUE/done/$idx.head")"
    rc="$(cat "$GATE_QUEUE/done/$idx.rc")"
    ms="$(cat "$GATE_QUEUE/done/$idx.ms" 2>/dev/null || echo 0)"
    out="$(cat "$GATE_QUEUE/done/$idx.out" 2>/dev/null || true)"
    gate_report "$name" "$desc" "$label" "$rc" "$out" "$ms" "$budget"
  done
}
