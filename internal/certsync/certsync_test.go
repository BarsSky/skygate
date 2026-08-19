// Package certsync — unit tests for the certsync scheduler.
//
// v1.5.0 / B147.
//
// What this test covers (per the B147 B-check contract):
//
//   - Version-bump detection: a new .version in S3 with
//     a higher version number than the local cache triggers
//     a pull.
//   - SHA-mismatch detection: the same version number with
//     a different SHA-256 also triggers a pull (defensive).
//   - SHA-match skip: same version + same SHA = no pull
//     (avoids the 30s-tick re-pull loop).
//   - Caddy reload stub: the test injects a fake reload
//     callback and verifies it's called after a successful
//     pull (so the operator can see that the reload wiring
//     works without needing a real Caddy container).
//   - Idempotency: two consecutive pulls with the same
//     remote state — second pull is a no-op (the operator
//     can see the audit log shows the first pull only).
//
// What's NOT covered here (and the B-check doesn't pin):
//   - Real S3 client integration (covered by the live
//     verify on the VM, post-deploy).
//   - The atomic rename + concurrent-write safety
//     (covered by the OS-level rename(2) guarantee; a
//     unit test would require a test-only filesystem
//     layer that the operator doesn't need to reason
//     about).
//   - The minio adapter (covered by the live verify —
//     the adapter is a 30-line shim, the logic is in
//     minio).
package certsync

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeS3 implements the S3Client interface for the
// unit tests. Stores objects in an in-memory map;
// tracks calls to HeadObject / GetObject / PutObject
// so the test can assert "only one GetObject per pull,
// not two" etc.
type fakeS3 struct {
	objects map[string][]byte
	// heads + gets track call counts; tests can
	// assert "GetObject was called 3 times (1 for
	// .version + 2 for cert + key)".
	heads atomic.Int64
	gets  atomic.Int64
	puts  atomic.Int64
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: make(map[string][]byte)}
}

func (f *fakeS3) HeadObject(_ context.Context, _, key string) (S3ObjectMeta, error) {
	f.heads.Add(1)
	_, ok := f.objects[key]
	if !ok {
		return S3ObjectMeta{}, &notFoundError{key: key}
	}
	return S3ObjectMeta{ETag: "fake-etag", Size: int64(len(f.objects[key]))}, nil
}

func (f *fakeS3) GetObject(_ context.Context, _, key string) ([]byte, error) {
	f.gets.Add(1)
	v, ok := f.objects[key]
	if !ok {
		return nil, &notFoundError{key: key}
	}
	return v, nil
}

func (f *fakeS3) PutObject(_ context.Context, _, key string, body []byte, _ string) error {
	f.puts.Add(1)
	f.objects[key] = body
	return nil
}

// notFoundError is the fake's "object missing" error.
// Mirrors the real minio client's behavior so the
// isNotFound helper in certsync.go matches it.
type notFoundError struct{ key string }

func (e *notFoundError) Error() string { return "NotFound: " + e.key }

// TestNoVersionIsNoOp verifies that a fresh deploy
// (no .version in S3) does not pull anything. The
// tick is a silent no-op.
func TestNoVersionIsNoOp(t *testing.T) {
	tmp := t.TempDir()
	s3 := newFakeS3()
	deps := CertSyncDeps{
		DB:       nil,
		LocalDir: tmp,
		S3Client: s3,
		S3Bucket: "test-bucket",
		Interval: time.Hour, // huge — only run the manual tick
	}
	cs := &CertSync{}
	cs.tick(context.Background(), deps)
	// The tick should check .version (1 GetObject call)
	// and silently return when it's missing. The cert
	// + key downloads are skipped because the version
	// check short-circuits the pull.
	if s3.gets.Load() != 1 {
		t.Errorf("expected exactly 1 GetObject call (the .version check), got %d", s3.gets.Load())
	}
	// No local cert created.
	if _, err := os.Stat(filepath.Join(tmp, LocalCert)); !os.IsNotExist(err) {
		t.Errorf("expected no cert to be created, got: %v", err)
	}
}

// TestVersionBumpTriggersPull verifies that a new
// .version in S3 with a higher version number than
// the local cache triggers a pull.
func TestVersionBumpTriggersPull(t *testing.T) {
	tmp := t.TempDir()
	s3 := newFakeS3()
	// Generate a self-signed cert + matching key.
	cert, key := mustGenTestCertKeyPair(t)
	s3.objects[CertS3Key] = cert
	s3.objects[KeyS3Key] = key
	// Push a .version with version=1.
	s3.objects[VersionS3Key] = mustMarshalVersion(t, VersionFile{
		Version: 1, SHA256: "deadbeef", UploadedAt: time.Now().UTC(),
	})

	var reloadCalled atomic.Int32
	deps := CertSyncDeps{
		DB:       nil,
		LocalDir: tmp,
		S3Client: s3,
		S3Bucket: "test-bucket",
		Interval: time.Hour,
		CaddyReload: func(_ context.Context) error {
			reloadCalled.Add(1)
			return nil
		},
	}
	cs := &CertSync{}
	cs.tick(context.Background(), deps)

	// Cert + key written to disk.
	if _, err := os.Stat(filepath.Join(tmp, LocalCert)); err != nil {
		t.Errorf("cert not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, LocalKey)); err != nil {
		t.Errorf("key not written: %v", err)
	}
	// Reload callback was called exactly once.
	if reloadCalled.Load() != 1 {
		t.Errorf("expected reload to be called once, got %d", reloadCalled.Load())
	}
	// Local version cache was written.
	cache, sha, err := loadLocalVersionCache(tmp)
	if err != nil {
		t.Fatalf("loadLocalVersionCache: %v", err)
	}
	if cache != 1 {
		t.Errorf("expected local version=1, got %d", cache)
	}
	if sha == "" {
		t.Errorf("expected non-empty SHA cache")
	}
	// Second tick with the same version = no-op for the
	// cert + key download (still does the .version check
	// every tick — that's the whole point of polling).
	// Expected delta: +1 (the .version read), NOT +3
	// (which would be .version + cert + key).
	getsBefore := s3.gets.Load()
	cs.tick(context.Background(), deps)
	if s3.gets.Load() != getsBefore+1 {
		t.Errorf("second tick should only re-read .version (+1 GetObject), got %d more (would mean a re-pull)", s3.gets.Load()-getsBefore-1)
	}
}

// TestSHAMismatchTriggersPull verifies that the
// defensive "same version, different SHA" branch
// catches a re-uploaded cert that bumped nothing but
// has new bytes.
func TestSHAMismatchTriggersPull(t *testing.T) {
	tmp := t.TempDir()
	s3 := newFakeS3()
	cert, key := mustGenTestCertKeyPair(t)
	s3.objects[CertS3Key] = cert
	s3.objects[KeyS3Key] = key
	s3.objects[VersionS3Key] = mustMarshalVersion(t, VersionFile{
		Version: 1, SHA256: "fake-original-sha", UploadedAt: time.Now().UTC(),
	})

	// Pre-seed the local cache with a DIFFERENT SHA so
	// the scheduler thinks it's seen a different cert
	// (the "version same, SHA different" branch).
	if err := saveLocalVersionCache(tmp, 1, "stale-cache-sha"); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}

	deps := CertSyncDeps{
		LocalDir: tmp,
		S3Client: s3,
		S3Bucket: "test-bucket",
		Interval: time.Hour,
	}
	cs := &CertSync{}
	cs.tick(context.Background(), deps)

	// Local cache should now have the new SHA.
	_, sha, err := loadLocalVersionCache(tmp)
	if err != nil {
		t.Fatalf("loadLocalVersionCache: %v", err)
	}
	if sha == "stale-cache-sha" {
		t.Errorf("SHA cache was not updated; SHA-mismatch branch did not trigger pull")
	}
}

// TestInvalidCertFails verifies that a pull with a
// cert that doesn't match the key does NOT replace
// the live files. The defensive validateCertKeyPair
// step must catch this.
func TestInvalidCertFails(t *testing.T) {
	tmp := t.TempDir()
	s3 := newFakeS3()
	cert, _ := mustGenTestCertKeyPair(t)
	// Use a DIFFERENT key (not the one matching the
	// cert) — validateCertKeyPair must catch this.
	_, otherKey := mustGenTestCertKeyPair(t)
	s3.objects[CertS3Key] = cert
	s3.objects[KeyS3Key] = otherKey
	s3.objects[VersionS3Key] = mustMarshalVersion(t, VersionFile{
		Version: 1, SHA256: "fake", UploadedAt: time.Now().UTC(),
	})

	deps := CertSyncDeps{
		LocalDir: tmp,
		S3Client: s3,
		S3Bucket: "test-bucket",
		Interval: time.Hour,
	}
	cs := &CertSync{}
	cs.tick(context.Background(), deps)

	// Cert was NOT written (validation failed).
	if _, err := os.Stat(filepath.Join(tmp, LocalCert)); !os.IsNotExist(err) {
		t.Errorf("cert was written despite invalid cert+key pair: %v", err)
	}
}

// ----- helpers ----------------------------------------------------------

// mustGenTestCertKeyPair generates a self-signed cert
// + matching RSA-2048 private key for tests. Returns
// PEM bytes (cert + key). Panics on error (test-only
// helper).
func mustGenTestCertKeyPair(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM
}

// mustMarshalVersion marshals a VersionFile to JSON
// bytes for tests. Panics on error.
func mustMarshalVersion(t *testing.T, v VersionFile) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal version: %v", err)
	}
	return b
}
