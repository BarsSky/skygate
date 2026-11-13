#!/usr/bin/env bash
# ============================================================================
# check_b469_cert_renewal.sh — B469
#
# A 90-DAY CERTIFICATE WITH NO RENEWAL LOOP IS A SCHEDULED OUTAGE.
#
# B467 gave the panel the ability to OBTAIN a certificate (Let's Encrypt over HTTP-01) and B467's
# own release notes said "renewal is still manual in this version". That is a complete feature with
# a 90-day fuse: the operator gets one certificate, and two months later the site expires silently
# because nothing re-runs the order.
#
# This block closes the loop for the certificate that is ALREADY served, and it is deliberately
# narrow:
#   * the names come from the current certificate's SANs — a renewal can only continue what the
#     operator asked for, never invent a domain list;
#   * it runs inside the read-only choke point, so a restored copy cannot ask the CA for the
#     primary's names (B465);
#   * it reuses issueCertFor/publishCertPair — the same issuer and the same writer as the button,
#     because two writers eventually disagree about `.version` and the local cache;
#   * "not due", "no certificate" and "unreadable" are three named outcomes, so a corrupt file
#     cannot silently postpone renewal forever.
#
# Contracts:
#   A. the decision is a named status computed from NotAfter, with an inclusive window
#   B. missing / unreadable / disabled are distinct, and only "due" asks the CA
#   C. the loop is opt-in, configurable, and started from the read-only-guarded function
#   D. renewal uses the CURRENT certificate's SANs and the SAME issuer + publisher
#   E. each attempt is logged and audited (certs.renew / *_failed)
#   F. a read-only copy never renews (ApplyReadOnlyCopy disables it)
#   G. the decision is pinned by tests
#   H. the docs tell the operator how to turn it on
#   I. the detector discriminates the pre-B469 shape
# ============================================================================
set -uo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b469.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

DECIDE=internal/acmeissue/renew_b469.go
RENEW=cmd/skygate/cert_renew_b469.go
CFG=internal/config/config.go
SCHED=cmd/skygate/main_schedulers.go
DOC=docs/certificates.md
RUDOC=docs/ru/certificates.md
gosurface CMD cmd/skygate/*.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$DECIDE" "$RENEW" "$CFG" "$SCHED" "$DOC" "$RUDOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the decision ---------------------------------------------------------
if has "$DECIDE" 'func NeedsRenewal(certPEM []byte, now time.Time, before time.Duration) RenewalStatus'; then
  ok "A1: the renewal decision is one function over (certificate, now, window)"
else
  bad "A1: there is no single renewal decision"
fi
if has "$DECIDE" 'if !now.Add(before).Before(cert.NotAfter) {'; then
  ok "A2: the window is inclusive at the edge (a 90-day cert renews when 30 days remain, not a tick later)"
else
  bad "A2: the window boundary is off by one"
fi
if has "$DECIDE" 'type RenewalStatus struct' && has "$DECIDE" 'NotAfter time.Time'; then
  ok "A3: the decision carries the numbers it was made from"
else
  bad "A3: the decision is a bare bool"
fi

# --- B: three named outcomes -------------------------------------------------
if has "$DECIDE" 'Reason: "no-cert", Due: true' && has "$DECIDE" 'Reason: "unreadable"'; then
  ok "B1: 'no certificate' and 'unreadable' are separate, named outcomes"
else
  bad "B1: a missing and a corrupt certificate look the same"
fi
if has "$DECIDE" 'return RenewalStatus{Reason: "disabled"}'; then
  ok "B2: a zero window is 'disabled', not an endless silent no-op"
else
  bad "B2: a misconfigured window would silently never renew"
fi
if has "$RENEW" 'if !st.Due {'; then
  ok "B3: only a due certificate reaches the CA"
else
  bad "B3: the loop calls the CA regardless of the decision"
fi

# --- C: opt-in, configurable, guarded ---------------------------------------
for knob in SKYGATE_ACME_RENEW_ENABLED SKYGATE_ACME_RENEW_BEFORE SKYGATE_ACME_RENEW_INTERVAL SKYGATE_ACME_STAGING; do
  if has "$CFG" "$knob"; then
    ok "C1: $knob is configurable"
  else
    bad "C1: $knob is missing"
  fi
done
if has "$RENEW" 'if !cfg.ACMERenewEnabled {'; then
  ok "C2: the loop is opt-in (a background loop against a rate-limited CA must be chosen)"
else
  bad "C2: the loop starts without being asked for"
fi
if has "$SCHED" 'wireCertRenewal(ctx, cfg, d)'; then
  ok "C3: the loop is started from the scheduler function that the read-only gate guards"
else
  bad "C3: the loop is started outside the guarded function"
fi
if has "$RENEW" 'ACMERenewBefore <= 0 || cfg.ACMERenewInterval <= 0'; then
  ok "C4: a non-positive window or interval refuses to start instead of hammering the CA"
else
  bad "C4: a misconfigured window can spin the loop"
fi

# --- D: the same names, the same issuer, the same writer ---------------------
if has "$RENEW" 'issueCertFor(ctx, cfg, st.Domains, cfg.ACMEStaging)'; then
  ok "D1: renewal uses the SAME issuer as the button"
else
  bad "D1: renewal has its own issuance path"
fi
if has "$RENEW" 'publishCertPair(ctx, d, cfg, res.CertPEM, res.KeyPEM)'; then
  ok "D2: renewal uses the SAME publisher as the upload form"
else
  bad "D2: renewal writes the pair some other way"
fi
if has "$DECIDE" 'Domains:  append([]string(nil), cert.DNSNames...)'; then
  ok "D3: the names come from the certificate being served (a renewal cannot invent a domain list)"
else
  bad "D3: the renewed names do not come from the current certificate"
fi
if has "$RENEW" 'if len(st.Domains) == 0 {'; then
  ok "D4: with nothing to continue the loop says so instead of asking the CA for guesswork"
else
  bad "D4: the loop would issue for an empty name list"
fi

# --- E: logged and audited ---------------------------------------------------
# The action names are matched WITHOUT their quotes: the code uses double quotes and a contract
# that pins the quote style breaks on a formatting change (B466's A3b lesson, trap #15).
if has "$RENEW" 'certs.renew' && has "$RENEW" 'certs.renew_failed'; then
  ok "E1: both outcomes are audited"
else
  bad "E1: a renewal attempt leaves no audit trail"
fi
if has "$RENEW" 'certs.renew_publish_failed'; then
  ok "E2: 'the CA issued it but it could not be written' is its own audit row"
else
  bad "E2: a publish failure would be reported as an issuance failure"
fi
if has "$RENEW" 'no attempt — %s'; then
  ok "E3: a 'no attempt' tick still says why, so the loop is visibly alive"
else
  bad "E3: a quiet loop is indistinguishable from a dead one"
fi

# --- F: a copy never renews --------------------------------------------------
if has "$CFG" 'disable("acme-renew", c.ACMERenewEnabled'; then
  ok "F1: ApplyReadOnlyCopy disables the renewal loop"
else
  bad "F1: a restored copy would renew the primary's certificate"
fi

# --- G: tests ---------------------------------------------------------------
_g=0
for t in TestB469_NeedsRenewalDecidesFromNotAfter TestB469_MissingAndUnreadableAreDifferentFacts \
         TestB469_DisabledWindowIsNamed TestB469_WindowBoundaryIsInclusive; do
  has internal/acmeissue/renew_b469_test.go "func $t(" && _g=$((_g+1))
done
if [ "$_g" -ge 4 ]; then
  ok "G1: the decision is pinned by four tests (fresh/due/expired, missing vs unreadable, disabled, boundary)"
else
  bad "G1: only $_g of 4 decision tests exist"
fi

# --- H: documented ----------------------------------------------------------
if has "$DOC" 'SKYGATE_ACME_RENEW_ENABLED'; then
  ok "H1: the EN docs say how to turn renewal on"
else
  bad "H1: renewal is undocumented"
fi
if has "$RUDOC" 'SKYGATE_ACME_RENEW_ENABLED'; then
  ok "H2: the RU docs say the same thing"
else
  bad "H2: the RU docs do not mention renewal"
fi

# --- I: the detector discriminates the pre-B469 shape ----------------------
cat > "$SKY_TMP/old.go" <<'EOF'
// Renewal is still manual in this version (the page shows days left).
EOF
discriminates=1
for _m in 'func NeedsRenewal(' 'no-cert' 'certs.renew' 'wireCertRenewal(ctx, cfg, d)' 'acme-renew'; do
  case "$_m" in
    'func NeedsRenewal('|'no-cert') has "$DECIDE" "$_m" || discriminates=0 ;;
    'certs.renew') has "$RENEW" "$_m" || discriminates=0 ;;
    'wireCertRenewal(ctx, cfg, d)') has "$SCHED" "$_m" || discriminates=0 ;;
    'acme-renew') has "$CFG" "$_m" || discriminates=0 ;;
  esac
done
for _m in 'func NeedsRenewal(' 'no-cert' 'certs.renew' 'wireCertRenewal'; do
  has "$SKY_TMP/old.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "I1: every predicate is present now and absent from the 'manual only' shape"
else
  bad "I1: the predicates do not discriminate the pre-B469 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB469: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB469: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
