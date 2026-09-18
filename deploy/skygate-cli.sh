#!/bin/bash
# deploy/skygate-cli.sh — host-side wrapper for `docker exec skygate ...`
#
# v0.29.2: This wrapper replaces the hardcoded `skygate` container
# name with a label-based lookup. The reason: in v0.29.0 the
# skygate service had `container_name: skygate` in docker-compose.yml,
# which caused a race with `docker compose up --force-recreate` —
# compose occasionally left the new container in `Created` state
# (not `Started`) because the old container's name hadn't been
# fully released by the time compose tried to create the new one.
#
# Removing `container_name: skygate` lets compose generate the
# name automatically (`skygate-skygate-N` with the project-prefix
# default). This eliminates the race but breaks every script that
# uses `docker exec skygate ...` directly — there are ~20 such
# references across AGENTS.md, deploy/*.sh, docs/*.md, etc.
#
# This wrapper hides the rename from those callers: it accepts
# the same `docker exec skygate <cmd>` syntax (with `skygate`
# as the literal container-name token) and translates it to
# `docker exec <real-name> <cmd>` using the
# `com.docker.compose.service=skygate` label. Install once on
# the host (`sudo cp skygate-cli.sh /usr/local/bin/skygate &&
# sudo chmod +x /usr/local/bin/skygate`) and every existing
# `docker exec skygate` invocation continues to work.
#
# Usage (from the host shell):
#   skygate ps                    # docker exec skygate ps
#   skygate sqlite3 /data/skygate.db ".tables"   # any command
#   skygate sh -c "echo hi"        # multiple args
#
# Limitations:
# - Only the literal token `skygate` is recognised. If a
#   caller wants to exec into a different container (e.g. the
#   old caddy sidecar), they should call `docker exec` directly
#   with the real name — this wrapper deliberately does NOT
#   guess.
# - The label lookup adds ~50ms per invocation vs. the hardcoded
#   name. For interactive shells that's fine; for hot loops in
#   scripts (e.g. the smoke test that runs every minute), use
#   a captured variable: `CID=$(skygate --id)` then
#   `docker exec "$CID" ...`.
#
# 2026-07-28: v0.29.2 — first cut.

set -eu

# Find the skygate container by compose service label. Project
# name "skygate" matches COMPOSE_PROJECT_NAME=skygate in
# docker-compose.yml. The label is set by compose automatically
# (it's the same one the orchestrator's ensureComposeServiceRunning
# uses — see internal/update/docker.go).
find_skygate_id() {
  docker ps -a --filter "label=com.docker.compose.service=skygate" \
    --filter "label=com.docker.compose.project=skygate" \
    --format '{{.ID}}' | head -1
}

case "${1:-}" in
  --id)
    # Return the container ID without execing. Useful for scripts
    # that want to do their own `docker exec "$CID" ...`.
    find_skygate_id
    ;;
  --help|-h)
    sed -n '3,40p' "$0"
    ;;
  "")
    echo "usage: skygate <docker-exec-args...>" >&2
    echo "       skygate --id          # print container ID" >&2
    echo "       skygate --help       # this message" >&2
    exit 2
    ;;
  *)
    # Translate `skygate <args...>` → `docker exec <real-id> <args...>`.
    CID=$(find_skygate_id)
    if [ -z "$CID" ]; then
      echo "skygate: no container with label com.docker.compose.service=skygate" >&2
      echo "         (is the skygate service running? try: docker compose ps)" >&2
      exit 1
    fi
    exec docker exec "$CID" "$@"
    ;;
esac
