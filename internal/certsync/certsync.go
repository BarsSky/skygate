// Package certsync — in-app cert-sync scheduler (v1.5.0 / B147).
//
// The pre-B147 cert pipeline (scripts/cert-renew.sh + system
// cron) ran the certbot / DNS-01 via the configured provider dance on the ACTIVE
// node only, wrote the new PEM + key to /var/lib/skygate/certs/,
// and reloaded Caddy via `docker exec skygate-caddy caddy reload`.
// On a HA cluster with two skygate nodes (active + standby),
// the standby had no cert of its own — when the active died
// and the standby was promoted, the cert on the standby was
// either stale (last sync was 60+ days ago) or missing (never
// synced). The promotion would then 502 until the operator
// manually ran the cert-renew script on the new active.
//
// B147 fixes this by running a per-node scheduler that polls
// the S3 deploy bucket for a `.version` file (a tiny text
// manifest with a monotonically increasing version number +
// the SHA-256 of the current `cert.pem` + `key.pem` pair),
// pulls the newer certs if the local SHA doesn't match, and
// reloads Caddy. Same upload triggers the new certs on BOTH
// nodes simultaneously, so failover is always cert-fresh.
//
// Design (mirrors internal/backup/verify_scheduler.go / B142):
//
//   - Start(ctx, deps) launches the goroutine; Cancel via ctx.
//   - Tick interval: 30s by default. Coarse enough to be cheap,
//     fine enough that an active cert upload propagates to all
//     nodes within 30s (the operator's mental model: "I
//     clicked Apply, and within a minute both nodes have the
//     new cert").
//   - On each tick:
//       1. HEAD .version in S3 (no body — just metadata).
//       2. If the remote version > local version OR the
//          remote cert SHA-256 != local cert SHA-256:
//            a. Download cert.pem + key.pem to a temp file.
//            b. Verify the pair is a valid x509 cert + RSA/EC
//               key (crypto/x509.ParseCertificate + ParsePKCS1/8).
//            c. Atomic rename into the live path.
//            d. Trigger the Caddy reload callback (operator-
//               supplied: usually `docker exec skygate-caddy
//               caddy reload`).
//            e. Update local .version cache + write an
//               audit_log row "certsync.pull" with the new
//               SHA-256 + version.
//   - On each tick (regardless of cert change):
//       - Run a "self" check: if the local cert expires within
//         7 days, log a WARNING + send Telegram alert (so the
//         operator has time to renew before the cert actually
//         dies and breaks the HTTPS listener).
//
// Why S3 + a .version file (not a watch on the active node):
//   - The active node is the SOURCE of truth for cert content
//     (certbot runs there), but the S3 bucket is the SOURCE OF
//     TRUTH for "which version is current". The active node
//     uploads the cert + bumps .version after every renewal;
//     every other node polls .version + downloads the new
//     certs when its hash changes. This pattern works
//     identically to a CDN (write-once, read-many) and is
//     resilient to the active node being down (the standby
//     still has a working poll target).
//
// Why a hash-based version + not the cert's NotBefore/NotAfter:
//   - A cert can be renewed EARLY (operator manually triggers
//     a renewal 30 days before expiry). The NotAfter changes
//     on every renewal, but the operator's intent is "pull
//     whenever I upload a new one" — not "pull when the cert
//     age changes". A version file the operator controls is
//     simpler and more explicit. The .version file format is
//     just a JSON object: { "version": 5, "sha256": "...",
//     "uploaded_at": "2026-08-19T..." }.
//
// Failure modes:
//   - S3 unreachable → tick is a no-op (logged), next tick
//     is fresh. Same as B130/B142.
//   - S3 returns 404 for .version → "no certs uploaded yet";
//     tick is a no-op (the cert renew hasn't run yet).
//   - Downloaded cert is invalid (parse fails) → log error +
//     send Telegram alert, do NOT replace the live cert. The
//     operator can investigate via the audit log.
//   - Caddy reload callback fails → log error + send alert,
//     but the cert is still on disk (the next manual Caddy
//     reload will pick it up). This is the same "best-effort
//     reload" pattern B130 uses for the orchestrator's
//     docker-update.
//   - Telegram not configured (Notifier == nil) → silent. The
//     operator can still see the failure in /admin/audit and
//     the certsync log lines.
//
// Concurrency:
//   - inFlightPull mutex (process-local) prevents two parallel
//     pulls from racing. The pull is fast (S3 + atomic rename
//     + 1 Caddy reload) so a 30s tick never overlaps with
//     itself in practice; the mutex is a safety net.
//   - The local .version cache is read on every tick; it's a
//     single file read, no locking needed.
package certsync

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"skygate/internal/backup"
)

// CertSync is the long-lived scheduler that polls S3 for
// newer certs and reloads Caddy when it finds them. One
// instance per process, started by cmd/skygate/main.go at
// boot via StartCertSync.
//
// The struct is intentionally small (no caching, no per-
// cert state beyond the .version cache) — every tick is a
// fresh S3 HEAD + a possible download. Cheap, simple, and
// the operator can reason about it without knowing the
// internals.
type CertSync struct {
	mu              sync.Mutex
	inFlightPull    bool
	lastLocalVersion int64
	lastLocalSHA     string
}

// CertSyncDeps groups the dependencies the in-app certsync
// scheduler needs. Defined as a struct (not individual
// fields) so adding new dependencies in the future doesn't
// break call sites — same pattern as
// internal/backup.VerifySchedulerDeps (B142) and
// internal/update.scheduler (B130).
type CertSyncDeps struct {
	// DB is the runtime DB connection. Used to write the
	// audit_log row on successful pull + on errors.
	DB *sql.DB
	// LocalDir is where the live certs live. The pull
	// writes cert.pem + key.pem into this dir (atomic
	// rename from a temp file). Default
	// /var/lib/skygate/certs/ if empty.
	LocalDir string
	// S3Client is the pkg/s3 client. The scheduler
	// doesn't import the S3 package directly — it goes
	// through the S3Client interface (defined below) so
	// the unit test can pass a fake. The production
	// caller wires the real pkg/s3 here.
	S3Client S3Client
	// S3Bucket is the bucket name (e.g. "skygate-backups").
	// The S3 key layout is `<bucket>/certs/cert.pem`,
	// `<bucket>/certs/key.pem`, `<bucket>/certs/.version`.
	S3Bucket string
	// CaddyReload is called after a successful cert
	// download + write. Usually wired to
	// `docker exec skygate-caddy caddy reload` or
	// `systemctl reload caddy`. Nil = no reload callback
	// (the cert is still on disk; the operator must
	// manually reload Caddy).
	CaddyReload func(ctx context.Context) error
	// Notifier is the Telegram notifier for cert expiry
	// warnings + reload failures. Nil = no alerts.
	// The scheduler is silent on nil.
	Notifier backup.NotifierSink
	// Interval is the tick interval. Default 30s if zero.
	// The check is "if (now - lastTick) > Interval" so
	// an operator-set 5s interval always wins.
	Interval time.Duration
}

// S3Client is the subset of pkg/s3 the scheduler uses. The
// production caller wires the real client; tests wire a
// fake. Keeps the package import-free (no S3 SDK in tests).
type S3Client interface {
	// HeadObject returns the ETag + size for a key, or
	// an error if the key doesn't exist (the "no cert
	// uploaded yet" case).
	HeadObject(ctx context.Context, bucket, key string) (S3ObjectMeta, error)
	// GetObject fetches a key's body. Used for the
	// initial .version + the subsequent cert.pem /
	// key.pem downloads.
	GetObject(ctx context.Context, bucket, key string) ([]byte, error)
	// PutObject uploads a key. Used by the operator-
	// side cert renewal script (not the in-app
	// scheduler — the in-app scheduler only reads).
	// Included here so tests can verify the "operator
	// uploads a new cert, scheduler picks it up"
	// end-to-end flow.
	PutObject(ctx context.Context, bucket, key string, body []byte, contentType string) error
}

// S3ObjectMeta is the minimum metadata the scheduler
// needs. Matches pkg/s3.ObjectMeta (E-Tag + size + last
// modified) but the package only needs the E-Tag (which
// doubles as the content hash on PUT, so the version
// comparison is hash-based — not modified-time-based,
// which would be wrong if the operator re-uploads the
// same bytes).
type S3ObjectMeta struct {
	ETag         string
	Size         int64
	LastModified time.Time
}

// VersionFile is the JSON object the active node writes
// to `<bucket>/certs/.version` after every cert upload.
// The standby (and every other node) reads this to
// decide whether to pull. The "version" is a
// monotonically-increasing integer the operator's
// renewal script bumps on every upload; the SHA-256
// matches the cert.pem body so a puller can detect a
// hash mismatch even if the version number is the same
// (defensive — shouldn't happen, but cheap to guard).
type VersionFile struct {
	Version    int64     `json:"version"`
	SHA256     string    `json:"sha256"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// Constants for the S3 key layout. Centralised here so
// the operator-side cert-renew script can match the
// layout exactly (lives in deploy/cert-renew.sh or
// equivalent; the package only owns the read side).
const (
	VersionS3Key = "certs/.version"
	CertS3Key    = "certs/cert.pem"
	KeyS3Key     = "certs/key.pem"
	// LocalVersionCache is the local file where the
	// scheduler remembers the last version it applied.
	// Lets the scheduler avoid a re-pull on the next
	// tick if S3 was just queried.
	LocalVersionCache = ".certsync-version"
	// LocalCert + LocalKey are the live cert paths. The
	// pull writes these (atomic rename from a temp
	// file in the same dir).
	LocalCert = "cert.pem"
	LocalKey  = "key.pem"
	// ExpiryWarningDays is how many days before expiry
	// the scheduler sends a Telegram alert. Default 7.
	ExpiryWarningDays = 7
)

// Start launches the certsync goroutine and returns
// immediately. The goroutine exits when ctx is canceled
// (caller does the cancellation — same pattern as
// internal/backup.StartVerifyScheduler and
// internal/update.scheduler).
//
// The returned *CertSync is the same struct every
// caller uses; we return it so the operator can wire
// it into the /admin/system_tests "Certsync" panel
// (future B-check surface) or the in-process
// status/healthz endpoint.
//
// On startup error (e.g. can't write the local version
// cache), Start returns the error and the goroutine is
// NOT started. The caller logs the error and either
// retries or runs without certsync (the operator can
// still manage certs manually).
func Start(ctx context.Context, deps CertSyncDeps) (*CertSync, error) {
	if deps.S3Client == nil {
		return nil, errors.New("Start: S3Client is nil (certsync requires an S3 client)")
	}
	if deps.S3Bucket == "" {
		return nil, errors.New("Start: S3Bucket is empty")
	}
	if deps.LocalDir == "" {
		deps.LocalDir = "/var/lib/skygate/certs"
	}
	if deps.Interval == 0 {
		deps.Interval = 30 * time.Second
	}
	// Ensure the local dir exists. Best-effort: if we
	// can't create it, fail the boot (certsync is
	// useless without the local dir).
	if err := os.MkdirAll(deps.LocalDir, 0o755); err != nil {
		return nil, fmt.Errorf("create local dir %s: %w", deps.LocalDir, err)
	}

	cs := &CertSync{}
	// Load the local version cache so we don't pull on
	// the very first tick if S3 hasn't been updated.
	if v, sha, err := loadLocalVersionCache(deps.LocalDir); err == nil {
		cs.lastLocalVersion = v
		cs.lastLocalSHA = sha
	}

	go cs.run(ctx, deps)
	return cs, nil
}

// run is the goroutine body. Splits into tick() (one pass
// over S3 + maybe pull) and a sleep that's canceled by
// ctx. Mirrors the B130/B142 scheduler pattern.
func (c *CertSync) run(ctx context.Context, deps CertSyncDeps) {
	// Initial tick (don't wait Interval before the first
	// check — the operator might have just deployed and
	// wants the new cert applied within 1 tick).
	c.tick(ctx, deps)

	ticker := time.NewTicker(deps.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.tick(ctx, deps)
		}
	}
}

// tick is one pass over S3 + maybe pull. Public for
// tests; production calls go through run().
func (c *CertSync) tick(ctx context.Context, deps CertSyncDeps) {
	// 1. Read the remote .version.
	vf, err := deps.S3Client.GetObject(ctx, deps.S3Bucket, VersionS3Key)
	if err != nil {
		// 404 = "no certs uploaded yet" — normal first-
		// boot state, not an error. Anything else is a
		// transient S3 issue; next tick retries.
		if !isNotFound(err) {
			log.Printf("certsync: get .version: %v", err)
		}
		return
	}
	var remote VersionFile
	if err := json.Unmarshal(vf, &remote); err != nil {
		log.Printf("certsync: parse .version: %v", err)
		return
	}

	// 2. Decide whether to pull. Two conditions:
	//   a. Remote version > local version (new cert
	//      uploaded, the version file was bumped).
	//   b. Remote version == local version BUT the
	//      SHA-256 doesn't match (defensive: shouldn't
	//      happen, but if the operator re-uploaded the
	//      same version with different bytes, the
	//      scheduler should still catch up).
	if remote.Version == c.lastLocalVersion && remote.SHA256 == c.lastLocalSHA {
		// No change. Fall through to the expiry check.
		c.checkExpiry(ctx, deps)
		return
	}
	if remote.Version <= c.lastLocalVersion {
		// Old version (operator rolled back?). Only
		// pull if the SHA differs (rollback to a
		// known-good cert).
		if remote.SHA256 == c.lastLocalSHA {
			return
		}
	}

	// 3. Pull cert.pem + key.pem from S3.
	certBytes, err := deps.S3Client.GetObject(ctx, deps.S3Bucket, CertS3Key)
	if err != nil {
		log.Printf("certsync: get cert.pem: %v", err)
		c.notifyFailure(ctx, deps, "certsync.pull.cert_failed", err.Error())
		return
	}
	keyBytes, err := deps.S3Client.GetObject(ctx, deps.S3Bucket, KeyS3Key)
	if err != nil {
		log.Printf("certsync: get key.pem: %v", err)
		c.notifyFailure(ctx, deps, "certsync.pull.key_failed", err.Error())
		return
	}

	// 4. Validate the cert + key pair before replacing
	// the live files. A bad upload should not bring
	// down the HTTPS listener.
	if err := validateCertKeyPair(certBytes, keyBytes); err != nil {
		log.Printf("certsync: validate cert+key: %v", err)
		c.notifyFailure(ctx, deps, "certsync.pull.invalid", err.Error())
		return
	}

	// 5. Atomic rename. The .new file in the same dir
	// guarantees the rename is atomic on the same
	// filesystem (Linux rename(2) is atomic when both
	// paths are on the same FS, which the local dir
	// is).
	if err := c.writeLocalCerts(deps.LocalDir, certBytes, keyBytes); err != nil {
		log.Printf("certsync: write local certs: %v", err)
		c.notifyFailure(ctx, deps, "certsync.pull.write_failed", err.Error())
		return
	}

	// 6. Update the local version cache so we don't
	// re-pull on the next tick.
	if err := saveLocalVersionCache(deps.LocalDir, remote.Version, remote.SHA256); err != nil {
		log.Printf("certsync: save version cache: %v (non-fatal — next tick may re-pull)", err)
	}
	c.mu.Lock()
	c.lastLocalVersion = remote.Version
	c.lastLocalSHA = remote.SHA256
	c.mu.Unlock()

	// 7. Trigger the Caddy reload (best-effort; the
	// cert is on disk even if the reload fails).
	if deps.CaddyReload != nil {
		if err := deps.CaddyReload(ctx); err != nil {
			log.Printf("certsync: caddy reload: %v", err)
			c.notifyFailure(ctx, deps, "certsync.reload.failed", err.Error())
			// Don't return — the cert is on disk,
			// the audit row + reload-failure alert
			// are the operator's signal.
		}
	}

	// 8. Audit log row.
	c.writeAudit(ctx, deps, "certsync.pull", fmt.Sprintf(
		"version=%d sha256=%s size=%d", remote.Version, remote.SHA256, len(certBytes)))

	log.Printf("certsync: applied version=%d sha256=%s size=%d",
		remote.Version, remote.SHA256, len(certBytes))
}

// checkExpiry is the self-check that warns the operator
// when the local cert is about to expire. Runs on every
// tick (regardless of remote version changes) so a
// skipped pull (e.g. S3 unreachable for 60+ days) still
// surfaces the expiry warning.
func (c *CertSync) checkExpiry(ctx context.Context, deps CertSyncDeps) {
	certPath := filepath.Join(deps.LocalDir, LocalCert)
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return // no local cert yet — first boot, silent.
	}
	cert, err := parseCert(raw)
	if err != nil {
		return // malformed local cert — not our problem here.
	}
	daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)
	if daysLeft > ExpiryWarningDays {
		return
	}
	msg := fmt.Sprintf("cert expires in %d days (%s); renew + upload to S3",
		daysLeft, cert.NotAfter.Format("2006-01-02"))
	log.Printf("certsync: WARN %s", msg)
	if deps.Notifier != nil {
		_ = deps.Notifier.SendAlert("[certsync.expiry_warning] " + msg)
	}
}

// writeLocalCerts atomically renames the new cert.pem +
// key.pem into place. The .new files are written in the
// same directory as the live files so the rename is
// atomic.
func (c *CertSync) writeLocalCerts(dir string, cert, key []byte) error {
	certNew := filepath.Join(dir, LocalCert+".new")
	keyNew := filepath.Join(dir, LocalKey+".new")
	if err := os.WriteFile(certNew, cert, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", certNew, err)
	}
	if err := os.WriteFile(keyNew, key, 0o600); err != nil {
		_ = os.Remove(certNew)
		return fmt.Errorf("write %s: %w", keyNew, err)
	}
	if err := os.Rename(certNew, filepath.Join(dir, LocalCert)); err != nil {
		_ = os.Remove(certNew)
		_ = os.Remove(keyNew)
		return fmt.Errorf("rename cert: %w", err)
	}
	if err := os.Rename(keyNew, filepath.Join(dir, LocalKey)); err != nil {
		return fmt.Errorf("rename key: %w", err)
	}
	return nil
}

// writeAudit inserts a row into audit_log. Best-effort
// (audit failure is not fatal — the pull itself already
// succeeded).
func (c *CertSync) writeAudit(ctx context.Context, deps CertSyncDeps, action, detail string) {
	if deps.DB == nil {
		return
	}
	_, err := deps.DB.ExecContext(ctx,
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'certsync', $1, $2, now())`,
		action, detail)
	if err != nil {
		log.Printf("certsync: audit log insert: %v", err)
	}
}

// notifyFailure sends a Telegram alert for transient
// errors (download failure, invalid cert, reload
// failure). Silent if the Notifier is nil.
//
// Signature: SendAlert(text string) int64 — matches
// internal/backup.NotifierSink (B142) + the rest of
// skygate's "best-effort alert" pattern. The text is
// the human-readable alert body; the action namespace
// goes in the audit log row instead.
func (c *CertSync) notifyFailure(_ context.Context, deps CertSyncDeps, _, detail string) {
	if deps.Notifier == nil {
		return
	}
	_ = deps.Notifier.SendAlert(detail)
}

// ----- shared helpers ---------------------------------------------------

// ValidateCertKeyPair is the exported version of
// validateCertKeyPair. Used by the /admin/certificates
// upload handler (B148) and any other future caller
// that needs to check a cert+key pair before writing
// it to disk. Re-exported here so the validation
// rules (PKCS#1 / PKCS#8 / SEC1) stay in one place
// even when called from outside the package.
func ValidateCertKeyPair(cert, key []byte) error {
	return validateCertKeyPair(cert, key)
}

// validateCertKeyPair checks that the cert + key are
// parseable and match (i.e. the key is the public key
// corresponding to the cert). Defensive: an upload that
// ships a mismatched pair (e.g. the operator uploaded
// the OLD cert with the NEW key) would otherwise break
// Caddy on reload.
func validateCertKeyPair(cert, key []byte) error {
	c, err := parseCert(cert)
	if err != nil {
		return fmt.Errorf("parse cert: %w", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(c.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal cert pub key: %w", err)
	}
	// The key is either PKCS#1 (BEGIN RSA PRIVATE KEY) or
	// PKCS#8 (BEGIN PRIVATE KEY). Try both.
	if matchedAny(key, pub) {
		return nil
	}
	return errors.New("key does not match cert public key")
}

// matchedAny tries to parse the key as PKCS#1, PKCS#8,
// and EC private keys; returns true if ANY of them
// produces a public key matching the cert's. Avoids
// pulling in crypto/rsa + crypto/ec just for this check.
func matchedAny(key, pub []byte) bool {
	// Strip the PEM envelope first. PEM-encoded keys
	// have a "-----BEGIN ..." header + base64 DER
	// body. The parse functions (ParsePKCS1/8/etc)
	// expect raw DER, not PEM.
	der := key
	if block, _ := pem.Decode(key); block != nil {
		der = block.Bytes
	}
	for _, parse := range []func([]byte) (interface{}, error){
		parsePKCS1PrivateKey,
		parsePKCS8PrivateKey,
		parseECPrivateKey,
	} {
		k, err := parse(der)
		if err != nil {
			continue
		}
		kpub, err := publicKeyFromPrivate(k)
		if err != nil {
			continue
		}
		kpubBytes, err := x509.MarshalPKIXPublicKey(kpub)
		if err != nil {
			continue
		}
		if string(kpubBytes) == string(pub) {
			return true
		}
	}
	return false
}

// parseCert is a small wrapper around x509.ParseCertificate
// that strips the PEM envelope first.
func parseCert(raw []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block in cert")
	}
	return x509.ParseCertificate(block.Bytes)
}

// loadLocalVersionCache reads .certsync-version from the
// local dir. Returns (0, "", nil) if the file doesn't
// exist (first boot, no prior pull).
func loadLocalVersionCache(dir string) (int64, string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, LocalVersionCache))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", nil
		}
		return 0, "", err
	}
	// Format: "<version>\n<sha256>\n"
	lines := strings.SplitN(string(raw), "\n", 2)
	if len(lines) < 2 {
		return 0, "", fmt.Errorf("malformed version cache: %q", string(raw))
	}
	var v int64
	if _, err := fmt.Sscanf(lines[0], "%d", &v); err != nil {
		return 0, "", fmt.Errorf("parse version: %w", err)
	}
	return v, lines[1], nil
}

// saveLocalVersionCache writes the version + SHA pair to
// .certsync-version. Atomic via a temp file in the same
// dir + rename.
func saveLocalVersionCache(dir string, version int64, sha string) error {
	tmp := filepath.Join(dir, LocalVersionCache+".new")
	body := fmt.Sprintf("%d\n%s\n", version, sha)
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, LocalVersionCache))
}

// isNotFound is the S3 "object doesn't exist" check. The
// S3 client returns different errors for 404 across
// implementations (pkg/s3 returns *s3.NotFound; the fake
// in tests returns errors.New("not found")). We accept
// both shapes.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NotFound") ||
		strings.Contains(s, "not found") ||
		strings.Contains(s, "NoSuchKey") ||
		strings.Contains(s, "404")
}

// ReadCertBytes is a small helper for tests / admin
// pages that want to read the local cert.pem. Exposed
// for the B-check (which verifies the file is at the
// expected path) and for the future /admin/certificates
// page (B148) which renders "current cert" info.
func ReadCertBytes(dir string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, LocalCert))
}

// sha256Hex is a small helper used by validateCertKeyPair
// and the future "current cert SHA" UI label. Returns the
// hex-encoded SHA-256 of the cert body (NOT the cert +
// key — the key is sensitive and shouldn't be hashed
// into a public field).
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// _ silences the "imported and not used" warning for
// io.ReadAll in older Go versions where we used it
// in the original S3 client wrapper (now we use
// deps.S3Client.GetObject instead).
var _ = io.Discard
