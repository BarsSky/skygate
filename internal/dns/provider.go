// Package dns — pluggable DNS provider interface for HA failover.
//
// v1.5.0 (B145 / B146) introduces this package. The HA chain
// promotes one node to "active" on failover; the promoted
// node's job is to:
//
//  1. Update the external DNS A-record to point at ITSELF
//     (so skygate.<your-domain> resolves to the new active's
//     public IP).
//  2. Renew the TLS certificate (see internal/certsync/,
//     B147).
//
// The first step is pluggable because not every operator
// uses the configured DNS provider. The 2026-08-18 v1.5.0 plan explicitly notes:
// "у другого администратора может быть не the configured DNS provider и
// необходимо будет учитывать адрес другого провайдера
// предоставляющего домен".
//
// Adding a new provider = implementing the Provider
// interface and registering the case in BuildProvider.
// See docs/internal/ha-v1.5.0-execution.md §"Pluggable DNS
// provider design" for the full rationale.

package dns

import (
	"context"
	"errors"
	"fmt"
)

// Provider is the contract every DNS backend must satisfy.
//
// The interface is intentionally narrow: an HA failover
// needs only "read the current A-record" and "atomically
// write a new A-record" (and a way to test that the auth
// still works). Domain registration, subdomain management,
// record CRUD beyond simple A-records — none of that is
// this package's concern.
//
// Concurrency: the same Provider instance may be called
// concurrently from multiple goroutines (the active node
// running the failover path + the standby doing a
// background "did I get promoted?" sanity check). All
// methods MUST be safe for concurrent use.
type Provider interface {
	// Name returns the provider identifier (e.g. "external",
	// "cloudflare", "route53", "rfc2136"). This is the
	// string operators set SKYGATE_DNS_PROVIDER to. It
	// MUST be stable across releases — admin UIs and
	// /etc/skygate.env configs depend on it.
	Name() string

	// GetRecord fetches the current A-record for `name`
	// in `zone` and returns the IPv4 / IPv6 string.
	// `name` is the bare record (e.g. "skygate", NOT
	// "skygate.<your-domain>") — the FQDN is built by the
	// provider from zone + name.
	//
	// Returns ErrRecordNotFound if the A-record does not
	// exist yet. Any other error is treated as a transient
	// failure (network blip, API rate limit, etc.) and
	// surfaced to the operator via the /admin/ha audit log.
	GetRecord(ctx context.Context, zone, name string) (ip string, err error)

	// UpdateRecord atomically updates the A-record for
	// `name` in `zone` to point at `ip`. "Atomically"
	// means the provider MUST guarantee that a concurrent
	// reader will see either the old value or the new
	// value, never a partial state (e.g. the configured DNS provider's
	// `replace_records` is the canonical example — see
	// the external package's comment for the actual
	// endpoint).
	UpdateRecord(ctx context.Context, zone, name, ip string) error

	// TestConnection verifies the auth credentials work
	// against the provider. The returned error is shown
	// verbatim on the /admin/ha "Test" button — keep it
	// short and operator-friendly.
	TestConnection(ctx context.Context) error
}

// Sentinel errors. Provider implementations should return
// these (or wrap them with fmt.Errorf %w) where appropriate
// so the caller can distinguish "transient" from "permanent".
var (
	// ErrRecordNotFound is returned by GetRecord when the
	// A-record does not exist. The HA failover treats this
	// as a fresh-deploy signal: just create the record at
	// the new active's IP.
	ErrRecordNotFound = errors.New("dns: record not found")

	// ErrUnsupported is returned by GetRecord/UpdateRecord
	// when the provider can't handle a specific record
	// type. v1.5.0 only uses A-records, so this shouldn't
	// fire; it's here for future-proofing.
	ErrUnsupported = errors.New("dns: operation not supported by this provider")
)

// ErrUnknownProvider is returned by BuildProvider when
// SKYGATE_DNS_PROVIDER (or equivalent) is set to a value
// that doesn't match any registered provider.
type ErrUnknownProvider struct {
	Name string
}

func (e ErrUnknownProvider) Error() string {
	return fmt.Sprintf("dns: unknown provider %q (registered: see internal/dns/provider.go)", e.Name)
}
