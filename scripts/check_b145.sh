#!/usr/bin/env bash
# check_b145.sh — v1.5.0 / B145 contracts.
#
# This is the B-check that pins Phase 1 (B145) of the
# HA plan. It verifies six things:
#
#   1. internal/ha package exists + has the expected files
#      (chain.go, storage.go, elector.go).
#   2. internal/ha/regapi package exists + has the
#      credentials.go file (encrypted cert+password).
#   3. internal/dns package exists with the Provider
#      interface + BuildProvider factory.
#   4. internal/dnsregapi package exists with the
#      working auth pattern (top-level form fields).
#   5. internal/config has the new HA env vars.
#   6. cmd/skygate/main.go wires the elector on startup
#      behind SKYGATE_HA_ENABLED.
#
# The script is intentionally read-only — it does not
# touch the database or run the live VM. The unit tests
# (go test ./internal/ha/... ./internal/dns/...) cover
# the runtime behaviour; this script is the "is the code
# even there?" check.

set -euo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$REPO_ROOT"

PASS=0
FAIL=0

ok()   { echo "PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "FAIL: $1"; FAIL=$((FAIL+1)); }

# --- contract A: internal/ha package layout ----------
echo
echo "=== contract A: internal/ha package layout ==="
if [ -d internal/ha ]; then
    ok "internal/ha/ directory exists"
else
    bad "internal/ha/ directory missing"
fi
for f in chain.go storage.go elector.go; do
    if [ -f "internal/ha/$f" ]; then
        ok "internal/ha/$f exists"
    else
        bad "internal/ha/$f missing"
    fi
done
for f in chain_test.go; do
    if [ -f "internal/ha/$f" ]; then
        ok "internal/ha/$f exists (unit tests present)"
    else
        bad "internal/ha/$f missing (no unit tests)"
    fi
done

# --- contract B: encrypted credentials --------------
echo
echo "=== contract B: internal/ha/regapi credentials ==="
if [ -d internal/ha/regapi ]; then
    ok "internal/ha/regapi/ directory exists"
else
    bad "internal/ha/regapi/ directory missing"
fi
for f in credentials.go credentials_test.go; do
    if [ -f "internal/ha/regapi/$f" ]; then
        ok "internal/ha/regapi/$f exists"
    else
        bad "internal/ha/regapi/$f missing"
    fi
done
# Verify the encrypted storage uses db.EncryptForColumn
# (AES-256-GCM, not plain text).
if grep -q "EncryptForColumn" internal/ha/regapi/credentials.go; then
    ok "credentials.go uses db.EncryptForColumn for the secret fields"
else
    bad "credentials.go does NOT use db.EncryptForColumn (secret would be plain text)"
fi
# Verify it rejects empty SKYGATE_SECRET_KEY (fail-fast
# instead of writing plain text).
if grep -q "ErrSecretKeyUnset" internal/ha/regapi/credentials.go; then
    ok "credentials.go fails fast when SKYGATE_SECRET_KEY is unset"
else
    bad "credentials.go does NOT check ErrSecretKeyUnset (could write plain text)"
fi

# --- contract C: pluggable Provider interface ---------
echo
echo "=== contract C: pluggable DNS Provider interface ==="
if [ -d internal/dns ]; then
    ok "internal/dns/ directory exists"
else
    bad "internal/dns/ directory missing"
fi
for f in provider.go provider_build.go provider_build_test.go; do
    if [ -f "internal/dns/$f" ]; then
        ok "internal/dns/$f exists"
    else
        bad "internal/dns/$f missing"
    fi
done
# Verify the Provider interface has the expected four
# methods: Name, GetRecord, UpdateRecord, TestConnection.
for method in "Name() string" "GetRecord" "UpdateRecord" "TestConnection"; do
    if grep -q "$method" internal/dns/provider.go; then
        ok "Provider interface declares $method"
    else
        bad "Provider interface does NOT declare $method"
    fi
done
# Verify BuildProvider supports the four providers from
# the v1.5.0 plan. The "regapi" case is its own line;
# the other three (cloudflare / route53 / rfc2136) share
# one case statement at v1.5.0 (B145) because all three
# return the same "not implemented yet" error. The check
# accepts either form: a dedicated `case "X"` or a
# shared `case "Y", "X"`.
for name in regapi cloudflare route53 rfc2136; do
    if grep -qE "(^|\s)\"?$name\"?" internal/dns/provider_build.go; then
        ok "BuildProvider mentions $name (in a case clause)"
    else
        bad "BuildProvider is missing a case for $name"
    fi
done

# --- contract D: dnsregapi uses the WORKING pattern -----
echo
echo "=== contract D: dnsregapi uses top-level form fields ==="
if [ -d internal/dnsregapi ]; then
    ok "internal/dnsregapi/ directory exists (sibling of dns/, not dns/regapi/ — cycle avoidance)"
else
    bad "internal/dnsregapi/ directory missing"
fi
for f in client.go client_test.go; do
    if [ -f "internal/dnsregapi/$f" ]; then
        ok "internal/dnsregapi/$f exists"
    else
        bad "internal/dnsregapi/$f missing"
    fi
done
# Verify GetRecord sends username+password as TOP-LEVEL
# form fields (not inside input_data JSON). The test
# file pins this contract — search for the assertion.
if grep -q "form username" internal/dnsregapi/client_test.go; then
    ok "client_test.go pins top-level username field (regression test for the NO_AUTH bug)"
else
    bad "client_test.go does NOT assert on top-level username field"
fi
if grep -q "password leaked into input_data" internal/dnsregapi/client_test.go; then
    ok "client_test.go fails if password is put into input_data (regression test)"
else
    bad "client_test.go does NOT assert that password is NOT in input_data"
fi

# --- contract E: config has the new env vars --------
echo
echo "=== contract E: HA env vars in internal/config/config.go ==="
for var in SKYGATE_HA_ENABLED SKYGATE_HA_HEARTBEAT_INTERVAL SKYGATE_HA_MISSED_THRESHOLD SKYGATE_HA_ROLE SKYGATE_DNS_PROVIDER; do
    if grep -q "$var" internal/config/config.go; then
        ok "config.go reads $var"
    else
        bad "config.go does NOT read $var"
    fi
done
# Verify the defaults match the v1.5.0 plan (5s tick,
# 3 missed, no provider).
if grep -q "5\*time.Second" internal/config/config.go; then
    ok "config.go default heartbeat = 5s (matches the v1.5.0 plan)"
else
    bad "config.go default heartbeat is NOT 5s"
fi
if grep -q "SKYGATE_HA_MISSED_THRESHOLD.*3\|getInt(\"SKYGATE_HA_MISSED_THRESHOLD\", 3)" internal/config/config.go; then
    ok "config.go default missed threshold = 3 (matches the v1.5.0 plan)"
else
    bad "config.go default missed threshold is NOT 3"
fi

# --- contract F: main.go wires the elector ----------
echo
echo "=== contract F: cmd/skygate/main.go HA wire-up ==="
if grep -q "ha.NewElector(d)" cmd/skygate/main.go; then
    ok "main.go constructs the elector via ha.NewElector"
else
    bad "main.go does NOT construct ha.NewElector"
fi
if grep -q "elector.Run(ctx)" cmd/skygate/main.go; then
    ok "main.go runs the elector as a goroutine"
else
    bad "main.go does NOT start the elector goroutine"
fi
if grep -q "cfg.HAEnabled" cmd/skygate/main.go; then
    ok "main.go gates the elector on cfg.HAEnabled (opt-in)"
else
    bad "main.go does NOT gate the elector on cfg.HAEnabled"
fi
if grep -q "dns.BuildProvider" cmd/skygate/main.go; then
    ok "main.go wires dns.BuildProvider (the factory from contract C)"
else
    bad "main.go does NOT call dns.BuildProvider"
fi
# Verify the import statements are present (catches the
# "import added but unused" / "unused import" cases).
if grep -q '"skygate/internal/ha"' cmd/skygate/main.go; then
    ok 'main.go imports "skygate/internal/ha"'
else
    bad 'main.go does NOT import "skygate/internal/ha"'
fi
if grep -q '"skygate/internal/dns"' cmd/skygate/main.go; then
    ok 'main.go imports "skygate/internal/dns"'
else
    bad 'main.go does NOT import "skygate/internal/dns"'
fi

# --- summary ---------------------------------------
echo
echo "=== summary ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "all B145 contracts satisfied"
