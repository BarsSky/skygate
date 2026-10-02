#!/usr/bin/env bash
# check_b340_no_fixed_tmp_scratch.sh — B340: a check's scratch files must be
# per-run, never a fixed /tmp path.
#
# WHY THIS FILE EXISTS (measured 2026-10-01)
# ------------------------------------------
# The 2026-10-01 catalog run on the reference VM ended with EIGHT failures that
# were not about the product at all:
#
#   B148, B149, B150, B169, B171, B172, B-mod-core, B-mod-db-retry
#
# Every one of them had written its output to a FIXED /tmp path — and every one
# of the logs it pointed at said `ok`. The cause is a property of the host, not
# of the checks: `sudo` on that VM yields uid 0 WITHOUT CAP_DAC_OVERRIDE (verify
# with `sudo capsh --print`: `Current: =ep`), so an existing file in /tmp that
# belongs to another user is READ-ONLY for it:
#
#   scripts/check_b169.sh: line 46: /tmp/_b169_awk.txt: Permission denied
#
# The first run of the day (as the operator user) leaves the artefacts; the next
# run (as root, the invocation AGENTS trap #8 prescribes) cannot truncate them,
# the redirect fails, the check reads a stale or empty file and reports a
# phantom FAIL. The failure is order-dependent, which is the worst kind: the same
# catalog is green at 10:40 and red at 11:30 on an unchanged tree.
#
# This class had already been paid for twice before, and both times only the
# files that happened to fire were fixed: B3/B18/B90 wrote build output to
# /tmp/skygate_verify* and /tmp/x, and those three were converted to mktemp in
# the block that found them. The whole population was never measured. It is now:
# 24 check scripts plus 24 inline `run_check` commands in the catalog wrote fixed
# paths, and this contract keeps the number at zero.
#
# The fix is `SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"` plus an EXIT trap
# in each affected script, and `f=$(mktemp)` for the catalog's inline scripts.
#
# Usage:  bash scripts/check_b340_no_fixed_tmp_scratch.sh
# Exit:   0 = no fixed scratch path anywhere, 1 = at least one

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# scan_file <file> — lines that write a FIXED /tmp path.
#
# The pattern only matches a path that starts with an identifier character, so
# the one legitimate mention in the tree — B336 contract D4, which greps the
# catalog for `-o /tmp/[A-Za-z0-9_.-]+` as a REGEX — does not match (its "path"
# begins with `[`). A path that arrives through the mktemp template is not a
# fixed path either.
scan_file() {
  grep -nE '(>+ *|tee +|-o +)/tmp/[A-Za-z0-9_]' "$1" 2>/dev/null \
    | grep -v 'skygate-check\.XXXXXX' || true
}

hdr "A. no check script writes a fixed /tmp path"

HITS=""
for f in scripts/check_*.sh; do
  [ -f "$f" ] || continue
  # This guard names a planted fixed path in order to prove it can see one, so
  # it is excluded from its own scan (exactly as B339 excludes itself). Nothing
  # else is exempt: the population is measured, not sampled.
  [ "$f" = "scripts/check_b340_no_fixed_tmp_scratch.sh" ] && continue
  h="$(scan_file "$f")"
  if [ -n "$h" ]; then
    HITS="${HITS}${f}:${h}
"
  fi
done
if [ -z "$HITS" ]; then
  ok "A1: every check script keeps its scratch files under a per-run directory"
else
  bad "A1: these check scripts still write a FIXED /tmp path (a second user cannot overwrite it):"
  printf '%s' "$HITS" | sed 's/^/        /'
fi

# The gate's own inline run_check commands build a small script in /tmp; the
# same rule applies, because a killed check leaves the file behind.
CAT_HITS="$(grep -nE 'f=/tmp/[A-Za-z0-9_]' scripts/verify_pre_deploy.sh 2>/dev/null || true)"
if [ -z "$CAT_HITS" ]; then
  ok "A2: no inline run_check command in the catalog uses a fixed /tmp path"
else
  bad "A2: scripts/verify_pre_deploy.sh still builds an inline script at a fixed path:"
  printf '%s\n' "$CAT_HITS" | sed 's/^/        /'
fi

hdr "B. the detector itself works (a planted violation must be caught)"

TMPD="$(mktemp -d /tmp/skygate-b340.XXXXXX)"
trap 'rm -rf "$TMPD"' EXIT
printf 'go build ./... > /tmp/planted_fixed.log 2>&1\n' > "$TMPD/bad.sh"
printf 'go build ./... > ${SKY_TMP}/good.log 2>&1\n' > "$TMPD/good.sh"
printf 'go build ./... | tee "$TMPD/x.log"\n' > "$TMPD/ok.sh"

if [ -n "$(scan_file "$TMPD/bad.sh")" ]; then
  ok "B1: a planted fixed-path write is detected"
else
  bad "B1: the detector missed a planted fixed-path write — 'no violations' would mean nothing"
fi
if [ -z "$(scan_file "$TMPD/good.sh")" ]; then
  ok "B2: a per-run path (SKY_TMP) is not flagged"
else
  bad "B2: the detector flags the very fix it exists to enforce"
fi
if [ -z "$(scan_file "$TMPD/ok.sh")" ]; then
  ok "B3: an already-unique path is not flagged"
else
  bad "B3: the detector flags an already-unique path"
fi

hdr "C. the three checks that were fixed by hand keep their shape"

# check_html_in_i18n.sh, check_b_modules_admin_live.sh, check_b_duplicate_users.sh
# and check_b_node_attribution.sh owned their own EXIT trap or read their scratch
# file from inside a quoted heredoc (where ${…} does not expand), so they could
# not be swept mechanically. Each was edited by hand; pin the shape so a future
# edit cannot quietly reintroduce a fixed path.
if grep -q 'TMP_UNWRAPPED' scripts/check_html_in_i18n.sh 2>/dev/null; then
  ok "C1: check_html_in_i18n.sh writes its unwrapped-key list to a mktemp file"
else
  bad "C1: check_html_in_i18n.sh is back to a fixed /tmp/html_unwrapped"
fi
if grep -q 'rm -rf "\$SKY_TMP"' scripts/check_b_modules_admin_live.sh 2>/dev/null; then
  ok "C2: check_b_modules_admin_live.sh extends its own EXIT trap with SKY_TMP"
else
  bad "C2: check_b_modules_admin_live.sh lost the SKY_TMP half of its trap"
fi
if grep -q "os.environ\['DUP_USERS_JSON'\]" scripts/check_b_duplicate_users.sh 2>/dev/null \
   && grep -q "os.environ\['ATTR_NODES_JSON'\]" scripts/check_b_node_attribution.sh 2>/dev/null; then
  ok "C3: the two heredoc readers take their scratch path from the environment"
else
  bad "C3: a quoted-heredoc reader is back to a hardcoded /tmp path (it cannot see SKY_TMP)"
fi

hdr "D. tracked, registered, indexed, documented"

if git ls-files --error-unmatch scripts/check_b340_no_fixed_tmp_scratch.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git (AGENTS trap #11)"
else
  bad "D1: this script is NOT tracked by git — a .gitignore rule is eating it (AGENTS trap #11)"
fi
if grep -q 'check_b340_no_fixed_tmp_scratch.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in scripts/verify_pre_deploy.sh — it would never run"
fi
if grep -q 'B340' AGENTS.md 2>/dev/null; then
  ok "D3: recorded in the AGENTS.md block index"
else
  bad "D3: no B340 line in the AGENTS.md block index"
fi
if grep -q 'B340' docs/LESSONS.md 2>/dev/null; then
  ok "D4: the trap is written up in docs/LESSONS.md"
else
  bad "D4: the trap is not written up in docs/LESSONS.md"
fi

hdr "B340 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
