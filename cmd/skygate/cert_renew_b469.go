// cert_renew_b469.go — start the certificate renewal loop, and the one attempt it performs.
//
// Started from wireOptionalSchedulers (which returns early on a READ-ONLY COPY, B465), so a restored
// copy cannot renew — it would issue against the primary's domain names from a host whose data is a
// snapshot.
//
// The loop is daily (SKYGATE_ACME_RENEW_INTERVAL): cheap, because the decision is a file read plus a
// date comparison. The window is SKYGATE_ACME_RENEW_BEFORE (default 30 days), and it must be smaller
// than the certificate's lifetime or the loop would renew on every tick — the config validation below
// refuses that instead of hammering the CA.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"skygate/internal/acmeissue"
	"skygate/internal/certsync"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/localtime"
)

// wireCertRenewal starts the renewal loop when the operator asked for it.
func wireCertRenewal(ctx context.Context, cfg *config.Config, d *db.ResettableDB) {
	if cfg == nil {
		return
	}
	if !cfg.ACMERenewEnabled {
		log.Printf("🔐 acme-renew: disabled (SKYGATE_ACME_RENEW_ENABLED=false) — the certificate must be renewed by hand on /admin/certificates")
		return
	}
	if cfg.ACMERenewBefore <= 0 || cfg.ACMERenewInterval <= 0 {
		log.Printf("🔐 acme-renew: NOT started — SKYGATE_ACME_RENEW_BEFORE=%s / SKYGATE_ACME_RENEW_INTERVAL=%s must both be positive", cfg.ACMERenewBefore, cfg.ACMERenewInterval)
		return
	}
	log.Printf("🔐 acme-renew: enabled (window=%s, check every %s, directory=%s, cert=%s)",
		cfg.ACMERenewBefore, cfg.ACMERenewInterval,
		acmeissue.ResolveDirectory(cfg.ACMEDirectory, cfg.ACMEStaging), filepath.Join(cfg.CertSyncLocalDir, certsync.LocalCert))
	go func() {
		// One check at boot, then on the interval: a host that spent a month offline renews as soon
		// as it comes back rather than waiting for the next slot.
		renewalTick(ctx, cfg, d)
		t := time.NewTicker(cfg.ACMERenewInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				renewalTick(ctx, cfg, d)
			}
		}
	}()
}

// renewalTick performs at most ONE renewal attempt and writes down what it decided.
func renewalTick(ctx context.Context, cfg *config.Config, d *db.ResettableDB) {
	dir := cfg.CertSyncLocalDir
	if dir == "" {
		dir = certsync.DefaultLocalDir
	}
	certPath := filepath.Join(dir, certsync.LocalCert)
	raw, err := os.ReadFile(certPath)
	if err != nil && !os.IsNotExist(err) {
		// A read error is not "no certificate": say which it is.
		log.Printf("🔐 acme-renew: cannot read %s: %v (no attempt made)", certPath, err)
		return
	}
	st := acmeissue.NeedsRenewal(raw, time.Now().UTC(), cfg.ACMERenewBefore)
	if !st.Due {
		if st.Reason == "fresh" {
			// One line per tick, at most every SKYGATE_ACME_RENEW_INTERVAL: an operator reading the
			// journal can see the loop is alive and how much time is left.
			log.Printf("🔐 acme-renew: no attempt — %s", st.Describe())
			return
		}
		log.Printf("🔐 acme-renew: no attempt — %s", st.Describe())
		return
	}
	if len(st.Domains) == 0 {
		// No certificate (or no SANs) means there is nothing to continue: a renewal must never
		// invent a domain list.
		log.Printf("🔐 acme-renew: nothing to renew — %s. Press «Получить сертификат у Let's Encrypt» on /admin/certificates once, and this loop will continue it.", st.Describe())
		return
	}

	log.Printf("🔐 acme-renew: renewing %v (%s) via %s", st.Domains, st.Describe(),
		acmeissue.ResolveDirectory(cfg.ACMEDirectory, cfg.ACMEStaging))
	res, ierr := issueCertFor(ctx, cfg, st.Domains, cfg.ACMEStaging)
	if ierr != nil {
		auditCertRenew(d, "certs.renew_failed", fmt.Sprintf("domains=%v reason=%s error=%v", st.Domains, st.Reason, ierr))
		log.Printf("🔐 acme-renew: FAILED — %v. The existing certificate stays in place; the next check is in %s.", ierr, cfg.ACMERenewInterval)
		return
	}
	pub, perr := publishCertPair(ctx, d, cfg, res.CertPEM, res.KeyPEM)
	if perr != nil {
		auditCertRenew(d, "certs.renew_publish_failed", fmt.Sprintf("domains=%v error=%v", st.Domains, perr))
		log.Printf("🔐 acme-renew: the CA issued a certificate but publishing failed — %v", perr)
		return
	}
	sum := sha256.Sum256(res.CertPEM)
	auditCertRenew(d, "certs.renew", fmt.Sprintf(
		"domains=%v from_days_left=%d sha256=%s path=%s s3=%t version=%d reloaded=%t elapsed=%s",
		st.Domains, st.DaysLeft, hex.EncodeToString(sum[:]), pub.CertPath, pub.S3, pub.Version, pub.Reloaded, res.Elapsed))
	log.Printf("🔐 acme-renew: renewed %v — written to %s (s3=%t version=%d, reloaded=%t, took %s)",
		st.Domains, pub.CertPath, pub.S3, pub.Version, pub.Reloaded, res.Elapsed)
}

// auditCertRenew records the attempt. A renewal is a network action against a rate-limited third
// party; the journal must answer "who tried what, when" without reading the panel.
func auditCertRenew(d *db.ResettableDB, action, detail string) {
	if d == nil || d.DB == nil {
		return
	}
	_, err := d.DB.Exec(
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'acme-renew', $1, $2, `+db.NowUnixSQL()+`)`,
		action, strings.TrimSpace(detail+" at="+localtime.Format(time.Now())))
	if err != nil {
		log.Printf("🔐 acme-renew: audit row %s failed: %v", action, err)
	}
}
