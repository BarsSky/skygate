#!/usr/bin/env bash
# check_b413_fast_gate.sh
#
# 2026-10-23 (B413) — the pre-deploy catalog must be FAST to work with, and the
# speed-up must be measured rather than assumed.
#
# OPERATOR: «очень долго осуществляется прогон гейт есть ли способ распараллелить
# выполнение его и тем самым ускорить?»
#
# THE MEASUREMENT THAT SHAPED THIS BLOCK (do not "fix" it back without one):
#
#   a full parallel pool was implemented (6 workers, file queue, atomic pickup) and
#   measured on the reference 8-core Windows workstation:
#
#     sequential            ~50 min   459/0   (the baseline)
#     6 workers            ~119 min   4 RED CHECKS on a healthy tree
#
#   Cause, from the per-check timings: 182 checks cost >= 10 s and together spend
#   88 % of the catalog's CPU; 19 are multi-minute (708 s, 350 s, 208 s …). They are
#   `check_b*.sh` contracts that themselves run `go test`, so six concurrent workers
#   oversubscribe the box and one go-test check eventually blows its budget and
#   reports a PRODUCT failure that is a scheduling artefact (AGENTS trap #15).
#   Parallelism does not create CPU, it only decides who waits for it.
#
# WHAT SHIPPED INSTEAD
#   * per-check TIMINGS + the eight slowest checks in the summary (and
#     SKYGATE_GATE_TIMINGS_OUT=<file> for the full TSV);
#   * SKYGATE_GATE_ONLY=<id,…> — run selected checks, print SKIP for the rest;
#   * scripts/gate_affected.sh — maps a diff to catalog entries and runs the subset,
#     so the everyday loop is minutes while push/tag/CI keep the full catalog.
#
# CONTRACTS
#   A  the selector exists, is tracked, parses, and refuses a bad base
#   B  selection behaves: it reports its reasons, always includes the meta checks,
#      is deterministic, and says which changed paths nobody owns
#   C  the catalog honours SKYGATE_GATE_ONLY (behavioural: selected PASS, the rest
#      SKIP, verdict green — a filter must never be able to make the gate lie)
#   D  the timing surface exists and is emitted (behavioural small run)
#   E  the measured decision is recorded where the next reader looks: the banner in
#      the catalog, no resurrected worker pool, docs, AGENTS index, registration

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE="scripts/verify_pre_deploy.sh"
SEL="scripts/gate_affected.sh"
DOC="docs/operations.md"
TMPD="$(mktemp -d "${TMPDIR:-/tmp}/b413.XXXXXX")" || exit 2
trap 'rm -rf "$TMPD"' EXIT

hdr "B413 — a fast gate must be measured, not assumed"

# --- A ------------------------------------------------------------------------
if [ -f "$SEL" ]; then ok "A1: $SEL exists"; else bad "A1: $SEL is missing"; fi
if git ls-files --error-unmatch "$SEL" >/dev/null 2>&1; then
  ok "A2: the selector is TRACKED by git (AGENTS trap #11)"
else
  bad "A2: $SEL is not tracked — it would be absent from the pushed tag"
fi
if bash -n "$SEL" 2>/dev/null; then ok "A3: the selector parses"; else bad "A3: the selector does not parse"; fi
if bash -n "$GATE" 2>/dev/null; then ok "A4: the catalog still parses"; else bad "A4: the catalog does not parse"; fi
# A mistyped base must be a REFUSAL, never an empty selection: "nothing to check"
# and "the base was wrong" must not look the same.
bash "$SEL" --base refs/heads/definitely-not-a-ref-b413 --dry-run >"$TMPD/badbase.log" 2>&1
rc=$?
if [ "$rc" -eq 2 ] && grep -q 'does not resolve' "$TMPD/badbase.log"; then
  ok "A5: an unresolvable base is refused with exit 2 (it cannot masquerade as an empty diff)"
else
  bad "A5: a bad base exited $rc without the named refusal"
fi

# --- B ------------------------------------------------------------------------
# A tiny synthetic diff: one file that the catalog names, one that it does not.
printf '#!/usr/bin/env bash\ntrue\n' >"$TMPD/touch.sh"
bash "$SEL" --base HEAD --dry-run >"$TMPD/sel.log" 2>&1
rc=$?
if [ "$rc" -eq 0 ] && grep -q 'selected .* check(s)' "$TMPD/sel.log"; then
  ok "B1: --dry-run reports the selection size and runs nothing"
else
  bad "B1: --dry-run failed (rc=$rc)"
fi
for m in "B1 <- always" "B325 <- always" "B413 <- always"; do
  if grep -qF "$m" "$TMPD/sel.log"; then
    ok "B2: the meta check set always includes '$m'"
  else
    bad "B2: the meta check set is missing '$m' — a diff-scoped run could skip the harness guards"
  fi
done
# Determinism: the same diff must select the same ids.
bash "$SEL" --base HEAD --dry-run 2>&1 | grep -oE '^  [A-Za-z0-9._-]+ <- ' | sort >"$TMPD/sel1.ids"
bash "$SEL" --base HEAD --dry-run 2>&1 | grep -oE '^  [A-Za-z0-9._-]+ <- ' | sort >"$TMPD/sel2.ids"
if cmp -s "$TMPD/sel1.ids" "$TMPD/sel2.ids"; then
  ok "B3: the selection is deterministic ($(wc -l <"$TMPD/sel1.ids" | tr -d ' ') ids for the same diff)"
else
  bad "B3: two runs over the SAME diff selected different checks"
fi
if grep -q 'NOT covered by any single check' "$TMPD/sel.log"; then
  ok "B4: unowned changed paths are reported instead of being silently ignored"
else
  # Not every diff has such a path; the surface must still exist in the script.
  if grep -q 'NOT covered by any single check' "$SEL"; then
    ok "B4: the unowned-path surface exists (this diff happened to own all of them)"
  else
    bad "B4: the selector never reports changed paths that no check owns"
  fi
fi

# --- C: the catalog honours the filter ---------------------------------------
# Behavioural and cheap: one selected check + everything else SKIP. The verdict must
# stay GREEN (a SKIP is not a failure) and the selected check must really run.
if SKYGATE_GATE_ONLY="B9" bash "$GATE" >"$TMPD/only.log" 2>&1; then
  PASSED="$(grep -cE '^  .*PASS' "$TMPD/only.log" || true)"
  SKIPPED="$(grep -c 'not selected by SKYGATE_GATE_ONLY' "$TMPD/only.log" || true)"
  if [ "${PASSED:-0}" -ge 1 ] && [ "${SKIPPED:-0}" -ge 100 ]; then
    ok "C1: SKYGATE_GATE_ONLY runs the selected check (${PASSED} PASS) and SKIPs the rest (${SKIPPED} rows)"
  else
    bad "C1: the filter did not behave (PASS=${PASSED:-0} SKIP=${SKIPPED:-0})"
  fi
else
  bad "C1: a diff-scoped run exited non-zero — a SKIP must never fail the verdict"
fi
if grep -q 'scope   : SKYGATE_GATE_ONLY=' "$TMPD/only.log"; then
  ok "C2: the summary states the scope, so a short run cannot be mistaken for a full one"
else
  bad "C2: a filtered run does not say it was filtered"
fi

# --- D: the timing surface ----------------------------------------------------
SKYGATE_GATE_ONLY="B9" SKYGATE_GATE_TIMINGS_OUT="$TMPD/timings.tsv" bash "$GATE" >"$TMPD/timed.log" 2>&1
if grep -q 'slowest checks' "$TMPD/timed.log"; then
  ok "D1: the summary prints the slowest checks"
else
  bad "D1: the summary has no timing surface — 'the gate is slow' stays folklore"
fi
if [ -s "$TMPD/timings.tsv" ] && grep -qE '^[0-9]+' "$TMPD/timings.tsv"; then
  ok "D2: SKYGATE_GATE_TIMINGS_OUT dumps per-check ms as TSV ($(wc -l <"$TMPD/timings.tsv" | tr -d ' ') rows)"
else
  bad "D2: the TSV timing dump is missing or empty"
fi

# --- E: the measured decision is recorded, and the pool is not coming back ----
if grep -q 'The parallel pool was built, measured and REMOVED' "$GATE"; then
  ok "E1: the catalog records that the pool was measured and removed (with the numbers)"
else
  bad "E1: the catalog does not record the measurement — the next reader would rebuild the pool"
fi
if grep -qE '119 min|2\.4x|6 workers' "$GATE"; then
  ok "E2: the numbers are in the banner, not just the conclusion"
else
  bad "E2: the banner states no measurement"
fi
if grep -q 'gate_jobs_mode\|gate_enqueue\|gate_worker' "$GATE" 2>/dev/null; then
  bad "E3: a worker pool is wired back into the catalog without a new measurement"
else
  ok "E3: no worker pool in the catalog (the measured decision stands)"
fi
for k in SKYGATE_GATE_ONLY SKYGATE_GATE_TIMINGS_OUT gate_now_ms 'slowest checks'; do
  if grep -qF "$k" "$GATE"; then ok "E4: the catalog carries '$k'"; else bad "E4: the catalog is missing '$k'"; fi
done
if grep -q 'gate_affected.sh' "$DOC" && grep -q 'SKYGATE_GATE_ONLY' "$DOC"; then
  ok "E5: $DOC documents the diff-scoped runner and the filter"
else
  bad "E5: $DOC does not document the fast path"
fi
if grep -q 'run_check "B413"' "$GATE"; then
  ok "E6: B413 is registered in the catalog"
else
  bad "E6: B413 is not registered — this contract would never run"
fi
if grep -q '\*\*B413\*\*' AGENTS.md; then
  ok "E7: AGENTS.md block index carries B413 (rule 2)"
else
  bad "E7: AGENTS.md has no B413 entry"
fi

printf '\n\033[1mB413 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
