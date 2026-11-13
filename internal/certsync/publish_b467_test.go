// B467 — the certificate page must WRITE what it reports.
//
// Pre-B467 `PostAdminCertificateUpload` validated the pair and called a callback that was
// never wired (Service.CertUploadToS3 stayed nil), so the handler wrote nothing — no local
// file, no S3 object — while the flash said «Cert uploaded … The certsync scheduler will pull
// within 30s». These tests pin the three states Publish has to keep in agreement: the local
// pair, the S3 objects + `.version`, and the local version cache (without which the very next
// certsync tick reverts the certificate that was just published).
package certsync

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// publishFakeS3 is an in-memory S3Client that also records the order of writes, because the ORDER
// is part of the contract: `.version` must be written after the objects it advertises.
type publishFakeS3 struct {
	objects  map[string][]byte
	puts     []string
	failPuts map[string]error
}

func newPublishFakeS3() *publishFakeS3 {
	return &publishFakeS3{objects: map[string][]byte{}, failPuts: map[string]error{}}
}

func (f *publishFakeS3) HeadObject(_ context.Context, _, key string) (S3ObjectMeta, error) {
	if b, ok := f.objects[key]; ok {
		return S3ObjectMeta{Size: int64(len(b))}, nil
	}
	return S3ObjectMeta{}, errors.New("not found")
}

func (f *publishFakeS3) GetObject(_ context.Context, _, key string) ([]byte, error) {
	if b, ok := f.objects[key]; ok {
		return b, nil
	}
	return nil, errors.New("not found")
}

func (f *publishFakeS3) PutObject(_ context.Context, _, key string, body []byte, _ string) error {
	if err, ok := f.failPuts[key]; ok {
		return err
	}
	f.objects[key] = append([]byte(nil), body...)
	f.puts = append(f.puts, key)
	return nil
}

// testPair generates a self-signed certificate and its matching EC key.
func testPair(t *testing.T, dns ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dns[0]},
		DNSNames:     dns,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// TestB467_PublishWritesTheLocalPair is the core promise: the files the page says it wrote
// really exist, with the permissions a private key needs.
func TestB467_PublishWritesTheLocalPair(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testPair(t, "skygate.example.com")

	res, err := Publish(context.Background(), PublishDeps{LocalDir: dir, Logger: func(string, ...any) {}}, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	gotCert, err := os.ReadFile(filepath.Join(dir, LocalCert))
	if err != nil {
		t.Fatalf("cert.pem was not written: %v", err)
	}
	if string(gotCert) != string(certPEM) {
		t.Error("cert.pem content differs from what was published")
	}
	gotKey, err := os.ReadFile(filepath.Join(dir, LocalKey))
	if err != nil {
		t.Fatalf("key.pem was not written: %v", err)
	}
	if string(gotKey) != string(keyPEM) {
		t.Error("key.pem content differs from what was published")
	}
	st, err := os.Stat(filepath.Join(dir, LocalKey))
	if err != nil {
		t.Fatalf("stat key.pem: %v", err)
	}
	// Windows has no Unix permission bits in the same sense — the mode assertion is about
	// the Linux deployment, so it is skipped where the host cannot express it (a check must
	// never turn a host fact into a product claim).
	if runtime.GOOS != "windows" {
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("key.pem mode = %o, want 600 — a private key must not be world-readable", perm)
		}
	}
	if res.S3 {
		t.Error("S3 must be false when no client/bucket is configured")
	}
	if !strings.Contains(res.S3Note, "local only") {
		t.Errorf("S3Note = %q, want it to say the publish was local only", res.S3Note)
	}
	if res.SHA256 == "" || len(res.SHA256) != 64 {
		t.Errorf("SHA256 = %q, want a hex sha256", res.SHA256)
	}
	// No reload callback → the result says so instead of implying one happened.
	if res.Reloaded || !strings.Contains(res.ReloadNote, "no reload callback") {
		t.Errorf("Reloaded=%v ReloadNote=%q, want an explicit 'no callback' note", res.Reloaded, res.ReloadNote)
	}
}

// TestB467_PublishKeepsThePreviousPair — `.prev` is the one step back an operator needs after
// a wrong upload; certsync's pull path has never kept one.
func TestB467_PublishKeepsThePreviousPair(t *testing.T) {
	dir := t.TempDir()
	firstCert, firstKey := testPair(t, "first.example.com")
	if _, err := Publish(context.Background(), PublishDeps{LocalDir: dir, Logger: func(string, ...any) {}}, firstCert, firstKey); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	secondCert, secondKey := testPair(t, "second.example.com")
	res, err := Publish(context.Background(), PublishDeps{LocalDir: dir, Logger: func(string, ...any) {}}, secondCert, secondKey)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if res.PrevCert == "" || res.PrevKey == "" {
		t.Fatalf("the previous pair was not kept: PrevCert=%q PrevKey=%q", res.PrevCert, res.PrevKey)
	}
	prev, err := os.ReadFile(res.PrevCert)
	if err != nil {
		t.Fatalf("read .prev cert: %v", err)
	}
	if string(prev) != string(firstCert) {
		t.Error(".prev does not hold the certificate that was replaced")
	}
}

// TestB467_PublishBumpsVersionAndWritesItLast — the ORDER is the contract: a reader must
// never see a .version that advertises objects that are not uploaded yet.
func TestB467_PublishBumpsVersionAndWritesItLast(t *testing.T) {
	f := newPublishFakeS3()
	dir := t.TempDir()
	certPEM, keyPEM := testPair(t, "skygate.example.com")
	deps := PublishDeps{LocalDir: dir, S3Client: f, S3Bucket: "b", Logger: func(string, ...any) {}}

	res, err := Publish(context.Background(), deps, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if !res.S3 || res.Version != 1 {
		t.Fatalf("S3=%v Version=%d, want true/1 for the first upload", res.S3, res.Version)
	}
	if len(f.puts) != 3 || f.puts[2] != VersionS3Key {
		t.Fatalf("upload order = %v, want cert, key, then %s", f.puts, VersionS3Key)
	}
	var vf VersionFile
	if err := json.Unmarshal(f.objects[VersionS3Key], &vf); err != nil {
		t.Fatalf("parse .version: %v", err)
	}
	if vf.SHA256 != res.SHA256 {
		t.Errorf(".version sha256 = %q, want %q", vf.SHA256, res.SHA256)
	}

	// Second publish bumps to 2 and the cache follows, or certsync would re-pull version 1.
	res2, err := Publish(context.Background(), deps, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if res2.Version != 2 {
		t.Errorf("second version = %d, want 2", res2.Version)
	}
	cachedVersion, cachedSHA, err := loadLocalVersionCache(dir)
	if err != nil {
		t.Fatalf("load local version cache: %v", err)
	}
	if cachedVersion != 2 || cachedSHA != res2.SHA256 {
		t.Errorf("local cache = %d/%s (len %d), want 2/%s (len %d)", cachedVersion, cachedSHA, len(cachedSHA), res2.SHA256, len(res2.SHA256))
	}
}

// TestB467_VersionCacheRoundTrips is the paired defect found by the test above: the writer
// ends the file with a newline and the reader returned the SHA WITH it, so the value seeded
// into the scheduler never matched a real SHA and every process start re-pulled the pair.
func TestB467_VersionCacheRoundTrips(t *testing.T) {
	dir := t.TempDir()
	const sha = "2802dac51769e3a0f987ec80abb09472a06ca045645c578c999de6b642d31927"
	if err := saveLocalVersionCache(dir, 7, sha); err != nil {
		t.Fatalf("save: %v", err)
	}
	v, got, err := loadLocalVersionCache(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if v != 7 {
		t.Errorf("version = %d, want 7", v)
	}
	if got != sha {
		t.Errorf("sha256 = %q (len %d), want %q (len %d) — the reader must return the value the writer stored", got, len(got), sha, len(sha))
	}
}

// TestB467_PublishedCertSurvivesTheNextCertsyncTick — the measured "the button worked, then
// the certificate reverted" failure mode: a scheduler whose remote version is not newer must
// leave the published pair alone.
func TestB467_PublishedCertSurvivesTheNextCertsyncTick(t *testing.T) {
	f := newPublishFakeS3()
	dir := t.TempDir()
	certPEM, keyPEM := testPair(t, "skygate.example.com")
	deps := PublishDeps{LocalDir: dir, S3Client: f, S3Bucket: "b", Logger: func(string, ...any) {}}
	if _, err := Publish(context.Background(), deps, certPEM, keyPEM); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// The scheduler boot-loads the version cache exactly like Start() does.
	version, sha, err := loadLocalVersionCache(dir)
	if err != nil {
		t.Fatalf("load cache: %v", err)
	}
	cs := &CertSync{lastLocalVersion: version, lastLocalSHA: sha}
	cs.tick(context.Background(), CertSyncDeps{LocalDir: dir, S3Client: f, S3Bucket: "b"})

	after, err := os.ReadFile(filepath.Join(dir, LocalCert))
	if err != nil {
		t.Fatalf("read cert after tick: %v", err)
	}
	if string(after) != string(certPEM) {
		t.Error("the next certsync tick replaced the pair that was just published")
	}
}

// TestB467_PublishRejectsABadPair — nothing may be written when the key does not match.
func TestB467_PublishRejectsABadPair(t *testing.T) {
	dir := t.TempDir()
	certPEM, _ := testPair(t, "skygate.example.com")
	_, otherKey := testPair(t, "other.example.com")
	if _, err := Publish(context.Background(), PublishDeps{LocalDir: dir, Logger: func(string, ...any) {}}, certPEM, otherKey); err == nil {
		t.Fatal("a mismatched key was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, LocalCert)); !os.IsNotExist(err) {
		t.Error("a rejected pair left a cert.pem behind")
	}
}

// TestB467_S3FailureStillTellsTheOperatorTheLocalCopyIsLive — the two halves fail
// independently, and "upload failed" without saying the host is already serving the new cert
// is how an operator re-uploads in a panic.
func TestB467_S3FailureStillTellsTheOperatorTheLocalCopyIsLive(t *testing.T) {
	f := newPublishFakeS3()
	f.failPuts[CertS3Key] = errors.New("403 Access Denied")
	dir := t.TempDir()
	certPEM, keyPEM := testPair(t, "skygate.example.com")

	res, err := Publish(context.Background(), PublishDeps{LocalDir: dir, S3Client: f, S3Bucket: "b", Logger: func(string, ...any) {}}, certPEM, keyPEM)
	if err == nil {
		t.Fatal("an S3 failure was reported as success")
	}
	if !strings.Contains(err.Error(), "LOCAL pair is written") {
		t.Errorf("error = %v, want it to say the local pair is live", err)
	}
	if res.S3 {
		t.Error("S3 must stay false when the upload failed")
	}
	if _, statErr := os.Stat(filepath.Join(dir, LocalCert)); statErr != nil {
		t.Error("the local pair must still be written when only S3 failed")
	}
}

// TestB467_ReloadFailureIsANoteNotAFailure — the files are already correct; a proxy that is
// not running must not make the page claim the certificate was not installed.
func TestB467_ReloadFailureIsANoteNotAFailure(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testPair(t, "skygate.example.com")
	res, err := Publish(context.Background(), PublishDeps{
		LocalDir:    dir,
		Logger:      func(string, ...any) {},
		CaddyReload: func(context.Context) error { return errors.New("caddy not running") },
	}, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("a reload failure must not fail the publish: %v", err)
	}
	if res.Reloaded {
		t.Error("Reloaded must be false when the callback failed")
	}
	if !strings.Contains(res.ReloadNote, "caddy not running") {
		t.Errorf("ReloadNote = %q, want the callback's error", res.ReloadNote)
	}
}
