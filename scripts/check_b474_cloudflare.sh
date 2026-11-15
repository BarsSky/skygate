#!/usr/bin/env bash
# ============================================================================
# check_b474_cloudflare.sh — B474
#
# THE SEAM'S FIRST REAL TEST. B473 extracted a provider interface, a registry and a credential table out
# of ONE provider (reg.ru), which is exactly the situation in which a "seam" can be an illusion: it only
# ever gets exercised by the code it was cut from. Cloudflare is the honest test — an API TOKEN instead of
# a login/password pair, and AAAA/TXT/TTL that the shipped reg.ru client cannot do.
#
# What this block had to add: ONE adapter, ONE registry entry, ONE BuildProvider case, one catalogue pair,
# one doc subsection. It did NOT touch the credential table, the migration chain, the save handler or the
# template — and that is the property worth asserting, because the moment a second provider needs a
# template edit or a schema change, the seam is not a seam.
#
# Contracts:
#   A. the adapter exists, satisfies the interface, and is reachable from BuildProvider
#   B. the registry entry is complete and its capabilities are the full set (a second provider with
#      reg.ru's chips would make the badges meaningless)
#   C. the token is a SECRET: sent as a bearer header, never prefilled into the page
#   D. the API's envelope is honoured — HTTP 200 with success:false is a FAILURE
#   E. records: create-then-patch (not delete+recreate), proxied refusal, ErrRecordNotFound
#   F. TTL is validated against the API's accepted set, with the set named
#   G. TXT: create + delete of the EXACT value, idempotent repeat, bounded pagination, short names
#   H. the tests are behavioural against a FAKE API (no network, no live account)
#   I. nothing about the storage or the page had to change (the seam claim, asserted)
#   J. documented in EN + RU, and the doc does not claim it was tested live
#   K. the detector discriminates a reg.ru-shaped adapter
# ============================================================================
set -uo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b474.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

CF=internal/dns/cloudflare_b474.go
CFT=internal/dns/cloudflare_b474_test.go
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

for f in "$CF" "$CFT" "$REG" "$BUILD" "$IFACE" "$CAT" "$CARD" "$TPL" "$STORE" "$MIG" "$DOC" "$RUDOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the adapter exists and is reachable ---------------------------------------
if has "$CF" 'var _ Provider = (*cloudflareProvider)(nil)'; then
  ok "A1: the adapter asserts the contract at compile time (a missing method fails the build, not a failover)"
else
  bad "A1: nothing proves the adapter satisfies dns.Provider"
fi
if has "$BUILD" 'case "cloudflare":'; then
  ok "A2: BuildProvider builds it by name"
else
  bad "A2: the adapter is unreachable — the registry would offer a provider that cannot be built"
fi
if has "$BUILD" 'errCloudflareNoToken()' && has "$CF" 'func errCloudflareNoToken() error'; then
  ok "A3: a missing token is refused with an actionable message instead of a 401 from the API"
else
  bad "A3: a missing token produces a provider that fails later"
fi
if has "$CF" 'cloudflareTokenFromEnv()' && has "$CF" 'SKYGATE_CLOUDFLARE_TOKEN'; then
  ok "A4: the documented env fallback exists for a CLI run before anything is saved"
else
  bad "A4: no way to test a token without saving a row"
fi
if has "$IFACE" 'Capabilities() Capabilities' && has "$CF" 'func (p *cloudflareProvider) Capabilities()'; then
  ok "A5: the second provider answers capabilities through the SAME contract"
else
  bad "A5: the adapter does not implement Capabilities"
fi

# --- B: the registry entry --------------------------------------------------------
if has "$REG" 'var cloudflareSpec = Spec{'; then
  ok "B1: the provider is declared in the registry (the panel renders it without a template edit)"
else
  bad "B1: Cloudflare is not in the registry — the card could not offer it"
fi
if has "$REG" '"cloudflare"' && has "$REG" 'Kind: FieldPassword, Required: true,'; then
  ok "B2: the one credential is a REQUIRED masked field (a token is a secret)"
else
  bad "B2: the token field is missing, optional or unmasked"
fi
if has "$REG" 'Caps: Capabilities{A: true, AAAA: true, TXT: true, TTL: true}'; then
  ok "B3: its capabilities are the full set — and they are genuinely implemented, unlike reg.ru's"
else
  bad "B3: the Cloudflare capabilities are understated or copied from reg.ru"
fi
_cf_pairs=0
for key in ha.dns_field_cf_token ha.dns_field_cf_token_help; do
  n=$(grep -c "\"$key\"" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -ge 2 ] && _cf_pairs=$((_cf_pairs+1))
done
if [ "$_cf_pairs" -eq 2 ]; then
  ok "B4: both label and help exist in RU and EN (a Russian label compiled into Go is invisible to EN)"
else
  bad "B4: only $_cf_pairs of 2 catalogue keys are paired"
fi
# B475 appended its own spec to the same list, so this asserts MEMBERSHIP rather than the exact literal:
# what matters is that Cloudflare is in the display order, not that it is the last entry forever.
if grep -q 'providerSpecs = \[\]Spec{.*cloudflareSpec' "$REG"; then
  ok "B5: the registry lists it, so the select, the groups and the badges all pick it up"
else
  bad "B5: the spec is declared but not listed — nothing would render it"
fi

# --- C: the token is a secret -----------------------------------------------------
if has "$CF" 'req.Header.Set("Authorization", "Bearer "+p.token)'; then
  ok "C1: the token travels in the Authorization header (a query parameter leaks into proxy logs)"
else
  bad "C1: the token is not sent as a bearer header"
fi
if grep -q 'Query()\.Set("token"\|?token=\|access_token=' "$CF" 2>/dev/null; then
  bad "C2: the token is placed in the URL"
else
  ok "C2: the token never appears in a URL"
fi
if has "$CARD" 'if !dnsFieldIsSecret(f.Kind) {' && has "$REG" 'Kind: FieldPassword'; then
  ok "C3: the existing prefill rule covers it — a stored token is never rendered back"
else
  bad "C3: a stored token could be prefilled into the page"
fi

# --- D: the envelope is honoured --------------------------------------------------
if has "$CF" 'case !env.Success:'; then
  ok "D1: HTTP 200 with success:false is a FAILURE (the trap that makes a panel report a false ok)"
else
  bad "D1: a success:false answer would be read as success"
fi
if has "$CF" 'case resp.StatusCode == http.StatusUnauthorized'; then
  ok "D2: a rejected token names the permission it needs, not just a status code"
else
  bad "D2: an auth failure is reported as a bare HTTP error"
fi
if has "$CF" 'the token cannot see the zone'; then
  ok "D3: an unreadable zone says which zone and which permission, instead of an empty record set"
else
  bad "D3: an invisible zone is indistinguishable from "no records""
fi

# --- E: records -------------------------------------------------------------------
if has "$CF" 'http.MethodPatch, "/zones/"'; then
  ok "E1: an existing record is PATCHED (delete+recreate would drop its id and look like a gap)"
else
  bad "E1: the update path does not patch the existing record"
fi
if has "$CF" 'if err := p.call(ctx, http.MethodPost, "/zones/"+id+"/dns_records"'; then
  ok "E2: a missing record is CREATED (the failover's fresh-deploy path)"
else
  bad "E2: there is no create path"
fi
if has "$CF" 'the A record %s is PROXIED'; then
  ok "E3: a proxied record is REFUSED — rewriting it would be a switch with no visible effect"
else
  bad "E3: a proxied record would be silently rewritten"
fi
if has "$CF" 'if net.ParseIP(strings.TrimSpace(ip)) == nil {'; then
  ok "E4: a non-IP target is refused before any request"
else
  bad "E4: an invalid address is sent to the API"
fi
if has "$CF" 'return "", ErrRecordNotFound'; then
  ok "E5: a missing record is ErrRecordNotFound (a fresh deploy), not an empty address"
else
  bad "E5: the empty state is not distinguished"
fi

# --- F: TTL ----------------------------------------------------------------------
if has "$CF" 'var cloudflareTTLsAllowed = []int{1, 60, 120, 300'; then
  ok "F1: the accepted TTL set is stated in the code the error message reads"
else
  bad "F1: the TTL set lives only in the API's documentation"
fi
if has "$CF" 'is not accepted; allowed: %v' ; then
  ok "F2: a rejected TTL names the allowed values instead of a provider error code"
else
  bad "F2: a rejected TTL produces an unhelpful error"
fi
if has "$CF" 'func (p *cloudflareProvider) SetTTL'; then
  ok "F3: the switch runbook's «lower the TTL a day before» is implemented for this provider"
else
  bad "F3: SetTTL is missing"
fi

# --- G: TXT ----------------------------------------------------------------------
if has "$CF" 'func (p *cloudflareProvider) CreateTXT' && has "$CF" 'func (p *cloudflareProvider) DeleteTXT'; then
  ok "G1: the DNS-01 half exists for real (reg.ru answers ErrUnsupported here)"
else
  bad "G1: TXT operations are missing"
fi
if has "$CF" 'if want != "" && strings.TrimSpace(r.Content) != want {'; then
  ok "G2: DeleteTXT removes the EXACT value — another challenge at the same name survives"
else
  bad "G2: a delete could remove an unrelated challenge record"
fi
if has "$CF" 'const cloudflareMaxPages = 50'; then
  ok "G3: pagination is bounded"
else
  bad "G3: ListRecords can loop unbounded against a rate-limited API"
fi
if has "$CF" 'func cfShortName(' && has "$CF" 'Name: cfShortName(r.Name, zone)'; then
  ok "G4: names come back SHORT, so the panel's summary matches what the operator typed"
else
  bad "G4: ListRecords returns FQDNs the page then has to guess about"
fi

# --- H: behavioural tests against a fake API -------------------------------------
_h=0
for t in TestB474_TokenTravelsInTheHeader TestB474_GetRecordAndNotFound \
         TestB474_UpdateCreatesThenPatches TestB474_ProxiedRecordIsRefused \
         TestB474_SetTTLValidatesAndWrites TestB474_TXTRoundTrip \
         TestB474_ListRecordsStripsTheZone TestB474_SuccessFalseIsAFailure \
         TestB474_UnreadableZoneNamesThePermission TestB474_CapabilitiesAreTheAdaptersTruth \
         TestB474_NoTokenIsActionable; do
  has "$CFT" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -eq 11 ]; then
  ok "H1: eleven behavioural tests cover the wire (header, envelope, create/patch, proxy, TTL, TXT, names)"
else
  bad "H1: only $_h of 11 adapter tests exist"
fi
if has "$CFT" 'httptest.NewServer' && ! grep -q 'https://api.cloudflare.com/client/v4' "$CFT"; then
  ok "H2: the tests run against a FAKE API — no live account, no network"
else
  bad "H2: the tests point at the real endpoint"
fi
if has "$CFT" 'a defect of the FAKE that looked exactly like an adapter'; then
  ok "H3: the fake's own id-collision defect is documented where it was fixed"
else
  bad "H3: the fake can hand two records the same id (that made a delete remove both)"
fi

# --- I: the seam claim ------------------------------------------------------------
# A second provider must not have required a schema change, a migration, a template edit or a new
# settings key. If any of those had been needed, B473 was an extraction rather than a seam.
if has "$MIG" 'dns_provider_creds' && ! grep -q 'cloudflare' "$MIG"; then
  ok "I1: the v0.84 schema is provider-agnostic (no cloudflare column, no second migration)"
else
  bad "I1: the storage had to change for a second provider — the seam is not a seam"
fi
if grep -q 'cloudflare' "$STORE"; then
  bad "I2: the credential STORE knows about cloudflare (a provider-specific branch in generic storage)"
else
  ok "I2: the storage is provider-agnostic — the row's fields are opaque there"
fi
if ! grep -q 'name="f_token"\|name="token"' "$TPL"; then
  ok "I3: the template renders the token field from the SPEC (no provider field names in the markup)"
else
  bad "I3: the template hardcodes the token field"
fi

# --- J: documented ------------------------------------------------------------------
if has "$DOC" 'Cloudflare (B474)' && has "$RUDOC" 'Cloudflare (B474)'; then
  ok "J1: both runbooks describe the provider, its token permissions and its TTL set"
else
  bad "J1: at least one runbook does not describe Cloudflare"
fi
if has "$DOC" 'exercised against a live account' && has "$RUDOC" 'прогонялся'; then
  ok "J2: the docs say it was verified against a fake API, not a live account"
else
  bad "J2: the docs do not state the verification limits"
fi
if has "$DOC" 'orange-cloud' || has "$DOC" 'proxied'; then
  ok "J3: the proxied-record refusal is documented before the operator hits it"
else
  bad "J3: the proxied-record behaviour is undocumented"
fi

# --- K: the detector discriminates a reg.ru-shaped adapter ------------------------
cat > "$SKY_TMP/old.go" <<'EOF'
package dns

type fakeProvider struct{ login, password string }

func (p *fakeProvider) Name() string { return "external" }
func (p *fakeProvider) GetRecord(ctx context.Context, zone, name string) (string, error) {
	return p.login, nil
}
EOF
discriminates=1
has "$CF" 'Authorization", "Bearer "' || discriminates=0
has "$CF" 'case !env.Success:' || discriminates=0
has "$CF" 'cloudflareTTLsAllowed' || discriminates=0
has "$REG" 'cloudflareSpec' || discriminates=0
# The pre-B474 shape (a login/password adapter) must carry NONE of the predicates above.
for pat in 'Authorization' 'success' 'cloudflareTTLsAllowed' 'cloudflareSpec'; do
  case "$(cat "$SKY_TMP/old.go")" in *"$pat"*) discriminates=0 ;; esac
done
if has "$SKY_TMP/old.go" 'password'; then :; else discriminates=0; fi
if [ "$discriminates" -eq 1 ]; then
  ok "K1: every predicate is present now and absent from a reg.ru-shaped adapter"
else
  bad "K1: the predicates do not discriminate a reg.ru-shaped adapter"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB474: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB474: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
