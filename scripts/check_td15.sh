#!/bin/bash
# check_td15.sh — pin: no unescaped backticks in run_check descriptions.
#
# TD-15 (v1.5.2) — false-alarm "headscale: command not found" at line
# 3221 of scripts/verify_pre_deploy.sh. The run_check "B160" call had
# its description argument in double quotes, but the description
# contained unescaped backticks around `headscale nodes expire
# --disable` (visual formatting). Bash treats backticks inside a
# double-quoted string as command substitution: it tried to exec
# `headscale nodes expire --disable`, the `headscale` binary isn't on
# PATH on the operator's Windows host, and bash emitted
# "scripts/verify_pre_deploy.sh: line 3221: headscale: command not
# found" to stderr. The run_check function captured stderr via
# `$(bash -c "$cmd" 2>&1)`, the failure bubbled up via $?, and
# the entire verify_pre_deploy.sh exited non-zero — even though
# the B160 check itself was clean. The pre-push hook printed
# "catalog RED — push ABORTED" but git push actually succeeded
# (the script's non-zero exit was treated as a false alarm by
# the operator's background task).
#
# This check pins: every run_check description in
# scripts/verify_pre_deploy.sh is free of unescaped backticks.
# Catches the regression class — any future B-description with
# markdown-style backticks will fail the catalog instead of
# silently tripping command substitution.
#
# Two known-safe patterns are explicitly allowed:
#  - escaped backticks: \`  (the backslash is what bash sees
#    inside the double-quoted string; the `\` is a no-op
#    visually)
#  - backticks inside single-quoted arguments (the inner
#    "cmd" in run_check is single-quoted by convention, but
#    we don't depend on that — we only check the
#    description argument)
#
# Contract A: 0 unescaped backticks in any run_check description.
# Contract B: 0 unescaped backticks in any echo line of
#             scripts/check_*.sh (the same bug class).
# Contract C: the original B160 fix is preserved
#             ('headscale nodes expire --disable' is in the
#             B160 description, not the backtick form).
# Contract D: TD-15 is mentioned in AGENTS.md.
# Contract E: TD-15 is registered in verify_pre_deploy.sh.
# Contract F: this script is executable.

set -u
cd "$(dirname "$0")/.." || exit 1

ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

PASS=0
FAIL=0

# ---------------------------------------------------------------------------
# Helper: count unescaped backticks in $1, print the violating
# substrings (max 5). A backtick is "escaped" if preceded by an
# ODD number of backslashes (so the LAST backslash is the escape;
# the earlier ones are themselves escaped).
# ---------------------------------------------------------------------------
count_unescaped_backticks() {
    local s="$1"
    local count=0
    local -a samples=()
    local i=0
    local n=${#s}
    while [ $i -lt $n ]; do
        local c="${s:$i:1}"
        if [ "$c" = '`' ]; then
            # Count preceding backslashes
            local bcount=0
            local j=$((i - 1))
            while [ $j -ge 0 ] && [ "${s:$j:1}" = '\' ]; do
                bcount=$((bcount + 1))
                j=$((j - 1))
            done
            if [ $((bcount % 2)) -eq 0 ]; then
                count=$((count + 1))
                if [ ${#samples[@]} -lt 5 ]; then
                    local start=$((i - 20))
                    [ $start -lt 0 ] && start=0
                    local end=$((i + 20))
                    [ $end -gt $n ] && end=$n
                    samples+=("pos=$i: ...${s:$start:$((end - start))}...")
                fi
            fi
        fi
        i=$((i + 1))
    done
    echo "$count"
    if [ $count -gt 0 ]; then
        for sample in "${samples[@]}"; do
            echo "    $sample"
        done
    fi
}

# ---------------------------------------------------------------------------
# Contract A: no unescaped backticks in run_check descriptions
# ---------------------------------------------------------------------------
# We use bash-only parsing of the verify_pre_deploy.sh file.
# A run_check call has the form:
#   run_check "B<num>" "<description>" \
#     '<cmd>'
# or (single-line):
#   run_check "B<num>" "<description>" '<cmd>'
#
# Strategy: read the file line by line. When we see the
# start of a run_check, accumulate lines into a buffer until
# we see a line ending with the closing single-quote of the
# cmd argument. Then extract the description and check it.

total_violations=0
tmpfile=$(mktemp)
trap "rm -f $tmpfile" EXIT

# Two awk programs: one for the multi-line form, one for inline.
# We do this in pure bash because the file is small (~3300 lines).

# First, extract every "run_check" call's description into a
# file, one per line, prefixed with the B-number.

awk '
BEGIN { in_call = 0; buf = ""; bnum = ""; }
/^[[:space:]]*run_check[[:space:]]+"B[0-9_]+"[[:space:]]+"/ {
    in_call = 1
    buf = $0
    # Extract B-number
    if (match($0, /run_check[[:space:]]+"B([0-9_]+)"/, arr)) {
        bnum = arr[1]
    } else {
        bnum = "??"
    }
    next
}
in_call == 1 {
    buf = buf "\n" $0
}
# End of multi-line form: line ends with single-quote (cmd closing)
in_call == 1 && /'\''[[:space:]]*\\?[[:space:]]*$/ {
    print "B" bnum "<<<" buf ">>>"
    in_call = 0
    buf = ""
    bnum = ""
    next
}
# End of inline form: closing single-quote, no continuation
in_call == 1 && /'\''/ && !/\\$/ {
    print "B" bnum "<<<" buf ">>>"
    in_call = 0
    buf = ""
    bnum = ""
    next
}
END {
    # Flush any trailing buffered call that ended the file
    if (in_call == 1) {
        print "B" bnum "<<<" buf ">>>"
    }
}
' scripts/verify_pre_deploy.sh > "$tmpfile"

while IFS= read -r entry; do
    [ -z "$entry" ] && continue
    # Split on <<<
    sep_pos=$(echo "$entry" | awk 'match($0, /<<</) { print RSTART; exit }')
    if [ -z "$sep_pos" ]; then continue; fi
    bname="${entry:0:$((sep_pos - 1))}"
    body="${entry:$((sep_pos + 2))}"
    # Drop the trailing >>> and any whitespace
    body="${body%>>>}"
    # body is the whole multi-line run_check call.
    # The description is the 2nd double-quoted string. We
    # find the first 3 double quotes; description is between
    # the 2nd and 3rd.
    # Use awk to extract.
    desc=$(echo "$body" | awk -F'"' 'NR==1 { if (NF >= 4) print $3 }' | head -1)
    # The above fails for multi-line bodies (NR==1 only). So
    # we use a different approach: replace newlines with spaces
    # in the body, then awk over the single line.
    flat_body=$(echo "$body" | tr '\n' ' ')
    desc=$(echo "$flat_body" | awk -F'"' '{ if (NF >= 4) print $3 }' | head -1)
    if [ -z "$desc" ]; then continue; fi
    # Count unescaped backticks
    result=$(count_unescaped_backticks "$desc")
    count=$(echo "$result" | head -1)
    if [ "$count" != "0" ]; then
        bad "contract A: $bname description has $count unescaped backtick(s):"
        echo "$result" | tail -n +2 | sed 's/^/        /'
        total_violations=$((total_violations + count))
    fi
done < "$tmpfile"

if [ $total_violations -eq 0 ]; then
    ok "contract A: 0 unescaped backticks in any run_check description in verify_pre_deploy.sh"
fi

# ---------------------------------------------------------------------------
# Contract B: no unescaped backticks in echo lines of scripts/check_*.sh
# ---------------------------------------------------------------------------
total_b=0
for f in scripts/check_*.sh; do
    [ -f "$f" ] || continue
    # Skip lines that are comments
    # awk: for each line, if it doesn't start with #, and contains
    # echo with a double-quoted string, extract the string and
    # check for unescaped backticks.
    awk_result=$(awk '
    BEGIN { quote = 0; content = ""; in_echo_dq = 0 }
/^[[:space:]]*#/ { next }
{
    line = $0
    # Find all echo "..." patterns on this line (could be multiple
    # but usually one). We iterate: find first echo ", take up to
    # closing " (not preceded by \), then advance.
    n = length(line)
    i = 1
    while (i <= n) {
        # Search for the literal sequence echo " on this line
        idx = index(substr(line, i), "echo \"")
        if (idx == 0) break
        start = i + idx + 5
        # Find next unescaped "
        j = start
        while (j <= n) {
            ch = substr(line, j, 1)
            if (ch == "\\") { j += 2; continue }
            if (ch == "\"") break
            j += 1
        }
        if (j > n) break
        content = substr(line, start, j - start)
        # Count unescaped backticks in content
        cnt = 0
        k = 1
        m = length(content)
        while (k <= m) {
            ch = substr(content, k, 1)
            if (ch == "`") {
                # Count preceding backslashes
                bcount = 0
                b = k - 1
                while (b >= 1 && substr(content, b, 1) == "\\") {
                    bcount += 1
                    b -= 1
                }
                if (bcount % 2 == 0) cnt += 1
            }
            k += 1
        }
        if (cnt > 0) {
            print FILENAME ":" NR ": " cnt " unescaped backtick(s) in: " content
        }
        i = j + 1
    }
}
' "$f")
    if [ -n "$awk_result" ]; then
        bad "contract B: unescaped backticks in $f:"
        echo "$awk_result" | sed 's/^/        /'
        total_b=$((total_b + 1))
    fi
done

if [ $total_b -eq 0 ]; then
    ok "contract B: 0 unescaped backticks in any echo line of scripts/check_*.sh"
fi

# ---------------------------------------------------------------------------
# Contract C: the B160 fix is preserved
# ---------------------------------------------------------------------------
if grep -qF "'headscale nodes expire --disable'" scripts/verify_pre_deploy.sh; then
    ok "contract C: B160 description uses 'headscale nodes expire --disable' (single-quoted, not backticked)"
else
    bad "contract C: B160 description is missing the single-quoted 'headscale nodes expire --disable' fix"
fi

# ---------------------------------------------------------------------------
# Contract D: TD-15 is mentioned in AGENTS.md
# ---------------------------------------------------------------------------
if grep -qF "TD-15" AGENTS.md; then
    ok "contract D: AGENTS.md mentions TD-15"
else
    bad "contract D: AGENTS.md does NOT mention TD-15"
fi

# ---------------------------------------------------------------------------
# Contract E: TD-15 is registered in verify_pre_deploy.sh
# ---------------------------------------------------------------------------
if grep -qF "check_td15.sh" scripts/verify_pre_deploy.sh; then
    ok "contract E: verify_pre_deploy.sh references check_td15.sh"
else
    bad "contract E: verify_pre_deploy.sh does NOT reference check_td15.sh"
fi

# ---------------------------------------------------------------------------
# Contract F: this script is executable
# ---------------------------------------------------------------------------
if [ -x scripts/check_td15.sh ]; then
    ok "contract F: scripts/check_td15.sh is executable"
else
    bad "contract F: scripts/check_td15.sh is NOT executable"
fi

# ---------------------------------------------------------------------------
echo
echo "=== TD-15 summary: $PASS pass, $FAIL fail ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "all contracts satisfied"
