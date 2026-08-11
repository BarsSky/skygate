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
#      env (fall back to "admin" / "skygate_admin_pass" if
#      unset — matches the live .env)
#   2. POSTs to /login on the VM (via direct ssh, same
#      pattern as R33/R34)
#   3. Captures the skygate_session cookie into a tmp
#      file the caller can re-use
#
# Usage:
#   . scripts/verify_login.sh   # sources $SKY_CK_FILE
#   ... curl -b "$SKY_CK_FILE" http://localhost:8080/admin/whatever
#
# Or as a one-liner:
#   CK=$(bash scripts/verify_login.sh)
#   ssh ... "curl -sS -b $CK http://localhost:8080/admin/services"
#
# The helper exits non-zero on login failure (caller checks
# $? to decide whether to skip the R-check or fail it).

set -e

ADMIN_USER="${SKYGATE_ADMIN_USER:-skyadmin}"
ADMIN_PASS="${SKYGATE_ADMIN_PASSWORD:-SkyAdm_1782736105_b6e7aac4}"
SSH_HOST="${SSH_HOST:-skyadmin@192.168.13.69}"

# Cookie file is per-process; the caller re-uses it for
# multiple R-checks within the same verify_post_deploy.sh
# run.
SKY_CK_FILE="${SKY_CK_FILE:-/tmp/_skygate_verify_cookie_$$}"

ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
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
LOGIN_BODY=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
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
COOKIE_CONTENTS=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
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
