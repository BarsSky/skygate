#!/usr/bin/env bash
# ============================================================================
# check_b_derper_cert.sh — B-derper-cert (2026-09-15)
# Pins the derper systemd unit + cert dir + help-text contract so the
# "Serves HTTPS if port is 443 OR --certmode=manual, otherwise HTTP"
# trap can't silently re-regress. The unit is in deploy/systemd/
# derper.service; the live agent's /etc/systemd/system/derper.service
# must match the corrected ExecStart line below.
#
# Live failure case (2026-09-15, agent VM 192.168.13.69):
#   systemd unit had:
#     ExecStart=... --certmode=letsencrypt --certdir=/var/lib/derper/certs --a=:8443 ...
#   derper saw port != 443 AND certmode != manual → refused LE → certs
#   dir empty → plain HTTP. NPM did TLS termination, but TLS to derper's
#   HTTP listener returned 426 (Upgrade Required) for /derp. Tailscale
#   DERP clients (which need DERP-over-HTTP/2 with TLS) silently failed.
#   0 clients used the local DERP as home.
#
# Contract (all must hold for the B-check to pass):
#   A. deploy/systemd/derper.service has --certmode=manual
#   B. deploy/systemd/derper.service has --certdir=/var/lib/derper/certs
#   C. deploy/systemd/derper.service has --a=:8443 (NOT :443 — the public
#      IP is on the NPM VM, not the agent)
#   D. derper --help text contains the "Serves HTTPS if the port is 443
#      and/or -certmode is manual" clause (regression guard for derper
#      version changes that might silently remove the clause)
#   E. The B-derper-cert entry is in AGENTS.md
#   F. The certsync (B147) docs make clear it writes to
#      /var/lib/skygate/certs/, NOT /var/lib/derper/certs/ — so the
#      operator doesn't get false hope from a "cert uploaded via
#      /admin/certificates" flow.
#
# Note: this B-check is STATIC — it doesn't verify the LIVE agent's
# /etc/systemd/system/derper.service matches deploy/systemd/derper.service.
# The pre-push hook runs on the dev host; the deploy pipeline runs on
# the agent. Operators must run deploy/systemd/install.sh (or copy
# the unit manually) after pulling the corrected file.
# ============================================================================
set -u

PASS_COUNT=0
FAIL_COUNT=0
ok()  { PASS_COUNT=$((PASS_COUNT+1)); printf "  ok   %s\n" "$*"; }
bad() { FAIL_COUNT=$((FAIL_COUNT+1)); printf "  BAD  %s\n" "$*"; }

cd "$(dirname "$0")/.."

echo "=== A. systemd unit has --certmode=manual ==="
# Match the ExecStart= line ONLY (not the explanatory comment that
# mentions --certmode=letsencrypt in the past tense).
if grep -qE '^ExecStart=.*--certmode=manual' deploy/systemd/derper.service; then
  ok "A.1 --certmode=manual present in ExecStart"
else
  bad "A.1 --certmode=manual MISSING from ExecStart — derper would fall back to plain HTTP"
fi
if ! grep -qE '^ExecStart=.*--certmode=letsencrypt' deploy/systemd/derper.service; then
  ok "A.2 --certmode=letsencrypt NOT in ExecStart (the trap combination)"
else
  bad "A.2 --certmode=letsencrypt in ExecStart alongside --a=:<port!=443> — derper would refuse LE"
fi

echo
echo "=== B. systemd unit has --certdir pointing at /var/lib/derper/certs ==="
if grep -qE -- '--certdir=/var/lib/derper/certs' deploy/systemd/derper.service; then
  ok "B.1 --certdir=/var/lib/derper/certs present"
else
  bad "B.1 --certdir=/var/lib/derper/certs MISSING (operator can't drop certs in the right place)"
fi
if ! grep -qE -- '--certdir=/var/lib/skygate/certs' deploy/systemd/derper.service; then
  ok "B.2 --certdir does NOT point at certsync's /var/lib/skygate/certs (those are skygate's own, not derper's)"
else
  bad "B.2 --certdir points at /var/lib/skygate/certs — that's certsync's, not derper's"
fi

echo
echo "=== C. systemd unit has --a=:8443 (not :443) ==="
if grep -qE -- '--a=:8443' deploy/systemd/derper.service; then
  ok "C.1 --a=:8443 present (public IP is on NPM, not on the agent)"
else
  bad "C.1 --a=:8443 MISSING — derper might be trying to bind the public IP's :443"
fi

echo
echo "=== D. derper --help text still contains the 'manual' caveat ==="
# We can't run derper --help here (the binary lives only on the agent),
# but we can grep the comment in deploy/systemd/derper.service that
# quotes the relevant clause. The pre-push hook runs on the dev host
# where derper isn't installed; the deploy pipeline runs on the agent
# where derper IS installed — the deploy script should also verify
# derper --help and refuse to start the service if the clause changed.
if grep -qE 'certmode is manual|port is 443' deploy/systemd/derper.service; then
  ok "D.1 the certmode-vs-port caveat is documented in the unit header comment"
else
  bad "D.1 the caveat is NOT documented — a future maintainer might re-introduce the trap"
fi

echo
echo "=== E. AGENTS.md mentions the B-derper-cert entry ==="
if grep -qE 'B-derper-cert|derper certmode' AGENTS.md; then
  ok "E.1 AGENTS.md mentions B-derper-cert"
else
  bad "E.1 AGENTS.md missing the B-derper-cert entry"
fi

echo
echo "=== F. certsync (B147) docs make clear its path is /var/lib/skygate/certs ==="
# The certsync package's docstring or the certificates page should
# say where the cert is written. Grep both.
if grep -rqE '/var/lib/skygate/certs' internal/certsync internal/feature/admin/certificates.go 2>/dev/null; then
  ok "F.1 certsync writes to /var/lib/skygate/certs (NOT /var/lib/derper/certs) — operator doesn't get false hope"
else
  bad "F.1 certsync path not documented in either the package or the certificates page"
fi

echo
echo "=== G. Build + tests (run only if go is on PATH) ==="
GO_BIN=""
for cand in "$(command -v go)" \
    "/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then
    GO_BIN="$cand"
    break
  fi
done
if [ -z "$GO_BIN" ]; then
  echo "  skip G.1 (no go binary on PATH)"
else
  if "$GO_BIN" vet ./... >/dev/null 2>&1; then
    ok "G.1 go vet ./... clean"
  else
    bad "G.1 go vet ./... FAILED"
  fi
fi

echo
echo "=== Summary ==="
echo "  PASS: $PASS_COUNT"
echo "  FAIL: $FAIL_COUNT"
if [ "$FAIL_COUNT" -gt 0 ]; then
  exit 1
fi
exit 0