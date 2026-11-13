// certificates_issue_b467.go — the "obtain a certificate from Let's Encrypt" action on
// /admin/certificates.
//
// WHY A MANUAL BUTTON, NOT A SCHEDULER. HTTP-01 issuance depends on a fact skygate cannot
// verify on its own: that `http://<domain>/.well-known/acme-challenge/<token>` reaches THIS
// process from the internet (port 80 forwarded, or the operator's proxy routing that path
// here). A background loop that retries into an unknown routing state burns the CA's
// rate limits and teaches the operator nothing. The button performs ONE attempt, reports the
// step it failed on, and leaves the decision to the operator — who can then wire the route and
// press it again.
//
// The button also refuses to pretend: it validates the names before any network call, it says
// which CA it will talk to (staging vs production), and it publishes the result through the
// same `certsync.Publish` the upload path uses, so the flash is built from what happened.
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"skygate/internal/acmeissue"
	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
)

// CertIssueFn obtains a certificate+key pair for the given domains. main.go wires it (the
// feature package must not know about the CA account key path, the email or the directory
// override — those are configuration).
//
// `staging` asks for the staging CA: a certificate signed by an untrusted root, which is
// exactly what a first drill needs (the flow can be proved without touching the production
// rate limits).
type CertIssueFn func(ctx context.Context, domains []string, staging bool) (acmeissue.Result, error)

// PostAdminCertificateIssue handles POST /admin/certificates/issue.
//
// Order of operations is deliberate:
//  1. validate the names (no CA call for a typo),
//  2. ask the CA (HTTP-01; the tokens are served by the route main.go registers),
//  3. publish the pair through certsync.Publish — the same files, the same `.version`, the
//     same `.prev` backup and the same proxy reload the upload path uses,
//  4. audit the outcome either way.
func (s *Service) PostAdminCertificateIssue(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	if s.CertIssue == nil {
		certRedirect(w, r, "", i18n.T(lang, "cert.issue_not_wired"))
		return
	}
	if err := r.ParseForm(); err != nil {
		certRedirect(w, r, "", i18n.T(lang, "cert.issue_form")+": "+err.Error())
		return
	}
	domains, err := acmeissue.ValidateDomains(r.FormValue("domains"))
	if err != nil {
		certRedirect(w, r, "", i18n.T(lang, "cert.issue_bad_domain")+": "+err.Error())
		return
	}
	staging := r.FormValue("staging") == "1"

	res, err := s.CertIssue(r.Context(), domains, staging)
	if err != nil {
		// The failure names the step (account / order / challenge / finalize) and the token
		// path the CA could not reach — that sentence is the whole value of this button.
		s.auditCertIssue(r, c, domains, staging, "", "certs.issue_failed", err.Error())
		certRedirect(w, r, "", i18n.T(lang, "cert.issue_failed")+": "+err.Error())
		return
	}

	// Publish through the same path the upload form uses. A nil publisher would mean the pair
	// was obtained and thrown away, so it is reported as the defect it is.
	if s.CertUploadToS3 == nil {
		s.auditCertIssue(r, c, domains, staging, res.AccountURL, "certs.issue_unpublished", "no publisher wired")
		certRedirect(w, r, "", i18n.T(lang, "cert.issue_no_publisher"))
		return
	}
	sum := sha256.Sum256(res.CertPEM)
	sha := hex.EncodeToString(sum[:])
	pub, perr := s.CertUploadToS3(r.Context(), res.CertPEM, res.KeyPEM, sha)
	if perr != nil {
		s.auditCertIssue(r, c, domains, staging, res.AccountURL, "certs.issue_publish_failed", perr.Error())
		certRedirect(w, r, "", i18n.T(lang, "cert.issue_publish_failed")+": "+perr.Error())
		return
	}

	s.auditCertIssue(r, c, domains, staging, res.AccountURL, "certs.issue", fmt.Sprintf(
		"domains=%v staging=%t sha256=%s path=%s s3=%t version=%d reloaded=%t elapsed=%s",
		domains, staging, sha, pub.CertPath, pub.S3, pub.Version, pub.Reloaded, res.Elapsed))

	s3 := i18n.T(lang, "cert.publish_s3_local_only") + ": " + pub.S3Note
	if pub.S3 {
		s3 = fmt.Sprintf("%s (version %d)", i18n.T(lang, "cert.publish_s3_ok"), pub.Version)
	}
	reload := i18n.T(lang, "cert.publish_reload_ok")
	if !pub.Reloaded {
		reload = i18n.T(lang, "cert.publish_reload_skipped") + ": " + pub.ReloadNote
	}
	ca := i18n.T(lang, "cert.issue_ca_production")
	if staging {
		ca = i18n.T(lang, "cert.issue_ca_staging")
	}
	certRedirect(w, r, fmt.Sprintf(i18n.T(lang, "cert.issue_ok"),
		ca, fmt.Sprintf("%v", domains), pub.CertPath, acmeissue.Fingerprint(res.CertPEM), s3, reload), "")
}

// auditCertIssue writes one audit row per attempt, success or failure. An issuance is a
// network action against a rate-limited third party; the journal must answer "who tried what,
// when, and with which account" without reading the panel.
func (s *Service) auditCertIssue(r *http.Request, c *auth.Claims, domains []string, staging bool, account, action, detail string) {
	if s.dbc() == nil || c == nil {
		return
	}
	_, _ = s.dbc().ExecContext(r.Context(),
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES ($1, $2, $3, $4, `+db.NowUnixSQL()+`)`,
		c.UserID, c.Username, action,
		fmt.Sprintf("domains=%v staging=%t account=%s %s", domains, staging, account, detail))
}
