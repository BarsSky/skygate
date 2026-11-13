// cert_publish_b467.go — B467: give /admin/certificates something to actually write with.
//
// Measured on the published v1.5.212 (2026-11-13): the upload form validated the pair, printed
// «Cert uploaded. SHA-256=… The certsync scheduler will pull within 30s» and wrote NOTHING.
// `Service.CertUploadToS3` is the callback the handler calls, it was nil on every install —
// nothing in cmd/skygate ever assigned it — and the handler has no write of its own (its only
// os.* call reads the cert for display). So the page's one action could not work, and the
// message said it had.
//
// This file is the assignment: one function that publishes a pair through certsync.Publish
// (local files + `.prev` + optional S3 with a bumped `.version` + the local version cache +
// the proxy reload) and reports every destination back to the page.
//
// It is a separate file for the B372 reason: main.go only ever shrinks, and wiring needs room
// for the explanation of what each failure means.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"skygate/internal/acmeissue"
	"skygate/internal/backup"
	"skygate/internal/certsync"
	"skygate/internal/config"
	"skygate/internal/db"
	adminsvc "skygate/internal/feature/admin"
)

// certChallengeTokens is the process-wide HTTP-01 store. It has to be reachable from two places
// that do not otherwise talk to each other: the public challenge route (which the CA fetches)
// and the admin "obtain a certificate" handler (which fills it during an order). One store, one
// lifetime, no disk.
var certChallengeTokens = acmeissue.NewTokens()

// registerCertChallengeRoute mounts the ACME HTTP-01 path. It is UNAUTHENTICATED by necessity —
// the CA has no session — and it is deliberately narrow: only
// /.well-known/acme-challenge/<one segment> is answered, so it cannot become a catch-all.
//
// PRECONDITION, stated where an implementer will look: this route makes skygate ABLE to answer a
// challenge. Whether the CA can reach it is a routing fact about the deployment (port 80
// forwarded, or the proxy sending this path here), and that is why issuance is a button with one
// attempt instead of a background retry loop.
func registerCertChallengeRoute(mux *http.ServeMux) {
	mux.Handle(acmeissue.ChallengePathPrefix, acmeissue.Handler(certChallengeTokens))
}

// registerCertIssueRoute mounts the button's POST. It is admin-only inside the handler (the
// same `CurrentUser` guard the other certificate endpoints use), so no middleware is needed
// here — and keeping it in this file keeps main.go's route table unchanged (B372).
func registerCertIssueRoute(mux *http.ServeMux, adminSvc *adminsvc.Service) {
	if mux == nil || adminSvc == nil {
		return
	}
	mux.HandleFunc("POST /admin/certificates/issue", adminSvc.PostAdminCertificateIssue)
}

// wireCertIssue installs the issuance callback: validate (already done by the handler), load
// the account key, run one HTTP-01 order against the selected CA.
func wireCertIssue(adminSvc *adminsvc.Service, cfg *config.Config) {
	if adminSvc == nil || cfg == nil {
		return
	}
	adminSvc.CertIssue = func(ctx context.Context, domains []string, staging bool) (acmeissue.Result, error) {
		return issueCertFor(ctx, cfg, domains, staging)
	}
	log.Printf("🔐 acme: issuance wired (account key %s, directory %s, contact %q)",
		cfg.ACMEAccountKeyPath, acmeissue.ResolveDirectory(cfg.ACMEDirectory, false), cfg.ACMEEmail)
}

// issueCertFor runs ONE HTTP-01 order. Extracted in B469 so the button and the renewal scheduler
// cannot diverge: there is exactly one way this installation asks the CA for a certificate.
func issueCertFor(ctx context.Context, cfg *config.Config, domains []string, staging bool) (acmeissue.Result, error) {
	key, err := acmeissue.LoadOrCreateAccountKey(cfg.ACMEAccountKeyPath)
	if err != nil {
		return acmeissue.Result{}, err
	}
	issuer := &acmeissue.Issuer{
		DirectoryURL: acmeissue.ResolveDirectory(cfg.ACMEDirectory, staging),
		Email:        strings.TrimSpace(cfg.ACMEEmail),
		Key:          key,
		Tokens:       certChallengeTokens,
	}
	return issuer.Issue(ctx, domains)
}

// wireCertPublish installs the publish callback on the admin service.
//
// The S3 half is built lazily, on the first publish, for two reasons: a native install has no
// S3 at all (and must still be able to install a certificate), and a broken S3 configuration
// must not be able to prevent the panel from booting — it is reported in the publish result
// instead.
func wireCertPublish(adminSvc *adminsvc.Service, d *db.ResettableDB, cfg *config.Config) {
	if adminSvc == nil || cfg == nil {
		return
	}
	adminSvc.CertUploadToS3 = func(ctx context.Context, certPEM, keyPEM []byte, sha string) (certsync.PublishResult, error) {
		return publishCertPair(ctx, d, cfg, certPEM, keyPEM)
	}
}

// publishCertPair writes a validated pair where certsync reads it (B467) — extracted in B469 for the
// same reason as issueCertFor: the upload form, the issue button and the renewal loop must share
// one writer, or three code paths will eventually disagree about `.version` and the local cache.
func publishCertPair(ctx context.Context, d *db.ResettableDB, cfg *config.Config, certPEM, keyPEM []byte) (certsync.PublishResult, error) {
	localDir := cfg.CertSyncLocalDir
	deps := certsync.PublishDeps{
		LocalDir:    localDir,
		CaddyReload: reloadProxyForCerts(ctx),
	}

	// S3 is optional AND best-effort at the configuration level: a missing or broken config is a
	// note in the result, never a reason to skip the local write.
	var note string
	if cfg.CertSyncBucket == "" {
		note = "no certsync bucket configured"
	} else if d == nil || d.DB == nil {
		note = "no database handle to read the S3 credentials from"
	} else if bcfg, err := certSyncS3Config(d.DB, cfg); err != nil {
		note = "S3 config: " + err.Error()
	} else if s3, err := backup.NewS3ClientForConfig(bcfg); err != nil {
		note = "S3 client: " + err.Error()
	} else if adapter, err := certsync.NewMinioS3Client(s3); err != nil {
		note = "S3 adapter: " + err.Error()
	} else {
		deps.S3Client = adapter
		deps.S3Bucket = cfg.CertSyncBucket
	}
	if note != "" {
		// Say it once, in the journal, in the operator's words — the publish result carries the
		// same sentence to the page.
		log.Printf("🔐 cert-publish: S3 not used (%s) — the certificate is written locally to %s", note, localDir)
	}

	res, err := certsync.Publish(ctx, deps, certPEM, keyPEM)
	if note != "" && !res.S3 && res.S3Note != "" {
		res.S3Note = note
	}
	return res, err
}

// reloadProxyForCerts returns the reload callback: it tells whatever terminates TLS to re-read
// the pair. Nothing here is required — a host whose proxy is not managed by skygate simply
// gets "no reload" in the result and the operator reloads it (or waits for its own watcher).
//
// The probe order is deliberate and cheap: a `caddy` binary on PATH or a running unit is asked
// to reload; anything else is reported as not configured rather than guessed at.
func reloadProxyForCerts(ctx context.Context) func(context.Context) error {
	return func(ctx context.Context) error {
		if _, err := exec.LookPath("caddy"); err == nil {
			out, err := exec.CommandContext(ctx, "caddy", "reload", "--config", "/etc/caddy/Caddyfile").CombinedOutput()
			if err != nil {
				return &reloadError{what: "caddy reload", detail: strings.TrimSpace(string(out)), err: err}
			}
			return nil
		}
		if _, err := os.Stat("/etc/caddy/Caddyfile"); err == nil {
			out, err := exec.CommandContext(ctx, "systemctl", "reload", "caddy").CombinedOutput()
			if err != nil {
				return &reloadError{what: "systemctl reload caddy", detail: strings.TrimSpace(string(out)), err: err}
			}
			return nil
		}
		// No managed proxy: the certificate is on disk and that is the honest outcome.
		return nil
	}
}

// reloadError keeps the command's own output in the message — "reload failed" without it makes
// the operator guess whether the Caddyfile path or the unit is wrong.
type reloadError struct {
	what   string
	detail string
	err    error
}

func (e *reloadError) Error() string {
	if e.detail == "" {
		return e.what + ": " + e.err.Error()
	}
	return e.what + ": " + e.err.Error() + ": " + e.detail
}

func (e *reloadError) Unwrap() error { return e.err }
