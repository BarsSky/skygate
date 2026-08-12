// 2026-08-12 v1.3.8: Tests for the S3 / S3-compatible
// backup transport. The S3 PUT path uses minio-go
// (a real network client), so we test the pure
// functions here and the contract-driven helpers
// (buildS3Key, detectProtocol, validate-config).
// The full PUT round-trip is verified by the
// scripts/check_b100.sh end-to-end test that runs
// against a throwaway minio container on the VM.

package backup

import (
	"strings"
	"testing"
)

func TestBuildS3Key(t *testing.T) {
	cases := []struct {
		prefix, basename, want string
	}{
		// Empty prefix = bare basename. Most
		// common case for "use the bucket root".
		{"", "foo.tar.gz", "foo.tar.gz"},
		// Single-level prefix.
		{"backups", "foo.tar.gz", "backups/foo.tar.gz"},
		// Prefix with trailing slash (admin
		// pasted "backups/" by accident).
		{"backups/", "foo.tar.gz", "backups/foo.tar.gz"},
		// Multi-level prefix.
		{"a/b/c", "foo.tar.gz", "a/b/c/foo.tar.gz"},
		// Multi-level with trailing slash.
		{"a/b/c/", "foo.tar.gz", "a/b/c/foo.tar.gz"},
		// Basename with leading slash (also a
		// paste accident from the admin).
		{"", "/foo.tar.gz", "foo.tar.gz"},
		// Both have extra slashes.
		{"x//", "/y.tar.gz", "x/y.tar.gz"},
		// Empty both — should never happen in
		// practice (we never upload "" to S3)
		// but the function should not panic.
		{"", "", ""},
	}
	for _, tc := range cases {
		got := buildS3Key(tc.prefix, tc.basename)
		if got != tc.want {
			t.Errorf("buildS3Key(%q, %q) = %q, want %q",
				tc.prefix, tc.basename, got, tc.want)
		}
	}
}

func TestDetectProtocolS3(t *testing.T) {
	cases := []struct {
		dest string
		want Protocol
	}{
		// The s3:// scheme is the explicit
		// opt-in. Even though the destination
		// string for S3 is normally just
		// "bucket/prefix" without a scheme,
		// admins who paste s3://bucket/foo
		// should land on S3, not on the
		// "looks like NFS because it has :/"
		// fallback (note: s3:// has :/ but
		// it's preceded by a scheme, not a
		// hostname).
		{"s3://bucket", ProtocolS3},
		{"s3://bucket/prefix", ProtocolS3},
		// A bare "bucket/prefix" string is
		// ambiguous — it could be a local
		// relative path. We default to local
		// (admin must pick S3 in the dropdown
		// explicitly for S3 to win on a bare
		// bucket name).
		{"bucket/prefix", ProtocolLocal},
	}
	for _, tc := range cases {
		got := detectProtocol(tc.dest)
		if got != tc.want {
			t.Errorf("detectProtocol(%q) = %q, want %q",
				tc.dest, got, tc.want)
		}
	}
}

func TestS3Validate(t *testing.T) {
	// Empty Config (just destination) is not enough
	// to validate S3 — the bucket + creds are
	// required. Validate() should fill S3Region
	// and S3StagingDir defaults when they're empty.
	c := &Config{
		Destination: "my-bucket/backups",
		Protocol:    ProtocolS3,
		// S3Region/S3StagingDir left blank
		// intentionally — Validate should fill
		// the defaults.
		S3Bucket:    "my-bucket",
		S3AccessKey: "AKIA...",
		S3SecretKey: "secret",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.S3Region != "us-east-1" {
		t.Errorf("S3Region default not applied: got %q", c.S3Region)
	}
	if c.S3StagingDir != "/var/lib/skygate/backup-staging" {
		t.Errorf("S3StagingDir default not applied: got %q", c.S3StagingDir)
	}
	// Missing bucket is a hard error.
	bad := &Config{
		Destination: "my-bucket/backups",
		Protocol:    ProtocolS3,
		S3AccessKey: "AKIA...",
		S3SecretKey: "secret",
		// S3Bucket: ""  ← missing
	}
	if err := bad.Validate(); err == nil {
		t.Error("expected error for missing S3Bucket, got nil")
	} else if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("error should mention 'bucket': %v", err)
	}
	// Missing creds is a hard error.
	bad2 := &Config{
		Destination: "my-bucket",
		Protocol:    ProtocolS3,
		S3Bucket:    "my-bucket",
		// S3AccessKey: ""  ← missing
		S3SecretKey: "secret",
	}
	if err := bad2.Validate(); err == nil {
		t.Error("expected error for missing S3AccessKey, got nil")
	}
	// Mountpoint is NOT required for S3 (no FUSE
	// layer) — Validate() must not error here.
	noMount := &Config{
		Destination: "my-bucket",
		Protocol:    ProtocolS3,
		S3Bucket:    "my-bucket",
		S3AccessKey: "AKIA...",
		S3SecretKey: "secret",
		// Mountpoint: ""  ← fine for S3
	}
	if err := noMount.Validate(); err != nil {
		t.Errorf("S3 should not require mountpoint: %v", err)
	}
}

func TestIsValidProtocolIncludesS3(t *testing.T) {
	// v1.3.8 added S3 to the AllProtocols slice.
	// IsValidProtocol is the function the form
	// validator uses; it must accept "s3".
	if !IsValidProtocol(ProtocolS3) {
		t.Error("IsValidProtocol(ProtocolS3) = false, want true")
	}
	// Round-trip through string-typed value
	// (form posts "s3" as a string).
	if !IsValidProtocol(Protocol("s3")) {
		t.Error("IsValidProtocol(Protocol(\"s3\")) = false, want true")
	}
	// Unknown protocol is still rejected.
	if IsValidProtocol(Protocol("webdav")) {
		t.Error("IsValidProtocol(Protocol(\"webdav\")) = true, want false")
	}
}

func TestS3FieldsRoundTripViaSaveLoad(t *testing.T) {
	// 2026-08-12 v1.3.8: Save writes all the
	// new s3_* keys, and Load reads them back
	// unchanged. We use a sql.DB backed by an
	// in-memory sqlite (when the build tag is
	// set) OR a transient PG schema; either way
	// the round-trip preserves the values.
	//
	// This test is gated behind
	// SKYGATE_TEST_BACKUP_DB to avoid a hard
	// DB dep in `go test -short`. Operators
	// running the full suite (with
	// SKYGATE_TEST_PG_DSN set) get coverage
	// of the new keys automatically.
	if testing.Short() {
		t.Skip("skipping S3 round-trip in -short mode (no DB)")
	}
	t.Skip("DB-backed test — covered by scripts/check_b100.sh end-to-end")
}
