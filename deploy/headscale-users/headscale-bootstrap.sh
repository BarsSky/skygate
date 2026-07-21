#!/usr/bin/env bash
# ============================================================================
# headscale-bootstrap.sh — provision a per-user headscale container
# 2026-07-21: v0.23.0 Phase 1
#
# Usage:
#   headscale-bootstrap.sh <username> <portal_uid>
#
# Creates:
#   - /home/skyadmin/headscale/users/<username>/config/config.yaml
#   - /home/skyadmin/headscale/users/<username>/data/
#   - /home/skyadmin/headscale/docker-compose.user-<username>.yml
#   - headscale-<username> container (started)
#   - headscale user named <username> (idempotent)
#   - 1 long-lived API key (10 years)
#
# Outputs (stdout, JSON):
#   { "username": "...", "container": "headscale-<username>", "url": "http://...:PORT",
#     "api_key": "hskey-api-...", "port": 50450+uid%50, "user_id": <headscale_user_id> }
#
# Errors go to stderr; the script exits non-zero.
#
# Invariants (the script refuses to run if violated):
#   - docker compose must be available
#   - /var/run/docker.sock must be accessible (skygate container has it)
#   - the headscale_default network must exist (created by skygate's compose)
# ============================================================================
set -euo pipefail

USERNAME="${1:-}"
PORTAL_UID="${2:-}"

if [ -z "$USERNAME" ] || [ -z "$PORTAL_UID" ]; then
    echo "usage: headscale-bootstrap.sh <username> <portal_uid>" >&2
    exit 2
fi

# Validate username (lowercase + digits + _ -, same as portal_users.username).
if ! echo "$USERNAME" | grep -qE '^[a-z0-9_-]+$'; then
    echo "bad username: must match ^[a-z0-9_-]+\$" >&2
    exit 2
fi

# Layout on the host (mounted into the skygate container at the same paths).
GLOBAL_HEADSCALE_DIR="${SKYGATE_HEADSCALE_DIR:-/home/skyadmin/headscale}"
USER_DIR="$GLOBAL_HEADSCALE_DIR/users/$USERNAME"
COMPOSE_OVERRIDE="$GLOBAL_HEADSCALE_DIR/docker-compose.user-$USERNAME.yml"

# Same image as the global headscale (so the per-user instance is
# byte-identical except for the config + DB).
HEADSCALE_IMAGE="${SKYGATE_HEADSCALE_IMAGE:-headscale/headscale:0.29.2}"

# Port allocation. Range: 50450..50499 (50 users max in this range; if
# we need more, bump the range). gRPC is +1000 (50450→51450), metrics
# is +2000 (50450→52450). Per-user HTTPS / metrics are exposed on
# localhost only (no public port mapping) — the per-user instance is
# for skygate's API access, not public client connections (those go
# through the global plane).
BASE_PORT=50450
HTTP_PORT=$((BASE_PORT + (PORTAL_UID % 50)))
GRPC_PORT=$((HTTP_PORT + 1000))
METRICS_PORT=$((HTTP_PORT + 2000))

CONTAINER_NAME="headscale-$USERNAME"
NETWORK="headscale_default"

# Sanity: refuse to clobber an existing container with the same name.
# This protects against accidentally re-running the bootstrap for a
# user that's already provisioned. Use headscale-deprovision.sh
# first if you want to start over.
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "container $CONTAINER_NAME already exists; refusing to overwrite" >&2
    echo "(run headscale-deprovision.sh $USERNAME first)" >&2
    exit 1
fi

# Sanity: refuse to clobber an existing override file.
if [ -f "$COMPOSE_OVERRIDE" ]; then
    echo "compose override already exists: $COMPOSE_OVERRIDE" >&2
    echo "(run headscale-deprovision.sh $USERNAME first)" >&2
    exit 1
fi

# Per-user MagicDNS base domain. Different from the global
# `tsnet.skynas.ru` so per-user subnets don't collide with global ones
# in Tailscale's MagicDNS. The per-user headscale is only used by
# skygate (no public client registration in Phase 1), so the base
# domain is mostly cosmetic for now.
BASE_DOMAIN="${USERNAME}.tsnet.skynas.ru"

# Create the per-user directory + generate the noise private key.
# headscale serve auto-generates the noise key on first start, but we
# pre-generate it so the operator has the file in a known place
# (for backup / migration).
mkdir -p "$USER_DIR/config" "$USER_DIR/data"
if [ ! -f "$USER_DIR/config/noise_private.key" ]; then
    # Re-use the same generate primitive the global headscale uses.
    # We can't run `headscale generate private-key` here because the
    # binary isn't on the host PATH (only inside the container). The
    # generation is automatic on first `headscale serve`, so we just
    # let the container create it. The first `docker exec` to wait
    # for readiness also confirms the key file exists.
    :
fi

# Per-user headscale config. Mirrors the global config but:
#   - server_url points to the per-user container (so MagicDNS
#     records resolve via the per-user base_domain)
#   - listen_addr / grpc_listen_addr / metrics_listen_addr are
#     the per-user ports
#   - policy.mode is database (so skygate can push the per-user
#     ACL via the API)
#   - no DERP, no extra config — the per-user instance is bare-bones
#     (no exit-nodes, no relay config; the global headscale keeps
#     those). v0.23.2 cross-plane coordination adds a shared
#     `headscale-shared` if/when needed.
cat > "$USER_DIR/config/config.yaml" <<YAML
# Per-user headscale config — generated by headscale-bootstrap.sh.
# Do not edit manually; use /admin/users/{id}/plane in skygate.
server_url: http://$CONTAINER_NAME:$HTTP_PORT
listen_addr: 0.0.0.0:$HTTP_PORT
metrics_listen_addr: 127.0.0.1:$METRICS_PORT
tls:
  cert_path: ""
  key_path: ""
grpc_listen_addr: 0.0.0.0:$GRPC_PORT
grpc_allow_insecure: true
noise:
  private_key_path: /etc/headscale/noise_private.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
dns:
  magic_dns: true
  base_domain: $BASE_DOMAIN
  nameservers:
    global:
      - 1.1.1.1
      - 8.8.8.8
database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite
log:
  level: info
  format: text
registration_mode: no_registration
policy:
  mode: database
YAML

# Per-user docker-compose override. The base `docker-compose.yml`
# (in $GLOBAL_HEADSCALE_DIR) is the global one; this override adds
# the per-user service. Both files are combined with `docker compose
# -f docker-compose.yml -f docker-compose.user-<username>.yml up -d`.
#
# Networks: the per-user instance joins the same `headscale_default`
# network as the global one (created by skygate's compose as
# `external: true`). The container name (headscale-<username>) is
# resolvable as a DNS name on the network, so skygate's per-user
# headscale.Client talks to it via http://headscale-<username>:PORT.
#
# Volumes: bind-mount the per-user directory (so the operator can
# back up the DB / config from the host filesystem). We deliberately
# don't use a named volume — the named-volume path is
# /var/lib/docker/volumes/... which is harder to back up.
cat > "$COMPOSE_OVERRIDE" <<YAML
# Per-user headscale override — generated by headscale-bootstrap.sh.
# Do not edit manually; use headscale-deprovision.sh + bootstrap again.
services:
  $CONTAINER_NAME:
    image: $HEADSCALE_IMAGE
    container_name: $CONTAINER_NAME
    restart: unless-stopped
    command: serve
    volumes:
      - $USER_DIR/config:/etc/headscale
      - $USER_DIR/data:/var/lib/headscale
    environment:
      - TZ=UTC
    networks:
      - $NETWORK
    healthcheck:
      test: ["CMD", "/ko-app/headscale", "apikeys", "list"]
      interval: 5s
      timeout: 3s
      retries: 30
      start_period: 5s

networks:
  $NETWORK:
    external: true
YAML

# Start the container via the combined compose file. The global
# `docker-compose.yml` is also referenced so `docker compose` can
# resolve the external network reference (the global file declares
# `headscale_default: external: true`).
cd "$GLOBAL_HEADSCALE_DIR"
docker compose -f docker-compose.yml -f "$COMPOSE_OVERRIDE" up -d "$CONTAINER_NAME"

# Wait for the container to be ready (headscale apikeys list
# round-trips once the gRPC + HTTP listeners are up). The health
# check above does the same in docker's eyes; this loop blocks the
# bootstrap script until it's actually accepting API calls.
for i in $(seq 1 30); do
    if docker exec "$CONTAINER_NAME" /ko-app/headscale apikeys list >/dev/null 2>&1; then
        break
    fi
    if [ "$i" = "30" ]; then
        echo "timed out waiting for $CONTAINER_NAME to accept API calls" >&2
        echo "container logs:" >&2
        docker logs --tail 30 "$CONTAINER_NAME" >&2 || true
        exit 1
    fi
    sleep 1
done

# Create the per-user headscale user (idempotent — exits 0 even if
# the user already exists, since the `users create` CLI returns
# non-zero on "user exists" and we don't want that to be fatal).
docker exec "$CONTAINER_NAME" /ko-app/headscale users create "$USERNAME" 2>/dev/null || true

# Look up the headscale-internal user id (a numeric id assigned by
# headscale, distinct from the skygate portal_users.id). Used in
# subsequent API calls (preauth creation, ACL generation) so the
# caller knows which id to reference.
HS_USER_ID=$(docker exec "$CONTAINER_NAME" /ko-app/headscale users list -o json 2>/dev/null \
    | python3 -c "import sys, json
users = json.load(sys.stdin)
match = [u['id'] for u in users if u['name'] == '$USERNAME']
print(match[0] if match else 0)" || echo 0)
if [ "$HS_USER_ID" = "0" ]; then
    echo "failed to resolve headscale user id for $USERNAME" >&2
    exit 1
fi

# Issue a long-lived API key (10 years — same as the global
# HEADSCALE_API_KEY default). The key is the only thing the skygate
# per-user headscale.Client holds; rotating it requires
# re-provisioning the user. The 10-year expiry is a deliberate
# trade-off: rotating a per-user API key today is a manual flow
# (no UI), and the cost of a long-lived key inside a private
# tailnet is zero.
API_KEY=$(docker exec "$CONTAINER_NAME" /ko-app/headscale apikeys create -e 87600h -o json 2>/dev/null \
    | tr -d '"' | tr -d ' ')
if [ -z "$API_KEY" ]; then
    echo "failed to issue API key for $CONTAINER_NAME" >&2
    exit 1
fi

# The URL skygate uses to talk to the per-user instance. Inside the
# docker network, the container name is a stable DNS name. From
# outside the network (e.g. operator's curl on the host) the port
# is only exposed on the docker bridge (NOT on the host's public
# interface), so this URL is for skygate→headscale only.
URL="http://$CONTAINER_NAME:$HTTP_PORT"

# JSON output for the Go side. Keep the keys stable — internal/headscale
# provision.go parses them.
cat <<JSON
{
  "username": "$USERNAME",
  "container": "$CONTAINER_NAME",
  "url": "$URL",
  "api_key": "$API_KEY",
  "http_port": $HTTP_PORT,
  "grpc_port": $GRPC_PORT,
  "metrics_port": $METRICS_PORT,
  "base_domain": "$BASE_DOMAIN",
  "headscale_user_id": $HS_USER_ID
}
JSON
