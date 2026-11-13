// publish_b467.go — B467: skygate must be able to WRITE the certificate it reports.
//
// Measured on the published v1.5.212 (2026-11-13): `/admin/certificates` rendered
// «Cert uploaded. SHA-256=… The certsync scheduler will pull within 30s» after a submit —
// and NOTHING had been written. `PostAdminCertificateUpload` validates the pair and then
// calls `Service.uploadCertToS3`, which is a stub: it returns nil when
// `s.CertUploadToS3 == nil`, and nothing in cmd/skygate ever set that callback. The handler
// contains no write at all (its only os.* call is os.ReadFile for display), the local
// directory is empty on a fresh host, and the flash promised a pull that could not happen.
// A certificate page that says "uploaded" without writing a byte is the same defect class as
// B465's stale verdict and B466's slow probe: the message and the measurement disagree.
//
// Publish is the ONE function that puts a pair where certsync reads it, and it keeps the
// three pieces of state that must agree with each other:
//
//  1. the local files (atomic rename, previous pair kept as .prev),
//  2. the S3 objects + `.version` (bumped LAST, so a crash can never advertise a pair that
//     is not there),
//  3. the local version cache, WITHOUT which the next certsync tick sees "remote is newer"
//     and immediately overwrites the certificate that was just issued — the trap that makes
//     "the button worked, then the cert reverted" look like a caching bug.
package certsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// DefaultLocalDir is where the live pair lives when the caller does not say otherwise.
// Same value Start() applies to CertSyncDeps.LocalDir.
const DefaultLocalDir = "/var/lib/skygate/certs"

// PublishDeps is what Publish needs. S3Client/S3Bucket are optional: a native install with
// no S3 (the common case — TLS is terminated by the operator's proxy) publishes locally and
// says so, instead of claiming an upload it did not perform.
type PublishDeps struct {
	// LocalDir is the directory certsync reads. Empty = DefaultLocalDir.
	LocalDir string
	// S3Client uploads the pair. Nil = local only (reported in PublishResult.S3Note).
	S3Client S3Client
	// S3Bucket is the bucket; empty = local only even when a client is set.
	S3Bucket string
	// CaddyReload is called after the files are in place. Nil = no reload (reported).
	CaddyReload func(ctx context.Context) error
	// Logger receives the publish decision. Nil = log.Printf.
	Logger func(format string, args ...any)
}

// PublishResult is what the caller can show the operator: every place the pair went, and
// every place it did NOT go, with the reason. The admin page builds its flash from this —
// never from an assumption.
type PublishResult struct {
	LocalDir  string
	CertPath  string
	KeyPath   string
	SHA256    string
	CertBytes int
	KeyBytes  int

	// PrevCert/PrevKey are the .prev paths written from a previous pair ("" when this is
	// the first certificate on the host).
	PrevCert string
	PrevKey  string

	// Version is the .version value in S3 (0 when S3 was not used).
	Version int64
	// S3 says whether the pair actually reached S3.
	S3 bool
	// S3Note explains a skip or a failure in operator language.
	S3Note string

	// Reloaded says whether a reload callback ran and succeeded.
	Reloaded bool
	// ReloadNote explains a skipped/failed reload.
	ReloadNote string

	WrittenAt time.Time
}

// describe renders the one-line summary the audit row and the log carry.
func (r PublishResult) describe() string {
	rel := "reload=none"
	switch {
	case r.Reloaded:
		rel = "reload=ok"
	case r.ReloadNote != "":
		rel = "reload=" + r.ReloadNote
	}
	s3 := "s3=no"
	if r.S3 {
		s3 = fmt.Sprintf("s3=yes version=%d", r.Version)
	} else if r.S3Note != "" {
		s3 = "s3=" + r.S3Note
	}
	return fmt.Sprintf("sha256=%s size=%d path=%s %s %s", r.SHA256, r.CertBytes, r.CertPath, s3, rel)
}

// Publish validates, writes and (optionally) uploads one certificate+key pair.
//
// A non-nil error means the caller must NOT claim success. The returned Result is still
// populated, because the two halves fail independently: if the local write succeeded and the
// S3 upload failed, the operator has to be told exactly that (the local certificate is live;
// the shared copy is stale), not "upload failed" with no mention of the file that changed.
func Publish(ctx context.Context, deps PublishDeps, certPEM, keyPEM []byte) (PublishResult, error) {
	logf := deps.Logger
	if logf == nil {
		logf = log.Printf
	}
	dir := deps.LocalDir
	if dir == "" {
		dir = DefaultLocalDir
	}
	res := PublishResult{
		LocalDir:  dir,
		CertPath:  filepath.Join(dir, LocalCert),
		KeyPath:   filepath.Join(dir, LocalKey),
		CertBytes: len(certPEM),
		KeyBytes:  len(keyPEM),
		WrittenAt: time.Now().UTC(),
	}

	// 1. Refuse a pair that does not belong together. certsync owns the rule (PKCS#1,
	// PKCS#8, SEC1) so the upload path, the pull path and this function agree.
	if err := ValidateCertKeyPair(certPEM, keyPEM); err != nil {
		return res, fmt.Errorf("certificate/key pair rejected: %w", err)
	}
	sum := sha256.Sum256(certPEM)
	res.SHA256 = hex.EncodeToString(sum[:])

	// 2. The directory must exist — os.Rename into a missing dir is the classic
	// "first certificate on a fresh native host" failure.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, fmt.Errorf("create %s: %w", dir, err)
	}

	// 3. Keep the previous pair. certsync's pull path has never done this, which is why a
	// bad upload could not be rolled back without a backup: `.prev` is the one step back.
	res.PrevCert, res.PrevKey = backupPair(dir)
	if res.PrevCert != "" {
		logf("certsync: previous pair kept as %s / %s", res.PrevCert, res.PrevKey)
	}

	// 4. Atomic write (same dir, rename(2)).
	if err := writeLocalCertPair(dir, certPEM, keyPEM); err != nil {
		return res, fmt.Errorf("write pair: %w", err)
	}
	logf("certsync: pair published to %s (sha256=%s size=%d)", res.CertPath, res.SHA256, res.CertBytes)

	// 5. S3 + .version, when configured. The order is deliberate: cert, key, THEN .version,
	// so a reader never sees a version that points at objects that are not uploaded yet.
	if deps.S3Client != nil && deps.S3Bucket != "" {
		version, err := bumpRemoteVersion(ctx, deps.S3Client, deps.S3Bucket)
		if err != nil {
			res.S3Note = "version read failed: " + err.Error()
			return res, fmt.Errorf("S3: read current .version: %w (the LOCAL pair is written and live at %s)", err, res.CertPath)
		}
		if err := deps.S3Client.PutObject(ctx, deps.S3Bucket, CertS3Key, certPEM, "application/x-pem-file"); err != nil {
			res.S3Note = "cert upload failed: " + err.Error()
			return res, fmt.Errorf("S3: upload cert.pem: %w (the LOCAL pair is written and live at %s)", err, res.CertPath)
		}
		if err := deps.S3Client.PutObject(ctx, deps.S3Bucket, KeyS3Key, keyPEM, "application/x-pem-file"); err != nil {
			res.S3Note = "key upload failed: " + err.Error()
			return res, fmt.Errorf("S3: upload key.pem: %w (the LOCAL pair is written and live at %s)", err, res.CertPath)
		}
		vf := VersionFile{Version: version, SHA256: res.SHA256, UploadedAt: res.WrittenAt}
		body, _ := json.Marshal(vf)
		if err := deps.S3Client.PutObject(ctx, deps.S3Bucket, VersionS3Key, body, "application/json"); err != nil {
			res.S3Note = ".version upload failed: " + err.Error()
			return res, fmt.Errorf("S3: upload .version: %w (the LOCAL pair is written and live at %s)", err, res.CertPath)
		}
		res.Version = version
		res.S3 = true
		// 6. The local cache must agree, or the next tick treats the remote as newer and
		// overwrites what we just published.
		if err := saveLocalVersionCache(dir, version, res.SHA256); err != nil {
			logf("certsync: save version cache: %v (the next tick may re-pull and revert this pair)", err)
		}
	} else {
		switch {
		case deps.S3Client == nil:
			res.S3Note = "no S3 client configured (local only)"
		default:
			res.S3Note = "no S3 bucket configured (local only)"
		}
	}

	// 7. Reload last: the files are already correct, so a reload failure is a note, not a
	// failure of the publish. The cert is on disk and the next reload (or restart) picks it up.
	if deps.CaddyReload != nil {
		if err := deps.CaddyReload(ctx); err != nil {
			res.ReloadNote = "failed: " + err.Error()
			logf("certsync: reload after publish: %v (the certificate is on disk)", err)
		} else {
			res.Reloaded = true
		}
	} else {
		res.ReloadNote = "no reload callback wired"
	}

	logf("certsync: publish ok — %s", res.describe())
	return res, nil
}

// bumpRemoteVersion reads the current .version and returns the next one. A missing object is
// version 1 (the first certificate ever published to this bucket).
func bumpRemoteVersion(ctx context.Context, client S3Client, bucket string) (int64, error) {
	raw, err := client.GetObject(ctx, bucket, VersionS3Key)
	if err != nil {
		if isNotFound(err) {
			return 1, nil
		}
		return 0, err
	}
	var cur VersionFile
	if err := json.Unmarshal(raw, &cur); err != nil {
		// A corrupt .version must not block a publish: treating it as 0 makes the next
		// version 1, which every reader compares correctly ("newer than nothing").
		return 1, nil
	}
	return cur.Version + 1, nil
}

// backupPair copies the live pair to .prev (best effort: a first publish has nothing to
// copy, and a failed backup must not block a correct certificate from being installed).
func backupPair(dir string) (prevCert, prevKey string) {
	cert := filepath.Join(dir, LocalCert)
	key := filepath.Join(dir, LocalKey)
	if _, err := os.Stat(cert); err != nil {
		return "", ""
	}
	prevCert = cert + ".prev"
	if err := copyFile(cert, prevCert, 0o644); err != nil {
		prevCert = ""
	}
	prevKey = key + ".prev"
	if err := copyFile(key, prevKey, 0o600); err != nil {
		prevKey = ""
	}
	return prevCert, prevKey
}

// copyFile is a small explicit copy (no os.Link: the caller may keep editing the source
// path, and a hard link would make the "previous" cert change under the operator's feet).
func copyFile(src, dst string, mode os.FileMode) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// writeLocalCertPair is the package-level form of CertSync.writeLocalCerts, so a caller with
// no scheduler (the admin page, the ACME issuer) writes exactly the same bytes the same way.
func writeLocalCertPair(dir string, cert, key []byte) error {
	return (&CertSync{}).writeLocalCerts(dir, cert, key)
}
