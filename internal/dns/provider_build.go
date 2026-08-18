// provider_build.go — the BuildProvider factory + supporting
// types. Kept in a separate file from provider.go so
// provider.go can stay free of database/sql (and any other
// "build-time" dependencies) — this lets future callers
// import internal/dns with only the Provider interface.

package dns

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	regapicreds "skygate/internal/ha/regapi"
	dnsregapi "skygate/internal/dnsregapi"
)

// BuildDeps is the dependency bundle BuildProvider needs.
// Keep it narrow — Provider implementations that need more
// (e.g. an API client for a managed-K8s service) should
// take those dependencies via their own constructor and
// not through this struct.
type BuildDeps struct {
	// DB is the *sql.DB that the provider can use to load
	// its own credentials (e.g. regapi.Store reads from
	// `global_settings`).
	DB *sql.DB
	// SecretKey is SKYGATE_SECRET_KEY (hex). Required by
	// any provider that stores encrypted credentials in
	// the skygate DB; ignored by providers that read
	// credentials from elsewhere (env, file, K8s secret).
	SecretKey string
}

// BuildProvider returns a Provider matching the given name
// ("regapi" / "cloudflare" / "route53" / "rfc2136"). The
// `name` argument is the value of SKYGATE_DNS_PROVIDER.
// Empty string → returns nil, nil (no DNS provider
// configured; the HA failover will skip the DNS-update
// step and just log "no DNS provider").
//
// At v1.5.0 (B145) only "regapi" is implemented. The
// other names return ErrUnknownProvider until a future
// B-check lands the corresponding implementation.
//
// To add a new provider: implement Provider, add a case
// here, document the env vars in config.go and the
// admin-managed creds path in /admin/ha (Phase 5).
func BuildProvider(name string, deps BuildDeps) (Provider, error) {
	switch name {
	case "":
		// Operator didn't pick a provider. Not an
		// error — /admin/ha just hides the "Test DNS"
		// button and the HA failover logs "no DNS
		// provider configured" once at startup.
		return nil, nil
	case "regapi":
		if deps.DB == nil {
			return nil, fmt.Errorf("dns: regapi requires a non-nil DB")
		}
		store := regapicreds.NewStore(deps.DB, deps.SecretKey)
		// Wrap the dnsregapi.Client in a thin adapter
		// that translates dnsregapi's internal sentinel
		// (errRecordNotFound) into dns.ErrRecordNotFound.
		// We need this translation because dnsregapi
		// can't import internal/dns (would create a
		// cycle — see dnsregapi/client.go package doc).
		return &regapiAdapter{Client: dnsregapi.NewClient(store)}, nil
	case "cloudflare", "route53", "rfc2136":
		// Reserved for future B-checks (B146+). The
		// error message is intentionally specific so
		// the operator knows which B-check to read.
		return nil, fmt.Errorf("dns: provider %q is not implemented yet (see docs/internal/ha-v1.5.0-execution.md — only regapi is shipped in v1.5.0 B145)", name)
	default:
		return nil, ErrUnknownProvider{Name: name}
	}
}

// regapiAdapter wraps dnsregapi.Client to satisfy the
// internal/dns.Provider interface. The only behavioural
// difference from the underlying Client is that the
// adapter translates the package-local errRecordNotFound
// into the public dns.ErrRecordNotFound, so callers don't
// have to know about dnsregapi's internals.
//
// All other methods forward to the underlying Client
// (which is a struct, not an interface, so the adapter
// is a thin pass-through — the indirection is the
// minimum needed to translate the sentinel error).
type regapiAdapter struct {
	Client *dnsregapi.Client
}

func (a *regapiAdapter) Name() string { return a.Client.Name() }

func (a *regapiAdapter) GetRecord(ctx context.Context, zone, name string) (string, error) {
	ip, err := a.Client.GetRecord(ctx, zone, name)
	if errors.Is(err, dnsregapi.ErrRecordNotFound) {
		return "", ErrRecordNotFound
	}
	return ip, err
}

func (a *regapiAdapter) UpdateRecord(ctx context.Context, zone, name, ip string) error {
	return a.Client.UpdateRecord(ctx, zone, name, ip)
}

func (a *regapiAdapter) TestConnection(ctx context.Context) error {
	return a.Client.TestConnection(ctx)
}
