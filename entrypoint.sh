#!/bin/sh
set -e
cd /app
echo "Downloading Go modules..."
go mod download || true
go mod tidy || true
apk add --no-cache openssh-client git 2>/dev/null
echo "Building Skygate..."
# 2026-07-11: inject build label from git so the web footer + telegram
# /version reflect the real tag/commit. .git is bind-mounted via
# docker-compose (`./:/app`); if it's missing (e.g. CI build from a
# tarball), fall back to "dev". The alpine base image does NOT include
# git, so we install it via apk above.
GIT_VER=$(git describe --tags --always 2>/dev/null || echo "dev")
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-X main.version=${GIT_VER} -X main.commit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}"
echo "  version=${GIT_VER} commit=${GIT_COMMIT} built=${BUILD_TIME}"
go build -ldflags "${LDFLAGS}" -o /app/skygate ./cmd/skygate || { echo "BUILD FAILED"; exit 1; }
chmod +x /app/skygate
echo "Skygate ready, starting..."
exec /app/skygate
