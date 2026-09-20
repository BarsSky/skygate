#!/usr/bin/env bash
# check_b20.sh — the autoupdater's git fetch must use --force.
#
# v0.32.6: without --force, an upstream tag move made every later
# `git fetch --tags` fail with
#     ! [rejected]  v1.5.2 -> v1.5.2 (would clobber existing tag)
# and the self-updater could never see the new tag again.
#
# 2026-09-19 — CONTRACT RENEGOTIATED for B272.5.
# The inline fetch that used to sit on the line right after
#     u.State.SetPhase(PhasePullBuild, …)
# moved into DockerUpgrader.fetchTarget, so the offline-host fallback
# (SKYGATE_UPDATE_GIT_URL / SKYGATE_UPDATE_GIT_MIRROR / the DB key
# update.git_url) can reuse the same code path with an explicit
# refspec. The old grep asserted ADJACENCY
#     grep -A1 'PhasePullBuild' internal/update/docker.go | grep -q 'runGit(ctx, "fetch", … --force)'
# which a correct refactor necessarily breaks. This script asserts the
# actual invariant instead: every fetch invocation in the upgrader
# passes --force, and the origin fetch still targets tags+prune.
#
# Raised 2026-09-19 when the gate run on the B272.5 commit reported
#   FAIL  B20  autoupdate git fetch uses --force (v0.32.6 stale-tag fix)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B20: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }

DOCKER=internal/update/docker.go
MANUAL=internal/update/manual.go

# A. The origin fetch still carries --tags --prune --force.
if grep -q 'runGit(ctx, "fetch", "--tags", "--prune", "--force")' "$DOCKER"; then
  ok "A.1 origin fetch is runGit(ctx, \"fetch\", \"--tags\", \"--prune\", \"--force\")"
else
  bad "A.1 the origin fetch must pass --tags --prune --force"
fi

# B. Every fetch invocation in the upgrader passes --force (origin and
#    the B272.5 mirror path alike). Counts, not adjacency.
total=$(grep -c 'runGit(ctx, "fetch"' "$DOCKER" || true)
if [ "${total:-0}" -ge 1 ]; then
  ok "B.1 the upgrader performs ${total} literal fetch invocation(s)"
else
  bad "B.1 no runGit(ctx, \"fetch\" …) found in $DOCKER"
fi
# Every literal fetch line must carry --force on the SAME line.
unforced=$(awk '/runGit\(ctx, "fetch"/ && index($0, "--force") == 0 { print NR": "$0 }' "$DOCKER")
if [ -z "$unforced" ]; then
  ok "B.2 every literal fetch invocation carries --force"
else
  bad "B.2 fetch invocation without --force: $unforced"
fi
# The mirror fetch builds its argv from a slice; assert --force is in it.
if grep -q '"--force", mirror}' "$DOCKER"; then
  ok "B.3 the mirror fetch argv includes --force"
else
  bad "B.3 the mirror fetch argv must include --force"
fi

# C. The B272.5 fallback is what makes the refactor worth it — pin it.
if grep -q 'func (u \*DockerUpgrader) fetchTarget(' "$DOCKER"; then
  ok "C.1 fetchTarget exists (origin + mirror share one fetch path)"
else
  bad "C.1 DockerUpgrader.fetchTarget is missing"
fi
if grep -q 'PhasePullBuild' "$DOCKER"; then
  ok "C.2 the pull/build phase is still announced"
else
  bad "C.2 SetPhase(PhasePullBuild, …) must remain"
fi

# D. The manual-steps generator tells the operator the same command.
if grep -q 'git fetch --tags --prune --force' "$MANUAL"; then
  ok "D.1 manual.go documents git fetch --tags --prune --force"
else
  bad "D.1 manual.go must document the --force fetch"
fi

# E. The reason is still recorded next to the code it explains.
if grep -q 'would clobber existing tag' "$DOCKER"; then
  ok "E.1 the stale-tag failure mode is documented in docker.go"
else
  bad "E.1 docker.go must keep the 'would clobber existing tag' note"
fi

printf '\n\033[1mB20: %d PASS, %d FAIL\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
