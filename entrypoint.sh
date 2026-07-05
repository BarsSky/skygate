#!/bin/sh
set -e
cd /app
echo "Downloading Go modules..."
go mod download || true
go mod tidy || true
apk add --no-cache openssh-client 2>/dev/null
echo "Building Skygate..."
go build -o /app/skygate ./cmd/skygate || { echo "BUILD FAILED"; exit 1; }
chmod +x /app/skygate
echo "Skygate ready, starting..."
exec /app/skygate
