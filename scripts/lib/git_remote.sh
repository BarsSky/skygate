#!/usr/bin/env bash
# scripts/lib/git_remote.sh — CAN `git ls-remote origin` ANSWER ABOUT THE PUBLISHED REPO?
#
# B522 (measured 2026-11-25): two contracts ask the remote a question and read an EMPTY
# answer as a fact about the repository:
#
#   check_b237_24.sh D.1  "the v1.5.2 tag is absent"       (per the operator's rule, a
#                          failed CI run must not leave the tag behind)
#   check_b95.sh  §11     "these two branches are gone from origin"
#
# Both are correct against the real remote. But an empty answer has (at least) three
# causes, and only one of them is about the product:
#
#   * the remote is reachable and the ref really is absent  → a product fact
#   * the remote cannot be reached (no network, auth)       → a fact about the host
#   * `origin` is not a network remote at all               → a fact about THIS CLONE
#
# The third one is not hypothetical: the release gate is run in a scratch clone fed by a
# git bundle, where `origin` is a local path. `git ls-remote` then succeeds (rc=0) and
# prints nothing, so B237.24.1 reported "remote v1.5.2 tag is absent — was the release
# workflow re-run?" on a repository where the tag exists. It cost a full gate run here;
# in CI it would cost a release.
#
# The rule this library exists to make one-place: NEVER interpret an empty remote answer
# unless the remote is a network URL. Callers SKIP (rule 1) when it is not.
#
# Usage:
#   . "$(dirname "$0")/lib/git_remote.sh"
#   if git_remote_is_network; then  ...interpret the answer...  else skip "local origin"; fi
#
# Deliberately narrow: it classifies the CONFIGURED origin URL, it does not probe the
# network. A network URL that cannot be reached still answers rc!=0, which callers
# already treat as SKIP — that half was fixed before this library existed.

# git_remote_url — the origin URL as configured, empty when there is no origin.
git_remote_url() {
    git remote get-url origin 2>/dev/null || true
}

# git_remote_is_network — true when the origin is something a network round trip can
# answer: https/http, git://, ssh://, or the scp-like git@host:path form. A local path,
# a file:// URL or a bundle is NOT a remote that can prove anything about the published
# repository, so its empty answer must never be read as "the ref is gone".
git_remote_is_network() {
    case "$(git_remote_url)" in
        http://*|https://*|git://*|ssh://*|git@*:*) return 0 ;;
        *) return 1 ;;
    esac
}
