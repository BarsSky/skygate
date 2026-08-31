// internal/deployrun/s3client.go — minimal S3 PUT client
// for the auto-deploy framework's step 4 (push .env to
// S3 at ha/deploy/<hostname>/.env).
//
// Why a local client: internal/backup has its own
// UploadToS3 function but it's unexported and tied to
// the backup runner's staging-dir flow. B194's step
// just needs a simple PutObject — a thin wrapper
// around minio-go is enough.

package deployrun

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Client is a minimal S3-compatible client for the
// auto-deploy framework. Uses MinIO's Go SDK (same
// library internal/backup uses) so we can talk to
// MinIO, AWS S3, Backblaze B2, etc. uniformly.
type S3Client struct {
	client   *minio.Client
	bucket   string
	endpoint  string
	hasCreds bool
}

// NewS3Client creates an S3 client from a Config.
// Returns nil if the config has no credentials (the
// framework marks the push step as skipped with a
// clear hint in that case — see steps/push_env_s3.go).
func NewS3Client(cfg *Config) (*S3Client, error) {
	if cfg.S3Endpoint == "" || cfg.S3AccessKey == "" || cfg.S3SecretKey == "" || cfg.S3Bucket == "" {
		return nil, fmt.Errorf("S3 not configured (need S3Endpoint, S3AccessKey, S3SecretKey, S3Bucket)")
	}
	useSSL := strings.HasPrefix(cfg.S3Endpoint, "https://")
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.S3Endpoint, "https://"), "http://")
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio.New endpoint=%s: %w", endpoint, err)
	}
	return &S3Client{client: cli, bucket: cfg.S3Bucket, endpoint: cfg.S3Endpoint, hasCreds: true}, nil
}

// PutObject uploads a single object. Returns the
// ETag on success. The reader is fully consumed.
func (c *S3Client) PutObject(ctx context.Context, key string, body io.Reader, size int64, contentType string) (string, error) {
	if !c.hasCreds {
		return "", fmt.Errorf("S3 not configured")
	}
	info, err := c.client.PutObject(ctx, c.bucket, key, body, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("PutObject bucket=%s key=%s: %w", c.bucket, key, err)
	}
	return info.ETag, nil
}

// DeleteObject removes an object. Used by Rollback.
func (c *S3Client) DeleteObject(ctx context.Context, key string) error {
	if !c.hasCreds {
		return nil
	}
	return c.client.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
}
