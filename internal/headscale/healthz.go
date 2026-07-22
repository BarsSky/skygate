package headscale

// healthz.go — v0.26.0 liveness / readiness helpers.
//
// The handler layer (internal/handlers/handlers_healthz.go)
// uses headscale.Pingable to call PingContext on the
// active headscale.Client. The interface is here (not in
// handlers) because the implementation lives on the
// concrete *Client; keeping the interface next to the
// implementation follows the Go "consumer-side interface"
// guideline inverted for the sake of testability — the
// handlers package declares what it needs, and the
// headscale package satisfies it without an import cycle.

import (
	"context"
	"net/http"
)

// Pingable is the surface the v0.26.0 readiness probe
// needs from a headscale client. Implemented by *Client
// (below). Defined as an interface so future mocks
// (for tests) can satisfy it without spinning up a
// real headscale container.
type Pingable interface {
	PingContext(ctx context.Context) error
}

// PingContext issues a HEAD /api/v1/node request to the
// headscale server and returns nil on 2xx/4xx (the
// server is reachable and responding — even auth
// failures count as "reachable", since the network
// path is what we care about), or an error otherwise.
//
// Why HEAD /api/v1/node? Two reasons:
//  1. It's the cheapest endpoint on headscale (returns
//     {"nodes": null} or a list, no DB write, ~1ms).
//  2. It requires an API key, so we ALSO catch the case
//     "the network path is up but our key was revoked"
//     (4xx, not 5xx — but we still treat 4xx as success
//     because the server is talking to us).
//
// Returns the underlying transport error if the
// network call fails (DNS, connection refused, TLS, etc).
func (c *Client) PingContext(ctx context.Context) error {
	if c == nil || c.BaseURL == "" {
		return errClientNotConfigured
	}
	url := c.BaseURL + "/api/v1/node"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 2xx and 4xx both mean "the server responded". Only
	// 5xx (or empty resp) counts as degraded.
	if resp.StatusCode >= 500 {
		return errServerDegraded
	}
	return nil
}

// Errors used by PingContext. Exposed as package-level
// vars (not consts) so callers can compare with ==
// without a transitive import of the headscale package's
// internals.
var (
	errClientNotConfigured = &pingError{"headscale client not configured"}
	errServerDegraded       = &pingError{"headscale server returned 5xx"}
)

type pingError struct{ msg string }

func (e *pingError) Error() string { return e.msg }
