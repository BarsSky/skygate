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
# WHY ONLY THE DOCKER SHAPE IS ACCEPTED
#
# All six checks are DOCKER-SHAPED: they shell out to `docker exec headscale …`,
# `docker exec skygate-skygate-1 …` and read the PG container. On a NATIVE
# (systemd) host they cannot answer their question at all, so "the stack is
# reachable over HTTP" is NOT a sufficient condition — measured 2026-11-15 on the
# retired host, which still forwarded `127.0.0.1:8080/healthz` and
# `127.0.0.1:50444/health` through the cutover SSH tunnel to the NEW host while
# having no containers: an HTTP probe called that a live pair and the checks went
# on to fail on `docker exec`. The guard therefore accepts exactly the shape the
# checks can use: the `skygate-skygate-1` AND `headscale` containers RUNNING.
# Making the six checks install-kind aware is a separate task (docs/ru/gate-live-skip.md).
#
# USAGE (one line, right after the other lib include or the shebang):
#
#   . "$(dirname "$0")/lib/skip_if_no_live_stack.sh"
#
# Escape hatch: SKYGATE_SKIP_LIVE_GATE=1 forces the guard off (run the check even
# without a live pair — for debugging what the check itself does).

_live_stack_up=0

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    if [ "$(docker inspect -f '{{.State.Running}}' skygate-skygate-1 2>/dev/null)" = "true" ] \
       && [ "$(docker inspect -f '{{.State.Running}}' headscale 2>/dev/null)" = "true" ]; then
        _live_stack_up=1
    fi
fi

if [ "$_live_stack_up" -eq 0 ] && [ -z "${SKYGATE_SKIP_LIVE_GATE:-}" ]; then
    echo "SKIP: no RUNNING skygate-skygate-1 + headscale container pair on this host — this check is docker-shaped and inspects live state; run it on the host that serves the tailnet (SKYGATE_SKIP_LIVE_GATE=1 to override)"
    exit 0
fi
