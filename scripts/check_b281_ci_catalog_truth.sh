#!/usr/bin/env bash
# scripts/check_b281_ci_catalog_truth.sh
# B281 (2026-09-22) — the CI catalog job must terminate, and "green" must mean
# 0 FAIL.
#
# WHY THIS B-BLOCK EXISTS
# The v1.5.46 release could not be cut: the `verify-pre` job came back as
# `cancelled` on every push and the log simply stopped mid-catalog (last printed
# check B260, then 22 minutes of silence until the runner killed the step). It
# was not a concurrency cancellation — the job hit its own `timeout-minutes: 30`
# — and nothing in the log named the check that was stuck. Four separate defects
# made that one red row possible, and, more importantly, made a green row
# meaningless:
#
#   1. run_check ran every check unbounded (`bash scripts/verify_pre_deploy.sh`
#      with no per-check budget), so one hung check ate the whole job budget;
#   2. the job budget itself was too small for a catalog that has grown to
#      B1-B281;
#   3. the catalog is deliberately fail-tolerant locally (it always exits 0 —
#      docs/operations.md §1.3: read the output, not `$?`), so CI reported
#      SUCCESS with ~15 FAIL rows inside it, which is what the new release gate
#      (scripts/ci_gate.sh, B280) reads as "CI is green";
#   4. the FAILs themselves: 36 check scripts probed a hardcoded
#      /usr/local/go/bin/go BEFORE `command -v go` (the runner image's Go 1.24.13
#      with GOTOOLCHAIN=local vs a go.mod requiring >= 1.25), staticcheck was
#      never installed on the runner (B95/B237.16), and 8 live-state checks
#      FAILed where AGENTS.md §1.1 requires SKIP.
#
# This is the regression guard for all four halves. It is pure grep/pure logic —
# no live state, no Go build — so it must never SKIP.
#
# CONTRACTS
#   A. ci.yml: the verify-pre job has a budget that fits the catalog (60 min)
#   B. ci.yml: staticcheck is installed before the catalog runs
#   C. ci.yml: $(go env GOPATH)/bin is put on $GITHUB_PATH
#   D. ci.yml: the catalog step REFUSES a FAIL/TIMEOUT row (green == 0 FAIL)
#   E. verify_pre_deploy.sh: run_check wraps each check in `timeout`
#   F. verify_pre_deploy.sh: rc 124 is reported as a NAMED TIMEOUT row
#   G. no check script probes a hardcoded go path before `command -v go`
#   H. check_b182.sh: the [D-annotator-call] count with an unescaped `(` inside
#      `grep -E` is gone (unbalanced group => grep exits 2 => permanent FAIL),
#      and the escaped E2 call-site form is still asserted
#   I. check_b191.sh: the live-state contracts print SKIP *only* (no `FAIL` line
#      that exits 0 — it misleads a standalone run and the 0-FAIL grep sees it)
#   J. check_b178.sh contract N probes `command -v go` first and captures the
#      `go test` output before matching it (AGENTS.md trap #9)
#   K. this check is registered in scripts/verify_pre_deploy.sh and indexed in
#      AGENTS.md
#
# USAGE
#   bash scripts/check_b281_ci_catalog_truth.sh
# Exit code: 0 always when every contract passes; 1 otherwise (the catalog's
# run_check turns a non-zero exit into a FAIL row).

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

ok()  { PASS=$((PASS+1)); printf '  PASS  %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  FAIL  %s\n' "$1"; }

check_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    ok "[$label] $actual"
  else
    bad "[$label] expected=$expected actual=$actual"
  fi
}

check_ge() {
  local label="$1" min="$2" actual="$3"
  if [ "$actual" -ge "$min" ] 2>/dev/null; then
    ok "[$label] $actual"
  else
    bad "[$label] expected>=$min actual=$actual"
  fi
}

count() {
  local n
  n=$(grep -cE "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

CI="$REPO/.github/workflows/ci.yml"
VPD="$REPO/scripts/verify_pre_deploy.sh"
AGENTS="$REPO/AGENTS.md"

echo "=== B281 contracts ==="

# The contracts A-D must be about the *catalog job* specifically — ci.yml has
# other jobs with their own budgets — so extract the verify-pre job block first
# (from `  verify-pre:` to the next top-level job key).
VJOB=""
if [ -f "$CI" ]; then
  VJOB=$(awk '/^  verify-pre:/{f=1;next} f && /^  [A-Za-z0-9_-]+:/{exit} f{print}' "$CI")
fi
job_count() {
  local n
  n=$(printf '%s\n' "$VJOB" | grep -cE "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

# --- A. the catalog job has a budget that fits the catalog -------------------
if [ -n "$VJOB" ]; then
  check_ge "A-timeout" 1 "$(job_count 'timeout-minutes: 60')"
  # A2. the 30-minute budget is what produced `cancelled`; it must be gone from
  #     THIS job (other jobs may keep their own)
  check_eq "A2-not-30" "0" "$(job_count 'timeout-minutes: 30')"
else
  bad "[A] verify-pre job block not found in $CI"
fi

# --- B. staticcheck present on the runner ------------------------------------
if [ -n "$VJOB" ]; then
  check_ge "B-install" 1 "$(job_count 'go install honnef\.co/go/tools/cmd/staticcheck@v0\.7\.0')"
  # B2. the pin is load-bearing: `@latest` resolved to a release requiring
  #     Go >= 1.26 while this job runs 1.25.14 with GOTOOLCHAIN=local, and the
  #     install step killed the job before a single check ran (16 s).
  check_eq "B2-no-latest" "0" "$(job_count 'staticcheck@latest')"
  # B3. prove the binary works here instead of failing inside the catalog
  check_ge "B3-version-probe" 1 "$(job_count 'staticcheck" -version')"
else
  bad "[B] verify-pre job block not found in $CI"
fi

# --- C. GOPATH/bin on PATH for the catalog step ------------------------------
if [ -n "$VJOB" ]; then
  check_ge "C-gopath-path" 1 "$(job_count 'GITHUB_PATH')"
else
  bad "[C] verify-pre job block not found in $CI"
fi

# --- D. green == 0 FAIL ------------------------------------------------------
if [ -n "$VJOB" ]; then
  check_ge "D-fail-grep" 1 "$(job_count 'FAIL\|TIMEOUT')"
  # D2. the step must actually fail the job, not just print
  check_ge "D2-error" 1 "$(job_count '::error::')"
  # D3. the enforcement must run on the ANALYSED log (ANSI stripped), otherwise
  #     the `^  FAIL  ` anchor misses every coloured row
  check_ge "D3-ansi-strip" 1 "$(job_count 'x1B')"
else
  bad "[D] verify-pre job block not found in $CI"
fi

# --- E/F. per-check budget in the catalog runner -----------------------------
if [ -f "$VPD" ]; then
  check_ge "E-budget-var" 1 "$(count "$VPD" 'SKYGATE_CHECK_TIMEOUT:-900')"
  check_ge "E-timeout-wrap" 1 "$(count "$VPD" 'timeout "\$budget" bash -c "\$cmd"')"
  check_ge "F-rc124" 1 "$(count "$VPD" '\[ "\$rc" -eq 124 \]')"
  check_ge "F-timeout-row" 1 "$(count "$VPD" 'TIMEOUT[$][{]NC[}]')"
  # E4/F2. the check's stdin is closed. On a runner the step's stdin is an open
  #        pipe that is never closed, so a bare `grep PATTERN` (no file operand)
  #        blocks forever and prints nothing — the 22-minute silence that got
  #        the job cancelled. `< /dev/null` turns that into an immediate EOF.
  check_ge "F2-stdin-closed" 2 "$(count "$VPD" '< /dev/null')"
else
  bad "[E/F] $VPD not found"
fi

# --- G. no hardcoded go probe before `command -v go` -------------------------
# The bug: a candidate list / assignment that puts /usr/local/go/bin/go first, so
# the check silently uses whatever Go sits at that path (on the GitHub runner:
# the image's 1.24.13 with GOTOOLCHAIN=local, against a go.mod requiring 1.25).
# The invariant is about CODE, so comment lines are stripped first — a comment
# may legitimately mention the path (check_b178.sh documents it).
first_code_line_with() {
  local f="$1" want="$2" ln text
  while IFS= read -r ln; do
    text=$(sed -n "${ln}p" "$f")
    text=${text%%#*}
    case "$text" in
      *"$want"*) echo "$ln"; return ;;
    esac
  done < <(grep -n "$want" "$f" | cut -d: -f1)
  echo ""
}
G_BAD=""
G_SCANNED=0
for f in "$REPO"/scripts/check_*.sh; do
  [ -f "$f" ] || continue
  grep -q '/usr/local/go/bin/go' "$f" 2>/dev/null || continue
  G_SCANNED=$((G_SCANNED+1))
  i_hard=$(first_code_line_with "$f" '/usr/local/go/bin/go')
  [ -n "$i_hard" ] || continue
  i_cmd=$(grep -n 'command -v go' "$f" | head -1 | cut -d: -f1)
  if [ -z "$i_cmd" ] || [ "$i_cmd" -gt "$i_hard" ]; then
    G_BAD="$G_BAD $(basename "$f")"
  fi
done
check_ge "G-scanned" 1 "$G_SCANNED"
check_eq "G-order" "" "$G_BAD"
# G2. in-line order: on a single-line candidate list, `command -v go` must come
#     BEFORE the hardcoded path. check_b182/183/184/186 had
#     `for cand in /usr/local/go/bin/go … "$(command -v go)"`, i.e. the correct
#     probe existed but was consulted LAST — the file-level G check above cannot
#     see that (both are on one line), and on the runner it resolved to the
#     image's Go 1.24.13.
G_INLINE_BAD=""
for f in "$REPO"/scripts/check_*.sh; do
  [ -f "$f" ] || continue
  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    ln="${hit%%:*}"
    text="${hit#*:}"
    pre="${text%%/usr/local/go/bin/go*}"
    case "$pre" in
      *'command -v go'*) ;;
      *) G_INLINE_BAD="$G_INLINE_BAD $(basename "$f"):$ln" ;;
    esac
  done < <(grep -nE '^[[:space:]]*for[[:space:]].*in[[:space:]].*/usr/local/go/bin/go' "$f" 2>/dev/null)
done
check_eq "G2-inline-order" "" "$G_INLINE_BAD"

# --- H. check_b182.sh: no unbalanced grep -E group ---------------------------
B182="$REPO/scripts/check_b182.sh"
if [ -f "$B182" ]; then
  # H1. the unescaped-paren form (grep -E => unbalanced group => exit 2 => 0)
  check_eq "H1-no-unescaped" "0" "$(count "$B182" "count .*'annotateRulesWithPrefs\(rr, func'")"
  # H2. the escaped call-site form is still asserted
  check_ge "H2-escaped" 1 "$(count "$B182" "annotateRulesWithPrefs\\\\\(rr, func")"
else
  bad "[H] $B182 not found"
fi

# --- I. check_b191.sh: live-state rows print SKIP only -----------------------
B191="$REPO/scripts/check_b191.sh"
if [ -f "$B191" ]; then
  check_eq "I1-no-fail-headscale" "0" "$(count "$B191" 'bad "headscale CLI not reachable')"
  check_eq "I2-no-fail-tailscale" "0" "$(count "$B191" 'bad "tailscale CLI not reachable')"
  check_ge "I3-skips" 3 "$(count "$B191" 'SKIP  live check')"
else
  bad "[I] $B191 not found"
fi

# --- J. check_b178.sh contract N: probe order + trap #9 ----------------------
B178="$REPO/scripts/check_b178.sh"
if [ -f "$B178" ]; then
  N_LINE=$(grep -nE '^[[:space:]]*for[[:space:]].*in[[:space:]]' "$B178" | grep 'go/bin/go' | head -1)
  if [ -n "$N_LINE" ]; then
    if echo "$N_LINE" | grep -q 'command -v go'; then
      ok "[J1-probe-order] candidate list consults command -v go"
    else
      bad "[J1-probe-order] candidate list does not consult command -v go first"
    fi
  else
    bad "[J1-probe-order] no go candidate list found in check_b178.sh"
  fi
  # J2. trap #9: never `go test ... | grep -q`
  check_eq "J2-no-pipe-grepq" "0" "$(count "$B178" 'test -count=1 ./internal/feature/exit_rules/\.\.\. 2>&1\) \| grep -q')"
  check_ge "J3-captured" 1 "$(count "$B178" 'B178_TEST_OUT=')"
else
  bad "[J] $B178 not found"
fi

# --- K. registration + index ------------------------------------------------
if [ -f "$VPD" ]; then
  check_ge "K1-registered" 1 "$(count "$VPD" 'check_b281_ci_catalog_truth\.sh')"
else
  bad "[K1] $VPD not found"
fi
if [ -f "$AGENTS" ]; then
  check_ge "K2-indexed" 1 "$(count "$AGENTS" '\*\*B281\*\*')"
else
  bad "[K2] $AGENTS not found"
fi

# --- L. the B261 silence: never glob a temp directory into a reader ----------
# `grep -q PATTERN "$PD/../"*` reads every entry of the temp PARENT (on a runner
# $TMPDIR is /home/runner/work/_temp) and discarded the result. A non-regular
# file in that directory blocks a reader forever — the exact 22-minute silence
# before the cancellation. The check's real assertion (`grep -q ... "$P_LOG"`)
# is right below it.
B261="$REPO/scripts/check_b261_native_self_update.sh"
if [ -f "$B261" ]; then
  check_eq "L1-no-tempdir-glob" "0" "$(count "$B261" '"\$PD/\.\./"\*')"
  check_ge "L2-real-assertion" 1 "$(count "$B261" "grep -q 'verified against SHA256SUMS' \"\\\$P_LOG\"")"
else
  bad "[L] $B261 not found"
fi

echo
echo "=== B281 summary: $PASS PASS, $FAIL FAIL ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
