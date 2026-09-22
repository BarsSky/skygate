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
# USAGE
#   scripts/ci_gate.sh [--repo OWNER/NAME] [--sha SHA] [--workflow ci.yml]
#                      [--branch main] [--wait SECONDS] [--allow-off-main]
#                      [--quiet]
#
#   --repo        GitHub repo; defaults to $GITHUB_REPOSITORY, then `gh repo view`
#   --sha         commit to verify; defaults to HEAD
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

# ── environment ───────────────────────────────────────────────────────────
command -v gh >/dev/null 2>&1 || cant "'gh' CLI not on PATH (needed to query the CI runs)"
gh auth status >/dev/null 2>&1 || cant "'gh' is not authenticated (run: gh auth login, or set GH_TOKEN)"

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
  if ! git merge-base --is-ancestor "$SHA" "origin/$BRANCH" 2>/dev/null; then
    fail "$SHA is not an ancestor of origin/$BRANCH — a release tag must point at a commit that went through the $BRANCH pipeline (override for a genuine hotfix with --allow-off-main / the SKYGATE_ALLOW_TAG_OFF_MAIN repo variable)"
  fi
  say "ci_gate: $SHA is on origin/$BRANCH"
fi

# ── CI runs for this exact commit ─────────────────────────────────────────
# head_sha filtering is what makes this the RIGHT run: a later push to main
# must never be able to satisfy the gate for an earlier commit.
query_runs() {
  gh api --paginate \
    "repos/$REPO/actions/workflows/$WORKFLOW/runs?head_sha=$SHA&per_page=100" \
    --jq '.workflow_runs[] | [.id, .status, (.conclusion // ""), .event, .html_url] | @tsv' 2>/dev/null
}

deadline=$(( $(date +%s) + WAIT ))
while :; do
  runs="$(query_runs)" || cant "the GitHub API query failed (repo=$REPO workflow=$WORKFLOW sha=$SHA)"
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
