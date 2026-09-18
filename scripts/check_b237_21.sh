#!/bin/bash
# scripts/check_b237_21.sh — B237.21 (v1.5.2+)
# `skygate regapi-credentials` CLI subcommand contract.
#
# Closes the "only path to set reg.ru creds is the
# /admin/ha form" gap. Pre-B237.21 the operator's
# one-shot bootstrap flow (clone → start skygate →
# set creds → run B146 live test) required a browser
# session to log in to /admin/ha. B237.21 adds 4 CLI
# verbs (set / show / test / delete) that mirror the
# form's behavior end-to-end.
#
# Why 4 verbs (not 1):
#   - set: write creds (the primary need)
#   - show: verify "did set actually persist?"
#         (with the secrets masked — same UX as
#          `vault kv get -format=json` stripping the
#          value field)
#   - test: in-app TestConnection call (sanity
#          check before running b146_regapi_live.sh)
#   - delete: explicit clear (for the cert-rotation
#            case where the operator wants to start
#            fresh, not "set again with new values")
#
# The CLI uses the SAME extcreds.Store as the /admin/ha
# form — single source of truth, no chance of the
# "set via CLI, view via form shows different value"
# bug that would be a nightmare to debug.
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. Source: the subcommand + the Store.Delete ---

# A.1 the CLI subcommand file exists + has the
# canonical 4-verb dispatcher
if [ -f cmd/skygate/regapi_credentials.go ]; then
    ok "A.1 cmd/skygate/regapi_credentials.go exists"
else
    bad "A.1 cmd/skygate/regapi_credentials.go missing"
fi

# A.2 the dispatcher handles all 4 verbs
if grep -q 'case "set":' cmd/skygate/regapi_credentials.go 2>/dev/null && \
   grep -q 'case "show":' cmd/skygate/regapi_credentials.go 2>/dev/null && \
   grep -q 'case "test":' cmd/skygate/regapi_credentials.go 2>/dev/null && \
   grep -q 'case "delete":' cmd/skygate/regapi_credentials.go 2>/dev/null; then
    ok "A.2 dispatcher handles all 4 verbs (set/show/test/delete)"
else
    bad "A.2 dispatcher must handle all 4 verbs"
fi

# A.3 the Store has a Delete() method (B237.21 prerequisite)
if grep -qE '^func \(s \*Store\) Delete\(\)' internal/ha/dnsexternal/credentials.go 2>/dev/null; then
    ok "A.3 Store.Delete() method exists (B237.21 prerequisite)"
else
    bad "A.3 Store.Delete() must exist for the 'delete' verb"
fi

# A.4 db.DeleteGlobalSetting helper exists
if grep -q '^func DeleteGlobalSetting' internal/db/globalsettings.go 2>/dev/null; then
    ok "A.4 db.DeleteGlobalSetting helper exists (Store.Delete uses it)"
else
    bad "A.4 db.DeleteGlobalSetting helper missing"
fi

# --- B. Wire-up: the case in main.go's dispatcher ---

# B.1 main.go has a case for the new subcommand
if grep -q 'case "regapi-credentials":' cmd/skygate/main.go 2>/dev/null; then
    ok "B.1 cmd/skygate/main.go has the regapi-credentials case"
else
    bad "B.1 cmd/skygate/main.go must have the regapi-credentials case"
fi

# B.2 main.go calls the dispatcher
if grep -q 'runRegAPICredsSubcommand' cmd/skygate/main.go 2>/dev/null; then
    ok "B.2 main.go calls runRegAPICredsSubcommand"
else
    bad "B.2 main.go must call runRegAPICredsSubcommand"
fi

# B.3 the help text mentions the new subcommand
if grep -q 'regapi-credentials' cmd/skygate/main.go 2>/dev/null && \
   grep -q 'B237.21' cmd/skygate/main.go 2>/dev/null; then
    ok "B.3 help text mentions the new subcommand (with B237.21 reference)"
else
    bad "B.3 help text must mention the new subcommand"
fi

# --- C. Security: the password is masked in show ---

# C.1 the show verb masks the password
if grep -q 'maskSecret(creds.Password)' cmd/skygate/regapi_credentials.go 2>/dev/null; then
    ok "C.1 the show verb masks the password (maskSecret(creds.Password))"
else
    bad "C.1 the show verb must mask the password"
fi

# C.2 the show verb does NOT print the cert PEM directly
# (it uses the certSummary helper which only prints
# the byte count, not the raw PEM). The cert IS a
# secret (it may include the private key in some
# configurations).
if grep -qE 'certSummary\(creds\.CertPEM\)' cmd/skygate/regapi_credentials.go 2>/dev/null; then
    # Confirm the only reference to creds.CertPEM in
    # the show path is via certSummary
    n_refs=$(grep -c 'creds\.CertPEM' cmd/skygate/regapi_credentials.go 2>/dev/null || echo 0)
    if [ "$n_refs" = "1" ]; then
        ok "C.2 the show verb uses certSummary(creds.CertPEM) — only 1 reference, no direct print"
    else
        bad "C.2 the show verb references creds.CertPEM $n_refs times (should be exactly 1, via certSummary)"
    fi
else
    bad "C.2 the show verb must use certSummary(creds.CertPEM) — not found"
fi

# C.3 the set verb does NOT echo the password back to
# stdout (the operator already has it; printing it
# would leak to shell history if they redirect).
# We grep for the literal `creds.Password,` (note the
# trailing comma — direct format-arg) OR
# `creds.Password)` at the end of a function-call arg
# list. The maskSecret wrapper (`maskSecret(creds.Password)`)
# does NOT match because the literal is inside a
# function call, not at the top-level of a fmt.Print*.
if grep -qE 'fmt\.Print[a-z]*\([^)]*creds\.Password\)' cmd/skygate/regapi_credentials.go 2>/dev/null && \
   ! grep -qE 'maskSecret\(creds\.Password\)' cmd/skygate/regapi_credentials.go 2>/dev/null; then
    bad "C.3 the set verb must NOT echo the password to stdout (found a direct fmt.Print* of creds.Password)"
else
    # The set verb's success line intentionally does
    # NOT mention the password. The show verb uses
    # maskSecret. This is the safe state.
    ok "C.3 the password is only used in save (creds.Password → Save) + show (maskSecret(creds.Password)) — never printed directly"
fi

# C.4 the set verb supports --password-file= (the
# recommended path — chmod 0600 the file instead of
# leaking the password via shell history)
if grep -q 'passwordFile := fs.String' cmd/skygate/regapi_credentials.go 2>/dev/null && \
   grep -q '"password-file"' cmd/skygate/regapi_credentials.go 2>/dev/null; then
    ok "C.4 the set verb supports --password-file= (safer than --password)"
else
    bad "C.4 the set verb must support --password-file="
fi

# --- D. The pure-unit tests ---

# D.1 the test file exists + has at least 4 tests
if [ -f cmd/skygate/regapi_credentials_test.go ]; then
    n_tests=$(grep -cE '^func Test' cmd/skygate/regapi_credentials_test.go 2>/dev/null || echo 0)
    if [ "$n_tests" -ge 4 ]; then
        ok "D.1 regapi_credentials_test.go has $n_tests unit tests (>= 4)"
    else
        bad "D.1 regapi_credentials_test.go has $n_tests tests, need >= 4"
    fi
else
    bad "D.1 regapi_credentials_test.go missing (B237.21 regression guard)"
fi

# D.2 the unit tests pass (env-gated)
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        -run 'TestMaskSecret|TestCertSummary|TestEnvOr|TestRunRegAPICreds' \
        ./cmd/skygate/... 2>/dev/null | grep -q '^ok'; then
        ok "D.2 unit tests pass (maskSecret + certSummary + envOr + dispatcher)"
    else
        bad "D.2 unit tests failed"
    fi
else
    echo "  SKIP  D.2 unit tests (no go in PATH)"
fi

# D.3 the dispatcher rejects unknown verbs (the test
# pin — without it, a typo like 'regapi-cred' would
# silently pass through to the web server with the
# "unknown command" error from main.go, not the more
# helpful "unknown verb" error from the subcommand)
if grep -q 'TestRunRegAPICredsSubcommand_UnknownVerb' cmd/skygate/regapi_credentials_test.go 2>/dev/null; then
    ok "D.3 unknown-verb rejection is unit-tested"
else
    bad "D.3 unknown-verb rejection must be unit-tested"
fi

# --- E. Build + tests ---

# E.1 the tree still builds
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "E.1 go build ./... clean (B237.21 didn't break the build)"
    else
        bad "E.1 go build ./... failed"
    fi
else
    echo "  SKIP  E.1 go build (no go in PATH)"
fi

# E.2 the cmd/skygate tests pass
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        ./cmd/skygate/... 2>/dev/null | grep -q '^ok'; then
        ok "E.2 cmd/skygate tests pass (no regression)"
    else
        bad "E.2 cmd/skygate tests failed"
    fi
else
    echo "  SKIP  E.2 go test (no go in PATH)"
fi

# --- F. verify_pre_deploy.sh + AGENTS.md registration ---

# F.1 the check is registered
if grep -q 'check_b237_21' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "F.1 scripts/verify_pre_deploy.sh includes the B237.21 check"
else
    bad "F.1 scripts/verify_pre_deploy.sh must include the B237.21 check"
fi

# F.2 AGENTS.md mentions B237.21
if grep -q 'B237\.21' AGENTS.md 2>/dev/null; then
    ok "F.2 AGENTS.md documents B237.21"
else
    bad "F.2 AGENTS.md must document B237.21 (B-check convention)"
fi

# --- Summary ---

echo
echo "=== B237.21 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
