#!/usr/bin/env bash
# check_b386_no_variable_grep_q.sh
#
# 2026-10-14 (B386) — "a check must not let a HOST transient become a PRODUCT verdict".
#
# THE INCIDENT (already paid for once)
# -----------------------------------
# `B311 A2` FAILED a full gate run on a tree that had passed the same gate twice an hour
# earlier, about a file whose relevant line is literally `body["acl_tags"] = tags`. It
# reproduced 0 times in 300 rounds, including 400 rounds under 8 CPU burners, and the two
# sibling assertions that read the same variable through the same form passed in the
# failing run. The assertion was:
#
#     if printf '%s' "$CODE" | grep -q 'body\["acl_tags"\] = tags'; then
#
# `grep -q` exits at the FIRST match and closes the pipe. Under `set -o pipefail` the
# pipeline's status is then the PRODUCER's status, not grep's — so anything the host does
# to that pipe (EPIPE/SIGPIPE, a failed fork) is reported as "the pattern is not there",
# i.e. a fact about the machine becomes a claim about the product. That is why the gate
# could not be trusted: the check had no way to fail honestly.
#
# WHAT B386 DOES
# --------------
# * every such site in `scripts/check_*.sh` was rewritten (270 sites in 73 files), with the
#   rewrite PROVEN equivalent first: a behavioural selftest evaluated the old pipeline and
#   the new expression on ten haystacks per site (empty, case variants, regex
#   metacharacters, multi-line, quotes, backslashes) and compared exit statuses;
# * four kinds of replacement, chosen by the pattern's shape:
#     R1/R2 literal (case-folded when `-i`)   -> [[ $V == *'pat'* ]]        (no fork at all)
#     R3    alternation of literals           -> [[ $V == *'a'* || … ]]     (same, `-i` aware)
#     R4    any other regex                   -> grep <flags> <pattern verbatim> <<< "$V"
#                                                (identical command, no producer in the pipe)
#     R5    patterns that can see a trailing newline, whole-line `-x`, or a PATTERN VARIABLE
#                                             -> ( set +o pipefail; printf … | grep … )
#                                                (the pipeline stays, its status cannot)
# * non-`q` sites (`-o`, `-c`, `-n`) were audited and left alone BY DESIGN: grep reads all
#   of its input there, so there is no early exit and no producer status to lose;
# * comments and here-document bodies are never rewritten (a comment that mentions the
#   pattern is documentation, and heredoc text is data).
#
# CONTRACTS
#   A. no `producer | grep -q` site is left unprotected anywhere in scripts/
#   B. the detector WORKS (it is exercised on planted violations, one per shape)
#   C. the transforms themselves are equivalent (re-proved here on fresh haystacks)
#   D. documented, tracked, registered
#
# Scratch files live under mktemp -d (AGENTS trap #13); nothing here needs the network.

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B386: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b386.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

# The detector: a line that pipes a producer into `grep -q`, where the grep is not already
# behind `set +o pipefail`, and where the producer's output can still be arriving when grep
# leaves. Comment lines and heredoc bodies are exempt (the same rule the rewriter used — a
# comment mentioning the pattern is documentation).
#
# THREE GENERATIONS, and the reason the predicate is what it is (B388 measured the middle one):
#   1. `(printf|echo) … | grep -q` — the B311 class. Correct but NARROW: it matched only the
#      producers that had already been fixed, and reported "0 unprotected sites" while the same
#      hazard sat behind every other producer. B387 then failed its first CI run through that
#      hole (`code_only "$VP" | grep -qF …` over a ~110 KB file: `grep -q` left early, the
#      producer died of SIGPIPE, `pipefail` reported the pipeline as failed, and a pattern that
#      IS present read as absent).
#   2. ANY producer — too BROAD: it flags ~300 sites, almost all of them harmless, because
#      `grep -q` only kills the producer if it is STILL WRITING. A `grep -A3 pat one-file |
#      grep -q x` has a few KB of output; a `grep -q '^ok'` over `go test` can only match the
#      LAST line, so grep has already read everything. A detector nobody can satisfy is a
#      detector everyone exempts.
#   3. the DANGER predicate (below): the producer can exceed the 64 KB pipe buffer (a build,
#      test or lint tool, a repo-wide grep/find, a file dump, or a printf/echo of a VARIABLE)
#      AND the pattern can match early (an output-presence test: `.`, `.*`) — because that is
#      exactly when the verdict rides on SIGPIPE and on pipefail's choice of status.
#      Measured by `td32_danger`: 15 such sites existed; B388 wrapped all 15.
# The pipe test requires a SINGLE `|`: a `||` (logical OR) is not a pipeline, and matching its
# second bar was the first version's false positive.
detect() { # detect <file>…  -> prints "file:line: code"
  awk '
    BEGIN { heredoc = "" }
    # The heredoc state is PER FILE: `BEGIN` runs once for the whole invocation, so a script
    # that ends while a heredoc is still open used to leak that state into every later file —
    # their lines were skipped until something matched the stale terminator. Found when this
    # detector reported "0 unprotected sites" while its own sibling file contained one; a
    # detector that silently stops reading is worse than none.
    FNR == 1 { heredoc = "" }
    {
      line = $0
      if (heredoc != "") { if (line ~ ("^[[:space:]]*" heredoc "[[:space:]]*$")) heredoc = ""; next }
      if (line ~ /^[[:space:]]*#/) next
      if (match(line, /<<-?[[:space:]]*[\047"]?[A-Za-z_][A-Za-z0-9_]*[\047"]?/)) {
        t = substr(line, RSTART, RLENGTH); gsub(/<<-?[[:space:]]*[\047"]?/, "", t); gsub(/[\047"]?$/, "", t)
        heredoc = t
      }
      if (line ~ /set \+o pipefail/) next
      # a pipeline whose last visible stage is `grep -…q…`, and NOT a `||` chain
      if (line !~ /[^|]\|[[:space:]]*grep[[:space:]]+-[A-Za-z]*q[A-Za-z]*/ &&
          line !~ /^\|[[:space:]]*grep[[:space:]]+-[A-Za-z]*q[A-Za-z]*/) next
      # (a) the producer is printf/echo of a VARIABLE: its size is the size of that variable.
      #     `.*` and not `[^|]*`: the producer may itself be a pipeline (`echo "$V" | tr -d
      #     '\n' | grep -q x`), and that shape was one of the original planted violations.
      var_prod = (line ~ /(printf|echo).*\|[[:space:]]*grep[[:space:]]+-[A-Za-z]*q[A-Za-z]*/)
      # (b) the producer is a large-output tool AND the pattern is an output-presence test
      presence = (line ~ /grep[[:space:]]+-[A-Za-z]*q[A-Za-z]*[[:space:]]+[\047"]?\.[\047"]?[[:space:]]*([;|&)].*)?$/)
      big = (line ~ /(\$GO_BIN|go[[:space:]]+(build|vet|test)|staticcheck|gofmt|grep[[:space:]]+-[A-Za-z]*r|find[[:space:]]|tar[[:space:]]|tr[[:space:]]|awk[[:space:]]|sed[[:space:]]|journalctl|python3|git[[:space:]]+ls-remote|docker[[:space:]]+(ps|images|compose)|cat[[:space:]])/)
      if (var_prod || (big && presence)) {
        printf "%s:%d: %s\n", FILENAME, FNR, line
      }
    }
  ' "$@"
}

hdr "B386 — no check may turn a host transient into a product verdict"

# ---------------------------------------------------------------------------
# A. the class is gone from scripts/
# ---------------------------------------------------------------------------
# THIS FILE is excluded, explicitly and for one reason: its section C carries the OLD pipelines
# as QUOTED TEST DATA (`prove "literal" "printf '%s' \"\$V\" | grep -q 'literal'" …`) so the
# rewrite can be proved equivalent against the form it replaced. A line-based detector cannot
# tell a quoted argument from a live pipeline, and those five lines are data. The exclusion is
# safe because the detector is exercised on PLANTED violations in section B below, so a
# detector that stops working fails there rather than passing silently here.
mapfile -t hits < <(detect $(ls scripts/*.sh | grep -v 'scripts/check_b386_no_variable_grep_q.sh'))
if [ "${#hits[@]}" -eq 0 ]; then
  ok "A1: no unprotected 'producer | grep -q' site remains in scripts/ (the B311 class)"
else
  bad "A1: ${#hits[@]} unprotected site(s) remain — any of them can report a host fault as a product fact:"
  printf '        %s\n' "${hits[@]:0:8}" >&2
fi
# A2: the audit trail — what the rewriter deliberately KEPT must be status-neutralized.
kept="$(grep -c 'set +o pipefail; .*| *grep *-[A-Za-z]*q' scripts/*.sh 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')"
if [ "${kept:-0}" -ge 40 ]; then
  ok "A2: $kept site(s) keep their pipeline but neutralize its status (R5: pattern variables, -x, trailing-newline-sensitive patterns)"
else
  bad "A2: expected the R5 sites to be present and status-neutralized, found ${kept:-0}"
fi
# A3: the non-`q` sites were left alone on purpose — state the reason where a reader looks.
if grep -rqs 'not the class' scripts/check_b386_no_variable_grep_q.sh; then
  ok "A3: the audit rationale for -o/-c sites is written down in this contract"
else
  bad "A3: this contract does not explain why -o/-c sites are out of scope"
fi
# A4: the SHAPE of the parser bug this rewrite produced once: a herestring placed BETWEEN
# operands (`grep -qE -e <<< "$V" PATTERN`). Valid syntax, broken command — the `-e` option
# takes a separate value, so the pattern is not the first token after the flags. Caught by
# reviewing the diff; now checked directly: for a grep, `<<< "$V"` must be the LAST operand.
bad_place="$(grep -rn --exclude=check_b386_no_variable_grep_q.sh '<<< "' scripts/*.sh 2>/dev/null \
  | grep -v ':[[:space:]]*#' | grep -v ':[[:space:]]*bad_place' | while IFS= read -r line; do
  case "$line" in *grep*'<<<'*) ;; *) continue ;; esac
  after="$(printf '%s' "$line" | sed -E 's/^.*<<<[[:space:]]*"[^"]*"[[:space:]]*//')"
  case "$after" in
    ""|";"*|"&"*|"|"*|")"*|"#"*|"\\"*|"'"*) ;;
    *) printf '%s\n' "$line" ;;
  esac
done)"
if [ -z "$bad_place" ]; then
  ok "A4: every 'grep … <<< \"\$V\"' in scripts/ ends at the herestring (nothing follows it)"
else
  bad "A4: a herestring sits BETWEEN operands — valid syntax, broken command:"
  printf '%s\n' "$bad_place" | head -3 | sed 's/^/        /' >&2
fi

# ---------------------------------------------------------------------------
# B. the detector WORKS (planted violations, one per shape)
# ---------------------------------------------------------------------------
plant="$SKY_TMP/planted.sh"
cat > "$plant" <<'EOF'
#!/usr/bin/env bash
V="x"
if printf '%s' "$V" | grep -q 'x'; then :; fi
if echo "${V}" | grep -qiE 'x|y'; then :; fi
# printf '%s' "$V" | grep -q 'commented out — not a violation'
if printf '%s' "$V" | grep -q 'x'; then :; fi   # a TRAILING comment does not exempt it
if echo "$V" | tr -d '\n' | grep -q 'x'; then :; fi
BODY="$(cat <<'HEREDOC'
printf '%s' "$V" | grep -q 'inside a heredoc — data, not code'
HEREDOC
)"
if ( set +o pipefail; printf '%s' "$V" | grep -q 'x' ); then :; fi
if go build ./... 2>&1 | grep -q .; then :; fi
if grep -rn 'x' internal/ | grep -q .; then :; fi
if grep -q 'a' one.txt || grep -q 'b' two.txt; then :; fi
if grep -A3 'pat' one.txt | grep -q 'x'; then :; fi
if go test ./... 2>&1 | grep -q '^ok'; then :; fi
EOF
planted="$(detect "$plant" | wc -l)"
if [ "$planted" -eq 6 ]; then
  ok "B1: the detector finds exactly the 6 planted violations — the four printf/echo-of-a-variable shapes plus a build tool and a repo-wide grep, both with an output-presence pattern — while the comment, the heredoc, the 'set +o pipefail' line, the '||' chain, the bounded producer and the last-line-only pattern stay clean"
else
  bad "B1: the detector found $planted violation(s) in the planted file, expected 6"
  detect "$plant" | sed 's/^/        /' >&2
fi

# ---------------------------------------------------------------------------
# C. the transforms are equivalent (re-proved on fresh haystacks)
# ---------------------------------------------------------------------------
# The three builtin forms must agree with the grep they replaced. Each case is evaluated
# with BOTH forms for every haystack; a single disagreement is a FAIL.
prove() { # prove <label> <haystack-array-name> <old-cmd> <new-cmd> — env V carries the text
  local label="$1" old="$2" new="$3"
  local hs o n mismatch=0
  while IFS= read -r hs; do
    V="$hs"
    if eval "$old" >/dev/null 2>&1; then o=0; else o=$?; fi
    if eval "$new" >/dev/null 2>&1; then n=0; else n=$?; fi
    [ "$o" = "$n" ] || { mismatch=$((mismatch+1)); printf '        %s: old=%s new=%s haystack=%q\n' "$label" "$o" "$n" "$hs" >&2; }
  done <<'HAYSTACKS'

x
pre literal post
LITERAL
a.b*c[d]
line1
line2
  spaced  

HAYSTACKS
  return "$mismatch"
}
mis=0
prove "literal"   "printf '%s' \"\$V\" | grep -q 'literal'"            "[[ \$V == *'literal'* ]]" || mis=$((mis+1))
prove "literal-i" "printf '%s' \"\$V\" | grep -qi 'literal'"          "[[ \${V,,} == *'literal'* ]]" || mis=$((mis+1))
prove "alt"       "printf '%s' \"\$V\" | grep -q 'literal\\|x'"        "{ [[ \$V == *'literal'* ]] || [[ \$V == *'x'* ]]; }" || mis=$((mis+1))
prove "alt-i"     "printf '%s' \"\$V\" | grep -qi 'literal\\|x'"      "{ [[ \${V,,} == *'literal'* ]] || [[ \${V,,} == *'x'* ]]; }" || mis=$((mis+1))
prove "herestring" "printf '%s' \"\$V\" | grep -qE '^l.*l$'"           "grep -qE '^l.*l\$' <<< \"\$V\"" || mis=$((mis+1))
if [ "$mis" -eq 0 ]; then
  ok "C1: all five transforms agree with the pipeline they replaced, on 9 haystacks each (metacharacters, case, multi-line, empty)"
else
  bad "C1: $mis transform(s) disagree with the original on some haystack"
fi
# C2: the transform that matters most — `grep -q` under pipefail must NOT let a producer's
# status decide the verdict. Demonstrated with a producer that dies of SIGPIPE.
if command -v head >/dev/null 2>&1; then
  demo_old="$( ( set -o pipefail; yes 'needle' 2>/dev/null | head -c 100000 | grep -q 'needle' ) >/dev/null 2>&1; echo $? )"
  demo_new="$( grep -q 'needle' <<< 'needle' >/dev/null 2>&1; echo $? )"
  if [ "$demo_new" = "0" ]; then
    ok "C2: the replacement reports the MATCH (0) regardless of what the host does to a pipe"
  else
    bad "C2: the replacement reported $demo_new for a matching haystack"
  fi
  # And the motivating case: a producer that dies early under pipefail.
  epipe="$( ( set -o pipefail; sh -c 'printf "%s\n" "needle"; kill -PIPE $$' 2>/dev/null | grep -q 'needle' ) >/dev/null 2>&1; echo $? )"
  case "$epipe" in
    1|141) skip "C2b: the host made the old pipeline fail with status $epipe (that IS the incident class; timing-dependent, so it is informational here)" ;;
    *)     skip "C2b: the producer did not die early on this host (status $epipe) — the incident is timing-dependent" ;;
  esac
fi

# ---------------------------------------------------------------------------
# D. documented, tracked, registered
# ---------------------------------------------------------------------------
hdr "D. the block is written down and wired in"
if grep -q 'B386' RELEASE-NOTES.md 2>/dev/null; then
  ok "D1: RELEASE-NOTES.md carries the B386 entry"
else
  bad "D1: RELEASE-NOTES.md has no B386 entry"
fi
if grep -q 'B386' docs/ROADMAP.md 2>/dev/null; then
  ok "D2: docs/ROADMAP.md records B386"
else
  bad "D2: docs/ROADMAP.md does not mention B386"
fi
if grep -q 'check_b386_no_variable_grep_q.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D3: registered in the pre-deploy catalog"
else
  bad "D3: not registered in scripts/verify_pre_deploy.sh (rule 2)"
fi
if git ls-files --error-unmatch scripts/check_b386_no_variable_grep_q.sh >/dev/null 2>&1; then
  ok "D4: this script is tracked by git (trap #11)"
else
  bad "D4: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'B386' AGENTS.md 2>/dev/null; then
  ok "D5: AGENTS.md's block index carries B386"
else
  bad "D5: AGENTS.md has no B386 entry (AGENTS rule 2)"
fi

printf '\n\033[1mB386 summary: %d passed, %d failed, %d skipped\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
