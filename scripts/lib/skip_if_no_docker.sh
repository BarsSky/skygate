#!/usr/bin/env bash
# scripts/lib/skip_if_no_docker.sh — shared pre-flight for the LIVE-STATE
# B-checks.
#
# WHY THIS EXISTS
#
# A handful of checks inspect the running docker stack by name
# (`skygate-skygate-1`, `skygate-pg-local`, `headscale`) because that is the
# only way to answer their question ("do the live rows agree with headscale?",
# "did the audit row survive on PG?"). On a workstation without a reachable
# daemon — Docker Desktop stopped, CI without docker, a laptop on the train —
# `docker inspect` fails and the check reported
#
#   FAIL  skygate-skygate-1 container not running
#
# which is an environment answer to a live-state question: it painted the whole
# `verify_pre_deploy.sh` gate red and, worse, buried the real findings among
# them. The gate is only useful if its red lines mean "something regressed".
#
# WHAT IT DOES NOT DO
#
# It does NOT hide real failures. It exits early ONLY when the docker daemon
# itself is unreachable (`docker info` fails). If docker works but a container
# is missing, the check fails below exactly as before — on the VM that IS a
# real finding.
#
# USAGE (one line, right after the shebang):
#
#   . "$(dirname "$0")/lib/skip_if_no_docker.sh"
#
# Escape hatch: SKYGATE_SKIP_DOCKER_GATE=1 forces the gate off (run the check
# even without docker — for debugging what the check itself does).

if [ -z "${SKYGATE_SKIP_DOCKER_GATE:-}" ] && ! docker info >/dev/null 2>&1; then
    echo "SKIP: docker daemon not reachable — this check inspects the live docker stack; run it on the skygate VM (SKYGATE_SKIP_DOCKER_GATE=1 to override)"
    exit 0
fi
