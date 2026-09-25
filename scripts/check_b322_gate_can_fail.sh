#!/usr/bin/env bash
# check_b322_gate_can_fail.sh
#
# 2026-09-25 (B322) — the guarantee catalog must be ABLE TO FAIL.
#
# WHY THIS EXISTS. The operator reported that edits broke real behaviour (exit rules
# stopped working, device tags stopped reaching headscale, the applied ACL policy went
# stale) while `bash scripts/verify_pre_deploy.sh` stayed green, and asked for an audit of
# every B1–B320 check for regression-detection power. The audit found, and this block
# repaired, four MECHANICAL ways a contract could never fail:
#
#   1. 24 entries (B59–B81, B86, B89, B90) ended their command with `bash "$f"; rm -f
#      "$f"`, so the pipeline's exit status was `rm`'s (always 0) and the gate printed
#      PASS no matter what the chain found. The wrapper now captures `rc=$?` and exits
#      with it.
#   2. `scripts/check_b251.sh` counted nothing and never exited non-zero, so its 13
#      contracts (the reserved tailnet name!) were decorative.
#   3. `go test -run <PATTERN>` filters pointing at tests that no longer exist print
#      `ok … [no tests to run]` and exit 0. Five such filters were live.
#   4. Check scripts could be referenced by NO catalog at all, so they simply never ran.
#
# The contracts below make each of those classes FAIL the gate when it comes back.
#
# CONTRACTS
#   A. no registered command may discard the exit status of the check it runs
#   B. every check script that records a FAIL must be able to exit non-zero
#   C. every `go test -run <PATTERN>` in the catalog must match at least one real test
#   D. every check script must be reachable from some catalog (gate, post-deploy, CI,
#      hook, Makefile) — directly or through another reachable script
#   E. no registered command may be a no-op (`echo`/`true`/`:`)
#   F. the gate must print a failing check's own FAIL lines, not only its first 20 lines
#   G. the repairs of this block are pinned (the wrapper form + the B251 counter)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B322: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE=scripts/verify_pre_deploy.sh

hdr "B322 — the guarantee catalog must be able to fail"

# --- A: no command may discard the exit status of what it tests ----------------------
A1=$(grep -c 'bash "\$f"; rm -f "\$f"' "$GATE" 2>/dev/null || true)
if [ "${A1:-0}" = "0" ]; then
  ok "A1: no masked printf|bash chain is left (the wrapper propagates rc)"
else
  bad "A1: $A1 chain(s) still end with \`bash \"\$f\"; rm -f \"\$f\"\` — their verdict is rm's exit status (always 0)"
fi
A2=$(grep -c 'rc=\$?; rm -f "\$f"; exit \$rc' "$GATE" 2>/dev/null || true)
if [ "${A2:-0}" -ge 20 ]; then
  ok "A2: $A2 chains now capture and propagate the verdict (rc=\$? … exit \$rc)"
else
  bad "A2: only ${A2:-0} chains propagate their verdict — expected the whole repaired band"
fi
# A trailing `|| true` / `; true` makes every failure invisible in exactly the same way.
A3=$(grep -nE "run_check.*(\\|\\| true|; true)'" "$GATE" 2>/dev/null | wc -l | tr -d ' ')
if [ "${A3:-0}" = "0" ]; then
  ok "A3: no registered command ends with \`|| true\` / \`; true\`"
else
  bad "A3: $A3 registered command(s) end with a success-forcing tail:"
  grep -nE "run_check.*(\\|\\| true|; true)'" "$GATE" 2>/dev/null | cut -c1-140 | sed 's/^/       /' >&2
fi

# --- B: a check that records FAIL must be able to exit non-zero ----------------------
# The idioms in this repo are many (`exit $FAIL`, `[ "$FAIL" -eq 0 ] || exit 1`,
# `if [[ "${FAIL}" -gt 0 ]]; then exit 1; fi`, and a bare `[ "$FAIL" -eq 0 ]` as the last
# command), so the detector looks for ANY of them in the script's tail rather than
# insisting on one spelling.
can_fail() { # file → 0 when the script can exit non-zero on a recorded FAIL
  local f="$1" tail last
  # tr -d '\r': a Windows checkout hands us CRLF, and the `$` anchors below would then
  # never match (the same trap that makes gofmt -l list every file on Windows).
  tail="$(grep -vE '^[[:space:]]*(#|$)' "$f" | tail -14 | tr -d '\r')"
  last="$(tail -n 1 <<< "$tail")"
  grep -qE 'exit[[:space:]]+"?\$?\{?FAIL' <<< "$tail" && return 0
  grep -qE 'FAIL[^A-Za-z]*(-eq[[:space:]]*0|-ne[[:space:]]*0|-gt[[:space:]]*0|-ge[[:space:]]*1).*\|\|[[:space:]]*exit' <<< "$tail" && return 0
  grep -qE '\[\[?[[:space:]]*"?\$?\{?FAIL\}?"?[[:space:]]+-(ne|gt|ge)[[:space:]]*[01]' <<< "$tail" && return 0
  # A bare final `[ "$FAIL" -eq 0 ]` IS the script's exit status — normalise the trailing
  # `]`/spaces away before matching.
  local norm
  norm="$(sed -E 's/[][[:space:]]+$//' <<< "$last")"
  grep -qE '\$?\{?FAIL\}?"?[[:space:]]+-eq[[:space:]]*0$' <<< "$norm" && return 0
  return 1
}
NOEXIT=""
for f in scripts/check_b*.sh; do
  [ -f "$f" ] || continue
  grep -q 'FAIL=\$((FAIL+1))' "$f" 2>/dev/null || continue
  can_fail "$f" && continue
  NOEXIT="$NOEXIT $(basename "$f")"
done
if [ -z "$NOEXIT" ]; then
  ok "B1: every check script that counts failures can exit non-zero"
else
  bad "B1: check script(s) count failures but never exit non-zero (a green gate on a broken contract):$NOEXIT"
fi
# The same defect one level down: printing the summary and returning 0 unconditionally.
# Only lines that actually INTERPOLATE the failure counter count as a summary — a PASS
# message that merely contains the word "summary" (check_b108.sh) is not one.
MASKS=""
for f in scripts/check_b*.sh; do
  [ -f "$f" ] || continue
  line=$(grep -nE 'summary.*(\$\{?FAIL\}?|\$\{?fail\}?)' "$f" 2>/dev/null | tail -1 | cut -d: -f1)
  [ -n "$line" ] || continue
  next=$(sed -n "$((line+1))p" "$f" | tr -d '\r')
  case "$next" in *"exit 0"*) MASKS="$MASKS $(basename "$f")";; esac
done
if [ -z "$MASKS" ]; then
  ok "B2: no check script returns 0 unconditionally right after printing its summary"
else
  bad "B2: script(s) print a summary and then \`exit 0\` with recorded failures:$(printf ' %s' $MASKS)"
fi

# --- C: every go test -run filter must match a real test -----------------------------
if command -v go >/dev/null 2>&1 || [ -n "${GO:-}" ]; then
  IDX=/tmp/b322_test_index.$$ 
  grep -rhoE '^func (Test|Fuzz|Benchmark)[A-Za-z0-9_]*' --include='*_test.go' . 2>/dev/null \
    | sed -E 's/^func //' | sort -u > "$IDX"
  TOTAL=$(wc -l < "$IDX" | tr -d ' ')
  if [ "${TOTAL:-0}" -lt 100 ]; then
    skip "C1: the test-name index looks empty ($TOTAL names) — skipping the filter contract"
  else
    BADF=""
    while IFS= read -r p; do
      [ -n "$p" ] || continue
      grep -qE -- "$p" "$IDX" || BADF="$BADF $p"
    done < <(grep -hoE -- "-run[[:space:]]+('[^']*'|\"[^\"]*\"|[^[:space:]'\"]+)" "$GATE" scripts/verify_post_deploy.sh scripts/check_b*.sh 2>/dev/null \
      | sed -E "s/^-run[[:space:]]+//; s/^['\"]//; s/['\"]\$//" \
      | grep -E '^[A-Za-z0-9_|^$.*+?()]+$' | grep -E 'Test|Fuzz|Benchmark|^B[0-9]' | sort -u)
    if [ -z "$BADF" ]; then
      ok "C1: every go test -run filter in the catalog matches at least one real test ($TOTAL known)"
    else
      bad "C1: run filter(s) that match NO test (they print [no tests to run] and always pass):$BADF"
    fi
  fi
  rm -f "$IDX"
else
  skip "C1: go not on PATH — run this contract on the VM"
fi

# --- D: every check script must be reachable from a catalog --------------------------
TEXT=$(cat "$GATE" scripts/verify_post_deploy.sh 2>/dev/null)
for extra in Makefile .githooks/* .github/workflows/*.yml; do
  [ -f "$extra" ] && TEXT="$TEXT$(cat "$extra" 2>/dev/null)"
done
# One level of indirection: a script invoked by another catalogued script is reachable.
for f in scripts/check_b*.sh; do
  [ -f "$f" ] || continue
  b=$(basename "$f")
  case "$TEXT" in *"$b"*) TEXT="$TEXT$(cat "$f" 2>/dev/null)";; esac
done
ORPHANS=""
for f in scripts/check_b*.sh; do
  [ -f "$f" ] || continue
  b=$(basename "$f")
  case "$TEXT" in *"$b"*) ;; *) ORPHANS="$ORPHANS $b";; esac
done
if [ -z "$ORPHANS" ]; then
  ok "D1: every check script is reachable from a catalog"
else
  bad "D1: check script(s) referenced by NO catalog (they never run):$ORPHANS"
fi

# --- E: no registered command may be a no-op -----------------------------------------
NOOP=$(grep -nE "run_check(_slow)? +\"[A-Za-z0-9.]+\" +\"[^\"]*\" +'(echo|true|:)( |')" "$GATE" 2>/dev/null | wc -l | tr -d ' ')
if [ "${NOOP:-0}" = "0" ]; then
  ok "E1: no registered command is a bare echo/true/:"
else
  bad "E1: $NOOP registered command(s) do nothing:"
  grep -nE "run_check(_slow)? +\"[A-Za-z0-9.]+\" +\"[^\"]*\" +'(echo|true|:)( |')" "$GATE" 2>/dev/null | cut -c1-140 | sed 's/^/       /' >&2
fi

# --- F: a failing check's own FAIL lines must be printed -----------------------------
if grep -q "grep -aE 'FAIL" "$GATE" 2>/dev/null; then
  ok "F1: the gate surfaces a failing check's own FAIL lines (they used to fall outside head -20)"
else
  bad "F1: the gate still prints only the first 20 lines of a failing check — late FAILs stay invisible"
fi

# --- H: the real regressions the armed band exposed must stay fixed -------------------
# Two shared code paths used SQLite-only `INSERT OR IGNORE`, so on PostgreSQL (verified on
# PG 15) joining a mesh and granting a subnet share failed with
# `ERROR: syntax error at or near "OR"`. Both are now dialect-branched, and the B60 sweep
# that used to guard the class is re-established below WITH the branch allowance (a
# legitimately branched statement is not a leak).
if grep -q 'func meshMemberInsertSQL(backend db.Backend) string' internal/mesh/mesh.go \
   && grep -q 'func subnetShareInsertSQL(backend db.Backend) string' internal/subnet/shares.go; then
  ok "H1: both idempotent INSERTs are built by a dialect-aware helper"
else
  bad "H1: a shared path lost its dialect-aware INSERT helper (mesh join / subnet share will break on PG)"
fi
BADSQL=""
for f in $(grep -rlE 'INSERT OR IGNORE' --include='*.go' internal/ cmd/ 2>/dev/null \
           | grep -vE 'migrations_|on_conflict_|dialect\.go|_test\.go'); do
  # Only LIVE SQL counts — most occurrences of the phrase are comments explaining it.
  grep -vE '^[[:space:]]*(//|/\*|\*)' "$f" 2>/dev/null | grep -q 'INSERT OR IGNORE' || continue
  grep -q 'BackendOf(' "$f" 2>/dev/null || BADSQL="$BADSQL $f"
done
if [ -z "$BADSQL" ]; then
  ok "H2: every non-migration INSERT OR IGNORE is behind a backend branch"
else
  bad "H2: SQLite-only INSERT OR IGNORE on a shared (unbranched) path:$BADSQL"
fi
if command -v go >/dev/null 2>&1 || [ -n "${GO:-}" ]; then
  GOBIN="${GO:-go}"
  OUT="$( ("$GOBIN" test ./internal/mesh/ ./internal/subnet/ -run 'B322' -count=1) 2>&1 )"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "H3: the dialect-form tests for both helpers pass"
  else
    bad "H3: the dialect-form tests failed:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "H3: go not on PATH — run the dialect-form tests on the VM"
fi

# --- G: the repairs of this block are pinned -----------------------------------------
if grep -q 'FAIL=\$((FAIL+1))' scripts/check_b251.sh && grep -q '\[ "\$FAIL" -eq 0 \] || exit 1' scripts/check_b251.sh; then
  ok "G1: check_b251.sh counts failures and exits non-zero (it guarded the reserved name while being unable to fail)"
else
  bad "G1: check_b251.sh is decorative again"
fi
if git ls-files --error-unmatch scripts/check_b322_gate_can_fail.sh >/dev/null 2>&1; then
  ok "G2: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "G2: this script is NOT tracked by git"
fi

printf '\n\033[1mB322 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
