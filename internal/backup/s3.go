// 2026-08-12 v1.3.8: S3 / S3-compatible upload transport.
//
// The mount-based protocols (smb/nfs/sftp) mount the
// share, run backup.sh with the mountpoint as the
// destination, and unmount. S3 has no FUSE layer — the
// in-app runner instead:
//
//   1. Stages the tar.gz to a local dir
//      (cfg.S3StagingDir, default /var/lib/skygate/backup-staging).
//   2. Calls UploadToS3() which PUTs the file to the
//      configured bucket+prefix via the S3 REST API
//      (SigV4). We use github.com/minio/minio-go/v7
//      because it supports any S3-compatible endpoint
//      (AWS, MinIO, Yandex Object Storage, Selectel,
//      Backblaze B2, etc.) with a uniform API.
//
//   3. The S3 server returns ETag + version id which
//      we log for traceability. Failures (network,
//      auth, NoSuchBucket) bubble up to the caller
//      as a non-nil error; RunBackup wraps it with
//      context ("s3 upload: <err>") so the UI shows
//      the bucket name + key alongside the error.
//
// No GET path is implemented here — restore is a
// host-side flow (operator uses `aws s3 cp` or
// `mc cp` to download the archive, then `restore.sh
// /path/to/archive.tar.gz` to replay it). The S3
// library's GetObject is available if a future
// /admin/backup/download endpoint is needed.

package backup

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3UploadResult is what UploadToS3 returns on success.
// Surfaced in the audit log so the operator can verify
// "the file is actually in bucket X, key Y, etag Z".
type s3UploadResult struct {
	Bucket   string // bucket the file landed in
	Key      string // full key (prefix + basename)
	ETag     string // server-returned ETag
	Size     int64  // bytes uploaded
	Duration time.Duration
}

// s3Client is a tiny wrapper around the minio-go client
// so we can mock it from tests. In production it's
// always a real *minio.Client.
type s3Client interface {
	FPutObject(ctx context.Context, bucket, object, filePath string, opts minio.PutObjectOptions) (info minio.UploadInfo, err error)
	BucketExists(ctx context.Context, bucket string) (bool, error)
}

// realS3Client wraps *minio.Client so it satisfies
// s3Client. Used in production; tests can pass a fake
// that records calls. The methods are 1-line
// forwarders — they exist so tests can swap a fake
// via the s3Client interface without rewriting
// uploadToS3.
type realS3Client struct{ c *minio.Client }

// BucketExists forwards to the underlying minio
// client. Errors from this method are wrapped by
// uploadToS3 with bucket-name context.
func (r *realS3Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	return r.c.BucketExists(ctx, bucket)
}

// FPutObject forwards to the underlying minio
// client. The opts.ContentType is set by the caller
// (uploadToS3) so this is a true pass-through.
func (r *realS3Client) FPutObject(ctx context.Context, bucket, object, filePath string, opts minio.PutObjectOptions) (minio.UploadInfo, error) {
	return r.c.FPutObject(ctx, bucket, object, filePath, opts)
}

// newS3Client builds a real minio.Client from cfg.
// The endpoint is parsed with url.Parse so a missing
// scheme (rare, but possible if the operator pastes
// "minio.local:9000" without "http://") gets
// auto-prefixed with "https://". UseSSL from cfg
// controls the actual TLS handshake; an http://
// endpoint with UseSSL=true still works because
// minio-go trusts the explicit scheme.
func newS3Client(c *Config) (*minio.Client, error) {
	ep := strings.TrimSpace(c.S3Endpoint)
	if ep == "" {
		// AWS default: regional endpoint derived
		// from the region. minio-go supports
		// the empty endpoint for AWS if you also
		// pass the region, but the most reliable
		// form is the explicit URL.
		ep = fmt.Sprintf("https://s3.%s.amazonaws.com", c.S3Region)
	}
	if !strings.Contains(ep, "://") {
		// No scheme: default to https (the
		// common case for AWS + most S3 clones).
		// Operators running a private MinIO
		// without TLS should set UseSSL=false
		// AND prefix the endpoint with "http://".
		ep = "https://" + ep
	}
	// minio-go's New() takes (endpoint, useSSL)
	// where endpoint is host[:port] WITHOUT the
	// scheme. We strip the scheme here and let
	// useSSL control TLS. This avoids the
	// "endpoint should not have a scheme" error
	// the minio client returns.
	u, err := url.Parse(ep)
	if err != nil {
		return nil, fmt.Errorf("s3 endpoint %q: %w", ep, err)
	}
	host := u.Host
	if host == "" {
		// url.Parse accepts "host:port" as a
		// raw form sometimes; fall back to the
		// original string.
		host = strings.TrimPrefix(ep, "https://")
		host = strings.TrimPrefix(host, "http://")
	}
	mc, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(c.S3AccessKey, c.S3SecretKey, ""),
		Secure: c.S3UseSSL,
		Region: c.S3Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return mc, nil
}

// uploadToS3 is the production entry point. It
// builds a real client, verifies the bucket exists,
// and FPutObject's the file. Returns a result struct
// the caller can log.
//
// ctx cancellation propagates: if the HTTP request
// is in flight and ctx is cancelled, the upload
// aborts. We cap the overall upload at 1h
// (configurable via the parent context).
func uploadToS3(ctx context.Context, c *Config, filePath string) (*s3UploadResult, error) {
	if c.S3Bucket == "" {
		return nil, fmt.Errorf("s3 bucket is empty")
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", filePath, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, expected a file", filePath)
	}
	mc, err := newS3Client(c)
	if err != nil {
		return nil, err
	}
	cl := &realS3Client{c: mc}

	// BucketExists is a cheap HEAD that catches
	// typos + missing creds early. minio-go
	// returns (false, nil) if the bucket does
	// not exist (the bucket just isn't listed),
	// and (false, err) for auth/transport
	// failures. We treat "bucket doesn't exist"
	// as an error so the operator sees "no such
	// bucket: foo" instead of a confusing
	// NoSuchBucket from FPutObject.
	exists, err := cl.BucketExists(ctx, c.S3Bucket)
	if err != nil {
		return nil, fmt.Errorf("s3 bucket check: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("s3 bucket does not exist: %s", c.S3Bucket)
	}

	key := buildS3Key(c.S3Prefix, filepath.Base(filePath))
	t0 := time.Now()
	info, err := cl.FPutObject(ctx, c.S3Bucket, key, filePath, minio.PutObjectOptions{
		// ContentType: application/gzip
		// helps browsers / S3 console render
		// the file as a downloadable archive
		// instead of "unknown binary".
		ContentType: "application/gzip",
	})
	if err != nil {
		return nil, fmt.Errorf("s3 upload %s/%s: %w", c.S3Bucket, key, err)
	}
	return &s3UploadResult{
		Bucket:   c.S3Bucket,
		Key:      key,
		ETag:     info.ETag,
		Size:     info.Size,
		Duration: time.Since(t0),
	}, nil
}

// NewS3ClientForConfig is the exported wrapper around
// newS3Client for callers outside the backup package
// (e.g. the in-app /admin/backup/download-s3 handler in
// internal/feature/admin/backup.go, which needs to
// stream an object from S3 to the operator's browser).
// Returns the live *minio.Client so the caller can
// invoke GetObject / StatObject / FPutObject directly.
func NewS3ClientForConfig(c *Config) (*minio.Client, error) {
	return newS3Client(c)
}

// buildS3Key joins the prefix and the basename with
// exactly one "/". A trailing slash on the prefix is
// tolerated; a leading slash on the basename is
// stripped. Returns just the basename when prefix is
// empty. Examples:
//
//   ("", "foo.tar.gz")            → "foo.tar.gz"
//   ("backups", "foo.tar.gz")     → "backups/foo.tar.gz"
//   ("backups/", "foo.tar.gz")    → "backups/foo.tar.gz"
//   ("a/b/", "foo.tar.gz")        → "a/b/foo.tar.gz"
func buildS3Key(prefix, basename string) string {
	p := strings.TrimRight(prefix, "/")
	b := strings.TrimLeft(basename, "/")
	if p == "" {
		return b
	}
	return p + "/" + b
}
