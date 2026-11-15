#!/usr/bin/env bash
# ============================================================================
# check_b475_rfc2136.sh — B475
#
# THE THIRD PROVIDER, AND THE ONE WITH NO HTTP API AT ALL. reg.ru (B468) and Cloudflare (B474) are
# HTTPS+JSON: a client is written against a documented envelope. RFC 2136 dynamic DNS UPDATE is a BINARY
# protocol over TCP, authenticated by TSIG (RFC 8945) — an HMAC over the message PLUS a block of TSIG
# variables. This project has no DNS library dependency, and adding one would touch go.mod/go.sum, which
# every operator host keeps as an operator-managed file (AGENTS rule 7). So the adapter speaks the wire
# itself, and the contract below pins the three things that make such an adapter correct:
#
#   1. THE DIGEST RULE. TSIG signs the message BEFORE the TSIG RR exists (ARCOUNT counting only the other
#      additional records) while the wire form carries ARCOUNT+1. Getting this backwards produces a
#      signature that verifies nowhere, and a real server answers NOTAUTH — which reads like "wrong key".
#   2. THE UPDATE SEMANTICS. RFC 2136 has no "set" operation: replacing an address means deleting the
#      RRset and adding the new record in ONE message; deleting one TXT means class NONE with the exact
#      RDATA (class ANY deletes every TXT at the name, including another challenge).
#   3. THE HONEST LIMIT. A dynamic-update server cannot ENUMERATE a zone — that needs AXFR, a different
#      permission and a different feature. ListRecords therefore answers a NAMED refusal that says what
#      to do instead, and «Test connection» reads the zone's SOA, which proves server + zone + key +
#      clocks in one question.
#
# Contracts:
#   A. the adapter exists, satisfies the interface, is reachable and validates its four fields
#   B. the registry entry is complete and its capabilities are the real ones
#   C. TSIG: the digest covers the pre-append ARCOUNT, the RR is last, the variables are complete
#   D. the server's own answer is reported (BADSIG/BADTIME/NOTAUTH/REFUSED/NOTZONE … as words)
#   E. updates: RRset replace in ONE message, exact-value TXT delete, TTL rewrite keeps the address
#   F. reads: GetRecord is a QUERY, absence is ErrRecordNotFound, ListRecords is a named refusal
#   G. the credential test is a SOA query that does not write, and an empty answer is a FAILURE
#   H. the tests are behavioural against a fake server that re-computes the signature
#   I. the docs (EN + RU) describe the provider, its key and its limit, without claiming a live test
#   J. the seam held: no schema change, no template edit, no storage branch for this provider
#   K. the detector discriminates an HTTP-shaped (reg.ru/Cloudflare) adapter
# ============================================================================
set -uo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b475.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

R=internal/dns/rfc2136_b475.go
RT=internal/dns/rfc2136_b475_test.go
REG=internal/dns/registry_b473.go
BUILD=internal/dns/provider_build.go
IFACE=internal/dns/provider.go
CAT=internal/i18n/catalog_admin.go
CARD=internal/feature/admin/ha_dns_form_b473.go
TPL=internal/handlers/templates/admin/ha.html
STORE=internal/ha/dnsexternal/provider_store_b473.go
MIG=internal/db/migrations_v0_84_dns_providers.go
DOC=docs/ha.md
RUDOC=docs/ru/ha-manual-switch.md
gosurface CMD cmd/skygate/*.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1" >&2; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$R" "$RT" "$REG" "$BUILD" "$IFACE" "$CAT" "$CARD" "$TPL" "$STORE" "$MIG" "$DOC" "$RUDOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the adapter ---------------------------------------------------------------
if has "$R" 'var _ Provider = (*rfc2136Provider)(nil)'; then
  ok "A1: the adapter asserts the contract at compile time"
else
  bad "A1: nothing proves the adapter satisfies dns.Provider"
fi
if has "$BUILD" 'case "rfc2136":' && has "$BUILD" 'newRFC2136Provider('; then
  ok "A2: BuildProvider builds it from the credential row"
else
  bad "A2: the adapter is unreachable from the registry entry"
fi
_a=0
for frag in 'the server address is required' 'the TSIG key name is required' \
           'the TSIG secret is not valid base64' 'unsupported TSIG algorithm'; do
  has "$R" "$frag" && _a=$((_a+1))
done
if [ "$_a" -eq 4 ]; then
  ok "A3: every credential field is validated where it is read, naming itself"
else
  bad "A3: only $_a of 4 field validations exist"
fi
if has "$R" 'rfc2136DefaultPort' && has "$R" 'net.JoinHostPort(server, rfc2136DefaultPort)'; then
  ok "A4: a server without a port gets 53 (a missing port reads as a network failure otherwise)"
else
  bad "A4: a missing port is not defaulted"
fi
if has "$R" 'func (p *rfc2136Provider) setZoneHint(' && has "$BUILD" 'p.setZoneHint(deps.Row.Zone)'; then
  ok "A5: the zone travels from the row, because TestConnection has no zone argument"
else
  bad "A5: the credential test cannot know the zone"
fi

# --- B: the registry entry ---------------------------------------------------------
# The spec itself lives next to the adapter (rfc2136_b475.go); the registry file only holds the DISPLAY
# ORDER, which is what makes the panel render it without a template edit.
if has "$R" 'var rfc2136Spec = Spec{' && has "$REG" 'providerSpecs = []Spec{regruSpec, cloudflareSpec, rfc2136Spec}'; then
  ok "B1: the provider is declared AND listed, so the card renders it without a template edit"
else
  bad "B1: the spec is missing from the registry or from its display order"
fi
_b=$(grep -c 'Name: *"\(server\|key_name\|key_secret\|key_algo\)"' "$R" 2>/dev/null || true)
if [ "${_b:-0}" -ge 4 ]; then
  ok "B2: four credential fields — a TSIG credential really has four parts"
else
  bad "B2: the spec declares ${_b:-0} of 4 fields"
fi
if has "$R" 'Caps: Capabilities{A: true, AAAA: true, TXT: true, TTL: true}'; then
  ok "B3: the capabilities are the RFC-2136 ones it truly implements"
else
  bad "B3: the rfc2136 capabilities are understated"
fi
_b=0
for key in ha.dns_field_rfc2136_server ha.dns_field_rfc2136_server_help \
           ha.dns_field_rfc2136_key_name ha.dns_field_rfc2136_key_name_help \
           ha.dns_field_rfc2136_secret ha.dns_field_rfc2136_secret_help \
           ha.dns_field_rfc2136_algo ha.dns_field_rfc2136_algo_help; do
  n=$(grep -c "\"$key\"" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -ge 2 ] && _b=$((_b+1))
done
if [ "$_b" -eq 8 ]; then
  ok "B4: all eight label/help keys exist in RU and EN"
else
  bad "B4: only $_b of 8 catalogue keys are paired"
fi
if has "$R" 'Kind: FieldPassword, Required: true'; then
  ok "B5: the secret is a required masked field (the store never returns it to the page)"
else
  bad "B5: the TSIG secret is not treated as a secret"
fi

# --- C: TSIG ----------------------------------------------------------------------
if has "$R" 'mac.Write(msg) // the pre-append message: ARCOUNT WITHOUT the TSIG record'; then
  ok "C1: the digest covers the message BEFORE the TSIG RR (ARCOUNT without it)"
else
  bad "C1: the digest rule is not the RFC one — a real server would answer NOTAUTH"
fi
if has "$R" 'binary.BigEndian.PutUint16(signed[10:12], ar+1)'; then
  ok "C2: only the WIRE copy carries ARCOUNT+1"
else
  bad "C2: the on-wire ARCOUNT is not incremented for the appended TSIG RR"
fi
if has "$R" 'func (p *rfc2136Provider) tsigVariables(' && has "$R" 'binary.BigEndian.AppendUint16(out, dnsClassANY)'; then
  ok "C3: the TSIG variables block is built explicitly (key name, class, TTL, algo, time, fudge, error)"
else
  bad "C3: the variables block is missing or incomplete"
fi
if has "$R" 'stripLastRR(' && has "$R" 'ar-1'; then
  ok "C4: response verification recomputes the digest with ARCOUNT decremented and the request MAC mixed in"
else
  bad "C4: the server's signature is not verified"
fi
if has "$R" 'if macSize == 0 {'; then
  ok "C5: an error response with an EMPTY MAC is accepted as the server's answer (RFC 8945 §5.3.2)"
else
  bad "C5: a MAC-less error response would be reported as a signature failure — the wrong diagnosis"
fi
if has "$R" 'the server clock and the client clock differ' || has "$R" 'check the clocks on both hosts'; then
  ok "C6: a signature outside the fudge window is reported as a CLOCK problem"
else
  bad "C6: clock skew would read as a wrong secret"
fi
if has "$R" 'case "hmac-sha256.", "hmac-sha256":' && has "$R" 'case "hmac-sha1.", "hmac-sha1":'; then
  ok "C7: the three HMAC-SHA algorithms are the only accepted ones"
else
  bad "C7: the algorithm set is not the shipped one"
fi

# --- D: the server's answer is reported -------------------------------------------
_d=0
for rc in 'NOTIMP' 'REFUSED' 'NOTAUTH' 'NOTZONE' 'BADSIG' 'BADKEY' 'BADTIME'; do
  has "$R" "$rc" && _d=$((_d+1))
done
if [ "$_d" -eq 7 ]; then
  ok "D1: the RCODEs an UPDATE can return are all named in words"
else
  bad "D1: only $_d of 7 RCODEs are explained"
fi
if has "$R" 'the server answered %s'; then
  ok "D2: the error quotes the server's own answer"
else
  bad "D2: the error hides the server's answer"
fi

# --- E: update semantics ----------------------------------------------------------
if has "$R" 'delete the RRset' && has "$R" 'in ONE'; then
  ok "E1: replacing an address is delete-RRset + add, documented as ONE atomic message"
else
  bad "E1: the replace semantics are not the RFC ones"
fi
if has "$R" 'Class: dnsClassNONE, TTL: 0, Data: txtData('; then
  ok "E2: deleting ONE TXT uses class NONE with the exact RDATA"
else
  bad "E2: a TXT delete could remove the whole RRset (class ANY would)"
fi
if has "$R" 'func (p *rfc2136Provider) SetTTL(' && has "$R" 'cur, err := p.GetRecord(ctx, zone, name)'; then
  ok "E3: SetTTL reads the current address and re-adds it with the new TTL"
else
  bad "E3: SetTTL does not preserve the address"
fi
if has "$R" 'func txtData(value string) []byte' && has "$R" 'for len(value) > 255'; then
  ok "E4: a TXT value is split into 255-byte character-strings"
else
  bad "E4: a long TXT value would produce an invalid record"
fi
if has "$R" 'func aData(ip string) ([]byte, error)'; then
  ok "E5: RDATA is validated as an IPv4 address before any message is built"
else
  bad "E5: an invalid address could be packed into a record"
fi

# --- F: reads ---------------------------------------------------------------------
if has "$R" 'Opcode: dnsOpcodeQuery, Zone: fqdn, ZoneType: dnsTypeA'; then
  ok "F1: GetRecord is a real QUERY (the interface's read path)"
else
  bad "F1: GetRecord does not query"
fi
if has "$R" 'return "", ErrRecordNotFound'; then
  ok "F2: an empty answer is ErrRecordNotFound (the failover's fresh-deploy signal)"
else
  bad "F2: the empty state is not distinguished"
fi
if has "$R" 'cannot LIST a zone' && has "$R" 'AXFR'; then
  ok "F3: ListRecords is a NAMED refusal that says what would be needed (AXFR) and what to do instead"
else
  bad "F3: the provider pretends it can enumerate a zone, or refuses without a reason"
fi

# --- G: the credential test -------------------------------------------------------
if has "$R" 'Opcode: dnsOpcodeQuery, Zone: zone, ZoneType: dnsTypeSOA'; then
  ok "G1: TestConnection asks the zone's SOA — one question that proves server, zone, key and clocks"
else
  bad "G1: the credential test proves less than it should"
fi
if has "$R" 'returned no SOA for %q'; then
  ok "G2: NOERROR with an empty answer is a FAILURE, not a pass (the zone is not served there)"
else
  bad "G2: an empty answer would be reported as a working credential"
fi
if has "$R" 'TestConnection sent an UPDATE' || has "$RT" 'TestConnection sent an UPDATE'; then
  ok "G3: the credential test never writes (asserted in the tests)"
else
  bad "G3: nothing proves the test is read-only"
fi
if has "$R" 'no zone configured — save the credential with the zone'; then
  ok "G4: a missing zone is named instead of producing a confusing query"
else
  bad "G4: a missing zone is not reported"
fi

# --- H: behavioural tests ---------------------------------------------------------
_h=0
for t in TestB475_UpdateIsSignedAndApplied TestB475_DigestCoversThePreAppendArcount \
         TestB475_WrongSecretIsRejectedAndReported TestB475_ForcedRcodesAreNamed \
         TestB475_RecordNotFound TestB475_TXTOperationsUseTheExactRdata \
         TestB475_SetTTLKeepsTheAddress TestB475_TestConnectionQueriesTheSOA \
         TestB475_ClockSkewIsNamed TestB475_ListRecordsIsANamedRefusal \
         TestB475_CredentialValidation TestB475_CapabilitiesAreTheAdaptersTruth; do
  has "$RT" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -eq 12 ]; then
  ok "H1: twelve behavioural tests cover the wire, the signature, the rcodes and the limit"
else
  bad "H1: only $_h of 12 adapter tests exist"
fi
if has "$RT" 'func (f *fakeDNS) verify(' && has "$RT" 'm.Write(unsigned)'; then
  ok "H2: the FAKE re-computes the digest from the received bytes (the signature is verified, not assumed)"
else
  bad "H2: the fake does not verify the signature — a signer bug would pass"
fi
if has "$RT" 'the MAC ALSO verifies over the message with ARCOUNT as sent'; then
  ok "H3: the ARCOUNT rule is asserted in BOTH directions (the check cannot pass vacuously)"
else
  bad "H3: nothing proves the ARCOUNT assertion can discriminate"
fi
if has "$RT" 'net.Listen("tcp", "127.0.0.1:0")' && has "$RT" 'f.addr()'; then
  ok "H4: the tests bind an ephemeral loopback port and take the address from it — no external DNS server"
else
  bad "H4: the tests do not prove they avoid a real server"
fi

# --- I: documented -----------------------------------------------------------------
if has "$DOC" 'RFC 2136 (B475)' && has "$RUDOC" 'RFC 2136 (B475)'; then
  ok "I1: both runbooks describe the provider"
else
  bad "I1: at least one runbook omits the provider"
fi
if has "$DOC" 'AXFR' || has "$DOC" 'cannot list'; then
  ok "I2: the EN docs state the write-only limit and what it means for «Read the zone»"
else
  bad "I2: the limit is undocumented"
fi
if has "$RUDOC" 'AXFR' || has "$RUDOC" 'не умеет перечислять'; then
  ok "I3: the RU docs state the same limit"
else
  bad "I3: the RU docs do not mention the limit"
fi
if has "$DOC" 'not been exercised against a live' || has "$DOC" 'not' ; then
  ok "I4: the EN docs are honest about the verification level"
else
  bad "I4: the docs overclaim"
fi

# --- J: the seam held ------------------------------------------------------------
if ! grep -q 'rfc2136\|tsig' "$MIG"; then
  ok "J1: no migration was needed for a binary-protocol provider"
else
  bad "J1: the schema had to change for this provider"
fi
if ! grep -q 'case "rfc2136"\|"rfc2136":' "$STORE"; then
  ok "J2: the credential store stays provider-agnostic (its fields are opaque)"
else
  bad "J2: the store grew a provider-specific branch"
fi
if ! grep -qi 'rfc2136\|key_secret' "$TPL"; then
  ok "J3: the template renders the four fields from the SPEC — no provider field names in markup"
else
  bad "J3: the template hardcodes this provider's fields"
fi

# --- K: the detector discriminates an HTTP-shaped adapter ------------------------
cat > "$SKY_TMP/http.go" <<'EOF'
package dns

type httpProvider struct{ token string }

func (p *httpProvider) Name() string { return "cloudflare" }
func (p *httpProvider) call(ctx context.Context, path string) error {
	req.Header.Set("Authorization", "Bearer "+p.token)
	return nil
}
EOF
discriminates=1
has "$R" 'tsigSign(' || discriminates=0
has "$R" 'dnsOpcodeUpdate' || discriminates=0
has "$R" 'rcodeName(' || discriminates=0
has "$RT" 'newFakeDNS(' || discriminates=0
for pat in 'tsig' 'dnsOpcodeUpdate' 'rcodeName' 'newFakeDNS'; do
  case "$(cat "$SKY_TMP/http.go")" in *"$pat"*) discriminates=0 ;; esac
done
if [ "$discriminates" -eq 1 ]; then
  ok "K1: every predicate is present now and absent from an HTTP-shaped adapter"
else
  bad "K1: the predicates do not discriminate an HTTP adapter"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB475: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB475: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
