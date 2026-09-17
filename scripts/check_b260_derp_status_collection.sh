#!/usr/bin/env bash
# check_b260_derp_status_collection.sh
#
# 2026-09-17 (B260) — regression check for the live /admin/derp
# "DERPER-SERVICE: stopped" bug. The pre-fix derp.go had:
#
#   1. derpURL := "http://192.0.2.1:8443"   (192.0.2.1 = RFC 5737
#      TEST-NET-1, not routable; the URL was hardcoded)
#   2. httpGet plain HTTP only (derper on :443 requires TLS
#      post-B-derper-cert)
#   3. bundledDERPPortFromDB LIMIT 1 without ORDER BY
#      (non-deterministic when the data has multiple
#      is_bundled=1 rows — flipped between ":443" and ":8443"
#      on every refresh)
#
# All three caused all 6 derper debug probes to silently fail,
# so /admin/derp always rendered "DERPER-SERVICE: stopped"
# regardless of derper's actual state. The page also showed
# ":3478 closed" and "0 active connections" because the
# same /debug/vars + /active-conn probes failed for the
# same reason.
#
# B260 fix:
#   - derpURL = "https://" + bundledHostname + ":" + bundledPort
#     (resolves via DB → env → default; deterministic ordering
#     via ORDER BY id ASC LIMIT 1)
#   - httpGet now supports https:// scheme with TLS-aware
#     transport; InsecureSkipVerify=true ONLY when the URL
#     host is a literal IP (cert CN mismatch is unavoidable)
#   - resolveDERPHostname mirrors resolveDERPPort's resolution
#     shape (DB → env → "")
#
# What this checks:
#   A) derpURL no longer hardcodes 192.0.2.1
#   B) derpURL uses https:// scheme (not http://)
#   C) bundledDERPPortFromDB has ORDER BY id
#   D) bundledDERPHostnameFromDB helper exists
#   E) resolveDERPHostname resolves DB → env → ""
#   F) httpGet supports https:// scheme with TLS
#   G) httpGet skips cert verification ONLY for IP hosts
#   H) unit tests cover the env-var + nil-DB fallback paths
#   I) go vet ./internal/feature/admin/... clean
#   J) go test passes (B260 env-only unit tests)
#   K) live-state prompt: ssh + check /admin/derp shows
#       ":443 listening" + "STUN UDP :3478 listening" +
#       Running=true (operator runs after deploy + first
#       page render)
#
# Run order: A-J as source-contract gates (run on every commit),
# K after the operator deploys and opens /admin/derp once.

set -euo pipefail
# Disable bash pathname expansion so patterns like "/data/*" in
# grep invocations don't get glob-expanded into the list of
# matching files.
set -f

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# Find Go (git-bash / MSYS2 subshell doesn't inherit Windows PATH).
if ! command -v go >/dev/null 2>&1; then
  for cand in '/mnt/c/Program Files/Go/bin' '/c/Program Files/Go/bin'; do
    if [ -f "$cand/go.exe" ] && "$cand/go.exe" version >/dev/null 2>&1; then
      export PATH="$cand:$PATH"
      break
    fi
  done
fi

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

DERP_GO="internal/feature/admin/derp.go"
DERP_RESOLVE="internal/feature/admin/derp_status_resolve.go"
TEST_FILE="internal/feature/admin/derp_status_resolve_b260_test.go"

hdr "B260 — /admin/derp status collection URL fix"

# --- A: derpURL no longer hardcodes 192.0.2.1 ---
# Match the actual `derpURL := "http://192.0.2.1...` assignment
# line (the pre-fix bug). The grep is constrained to assignment
# patterns to avoid matching (1) the comment explaining the bug,
# (2) the legitimate `net.DialTimeout("udp", "192.0.2.1:80", ...)`
# used as a fake STUN probe target (line ~318).
if grep -E '^\s*derpURL\s*:=\s*"http://192\.0\.2\.1' "$DERP_GO" >/dev/null 2>&1; then
  fail "A: derp.go still assigns derpURL := \"http://192.0.2.1...\" — derper probes still go to TEST-NET-1"
else
  ok "A: derpURL no longer hardcodes 192.0.2.1 (the comment + STUN-probe references are fine)"
fi

# --- B: derpURL uses https:// scheme ---
if grep -q 'derpURL\s*:=\s*"https://' "$DERP_GO"; then
  ok "B: derpURL is built with https:// scheme (post-B-derper-cert derper requires TLS)"
else
  fail "B: derpURL doesn't use https:// scheme — derper rejects plain HTTP"
fi

# --- C: bundledDERPPortFromDB has ORDER BY id ---
if grep -q 'WHERE is_bundled = 1 AND enabled = 1' "$DERP_RESOLVE" \
   && grep -q 'ORDER BY id ASC' "$DERP_RESOLVE"; then
  ok "C: bundledDERPPortFromDB uses ORDER BY id ASC (deterministic when multiple bundled rows exist)"
else
  fail "C: bundledDERPPortFromDB missing ORDER BY id ASC — dual-bundled-row data still flips UI between :443 and :8443"
fi

# --- D: bundledDERPHostnameFromDB helper exists ---
if grep -q 'func bundledDERPHostnameFromDB' "$DERP_RESOLVE"; then
  ok "D: bundledDERPHostnameFromDB helper exists (mirror of bundledDERPPortFromDB for hostname)"
else
  fail "D: bundledDERPHostnameFromDB missing — collectDerpStatus can't build the URL"
fi

# --- E: resolveDERPHostname resolves DB → env → "" ---
if grep -q 'func resolveDERPHostname' "$DERP_RESOLVE"; then
  # Check the resolution order: DB → env → ""
  if grep -A20 'func resolveDERPHostname' "$DERP_RESOLVE" | grep -q 'bundledDERPHostnameFromDB' \
     && grep -A20 'func resolveDERPHostname' "$DERP_RESOLVE" | grep -q 'SKYGATE_DERP_HOSTNAME'; then
    ok "E: resolveDERPHostname resolves DB → env → \"\" (mirror of resolveDERPPort shape)"
  else
    fail "E: resolveDERPHostname missing one of DB/env fallback layers"
  fi
else
  fail "E: resolveDERPHostname missing — collectDerpStatus has no hostname source"
fi

# --- F: httpGet supports https:// scheme with TLS ---
if grep -q 'u\.Scheme == "https"' "$DERP_GO" \
   && grep -q 'http\.Transport{' "$DERP_GO" \
   && grep -q 'TLSClientConfig' "$DERP_GO"; then
  ok "F: httpGet supports https:// scheme with TLS-aware transport"
else
  fail "F: httpGet doesn't have TLS-aware transport — HTTPS probes still fail"
fi

# --- G: httpGet skips cert verification ONLY for IP hosts ---
if grep -q 'net\.ParseIP(hostnameOnly)' "$DERP_GO" \
   && grep -q 'InsecureSkipVerify' "$DERP_GO"; then
  ok "G: httpGet uses InsecureSkipVerify ONLY when URL host is a literal IP (cert CN mismatch unavoidable)"
else
  fail "G: httpGet doesn't gate InsecureSkipVerify on IP-vs-hostname (cert validation broken for hostname-based probes)"
fi

# --- H: unit tests cover env-var + nil-DB fallback paths ---
if [ -f "$TEST_FILE" ]; then
  tests_present=0
  for t in NilDBEnvFallback NilDBEmptyWhenNoEnv EnvWhitespacesTrimming; do
    if grep -q "TestResolveDERPHostname_$t" "$TEST_FILE"; then
      tests_present=$((tests_present + 1))
    fi
  done
  if [ "$tests_present" -ge 3 ]; then
    ok "H: unit test file present with the env-fallback pattern ($tests_present/3 cases detected)"
  else
    fail "H: only $tests_present/3 B260 test cases present — fix the gaps before commit"
  fi
else
  fail "H: $TEST_FILE missing — B260 has no regression lock-in"
fi

# --- I: go vet clean ---
if command -v go >/dev/null 2>&1; then
  if go vet ./internal/feature/admin/... >/dev/null 2>&1; then
    ok "I: go vet ./internal/feature/admin/... clean"
  else
    fail "I: go vet ./internal/feature/admin/... reported issues"
  fi
else
  ok "I: go vet skipped (go not reachable in this bash PATH — re-run manually: go vet ./internal/feature/admin/...)"
fi

# --- J: go test the B260 unit tests ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/feature/admin/ -run 'TestResolveDERPHostname' -count=1 >/dev/null 2>&1; then
    ok "J: go test ./internal/feature/admin/ -run TestResolveDERPHostname -count=1 passes"
  else
    fail "J: go test failed — run manually: go test ./internal/feature/admin/ -run TestResolveDERPHostname -count=1 -v"
  fi
else
  ok "J: go test skipped (go not reachable in this bash PATH — re-run manually: go test ./internal/feature/admin/ -run TestResolveDERPHostname -v)"
fi

# --- K: live-state prompt ---
hdr "K: live-state (operator-side, post-deploy)"
cat <<'NOTE'
  Confirm on agent VM (192.168.13.69) that the /admin/derp
  status section now reflects derper's actual state. Run
  after deploy + first page render:

    ssh hermes-debug@192.168.13.69 \
      'curl -sk https://derp.skynas.ru/derp -i \
         -H "Upgrade: websocket" -H "Connection: Upgrade" \
         -H "Sec-WebSocket-Version: 13" \
         -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
         | head -3'

  Then open https://head.skynas.ru/admin/derp in a browser.

  Pre-fix:  "DERPER-SERVICE: stopped", ":8443", ":3478 closed",
            "0 active connections"
  Post-fix: "DERPER-SERVICE: running", ":443", ":3478 listening",
            nonzero "active connections" if any Tailscale
            client is currently using the DERP relay
NOTE

ok "K: live-state note printed (run manually after deploy)"

printf '\n\033[32mB260 regression check passed — safe to commit\033[0m\n'
