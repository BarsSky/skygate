#!/bin/bash
# check_b168.sh — B168 (v1.5.2) live OIDC e2e on a public hostname
#
# B167 closed the OIDC config auto-sync (admin side: write
# headscale.conf + restart headscale). B168 closes the
# operator side: wire the OIDC endpoints on a public
# hostname (skygate.skynas.ru) so a Tailscale client (in
# the operator's browser) can actually reach the
# /oidc/authorize + /login + /oidc/callback flow.
#
# The B-check is split into:
#  A. Source-contract (the nginx snippet + the setup
#     script + the deploy/oidc-sync.sh re-use all exist)
#  B. Wiring (the setup script is idempotent + handles
#     the 3 failure modes: DNS not propagated, nginx
#     not reloaded, skygate .env not updated)
#  C. The setup script's audit log row
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source files exist + have the right structure"

# A.1 — the nginx snippet.
if [ -f deploy/snippets/nginx-skygate-oidc.conf ]; then
    ok "deploy/snippets/nginx-skygate-oidc.conf exists"
else
    bad "deploy/snippets/nginx-skygate-oidc.conf MISSING (operator needs the server block to paste into their nginx)"
fi

# A.2 — the snippet must have a `server { listen 443 ... server_name
# skygate.skynas.ru; ... }` block.
if grep -qE 'listen 443 ssl http2' deploy/snippets/nginx-skygate-oidc.conf \
   && grep -qE 'server_name skygate\.skynas\.ru' deploy/snippets/nginx-skygate-oidc.conf; then
    ok "nginx snippet has 'listen 443 + server_name skygate.skynas.ru'"
else
    bad "nginx snippet missing the HTTPS server block (operator can't paste a broken config)"
fi

# A.3 — the snippet must proxy the 5 OIDC endpoints + the
# /admin/oidc + /admin/oidc/sync paths (so the operator can
# verify the live config from the web UI).
for path in "/.well-known/openid-configuration" "/oidc/jwks.json" "/oidc/" "/admin/oidc" "/admin/oidc/sync"; do
    if grep -qE "location[ =]+${path}" deploy/snippets/nginx-skygate-oidc.conf; then
        ok "nginx snippet routes '$path' to skygate"
    else
        bad "nginx snippet missing location for '$path' (the OIDC flow or the admin page would 404)"
    fi
done

# A.4 — the snippet must set X-Forwarded-Proto (the skygate
# OIDC code uses it to decide http:// vs https:// in the
# issuer claim).
if grep -qE 'X-Forwarded-Proto' deploy/snippets/nginx-skygate-oidc.conf; then
    ok "nginx snippet sets X-Forwarded-Proto (skygate needs it to render https:// in the issuer claim)"
else
    bad "nginx snippet does NOT set X-Forwarded-Proto (the issuer would render as http:// — breaks the Tailscale login flow)"
fi

# A.5 — the setup script.
if [ -x deploy/scripts/setup-skygate-public.sh ] && head -1 deploy/scripts/setup-skygate-public.sh | grep -qE '^#!/usr/bin/env bash$|^#!/bin/bash$'; then
    ok "deploy/scripts/setup-skygate-public.sh exists + is executable + bash shebang"
else
    bad "deploy/scripts/setup-skygate-public.sh MISSING or not executable or not bash"
fi

# A.6 — the 5-step flow.
EXPECTED_STEPS=("[1/5]" "[2/5]" "[3/5]" "[4/5]" "[5/5]")
for step in "${EXPECTED_STEPS[@]}"; do
    if grep -qF "$step" deploy/scripts/setup-skygate-public.sh; then
        ok "setup script has step $step"
    else
        bad "setup script missing step $step"
    fi
done

# A.7 — the script must call deploy/oidc-sync.sh (B167's
# script) to push the new config to headscale. A regression
# would mean B168 doesn't reuse B167 — and we'd be
# duplicating the bash-merging logic.
if grep -qE 'oidc-sync\.sh' deploy/scripts/setup-skygate-public.sh; then
    ok "setup script reuses deploy/oidc-sync.sh (no duplication of the bash-merging logic)"
else
    bad "setup script does NOT call deploy/oidc-sync.sh (would duplicate the bash-merging logic)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: idempotency + safety"

# B.1 — the script must be idempotent (re-running with the
# same args is a no-op, not a destructive re-apply). A
# regression would re-write the .env + re-restart skygate on
# every cron tick.
if grep -qE '.env already up to date' deploy/scripts/setup-skygate-public.sh; then
    ok "setup script is idempotent (skips when .env is already up to date)"
else
    bad "setup script is NOT idempotent (re-running would re-write .env + re-restart skygate every time)"
fi

# B.2 — the script must validate the discovery doc is
# reachable BEFORE updating .env. A regression that updates
# .env first would leave skygate advertising a non-existent
# URL until the operator fixes DNS.
if grep -qE 'validate.*reachable|discovery.*200' deploy/scripts/setup-skygate-public.sh; then
    ok "setup script validates discovery doc reachable BEFORE updating .env"
else
    bad "setup script does NOT validate the discovery doc (would leave skygate advertising a broken issuer URL)"
fi

# B.3 — the script must verify the new issuer is reported
# by skygate AFTER the restart (round-trip check: the .env
# update took effect, the new RSA keypair is loaded, the
# discovery doc advertises the new URL). A regression that
# only checks /healthz would silently leave skygate
# advertising the old placeholder.
if grep -qE 'NEW_ISSUER|discovery doc reports issuer' deploy/scripts/setup-skygate-public.sh; then
    ok "setup script verifies the discovery doc reports the new issuer AFTER the restart"
else
    bad "setup script does NOT verify the new issuer (would silently leave skygate on the old placeholder)"
fi

# B.4 — the script must back up .env before writing.
if grep -qE 'pre-setup-public' deploy/scripts/setup-skygate-public.sh; then
    ok "setup script backs up .env before writing (pre-setup-public.YYYYMMDDHHMMSS)"
else
    bad "setup script does NOT back up .env before writing (operator would lose the old .env on a typo)"
fi

# ---------------------------------------------------------------------------
hdr "contract C: audit log row"

# C.1 — the script must write an 'oidc_setup' audit row so
# /admin/audit surfaces the public OIDC wiring event.
if grep -qE "'oidc_setup'|'oidc_setup'," deploy/scripts/setup-skygate-public.sh; then
    ok "setup script writes 'oidc_setup' audit row"
else
    bad "setup script does NOT write an audit row (the wiring event would be invisible in /admin/audit)"
fi

# ---------------------------------------------------------------------------
hdr "summary"
echo "B168: setup-skygate-public.sh + deploy/snippets/nginx-skygate-oidc.conf (live OIDC e2e on a public hostname)"
echo "all contracts satisfied"
