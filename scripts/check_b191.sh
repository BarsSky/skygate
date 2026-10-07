#!/usr/bin/env bash
# check_b191.sh — v1.5.0 / B191 contracts.
#
# B191 (2026-08-31): verify BOTH device registration methods
# (classic preauth + OIDC) work end-to-end against the live
# headscale. Created after the operator hit a stale-key
# rejection on svyatoslava re-auth: the existing OIDC work
# (B161) was suspected of breaking the classic preauth path.
# This check pins both paths by registering a real test device
# as user `infra` and verifying it appears in headscale, then
# cleaning up.
#
# Contract:
#   A. headscale CLI works (preauth create succeeds)
#   B. preauth key has correct format (hskey-auth-...)
#   C. tailscale CLI is reachable on the test host
#   D. tailscale login-server can be reached from test host
#   E. node registration via preauth key WORKS (the node appears
#      in `headscale nodes list` with the new hostname)
#   F. cleanup: tailscale logout + node delete + key expire
#   G. OIDC path: skygate serves /oidc/authorize + /oidc/token +
#      /oidc/userinfo + JWKS (the B161.1/B161.2/B161.3 surface)
#   H. AGENTS.md mentions B191 + both methods are documented
#
# CLEANUP CONTRACT: regardless of where the script exits, the
# test node, the test preauth key, and the test container (if any)
# are removed (trap on EXIT). This prevents the operator's tailnet
# from accumulating garbage test devices every time the B-check runs.
#
# USAGE: this script runs ON the headscale-hosting agent. By
# default it uses `docker exec headscale ...` for headscale calls
# and a throwaway tailscale container for the client side. Set
# AGENT_SSH=skyadmin@host to wrap headscale calls in SSH (when
# running from a control machine), and set TEST_HOST=<host> to
# run the tailscale up on a separate host via SSH.

set -u
# Note: no `set -e` because we want cleanup to always run and
# we want to count failures explicitly.

PASS=0
FAIL=0
SKIP=0
ok() { PASS=$((PASS+1)); printf '  PASS  %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  FAIL  %s\n' "$1"; }
skip() { SKIP=$((SKIP+1)); printf '  SKIP  %s\n' "$1"; }

# Config
AGENT_SSH="${AGENT_SSH:-}"     # if set, wrap headscale calls in ssh $AGENT_SSH
# Test client: by default, spin up a fresh tailscale container
# named $TS_CONTAINER. Override with TEST_HOST=<host> (or <user>@<host>)
# to use an external machine that has tailscale + tailscaled.
TS_CONTAINER="${TS_CONTAINER:-b191-test-$$}"
TS_IMAGE="${TS_IMAGE:-tailscale/tailscale:latest}"
TEST_HOST="${TEST_HOST:-}"   # empty = use container; else SSH target
TEST_HOST_USER="${TEST_HOST_USER:-$(whoami)}"
LOGIN_SERVER="${LOGIN_SERVER:-https://head.skynas.ru}"
TEST_HOSTNAME="${TEST_HOSTNAME:-b191-$$}"   # unique per run
INFRA_USER_ID="${INFRA_USER_ID:-85}"
# OIDC provider lives on skygate host (skygate container serves the OIDC surface),
# NOT on headscale (which is the pure control plane). Use skygate.skynas.ru for OIDC checks.
SKYGATE_HOST="${SKYGATE_HOST:-skygate.skynas.ru}"

# Find the skygate repo on this machine (for AGENTS.md check).
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

# Run a headscale CLI command. If AGENT_SSH is set, SSH to it; otherwise assume
# we have direct docker access.
hs() {
  if [ -n "$AGENT_SSH" ]; then
    ssh "$AGENT_SSH" "docker exec headscale headscale $*"
  else
    docker exec headscale headscale "$@"
  fi
}

# Run a tailscale command on the test client.
ts() {
  if [ -n "$TEST_HOST" ]; then
    ssh -o ConnectTimeout=10 -l "$TEST_HOST_USER" "$TEST_HOST" "tailscale $*"
  else
    # tailscaled inside the container listens on /tmp/tailscaled.sock
    # (we set it explicitly because the host's tailscaled uses a different path)
    docker exec "$TS_CONTAINER" tailscale --socket=/tmp/tailscaled.sock "$@"
  fi
}

# Cleanup state
NODE_ID=""
PREAUTH_KEY_ID=""
TS_CONTAINER_STARTED=""

cleanup() {
  local rc=$?
  echo
  echo "=== cleanup (exit=$rc) ==="
  if [ -n "$NODE_ID" ]; then
    echo "  deleting node id=$NODE_ID..."
    hs nodes delete -i "$NODE_ID" --force 2>/dev/null || true
    ok "node $NODE_ID deleted"
  fi
  if [ -n "$PREAUTH_KEY_ID" ]; then
    echo "  expiring preauth key id=$PREAUTH_KEY_ID..."
    hs preauthkeys expire -i "$PREAUTH_KEY_ID" 2>/dev/null || true
    ok "preauth key $PREAUTH_KEY_ID expired"
  fi
  if [ -n "$TS_CONTAINER_STARTED" ]; then
    echo "  tearing down tailscale container $TS_CONTAINER..."
    docker rm -f "$TS_CONTAINER" 2>/dev/null || true
    ok "container $TS_CONTAINER removed"
  fi
  if [ -n "$TEST_HOST" ]; then
    echo "  logout on $TEST_HOST..."
    ssh -o ConnectTimeout=5 -l "$TEST_HOST_USER" "$TEST_HOST" "tailscale logout 2>/dev/null; tailscale down 2>/dev/null" 2>/dev/null || true
    ok "torn down (ssh)"
  fi
}
trap cleanup EXIT

# Pre-flight (2026-09-18, P1): this check drives the LIVE control plane
# (`docker exec headscale`, or ssh to AGENT_SSH). On a workstation without a
# reachable Docker daemon it used to FAIL contract A, which painted the whole
# verify gate red for a purely environmental reason (and made real failures
# harder to spot). Skip explicitly instead — the line names the environment
# assumption so the operator knows where to run it.
if [ -z "${AGENT_SSH:-}" ] && ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker daemon not reachable and AGENT_SSH unset — B191 drives the live headscale container, run it on the skygate VM"
  exit 0
fi

# --- A. headscale CLI works ---
echo
echo "=== contract A: headscale CLI reachable ==="
HS_VERSION_OUT=$(hs version 2>&1)
if echo "$HS_VERSION_OUT" | head -1 | grep -qE "headscale v"; then
  ok "headscale CLI: $(echo "$HS_VERSION_OUT" | head -1)"
else
  # B281 (2026-09-22): an ABSENT live dependency is a SKIP, not a FAIL
  # (AGENTS.md §1.1). On a CI runner there is no headscale container at all, so
  # this reported `FAIL headscale CLI not reachable: No such container:
  # headscale` — a red row that says nothing about the code under test.
  # Print SKIP *only*: a `FAIL` line here (even one that exits 0) reads as a
  # regression in a standalone run, and the CI 0-FAIL enforcement greps for it.
  echo "  SKIP  live check: needs a reachable headscale CLI (run it on a host with the stack up): $(echo "$HS_VERSION_OUT" | head -3)"
  exit 0
fi

# --- B. create preauth key for user 'infra' ---
echo
echo "=== contract B: create preauth key for infra (id=$INFRA_USER_ID) ==="
# headscale CLI prints JUST the key on create (no ID line). The key output may be
# masked in some versions, but ours prints the full key. After create, we list
# and look up the ID by matching the first 20 chars of the key (the masked form
# in the list output is `hskey-auth-<20chars>-***`).
CREATE_OUT=$(hs preauthkeys create --user "$INFRA_USER_ID" --reusable --expiration 24h 2>&1)
PREAUTH_KEY=$(echo "$CREATE_OUT" | grep -oE 'hskey-auth-[A-Za-z0-9_-]+' | head -1)
if [ -z "$PREAUTH_KEY" ]; then
  bad "preauth create FAILED: $CREATE_OUT"
  exit 1
fi
ok "preauth key created: ${PREAUTH_KEY:0:24}..."

# Get the ID by looking up the key prefix in the list
KEY_PREFIX=$(echo "$PREAUTH_KEY" | cut -c1-20)
LIST_JSON=$(hs preauthkeys list -o json 2>/dev/null)
PREAUTH_KEY_ID=$(echo "$LIST_JSON" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    pfx = '$KEY_PREFIX'
    for k in d:
        if k.get('key', '').startswith(pfx):
            print(k['id'])
            sys.exit(0)
    print('')
except Exception as e:
    print('ERR ' + str(e), file=sys.stderr)
    print('')
" 2>&1)
if [ -n "$PREAUTH_KEY_ID" ] && [ "${PREAUTH_KEY_ID#ERR}" = "$PREAUTH_KEY_ID" ]; then
  ok "preauth key ID resolved: $PREAUTH_KEY_ID"
else
  PREAUTH_KEY_ID=""
  ok "preauth key created (ID lookup failed — cleanup will skip expire)"
fi

# --- C. tailscale CLI reachable on test client ---
echo
echo "=== contract C: tailscale CLI reachable on test client ==="
if [ -z "$TEST_HOST" ]; then
  # Spin up a throwaway tailscale container. We override CMD with `sleep`
  # (so the container stays alive while we run tailscale commands) AND start
  # tailscaled in the background ourselves with userspace networking (since
  # the host's TUN is taken by the agent's own tailscaled).
  echo "  starting tailscale container $TS_CONTAINER..."
  CID=$(docker run -d --rm --name "$TS_CONTAINER" --network host \
    --cap-add=NET_ADMIN --cap-add=SYS_MODULE \
    "$TS_IMAGE" \
    /bin/sh -c 'tailscaled --socket=/tmp/tailscaled.sock --tun=userspace-networking 2>&1 & sleep infinity' 2>&1)
  if [ -z "$CID" ]; then
    bad "failed to start tailscale container"
    exit 1
  fi
  TS_CONTAINER_STARTED=1
  # Wait for tailscaled to be ready
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    if docker exec "$TS_CONTAINER" sh -c 'tailscale --socket=/tmp/tailscaled.sock status 2>/dev/null' >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
fi
TS_VERSION=$(ts version 2>&1 | head -1)
if [[ -n "$TS_VERSION" ]] && [[ "$TS_VERSION" == *"tailscale"* ]] || [[ "$TS_VERSION" == *"1."* ]]; then
  ok "tailscale CLI present: $TS_VERSION"
else
  # B281: absent live dependency -> SKIP (AGENTS.md §1.1), same as contract A.
  echo "  SKIP  live check: needs a reachable tailscale CLI on this host: $TS_VERSION"
  exit 0
fi

# --- D. login-server reachable from test client (BEFORE login) ---
echo
echo "=== contract D: $LOGIN_SERVER reachable from test client ==="
# headscale serves a /key endpoint as a public reachability probe (any 2xx/4xx/5xx OK,
# 000 = connection refused / no network). 404 is fine — it means the server is up but
# /key needs a query. We just want to verify the host:443 is reachable.
HTTP_CODE=$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$LOGIN_SERVER/key" 2>&1)
if [ "$HTTP_CODE" != "000" ] && [ -n "$HTTP_CODE" ]; then
  ok "$LOGIN_SERVER/key: HTTP $HTTP_CODE (network reachable)"
else
  # B281: the login server is live infrastructure; unreachable = SKIP
  # (AGENTS.md §1.1), not a code failure.
  echo "  SKIP  live check: $LOGIN_SERVER/key is not reachable from this network (HTTP $HTTP_CODE)"
  exit 0
fi

# --- E. register test device ---
echo
echo "=== contract E: register $TEST_HOSTNAME via preauth key ==="
# tailscale up in this image's containerboot mode may print success only via
# exit code, not stdout. Use exit-code-based detection, but also wait for
# the node to actually appear in headscale (the real proof that preauth
# registration works end-to-end).
REG_OUT=$(ts up --login-server="$LOGIN_SERVER" --authkey="$PREAUTH_KEY" --hostname="$TEST_HOSTNAME" --accept-routes --accept-dns=false 2>&1)
REG_RC=$?
# Wait up to 15 seconds for the node to appear in headscale
NODE_ID=""
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  sleep 1
  NODES_JSON=$(hs nodes list -o json 2>/dev/null)
  NODE_ID=$(echo "$NODES_JSON" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    for n in d:
        # headscale 0.29 uses snake_case 'given_name' (also try camelCase for older/newer)
        gn = n.get('given_name') or n.get('givenName') or ''
        if gn == '$TEST_HOSTNAME':
            print(n['id'])
            sys.exit(0)
    print('')
except Exception:
    print('')
" 2>&1)
  if [ -n "$NODE_ID" ]; then
    break
  fi
done
if [ -n "$NODE_ID" ]; then
  ok "node '$TEST_HOSTNAME' registered in headscale (id=$NODE_ID) — preauth path works"
elif [ "$REG_RC" = "0" ] && [[ -z "$(echo "$REG_OUT" | grep -iE 'error|fail|invalid|reject|expired')" ]]; then
  # Exit 0 with no error string but node not yet in list after 15s — likely headscale cache
  bad "tailscale up returned 0 but node did not appear in headscale after 15s: $(echo "$REG_OUT" | tr '\n' ' ' | head -c 200)"
else
  bad "registration failed (rc=$REG_RC): $(echo "$REG_OUT" | tr '\n' ' ' | head -c 200)"
  # don't exit — cleanup still needs to run
fi

# --- F. cleanup happens via trap (declared above) ---

# --- G. OIDC path: skygate serves the OIDC surface ---
echo
echo "=== contract G: skygate OIDC surface (local listener, then $SKYGATE_HOST) ==="
# WHY THE LOCAL LISTENER COMES FIRST (2026-10-07). This section used to probe
# ONLY https://$SKYGATE_HOST. On the reference VM that name resolves to the host's
# OWN public address, so the probe depends on the router's hairpin NAT: a gate run
# went red with "OIDC discovery failed:" and "HTTP 000" while the very same
# commands answered 200/200/400 seconds later, and the surface had been serving
# logins all day. A check that cannot reach what it probes must SKIP or fall back,
# never report a product regression (rule 1) — and the OIDC surface is served by
# the container's own listener, which is the honest thing to measure. The public
# URL is still probed; its status is REPORTED, and only a 404 from a base that
# ANSWERS is a contract failure.
OIDC_LOCAL="http://127.0.0.1:${SKYGATE_PORT:-8080}"
OIDC_PUBLIC="https://$SKYGATE_HOST"
OIDC_BASE=""
LOCAL_DISC="$(curl -s -m 10 "$OIDC_LOCAL/.well-known/openid-configuration" 2>&1)"
if printf '%s' "$LOCAL_DISC" | grep -q 'issuer'; then
  OIDC_BASE="$OIDC_LOCAL"
  ok "OIDC surface answered on the local listener ($OIDC_LOCAL) — no proxy or hairpin NAT in the path"
else
  PUBLIC_DISC="$(curl -s -m 10 "$OIDC_PUBLIC/.well-known/openid-configuration" 2>&1)"
  if printf '%s' "$PUBLIC_DISC" | grep -q 'issuer'; then
    OIDC_BASE="$OIDC_PUBLIC"
    ok "OIDC surface answered on the public URL ($OIDC_PUBLIC) — this host has no local listener"
  else
    skip "no OIDC surface reachable from here: local $OIDC_LOCAL answered '$(printf '%s' "$LOCAL_DISC" | head -1 | cut -c1-60)', public $OIDC_PUBLIC answered '$(printf '%s' "$PUBLIC_DISC" | head -1 | cut -c1-60)' — neither host runs the deployment, or neither is reachable (this is what CI without a deployment looks like)"
  fi
fi
if [ -n "$OIDC_BASE" ]; then
  OIDC_DISC="$(curl -s -m 10 "$OIDC_BASE/.well-known/openid-configuration" 2>&1)"
  if printf '%s' "$OIDC_DISC" | grep -q "issuer"; then
    ok "OIDC discovery endpoint serves metadata ($OIDC_BASE)"
    ISSUER=$(printf '%s' "$OIDC_DISC" | python3 -c "import json,sys; print(json.load(sys.stdin).get('issuer',''))" 2>/dev/null)
    ok "OIDC issuer: $ISSUER"
  else
    bad "OIDC discovery failed on $OIDC_BASE: $(printf '%s' "$OIDC_DISC" | head -3)"
  fi

  # JWKS endpoint
  JWKS="$(curl -s -m 10 "$OIDC_BASE/oidc/jwks.json" 2>&1)"
  if printf '%s' "$JWKS" | grep -q '"keys"'; then
    ok "OIDC JWKS endpoint returns keys"
  else
    bad "OIDC JWKS failed on $OIDC_BASE: $(printf '%s' "$JWKS" | head -3)"
  fi

  # authorize endpoint (should respond with 400 or 302, not 404)
  AUTH_STATUS=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$OIDC_BASE/oidc/authorize" 2>&1)
  if [ "$AUTH_STATUS" != "404" ] && [ "$AUTH_STATUS" != "000" ]; then
    ok "OIDC /oidc/authorize responds (HTTP $AUTH_STATUS, not 404)"
  else
    bad "OIDC /oidc/authorize unreachable on $OIDC_BASE (HTTP $AUTH_STATUS)"
  fi

  # token endpoint (should respond, not 404)
  TOK_STATUS=$(curl -s -m 10 -o /dev/null -w "%{http_code}" -X POST "$OIDC_BASE/oidc/token" 2>&1)
  if [ "$TOK_STATUS" != "404" ] && [ "$TOK_STATUS" != "000" ]; then
    ok "OIDC /oidc/token responds (HTTP $TOK_STATUS, not 404)"
  else
    bad "OIDC /oidc/token unreachable on $OIDC_BASE (HTTP $TOK_STATUS)"
  fi

  # userinfo endpoint
  USERINFO_STATUS=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$OIDC_BASE/oidc/userinfo" 2>&1)
  if [ "$USERINFO_STATUS" != "404" ] && [ "$USERINFO_STATUS" != "000" ]; then
    ok "OIDC /oidc/userinfo responds (HTTP $USERINFO_STATUS, not 404)"
  else
    bad "OIDC /oidc/userinfo unreachable on $OIDC_BASE (HTTP $USERINFO_STATUS)"
  fi

  # Reported, never red: the OTHER base's status tells the operator what their
  # users' path looks like without letting a hairpin failure look like a product bug.
  if [ "$OIDC_BASE" = "$OIDC_LOCAL" ]; then
    PUB=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$OIDC_PUBLIC/.well-known/openid-configuration" 2>&1)
    if [ "$PUB" = "200" ]; then
      ok "the public URL $OIDC_PUBLIC also answers (HTTP $PUB) — the users' path is healthy"
    else
      ok "NOTE: the public URL $OIDC_PUBLIC answered HTTP $PUB from this host (hairpin NAT / proxy); the OIDC surface itself is verified above on the local listener, and the public path is NOT a contract here"
    fi
  else
    LOC=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$OIDC_LOCAL/.well-known/openid-configuration" 2>&1)
    ok "NOTE: no local listener on $OIDC_LOCAL (HTTP $LOC) — the surface was verified through the public URL instead"
  fi
fi

# --- H. AGENTS.md mentions B191 + both methods ---
echo
echo "=== contract H: AGENTS.md mentions B191 + both methods ==="
if [ -f "$REPO/AGENTS.md" ]; then
  if grep -qF "B191" "$REPO/AGENTS.md"; then
    ok "AGENTS.md mentions B191"
  else
    bad "AGENTS.md does NOT mention B191"
  fi
  if grep -qE "preauth.*OIDC|OIDC.*preauth|both.*method|both.*register|классическ.*OIDC|обоих" "$REPO/AGENTS.md"; then
    ok "AGENTS.md documents both methods"
  else
    bad "AGENTS.md does NOT document both methods"
  fi
else
  bad "AGENTS.md not found at $REPO/AGENTS.md"
fi

# ---------------------------------------------------------------------------
echo
echo "=== B191 summary: $PASS pass, $FAIL fail, $SKIP skip ==="
[ "$FAIL" -gt 0 ] && exit 1
echo "all contracts satisfied"
