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
#   A. the certificate field is OPTIONAL and folded away with an explanation
#   B. the other three fields stay required (no half a credential)
#   C. the test button's result is FLASHED, with all four statuses worded once
#   D. a read-only "read the zone" action exists and is bounded
#   E. the success banner is translated (no hardcoded English on the page)
#   F. i18n pairs for every new string
#   G. the pure decisions are pinned by tests, and the page by a RENDER test
#   H. the docs tell the operator the panel is usable (EN + RU)
#   I. the detector discriminates the pre-B472 shape
# ============================================================================
set -uo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b472.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

FORM=internal/feature/admin/ha_dns_form_b472.go
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

for f in "$FORM" "$HA" "$TPL" "$CAT" "$TESTS" "$RENDERTEST" "$DOC" "$RUDOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the certificate is optional ------------------------------------------------
# The textarea TAG must not carry `required`. Reading the tag (not the whole file) keeps a
# `required` on a sibling input from satisfying the assertion by accident.
tag=$(python3 - "$TPL" <<'PY'
import re, sys
src = open(sys.argv[1], encoding='utf-8').read()
m = re.search(r'(?s)<textarea[^>]*name="cert_pem"[^>]*>', src)
print(m.group(0) if m else '')
PY
)
case "$tag" in
  '') bad "A1: no cert_pem textarea found — mTLS providers would lose the field entirely" ;;
  *required*) bad "A1: cert_pem is still required: the browser refuses to submit the empty value reg.ru wants" ;;
  *) ok "A1: the certificate field is optional in the markup" ;;
esac
if has "$TPL" '<details' && has "$TPL" 'ha.dns_cert_optional'; then
  ok "A2: the certificate is folded into an optional block that names when it IS needed"
else
  bad "A2: the certificate field is still presented as a required part of the form"
fi
if has "$TPL" 'ha.dns_cert_hint'; then
  ok "A3: the hint says to leave it empty for reg.ru (the old text said the opposite)"
else
  bad "A3: no hint — the operator will fill it again and break the client"
fi
if has "$CAT" 'leave it empty for reg.ru' || has "$CAT" 'оставьте поле пустым'; then
  ok "A4: the help text no longer instructs the operator to paste a certificate"
else
  bad "A4: the stale «paste your SSL cert» instruction survives"
fi

# --- B: the other fields stay required --------------------------------------------
_b=0
for name in zone login password; do
  t=$(python3 - "$TPL" "$name" <<'PY'
import re, sys
src = open(sys.argv[1], encoding='utf-8').read()
m = re.search(r'(?s)<input[^>]*name="' + sys.argv[2] + r'"[^>]*>', src)
print(m.group(0) if m else '')
PY
)
  case "$t" in *required*) _b=$((_b+1)) ;; esac
done
if [ "$_b" -eq 3 ]; then
  ok "B1: zone/login/password are all still required (a save cannot store half a credential)"
else
  bad "B1: only $_b of 3 credential fields are required"
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
if has "$FORM" 'func (s *Service) PostAdminHADNSRecords(' && has "$FORM" 'client.RecordsFor('; then
  ok "D2: it reads the zone through the SAME client the CLI uses"
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
_f=0
for key in ha.dns_cert_optional ha.dns_cert_hint ha.dns_records ha.dns_records_names \
           ha.dns_records_ok ha.dns_records_no_a ha.dns_records_err ha.dns_configured_ok \
           ha.dns_test_error ha.dns_test_other ha.dns_store_missing; do
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
         TestB472_ParseDNSNames TestB472_RecordsSummary; do
  has "$TESTS" "func $t(" && _g=$((_g+1))
done
if [ "$_g" -ge 4 ]; then
  ok "G1: all four test outcomes, the one-line rule, the names field and the zone summary are pinned"
else
  bad "G1: only $_g of 4 behaviour tests exist"
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
if has "$RENDERTEST" 'textarea[^>]*name="cert_pem"'; then
  ok "G3: the render test isolates the textarea TAG before looking for required"
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
has "$TPL" '<details' || discriminates=0
has "$TPL" 'action="/admin/ha/dns/records"' || discriminates=0
has "$FORM" 'func dnsTestMessage(' || discriminates=0
has "$CAT" 'ha.dns_cert_hint' || discriminates=0
# The old shape must be REJECTED by the same predicates the contracts use.
case "$(cat "$SKY_TMP/old.html")" in
  *'<details'*|*'dns/records'*) discriminates=0 ;;
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
