#!/bin/bash
# scripts/verify_login.sh — v0.33.1.42 D1: cookie-based admin
# authentication for verify_post_deploy.sh.
#
# Why a separate file: the R31/R32/R34 checks all want to
# access admin pages, but POSTing to /login requires the
# exact form-encoded body + cookie-jar pattern. Embedding
# the same 5 lines of login boilerplate three times in
# verify_post_deploy.sh creates a maintenance hazard (the
# form fields have to stay in sync with the actual handler
# in internal/feature/auth/service.go; one drift and all
# three checks silently fail with "302 to /login").
#
# This helper:
#   1. Reads SKYGATE_ADMIN_USER + SKYGATE_ADMIN_PASSWORD from
#      env (REQUIRED — no defaults; the script refuses to run
#      if either is unset, to avoid hardcoding the live
#      operator credentials in a tracked file).
#   2. Reads SSH_HOST from $1 (positional) or $SSH_HOST env
#      (also REQUIRED — no defaults). Same rationale.
#   3. POSTs to /login on the VM (via direct ssh, same
#      pattern as R33/R34)
#   4. Captures the skygate_session cookie into a tmp
#      file the caller can re-use
#
# Usage:
#   SKYGATE_ADMIN_USER=skyadmin \
#     SKYGATE_ADMIN_PASSWORD='<set-via-env>' \
#     SSH_HOST='skyadmin@<VM_HOST>' \
#     bash scripts/verify_login.sh
#
# Or via the operator's .env:
#   set -a; . /home/skyadmin/skygate/.env; set +a
#   bash scripts/verify_login.sh
#
# The helper exits non-zero on login failure (caller checks
# $? to decide whether to skip the R-check or fail it).
# It also exits non-zero if any of the required env vars
# is missing — this is intentional; it prevents accidentally
# running with stale or hardcoded credentials.

set -e

# 2026-08-11: v0.34.0.1 — removed the live-credential
# defaults that shipped in v0.33.1.42. The operator's
# live admin password and SSH host were in tracked git
# history as default values (an artifact of the v0.33.1.42
# post-deploy fix that needed the live creds to make the
# R31/R32/R34 cookie-auth checks pass on the operator's
# actual deployment). Moving to env-var-only is the
# immediate mitigation; the full history-rewrite /
# global-squash to v1.0.0 is
# tracked in docs/PLANS.md.
if [ -z "${SKYGATE_ADMIN_USER:-}" ]; then
  echo "verify_login: SKYGATE_ADMIN_USER env var is required (no default; do not hardcode operator credentials in tracked files)" >&2
  exit 2
fi
# v1.0.0.15: accept SKYGATE_ADMIN_PASS (the .env convention)
# as a fallback for SKYGATE_ADMIN_PASSWORD. The operator's
# .env uses the short form; the verify scripts historically
# used the long form. Both should work.
if [ -z "${SKYGATE_ADMIN_PASSWORD:-}" ] && [ -n "${SKYGATE_ADMIN_PASS:-}" ]; then
  export SKYGATE_ADMIN_PASSWORD="$SKYGATE_ADMIN_PASS"
fi
if [ -z "${SKYGATE_ADMIN_PASSWORD:-}" ]; then
  echo "verify_login: SKYGATE_ADMIN_PASSWORD env var is required (no default; do not hardcode operator credentials in tracked files)" >&2
  exit 2
fi
if [ -z "${SSH_HOST:-}" ] && [ -z "${1:-}" ]; then
  echo "verify_login: SSH_HOST env var or positional \$1 is required (e.g. 'skyadmin@<VM_HOST>')" >&2
  exit 2
fi
# Positional $1 wins (matches the B61 contract: "user@host" as $1).
# Falls back to $SSH_HOST env var if $1 is empty.
SSH_HOST="${1:-${SSH_HOST:-}}"
ADMIN_USER="${SKYGATE_ADMIN_USER}"
ADMIN_PASS="${SKYGATE_ADMIN_PASSWORD}"

# Cookie file is per-process; the caller re-uses it for
# multiple R-checks within the same verify_post_deploy.sh
# run.
SKY_CK_FILE="${SKY_CK_FILE:-/tmp/_skygate_verify_cookie_$$}"

# 2026-08-11: v1.0.0.4 — also accept an explicit SSH_KEY via env
# (parent verify_post_deploy.sh resolves it the same way the
# host-side ssh fallback does). Without -i, on Windows hosts the
# subshell that runs this script does NOT inherit the parent's
# ssh-agent forwarding, and ssh falls back to "Permission denied
# (publickey,password)". The parent sets SSH_KEY=id_ed25519 path;
# pass it through.
SSH_KEY_FLAG=""
if [ -n "${SSH_KEY:-}" ] && [ -f "${SSH_KEY}" ]; then
  SSH_KEY_FLAG="-i ${SSH_KEY} -o IdentitiesOnly=yes"
fi

ssh $SSH_KEY_FLAG -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "$SSH_HOST" "rm -f /tmp/_skygate_verify_cookie" 2>/dev/null

# POST to /login. The form fields (username, password) match
# the handler in internal/feature/auth/service.go:103. The
# response sets a Set-Cookie: skygate_session=<jwt>; ... header
# that we capture into a cookie jar on the VM side, then
# base64-encode for transfer back to the verify_post_deploy.sh
# caller (which runs on the operator's machine, not the VM).
#
# The 302 redirect on success sends us to /dashboard. We follow
# it (curl -L) so the cookie jar is populated with the
# post-login Set-Cookie, not the pre-login response (which
# would only have last_username).
LOGIN_BODY=$(ssh $SSH_KEY_FLAG -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "$SSH_HOST" "curl -sS -L -c /tmp/_skygate_verify_cookie \
    -d 'username=${ADMIN_USER}&password=${ADMIN_PASS}&remember=0' \
    -o /dev/null -w '%{http_code}' \
    http://localhost:8080/login" 2>/dev/null || echo "000")

if [ "$LOGIN_BODY" != "200" ]; then
  echo "verify_login: POST /login returned $LOGIN_BODY (expected 200 after follow-redirect)" >&2
  echo "verify_login: check SKYGATE_ADMIN_USER + SKYGATE_ADMIN_PASSWORD env vars" >&2
  exit 1
fi

# Verify the cookie jar has a skygate_session entry. If the
# login succeeded but the cookie file is empty/missing, we
# have a different problem (e.g. /login returned 200 but
# didn't set a cookie — would mean a bug in the handler).
COOKIE_CONTENTS=$(ssh $SSH_KEY_FLAG -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "$SSH_HOST" "cat /tmp/_skygate_verify_cookie 2>/dev/null" 2>/dev/null || echo "")

if ! echo "$COOKIE_CONTENTS" | grep -q "skygate_session"; then
  echo "verify_login: cookie jar on VM has no skygate_session entry (login appeared to succeed but Set-Cookie missing)" >&2
  exit 1
fi

# Echo the cookie jar path. The caller re-uses it via
# `ssh $SSH_HOST "curl -sS -b $CK <url>"` (where $CK is the
# REMOTE path — curl runs on the VM, so it reads the local
# cookie file). We echo the REMOTE path, not the local one
# (the local machine has no /tmp/_skygate_verify_cookie).
echo "/tmp/_skygate_verify_cookie"
