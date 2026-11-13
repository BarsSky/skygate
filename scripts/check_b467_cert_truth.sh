#!/usr/bin/env bash
# ============================================================================
# check_b467_cert_truth.sh — B467
#
# THE CERTIFICATE PAGE MUST OBTAIN AND WRITE A CERTIFICATE — AND SAY ONLY WHAT IT DID.
#
# Measured on the published v1.5.212 (2026-11-13), answering the operator's question «есть ли в
# самом skygate удобный процесс формирования сертификатов … по кнопке … через Let's Encrypt»:
#
#   1. There was NO ACME client at all. `go.mod` carried no acme dependency, `docs/ha.md`
#      Phase 4 said «LE acquisition DEFERRED (G4)», and `internal/dnsexternal` is an A-record
#      placeholder with no TXT support — so DNS-01 could not work either.
#   2. The one button that existed (/admin/certificates «Загрузить») wrote NOTHING and said it
#      had: `PostAdminCertificateUpload` validated the pair and called
#      `Service.uploadCertToS3`, which returned nil when `Service.CertUploadToS3 == nil` — and
#      nothing in cmd/skygate ever assigned that field. The handler contains no write at all
#      (its only os.* call reads the cert for display), and `/var/lib/skygate/certs/` is empty
#      on a fresh host. So the page's single action was a no-op reporting success.
#
# Contracts:
#   A. a real publisher exists and is wired; nothing reports an upload it did not perform
#   B. the flash is built from the RESULT (paths, S3, reload), not from a promise
#   C. publish keeps the previous pair, bumps .version LAST and updates the local cache
#   D. the local version cache round-trips (the SHA used to come back with a newline)
#   E. Let's Encrypt over HTTP-01 exists, with its precondition stated
#   F. the challenge route is narrow and the button is admin-only
#   G. the page offers the action (template + RU/EN keys)
#   H. tests pin the flow (including a CA that actually fetches the challenge)
#   I. the detector discriminates the pre-B467 shape
# ============================================================================
set -uo pipefail
# B339: read the cmd/skygate SURFACE, never cmd/skygate/main.go as one file — a contract that
# pins the entrypoint's path turns a pure code move red while the product is unchanged.
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b467.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

PUB=internal/certsync/publish_b467.go
CSYNC=internal/certsync/certsync.go
CERTGO=internal/feature/admin/certificates.go
ISSUEGO=internal/feature/admin/certificates_issue_b467.go
ACME=internal/acmeissue/acmeissue.go
WIRE=cmd/skygate/cert_publish_b467.go
TMPL=internal/handlers/templates/admin/certificates.html
CAT=internal/i18n/catalog_admin.go
gosurface CMD cmd/skygate/*.go
MAIN_CALLS="$SKY_TMP/main_calls.txt"
printf '%s\n' 'wireCertPublish(adminSvc, d, cfg)' 'registerCertChallengeRoute(mux)' 'wireCertIssue(adminSvc, cfg)' 'registerCertIssueRoute(mux, adminSvc)' > "$MAIN_CALLS"

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$PUB" "$CSYNC" "$CERTGO" "$ISSUEGO" "$ACME" "$WIRE" "$TMPL" "$CAT"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: a real publisher, actually wired --------------------------------------
if has "$PUB" 'func Publish(ctx context.Context, deps PublishDeps' && has "$PUB" 'func writeLocalCertPair'; then
  ok "A1: one function puts a pair where certsync reads it (local files, atomic rename)"
else
  bad "A1: there is no publisher that writes the pair"
fi
if has "$WIRE" 'adminSvc.CertUploadToS3 = func' && has "$CMD" 'wireCertPublish(adminSvc, d, cfg)'; then
  ok "A2: main.go installs the publisher (pre-B467 the field stayed nil on every install)"
else
  bad "A2: the publisher is not wired — the upload button would still write nothing"
fi
# A3 — the specific pre-B467 lie: a nil callback returning success.
if ! has "$CERTGO" 'if s.CertUploadToS3 == nil {' || has "$CERTGO" 'cert.publish_no_writer'; then
  ok "A3: an unwired publisher is an ERROR, not a silent success"
else
  bad "A3: a missing publisher can still report success"
fi
if has "$CERTGO" 'func (s *Service) uploadCertToS3'; then
  bad "A4: the stub uploadCertToS3 is still there"
else
  ok "A4: the stub that described a flow no code performed is gone"
fi

# --- B: the flash is the result, not a promise --------------------------------
if has "$CERTGO" 'res.CertPath' && has "$CERTGO" 'res.S3' && has "$CERTGO" 'res.Reloaded'; then
  ok "B1: the flash is built from the publish result (path, S3, reload)"
else
  bad "B1: the flash still promises instead of reporting"
fi
if has "$CERTGO" 'cert.publish_ok' && has "$CAT" '"cert.publish_ok"'; then
  ok "B2: the sentence is an i18n key, present in the catalogue"
else
  bad "B2: the flash text is not localised"
fi
if has "$CERTGO" 'certsync scheduler will pull within'; then
  bad "B3: the old 'the scheduler will pull within 30s' promise is still in the handler"
else
  ok "B3: the false promise is gone"
fi

# --- C: what publish keeps consistent ----------------------------------------
if has "$PUB" 'backupPair(dir)' && has "$PUB" 'cert + ".prev"'; then
  ok "C1: the previous pair is kept as .prev (the one step back)"
else
  bad "C1: a publish leaves no way back"
fi
if has "$PUB" 'cert, key, THEN .version'; then
  ok "C2: .version is written LAST, so it never advertises objects that are not there"
else
  bad "C2: .version ordering is not stated or not implemented"
fi
if has "$PUB" 'saveLocalVersionCache(dir, version, res.SHA256)'; then
  ok "C3: the local version cache follows the publish (otherwise the next tick reverts it)"
else
  bad "C3: certsync would re-pull and revert the published pair"
fi
if has "$PUB" 'the LOCAL pair is written and live at %s'; then
  ok "C4: an S3 failure still says the local pair is live"
else
  bad "C4: an S3 failure would read as 'nothing happened'"
fi
if has "$PUB" 'ReloadNote' && has "$PUB" 'the certificate is on disk'; then
  ok "C5: a reload failure is a note, not a failed publish"
else
  bad "C5: a proxy that is not running would fail the publish"
fi

# --- D: the version cache round-trips ---------------------------------------
if has "$CSYNC" 'return v, strings.TrimSpace(lines[1]), nil'; then
  ok "D1: the version cache SHA is trimmed on read (it used to come back with a newline)"
else
  bad "D1: the reader returns the stored line with its newline"
fi
if has internal/certsync/publish_b467_test.go 'TestB467_VersionCacheRoundTrips'; then
  ok "D2: the round trip is pinned by a test"
else
  bad "D2: nothing pins the version cache round trip"
fi

# --- E: Let's Encrypt over HTTP-01 ------------------------------------------
if has "$ACME" 'golang.org/x/crypto/acme' && has "$ACME" 'func (i *Issuer) Issue(ctx context.Context, domains []string)'; then
  ok "E1: an ACME client exists and performs a real issuance"
else
  bad "E1: there is still no ACME client"
fi
if has "$ACME" 'HTTP01ChallengeResponse' && has "$ACME" 'WaitAuthorization' && has "$ACME" 'AuthorizeOrder'; then
  ok "E2: the order is an actual HTTP-01 flow (authorize, answer, wait, finalize)"
else
  bad "E2: the HTTP-01 steps are missing"
fi
if has "$ACME" 'CreateOrderCert(ctx, order.FinalizeURL'; then
  ok "E3: the CSR goes to the FINALIZE url (order.URI is a 404 on every CA)"
else
  bad "E3: the CSR is posted to the wrong resource"
fi
if has "$ACME" 'LetsEncryptStaging' && has "$ACME" 'func ResolveDirectory'; then
  ok "E4: staging is available for a first drill without touching production limits"
else
  bad "E4: there is no safe drill contour"
fi
if has "$ACME" 'PRECONDITION' || has "$WIRE" 'PRECONDITION'; then
  ok "E5: the reachability precondition is written down where an implementer looks"
else
  bad "E5: the precondition (the CA must reach this process) is nowhere stated"
fi
if has "$ACME" 'a wildcard needs DNS-01'; then
  ok "E6: a wildcard is refused with the reason (no TXT support in this installation)"
else
  bad "E6: a wildcard would fail at the CA instead of at the form"
fi
if has "$ACME" 'LoadOrCreateAccountKey' && has "$ACME" '0o600'; then
  ok "E7: the account key is created once, 0600, outside /tmp"
else
  bad "E7: the account key handling is not pinned"
fi

# --- F: the route and the guard ---------------------------------------------
if has "$WIRE" 'func registerCertChallengeRoute' && has "$CMD" 'registerCertChallengeRoute(mux)'; then
  ok "F1: the public challenge route is registered"
else
  bad "F1: the CA has nowhere to fetch the challenge from"
fi
if has "$ACME" 'strings.Contains(token, "/")'; then
  ok "F2: the challenge handler answers one segment only (it cannot become a catch-all)"
else
  bad "F2: the challenge handler is broader than the path"
fi
if has "$ISSUEGO" 'func (s *Service) PostAdminCertificateIssue' && has "$ISSUEGO" 'c == nil || !c.IsAdmin'; then
  ok "F3: the issuance endpoint is admin-only"
else
  bad "F3: the issuance endpoint has no admin guard"
fi
if has "$WIRE" 'mux.HandleFunc("POST /admin/certificates/issue"'; then
  ok "F4: the button has a route"
else
  bad "F4: the button posts to nothing"
fi
if has "$ISSUEGO" 'acmeissue.ValidateDomains(r.FormValue("domains"))'; then
  ok "F5: names are validated before a rate-limited CA call"
else
  bad "F5: a typo would reach the CA"
fi
if has "$ISSUEGO" 's.CertUploadToS3 == nil' && has "$ISSUEGO" 'cert.issue_no_publisher'; then
  ok "F6: an obtained-but-unpublishable certificate is reported as the defect it is"
else
  bad "F6: a good certificate could be obtained and thrown away silently"
fi

# --- G: the page offers it ---------------------------------------------------
for k in cert.issue_title cert.issue_domains cert.issue_staging cert.issue_submit; do
  if has "$TMPL" "\"$k\"" && has "$CAT" "\"$k\""; then
    ok "G: the page renders $k and the catalogue defines it"
  else
    bad "G: $k is missing from the template or the catalogue"
  fi
done
_ru=$(grep -c '"cert.issue_ok"' "$CAT" || true)
if [ "${_ru:-0}" -ge 2 ]; then
  ok "G5: the result sentence is defined in BOTH languages"
else
  bad "G5: cert.issue_ok is not in both catalogues"
fi

# --- H: tests -----------------------------------------------------------------
_h=0
for t in TestB467_PublishWritesTheLocalPair TestB467_PublishKeepsThePreviousPair \
         TestB467_PublishBumpsVersionAndWritesItLast TestB467_PublishedCertSurvivesTheNextCertsyncTick \
         TestB467_PublishRejectsABadPair TestB467_S3FailureStillTellsTheOperatorTheLocalCopyIsLive; do
  has internal/certsync/publish_b467_test.go "func $t(" && _h=$((_h+1))
done
if [ "$_h" -ge 6 ]; then
  ok "H1: the publish path is pinned by tests (local write, .prev, version order, revert, rejection)"
else
  bad "H1: only $_h of 6 publish tests exist"
fi
_a=0
for t in TestB467_ValidateDomainsAcceptsAndNormalises TestB467_ChallengeHandlerServesOnlyKnownTokens \
         TestB467_IssueOverHTTP01EndToEnd TestB467_IssueFailsWhenTheCANeverValidates \
         TestB467_AccountKeyIsCreatedOnceAndReused TestB467_CSRCarriesEveryDomain; do
  has internal/acmeissue/acmeissue_b467_test.go "func $t(" && _a=$((_a+1))
done
if [ "$_a" -ge 6 ]; then
  ok "H2: the ACME flow is pinned end to end against a CA that actually fetches the challenge"
else
  bad "H2: only $_a of 6 ACME tests exist"
fi

# --- I: the detector discriminates the pre-B467 shape ------------------------
cat > "$SKY_TMP/old_certificates.go" <<'EOF'
// Upload to S3. The S3 client is wired in main.go (same as B147).
func (s *Service) uploadCertToS3(ctx context.Context, cert, key []byte, newSHA string) error {
	if s.CertUploadToS3 == nil {
		return nil // silent — surface still works
	}
	return s.CertUploadToS3(ctx, cert, key, newSHA)
}
EOF
cat > "$SKY_TMP/old_handler.go" <<'EOF'
	certRedirect(w, r, fmt.Sprintf("Cert uploaded. SHA-256=%s. The certsync scheduler will pull within %s.", newSHA, s.certsyncInterval()), "")
EOF
discriminates=1
for _marker in 'func Publish(' '.prev' 'IntegrityTimeout' 'acme' 'unmeasured'; do
  case "$_marker" in
    IntegrityTimeout|unmeasured) : ;;  # markers from other blocks must NOT appear here
    *) has "$PUB" "$_marker" 2>/dev/null || has "$ACME" "$_marker" 2>/dev/null || discriminates=0 ;;
  esac
done
for _marker in 'func Publish(' 'HTTP01ChallengeResponse' '.prev'; do
  has "$SKY_TMP/old_certificates.go" "$_marker" && discriminates=0
  has "$SKY_TMP/old_handler.go" "$_marker" && discriminates=0
done
if has "$SKY_TMP/old_certificates.go" 'return nil // silent'; then
  ok "I1: the planted pre-B467 stub is what the detector looks for"
else
  bad "I1: the detector's fixture does not describe the old shape"
fi
if [ "$discriminates" -eq 1 ]; then
  ok "I2: the predicates are absent from the pre-B467 shape"
else
  bad "I2: the predicates do not discriminate the pre-B467 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB467: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB467: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
