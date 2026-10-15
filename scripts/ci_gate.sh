#!/usr/bin/env bash
# scripts/ci_gate.sh — B280 (v1.5.46) — "do not tag / do not release a
# commit whose CI is not green".
#
# ONE implementation of the question, called by three places so they can
# never disagree:
#
#   1. .githooks/pre-tag           — refuses the local `git tag -a` (client side)
#   2. .github/workflows/release.yml `preflight` job — refuses the release,
#      so a tag pushed with --no-verify (or from a machine without hooks)
#      cannot publish images or a GitHub Release (server side)
#   3. .github/workflows/tag-release.yml — the sanctioned way to CREATE the
#      tag: it runs this gate first, so the tag only exists for a tested
#      commit
#
# WHY (2026-09-22): the v1.5.41 → v1.5.45 cycle pushed tags after
# `git push --no-verify`, skipping the pre-push gate. CI caught two real
# regressions in that window (an RU i18n parity break and a raw-http.Error
# leak in v1.5.44) — but the tag already existed, so the release workflow
# built and published binaries from a commit CI had never accepted.
# A tag is a promise that a commit was tested; this script is what makes
# the promise checkable.
#
# RESOLVING THE SHA (B381, 2026-10-13)
#   Both sanctioned callers already pass a FULL sha (`.githooks/pre-tag` gets
#   `$2` from git, `tag-release.yml` runs `git rev-parse HEAD`), so this never
#   affects tagging. It does affect the operator who types
#   `scripts/ci_gate.sh --sha 124f54ef`: `repos/.../runs?head_sha=<prefix>`
#   matches NOTHING — the API compares the full value — and the empty answer
#   was reported as `no 'ci.yml' run found for 124f54ef (was the commit pushed
#   with --no-verify?)` while that commit's CI was green and running. The sha
#   is therefore canonicalised through the local object database whenever it
#   resolves, and a sha that does NOT resolve is reported as a limitation of
#   the query rather than as a fact about the commit. Exit codes are unchanged.
#
# USAGE
#   scripts/ci_gate.sh [--repo OWNER/NAME] [--sha SHA] [--workflow ci.yml]
#                      [--branch main] [--wait SECONDS] [--allow-off-main]
#                      [--quiet]
#
#   --repo        GitHub repo; defaults to $GITHUB_REPOSITORY, then `gh repo view`
#   --sha         commit to verify; defaults to HEAD. A SHORT sha is accepted
#                 and resolved through the local object database (B381): the
#                 GitHub `head_sha` filter only matches the full 40-character
#                 value, so a prefix used to be answered with an empty list and
#                 reported as "this commit was never tested" — a false refusal
#                 about a commit whose CI was green, which the operator cannot
#                 act on. See the RESOLVING THE SHA note below.
#   --workflow    the workflow file that must be green (default ci.yml)
#   --branch      the branch the commit must be an ancestor of (default main)
#   --wait N      poll up to N seconds while runs are queued/in_progress
#                 (default 0 = never wait; a running CI is "not green yet")
#   --allow-off-main  skip the "commit must be on <branch>" check
#   --quiet       only the verdict line
#
# EXIT CODES
#   0  green — at least one CI run completed successfully, none failed
#   1  not green — failed / cancelled / timed out / still running / no run
#      at all (the message names which), OR the commit is not on <branch>
#   2  cannot verify — no `gh`, not authenticated, API/network failure, or
#      `origin/<branch>` is not available for the ancestry check. Callers
#      MUST treat 2 as "refuse": an unverifiable commit is not a tested one.
#
# The escape hatch is deliberate and lives at the CALLER (SKIP_PRE_TAG_CHECK=1
# for the hook, the SKYGATE_ALLOW_TAG_OFF_MAIN repository variable for the
# workflow) so every bypass is visible where it is taken.
#
# 2026-09-22: B280.
# 2026-10-13: B381 — a short sha is resolved, never reported as an untested commit.

set -uo pipefail

REPO="${GITHUB_REPOSITORY:-}"
SHA="HEAD"
WORKFLOW="ci.yml"
BRANCH="main"
WAIT=0
ALLOW_OFF_MAIN=0
QUIET=0

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) REPO="${2:-}"; shift 2 ;;
    --sha) SHA="${2:-}"; shift 2 ;;
    --workflow) WORKFLOW="${2:-}"; shift 2 ;;
    --branch) BRANCH="${2:-}"; shift 2 ;;
    --wait) WAIT="${2:-0}"; shift 2 ;;
    --allow-off-main) ALLOW_OFF_MAIN=1; shift ;;
    --quiet) QUIET=1; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) printf 'ci_gate: unknown argument %q (try --help)\n' "$1" >&2; exit 2 ;;
  esac
done

say()  { [ "$QUIET" = "1" ] || printf '%s\n' "$*"; }
fail() { printf 'ci_gate: NOT GREEN — %s\n' "$*" >&2; exit 1; }
cant() { printf 'ci_gate: CANNOT VERIFY — %s\n' "$*" >&2; exit 2; }

summary() {
  # GitHub renders this under the job; harmless locally.
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
  [ -w "${GITHUB_STEP_SUMMARY}" ] || return 0
  printf '%s\n' "$*" >> "$GITHUB_STEP_SUMMARY"
}

# ── host diagnostics (B385, 2026-10-14) ───────────────────────────────────
# Three consecutive CANNOT VERIFY runs on the reference workstation each named the WRONG
# cause, and the real one was never printed:
#   * "the GitHub API query failed (repo=… workflow=… sha=…)" — the API call's stderr was
#     thrown away by `2>/dev/null` INSIDE query_runs, and the actual reason was that `gh`
#     could not read the Windows credential store ("unable to get credential storage lock
#     in 1000 ms: File exists");
#   * "'gh' is not authenticated (run: gh auth login, or set GH_TOKEN)" — the token was
#     fine; the credential store was LOCKED at that instant. That advice sends the operator
#     to re-authenticate a working account;
#   * "'gh' CLI not on PATH" — true, but it did not say WHICH interpreter was running. On
#     that host a bare `bash` is C:\Windows\System32\bash, the WSL launcher: its PATH has
#     no `gh` at all, and its /c/Projects/skygate is an EMPTY directory, so a command that
#     "worked a minute ago" fails with no visible difference.
# So every refusal below names the class of the failure, the raw text it was classified
# from, and the interpreter/gh it was observed with. Exit codes are unchanged (0/1/2) and
# no bypass was added — the callers depend on both (rule 14).
bash_path() { command -v bash 2>/dev/null || printf 'unknown'; }
gh_path()   { command -v gh 2>/dev/null || printf 'NOT FOUND'; }
path_head() { printf '%s' "$PATH" | cut -c1-200; }
squash()    { printf '%s' "${1:-}" | tr '\n\t' '  ' | tr -s ' ' | cut -c1-400; }

# classify_auth <gh auth status output> — WHY it failed, in the operator's terms.
classify_auth() {
  _ca="$(printf '%s' "${1:-}" | tr 'A-Z' 'a-z')"
  case "$_ca" in
    *"credential storage lock"*|*"keyring"*|*"stub received bad data"*|*"failed to get token"*)
      printf 'the stored token could not be READ — the host credential store is locked or unavailable. This is NOT "not logged in", and re-authenticating cannot fix it: retry, or hand the token over explicitly (GH_TOKEN="$(gh auth token)" bash scripts/ci_gate.sh …)' ;;
    *"not logged"*|*"authentication required"*|*"bad credentials"*|*"http 401"*|*"http 403"*)
      printf 'gh is genuinely NOT AUTHENTICATED — run `gh auth login`, or set GH_TOKEN' ;;
    *)
      printf 'the auth check itself failed' ;;
  esac
}

# classify_api <gh api stderr> — which layer refused the query.
classify_api() {
  _cp="$(printf '%s' "${1:-}" | tr 'A-Z' 'a-z')"
  case "$_cp" in
    *"rate limit"*)                                            printf 'GitHub API rate limit' ;;
    *"could not resolve"*|*"no such host"*|*"connection"*|*"timed out"*|*"tls"*|*"network"*)
                                                               printf 'network/DNS to api.github.com' ;;
    *"credential"*|*"auth"*|*"401"*|*"403"*|*"locked"*)         printf 'credentials could not be read by the API layer' ;;
    "")                                                        printf 'no stderr at all (the command failed silently)' ;;
    *)                                                         printf 'unclassified — the raw text below is the whole evidence' ;;
  esac
}

# ── environment ───────────────────────────────────────────────────────────
command -v gh >/dev/null 2>&1 || cant "'gh' CLI not on PATH — interpreter=$(bash_path), PATH head: $(path_head). NOTE: where a bare \`bash\` is C:\\Windows\\System32\\bash (the WSL launcher) its PATH carries no gh and its /c/Projects/skygate is an EMPTY directory; run the gate with the MSYS shell instead (/usr/bin/bash scripts/ci_gate.sh …) or put the GitHub CLI on PATH"
if ! AUTH_OUT="$(gh auth status 2>&1)"; then
  cant "'gh' cannot authenticate: $(classify_auth "$AUTH_OUT"). raw: [$(squash "$AUTH_OUT")] (interpreter=$(bash_path) gh=$(gh_path) PATH head: $(path_head))"
fi
say "ci_gate: interpreter=$(bash_path) gh=$(gh_path)"

if [ -z "$REPO" ]; then
  REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null || true)"
fi
[ -n "$REPO" ] || cant "cannot determine the GitHub repo (pass --repo OWNER/NAME or set GITHUB_REPOSITORY)"

# Resolve the SHA we are actually gating.
if [ "$SHA" = "HEAD" ]; then
  SHA="$(git rev-parse HEAD 2>/dev/null || true)"
fi
[ -n "$SHA" ] || cant "cannot resolve the commit to verify"
case "$SHA" in
  *[!0-9a-fA-F]*) cant "sha '$SHA' is not a hex git object name" ;;
esac

# B381: the GitHub `head_sha` filter compares the FULL 40-character value, so a
# prefix answers with an empty list — which used to be reported as "this commit
# was never tested". Canonicalise through the local object database when we can;
# a sha we cannot resolve is a limitation of the QUERY, and must never be turned
# into a verdict about the commit (see the RESOLVING THE SHA note above).
SHA_IS_SHORT=0
if [ "${#SHA}" -lt 40 ]; then
  SHA_IS_SHORT=1
  resolved="$(git rev-parse --verify --quiet "${SHA}^{commit}" 2>/dev/null || true)"
  if [ -n "$resolved" ]; then
    say "ci_gate: $SHA resolved to $resolved"
    SHA="$resolved"
    SHA_IS_SHORT=0
  else
    say "ci_gate: WARNING — '$SHA' is ${#SHA} character(s) and does not resolve in this repository."
    say "ci_gate:           GitHub's head_sha filter needs the full 40-character sha, so a short sha"
    say "ci_gate:           can only be answered with an empty list. Pass the full sha (git rev-parse <ref>)."
  fi
fi

say "ci_gate: repo=$REPO sha=$SHA workflow=$WORKFLOW branch=$BRANCH"

# ── the commit must be on the release branch ──────────────────────────────
# A tag on a side branch (a hotfix experiment, a scratch commit) has no
# meaning for a release: the branch never went through the main-line CI.
if [ "$ALLOW_OFF_MAIN" != "1" ]; then
  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    cant "not inside a git repository, so 'is $SHA on $BRANCH' cannot be checked"
  fi
  if ! git rev-parse --verify --quiet "origin/$BRANCH" >/dev/null; then
    cant "origin/$BRANCH is not available locally (fetch it: git fetch origin $BRANCH)"
  fi
  # B381, same principle as above: a commit this clone cannot resolve is a
  # limitation of the LOOKUP. `git merge-base --is-ancestor <unknown>` fails, and
  # that failure used to be reported as "$SHA is not an ancestor of origin/main"
  # — a claim about the commit built out of our own missing object.
  if ! git rev-parse --verify --quiet "${SHA}^{commit}" >/dev/null 2>&1; then
    cant "sha '$SHA' is not in this repository's object database, so its ancestry cannot be checked (fetch it: git fetch origin $BRANCH)"
  fi
  if ! git merge-base --is-ancestor "$SHA" "origin/$BRANCH" 2>/dev/null; then
    fail "$SHA is not an ancestor of origin/$BRANCH — a release tag must point at a commit that went through the $BRANCH pipeline (override for a genuine hotfix with --allow-off-main / the SKYGATE_ALLOW_TAG_OFF_MAIN repo variable)"
  fi
  say "ci_gate: $SHA is on origin/$BRANCH"
fi

# ── CI runs for this exact commit ─────────────────────────────────────────
# head_sha filtering is what makes this the RIGHT run: a later push to main
# must never be able to satisfy the gate for an earlier commit.
# B385: the API's stderr is CAPTURED, not discarded — `2>/dev/null` here is what made the
# three false diagnoses possible ("API query failed" said nothing about why).
GH_ERR="$(mktemp 2>/dev/null || printf '/tmp/ci_gate_err.%s' "$$")"
trap 'rm -f "$GH_ERR"' EXIT
query_runs() {
  gh api --paginate \
    "repos/$REPO/actions/workflows/$WORKFLOW/runs?head_sha=$SHA&per_page=100" \
    --jq '.workflow_runs[] | [.id, .status, (.conclusion // ""), .event, .html_url] | @tsv' 2>"$GH_ERR"
}

deadline=$(( $(date +%s) + WAIT ))
while :; do
  if ! runs="$(query_runs)"; then
    api_err="$(cat "$GH_ERR" 2>/dev/null || true)"
    cant "the GitHub API query failed (repo=$REPO workflow=$WORKFLOW sha=$SHA): $(classify_api "$api_err"). raw: [$(squash "$api_err")] (gh=$(gh_path) interpreter=$(bash_path))"
  fi
  # A missing workflow file answers with an empty list, not an error.
  total=0; ok=0; pending=0; bad=""
  while IFS=$'\t' read -r id status conclusion event url; do
    [ -n "${id:-}" ] || continue
    total=$((total + 1))
    case "$status" in
      completed)
        if [ "$conclusion" = "success" ]; then
          ok=$((ok + 1))
        else
          bad="${bad}${bad:+, }${id}:${conclusion}(${event})"
        fi
        ;;
      *) pending=$((pending + 1)) ;;
    esac
  done <<< "$runs"

  if [ "$total" -eq 0 ]; then
    summary "### CI gate: REFUSED"
    summary ""
    summary "No \`$WORKFLOW\` run exists for \`$SHA\` — the commit was never tested."
    if [ "$SHA_IS_SHORT" = "1" ]; then
      # B381: do not send the operator hunting for a --no-verify push that never
      # happened. An unresolvable short sha explains this empty answer by itself.
      fail "no '$WORKFLOW' run found for the short sha $SHA, and it does not resolve in this repository — the GitHub head_sha filter needs the full 40-character sha, so re-run with the full sha (git rev-parse <ref>) before concluding anything about CI"
    fi
    fail "no '$WORKFLOW' run found for $SHA (was the commit pushed with --no-verify, or is CI disabled for it?)"
  fi
  if [ -n "$bad" ]; then
    summary "### CI gate: REFUSED"
    summary ""
    summary "Failing \`$WORKFLOW\` run(s) for \`$SHA\`: \`$bad\`"
    fail "failing CI run(s) for $SHA: $bad — re-run them (gh run rerun <id>) or tag a commit that passed"
  fi
  if [ "$pending" -eq 0 ]; then
    say "ci_gate: GREEN — $ok successful $WORKFLOW run(s) for $SHA, none failed"
    summary "### CI gate: GREEN"
    summary ""
    summary "\`$ok\` successful \`$WORKFLOW\` run(s) for \`$SHA\`."
    exit 0
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    fail "$pending $WORKFLOW run(s) for $SHA are still queued/in_progress after ${WAIT}s — wait for CI to finish, then retry"
  fi
  say "ci_gate: $pending run(s) still in progress; waiting (deadline in $(( deadline - $(date +%s) ))s)"
  sleep 30
done
