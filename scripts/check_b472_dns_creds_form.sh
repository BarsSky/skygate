#!/usr/bin/env bash
# ============================================================================
# check_b472_dns_creds_form.sh — B472
#
# «ПО КРЕДАМ КУДА ЗАДАТЬ НА САМОМ SKYGATE ЕСТЬ ПОЛЕ ВВОДА?»
#
# The field existed — and could not be used. Three defects on one card, each enough to send the
# operator back to the shell:
#
#   1. THE FORM DEMANDED A CLIENT CERTIFICATE. `cert_pem` was `required` in the template, left over
#      from the mTLS model B468 removed. `Credentials.Validate()` accepts — and for reg.ru EXPECTS —
#      an EMPTY certificate, and a stored one makes the client try mTLS. So the browser refused to
#      submit the only shape the server wanted, and the hint under the form («вставьте SSL cert
#      (PEM) + alt-пароль») told the operator to do exactly that.
#   2. THE TEST BUTTON SAID NOTHING. The handler computed a sentence and threw it away (`_ = ok`
#      plus an empty redirect) while four i18n keys for those sentences sat unused.
#   3. NOTHING SHOWED WHAT DNS SAYS. The CLI has `dns-records`; the page had nothing, so the
#      runbook's first question needed a shell — on the host that is about to be stopped.
#
# Contracts:
#   A. the certificate field is OPTIONAL and its hint says so
#   B. the provider's other required fields stay required (no half a credential)
#   C. the test button's result is FLASHED, with all four statuses worded once
#   D. a read-only "read the zone" action exists and is bounded
#   E. the success banner is translated (no hardcoded English on the page)
#   F. i18n pairs for every new string
#   G. the pure decisions are pinned by tests, and the page by a RENDER test
#   H. the docs tell the operator the panel is usable (EN + RU)
#   I. the detector discriminates the pre-B472 shape
#
# RENEGOTIATED BY B473 (2026-11-14). The card is schema-driven now, so three of the shapes below moved
# without any of the guarantees moving with them:
#   * the certificate is `f_cert_pem`, rendered from `dns.Spec.Fields` instead of a hand-written
#     `<textarea name="cert_pem">` inside a collapsed `<details>`;
#   * login/password are `f_login`/`f_password`, and their `required` comes from the SPEC, so the
#     assertion reads the registry AND the template instead of a literal attribute;
#   * the provider is a `<select>` (a CHOICE) instead of a hidden field, and «Read the zone» goes
#     through the `dns.Provider` interface instead of importing the reg.ru client.
# Everything this block was written for — the browser can submit the shape the server wants, the test
# button answers, the zone can be read from the page — is still asserted.
# ============================================================================
set -uo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b472.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

FORM=internal/feature/admin/ha_dns_form_b472.go
# B473 split the card: the schema-driven save/render lives in its own file, and the provider schema (what
# each provider asks for) lives in the registry the page renders from.
B473=internal/feature/admin/ha_dns_form_b473.go
REG=internal/dns/registry_b473.go
HA=internal/feature/admin/ha.go
TPL=internal/handlers/templates/admin/ha.html
CAT=internal/i18n/catalog_admin.go
TESTS=internal/feature/admin/ha_dns_b472_test.go
RENDERTEST=internal/feature/admin/ha_dns_render_b472_test.go
DOC=docs/ha.md
RUDOC=docs/ru/ha-manual-switch.md
gosurface CMD cmd/skygate/*.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$FORM" "$B473" "$REG" "$HA" "$TPL" "$CAT" "$TESTS" "$RENDERTEST" "$DOC" "$RUDOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the certificate is optional ------------------------------------------------
# B473 renders the field from the registry, so the template has ONE textarea whose name comes from
# `{{.Name}}` (the certificate is its only textarea-kind field today). The contract reads that TAG — not
# the whole file — so a `required` on a sibling input cannot satisfy the assertion by accident, and the
# RENDER test (G-section) pins the concrete `f_cert_pem` tag it produces.
tag=$(python3 - "$TPL" <<'PY'
import re, sys
src = open(sys.argv[1], encoding='utf-8').read()
m = re.search(r'(?s)<textarea[^>]*>', src)
print(m.group(0) if m else '')
PY
)
case "$tag" in
  '') bad "A1: no textarea found — mTLS providers would lose the certificate field entirely" ;;
  *required*) bad "A1: the certificate field is still required: the browser refuses to submit the empty value reg.ru wants" ;;
  *'name="f_{{.Name}}"'*|*'name="f_cert_pem"'*) ok "A1: the certificate textarea is optional in the markup" ;;
  *) bad "A1: the textarea is not a provider-schema field (name is not f_<field>)" ;;
esac
# B473: "optional" is now expressed by the SPEC (Required: false) and by the field's own help text, not
# by folding the input into a <details> block. Both halves are asserted: the schema says optional, and
# the page explains when the field IS needed.
if has "$REG" 'Name: "cert_pem"' && ! python3 - "$REG" <<'PY'
import re, sys
src = open(sys.argv[1], encoding='utf-8').read()
m = re.search(r'(?s)Name:\s*"cert_pem".*?\}', src)
sys.exit(0 if m and re.search(r'Required:\s*true', m.group(0)) else 1)
PY
then
  ok "A2: the certificate is declared OPTIONAL in the provider schema"
else
  bad "A2: the provider schema marks the certificate required (or no longer declares the field)"
fi
if has "$TPL" 'ha.dns_field_cert_help' || has "$TPL" '{{.Help}}'; then
  ok "A3: the field's help text is rendered (it is the registry's HelpKey, resolved by the view)"
else
  bad "A3: no hint — the operator will fill it again and break the client"
fi
if has "$CAT" 'оставьте поле ПУСТЫМ' || has "$CAT" 'leave this field EMPTY'; then
  ok "A4: the help text no longer instructs the operator to paste a certificate"
else
  bad "A4: the stale «paste your SSL cert» instruction survives"
fi
# B473: the provider is a CHOICE now — a <select> generated from the registry (dns.Specs()), which is
# what lets the same form ask a Cloudflare token instead of reg.ru's login. B472.1's actual finding still
# holds and is asserted below: it must not be a readonly INPUT holding an internal alias.
if has "$TPL" '<select name="provider"' && has "$TPL" '{{range .Data.DNSCard.Groups}}'; then
  ok "A5: the provider is a select generated from the provider registry"
else
  bad "A5: the provider is not chosen from the registry — a second provider cannot be configured"
fi
if grep -n -- 'name="provider"[^>]*readonly' "$TPL" >/dev/null 2>&1; then
  bad "A5b: the provider is still a readonly input (reads as a control the operator is denied)"
else
  ok "A5b: no readonly provider input remains"
fi
if has "$TPL" 'ha.dns_zone_hint' && has "$CAT" 'Do NOT put the registrar'; then
  ok "A5c: the zone carries a hint that names the mistake actually made (the registrar's own domain)"
else
  bad "A5c: the zone field is unexplained — `reg.ru` was typed into it live"
fi
if has "$B473" 'zone := normalizeDNSZone(rawZone)'; then
  ok "A5d: the zone is normalised before it reaches the registrar (URL/paste/case/trailing dot)"
else
  bad "A5d: the raw zone goes to the API"
fi

# --- B: the provider's required fields stay required -------------------------------
# B473: `required` is a POLICY of the spec, not an attribute typed into the template. The contract is
# therefore two-sided: the registry declares login/password Required, and the template emits `required`
# from `.Required` — a hardcoded attribute would not survive the next provider.
_b=0
for name in login password; do
  if python3 - "$REG" "$name" <<'PY'
import re, sys
src = open(sys.argv[1], encoding='utf-8').read()
m = re.search(r'(?s)Name:\s*"' + sys.argv[2] + r'".*?\}', src)
sys.exit(0 if m and re.search(r'Required:\s*true', m.group(0)) else 1)
PY
  then _b=$((_b+1)); fi
done
if has "$TPL" '{{if and .Required $editing}}required{{end}}'; then
  _b=$((_b+1))
fi
if has "$TPL" 'name="zone"'; then
  # the zone stays a hand-written required input: it is a COLUMN, not a provider field
  zone_required=$(python3 - "$TPL" <<'PY'
import re, sys
src = open(sys.argv[1], encoding='utf-8').read()
m = re.search(r'(?s)<input[^>]*name="zone"[^>]*>', src)
print('yes' if m and 'required' in m.group(0) else 'no')
PY
)
  [ "$zone_required" = "yes" ] && _b=$((_b+1))
fi
if [ "$_b" -eq 4 ]; then
  ok "B1: zone + the spec's required fields are all still required (a save cannot store half a credential)"
else
  bad "B1: only $_b of 4 required-check signals hold (login, password, template rule, zone)"
fi

# --- C: the test button answers ----------------------------------------------------
if has "$FORM" 'func dnsTestMessage('; then
  ok "C1: the wording of every test outcome lives in one pure function"
else
  bad "C1: no single place that words a test result"
fi
if has "$HA" 'dnsTestMessage(lang, res, err)'; then
  ok "C2: the handler USES that message"
else
  bad "C2: the handler computes a sentence some other way"
fi
if grep -n -- '_ = ok' "$HA" 2>/dev/null | grep -v ':[[:space:]]*//' | grep -q .; then
  bad "C3: the old «compute and drop» shape is still there (the result is discarded)"
else
  ok "C3: no result is thrown away any more"
fi
if has "$HA" 'haRedirect(w, r, msg, "")' && has "$HA" 'haRedirect(w, r, "", msg)'; then
  ok "C4: success and failure travel in their own flash slots"
else
  bad "C4: the outcome is not routed into the flash"
fi
_c=0
for key in ha.dns_test_ok ha.dns_test_auth_error ha.dns_test_network_error ha.dns_test_not_configured; do
  n=$(grep -c "\"$key\"" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -ge 2 ] && has "$FORM" "$key" && _c=$((_c+1))
done
if [ "$_c" -eq 4 ]; then
  ok "C5: all four outcomes use their own (paired) i18n key"
else
  bad "C5: only $_c of 4 outcomes are worded through a paired key"
fi

# --- D: reading the zone ------------------------------------------------------------
if has "$CMD" 'POST /admin/ha/dns/records'; then
  ok "D1: the read-only zone action is routed"
else
  bad "D1: /admin/ha/dns/records is not routed"
fi
if has "$FORM" 'func (s *Service) PostAdminHADNSRecords(' && has "$FORM" 'provider.ListRecords('; then
  ok "D2: it reads the zone through the PROVIDER interface the CLI also builds"
else
  bad "D2: the page reads DNS some other way"
fi
if has "$FORM" 'if len(out) >= 12 {'; then
  ok "D3: the names list is bounded (the flash is one line, a zone has hundreds of records)"
else
  bad "D3: an unbounded names field can produce a URL-sized flash"
fi
if has "$TPL" 'action="/admin/ha/dns/records"' && has "$TPL" 'name="names"'; then
  ok "D4: the button and its input are on the page"
else
  bad "D4: the action exists but nothing calls it"
fi

# --- E: no hardcoded English on the card -------------------------------------------
if has "$TPL" 'the configured DNS provider credentials configured'; then
  bad "E1: the hardcoded English success line is still in the template"
else
  ok "E1: the success banner is translated"
fi
if has "$TPL" 'ha.dns_configured_ok'; then
  ok "E2: the translated banner is actually rendered"
else
  bad "E2: the banner key exists but the template does not use it"
fi

# --- F: i18n pairs ------------------------------------------------------------------
# B473 removed `ha.dns_cert_optional` / `ha.dns_cert_hint` / `ha.dns_login` / `ha.dns_password` /
# `ha.dns_cert` from the catalogues: the card's prose is per PROVIDER FIELD now (the registry holds the
# keys), so those five strings have no reader left. The B472 strings that still have one are asserted.
_f=0
for key in ha.dns_records ha.dns_records_names \
           ha.dns_records_ok ha.dns_records_no_a ha.dns_records_err ha.dns_configured_ok \
           ha.dns_test_error ha.dns_test_other ha.dns_store_missing \
           ha.dns_field_cert_help ha.dns_zone_hint; do
  n=$(grep -c "\"$key\"" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -ge 2 ] && _f=$((_f+1))
done
if [ "$_f" -ge 11 ]; then
  ok "F1: every new string is in BOTH catalogues (RU + EN)"
else
  bad "F1: only $_f of 11 new keys are paired"
fi

# --- G: tests -----------------------------------------------------------------------
_g=0
for t in TestB472_TestMessageCoversEveryStatus TestB472_TestMessageKeepsOneLine \
         TestB472_ParseDNSNames TestB472_RecordsSummary \
         TestB472_1_ZoneIsNormalised TestB472_1_ProviderIsAFact; do
  has "$TESTS" "func $t(" && _g=$((_g+1))
done
if [ "$_g" -ge 6 ]; then
  ok "G1: all four test outcomes, the one-line rule, the names field, the zone summary, the zone normaliser and the provider post are pinned"
else
  bad "G1: only $_g of 6 behaviour tests exist"
fi
_h=0
for t in TestB472_CertificateIsOptional TestB472_CardOffersBothButtons TestB472_FlashRenders; do
  has "$RENDERTEST" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -ge 3 ]; then
  ok "G2: the RENDER test executes the template (the required-gate is only visible in markup)"
else
  bad "G2: only $_h of 3 render tests exist"
fi
if has "$RENDERTEST" 'FindString(html)'; then
  ok "G3: the render test isolates the field TAG before looking for required"
else
  bad "G3: the render test does not isolate the tag — a `required` elsewhere could mask the gate"
fi

# --- H: documented ------------------------------------------------------------------
if has "$DOC" 'admin/ha'; then
  ok "H1: the EN runbook says the credentials can be set from the panel"
else
  bad "H1: the runbook still sends the operator to the CLI only"
fi
if has "$RUDOC" 'admin/ha'; then
  ok "H2: the RU runbook says the same"
else
  bad "H2: the RU runbook has no mention"
fi

# --- I: the detector discriminates the pre-B472 shape ------------------------------
cat > "$SKY_TMP/old.html" <<'EOF'
<form method="POST" action="/admin/ha/dns/save">
  <textarea name="cert_pem" rows="5" required></textarea>
  <button type="submit" form="dns-test" formaction="/admin/ha/dns/test">Test</button>
</form>
<div class="alert alert-success">the configured DNS provider credentials configured.</div>
EOF
discriminates=1
has "$TPL" '<textarea name="f_{{.Name}}"' || discriminates=0
has "$TPL" 'action="/admin/ha/dns/records"' || discriminates=0
has "$FORM" 'func dnsTestMessage(' || discriminates=0
has "$CAT" 'ha.dns_field_cert_help' || discriminates=0
# The old shape must be REJECTED by the same predicates the contracts use: it carries `required` on
# cert_pem, no schema-driven field tag and no read-the-zone form.
case "$(cat "$SKY_TMP/old.html")" in
  *'name="f_cert_pem"'*|*'dns/records'*) discriminates=0 ;;
esac
if has "$SKY_TMP/old.html" 'required' && has "$SKY_TMP/old.html" 'cert_pem'; then
  : # the old shape carries exactly the gate the contract rejects
else
  discriminates=0
fi
if [ "$discriminates" -eq 1 ]; then
  ok "I1: every predicate is present now and absent from the pre-B472 shape"
else
  bad "I1: the predicates do not discriminate the pre-B472 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB472: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB472: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
