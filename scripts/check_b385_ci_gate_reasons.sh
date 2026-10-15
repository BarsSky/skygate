#!/usr/bin/env bash
# check_b385_ci_gate_reasons.sh
#
# 2026-10-14 (B385) — "a refusal must name the reason, and the interpreter".
#
# THE INCIDENT (reference workstation, while releasing v1.5.124)
# -------------------------------------------------------------
# `scripts/ci_gate.sh` answered CANNOT VERIFY three times in a row, and every time it
# named the WRONG cause:
#
#   1. "the GitHub API query failed (repo=… workflow=… sha=…)"  — no cause at all. The
#      API call's stderr was discarded by `2>/dev/null` INSIDE `query_runs`, so the one
#      line that explained everything (gh could not read the Windows credential store:
#      "unable to get credential storage lock in 1000 ms: File exists") never reached the
#      operator.
#   2. "'gh' is not authenticated (run: gh auth login, or set GH_TOKEN)"  — the token was
#      valid; the credential store was LOCKED at that instant. The advice sends the
#      operator to re-authenticate a working account, and the real remedy (retry, or hand
#      over `GH_TOKEN="$(gh auth token)"`) was nowhere.
#   3. "'gh' CLI not on PATH"  — true but incomplete: it did not say WHICH interpreter was
#      running. On that host a bare `bash` is `C:\Windows\System32\bash`, the WSL LAUNCHER:
#      its PATH carries no `gh` at all, and its `/c/Projects/skygate` is an EMPTY directory
#      (the real checkout is `/mnt/c/Projects/skygate`), so a command that "worked a minute
#      ago" fails with nothing visible having changed.
#
# This is the misleading-verdict class B378 closed on the push gate and B381 on the sha
# lookup, in the one path that had not been converted: the gate could not be trusted to
# say WHY it refused.
#
# CONTRACTS
#   A. the cause can no longer be discarded, and nothing else about the gate changed
#      (exit codes 0/1/2, no bypass, the three sanctioned callers)
#   B. behavioural, with a stubbed `gh`: the missing CLI, the LOCKED credential store, a
#      genuinely unauthenticated gh, an API rate limit, a network failure — each verdict
#      names the class AND carries the raw text — plus the green/failed paths still work
#   C. documented (AGENTS trap + docs/operations.md), tracked, registered, released
#
# Scratch files live under mktemp -d (trap #13); no network and no real gh is used.
# The gate is driven with $BASH (the interpreter running THIS check), never with a bare
# `bash`: on the reference host a bare `bash` is the WSL launcher, which is the very trap
# this block documents (and which made three of its own verdicts unexplainable).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B385: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE=scripts/ci_gate.sh
RUN_BASH="${BASH:-/usr/bin/bash}"
SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b385.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

hdr "B385 — the CI gate must name the reason it cannot verify"

# ---------------------------------------------------------------------------
# A. the cause survives, and the gate's contract is unchanged
# ---------------------------------------------------------------------------
api_line="$(grep -n 'gh api --paginate' "$GATE" 2>/dev/null | head -1)"
if [ -n "$api_line" ] && ! printf '%s' "$api_line" | grep -q '2>/dev/null' \
   && grep -q '2>"\$GH_ERR"' "$GATE"; then
  ok "A1: the API call's stderr is CAPTURED (2>\$GH_ERR), not discarded (the 2>/dev/null that hid every cause)"
else
  bad "A1: the API call still discards its stderr: $api_line"
fi
e1=0; e2=0; e0=0
grep -q 'exit 2' "$GATE" && e2=1
grep -q 'exit 1' "$GATE" && e1=1
grep -q 'exit 0' "$GATE" && e0=1
if [ "$e0" = 1 ] && [ "$e1" = 1 ] && [ "$e2" = 1 ]; then
  if grep -qE 'exit [3-9]' "$GATE"; then
    bad "A2: the gate gained an exit code outside 0/1/2 — every caller treats 2 as refuse and would misread it"
  else
    ok "A2: exit codes stay 0 (green) / 1 (not green) / 2 (cannot verify)"
  fi
else
  bad "A2: one of the three verdicts lost its exit code (0=$e0 1=$e1 2=$e2)"
fi
if grep -qE '^[[:space:]]*(--skip|--force|--no-verify|--ignore[a-z-]*)\)' "$GATE" \
   || grep -qE '^[[:space:]]*[A-Za-z_]*SKIP[A-Za-z_]*=' "$GATE"; then
  bad "A3: the gate gained a bypass switch — overrides belong at the CALLER, not in the gate"
else
  ok "A3: no bypass switch inside the gate (the caller-level escape hatches are unchanged)"
fi
callers=0
for f in .githooks/pre-tag .github/workflows/tag-release.yml .github/workflows/release.yml; do
  grep -q 'ci_gate\.sh' "$f" 2>/dev/null && callers=$((callers+1))
done
if [ "$callers" -eq 3 ]; then
  ok "A4: the three sanctioned callers still go through the one gate"
else
  bad "A4: only $callers of 3 callers use scripts/ci_gate.sh"
fi

# ---------------------------------------------------------------------------
# B. behavioural: each refusal names the class AND the raw text
# ---------------------------------------------------------------------------
STUB="$SKY_TMP/bin"; mkdir -p "$STUB"
cat > "$STUB/gh" <<'STUBEOF'
#!/usr/bin/env bash
# A gh whose behaviour is chosen by the environment. An EMPTY variable means "not set"
# (checked explicitly): defaulting JSON-ish text with ${VAR:-…} leaks a brace into the
# output, the trap recorded in check_b384.
case "${1:-}" in
  auth)
    if [ -z "${STUB_AUTH:-}" ] || [ "${STUB_AUTH}" = "ok" ]; then exit 0; fi
    printf '%s\n' "${STUB_AUTH_MSG:-You are not logged into any GitHub hosts.}" >&2
    exit 1
    ;;
  api)
    if [ -n "${STUB_API_ERR:-}" ]; then printf '%s\n' "$STUB_API_ERR" >&2; exit 1; fi
    case "${STUB_API:-green}" in
      green)  printf '%s\n' $'101\tcompleted\tsuccess\tpush\thttps://example.invalid/101' ;;
      failed) printf '%s\n' $'102\tcompleted\tfailure\tpush\thttps://example.invalid/102' ;;
      none)   : ;;
    esac
    exit 0
    ;;
esac
exit 0
STUBEOF
chmod +x "$STUB/gh" 2>/dev/null || true

# Every STUB_* is EXPORTED so the child gate sees it; each case resets all four.
export STUB_AUTH="" STUB_AUTH_MSG="" STUB_API="green" STUB_API_ERR=""
reset_stub() { STUB_AUTH=""; STUB_AUTH_MSG=""; STUB_API="green"; STUB_API_ERR=""; }

gate_out=""; gate_rc=0
gate_run() { # gate_run <label> <want-rc> <needle…>
  local label="$1" want="$2"; shift 2
  gate_rc=0
  gate_out="$(PATH="$STUB:$PATH" "$RUN_BASH" "$GATE" --repo example/repo \
      --sha 0123456789abcdef --allow-off-main 2>&1)" || gate_rc=$?
  if [ "$gate_rc" != "$want" ]; then
    bad "$label → exit $gate_rc, want $want; output: $(printf '%s' "$gate_out" | tr '\n' ' ' | cut -c1-300)"
    reset_stub
    return 1
  fi
  local missing="" needle
  for needle in "$@"; do
    case "$gate_out" in *"$needle"*) ;; *) missing="${missing} [$needle]" ;; esac
  done
  if [ -z "$missing" ]; then
    ok "$label → exit $gate_rc, and the verdict names the cause"
  else
    bad "$label → exit $gate_rc but the verdict does not name:$missing; output: $(printf '%s' "$gate_out" | tr '\n' ' ' | cut -c1-400)"
  fi
  reset_stub
}

# B1: gh missing entirely — the verdict must name the interpreter and the WSL trap.
# (A minimal PATH keeps bash/git/coreutils and drops the GitHub CLI.)
MINPATH="$(dirname "$RUN_BASH"):/usr/bin:/bin"
if PATH="$MINPATH" command -v gh >/dev/null 2>&1; then
  skip "B1: a real gh is reachable from the minimal PATH ($MINPATH) — the 'gh missing' case cannot be simulated here"
else
  gate_rc=0
  gate_out="$(PATH="$MINPATH" "$RUN_BASH" "$GATE" --repo example/repo \
      --sha 0123456789abcdef --allow-off-main 2>&1)" || gate_rc=$?
  if [ "$gate_rc" != "2" ]; then
    bad "B1: no gh on PATH → exit $gate_rc, want 2"
  else
    missing=""
    for needle in "'gh' CLI not on PATH" "interpreter=" "WSL launcher" "/usr/bin/bash" "PATH head:"; do
      case "$gate_out" in *"$needle"*) ;; *) missing="${missing} [$needle]" ;; esac
    done
    if [ -z "$missing" ]; then
      ok "B1: gh missing → exit 2, naming the interpreter, the PATH and the WSL-launcher trap"
    else
      bad "B1: the 'gh not on PATH' verdict still hides the interpreter:$missing"
    fi
  fi
fi

# B2: the LOCKED credential store — the live Windows failure. It must not be reported as
# "not logged in", and it must not send the operator to re-authenticate.
STUB_AUTH=fail
STUB_AUTH_MSG="fatal: unable to get credential storage lock in 1000 ms: File exists"
gate_run "B2: the credential store is LOCKED" 2 "credential storage lock" "could not be READ" 'NOT "not logged in"'
case "$gate_out" in
  *"gh auth login"*) bad "B2b: a LOCKED credential store still advises 'gh auth login' — the advice that wasted the operator's time" ;;
  *) ok "B2b: a locked credential store does not get the 'gh auth login' advice" ;;
esac

# B3: genuinely unauthenticated — here the advice is correct and must be given.
STUB_AUTH=fail
STUB_AUTH_MSG="You are not logged into any GitHub hosts. Run gh auth login to authenticate."
gate_run "B3: gh is genuinely NOT authenticated" 2 "NOT AUTHENTICATED" "gh auth login"

# B4: the API refuses — the raw stderr must appear (it used to be discarded).
STUB_API_ERR="API rate limit exceeded for user ID 1."
gate_run "B4: the API rate limit" 2 "the GitHub API query failed" "rate limit" "API rate limit exceeded"

# B5: the API is unreachable — classified as network/DNS, raw text kept.
STUB_API_ERR='error: Post "https://api.github.com/graphql": dial tcp: lookup api.github.com: no such host'
gate_run "B5: the API is unreachable" 2 "network/DNS" "no such host"

# B6-B8: the happy and the failing paths still behave (the diagnostics must not break them).
STUB_API=green
gate_run "B6: CI is green" 0 "GREEN"
STUB_API=failed
gate_run "B7: a CI run failed" 1 "NOT GREEN"
STUB_API=none
gate_run "B8: no run exists for the commit" 1 "no 'ci.yml' run found"

# ---------------------------------------------------------------------------
# C. documented, tracked, registered, released
# ---------------------------------------------------------------------------
hdr "C. the trap and the fix are written down"
if grep -qi 'WSL launcher' AGENTS.md 2>/dev/null && grep -q 'System32.bash' AGENTS.md 2>/dev/null; then
  ok "C1: AGENTS.md carries the WSL-launcher trap (a bare bash is not Git Bash on this host)"
else
  bad "C1: AGENTS.md does not warn that a bare \`bash\` may be the WSL launcher"
fi
if grep -q 'B385' docs/operations.md 2>/dev/null && grep -q 'check_b385_ci_gate_reasons.sh' docs/operations.md 2>/dev/null; then
  ok "C2: docs/operations.md documents what a refusal now names"
else
  bad "C2: docs/operations.md does not document the B385 verdict shape"
fi
if grep -q 'B385' RELEASE-NOTES.md 2>/dev/null; then
  ok "C3: RELEASE-NOTES.md carries the B385 entry"
else
  bad "C3: RELEASE-NOTES.md has no B385 entry"
fi
if grep -q 'check_b385_ci_gate_reasons.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "C4: registered in the pre-deploy catalog"
else
  bad "C4: not registered in scripts/verify_pre_deploy.sh (rule 2)"
fi
if git ls-files --error-unmatch scripts/check_b385_ci_gate_reasons.sh >/dev/null 2>&1; then
  ok "C5: this script is tracked by git (trap #11)"
else
  bad "C5: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'B385' AGENTS.md 2>/dev/null; then
  ok "C6: AGENTS.md's block index carries B385"
else
  bad "C6: AGENTS.md has no B385 entry (AGENTS rule 2)"
fi

printf '\n\033[1mB385 summary: %d passed, %d failed, %d skipped\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
