#!/usr/bin/env bash
# check_b445_update_truth.sh — B445 (2026-10-29)
#
# «на странице обновлений снова не отображается последняя версия доступная для установки».
#
# MEASURED on the reference install while diagnosing it: `api.github.com/rate_limit` from inside
# the skygate container answered core limit 60 / remaining 0, and the journal had carried
#
#   release-monitor: GitHub rate limit hit — the next check is in ≤1h0m0s; set
#   SKYGATE_GITHUB_TOKEN to raise 60 requests/hour to 5000
#
# every hour for two days. The install was on v1.5.189 while v1.5.190…v1.5.195 were published:
# the page had no fresh answer to show, AND the SCHEDULED update refused to act because it
# treats an empty `Latest` as "nothing to do". One rate-limited door froze the installation.
#
# The fix has two halves:
#   1. A SECOND DOOR: `git ls-remote --tags` needs no API quota at all, and the updater's own git
#      already speaks it. The checker falls back to the highest semver tag and reports
#      Source=git-tags with NotesUnavailable — the VERSION is exact, the release NOTES are not,
#      and the page says which.
#   2. HONESTY ABOUT THE FAILURE: when neither door answers, the error names the reset time in
#      words AND the remedy (SKYGATE_GITHUB_TOKEN), instead of a bare dash or «reset at unix …».
#
# CONTRACTS: 18.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

TAG=internal/update/tagtruth_b445.go
TAG_T=internal/update/tagtruth_b445_test.go
CHK_T=internal/update/checker_test.go
CHK=internal/update/checker.go
UPD=internal/feature/admin/update.go
TPL=internal/handlers/templates/admin/update.html
I18N=internal/i18n/catalog_update.go
GATE=scripts/verify_pre_deploy.sh
for f in "$TAG" "$TAG_T" "$CHK" "$UPD" "$TPL" "$I18N" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B445: tagtruth=%s checker=%s page=%s\n' "$TAG" "$CHK" "$UPD"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b445.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the second door: the version from git tags, no API quota"
GOFMT_OUT="$(gofmt -l "$TAG" "$TAG_T" "$CHK" "$UPD" "$TPL" "$I18N" 2>/dev/null | grep -v '\.html$' || true)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$CHK" 'func (c *Checker) fetchFromTags'; then
  ok "A2: the checker has a fallback that does not use the GitHub API"
else
  bad "A2: the checker still depends on the single rate-limited door"
fi
if has "$TAG" 'git", "ls-remote", "--tags", "--refs"'; then
  ok "A3: the fallback asks git for the tags (no API quota involved)"
else
  bad "A3: the fallback does not use git ls-remote"
fi
if has "$TAG" 'HighestSemverTag'; then
  ok "A4: the tag → version choice is a pure function"
else
  bad "A4: the version choice is not testable"
fi
# Junk must never become "the latest version": the real listing contains peeled annotated tags
# and hand-made non-version tags.
if has "$TAG" 'looksLikeSemver' && has "$TAG" 'hasPrereleaseSuffix'; then
  ok "A5: non-semver tags are skipped and the stable channel still excludes prereleases"
else
  bad "A5: a junk tag could be presented as the latest version"
fi

hdr "B. the page says WHERE the version came from"
if has "$CHK" 'NotesUnavailable'; then
  ok "B1: a tag-derived answer carries NotesUnavailable instead of pretending the notes arrived"
else
  bad "B1: a tag-derived answer is indistinguishable from a full API answer"
fi
# The result must NOT carry an Error in the fallback case: an error bypasses the success cache,
# so every render would re-run git (the quota-burning shape B424 fixed for the API).
if has "$TAG_T" 'FallbackIsCachedLikeASuccess'; then
  ok "B2: the fallback is cached like a success (proved by a test)"
else
  bad "B2: nothing proves the fallback does not run on every page load"
fi
if has "$TPL" 'NotesUnavailable' && has "$TPL" 'update.from_git_tags'; then
  ok "B3: the page renders the source note"
else
  bad "B3: the page presents a tag-derived version as if the API had answered"
fi
for k in update.from_git_tags update.notes_unavailable; do
  n="$(grep -c "\"$k\"" "$I18N" 2>/dev/null || true)"
  if [ "${n:-0}" -eq 2 ]; then
    ok "B4: $k is defined twice (RU + EN)"
  else
    bad "B4: $k is defined ${n:-0} time(s) — B325 requires a pair"
  fi
done

hdr "C. when neither door answers, the failure is actionable"
if has "$CHK" 'SKYGATE_GITHUB_TOKEN'; then
  ok "C1: the rate-limit error names the remedy"
else
  bad "C1: the error says "rate limited" and nothing about what to do"
fi
if has "$CHK" 'resets at'; then
  ok "C2: the reset time is reported in words, not only as unix seconds"
else
  bad "C2: the operator is told «reset at unix 1793306619», which says nothing"
fi
if has "$UPD" 'TagLister:'; then
  ok "C3: the admin service actually wires the fallback into the checker"
else
  bad "C3: the fallback exists but is never wired — dead code"
fi
if has "$UPD" 'updateGitRemoteFor'; then
  ok "C4: the fallback honours the configured git mirror (an offline host has no route to github.com)"
else
  bad "C4: the fallback always asks origin — useless on a mirror-only host"
fi

hdr "D. the rules are pinned by tests"
if ! command -v go >/dev/null 2>&1; then
  skip "D1: no go toolchain on PATH — cannot run TestB445 here (CI is authoritative)"
elif go test ./internal/update -run 'TestB445' -count=1 >"$WORK/t.log" 2>&1; then
  ok "D1: TestB445 green (tag choice, junk tags, rate-limited fallback, no-door error, cache)"
else
  bad "D1: TestB445 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "D2: no go toolchain on PATH — cannot run the update package here"
elif go test ./internal/update ./internal/feature/admin -count=1 >"$WORK/p.log" 2>&1; then
  ok "D2: both touched packages are green (B424/B431 cache contracts included)"
else
  bad "D2: a package suite failed — $(tail -3 "$WORK/p.log" | tr '\n' ' ')"
fi

hdr "E. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b445_update_truth.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: this script is not tracked by git"
fi
if grep -qF 'check_b445_update_truth' "$GATE" 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B445"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "E3: the B445 catalog description contains a backtick (TD-15)" ;;
  "")    bad "E3: the B445 catalog entry was not found" ;;
  *)     ok "E3: the B445 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB445 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
