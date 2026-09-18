#!/usr/bin/env bash
# check_b236.sh — B236: Tailscale subnet-routes management on /admin/tailscale.
#
# WHY THIS FILE EXISTS (2026-09-18)
# ---------------------------------
# verify_pre_deploy.sh has always contained:
#
#   run_check "B236" "… 39 B-check contracts in scripts/check_b236.sh." \
#     'test -f scripts/check_b236.sh && bash scripts/check_b236.sh'
#
# but the file was never written. Because run_check counts a non-zero exit
# as FAIL, the gate reported a permanent B236 failure on every checkout —
# and the `test -f` guard made it look like a missing-file glitch rather
# than a missing contract. This is that contract.
#
# It is a grep-based source contract (the same shape as the other
# check_b*.sh) plus one runtime contract that the unit tests still pass.
#
# Usage:  bash scripts/check_b236.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TS="$REPO_ROOT/internal/feature/admin/tailscale.go"
TST="$REPO_ROOT/internal/feature/admin/tailscale_b236_test.go"
MAIN="$REPO_ROOT/cmd/skygate/main.go"

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }
ok()   { echo "ok  : $*"; }

require_file() {
  [ -f "$1" ] && ok "present: $(basename "$1")" || fail "missing file: $1"
}

echo "=== A. source files present ==="
require_file "$TS"
require_file "$TST"
require_file "$MAIN"

echo
echo "=== B. the handler and its route ==="
# set_advertise_routes is not a separate route: it is a form ACTION
# dispatched inside PostAdminTailscale (the switch on the form's action
# field), and the route itself is POST /admin/tailscale.
grep -q 'case "set_advertise_routes"' "$TS" \
  && ok "set_advertise_routes action dispatched in tailscale.go" \
  || fail "set_advertise_routes action missing from the tailscale.go switch"
grep -q 'POST /admin/tailscale' "$MAIN" \
  && ok "POST /admin/tailscale registered in main.go" \
  || fail "POST /admin/tailscale route not registered in main.go"

echo
echo "=== C. the write path uses tailscale set --advertise-routes= ==="
# The whole point of B236 is an idempotent REPLACE, not an append.
grep -q -- '--advertise-routes=' "$TS" \
  && ok "uses --advertise-routes=" \
  || fail "--advertise-routes= not found — the handler must SET the route list"

echo
echo "=== D. guards: host LAN + docker bridges ==="
grep -q 'detectHostLAN' "$TS" \
  && ok "detectHostLAN present" \
  || fail "detectHostLAN missing — the handler must refuse the host's own LAN"
grep -q 'SKYGATE_HOST_LAN_OVERRIDE' "$TS" \
  && ok "SKYGATE_HOST_LAN_OVERRIDE override present" \
  || fail "SKYGATE_HOST_LAN_OVERRIDE override missing (operator escape hatch)"
grep -q 'dockerBridgeRanges' "$TS" \
  && ok "dockerBridgeRanges present" \
  || fail "dockerBridgeRanges missing — 172.17-172.32 must be refused"
grep -q 'cidrOverlaps' "$TS" \
  && ok "cidrOverlaps helper present" \
  || fail "cidrOverlaps helper missing"

echo
echo "=== E. unit tests still cover the three helpers ==="
for fn in cidrOverlaps detectHostLAN dockerBridgeRanges; do
  grep -q "$fn" "$TST" \
    && ok "unit test references $fn" \
    || fail "no unit test references $fn"
done

echo
echo "=== F. build + the B236 unit tests ==="
# `go` is frequently absent from the PATH inside git-bash on Windows (the
# operator's Windows checkout), where this script is often run by hand.
# Skip loudly rather than reporting a spurious FAIL — CI and the VM both
# have Go, so the contract still runs where it matters.
if ! command -v go >/dev/null 2>&1; then
  echo "SKIP: go is not on PATH in this shell — run this on the VM/CI for contracts F"
else
  # Build the code packages explicitly rather than `./...`.
  #
  # `go build ./...` walks the whole repository, and on the operator's VM the
  # repo root contains a root-owned, mode-0700 directory (data/oidc-keys-test),
  # so the walk dies with "open data/oidc-keys-test: permission denied" when
  # run as skyadmin — a false FAIL that has nothing to do with B236. CI uses
  # `./...` on a fresh checkout where that directory does not exist; here we
  # only care about the packages.
  BUILD_PKGS="./cmd/... ./internal/..."
  if ( cd "$REPO_ROOT" && go build $BUILD_PKGS ) >/dev/null 2>&1; then
    ok "go build $BUILD_PKGS"
  else
    fail "go build $BUILD_PKGS failed"
  fi
  if ( cd "$REPO_ROOT" && go test ./internal/feature/admin/ -count=1 ) >/dev/null 2>&1; then
    ok "go test ./internal/feature/admin/ passes"
  else
    fail "go test ./internal/feature/admin/ failed"
  fi
fi

echo
if [ "$FAILED" -eq 0 ]; then
  echo "RESULT: PASS — B236 contracts hold"
  exit 0
fi
echo "RESULT: FAIL — see above" >&2
exit 1
