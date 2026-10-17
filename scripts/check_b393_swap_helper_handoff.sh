#!/usr/bin/env bash
# check_b393_swap_helper_handoff.sh
#
# 2026-10-17 (B393) — A SELF-UPDATE MUST PROVE IT SWAPPED, AND THE HELPER MUST
# SEE THE SAME /data THE ORCHESTRATOR WROTE TO.
#
# OPERATOR-VISIBLE FAILURE
#   /admin/update reported `phase=done, error=null` while the panel kept serving
#   the previous build. Two releases in a row (v1.5.130 then v1.5.131) only
#   became live because the container was recreated BY HAND:
#       docker compose up -d --force-recreate --no-deps skygate
#
# WHAT WAS MEASURED ON THE HOST (2026-10-17, verbatim from data/skygate-update-swap.log)
#   [2026-10-16T20:08:31Z] helper container spawned (pid=58666), parent script exiting
#   /bin/sh: can't open '/data/skygate-swap-helper.sh': No such file or directory
#   [2026-10-16T20:34:45Z] helper container spawned (pid=60172), parent script exiting
#   /bin/sh: can't open '/data/skygate-swap-helper.sh': No such file or directory
#
#   Two independent defects produced that single sentence:
#     R1  BOTH swap scripts mounted the literal `skygate-data` volume. That
#         volume is a legacy leftover (created 20:08:31Z, EMPTY); the app's
#         /data is the checkout's ./data BIND mount, which is where the script
#         was written. The helper mounted the wrong filesystem.
#     R2  Nothing checked that the swap happened. pollHealthz accepted any 200
#         with status:ok, and confirmPendingSwap (admin) did the same from every
#         /admin/update page render, then called store.Complete(). The OLD
#         container answers that 200 perfectly well, so the job was promoted to
#         `done` within one page view. Before v1.5.131 the swap "worked" only
#         because a stale pre-v1.3.1 helper happened to survive in the volume.
#
# FIXES PINNED HERE
#   F1  one resolver (resolveDataMount) reads the live container's own /data
#       mount and renders it into BOTH scripts; the literal volume is gone.
#   F2  a pre-flight inside the outer script proves the helper can see its
#       script BEFORE spawning it, and writes a FAILED marker when it cannot.
#   F3  both probes compare the served build with the job target; a 200 from an
#       old build is no longer accepted, and a still-booting process
#       (ready:false / phase) is not mistaken for a finished swap.
#   F4  the new process resolves the verdict at boot (main.go), so the job does
#       not linger at build_done when nobody opens the panel; after
#       SwapGracePeriod without evidence it is declared failed with the log tail.
#   F5  the swap-log FAILED marker resolves the job immediately.
#
# CONTRACTS
#   A. the mount is resolved, not assumed; both scripts consume one resolver
#   B. a pre-flight exists in both scripts and a failure says so in the log
#   C. both verdicts are build-aware (the container poll and the admin page)
#   D. the boot-time witness exists AND has a caller
#   E. BEHAVIOURAL: the Go tests run — the toolchain decides
#   F. registered, tracked, documented
#   G. DETECTOR PROOF: re-planting the literal volume makes this check FAIL
#   H. LIVE (SKIP without docker): the running container's /data mount is what
#      the resolver would pick
#
# Scratch files live under mktemp -d (AGENTS trap #13). Producers are captured
# before they are matched — never `producer | grep -q` under pipefail
# (AGENTS trap #9, B386's detector scans scripts/*.sh for exactly that).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B393: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# B339: a contract reads a SURFACE, not a file. The boot-time witness lives in
# the cmd/skygate package; pinning one path would turn a pure code move red
# while the behaviour is unchanged (and would miss a re-introduction in a
# sibling file). `gosurface` concatenates the package's non-test files.
. scripts/lib/gosurface.sh
gosurface SKY_MAIN cmd/skygate/*.go

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b393.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

DOCKER=internal/update/docker.go
VERIFY=internal/update/swapverify.go
MOUNT=internal/update/swapmount.go
ADMIN=internal/feature/admin/update.go
MAIN="$SKY_MAIN"
TEST_UPDATE=internal/update/docker_b393_test.go
TEST_ADMIN=internal/feature/admin/update_b393_test.go

hdr "B393 — the swap must be handed the right /data and must prove itself"

DOCKER_SRC="$(cat "$DOCKER" 2>/dev/null || true)"
VERIFY_SRC="$(cat "$VERIFY" 2>/dev/null || true)"
MOUNT_SRC="$(cat "$MOUNT" 2>/dev/null || true)"
ADMIN_SRC="$(cat "$ADMIN" 2>/dev/null || true)"
MAIN_SRC="$(cat "$MAIN" 2>/dev/null || true)"

# count_of <needle> <file> — occurrences of a literal, 0 when absent.
count_of() { grep -cF -- "$1" "$2" 2>/dev/null || true; }

# ---------------------------------------------------------------------------
# A. F1 — one resolver, both scripts, no hardcoded volume
# ---------------------------------------------------------------------------
hdr "A — the /data mount is resolved from the live container, not assumed"

n="$(count_of 'skygate-data:/data' "$DOCKER")"
if [ "${n:-0}" -eq 0 ]; then
  ok "A1: no script mounts the legacy volume skygate-data:/data any more (that volume is EMPTY on a bind-mounted install: this was root cause R1)"
else
  bad "A1: ${n} script(s) still mount the literal skygate-data:/data — the helper will read an empty filesystem again"
fi

n="$(count_of 'DATA_MOUNT=__SKYGATE_DATA_MOUNT__' "$DOCKER")"
if [ "${n:-0}" -eq 2 ]; then
  ok "A2: BOTH the success and the rollback script carry the resolved-mount placeholder (a single fix is how this survived in two places)"
elif [ "${n:-0}" -eq 1 ]; then
  bad "A2: only ONE script carries the placeholder — the other still assumes a mount"
else
  bad "A2: neither script carries the placeholder (found ${n:-0})"
fi

n="$(count_of '-v "$DATA_MOUNT:/data"' "$DOCKER")"
if [ "${n:-0}" -eq 2 ]; then
  ok "A3: both helper invocations mount \$DATA_MOUNT at /data"
else
  bad "A3: expected 2 helper mounts of \$DATA_MOUNT, found ${n:-0}"
fi

case "$MOUNT_SRC" in
  *'func (u *DockerUpgrader) resolveDataMount(ctx context.Context) (string, string)'*)
    ok "A4: resolveDataMount exists and reports provenance as well as the value" ;;
  *) bad "A4: no resolveDataMount — nothing derives the mount from the live container" ;;
esac

case "$MOUNT_SRC" in
  *'dataMountInspectFormat'*'{{if eq .Destination "/data"}}'*)
    ok "A5: the resolver inspects the container's own /data mount (authoritative by construction — the orchestrator IS that container)" ;;
  *) bad "A5: the resolver does not inspect the /data mount" ;;
esac

case "$MOUNT_SRC" in
  *'defaultHostRepoPath'*'SKYGATE_HOST_REPO_PATH'*)
    ok "A6: the documented fallback (SKYGATE_HOST_REPO_PATH / default repo path + /data) is present and overridable" ;;
  *) bad "A6: no fallback path — a host without docker introspection gets an empty mount value" ;;
esac

case "$MOUNT_SRC" in
  *'case "volume"'*'case "bind"'*)
    ok "A7: a volume resolves to the VOLUME NAME and a bind to the HOST PATH (the two need different -v values)" ;;
  *) bad "A7: the resolver does not distinguish a volume from a bind" ;;
esac

case "$DOCKER_SRC" in
  *'renderSwapScript(swapSubprocessScript, dataMount)'*)
    ok "A8: the success path renders the script with the resolved mount" ;;
  *) bad "A8: the success path does not render with the resolved mount" ;;
esac
case "$DOCKER_SRC" in
  *'renderSwapScript(rollbackSwapScriptTemplate, rollbackDataMount)'*)
    ok "A9: the rollback path renders with the SAME resolver (it carries its own helper spawn, so it needed the same fix)" ;;
  *) bad "A9: the rollback path does not render with the resolved mount" ;;
esac
case "$DOCKER_SRC" in
  *'SKYGATE_SWAP_HELPER_IMAGE'*)
    ok "A10: the helper image is overridable (SKYGATE_SWAP_HELPER_IMAGE), so an offline host can pin a local mirror" ;;
  *) bad "A10: the helper image is hardcoded again" ;;
esac

# ---------------------------------------------------------------------------
# B. F2/F5 — the pre-flight, and a failure that says so
# ---------------------------------------------------------------------------
hdr "B — a wrong mount is a loud failure, not a silent 127"

n="$(count_of 'test -s /data/skygate-swap-helper.sh' "$DOCKER")"
if [ "${n:-0}" -eq 2 ]; then
  ok "B1: BOTH scripts prove the helper can see its script before spawning it (pre-B393 the only evidence was a buried /bin/sh error)"
else
  bad "B1: expected a pre-flight in both scripts, found ${n:-0}"
fi

n="$(count_of 'FAILED: the helper cannot see /data/skygate-swap-helper.sh' "$DOCKER")"
if [ "${n:-0}" -eq 1 ]; then
  ok "B2: the pre-flight writes a FAILED marker naming the mount value and the break-glass command"
else
  bad "B2: the pre-flight does not write a FAILED marker (found ${n:-0})"
fi

case "$VERIFY_SRC" in
  *'swapLogFailure'*'HasPrefix(body, "FAILED:")'*)
    ok "B3: the verdict reads that marker, so the job fails at once instead of after the grace period" ;;
  *) bad "B3: nothing reads the FAILED marker — the job would sit at build_done" ;;
esac

case "$VERIFY_SRC" in
  *'at.Before(since.Add(-2*time.Second))'*)
    ok "B4: only markers written at/after the job start count, so an old FATAL cannot fail a new job" ;;
  *) bad "B4: the marker scan does not filter by job start — a previous failure could fail the next update" ;;
esac

# ---------------------------------------------------------------------------
# C. F3 — both verdicts must name the build they expect
# ---------------------------------------------------------------------------
hdr "C — a 200 from the OLD build is not a successful update"

case "$DOCKER_SRC" in
  *'func (u *DockerUpgrader) pollHealthz(ctx context.Context, timeout time.Duration, expectedBuild string) error'*)
    ok "C1: pollHealthz takes the expected build (its doc comment had promised this parameter for months while the code accepted any 200)" ;;
  *) bad "C1: pollHealthz still has no expected-build parameter — root cause R2" ;;
esac

case "$DOCKER_SRC" in
  *'BuildSatisfiesTarget(build, expectedBuild, u.CurrentVersion)'*)
    ok "C2: the in-container poll compares the served build with the job target" ;;
  *) bad "C2: the in-container poll does not compare the build" ;;
esac

case "$DOCKER_SRC" in
  *'healthzBootInProgress(body)'*)
    ok "C3: a still-booting process (ready:false / phase set, B269) is not mistaken for a finished swap — it serves the NEW build string early" ;;
  *) bad "C3: the poll accepts a booting process as a finished swap" ;;
esac

case "$ADMIN_SRC" in
  *'update.ResolvePendingSwap(bgCtx, store, s.BuildVersion)'*)
    ok "C4: confirmPendingSwap delegates to the shared evidence-based verdict" ;;
  *) bad "C4: the admin page still decides on its own" ;;
esac

n="$(count_of 'http://localhost:8080/healthz' "$ADMIN")"
if [ "${n:-1}" -eq 0 ]; then
  ok "C5: the admin path no longer polls /healthz itself — and that poll could never have answered the question, because the process asking is the one serving the URL (the OLD container)"
else
  bad "C5: the admin path still polls localhost:8080/healthz (${n} occurrence(s)); that request is served by the very process asking it, so it can only ever confirm the build that is already running"
fi

case "$VERIFY_SRC" in
  *'func BuildSatisfiesTarget(build, target, previousBuild string) bool'*)
    ok "C6: one exported comparison, so a third string comparison cannot appear on the release path" ;;
  *) bad "C6: BuildSatisfiesTarget is missing" ;;
esac

case "$VERIFY_SRC" in
  *'SwapFromContainerID'*'fp.ID != st.SwapFromContainerID'*)
    ok "C7: a recreated container is the second witness (proves the swap even when the target is a branch and the label is unchanged)" ;;
  *) bad "C7: the container-id witness is missing" ;;
esac

case "$VERIFY_SRC" in
  *'SwapGracePeriod'*'store.Fail'*)
    ok "C8: an unfinished job is declared failed after the grace period, with a diagnostic instead of eternal build_done" ;;
  *) bad "C8: nothing declares an unfinished swap failed" ;;
esac

case "$VERIFY_SRC" in
  *'appendSwapLogTail'*'swap log: '*)
    ok "C9: the swap log tail is folded into the job log, so the page explains itself without a shell" ;;
  *) bad "C9: the failure does not carry the swap log tail" ;;
esac

# ---------------------------------------------------------------------------
# D. F4 — the boot-time witness has a caller
# ---------------------------------------------------------------------------
hdr "D — the new process resolves the verdict at boot"

case "$MAIN_SRC" in
  *'update.ResolvePendingSwap(context.Background(), updateStore, app.BuildVersion)'*)
    ok "D1: main.go calls ResolvePendingSwap with the build label of THIS process (declaring a function nobody calls is how the panel kept lying)" ;;
  *) bad "D1: no boot-time caller in the cmd/skygate surface" ;;
esac

# The call has to be after BuildVersion is known, otherwise it compares against
# an empty label and can never match.
BV_LINE="$(grep -n 'app.BuildVersion = buildVersionString()' "$MAIN" 2>/dev/null | head -1 | cut -d: -f1 || true)"
CALL_LINE="$(grep -n 'update.ResolvePendingSwap(context.Background(), updateStore, app.BuildVersion)' "$MAIN" 2>/dev/null | head -1 | cut -d: -f1 || true)"
if [ -n "${BV_LINE:-}" ] && [ -n "${CALL_LINE:-}" ] && [ "${CALL_LINE:-0}" -gt "${BV_LINE:-0}" ]; then
  ok "D2: the call sits after the build label is assigned (line ${CALL_LINE} > ${BV_LINE}), so it compares against a real label"
else
  bad "D2: the call is missing or ordered before app.BuildVersion is set (${CALL_LINE:-?} vs ${BV_LINE:-?})"
fi

# ---------------------------------------------------------------------------
# E. behavioural — the toolchain decides
# ---------------------------------------------------------------------------
hdr "E — behavioural: the swap-verdict and admin-page tests run"

GO_BIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GO_BIN" ] && [ -x "/c/Program Files/Go/bin/go.exe" ]; then
  GO_BIN="/c/Program Files/Go/bin/go.exe"
fi
if [ -z "$GO_BIN" ]; then
  skip "E1: the Go toolchain is not on PATH — cannot run the behavioural contracts"
else
  TEST_TIMEOUT="${SKYGATE_CHECK_TIMEOUT:-600}"
  OUT="$(timeout "$TEST_TIMEOUT" "$GO_BIN" test ./internal/update/ -run 'TestParseDataMountInspect|TestSwapScripts_UseResolvedMount|TestShQuote|TestBuildSatisfiesTarget|TestEvaluateSwap|TestSwapLogFailure|TestResolvePendingSwap|TestPendingSwapSurvivesPersistence|TestHealthzProbeParsing' -count=1 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "E1: the mount resolver, the rendered scripts, the build comparison and the verdict transitions pass (including 'old build in the old container is NOT a swap')"
  else
    bad "E1: the update-package B393 tests failed (rc=$rc)"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/        /' >&2
  fi

  OUT2="$(timeout "$TEST_TIMEOUT" "$GO_BIN" test ./internal/feature/admin/ -run 'ConfirmPendingSwap' -count=1 2>&1)"
  rc2=$?
  if [ "$rc2" -eq 0 ]; then
    ok "E2: the admin page path refuses a stale build, completes a matching one, and keeps the job open inside the grace window"
  else
    bad "E2: the admin B393 tests failed (rc=$rc2)"
    printf '%s\n' "$OUT2" | tail -25 | sed 's/^/        /' >&2
  fi
fi

# ---------------------------------------------------------------------------
# F. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "F — registered, tracked, documented"

MISSING_TRACK=""
for f in "$DOCKER" "$VERIFY" "$MOUNT" "$TEST_UPDATE" "$TEST_ADMIN"; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then : ; else MISSING_TRACK="${MISSING_TRACK}${f} "; fi
done
if [ -z "$MISSING_TRACK" ]; then
  ok "F1: every file this block adds is tracked by git (AGENTS trap #11: .gitignore can eat a new file silently)"
else
  bad "F1: untracked: $MISSING_TRACK"
fi

if grep -q 'check_b393_swap_helper_handoff' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in scripts/verify_pre_deploy.sh"
fi
if grep -q '^\- \*\*B393\*\*' AGENTS.md 2>/dev/null; then
  ok "F3: one-line entry in the AGENTS.md block index"
else
  bad "F3: no B393 entry in the AGENTS.md block index"
fi
if grep -q 'SKYGATE_DATA_MOUNT' docs/UPDATE.md 2>/dev/null; then
  ok "F4: docs/UPDATE.md documents the resolver and its override"
else
  bad "F4: docs/UPDATE.md does not document the /data resolver"
fi
if grep -q 'SKYGATE_DATA_MOUNT' docs/ru/UPDATE.md 2>/dev/null; then
  ok "F5: docs/ru/UPDATE.md documents it too (rule 12: RU and EN move together)"
else
  bad "F5: docs/ru/UPDATE.md is missing the resolver note"
fi
if grep -q 'B393' docs/ROADMAP.md 2>/dev/null && grep -q 'B393' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "F6: both ROADMAP files record the block"
else
  bad "F6: docs/ROADMAP.md and/or docs/ru/ROADMAP.md do not record B393"
fi
if grep -q 'B393' docs/LESSONS.md 2>/dev/null; then
  ok "F7: docs/LESSONS.md carries the lesson (a helper must see the writer's filesystem; a health check without an expected build proves nothing)"
else
  bad "F7: no lesson recorded in docs/LESSONS.md"
fi
if grep -q 'v1.5.132' RELEASE-NOTES.md 2>/dev/null; then
  ok "F8: RELEASE-NOTES.md carries the release entry"
else
  bad "F8: no v1.5.132 entry in RELEASE-NOTES.md"
fi

# ---------------------------------------------------------------------------
# G. detector proof — re-planting the violation must FAIL this check
# ---------------------------------------------------------------------------
hdr "G — the detector actually detects (plant the bug in a copy)"

printf 'DATA_MOUNT=__SKYGATE_DATA_MOUNT__\n  -v "$DATA_MOUNT:/data" \\\n' > "$SKY_TMP/clean.sh"
printf 'DATA_MOUNT=__SKYGATE_DATA_MOUNT__\n  -v skygate-data:/data \\\n' > "$SKY_TMP/planted.sh"

clean_n="$(count_of 'skygate-data:/data' "$SKY_TMP/clean.sh")"
planted_n="$(count_of 'skygate-data:/data' "$SKY_TMP/planted.sh")"
if [ "${clean_n:-0}" -eq 0 ] && [ "${planted_n:-1}" -ge 1 ]; then
  ok "G1: the A1 detector reports 0 on the fixed form and ${planted_n} on the literal volume, so a revert cannot pass silently"
else
  bad "G1: the detector is blind (clean=${clean_n:-0} planted=${planted_n:-0}) — contract A1 proves nothing"
fi

# The real file must still be clean after the copy experiment.
n="$(count_of 'skygate-data:/data' "$DOCKER")"
if [ "${n:-1}" -eq 0 ]; then
  ok "G2: the planted copy is a scratch file only; the real script stays fixed"
else
  bad "G2: the real script now carries the literal volume (${n})"
fi

# ---------------------------------------------------------------------------
# H. live — SKIP without docker
# ---------------------------------------------------------------------------
hdr "H — live: the running container's /data mount is what the resolver picks"

DOCKER_BIN="$(command -v docker 2>/dev/null || true)"
if [ -z "$DOCKER_BIN" ]; then
  skip "H1: docker is not on PATH — cannot inspect a live container (SKIP, never FAIL: the live state is not available here)"
else
  CID="$("$DOCKER_BIN" ps -aq --filter label=com.docker.compose.service=skygate 2>/dev/null | head -1 || true)"
  if [ -z "${CID:-}" ]; then
    skip "H1: no skygate container is running on this machine — nothing to inspect"
  else
    MNT="$("$DOCKER_BIN" inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}}|{{.Name}}|{{.Source}}{{end}}{{end}}' "$CID" 2>/dev/null | head -1 || true)"
    case "$MNT" in
      volume\|*|bind\|*)
        ok "H1: the live container's /data mount is '$MNT' — that is what resolveDataMount renders into both swap scripts"
        case "$MNT" in
          volume\|skygate-data\|*)
            skip "H2: this host still mounts the LEGACY skygate-data volume at /data, so the resolver deliberately picks it (the mount is correct for THIS host; the pre-flight still validates the write)" ;;
          *)
            ok "H2: the live /data is not the legacy volume, i.e. exactly the shape that broke the pre-B393 helper" ;;
        esac ;;
      "")
        bad "H1: the running container has no /data mount at all — the update job cannot persist its state" ;;
      *)
        skip "H1: unrecognised /data mount shape '$MNT' — not judging a live host from here" ;;
    esac
  fi
fi

printf '\n\033[1mB393: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
