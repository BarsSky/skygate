#!/usr/bin/env bash
# scripts/lib/skip_if_no_live_stack.sh — shared pre-flight for the B-checks that
# inspect a RUNNING skygate+headscale stack (B479, 2026-11-15).
#
# WHY THIS EXISTS
#
# AGENTS rule 1: "a check that needs live state it does not have must report SKIP,
# never FAIL". `lib/skip_if_no_docker.sh` already does that for a missing docker
# DAEMON, but it answers a different question — docker can be perfectly reachable
# while the stack it inspects is not running. Measured on the host this project
# retired during the 2026-11-15 migration: the `skygate` and `headscale` containers
# were stopped deliberately (their names survived, `docker info` worked, the old
# derper container still ran), and six live-state checks failed the whole gate
# because `docker exec <name>` on a STOPPED container is an error, not a finding:
#
#   B243-duplicate-users, B243-reconcile-audit, B243-node-owner-map-orphans,
#   B-mod-tag-owners-coverage, B188.2, B188.3
#
# The gate then reported "7 checks failed" for a tree whose product code was fine,
# which is exactly the failure mode rule 1 forbids: a red line must mean
# "something regressed".
#
# WHICH SHAPES ARE ACCEPTED
#
# B488 (TD-37) closed the docker-only assumption: the checks read the installation
# through `lib/live_db.sh`, which knows BOTH the compose pair and the NATIVE
# (systemd) install, so this guard asks THAT library (live_kind) instead of
# inspecting docker itself. The library decides in ONE place, so the guard and the
# checks cannot drift apart — drift shows up as a silent SKIP, which is the failure
# mode rule 1 forbids.
#
# Why the decision is NOT taken over HTTP is unchanged and was measured 2026-11-15
# on the retired host: it still forwarded `127.0.0.1:8080/healthz` and
# `127.0.0.1:50444/health` through the cutover SSH tunnel to the NEW host while
# having no installation of its own — an HTTP probe called that a live pair and the
# checks went on to fail on commands the host could not run. "Can this host's
# headscale CLI and panel database answer" is the question, and only the library
# asks it.
#
# USAGE (one line, right after the other lib include or the shebang):
#
#   . "$(dirname "$0")/lib/skip_if_no_live_stack.sh"
#
# Escape hatch: SKYGATE_SKIP_LIVE_GATE=1 forces the guard off (run the check even
# without a live pair — for debugging what the check itself does).

# B488 (TD-37): the SHAPE question now lives in lib/live_db.sh, which also knows the NATIVE install
# (systemd + /etc/skygate/skygate.env). The six checks that used to be docker-shaped now read the
# installation through that library, so this guard must accept the shape they can actually use —
# otherwise a native host keeps skipping the very contracts that verify its live state.
_live_stack_up=0

# Locate this file WITHOUT dirname: the guard is proved by a contract that runs it under a scratch
# PATH holding no external tools, and spawning `dirname` there answered "command not found" while
# the guard silently stopped asking (B479 A2 caught it). Parameter expansion needs no fork.
_guard_self="${BASH_SOURCE[0]:-$0}"
case "$_guard_self" in
    */*) _guard_dir="${_guard_self%/*}" ;;
    *)   _guard_dir="." ;;
esac

if [ -f "$_guard_dir/live_db.sh" ]; then
    # shellcheck source=/dev/null
    . "$_guard_dir/live_db.sh"
    [ "$(live_kind)" != "none" ] && _live_stack_up=1
fi

if [ "$_live_stack_up" -eq 0 ] && [ -z "${SKYGATE_SKIP_LIVE_GATE:-}" ]; then
    echo "SKIP: no live installation on this host (no running skygate-skygate-1+headscale container pair and no /etc/skygate/skygate.env) — this check inspects live state; run it on the host that serves the tailnet (SKYGATE_SKIP_LIVE_GATE=1 to override)"
    exit 0
fi
