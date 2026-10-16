#!/usr/bin/env bash
# check_b388_grep_q_pipeline.sh
#
# 2026-10-15 (B388, closes TD-32) — THE PIPELINE'S VERDICT MUST BE GREP'S, NOT THE PRODUCER'S.
#
# THE CLASS, DECOMPOSED BY MEASUREMENT
# ------------------------------------
# `producer | grep -q pat` under `set -o pipefail` is wrong in TWO different ways, and they need
# two different remedies. TD-32 carried this as one item; measuring the 333 piped `grep -q`
# sites in scripts/ separated them:
#
#   H1 — SIGPIPE. `grep -q` exits at the FIRST match and closes the pipe. A producer still
#        writing dies with SIGPIPE (141), and `pipefail` then reports the PIPELINE as failed —
#        so a pattern that IS present reads as absent. Needs: a producer larger than the 64 KB
#        pipe buffer AND a pattern that can match early. B387 failed its own first CI run this
#        way (`code_only "$VP" | grep -qF …` over a ~110 KB file; MSYS passed, Linux did not).
#
#   H2 — POLARITY. Even with a small producer, `pipefail` makes a FAILING producer fail the
#        pipeline. For an OUTPUT-PRESENCE test that INVERTS the verdict: `go build 2>&1 |
#        grep -q .` asks "did the tool print anything?", a broken build is exactly what prints,
#        and the pipeline then reported failure-of-the-wrong-kind — so the "build is broken"
#        branch was SKIPPED and the check passed a broken tree.
#
# THE FIXES
# ---------
#   H1+H2 together are removed by neutralising pipefail for that one pipeline:
#       ( set +o pipefail; producer | grep -q pat )
#   The subshell keeps the pipeline's shape, so grep still decides and the exit status is
#   unchanged (0 matched / 1 not / 2 error), while the producer's status — whether 141 from
#   SIGPIPE or 1 from a failing tool — can no longer decide anything.
#
#   An equivalent local remedy exists for H1 alone (drop `-q` and redirect the grep's stdout:
#   plain `grep` reads ALL of its input, so the producer is never killed). It is proved
#   equivalent below, but it is NOT applied tree-wide: doing that mechanically needs a real
#   shell parser, and the obvious regex/tokenizer approaches corrupt text INSIDE quoted strings
#   (`"$( … sed 's/…"[^"]*"…' … )"` is re-parsed by the shell, which no naive tokenizer models —
#   the first attempt here mangled message strings and B386's own contract C-section before it
#   was caught by diffing against HEAD). The wrapper needs no parsing at all, so it is what
#   shipped.
#
#   THE 15 SITES THAT WERE ACTUALLY DANGEROUS (measured: large producer + early-match pattern):
#     check_b237_22.sh:299,306,314   check_b237_24.sh:214,219
#     check_b_modules_admin.sh:82,87 check_b_tailscale_module.sh:102,109
#     check_b360_prefix_reservation.sh:193,196   check_b378_push_gate_and_host_artefacts.sh:210
#     check_b95.sh:161,165           verify_backup.sh:83
#   The other ~318 sites are SAFE, and the reason is recorded rather than assumed: either the
#   producer is bounded (a 3-byte `curl -w '%{http_code}'`, a docker name list, `grep -A<n>`
#   over one file, `head`), or the pattern can only match the LAST line (`go test | grep -q
#   '^ok'`), which forces grep to read everything before it can answer.
#
# CONTRACTS
#   A. the danger predicate is EMPTY across scripts/ (B386's detector, widened by B388), and
#      the 15 fixed sites carry the wrapper
#   B. BEHAVIOURAL: with a deliberately broken Go package, the old form answers "nothing was
#      printed" (the bug) and the wrapped form answers "something was printed" (the fix);
#      and with a producer larger than the pipe buffer, pipefail turns a present pattern into
#      a failed pipeline while the wrapper does not
#   C. registered, tracked, documented
#
# Scratch files live under mktemp -d (AGENTS trap #13). The lines that must deliberately be
# spelled as the OLD form build their pipeline from a variable, so this file does not itself
# contain a danger shape — B386's detector scans `scripts/*.sh`, which includes this file.

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B388: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b388.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

hdr "B388 — the pipeline's verdict must be grep's, not the producer's"

# ---------------------------------------------------------------------------
# A. the danger predicate is empty, and the fixes are in place
# ---------------------------------------------------------------------------
# B386 owns the detector (widened by this block to the danger predicate: a producer that can
# outrun the pipe buffer AND a pattern that can match early, plus the printf/echo-of-a-variable
# class). Asserting it here by RUNNING it keeps one implementation.
b386_out="$(bash scripts/check_b386_no_variable_grep_q.sh 2>&1)"
b386_rc=$?
# Captured first, matched with a builtin: `printf … | grep -q` would be a danger shape IN THIS
# FILE, and B386's detector scans scripts/*.sh (this file included) — it flagged exactly this
# line before the fix, which is the detector doing its job.
b386_a1="$(printf '%s\n' "$b386_out" | sed -e 's/\x1b\[[0-9;]*m//g' | grep -E '^(FAIL|PASS) +A1|^  (FAIL|PASS) A1' || true)"
case "$b386_a1" in
  *"PASS A1: no unprotected"*)
    ok "A1: the danger predicate is empty across scripts/ (B386's detector, widened by B388: large producer + early-match pattern, and printf/echo of a variable)" ;;
  *)
    bad "A1: unprotected danger sites remain (B386 A1 did not pass; rc=$b386_rc)"
    printf '%s\n' "$b386_out" | sed -e 's/\x1b\[[0-9;]*m//g' | grep -E 'A1:|^        scripts/' | head -10 >&2 ;;
esac

# A2: the 15 sites that were measured dangerous carry the wrapper. Pinned BY NAME so a later
# edit cannot quietly remove one of them.
MISSING=""
while IFS=: read -r f pat; do
  [ -n "$f" ] || continue
  if ! grep -qF "$pat" "$f"; then
    MISSING="${MISSING}${f}: ${pat}"$'\n'
  fi
done <<'SITES'
scripts/check_b237_22.sh:( set +o pipefail; go build ./... 2>&1 | grep -q .
scripts/check_b237_22.sh:( set +o pipefail; go vet ./... 2>&1 | grep -q .
scripts/check_b237_22.sh:( set +o pipefail; staticcheck ./internal/feature/exit_rules/... 2>&1 | grep -E 'cdn_group' | grep -q .
scripts/check_b237_24.sh:( set +o pipefail; "$GO_BIN" build ./... 2>&1 | grep -q .
scripts/check_b237_24.sh:( set +o pipefail; "$GO_BIN" vet ./... 2>&1 | grep -q .
scripts/check_b_modules_admin.sh:( set +o pipefail; "$GO_BIN" build ./... 2>&1 | grep -q .
scripts/check_b_modules_admin.sh:( set +o pipefail; "$GO_BIN" vet ./... 2>&1 | grep -q .
scripts/check_b_tailscale_module.sh:( set +o pipefail; "$GO_BIN" build ./internal/module/tailscale/... 2>&1 | grep -q .
scripts/check_b_tailscale_module.sh:( set +o pipefail; "$GO_BIN" vet ./internal/module/tailscale/... 2>&1 | grep -q .
scripts/check_b360_prefix_reservation.sh:( set +o pipefail; grep -rn 'SetForceRelay(d, "")' --include='*.go' internal/ | grep -v '_test.go' | grep -q .
scripts/check_b360_prefix_reservation.sh:( set +o pipefail; grep -rn 'DeleteGlobalSetting(d, forceRelaySettingKey)' --include='*.go' internal/ | grep -q .
scripts/check_b378_push_gate_and_host_artefacts.sh:if ! ( set +o pipefail; gofmt -l "$f" 2>/dev/null | grep -q .
scripts/check_b95.sh:( set +o pipefail; git ls-remote origin 'refs/heads/feature/telegram-bot-ux' 2>/dev/null | grep -q .
scripts/check_b95.sh:( set +o pipefail; git ls-remote origin 'refs/heads/feat/postgres-migration' 2>/dev/null | grep -q .
scripts/verify_backup.sh:( set +o pipefail; find "$TMPDIR" -name 'skygate.db' -type f | grep -q .
SITES
if [ -z "$MISSING" ]; then
  ok "A2: all 15 measured-dangerous sites carry '( set +o pipefail; … )' (pinned by name)"
else
  bad "A2: a site that was measured dangerous lost its wrapper:"
  printf '%s' "$MISSING" | sed 's/^/        /' >&2
fi

# ---------------------------------------------------------------------------
# B. behavioural proofs (the toolchain decides, not a source grep)
# ---------------------------------------------------------------------------
hdr "B — behavioural: the two hazards, reproduced and fixed"

GO_BIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GO_BIN" ] && [ -x "/c/Program Files/Go/bin/go.exe" ]; then
  GO_BIN="/c/Program Files/Go/bin/go.exe"
fi

if [ -z "$GO_BIN" ]; then
  skip "B1: no 'go' on PATH — the H2 polarity proof needs a real compiler"
else
  mkdir -p "$SKY_TMP/broken"
  cat > "$SKY_TMP/broken/go.mod" <<'GOMOD'
module broken

go 1.21
GOMOD
  # A package that CANNOT compile: the compiler will print to stderr, which `2>&1` forwards
  # into the pipe — exactly the case that "did it print anything?" must answer YES for.
  cat > "$SKY_TMP/broken/broken.go" <<'GOBAD'
package broken

func Broken() int { return "not an int" }
GOBAD

  # The two shapes live in FIXTURE SCRIPTS written through a heredoc, exactly like B386's
  # planted detector file: a heredoc body is data, so this check does not itself contain a
  # danger shape (B386's detector scans scripts/*.sh, which includes this file), and the
  # fixture is the OLD shape VERBATIM — spelled as the checks spelled it.
  cat > "$SKY_TMP/old_shape.sh" <<'OLDSHAPE'
#!/usr/bin/env bash
set -o pipefail
if (cd "$1" && "$2" build ./... 2>&1) | grep -q .; then
  echo SAW_OUTPUT
else
  echo SAW_NOTHING
fi
OLDSHAPE
  cat > "$SKY_TMP/new_shape.sh" <<'NEWSHAPE'
#!/usr/bin/env bash
set -o pipefail
if ( set +o pipefail; (cd "$1" && "$2" build ./... 2>&1) | grep -q . ); then
  echo SAW_OUTPUT
else
  echo SAW_NOTHING
fi
NEWSHAPE
  OLD_SAID="$(bash "$SKY_TMP/old_shape.sh" "$SKY_TMP/broken" "$GO_BIN" 2>/dev/null)"
  NEW_SAID="$(bash "$SKY_TMP/new_shape.sh" "$SKY_TMP/broken" "$GO_BIN" 2>/dev/null)"

  # The old shape says "nothing printed" for a build that PRINTS ITS ERRORS — that is the bug:
  # the caller's `then` branch (report the failure) is skipped and a broken tree passes.
  if [ "$OLD_SAID" = "SAW_NOTHING" ]; then
    ok "B1a: the OLD shape answers 'nothing was printed' for a broken build — which is why 9 checks passed a broken tree (polarity inverted by pipefail)"
  else
    bad "B1a: the old shape said '$OLD_SAID'; the H2 inversion did not reproduce"
  fi
  if [ "$NEW_SAID" = "SAW_OUTPUT" ]; then
    ok "B1b: the WRAPPED shape answers 'something was printed' for the same broken build — the failure is now reported"
  else
    bad "B1b: the wrapped shape said '$NEW_SAID' for a broken build"
  fi
fi

# B2 — H1: a producer larger than the 64 KB pipe buffer, whose match is on the FIRST line.
big_producer() { i=0; while [ $i -lt 4000 ]; do printf 'line %s padding padding padding padding\n' "$i"; i=$((i+1)); done; }
H1_OLD=0 H1_NEW=0
if ( set -o pipefail; big_producer | grep -q 'line 0 ' ); then H1_OLD=0; else H1_OLD=$?; fi
if ( set +o pipefail; big_producer | grep -q 'line 0 ' ); then H1_NEW=0; else H1_NEW=$?; fi
if [ "$H1_OLD" -ne 0 ]; then
  ok "B2a: with pipefail, an early match in a >64 KB producer reports FAILURE (rc=$H1_OLD = SIGPIPE from the producer) although grep matched"
else
  skip "B2a: the host did not reproduce the SIGPIPE race this run (informational; timing-dependent)"
fi
if [ "$H1_NEW" -eq 0 ]; then
  ok "B2b: the wrapped form reports the MATCH for the same input (rc=0)"
else
  bad "B2b: the wrapped form reported rc=$H1_NEW — the match was not seen"
fi

# B3 — the H1-only remedy that was NOT applied tree-wide is nevertheless equivalent when used
# correctly; proving it documents why the wrapper was chosen over a mass `-q` removal.
equiv_bad=0
while IFS= read -r hs; do
  [ -n "$hs" ] || continue
  o=0 n=0
  printf '%s\n' "$hs" > "$SKY_TMP/h.txt"
  grep -q 'needle' "$SKY_TMP/h.txt" || o=$?
  grep 'needle' "$SKY_TMP/h.txt" >/dev/null || n=$?
  [ "$o" = "$n" ] || equiv_bad=$((equiv_bad+1))
done <<'HAYSTACKS'
needle
no match here
needle at the start
trailing needle

HAYSTACKS
if [ "$equiv_bad" -eq 0 ]; then
  ok "B3: 'grep -q pat file' and 'grep pat file >/dev/null' agree on the exit status for every haystack (the H1-only remedy is equivalent)"
else
  bad "B3: the two grep forms disagreed on $equiv_bad haystack(s)"
fi

# ---------------------------------------------------------------------------
# C. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "C — the block is wired in"

if git ls-files --error-unmatch scripts/check_b388_grep_q_pipeline.sh >/dev/null 2>&1; then
  ok "C1: this check script is tracked by git"
else
  bad "C1: scripts/check_b388_grep_q_pipeline.sh is not tracked by git"
fi
if grep -qF 'check_b388_grep_q_pipeline.sh' scripts/verify_pre_deploy.sh; then
  ok "C2: registered in scripts/verify_pre_deploy.sh (rule 2)"
else
  bad "C2: not registered in scripts/verify_pre_deploy.sh — the gate will never run it"
fi
if grep -qE '^- \*\*B388\*\*' AGENTS.md; then
  ok "C3: one-line index entry in AGENTS.md (rule 2)"
else
  bad "C3: no B388 entry in the AGENTS.md block index"
fi
if grep -qF 'B388' RELEASE-NOTES.md; then
  ok "C4: RELEASE-NOTES.md carries the B388 entry"
else
  bad "C4: RELEASE-NOTES.md has no B388 entry"
fi
if grep -qF 'TD-32' docs/ROADMAP.md; then
  ok "C5: docs/ROADMAP.md records TD-32 (the measured decomposition)"
else
  bad "C5: docs/ROADMAP.md does not mention TD-32"
fi
B388_DESC="$(grep -F 'run_check "B388"' scripts/verify_pre_deploy.sh || true)"
case "$B388_DESC" in
  *'`'*) bad "C6: the run_check description contains a backtick — it is evaluated by bash (TD-15)" ;;
  "")    bad "C6: no run_check \"B388\" line found" ;;
  *)     ok "C6: the run_check description carries no backtick (TD-15)" ;;
esac

echo
printf 'B388: %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
