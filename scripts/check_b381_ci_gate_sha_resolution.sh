#!/usr/bin/env bash
# check_b381_ci_gate_sha_resolution.sh
#
# 2026-10-13 (B381, v1.5.120) — "the CI gate must not answer 'this commit was
# never tested' about a commit it simply could not look up".
#
# WHY
# ---
# Measured while releasing v1.5.119. The push was green (428/0), `ci.yml` was
# running for 124f54ef…, and
#
#     bash scripts/ci_gate.sh --sha 124f54ef
#
# printed
#
#     ci_gate: NOT GREEN — no 'ci.yml' run found for 124f54ef
#              (was the commit pushed with --no-verify, or is CI disabled for it?)
#
# The commit HAD been tested and its run was in progress. `scripts/ci_gate.sh`
# passes `--sha` straight into the GitHub query
# `repos/<repo>/actions/workflows/ci.yml/runs?head_sha=<sha>`, and that filter
# compares the FULL 40-character value: a prefix matches NOTHING, and an empty
# list is then reported with `total -eq 0` as "the commit was never tested".
# The verdict fails safe (it refuses, it never publishes), but it is WRONG and
# it sends the operator to look for a `--no-verify` push that never happened —
# the same class of misleading-verdict defect B378 fixed on the push gate.
#
# IMPACT (measured, so the severity is stated rather than assumed)
# ---------------------------------------------------------------
# The two sanctioned callers already pass a full sha — `.githooks/pre-tag`
# receives `$2` from git and `.github/workflows/tag-release.yml` runs
# `git rev-parse HEAD` — so tagging was never at risk. The defect is confined
# to a hand-invoked `--sha <prefix>`, which is exactly how an operator checks
# "was the commit I just pushed tested?".
#
# WHAT B381 SHIPS
#   * scripts/ci_gate.sh canonicalises a short sha through the local object
#     database (`git rev-parse --verify --quiet "<sha>^{commit}"`) BEFORE the
#     ancestry check and the API query, so a prefix that names a real commit is
#     looked up as that commit.
#   * A sha that does NOT resolve is reported as a limitation of the QUERY
#     ("the head_sha filter needs the full 40-character sha"), never as a fact
#     about CI. Exit codes are unchanged: 0 green, 1 not green, 2 cannot verify.
#
# CONTRACTS
#   A. source: resolution exists and runs BEFORE the ancestry check + query
#   B. behaviour: a short sha is looked up as the FULL sha (stubbed gh records
#      the URL it was handed) — this is the mutation tooth: delete the
#      canonicalisation and B1/B2/B3 go red
#   C. behaviour: a full sha is passed through verbatim (no re-resolution)
#   D. behaviour: an unresolvable short sha names the 40-character requirement
#      instead of blaming a `--no-verify` push, and still exits 1
#   E. the sanctioned callers still pass a full sha (the fix must not be the
#      thing that keeps tagging honest)
#   F. docs + registration + tracked by git
#
# NOTE ON STYLE: this check uses only bash + git + a stubbed `gh`. It never
# touches the network, and every scratch file lives under `mktemp -d` (trap
# #13: a check must not write a fixed /tmp path).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B381: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

export LC_ALL=C

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE=scripts/ci_gate.sh
CATALOG=scripts/verify_pre_deploy.sh
ROOT="$PWD"
SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b381.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

hdr "B381 (v1.5.120) — the CI gate resolves the sha it is asked about"

# --- A: the fix is in the source, and in the RIGHT ORDER ---------------------
if [ -f "$GATE" ]; then
  ok "A1: $GATE exists"
else
  bad "A1: $GATE is missing"
  printf '\n\033[1mB381 summary: %d passed, %d failed, %d skipped\033[0m\n' "$PASS" "$FAIL" "$SKIP"
  exit 1
fi

if grep -Fq 'rev-parse --verify --quiet "${SHA}^{commit}"' "$GATE"; then
  ok "A2: a short sha is canonicalised through git (rev-parse … ^{commit})"
else
  bad "A2: $GATE does not canonicalise the sha it was given — a prefix reaches the head_sha filter and matches nothing"
fi

# Order: resolution must precede BOTH the ancestry check and the API query.
# The patterns name the CODE, not the prose: the header comment quotes both the
# filter and `merge-base --is-ancestor`, and matching the comment would measure
# the documentation instead of the behaviour.
line_resolve="$(grep -n 'rev-parse --verify --quiet' "$GATE" | head -1 | cut -d: -f1)"
line_ance="$(grep -n 'if ! git merge-base --is-ancestor' "$GATE" | head -1 | cut -d: -f1)"
line_query="$(grep -n 'head_sha=\$SHA' "$GATE" | head -1 | cut -d: -f1)"
if [ -n "$line_resolve" ] && [ -n "$line_ance" ] && [ -n "$line_query" ] \
   && [ "$line_resolve" -lt "$line_ance" ] && [ "$line_resolve" -lt "$line_query" ]; then
  ok "A3: the resolution runs before the ancestry check ($line_resolve < $line_ance) and before the API query ($line_resolve < $line_query)"
else
  bad "A3: resolution order is wrong (resolve=$line_resolve ancestry=$line_ance query=$line_query) — resolving after the query would change nothing"
fi

# The query must still be keyed on the (now canonical) $SHA, not on a copy.
if grep -q 'runs?head_sha=\$SHA' "$GATE"; then
  ok "A4: the query is keyed on \$SHA, so the canonical value is what GitHub sees"
else
  bad "A4: the query no longer uses \$SHA (the canonical value would be ignored)"
fi

# The same principle one step earlier: an unresolvable commit must not be
# reported as "not an ancestor of origin/main" — that verdict is built out of
# our own missing object, not out of a fact about the commit.
if grep -q 'not in this repository.s object database' "$GATE"; then
  ok "A5: an unresolvable commit is a LOOKUP failure (exit 2), not an ancestry verdict"
else
  bad "A5: an unresolvable commit is still converted into an ancestry claim"
fi

# --- stub gh -----------------------------------------------------------------
STUB="$SKY_TMP/stub"; mkdir -p "$STUB/bin"
cat > "$STUB/bin/gh" <<'STUBEOF'
#!/usr/bin/env bash
# Minimal `gh` stand-in: records the argv of every `api` call, then answers with
# the tab-separated rows ci_gate.sh parses (id, status, conclusion, event, url).
case "${1:-}" in
  auth) exit 0 ;;
  api)
    printf '%s\n' "$*" >> "${STUB_LOG:?STUB_LOG must be set}"
    case "${STUB_MODE:-green}" in
      green) printf '%s\n' $'101\tcompleted\tsuccess\tpush\thttps://example.invalid/101' ;;
      none)  : ;;
      *)     exit 1 ;;
    esac
    exit 0 ;;
esac
exit 0
STUBEOF
chmod +x "$STUB/bin/gh" 2>/dev/null || true

# A real repository with a real commit, so `git rev-parse <prefix>` resolves the
# way it does on the operator's machine.
REPO_DIR="$SKY_TMP/repo"; mkdir -p "$REPO_DIR"
if git -C "$REPO_DIR" init -q 2>/dev/null \
   && git -C "$REPO_DIR" config user.email t@example.invalid \
   && git -C "$REPO_DIR" config user.name t; then
  echo a > "$REPO_DIR/a"
  git -C "$REPO_DIR" add . >/dev/null 2>&1
  git -C "$REPO_DIR" commit -qm one >/dev/null 2>&1
  FULL="$(git -C "$REPO_DIR" rev-parse HEAD)"
  SHORT="$(git -C "$REPO_DIR" rev-parse --short=8 HEAD)"
  git -C "$REPO_DIR" update-ref refs/remotes/origin/main "$FULL"
else
  FULL=""; SHORT=""
  skip "the scratch repository could not be created — behaviour contracts skipped"
fi

run_gate() { # <sha> <stub-mode> <out-file> [extra gate args…] ; echoes the exit code
  local sha="$1" mode="$2" out="$3"; shift 3
  local rc=0
  ( cd "$REPO_DIR" && PATH="$STUB/bin:$PATH" STUB_MODE="$mode" STUB_LOG="$SKY_TMP/url.log" \
      bash "$ROOT/$GATE" --repo example/repo --sha "$sha" --quiet "$@" >"$out" 2>&1 ) || rc=$?
  printf '%s' "$rc"
}

if [ -n "$FULL" ]; then
  # --- B: a short sha is looked up as the FULL sha (the mutation tooth) -------
  # No --allow-off-main here ON PURPOSE: the ancestry check must also benefit,
  # which only happens because the resolution runs first (contract A3).
  : > "$SKY_TMP/url.log"
  rc="$(run_gate "$SHORT" green "$SKY_TMP/b1.out")"
  if [ "$rc" = "0" ]; then
    ok "B1: a short sha of a real commit gates green (exit 0) — the whole point: it was never an untested commit"
  else
    bad "B1: a short sha of a real commit exited $rc, want 0 — output: $(tr '\n' ' ' < "$SKY_TMP/b1.out")"
  fi
  if grep -q "$FULL" "$SKY_TMP/url.log" 2>/dev/null; then
    ok "B2: the head_sha filter received the FULL sha $FULL"
  else
    bad "B2: the head_sha filter did not receive the full sha — it was handed: $(tr '\n' ' ' < "$SKY_TMP/url.log" 2>/dev/null)"
  fi
  if grep -qE "head_sha=$SHORT&" "$SKY_TMP/url.log" 2>/dev/null; then
    bad "B3: the SHORT sha $SHORT reached the API — GitHub matches nothing for a prefix, which is the false 'never tested' verdict"
  else
    ok "B3: the short sha never reaches the API"
  fi

  # --- C: a full sha is passed through verbatim ------------------------------
  : > "$SKY_TMP/url.log"
  rc="$(run_gate "$FULL" green "$SKY_TMP/c1.out")"
  if [ "$rc" = "0" ] && grep -q "$FULL" "$SKY_TMP/url.log" 2>/dev/null; then
    ok "C1: a full sha is passed through unchanged and gates green"
  else
    bad "C1: a full sha exited $rc with url log: $(tr '\n' ' ' < "$SKY_TMP/url.log" 2>/dev/null)"
  fi

  # --- D: an unresolvable short sha must not blame a --no-verify push --------
  # `--allow-off-main` because with the ancestry check ON the answer is (now,
  # correctly) "cannot verify" — this contract is about what the API path says.
  : > "$SKY_TMP/url.log"
  rc="$(run_gate "deadbee" none "$SKY_TMP/d1.out" --allow-off-main)"
  if [ "$rc" = "1" ]; then
    ok "D1: an unresolvable short sha still refuses (exit 1) — the verdict never softens"
  else
    bad "D1: an unresolvable short sha exited $rc, want 1"
  fi
  if grep -q '40-character' "$SKY_TMP/d1.out" 2>/dev/null; then
    ok "D2: the message names the real cause (the head_sha filter needs the full 40-character sha)"
  else
    bad "D2: the message does not explain the lookup limitation — output: $(tr '\n' ' ' < "$SKY_TMP/d1.out")"
  fi
  if grep -q 'no-verify' "$SKY_TMP/d1.out" 2>/dev/null; then
    bad "D3: the message still sends the operator hunting for a --no-verify push that this defect, not they, produced"
  else
    ok "D3: the misleading 'was the commit pushed with --no-verify?' diagnosis is not reached for an unresolvable short sha"
  fi

  # A full sha this clone does not have is a LOOKUP failure, not a claim that
  # the commit is off-branch — the two were indistinguishable before B381.
  : > "$SKY_TMP/url.log"
  rc="$(run_gate "0123456789abcdef0123456789abcdef01234567" green "$SKY_TMP/d4.out")"
  if [ "$rc" = "2" ]; then
    ok "D4: a commit this clone cannot resolve → exit 2 (cannot verify), the verdict callers must refuse on"
  else
    bad "D4: an unresolvable full sha exited $rc, want 2 — output: $(tr '\n' ' ' < "$SKY_TMP/d4.out")"
  fi
  if grep -q 'not an ancestor' "$SKY_TMP/d4.out" 2>/dev/null; then
    bad "D5: the gate claimed the unresolvable commit is 'not an ancestor of origin/main' — a verdict built from our own missing object"
  else
    ok "D5: the gate does not turn a missing object into an ancestry claim"
  fi
else
  skip "B/C/D: behaviour contracts skipped (no scratch repository)"
fi

# --- E: the sanctioned callers pass a FULL sha -------------------------------
if grep -q 'rev-parse HEAD' .github/workflows/tag-release.yml \
   && grep -q 'ci_gate.sh' .github/workflows/tag-release.yml; then
  ok "E1: tag-release.yml resolves the full commit before calling the gate"
else
  bad "E1: tag-release.yml no longer passes a full sha to the gate"
fi
if grep -q -- '--sha "\$sha1"' .githooks/pre-tag; then
  ok "E2: the pre-tag hook passes the sha git handed it"
else
  bad "E2: the pre-tag hook no longer passes a sha to the gate"
fi

# --- F: docs + registration + tracked by git (trap #11) ----------------------
for f in "$GATE" scripts/check_b381_ci_gate_sha_resolution.sh; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    ok "F1: $f is tracked by git (trap #11 — .gitignore can eat a new script silently)"
  else
    bad "F1: $f is NOT tracked by git"
  fi
done

if grep -q 'check_b381_ci_gate_sha_resolution.sh' "$CATALOG"; then
  ok "F2: registered in $CATALOG"
else
  bad "F2: not registered in $CATALOG (rule 2: a feature ships with a registered contract)"
fi

if grep -q 'B381' RELEASE-NOTES.md 2>/dev/null; then
  ok "F3: RELEASE-NOTES.md carries the B381 entry"
else
  bad "F3: RELEASE-NOTES.md has no B381 entry"
fi

if grep -q 'B381' docs/ROADMAP.md 2>/dev/null; then
  ok "F4: docs/ROADMAP.md records B381"
else
  bad "F4: docs/ROADMAP.md does not mention B381"
fi

# --- G: the catalog registration itself cannot be silently broken ------------
# B381 paid a full 55-minute gate run for its OWN registration: the
# `run_check "B381" "…"` description line was missing the trailing ` \`, so bash
# called run_check with two arguments. The catalog's runtime guard did its job —
# it printed CATALOG ERROR, counted the check as FAIL and the push was REFUSED
# (the safety net works) — but an hour of wall clock is a silly price for a
# character, and the failure is visible in the file in three seconds.
hdr "G. the catalog's own registration cannot be silently broken"
# A description-only entry ends at its closing quote ONLY when it forgot the
# continuation: with the backslash the line ends in ` \`. A COMPLETE one-liner
# also ends in a quote (`run_check "B1" "desc" "$cmd"`), so the discriminator is
# the number of unescaped double quotes — FOUR is `"id"` + `"description"` and
# nothing else, which is exactly the broken shape (`"$name" "$desc" "$@"` and a
# one-liner both carry six). Escaped quotes (\") inside a description are
# stripped first, so they cannot mask it.
G_AWK='/^[[:space:]]*run_check[[:space:]]/ {
  probe = $0
  gsub(/\\"/, "", probe)
  n = gsub(/"/, "", probe)
  line = $0
  sub(/[[:space:]]+$/, "", line)
  if (n == 4 && substr(line, length(line)) == "\"") { printf "%d: %s\n", NR, $0 }
}'
bad_lines="$(awk "$G_AWK" "$CATALOG" 2>/dev/null || true)"
if [ -z "$bad_lines" ]; then
  ok "G1: every run_check entry in $CATALOG passes a command argument (no description line ends at its closing quote)"
else
  bad "G1: $(printf '%s\n' "$bad_lines" | wc -l) run_check line(s) end at the closing quote, so the command is a separate statement — the catalog reports CATALOG ERROR and counts a FAIL: $(printf '%s' "$bad_lines" | head -2 | tr '\n' ' ')"
fi

# Teeth: the detector must flag a planted violation and leave the correct
# shapes alone — a one-liner with the command on the same line, and a
# description-only line that keeps its backslash.
cat > "$SKY_TMP/planted.sh" <<'PLANTEDEOF'
run_check "B997" "a one-line entry with the command inline" "go test ./..."
run_check "B998" "a description that forgets its continuation"
  'test -f scripts/check_b998.sh && bash scripts/check_b998.sh'
run_check "B999" "a description that keeps it" \
  'test -f scripts/check_b999.sh && bash scripts/check_b999.sh'
run_check "B996" "a description with an escaped \"quote\" in it" \
  'test -f scripts/check_b996.sh && bash scripts/check_b996.sh'
PLANTEDEOF
planted_hits="$(awk "$G_AWK" "$SKY_TMP/planted.sh" 2>/dev/null || true)"
if [ "$(printf '%s\n' "$planted_hits" | grep -c 'B998' || true)" = "1" ] \
   && [ "$(printf '%s\n' "$planted_hits" | wc -l)" = "1" ]; then
  ok "G2: the detector flags the planted violation and nothing else (1 of 4 synthetic entries)"
else
  bad "G2: the detector flagged $(printf '%s' "$planted_hits" | tr '\n' ' ') — want exactly the B998 entry, so it cannot be trusted"
fi

if bash -n "$CATALOG" 2>/dev/null; then
  ok "G3: $CATALOG is still syntactically valid (bash -n)"
else
  bad "G3: $CATALOG does not parse (bash -n)"
fi

printf '\n\033[1mB381 summary: %d passed, %d failed, %d skipped\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
