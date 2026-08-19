// Package certsync — S3 adapter.
//
// Bridges the S3Client interface (declared in
// certsync.go) to the production minio.Client used by
// the rest of skygate (internal/backup/s3.go exposes
// NewS3ClientForConfig which returns a *minio.Client).
// Keeping the interface in the package (not in main.go)
// lets the unit test substitute a fake without touching
// the rest of the codebase.

package certsync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
)

// MinioS3Client adapts a *minio.Client to the S3Client
// interface. The adapter is intentionally thin —
// HeadObject maps to StatObject (size + ETag +
// LastModified), GetObject reads the full body into a
// byte slice (the certs are small — <10KB — so an
// in-memory buffer is fine; the S3 client also supports
// streaming via the body closer, but for cert content
// the simplicity wins), PutObject is the production-
// side helper (unused by the read-side scheduler but
// exposed so the operator-side renewal script can use
// the same interface in tests).
type MinioS3Client struct {
	cli *minio.Client
}

// NewMinioS3Client returns an S3Client adapter wrapping
// the given minio.Client. Returns an error if cli is
// nil (defensive — the production caller always passes
// a real client, but the test might pass nil for
// negative-path coverage).
func NewMinioS3Client(cli *minio.Client) (S3Client, error) {
	if cli == nil {
		return nil, errors.New("NewMinioS3Client: nil minio.Client")
	}
	return &MinioS3Client{cli: cli}, nil
}

// HeadObject calls minio's StatObject. Returns an
// S3ObjectMeta on success, or an error if the object
// doesn't exist (the "no certs uploaded yet" case).
func (m *MinioS3Client) HeadObject(ctx context.Context, bucket, key string) (S3ObjectMeta, error) {
	stat, err := m.cli.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return S3ObjectMeta{}, err
	}
	return S3ObjectMeta{
		ETag:         strings.Trim(stat.ETag, `"`),
		Size:         stat.Size,
		LastModified: stat.LastModified,
	}, nil
}

// GetObject reads the full object body into a byte
// slice. The cert files are small (PEM <10KB) so an
// in-memory buffer is fine; this also keeps the
// scheduler's logic simple (no streaming / partial-
// read handling).
func (m *MinioS3Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	obj, err := m.cli.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	// io.ReadAll handles the 404 case automatically —
	// the minio client returns an error from
	// obj.Read() if the object is missing, which
	// bubbles up here.
	return io.ReadAll(obj)
}

// PutObject uploads a key's body. Used by the
// operator-side cert-renew script's test (not by
// the in-app scheduler). The minio client uses
// io.Reader; we wrap the byte slice in a bytes.Reader.
func (m *MinioS3Client) PutObject(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	reader := bytes.NewReader(body)
	_, err := m.cli.PutObject(ctx, bucket, key, reader, int64(len(body)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}
