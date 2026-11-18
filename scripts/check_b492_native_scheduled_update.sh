#!/usr/bin/env bash
# B492 (TD-42) — the SCHEDULED auto-update must work on a NATIVE install, not only in Docker.
#
# THE MEASURED DEFECT
#
# `tick` step 4b and `runScheduled` both began with
#
#	installKind := DetectInstallKind()
#	if installKind != InstallDocker { logScheduledSkip(installKind); return }
#
# so on the PRODUCTION host — systemd since the 2026-11-15 migration — an armed schedule
# (`update_schedule_enabled=1`, `update_schedule_time=08:00`, last run 2026-11-15) did nothing at all
# and logged "NOT SUPPORTED" once a slot. The native kinds were never actually incapable: the MANUAL
# path has staged them through the privileged applier since B261, and the host has everything that
# path needs (`skygate-update.path` enabled+active, the root-owned helper, the staging dir). Only the
# scheduler's guard said otherwise. The operator's ask (2026-11-18): «и для нативного варианта
# сделать автообновление по расписанию».
#
# Contracts:
#   A. ONE predicate decides, and BOTH the tick and the run use it — a second copy of the guard is
#      exactly how the first one survived two releases of native support
#   B. a native run stages the release TAG for the privileged applier (never the display label: the
#      applier downloads `skygate-<tag>-<os>-<arch>.tar.gz`, so a `+<sha>` suffix would 404 AFTER the
#      service stopped)
#   C. a target that is not a release tag fails the job VISIBLY — after the job exists, before the
#      applier is asked to do anything, with an actionable message
#   D. the Docker path is unchanged (`GitRefForBuildLabel`, the Docker upgrader)
#   E. an UNDETECTED install kind still skips loudly instead of staging a request nobody applies
#   F. the wiring hands the scheduler the native staging directory
#   G. the page's warning banner uses the SAME predicate (the pre-B492 banner said "Docker only" too)
#   H. the behaviour is pinned by Go tests that run here
#   I. discoverable: registered in the catalog, indexed in AGENTS.md, recorded in the roadmap

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

SCHED="internal/update/scheduler.go"
WIRE="cmd/skygate/main_update_scheduler.go"
ADM="internal/feature/admin/update.go"
I18N="internal/i18n/catalog_update.go"
TEST="internal/update/scheduler_native_b492_test.go"

line_of() { grep -n "$1" "$2" 2>/dev/null | head -1 | cut -d: -f1; }

# ═══════════════ A. one predicate, used twice ═══════════════
case "$(cat "$SCHED")" in
    *'func schedulerDrivesKind(kind InstallKind) bool'*) ok "A1: the capability predicate exists" ;;
    *) bad "A1: no schedulerDrivesKind predicate" ;;
esac
case "$(cat "$SCHED")" in
    *'func SchedulerDrivesKind(kind InstallKind) bool'*) ok "A2: it is exported for the page (one answer, two readers)" ;;
    *) bad "A2: the predicate is not exported, so the page must guess" ;;
esac
n_kind_seam="$(grep -c 'detectInstallKindFn()' "$SCHED" 2>/dev/null)"
if [ "${n_kind_seam:-0}" -ge 2 ]; then
    ok "A3: both the tick and the run ask the injected kind seam ($n_kind_seam call sites)"
else
    bad "A3: only ${n_kind_seam:-0} call site(s) use the seam — one path would still be untestable"
fi
if grep -q 'installKind != InstallDocker' "$SCHED" 2>/dev/null; then
    bad "A4: the Docker-only guard survives in scheduler.go (it is the defect)"
else
    ok "A4: no 'Docker-only' guard remains in the scheduler"
fi
case "$(cat "$SCHED")" in
    *'func logScheduledSkip'*'runs only on Docker'*)
        bad "A5: the skip log still claims the schedule is Docker-only" ;;
    *'runs only on Docker'*)
        bad "A5: the 'runs only on Docker' wording survives somewhere" ;;
    *) ok "A5: the skip log describes the real capability set" ;;
esac

# ═══════════════ B. the native branch stages the release tag ═══════════════
case "$(cat "$SCHED")" in
    *'NativeReleaseTagFor(target)'*) ok "B1: the native branch derives a release tag from the target" ;;
    *) bad "B1: the native branch does not derive a release tag" ;;
esac
case "$(cat "$SCHED")" in
    *'newNativeUpdater(installKind, nativeTag).Run(ctxRun, nativeTag)'*)
        ok "B2: the native upgrader receives the TAG (not the display label)" ;;
    *) bad "B2: the native upgrader is not called with the release tag" ;;
esac

# ═══════════════ C. the refusal is visible and ordered ═══════════════
start_line="$(line_of 'deps.State.Start(jobID' "$SCHED")"
tag_line="$(line_of 'nativeTag = NativeReleaseTagFor(target)' "$SCHED")"
run_line="$(line_of 'ctxRun, cancel := context.WithTimeout' "$SCHED")"
if [ -n "$start_line" ] && [ -n "$tag_line" ] && [ -n "$run_line" ] \
   && [ "$tag_line" -gt "$start_line" ] && [ "$tag_line" -lt "$run_line" ]; then
    ok "C1: the refusal sits between 'the job exists' (line $start_line) and 'the swap starts' (line $run_line)"
else
    bad "C1: the refusal is misplaced (start=$start_line tag=$tag_line run=$run_line) — a failure written before the job exists is written to nothing"
fi
case "$(cat "$SCHED")" in
    *'is not a release tag'*) ok "C2: the failure names the real problem and what to do" ;;
    *) bad "C2: the failure message does not say the target is not a release tag" ;;
esac
case "$(cat "$SCHED")" in
    *'alert.update.scheduled_failed'*) ok "C3: a refused scheduled run also ALERTS (unattended means nobody watches the page)" ;;
    *) bad "C3: a refused scheduled run is silent — the operator would learn about it from the panel being old" ;;
esac

# ═══════════════ D. Docker unchanged ═══════════════
case "$(cat "$SCHED")" in
    *'newUpgrader(deps.RepoPath, current).Run(ctxRun, GitRefForBuildLabel(target))'*)
        ok "D1: the Docker branch still normalises the ref with GitRefForBuildLabel" ;;
    *) bad "D1: the Docker branch changed shape" ;;
esac

# ═══════════════ E/F. undetected kinds and the staging dir ═══════════════
case "$(cat "$SCHED")" in
    *'if !schedulerDrivesKind(installKind) {'*) ok "E1: an undetected kind still takes the loud skip" ;;
    *) bad "E1: the skip branch is gone — an undetected kind would stage a request nobody applies" ;;
esac
case "$(cat "$WIRE")" in
    *'UpdateDir: cfg.UpdateDir'*) ok "F1: the scheduler is handed the native staging directory" ;;
    *) bad "F1: the native staging directory is not wired — the applier would watch an empty path" ;;
esac

# ═══════════════ G. the page tells the same truth ═══════════════
case "$(cat "$ADM")" in
    *'update.SchedulerDrivesKind(installKind)'*) ok "G1: the page's warning uses the same predicate" ;;
    *) bad "G1: the page computes support on its own — the banner and the tick can disagree" ;;
esac
ru_n="$(grep -c 'update.scheduler_not_supported.*systemd' "$I18N" 2>/dev/null)"
if [ "${ru_n:-0}" -ge 2 ]; then
    ok "G2: both catalogues describe the real capability set (Docker + native)"
else
    bad "G2: the banner text still implies Docker-only in at least one catalogue (${ru_n:-0} of 2 updated)"
fi

# ═══════════════ H. the behaviour is tested ═══════════════
if [ -f "$TEST" ]; then ok "H1: the tests exist"; else bad "H1: no test file"; fi
missing=""
for t in TestB492_SchedulerDrivesNativeAndDocker TestB492_NativeScheduledRunStagesTheReleaseTag \
         TestB492_NativeScheduledRunRefusesAGitDescribeTarget TestB492_UnknownKindStillSkipsLoudly \
         TestB492_NativeTickIsNotSkipped; do
    grep -q "func $t" "$TEST" 2>/dev/null || missing="$missing $t"
done
case "$missing" in
    '') ok "H2: the five behavioural tests are present (including the tick-level one)" ;;
    *)  bad "H2: missing tests:$missing" ;;
esac
tout="$(go test ./internal/update/ -run 'B492' -count=1 2>&1)"; trc=$?
if [ "$trc" -eq 0 ]; then
    ok "H3: the scheduled-native tests pass here"
else
    bad "H3: the scheduled-native tests fail: $(printf '%s' "$tout" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════ I. discoverability ═══════════════
if grep -q 'check_b492_native_scheduled_update.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "I1: registered in scripts/verify_pre_deploy.sh"
else
    bad "I1: not registered in the catalog"
fi
if grep -q '^\- \*\*B492\*\*' AGENTS.md 2>/dev/null; then
    ok "I2: the AGENTS index carries its line"
else
    bad "I2: no AGENTS index line"
fi
if grep -q 'B492' docs/ROADMAP.md 2>/dev/null; then
    ok "I3: the roadmap records it (TD-42)"
else
    bad "I3: the roadmap does not mention B492"
fi

echo
echo "  B492: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
