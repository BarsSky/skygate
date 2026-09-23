#!/usr/bin/env bash
# check_b307_stun_fingerprint.sh
#
# 2026-09-23 (B307, v1.5.72) — the STUN probe must speak the dialect the relay
# requires (SOFTWARE + FINGERPRINT).
#
# LIVE ROOT CAUSE (agent VM, measured — not theorised):
#
#   * /admin/derp showed "STUN UDP :3478 closed" on a relay whose STUN answers
#     real clients (tailscale netcheck scored every public region through it);
#   * the relay's OWN counters (read from /debug/vars over loopback) moved like
#     this while probing 127.0.0.1:3478:
#
#         before:                              {"not_stun":20,"success":0}
#         4 bare RFC 5389 Binding Requests  →  {"not_stun":24,"success":0}
#         1 request with SOFTWARE+FINGERPRINT → {"not_stun":25,"success":1}
#
#   * the source of tailscale.com/net/stun explains it: ParseBindingRequest
#     returns ErrNoFingerprint ("STUN request didn't end in fingerprint") and
#     ErrWrongFingerprint; stunserver.Serve counts both as not_stun and never
#     answers. Tailscale's own client (net/stun.Request) always sends SOFTWARE +
#     FINGERPRINT, which is why netcheck succeeds where skygate's bare request
#     was silently dropped;
#   * python proof with the same bytes skygate now builds:
#         tailscale-shaped: REPLY 44 bytes (txid match=True)
#         bare rfc5389   : silent
#
# Before this fix the tile reported a FALSE NEGATIVE (a red STUN tile on a
# healthy relay) — the very class B265 set out to eliminate. Rebuilding derper
# from upstream did NOT change it (fresh v1.70.0 and 1.102.4 behave identically),
# which is what pointed the investigation back at the probe.
#
# CONTRACTS
#   A. the default request carries SOFTWARE + a VALID FINGERPRINT
#   B. the bare shape survives as a documented fallback
#   C. a fingerprint-requiring server (derper's rule) is answered
#   D. failures name both request shapes
#   E. the live evidence is recorded in the source
#   F. the Go tests exist and pass
#   G. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B307: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SRC=internal/feature/admin/derp_stun.go
TESTFILE=internal/feature/admin/derp_stun_b307_test.go
WIRETEST=internal/feature/admin/derp_stun_b265_test.go

hdr "B307 — the STUN probe sends SOFTWARE + FINGERPRINT"

# --- A: the default shape ------------------------------------------------------
if grep -q 'stunAttrSoftware' "$SRC" && grep -q 'stunAttrFingerprint' "$SRC"; then
  ok "A1: the SOFTWARE/FINGERPRINT attribute constants exist"
else
  bad "A1: the fingerprint attributes are missing"
fi
if grep -q 'crc32.ChecksumIEEE(pkt) ^ stunFingerprintXORMask' "$SRC"; then
  ok "A2: the fingerprint is CRC32 XOR 0x5354554e (what stun.ParseBindingRequest verifies)"
else
  bad "A2: the fingerprint is not computed the way Tailscale verifies it"
fi
if grep -q 'stunSoftwareValue' "$SRC" && grep -q 'stunSoftwareValue *=' "$SRC"; then
  ok "A3: SOFTWARE carries the value Tailscale's own client sends"
else
  bad "A3: SOFTWARE is missing or unnamed"
fi
if grep -q 'uint16(total-stunHeaderLen)' "$SRC"; then
  ok "A4: the header length counts the fingerprint attribute (RFC 5389 §15.5)"
else
  bad "A4: the header length does not include the fingerprint"
fi

# --- B: the fallback ----------------------------------------------------------
if grep -q 'func buildSTUNBareBindingRequest() (pkt, txID \[\]byte, err error)' "$SRC"; then
  ok "B1: the bare RFC 5389 shape survives as a fallback"
else
  bad "B1: the bare shape was dropped (a generic STUN server would no longer be probed)"
fi
if grep -q '"fingerprint", buildSTUNBindingRequest' "$SRC" && grep -q '"bare", buildSTUNBareBindingRequest' "$SRC"; then
  ok "B2: the probe tries the fingerprint shape FIRST, then the bare one"
else
  bad "B2: the probe does not try both shapes in order"
fi

# --- C/D: the tests that pin the behaviour ------------------------------------
if grep -q 'func tailscaleStyleSTUNServer' "$TESTFILE" \
   && grep -q 'fingerprintOK' "$TESTFILE"; then
  ok "C1: a server implementing derper's rule is part of the test"
else
  bad "C1: no test reproduces the fingerprint requirement"
fi
if grep -q 'TestProbeSTUNSpeaksFingerprint_B307' "$TESTFILE"; then
  ok "C2: the probe must be answered by a fingerprint-requiring server"
else
  bad "C2: the decisive test is missing"
fi
if grep -q 'want it to name both request shapes' "$TESTFILE"; then
  ok "D1: a failure must name both shapes"
else
  bad "D1: a failure could hide which packet was sent"
fi
if grep -q 'TestProbeSTUNFallsBackToBareForGenericServers_B307' "$TESTFILE"; then
  ok "D2: the fallback path is pinned"
else
  bad "D2: the fallback path is untested"
fi

# --- E: the evidence lives in the source --------------------------------------
if grep -q 'not_stun' "$SRC" && grep -q 'ErrNoFingerprint' "$SRC"; then
  ok "E1: the live counters and the parser error are recorded where the fix is"
else
  bad "E1: the root cause is undocumented in the source"
fi
if grep -q 'RENEGOTIATED' "$WIRETEST" && grep -q 'buildSTUNBareBindingRequest' "$WIRETEST"; then
  ok "E2: the old wire-format contract is renegotiated in place, with the bare shape still pinned"
else
  bad "E2: the wire-format test was not renegotiated"
fi

# --- F: tests ------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'STUN|Stun|B307' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F1: the STUN tests pass"
  else
    bad "F1: the STUN tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "F1: go not on PATH — run the STUN tests on the VM"
fi

# --- G: git --------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b307_stun_fingerprint.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b307_stun_fingerprint.sh is tracked by git"
else
  bad "G1: scripts/check_b307_stun_fingerprint.sh is NOT tracked"
fi

printf '\n\033[1mB307 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
