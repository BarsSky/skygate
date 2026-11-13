#!/usr/bin/env bash
# ============================================================================
# check_b468_dns_switch.sh — B468
#
# THE DNS HALF OF A MANUAL SWITCH MUST ACTUALLY REACH THE REGISTRAR.
#
# Measured 2026-11-13, while preparing the manual A→B switch: the operator had already provisioned
# reg.ru API credentials, and skygate could not use them at all. Three independent defects, each
# enough on its own:
#
#   1. The shipped client pointed at a PLACEHOLDER host — literally
#      https://api.<your-domain>/api/<provider-v2-path>/zone/… — and the shell live-test built
#      https://api.${SKYGATE_DNS_REGAPI_ZONE}/api/regru2, i.e. it interpolated the ZONE into the
#      HOST. Neither could ever reach api.reg.ru.
#   2. The record parsing looked for `name`/`type` while the registrar sends
#      `subname`/`rectype`, so a present A record read as "not found".
#   3. The credential store REQUIRED a client certificate (mTLS), which is not how the reg.ru API
#      authenticates — so every login+password pair failed validation before any call was made.
#
# The result: "test credentials" reported a failure for credentials that may have been perfect, and
# the manual switch had no way to move the public name at all.
#
# Contracts:
#   A. the endpoint is the registrar's, and the zone is never part of the host
#   B. the wire format is the one the registrar uses (fields, envelope, error text)
#   C. the provider builds under its real name (and the legacy alias), credentials without a cert
#   D. one helper for the URL/format — the client and the store's TestConnection cannot drift
#   E. the switch verbs exist, are dispatched, verify by reading back, and audit the change
#   F. the runbook states the two rules (order of copies, TTL) and the rollback
#   G. tests pin the protocol against a fake that MUTATES its zone
#   H. the detector discriminates the pre-B468 shape
# ============================================================================
set -uo pipefail
# B339: read the cmd/skygate SURFACE, never cmd/skygate/main.go as one file (the same rule B467
# follows) — a contract that pins the entrypoint's path turns a pure code move red.
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b468.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

API=internal/ha/dnsexternal/regru_api.go
CREDS=internal/ha/dnsexternal/credentials.go
CLIENT=internal/dnsexternal/client.go
CTEST=internal/dnsexternal/client_test.go
BUILD=internal/dns/provider_build.go
CLI=cmd/skygate/dns_cli_b468.go
gosurface CMD cmd/skygate/*.go
HADOC=docs/ha.md
RUHADOC=docs/ru/ha-manual-switch.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$API" "$CREDS" "$CLIENT" "$CTEST" "$BUILD" "$CLI" "$HADOC" "$RUHADOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the endpoint ---------------------------------------------------------
if has "$API" 'RegAPIBaseURL = "https://api.reg.ru/api/regru2"'; then
  ok "A1: the shipped base is the registrar's real API root"
else
  bad "A1: the base URL is not the registrar's endpoint"
fi
# A2 — no placeholder and no zone-in-host in the product CODE. Comment lines are excluded: the
# fix documents the old shape (that is how the next reader learns why the constant exists), and a
# check must not turn its own explanation into the regression it looks for (B466 A3b, trap #15).
if grep -rn --include='*.go' -e 'api\.<your-domain>' -e '<provider-v2-path>' internal cmd 2>/dev/null \
     | grep -v '_test\.go' | grep -v ':[[:space:]]*//' | grep -q .; then
  bad "A2: a placeholder API host is still in the product code"
else
  ok "A2: no placeholder endpoint remains in the product code (only the documentation of it)"
fi
if grep -q 'api\.\${SKYGATE_DNS_REGAPI_ZONE}' scripts/b146_regapi_live.sh 2>/dev/null; then
  bad "A3: the live-test script still interpolates the ZONE into the API host"
else
  ok "A3: the live-test script no longer builds api.<zone>"
fi
if has "$API" 'RegAPIBaseURLEnv' && has "$API" 'func RegAPIBase()'; then
  ok "A4: the endpoint is overridable (tests / a proxy) and resolved in one place"
else
  bad "A4: the base URL has no single resolution point"
fi

# --- B: the wire format ------------------------------------------------------
for field in 'form.Set("username"' 'form.Set("password"' 'form.Set("input_format", "json")' 'form.Set("input_data"'; do
  if has "$API" "$field"; then
    ok "B1: the request carries $field"
  else
    bad "B1: the request does not carry $field"
  fi
done
if has "$API" 'json:"subname,omitempty"' && has "$API" 'json:"rectype,omitempty"'; then
  ok "B2: the record type reads the registrar's field names (subname/rectype)"
else
  bad "B2: the record parsing does not match the registrar's fields"
fi
if has "$API" 'func (r RegAPIResponse) Err(op string) error' && has "$API" 'ErrorText'; then
  ok "B3: a refusal surfaces the registrar's own code and text"
else
  bad "B3: a refusal would be reported without its reason"
fi
if has "$API" 'func ZoneRecords(raw json.RawMessage)'; then
  ok "B4: the zone answer is parsed in one place (both container shapes)"
else
  bad "B4: the zone answer parsing is duplicated or missing"
fi

# --- C: the provider and the credentials ------------------------------------
if has "$BUILD" 'case "regru", "external":'; then
  ok "C1: the factory builds the real provider under its name (plus the legacy alias)"
else
  bad "C1: BuildProvider does not accept 'regru'"
fi
if has "$CLIENT" 'func (c *Client) Name() string { return "regru" }'; then
  ok "C2: the client reports its honest provider name"
else
  bad "C2: the client still reports a vague name"
fi
# C3 — the mTLS-only rule that rejected the operator's login+password.
if has "$CREDS" 'if c.CertPEM != "" && !strings.Contains(c.CertPEM, "BEGIN CERTIFICATE")'; then
  ok "C3: a client certificate is optional; a malformed one is still rejected"
else
  bad "C3: the credential validation still demands a certificate"
fi
if has "$CREDS" 'const ProviderRegru = "regru"'; then
  ok "C4: the provider name is a first-class constant"
else
  bad "C4: the provider name is not defined"
fi

# --- D: one helper for the call ---------------------------------------------
if has "$API" 'func PostRegAPIAt(' && has "$CLIENT" 'dnsexternal.PostRegAPIAt(' \
   && has "$CREDS" 'PostRegAPI(ctx, s.HTTP, RegAPIPathZoneRecords'; then
  ok "D1: the credential store and the provider client share one request helper"
else
  bad "D1: two code paths build the API call"
fi
if has "$CREDS" 'PostRegAPI(ctx, s.HTTP, RegAPIPathZoneRecords'; then
  ok "D2: the store's TestConnection uses the real read-only endpoint"
else
  bad "D2: TestConnection calls something else (the drift that shipped)"
fi
if has "$CREDS" 'func (s *Store) TestConnectionWithCreds('; then
  ok "D3: credentials can be proven BEFORE they are saved"
else
  bad "D3: a credential triple cannot be tested without saving it first"
fi

# --- E: the switch verbs ----------------------------------------------------
if has "$CMD" 'case "dns-records", "dns-set-a":'; then
  ok "E1: the verbs are dispatched from the CLI switch"
else
  bad "E1: the verbs are not reachable from the CLI"
fi
if has "$CLI" 'func runDNSRecords(' && has "$CLI" 'func runDNSSetA('; then
  ok "E2: a read verb and a write verb exist (read first, write second)"
else
  bad "E2: the switch verbs are incomplete"
fi
if has "$CLI" '--dry-run' || has "$CLI" 'dry-run'; then
  ok "E3: the write verb can be rehearsed without writing"
else
  bad "E3: the write verb has no dry run"
fi
if has "$CLI" '"ttl"' && has "$CLIENT" 'func (c *Client) SetTTL('; then
  ok "E4: the TTL can be lowered (a switch is bounded by the TTL, not the API call)"
else
  bad "E4: nothing lets the operator lower the TTL before a switch"
fi
if has "$CLIENT" 'the API accepted %q but the zone still answers %q'; then
  ok "E5: a write is verified by reading the zone back"
else
  bad "E5: a write reports success without reading it back"
fi
if has "$CLI" "'dns.manual_switch'"; then
  ok "E6: the switch writes an audit row"
else
  bad "E6: a manual DNS change would leave no audit trail"
fi
if has "$CLI" 'splitNames('; then
  ok "E7: several public names move in one verified command"
else
  bad "E7: each name needs a separate invocation"
fi

# --- F: the runbook ---------------------------------------------------------
if has "$HADOC" 'Manual switch between two copies'; then
  ok "F1: the manual switch has its own runbook section"
else
  bad "F1: the manual switch is undocumented"
fi
if has "$HADOC" 'Never two headscales with the same noise key'; then
  ok "F2: the two-copies rule is stated where an operator reads the procedure"
else
  bad "F2: the runbook does not forbid running both copies"
fi
if has "$HADOC" 'bounded by the TTL' && has "$HADOC" 'a day before'; then
  ok "F3: the TTL constraint (lower it a day before) is stated"
else
  bad "F3: the runbook does not mention the TTL window"
fi
if has "$HADOC" '### 6.4.3 Rollback' || has "$RUHADOC" '## Откат'; then
  ok "F4: the rollback is part of the procedure"
else
  bad "F4: the procedure has no rollback"
fi
if has "$RUHADOC" 'SKYGATE_READONLY_COPY'; then
  ok "F5: the RU runbook warns that a copy must have read-only unset before the switch"
else
  bad "F5: the RU runbook omits the read-only flag"
fi

# --- G: tests ---------------------------------------------------------------
_g=0
for t in TestB468_GetRecordReadsTheRegistrarShape TestB468_GetRecordMissingReturnsTheSentinel \
         TestB468_NoAuthTrapIsNotReintroduced TestB468_UpdateRecordWritesAndReadsBack \
         TestB468_ErrorTextIsSurfaced TestB468_TestConnectionIsReadOnlyAndNamesTheZone \
         TestB468_StoreTestConnectionUsesTheSameEndpoint TestB468_ZoneRecordsAcceptsBothShapes \
         TestB468_BaseURLIsNotAZone TestB468_CredentialValidationAcceptsLoginPasswordOnly; do
  has "$CTEST" "func $t(" && _g=$((_g+1))
done
if [ "$_g" -ge 10 ]; then
  ok "G1: the protocol is pinned by 10 tests, including a fake that mutates its zone"
else
  bad "G1: only $_g of 10 protocol tests exist"
fi
if has "$CTEST" 'f.records = setA('; then
  ok "G2: the fake MUTATES the zone, so the read-back verification is really exercised"
else
  bad "G2: the fake cannot answer differently after a write"
fi

# --- H: the detector discriminates the pre-B468 shape ----------------------
cat > "$SKY_TMP/old_client.go" <<'EOF'
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.<your-domain>/api/<provider-v2-path>/zone/replace_records", strings.NewReader(form.Encode()))
			if strings.EqualFold(r.Type, "A") && strings.EqualFold(r.Name, name) && r.Content != "" {
EOF
cat > "$SKY_TMP/old_creds.go" <<'EOF'
	if c.CertPEM == "" {
		return errors.New("dnsexternal: cert_pem is required")
	}
	apiURL := "https://api.<your-domain>/api/<provider-v2-path>/zone/get_zone"
EOF
discriminates=1
for _m in 'https://api.reg.ru' 'subname,omitempty' 'PostRegAPIAt' 'CertPEM != ""' 'SetTTL' 'dns.manual_switch'; do
  case "$_m" in
    'dns.manual_switch') has "$CLI" "$_m" || discriminates=0 ;;
    'SetTTL') has "$CLIENT" "$_m" || discriminates=0 ;;
    *) has "$API" "$_m" || has "$CLIENT" "$_m" || has "$CREDS" "$_m" || discriminates=0 ;;
  esac
done
for _m in 'https://api.reg.ru' 'subname,omitempty' 'PostRegAPIAt' 'CertPEM != ""'; do
  has "$SKY_TMP/old_client.go" "$_m" && discriminates=0
  has "$SKY_TMP/old_creds.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "H1: every predicate is present in the fixed files and absent from the pre-B468 shape"
else
  bad "H1: the predicates do not discriminate the pre-B468 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB468: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB468: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
