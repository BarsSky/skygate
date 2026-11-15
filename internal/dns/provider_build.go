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
	"net/http"
	"strings"

	dnsexternal "skygate/internal/dnsexternal"
	extcreds "skygate/internal/ha/dnsexternal"
)

// BuildDeps is the dependency bundle BuildProvider needs.
// Keep it narrow — Provider implementations that need more
// (e.g. an API client for a managed-K8s service) should
// take those dependencies via their own constructor and
// not through this struct.
type BuildDeps struct {
	// DB is the *sql.DB that the provider can use to load
	// its own credentials (e.g. dnsexternal.Store reads
	// from `global_settings`).
	DB *sql.DB
	// SecretKey is SKYGATE_SECRET_KEY (hex). Required by
	// any provider that stores encrypted credentials in
	// the skygate DB; ignored by providers that read
	// credentials from elsewhere (env, file, K8s secret).
	SecretKey string
	// Row is the dns_provider_creds row to build the provider from (B473). Non-nil means "use THESE
	// credentials": the panel and the CLI pass the active row, so a provider is selected by DATA and
	// the five legacy settings keys stay history after the one-time import. Nil keeps the pre-B473
	// behaviour (read the legacy row), which the schedulers still rely on for an install that has
	// never been saved through the panel.
	Row *extcreds.ProviderRow
	// BaseURL overrides the provider's API endpoint (B474). Empty = the provider's production base.
	// The tests point it at an httptest server; nobody else needs it, but a field is cheaper than an
	// env var that only tests can set on the shipped binary.
	BaseURL string
	// HTTPClient overrides the HTTP client (B474). Nil = a bounded default (a 20s timeout).
	HTTPClient *http.Client
}

// BuildProvider returns a Provider matching the given name
// ("external" / "cloudflare" / "route53" / "rfc2136"). The
// `name` argument is the value of SKYGATE_DNS_PROVIDER.
// Empty string → returns nil, nil (no DNS provider
// configured; the HA failover will skip the DNS-update
// step and just log "no DNS provider").
//
// At v1.5.0 (B145) only "external" is implemented (the
// shipped client is for a popular Russian registrar whose
// API uses a common JSON+form+mTLS pattern; the
// implementation lives in internal/dnsexternal and the
// creds store in internal/ha/dnsexternal). The
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
	case "regru", "external":
		// B468 (2026-11-13): "regru" is the honest name for the shipped provider — the client
		// speaks https://api.reg.ru/api/regru2 with a login + ALTERNATIVE password + zone.
		// "external" stays accepted as a historical alias so an install whose SKYGATE_DNS_PROVIDER
		// (or saved credential row) says "external" keeps building the same object.
		//
		// B473: when the caller passes a dns_provider_creds ROW, the client is built from THAT row's
		// fields and never reads the legacy settings keys — that is how a credential row (and later a
		// second provider) reaches the same code path.
		if deps.Row != nil {
			return &externalAdapter{Client: dnsexternal.NewClientWithCreds(dnsexternal.CredentialsFromRow(*deps.Row))}, nil
		}
		if deps.DB == nil {
			return nil, fmt.Errorf("dns: the %q provider requires a non-nil DB", name)
		}
		store := extcreds.NewStore(deps.DB, deps.SecretKey)
		// Wrap the dnsexternal.Client in a thin adapter
		// that translates dnsexternal's internal sentinel
		// (ErrRecordNotFound) into dns.ErrRecordNotFound.
		// We need this translation because dnsexternal
		// can't import internal/dns (would create a
		// cycle — see dnsexternal/client.go package doc).
		return &externalAdapter{Client: dnsexternal.NewClient(store)}, nil
	case "cloudflare":
		// B474: the SECOND provider, and the proof that the B473 seam is real — it authenticates with an
		// API token instead of a login/password pair, and it can do AAAA/TXT/TTL where the reg.ru client
		// cannot. The token comes from the credential ROW (the panel's path) or, for a CLI/bootstrap
		// run before anything is stored, from SKYGATE_CLOUDFLARE_TOKEN.
		var token string
		if deps.Row != nil {
			token = strings.TrimSpace(deps.Row.Secret("token"))
		}
		if token == "" {
			token = cloudflareTokenFromEnv()
		}
		if token == "" {
			return nil, errCloudflareNoToken()
		}
		return newCloudflareProvider(token, deps.BaseURL, deps.HTTPClient), nil
	case "route53", "rfc2136":
		// Reserved for a future B-check. The
		// error message is intentionally specific so
		// the operator knows which B-check to read.
		return nil, fmt.Errorf("dns: provider %q is not implemented yet (see docs/ha.md — the shipped implementations are 'regru' / its alias 'external' (B145 + B468) and 'cloudflare' (B474))", name)
	default:
		return nil, ErrUnknownProvider{Name: name}
	}
}

// externalAdapter wraps dnsexternal.Client to satisfy the
// internal/dns.Provider interface. The only behavioural
// difference from the underlying Client is that the
// adapter translates the package-local errRecordNotFound
// into the public dns.ErrRecordNotFound, so callers don't
// have to know about dnsexternal's internals.
//
// All other methods forward to the underlying Client
// (which is a struct, not an interface, so the adapter
// is a thin pass-through — the indirection is the
// minimum needed to translate the sentinel error).
type externalAdapter struct {
	Client *dnsexternal.Client
}

func (a *externalAdapter) Name() string { return a.Client.Name() }

// Capabilities — B473. reg.ru over our client does A records and TTL (both implemented in
// internal/dnsexternal); AAAA and TXT are NOT implemented there, and saying so is the honest answer
// the panel and the runbook read. Returning "everything true" would make the DNS-01 / AAAA paths
// attempt a call that cannot work.
func (a *externalAdapter) Capabilities() Capabilities { return regruSpec.Caps }

// SetTTL lowers (or raises) the TTL of the A record — the switch runbook's "lower it a day before".
func (a *externalAdapter) SetTTL(ctx context.Context, zone, name string, ttl int) error {
	if !a.Capabilities().TTL {
		return ErrUnsupported
	}
	return a.Client.SetTTL(ctx, zone, name, ttl)
}

// ListRecords is the panel's «Read the zone». It reuses the client's RecordsFor, which already
// returns every record in the zone plus the A record a switch would move.
func (a *externalAdapter) ListRecords(ctx context.Context, zone string) ([]Record, error) {
	all, _, _, err := a.Client.RecordsFor(ctx, zone, "")
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(all))
	for _, r := range all {
		name := strings.TrimSpace(r.Subname)
		if name == "" {
			name = strings.TrimSpace(r.Name)
		}
		typ := strings.TrimSpace(r.Rectype)
		if typ == "" {
			typ = strings.TrimSpace(r.Type)
		}
		out = append(out, Record{Name: name, Type: typ, Content: strings.TrimSpace(r.Content), TTL: r.TTL})
	}
	return out, nil
}

// CreateTXT / DeleteTXT — not implemented by the shipped reg.ru client, and reported as such rather
// than faked. A provider that CAN do DNS-01 (Cloudflare, RFC 2136) implements them for real.
func (a *externalAdapter) CreateTXT(ctx context.Context, zone, name, value string, ttl int) error {
	return ErrUnsupported
}

func (a *externalAdapter) DeleteTXT(ctx context.Context, zone, name, value string) error {
	return ErrUnsupported
}

func (a *externalAdapter) GetRecord(ctx context.Context, zone, name string) (string, error) {
	ip, err := a.Client.GetRecord(ctx, zone, name)
	if errors.Is(err, dnsexternal.ErrRecordNotFound) {
		return "", ErrRecordNotFound
	}
	return ip, err
}

func (a *externalAdapter) UpdateRecord(ctx context.Context, zone, name, ip string) error {
	return a.Client.UpdateRecord(ctx, zone, name, ip)
}

func (a *externalAdapter) TestConnection(ctx context.Context) error {
	return a.Client.TestConnection(ctx)
}
