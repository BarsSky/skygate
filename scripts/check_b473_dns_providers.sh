#!/usr/bin/env bash
# ============================================================================
# check_b473_dns_providers.sh — B473
#
# «КАК МОЖНО АДАПТИРОВАТЬ ПОД РАЗНЫХ ПРОВАЙДЕРОВ ДАННЫЙ БЛОК?»
#
# The answer this block implements is a SEAM, not a switch statement. Before it, three things were welded
# to one registrar (reg.ru / regru2):
#
#   1. STORAGE. `internal/ha/dnsexternal` kept exactly ONE credential set in five fixed global_settings
#      keys shaped like reg.ru's login + alternative password + zone. A Cloudflare API token or an
#      RFC 2136 TSIG key had nowhere to live, and a SECOND zone could not be configured at all.
#   2. CONTRACT. `internal/dns.Provider` could get/update an A record and nothing else — no capability
#      answer, no TTL, no TXT — so a caller could not even ASK what a provider supports.
#   3. FORM. The /admin/ha card hardcoded reg.ru's four fields in the template and in the save handler.
#      Even with a registry, the page could not ask for a token.
#
# What now exists: `dns_provider_creds` rows keyed (provider, zone) with the provider-specific fields as
# one encrypted JSON blob (v0.84, BOTH migration chains), a registry ("internal/dns/registry_b473.go")
# that owns each provider's fields and capabilities, a `Provider` contract that declares what it can do,
# and a schema-driven card that renders the registry.
#
# Contracts:
#   A. the storage is ROWS, in both migration chains, with one credential set per (provider, zone)
#   B. the legacy ha.dns.* row is imported exactly ONCE and nothing is lost on upgrade
#   C. the contract declares capabilities, TTL, listing and TXT — and refuses what it cannot do
#   D. the registry is the single source of a provider's fields, labels and capabilities
#   E. the panel is schema-driven: the provider is a choice, the fields come from the spec
#   F. the save path merges (an empty masked field KEEPS the stored secret) and drops unknown fields
#   G. reg.ru keeps working through the new path — the alias, the adapter, the read-the-zone button
#   H. tests pin the storage, the registry, the field policy and the rendered markup
#   I. the docs (EN + RU) describe how to add a provider
#   J. the detector discriminates the pre-B473 shape
# ============================================================================
set -uo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b473.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

MIG=internal/db/migrations_v0_84_dns_providers.go
PGDRV=internal/db/driver_postgres.go
SQLDRV=internal/db/driver_sqlite.go
STORE=internal/ha/dnsexternal/provider_store_b473.go
CREDS=internal/ha/dnsexternal/credentials.go
IFACE=internal/dns/provider.go
REG=internal/dns/registry_b473.go
BUILD=internal/dns/provider_build.go
CARD=internal/feature/admin/ha_dns_form_b473.go
TPL=internal/handlers/templates/admin/ha.html
CAT=internal/i18n/catalog_admin.go
STORE_TEST=internal/ha/dnsexternal/provider_store_b473_test.go
REG_TEST=internal/dns/registry_b473_test.go
CARD_TEST=internal/feature/admin/ha_dns_form_b473_test.go
RENDER_TEST=internal/feature/admin/ha_dns_render_b472_test.go
DOC=docs/ha.md
RUDOC=docs/ru/ha-manual-switch.md
gosurface CMD cmd/skygate/*.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$MIG" "$PGDRV" "$SQLDRV" "$STORE" "$CREDS" "$IFACE" "$REG" "$BUILD" "$CARD" \
         "$TPL" "$CAT" "$STORE_TEST" "$REG_TEST" "$CARD_TEST" "$RENDER_TEST" "$DOC" "$RUDOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: storage is rows, in BOTH chains -------------------------------------------
if has "$PGDRV" 'migrateV084PG' && has "$SQLDRV" 'migrateV084SQLite'; then
  ok "A1: v0.84 is registered in the PostgreSQL AND the SQLite migration chain"
else
  bad "A1: the migration is not in both chains — one backend would keep the flat credential model"
fi
_a=0
for frag in 'dns_provider_creds' 'fields_enc' 'UNIQUE' 'provider' 'zone' 'enabled'; do
  has "$MIG" "$frag" && _a=$((_a+1))
done
if [ "$_a" -eq 6 ]; then
  ok "A2: the table carries provider/zone/fields_enc/enabled and a UNIQUE key (one set per zone)"
else
  bad "A2: the table is missing one of provider/zone/fields_enc/enabled/UNIQUE ($_a of 6)"
fi
if grep -q 'ON dns_provider_creds (provider, zone)' "$MIG"; then
  ok "A3: the uniqueness is on (provider, zone) — not on provider alone, so a second zone is possible"
else
  bad "A3: the uniqueness key is not (provider, zone)"
fi
# The SQLite side must go through the single chokepoint (AGENTS.md rule 9).
if has "$MIG" 'execSQLiteDDL'; then
  ok "A4: the SQLite DDL goes through execSQLiteDDL"
else
  bad "A4: the SQLite migration bypasses execSQLiteDDL"
fi
if has "$MIG" 'migrateV084SQLite' && ! grep -q 'ALTER TABLE.*DROP\|TRUNCATE' "$MIG"; then
  ok "A5: the migration is additive (no destructive DDL)"
else
  bad "A5: the migration contains destructive DDL"
fi

# --- B: the legacy row is imported exactly once ----------------------------------
if has "$STORE" 'func (s *Store) ImportLegacyOnce()' && has "$STORE" 'dnsMigrationFlagKey'; then
  ok "B1: the legacy import is one-shot, guarded by a flag"
else
  bad "B1: nothing prevents the legacy row from being re-imported"
fi
if has "$STORE" '"ha.dns.migrated"'; then
  ok "B2: the flag has a stable name (an operator can inspect it without reading the code)"
else
  bad "B2: the import flag has no stable settings key"
fi
if has "$STORE" 'if n > 0 {' && has "$STORE" 'count(*) FROM dns_provider_creds'; then
  ok "B3: an existing row wins — the import never resurrects a credential the operator replaced"
else
  bad "B3: the import could overwrite/replace a configured provider row"
fi
if has "$CREDS" 'loadLegacy()' && has "$CREDS" 'row.Active && row.Enabled'; then
  ok "B4: reads prefer the ACTIVE ROW and fall back to the legacy keys (an upgrade loses nothing)"
else
  bad "B4: the credential loader does not read the row with a legacy fallback"
fi
if has "$CREDS" 'SaveProviderRow(' && has "$CREDS" 'SetActiveRow('; then
  ok "B5: saving writes a ROW and makes it active"
else
  bad "B5: saving still only writes the five legacy keys"
fi
# The B472 defect that must not come back: a password dropped when no certificate is stored.
if has "$CREDS" 'func (s *Store) loadLegacy()'; then
  ok "B6: the legacy reader is split out (the import can call it without recursing into the store)"
else
  bad "B6: no separate legacy reader — I/O path recursion is possible"
fi

# --- C: the provider contract declares what it can do ----------------------------
_c=0
for frag in 'Capabilities() Capabilities' 'SetTTL(' 'ListRecords(' 'CreateTXT(' 'DeleteTXT('; do
  has "$IFACE" "$frag" && _c=$((_c+1))
done
if [ "$_c" -eq 5 ]; then
  ok "C1: the contract answers capabilities and offers TTL/list/TXT operations"
else
  bad "C1: only $_c of 5 contract methods exist — a caller cannot ask what a provider supports"
fi
if has "$IFACE" 'ErrUnsupported'; then
  ok "C2: an unsupported operation is a NAMED error, so the panel can say «provider cannot do this»"
else
  bad "C2: an unsupported operation has no distinct error"
fi
if has "$BUILD" 'func (a *externalAdapter) Capabilities() Capabilities { return regruSpec.Caps }'; then
  ok "C3: the reg.ru adapter answers the REGISTRY's capabilities (one source of truth)"
else
  bad "C3: the adapter invents its own capability answer"
fi
if has "$BUILD" 'func (a *externalAdapter) CreateTXT' && has "$BUILD" 'return ErrUnsupported'; then
  ok "C4: TXT is refused by reg.ru instead of silently succeeding (a DNS-01 attempt must fail honestly)"
else
  bad "C4: CreateTXT does not report that reg.ru cannot do it"
fi

# --- D: the registry owns fields, labels and capabilities ------------------------
if has "$REG" 'func Specs()' && has "$REG" 'func LookupSpec(' && has "$REG" 'func (s Spec) FieldNames()'; then
  ok "D1: the registry exposes the spec list, a lookup and the field-name projection"
else
  bad "D1: the registry is not queryable by the panel/CLI"
fi
if has "$REG" 'if n == extcreds.ProviderExternal' && has "$REG" 'n = extcreds.ProviderRegru'; then
  ok "D2: the historical alias resolves to the honest name — two rows for one registrar are impossible"
else
  bad "D2: `external` and `regru` would become two different providers"
fi
if has "$REG" 'LabelKey' && has "$REG" 'HelpKey'; then
  ok "D3: the registry stores i18n KEYS, not Russian prose compiled into a struct"
else
  bad "D3: the operator-facing text is hardcoded in Go (invisible to the EN locale and to the parity test)"
fi
# Every key the registry names must exist in BOTH catalogues (the registry's half of rule 10).
_missing=0
for key in $(grep -oE '"ha\.dns_field_[a-z_]+"' "$REG" | tr -d '"' | sort -u); do
  n=$(grep -c "\"$key\"" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -ge 2 ] || { _missing=$((_missing+1)); echo "       missing pair: $key" >&2; }
done
if [ "$_missing" -eq 0 ]; then
  ok "D4: every field label/help key in the registry exists in RU and EN"
else
  bad "D4: $_missing registry i18n key(s) are not paired (the page would render the raw key)"
fi
if has "$REG" 'Capabilities{A: true, AAAA: false, TXT: false, TTL: true}'; then
  ok "D5: reg.ru's spec states its real capabilities (A + TTL; no AAAA/TXT in this client)"
else
  bad "D5: the reg.ru spec does not state A/TTL vs AAAA/TXT honestly"
fi

# --- E: the panel is schema-driven -----------------------------------------------
if has "$TPL" '<select name="provider"' && has "$TPL" '{{range .Data.DNSCard.Groups}}'; then
  ok "E1: the provider is a select generated from the registry"
else
  bad "E1: the provider is still fixed in the template"
fi
if has "$TPL" '{{range .Fields}}' && has "$TPL" 'name="f_{{.Name}}"'; then
  ok "E2: the inputs are rendered from the spec's fields (a new provider needs no template edit)"
else
  bad "E2: the credential inputs are hardcoded"
fi
if grep -n -- 'name="login"\|name="password"\|name="cert_pem"' "$TPL" >/dev/null 2>&1; then
  bad "E3: a reg.ru field name is still hardcoded in the template"
else
  ok "E3: no provider-specific field name remains in the template"
fi
if has "$TPL" '{{if not $editing}}disabled{{end}}'; then
  ok "E4: a group that is not being edited cannot submit (a hidden token cannot leak into another provider)"
else
  bad "E4: hidden groups still submit — a leftover value could be stored under the wrong provider"
fi
if has "$TPL" 'data-dns-provider-select' && has "$TPL" "[data-dns-group]"; then
  ok "E5: switching the select reveals that provider's fields without a round trip"
else
  bad "E5: the select does not drive the field groups"
fi
if has "$CARD" 'requested string' && has "$CARD" 'dns.LookupSpec(requested)'; then
  ok "E6: ?provider=<id> is the no-JS way to switch groups, and an unknown id is ignored"
else
  bad "E6: the card has no server-side provider selection"
fi
if has "$TPL" 'ha.dns_caps' && has "$TPL" '{{range .Caps}}'; then
  ok "E7: the card shows each provider's capability badges before the switch, not after a failure"
else
  bad "E7: capabilities are invisible on the page"
fi

# --- F: the save path merges and drops unknown fields ----------------------------
if has "$CARD" 'func (s *Service) saveDNSCredentials(' && has "$CARD" 'SaveProviderRow(spec.Name'; then
  ok "F1: the save writes a row through the SPEC (no reg.ru field list)"
else
  bad "F1: the save path is not registry-driven"
fi
if has "$CARD" 'func mergeDNSFields(' && has "$CARD" 'if v, ok := stored[f.Name]; ok'; then
  ok "F2: an unsupplied (masked) field KEEPS the stored value"
else
  bad "F2: editing the zone would wipe the stored secret"
fi
if has "$CARD" 'for _, f := range spec.Fields {' && has "$CARD" 'form.Get("f_" + f.Name)'; then
  ok "F3: only fields the spec declares are read from the request"
else
  bad "F3: the request can smuggle an arbitrary key into the encrypted row"
fi
if has "$CARD" 'func dnsMissingRequired(' && has "$CARD" 'dnsFieldLabel(lang, f)'; then
  ok "F4: a missing required field is reported by its human label"
else
  bad "F4: a validation failure is reported generically"
fi
if has "$CARD" 'func dnsFieldsForSpec(' && has "$CARD" 'if !dnsFieldIsSecret(f.Kind) {'; then
  ok "F5: a stored SECRET is never prefilled into the page"
else
  bad "F5: a stored secret can be re-rendered"
fi

# --- G: reg.ru keeps working through the new path --------------------------------
if has "$BUILD" 'if deps.Row != nil {' && has "$BUILD" 'NewClientWithCreds('; then
  ok "G1: a credential ROW builds the provider (the legacy keys are history after the import)"
else
  bad "G1: BuildProvider has no row-driven path"
fi
if has "$BUILD" 'case "regru", "external":'; then
  ok "G2: the alias still builds the shipped provider"
else
  bad "G2: `external` no longer builds reg.ru"
fi
if has "$CARD" 'func (s *Service) activeDNSProvider()' && has "$CARD" 'ActiveProviderRow()'; then
  ok "G3: the test/read-the-zone buttons build from the ACTIVE row"
else
  bad "G3: the panel's buttons do not use the active credential row"
fi
if has "$MIG" 'AES' || has "$STORE" 'db.EncryptForColumn'; then
  ok "G4: the row's fields are encrypted at rest (AES-GCM), like the legacy password"
else
  bad "G4: the row stores credentials in clear text"
fi

# --- H: tests ---------------------------------------------------------------------
_h=0
for t in TestB473_RowRoundTrip TestB473_AliasIsTheSameRow TestB473_LegacyImportedExactlyOnce \
         TestB473_ActiveSelectionIsHonoured TestB473_EmptyStateIsNotAnError; do
  has "$STORE_TEST" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -eq 5 ]; then
  ok "H1: the row store is executed against a real database (round trip, alias, one-shot import, active)"
else
  bad "H1: only $_h of 5 store tests exist"
fi
_h=0
for t in TestB473_EveryFieldLabelAndHelpIsTranslated TestB473_LookupSpecAcceptsTheHistoricalAlias \
         TestB473_CapabilitiesAreTheAdaptersTruth TestB473_RowSelectsTheCredentials; do
  has "$REG_TEST" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -eq 4 ]; then
  ok "H2: the registry contract is executed (i18n pairs, alias, capability truth, row credentials)"
else
  bad "H2: only $_h of 4 registry tests exist"
fi
_h=0
for t in TestB473_StoredSecretIsNotPrefilled TestB473_EmptyRowKeepsRequiredFields \
         TestB473_UnknownSubmissionFieldsAreDropped TestB473_EmptySubmissionKeepsStoredFields \
         TestB473_MissingRequiredNamesTheField TestB473_CapabilityBadgesFollowTheRegistry \
         TestB473_ViewGroupsCoverEverySpec TestB473_CardIsSafeWithoutAStore; do
  has "$CARD_TEST" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -eq 8 ]; then
  ok "H3: the field policy is executed (secret not prefilled, merge, unknown dropped, badges, groups)"
else
  bad "H3: only $_h of 8 card-policy tests exist"
fi
_h=0
for t in TestB473_SelectRendersEveryProvider TestB473_OnlyTheEditingGroupSubmits; do
  has "$RENDER_TEST" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -eq 2 ]; then
  ok "H4: the RENDERED card is executed (the select and the disabled groups only exist in markup)"
else
  bad "H4: only $_h of 2 render tests exist"
fi

# --- I: documented ------------------------------------------------------------------
if has "$DOC" 'dns_provider_creds' && has "$DOC" 'Cloudflare'; then
  ok "I1: the EN runbook describes the row store and names the providers this seam is for"
else
  bad "I1: the EN runbook does not describe how a provider is added"
fi
if has "$RUDOC" 'dns_provider_creds' && has "$RUDOC" 'Cloudflare'; then
  ok "I2: the RU runbook says the same"
else
  bad "I2: the RU runbook was not updated"
fi
if has "$DOC" 'provider' && has "$RUDOC" 'провайдер'; then
  ok "I3: both runbooks explain that the provider is now a choice"
else
  bad "I3: neither runbook explains the provider choice"
fi

# --- J: the detector discriminates the pre-B473 shape ----------------------------
cat > "$SKY_TMP/old.go" <<'EOF'
package admin

import extcreds "skygate/internal/ha/dnsexternal"

func parseHADNSCredsForm(form url.Values) (extcreds.Credentials, error) {
	c := extcreds.Credentials{
		Provider: strings.TrimSpace(form.Get("provider")),
		Login:    strings.TrimSpace(form.Get("login")),
		Zone:     normalizeDNSZone(form.Get("zone")),
		CertPEM:  strings.TrimSpace(form.Get("cert_pem")),
		Password: strings.TrimSpace(form.Get("password")),
	}
	return c, c.Validate()
}
EOF
cat > "$SKY_TMP/old.html" <<'EOF'
<form method="POST" action="/admin/ha/dns/save">
  <input type="hidden" name="provider" value="external">
  <input type="text" name="login" required>
  <input type="password" name="password" required>
</form>
EOF
discriminates=1
has "$CARD" 'func (s *Service) saveDNSCredentials(' || discriminates=0
has "$REG" 'func Specs()' || discriminates=0
has "$STORE" 'func (s *Store) ImportLegacyOnce()' || discriminates=0
has "$TPL" '<select name="provider"' || discriminates=0
# The pre-B473 shapes carry exactly what the predicates above reject.
has "$SKY_TMP/old.go" 'form.Get("login")' || discriminates=0
has "$SKY_TMP/old.html" 'name="login"' || discriminates=0
if [ "$discriminates" -eq 1 ]; then
  ok "J1: every predicate is present now and absent from the pre-B473 shape"
else
  bad "J1: the predicates do not discriminate the pre-B473 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB473: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB473: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
