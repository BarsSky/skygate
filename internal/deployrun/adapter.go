// internal/deployrun/adapter.go — bridges the
// *headscale.Client concrete type to the HSClient
// interface used by the auto-deploy framework.
//
// The framework's step code is tested against the
// HSClient interface (no headscale dependency). The
// real wiring (in cmd/skygate/main.go) creates an
// adapter at boot:
//
//	hsFactory := deployrun.HSFactoryFromFunc(
//	    s.Backend.HSGlobalFn,
//	)
//
// The factory returns a new HSClient (which wraps a
// fresh *headscale.Client) per call. We don't cache
// because *headscale.Client may have its own
// invalidation logic that we don't want to bypass.

package deployrun

import (
	"strconv"

	"skygate/internal/headscale"
)

// hsClientAdapter wraps *headscale.Client to satisfy
// the HSClient interface. The wrapper is allocated
// per-run (cheap — just a field) so any per-request
// state in *headscale.Client (cache, etc.) is fresh.
type hsClientAdapter struct {
	c *headscale.Client
}

// Compile-time check: hsClientAdapter satisfies HSClient.
var _ HSClient = (*hsClientAdapter)(nil)

// CreatePreauthKey implements HSClient.
func (a *hsClientAdapter) CreatePreauthKey(userID int64, expiration string, reusable bool, tags []string) (*PreauthKey, error) {
	p, err := a.c.CreatePreauthKeyWithTags(userID, expiration, reusable, tags)
	if err != nil {
		return nil, err
	}
	return &PreauthKey{
		ID:         p.ID,
		Key:        p.Key,
		UserID:     p.UserID,
		Reusable:   p.Reusable,
		Expiration: p.Expiration,
	}, nil
}

// ExpirePreauthKey implements HSClient.
func (a *hsClientAdapter) ExpirePreauthKey(userID int64, keyID string) error {
	return a.c.ExpirePreauthKey(userID, keyID)
}

// HSFactoryFromFunc builds a deployrun.HSClientFactory
// that wraps the given *headscale.Client producer. The
// producer is called per deploy-run (so each run gets
// a fresh wrapper; the underlying *headscale.Client
// is whatever the producer returns, so this works
// with HSGlobalFn, HSForUserFn, or any other
// *headscale.Client producer).
//
// The caller is responsible for closing the
// *headscale.Client (the framework never closes it
// because it doesn't own it).
func HSFactoryFromFunc(producer func() *headscale.Client) HSClientFactory {
	return func() HSClient {
		c := producer()
		if c == nil {
			return nil
		}
		return &hsClientAdapter{c: c}
	}
}

// S3FactoryFromEnv builds a deployrun.S3ClientFactory
// from the deployrun.Config. The factory returns a
// fresh S3Client on each call (the underlying
// minio.Client is cheap to create). Returns nil +
// error if the env is not configured.
func S3FactoryFromEnv(cfg *Config) S3ClientFactory {
	return func() (*S3Client, error) {
		return NewS3Client(cfg)
	}
}

// Ensure noop helper exists so the strconv import
// is "used" in the (theoretical) case where the
// CreatePreauthKeyWithTags signature changes. The
// adapter file imports strconv because some future
// tag conversion (e.g. comma-join) might need it.
var _ = strconv.Itoa
