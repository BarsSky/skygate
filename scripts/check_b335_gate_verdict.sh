#!/usr/bin/env bash
# check_b335_gate_verdict.sh
#
# 2026-10-01 (B335) — the guarantee catalog must have a VERDICT, not just a report.
#
# THE MEASUREMENT
# ---------------
# A full baseline run on the reference VM (2026-10-01, v1.5.94) ended with:
#
#	368 PASS, 10 FAIL, 1 SKIP … and exit code 0
#
# `scripts/verify_pre_deploy.sh` maintained `RESULTS_PASS`/`RESULTS_FAIL` and
# never read them, so the script's status was the status of its LAST statement.
# Two consequences, both live:
#
#   * `.githooks/pre-push` branches on that status, so it blocked nothing;
#   * the only thing enforcing anything was CI's `grep '^  FAIL'` on the log —
#     and a local run printed a green-looking wall of PASS rows with the ten
#     failures buried in it.
#
# That is TD-22. B322 made the individual checks able to fail; this block makes
# the CATALOG's verdict real: the summary is printed, a TIMEOUT counts as a
# failure, and the exit code is the verdict.
#
# WHY A BEHAVIOURAL HALF
# ----------------------
# A source grep cannot tell "prints a summary" from "prints a summary and then
# continues to exit 0". So the contract EXTRACTS the real verdict block from the
# real catalog and executes it against synthetic result counters. The extraction
# keeps the two in lock-step: edit the verdict, and this check runs the edit.
#
# CONTRACTS
#   A. the catalog has a verdict block that prints every counter and returns it
#   B. BEHAVIOURAL: the extracted block exits 1 on FAIL, 1 on TIMEOUT, 0 otherwise
#   C. the counters the verdict reads are actually maintained by the wrapper
#   D. the measured baseline is recorded where the operator will look for it
#   E. tracked by git (AGENTS trap #11) and registered in the catalog

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B335: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE=scripts/verify_pre_deploy.sh

# Capture the verdict block ONCE, then match with herestrings. Piping a
# producer into `grep -q` under `pipefail` is AGENTS trap #9: grep exits at its
# first match, closes the pipe, and the still-writing producer dies with
# SIGPIPE (141) — which `pipefail` then reports as a failed pipeline even though
# the match succeeded. This check tripped over its own trap on the first run.
VERDICT_TEXT="$(awk "/THE GATE.S VERDICT/,0" "$GATE" 2>/dev/null)"

hdr "B335 — the catalog's verdict must be real"

# ---------------------------------------------------------------------------
hdr "A. the catalog has a verdict block"
# ---------------------------------------------------------------------------
if [ -n "$VERDICT_TEXT" ]; then
  ok "A1: $GATE carries the verdict block ($(printf '%s\n' "$VERDICT_TEXT" | wc -l | tr -d ' ') lines)"
else
  bad "A1: $GATE has no verdict block — the catalog is a report, not a gate (TD-22)"
fi

for counter in RESULTS_PASS RESULTS_FAIL RESULTS_TIMEOUT RESULTS_SKIP; do
  if grep -qF "\$$counter" <<< "$VERDICT_TEXT"; then
    ok "A2: the summary reports \$$counter"
  else
    bad "A2: the summary does not report \$$counter"
  fi
done

if grep -qE 'exit 1' <<< "$VERDICT_TEXT"; then
  ok "A3: the verdict can return a non-zero status"
else
  bad "A3: the verdict block never exits non-zero — a run with FAILs would still report success"
fi
if tail -3 "$GATE" | grep -qE '^exit 0'; then
  ok "A4: the catalog's last statement is the verdict's own 'exit 0' (nothing can overwrite the status afterwards)"
else
  bad "A4: the catalog does not END on the verdict's 'exit 0' — a later statement would decide the status again, which is the TD-22 defect"
fi
if grep -qi 'TIMEOUT' <<< "$VERDICT_TEXT"; then
  ok "A5: a TIMEOUT is treated as a failure (a check that produced no result is not a check that passed)"
else
  bad "A5: the verdict ignores RESULTS_TIMEOUT — a killed check would not affect the status"
fi

# ---------------------------------------------------------------------------
hdr "B. behavioural — the REAL verdict block rejects a failing run"
# ---------------------------------------------------------------------------
# Execute the block extracted above, verbatim, with stubbed helpers. Extracting
# rather than reimplementing is the point: this runs the code the catalog runs.
VERDICT="$(mktemp)"
printf '%s\n' "$VERDICT_TEXT" > "$VERDICT"
# Drop the trailing `exit 0`/`exit 1`? No — the exit status IS what we assert.
if [ -s "$VERDICT" ]; then
  ok "B1: the verdict block was extracted ($(wc -l < "$VERDICT" | tr -d ' ') lines)"
else
  bad "B1: could not extract the verdict block from $GATE"
fi

run_verdict() { # pass fail timeout skip → exit code
  local p="$1" f="$2" t="$3" s="$4"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'RED=""; GRN=""; YLW=""; NC=""\n'
    printf 'RESULTS_PASS=%d; RESULTS_FAIL=%d; RESULTS_TIMEOUT=%d; RESULTS_SKIP=%d\n' "$p" "$f" "$t" "$s"
    printf 'RESULTS_SKIP_ROWS=0; RESULTS_FAILNAMES=" Btest"\n'
    printf 'SKYGATE_TEST_PG_DSN=""\n'
    cat "$VERDICT"
  } > "$VERDICT.run"
  # `bash <file>` so a missing exec bit cannot mask the answer.
  bash "$VERDICT.run" >/dev/null 2>&1
  return $?
}

run_verdict 368 0 0 1; rc=$?
if [ "$rc" -eq 0 ]; then
  ok "B2: a clean run (368 PASS / 0 FAIL) exits 0"
else
  bad "B2: a clean run exited $rc — the gate would block a green tree"
fi
run_verdict 368 10 0 1; rc=$?
if [ "$rc" -ne 0 ]; then
  ok "B3: the measured baseline (10 FAIL) exits non-zero ($rc) — this is exactly what v1.5.94 got wrong"
else
  bad "B3: a run with 10 FAILs exited 0. THIS IS THE TD-22 DEFECT: the gate reports success while reporting failures"
fi
run_verdict 0 0 1 0; rc=$?
if [ "$rc" -ne 0 ]; then
  ok "B4: a TIMEOUT alone exits non-zero ($rc) — a killed check is a check with no result, not a pass"
else
  bad "B4: a run whose only result was a TIMEOUT exited 0"
fi
run_verdict 100 0 0 20; rc=$?
if [ "$rc" -eq 0 ]; then
  ok "B5: SKIPs alone exit 0 — a check whose live dependency is absent must SKIP, never FAIL (AGENTS rule 1)"
else
  bad "B5: a run of SKIPs exited $rc — SKIP must never fail the gate (AGENTS rule 1)"
fi
rm -f "$VERDICT" "$VERDICT.run" 2>/dev/null

# ---------------------------------------------------------------------------
hdr "C. the counters the verdict reads are maintained"
# ---------------------------------------------------------------------------
if grep -qE 'RESULTS_FAIL=\$\(\(RESULTS_FAIL \+ 1\)\)' "$GATE"; then
  n=$(grep -cE 'RESULTS_FAIL=\$\(\(RESULTS_FAIL \+ 1\)\)' "$GATE")
  ok "C1: RESULTS_FAIL is incremented on failure ($n sites)"
else
  bad "C1: RESULTS_FAIL is never incremented — the verdict would always see 0"
fi
if grep -qE 'RESULTS_TIMEOUT=\$\(\(RESULTS_TIMEOUT \+ 1\)\)' "$GATE"; then
  ok "C2: RESULTS_TIMEOUT is incremented on a killed check"
else
  bad "C2: RESULTS_TIMEOUT is never incremented"
fi
if grep -qE 'RESULTS_FAILNAMES="\$RESULTS_FAILNAMES \$name"' "$GATE"; then
  ok "C3: the failing check NAMES are collected, so the summary can list them instead of making the operator scroll"
else
  bad "C3: the failing check names are not collected — the summary cannot say WHICH checks failed"
fi
if grep -qE 'RESULTS_SKIP=\$\(\(RESULTS_SKIP \+ 1\)\)' "$GATE"; then
  ok "C4: RESULTS_SKIP is maintained (the --quick path reports SKIP, not a silent omission)"
else
  bad "C4: RESULTS_SKIP is never incremented"
fi
if grep -qE 'RESULTS_SKIP_ROWS=\$\(\(RESULTS_SKIP_ROWS' "$GATE"; then
  ok "C5: sub-contract SKIP rows are counted, so 'verified' can be told apart from 'had nothing to verify'"
else
  bad "C5: sub-contract SKIP rows are not counted — the summary cannot distinguish an inspected contract from an unexercised one"
fi

# ---------------------------------------------------------------------------
hdr "D. the measured baseline is recorded where the operator looks"
# ---------------------------------------------------------------------------
if grep -q '368 PASS' docs/ROADMAP.md || grep -q '368 PASS' "$GATE"; then
  ok "D1: the v1.5.94 baseline (368 PASS / 10 FAIL / exit 0) is recorded"
else
  bad "D1: the measured baseline is not recorded — the next reader cannot tell whether 10 FAILs is new"
fi
if grep -q 'TD-22' docs/ROADMAP.md; then
  ok "D2: docs/ROADMAP.md carries the TD-22 row"
else
  bad "D2: docs/ROADMAP.md has no TD-22 row"
fi

# ---------------------------------------------------------------------------
hdr "E. tracked by git and registered in the catalog"
# ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b335_gate_verdict.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: scripts/check_b335_gate_verdict.sh is NOT tracked by git (AGENTS trap #11: .gitignore can eat a new script)"
fi
if grep -q 'run_check "B335"' "$GATE"; then
  ok "E2: $GATE registers B335"
else
  bad "E2: $GATE does not register B335 — the contract would never run"
fi
if grep -q '\*\*B335\*\*' AGENTS.md; then
  ok "E3: AGENTS.md's block index carries B335"
else
  bad "E3: AGENTS.md has no B335 entry (AGENTS rule 2)"
fi

# ---------------------------------------------------------------------------
hdr "F. every catalog entry keeps its ARGUMENT BOUNDARIES"
# ---------------------------------------------------------------------------
# Measured 2026-10-02: the B343 entry was registered with the description's
# CLOSING QUOTE missing (`run_check "B343" "…essay \`), so bash swallowed the
# following lines into the string until the next `"` — which happened to be in a
# COMMENT block, so the file stayed quote-BALANCED and `bash -n` passed while the
# command that actually ran was catalog garbage:
#
#	must: line 1: admin: command not found
#
# The gate read that as a product FAIL on B343. The runtime arity guard cannot
# see this class: the call still has three arguments, one of them wrong.
catalog_quote_violations() {
  local f="$1" line lineno=0 body stripped out=""
  while IFS= read -r line; do
    lineno=$((lineno + 1))
    case "$line" in 'run_check "'*) ;; *) continue ;; esac
    case "$line" in *'\') ;; *) continue ;; esac   # only continued entries: one-liners carry the command
    body="${line%\\}"
    body="${body%"${body##*[![:space:]]}"}"
    case "$body" in
      *'"') ;;
      *) out="$out $lineno(description-not-closed)"; continue ;;
    esac
    stripped="${body//\"/}"
    if [ $(( (${#body} - ${#stripped}) % 2 )) -ne 0 ]; then
      out="$out $lineno(odd-quote-count)"
    fi
  done < "$f"
  printf '%s' "$out"
}
VIOL="$(catalog_quote_violations "$GATE")"
if [ -z "$VIOL" ]; then
  ok "F1: every continued run_check closes its description quote before the backslash"
else
  bad "F1: malformed catalog entr(ies):$VIOL — a missing closing quote swallows the following lines (AGENTS rule: the description is an ARGUMENT)"
fi
# The detector must be able to fail, or "no violations" means "the detector broke".
PLANTED="$(mktemp)"
printf 'run_check "B999" "a description with no closing quote \\\n  '"'"'true'"'"'\n' > "$PLANTED"
PLANTED_VIOL="$(catalog_quote_violations "$PLANTED")"
rm -f "$PLANTED"
case "$PLANTED_VIOL" in
  *not-closed*) ok "F2: the detector fires on the exact shape B343 had (a planted entry with no closing quote)" ;;
  *) bad "F2: the detector did NOT see a planted unclosed entry — 'no violations' would be vacuous" ;;
esac

printf '\n\033[1mB335 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
