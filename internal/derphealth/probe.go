package derphealth

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"skygate/internal/derpcfg"
)

// ProbeOne measures the TLS-handshake latency to a single
// DERP server. We do a fresh dial + TLS handshake rather
// than a HEAD/GET over a keepalive connection, because
// (a) we want the cold-start latency (the same path
// Tailscale clients pay when they first connect) and
// (b) HEAD over a fresh connection exercises the same
// path as a Tailscale control plane connection would.
//
// Returns:
//   - latencyMs: milliseconds for the full TCP+TLS handshake,
//     or 0 on failure.
//   - err: non-nil if the dial / handshake failed. The
//     caller persists this in last_error so the dashboard
//     can surface "degraded" DERPs without dropping the
//     last good latency value.
// ProbeOneTLSConfig is the per-probe TLS config override.
// nil = use the system trust store (default for production
// DERP). Tests inject a custom config with the test cert as
// RootCA so the self-signed httptest server verifies.
var ProbeOneTLSConfig *tls.Config

// dialTargetFor splits "where to connect" from "which name to speak" for one
// DERP row (B289.1).
//
// Two live defects made the health dashboard lie about the operator's own relay
// on `skygate-host` (2026-09-23):
//
//   - the probe dialled the bare hostname, forcing :443 even when the row's URL
//     named another port (derp_health is the only probe that validates the
//     certificate against the system trust store, so it must reach the SAME
//     endpoint clients use);
//   - inside the skygate container the relay's own public name resolves to
//     127.0.0.1 (the host's /etc/hosts leaks in — AGENTS trap #2), so the probe
//     hit the container's own loopback and the row read
//     `tls dial: dial tcp 127.0.0.1:443: connect: connection refused` for a
//     perfectly healthy relay.
//
// The address may therefore be the operator's explicit hint
// (SKYGATE_DERP_PROBE_HOST, the same knob the map guard uses); the SNI is ALWAYS
// the row's hostname, because `derper --certmode=manual` resolves its certificate
// by SNI and Go sends no SNI at all for an IP literal.
func dialTargetFor(d DERPInfo) (addr, serverName, port string) {
	host := strings.TrimSpace(d.Host)
	port = "443"
	if u, err := url.Parse(strings.TrimSpace(d.URL)); err == nil && u.Host != "" {
		if p := u.Port(); p != "" {
			port = p
		}
		if h := u.Hostname(); h != "" && host == "" {
			host = h
		}
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		if p != "" {
			port = p
		}
	}
	serverName = host
	addr = host
	// B296: the hint travels on the row (resolved by FetchOwnDERPs via
	// internal/derpcfg: DB override > .env > none). The os.Getenv fallback
	// keeps callers that build a DERPInfo by hand (CLI, tests) working.
	hint := strings.TrimSpace(d.ProbeHost)
	if hint == "" {
		hint = derpcfg.EnvHost()
	}
	if hint != "" && nameResolvesToLoopback(host) {
		addr = hint
	}
	return addr, serverName, port
}

// nameResolvesToLoopback reports whether `host` resolves, from THIS process, only
// to loopback/unspecified addresses.
func nameResolvesToLoopback(host string) bool {
	if host == "" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() && !ip.IsUnspecified() {
			return false
		}
	}
	return true
}

func ProbeOne(ctx context.Context, d DERPInfo, httpClient *http.Client) (int, error) {
	if d.Host == "" {
		return 0, fmt.Errorf("empty host")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: probeTimeout}
	}
	// We want the dial + TLS handshake time specifically,
	// not a full HTTP roundtrip. Build a tls.Dialer that
	// times out and track the elapsed time around Dial.
	//
	// B289.1: the address to dial and the name to present are separate — see
	// dialTargetFor for the live failure that rule comes from.
	dialAddr, serverName, port := dialTargetFor(d)
	addr := net.JoinHostPort(dialAddr, port)
	dialer := &net.Dialer{Timeout: probeTimeout}
	tlsCfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	if ProbeOneTLSConfig != nil {
		// 2026-08-27: B189 pre-existing fix — the old code did
		// `cp := *ProbeOneTLSConfig` (value-copy) which `go vet`
		// flags because tls.Config contains a sync.RWMutex and
		// value-copying a mutex is a known foot-gun (the copied
		// mutex has zero state and unlocks a never-locked lock
		// = undefined behavior). Switched to tls.Config.Clone()
		// (Go 1.8+) which does a proper deep-copy and lets
		// `go vet ./...` pass.
		cp := ProbeOneTLSConfig.Clone()
		if cp.ServerName == "" {
			cp.ServerName = serverName
		}
		// Preserve the caller-requested MinVersion (caller
		// might want a stricter TLS floor than the default
		// tls.VersionTLS12 we just set in tlsCfg).
		if cp.MinVersion != 0 {
			tlsCfg.MinVersion = cp.MinVersion
		}
		// Surface the caller's RootCAs / InsecureSkipVerify /
		// any other field they configured. tlsCfg starts as
		// a fresh {ServerName, MinVersion} config; we merge
		// in everything the caller's clone carried so the
		// probe uses the same trust roots the rest of
		// skygate does.
		cp.ServerName = tlsCfg.ServerName
		cp.MinVersion = tlsCfg.MinVersion
		tlsCfg = cp
	}
	tlsDialer := &tls.Dialer{
		NetDialer: dialer,
		Config:    tlsCfg,
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := tlsDialer.DialContext(cctx, "tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("tls dial: %w", err)
	}
	// Closing here intentionally forces the handshake
	// state to be torn down; we only care about the
	// handshake time, not the full connection.
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		conn.Close()
		return 0, fmt.Errorf("not a tls.Conn")
	}
	// Handshake is normally implicit on first Read/Write,
	// but Dialer.DialContext in Go 1.20+ does NOT call
	// Handshake. Force it so the latency includes the
	// full cert verify.
	if err := tlsConn.HandshakeContext(cctx); err != nil {
		tlsConn.Close()
		return 0, fmt.Errorf("tls handshake: %w", err)
	}
	elapsed := time.Since(start)
	tlsConn.Close()
	return int(elapsed.Milliseconds()), nil
}

// ProbeResult is the per-DERP outcome of one probe. The
// dashboard uses LatencyMs + Healthy + Err.
type ProbeResult struct {
	Info      DERPInfo
	LatencyMs int
	Healthy   bool
	Err       error
}

// ProbeAll probes every DERP in `derps` in parallel and
// persists each result via `persist` (one DB upsert per
// DERP). Bounded by ProbeAllTimeout so a slow set of
// DERPs doesn't block the cron forever.
//
// `derps` is the union list (own + public) from
// FetchAllDERPs. `persist` is a callback the caller wires
// to write the result into derp_health; decoupling lets
// unit tests use a fake without standing up a *sql.DB.
func ProbeAll(ctx context.Context, derps []DERPInfo, httpClient *http.Client,
	persist func(context.Context, DERPInfo, int, bool, error) error) []ProbeResult {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: probeTimeout}
	}
	results := make([]ProbeResult, len(derps))
	var wg sync.WaitGroup
	for i, d := range derps {
		i, d := i, d
		wg.Add(1)
		go func() {
			defer wg.Done()
			lat, err := ProbeOne(ctx, d, httpClient)
			healthy := err == nil
			results[i] = ProbeResult{Info: d, LatencyMs: lat, Healthy: healthy, Err: err}
			if persist != nil {
				_ = persist(ctx, d, lat, healthy, err)
			}
		}()
	}
	wg.Wait()
	return results
}

// PersistToDB is the production persist callback for
// ProbeAll. Wraps the upsertQuery with the result of one
// probe. Failures here are not fatal for the probe run —
// the dashboard will show last_error from a previous
// successful persist.
//
// Exported so the CLI subcommand (cmd/skygate/derp_probe.go)
// can use the same persist path as the cron + dashboard
// handler — keeps the three callers in lockstep.
func PersistToDB(db *sql.DB) func(context.Context, DERPInfo, int, bool, error) error {
	return func(ctx context.Context, d DERPInfo, lat int, healthy bool, probeErr error) error {
		errStr := ""
		if probeErr != nil {
			errStr = probeErr.Error()
		}
		_, err := db.ExecContext(ctx, upsertQuery,
			d.RegionID, boolToInt(d.IsOwn), d.Host, d.URL,
			d.Name, d.RegionCode, d.RegionName, d.Locality, d.Country,
			nullableInt(lat, healthy),
			time.Now().Unix(),
			boolToInt(healthy),
			errStr,
			0, 0, // probes_total / probes_failed are handled by SQL
		)
		return err
	}
}

// boolToInt is a tiny helper to convert bool to the
// 0/1 int form the schema uses for INTEGER NOT NULL.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullableInt returns nil (NULL) when the probe failed
// (so the latency column is NULL on failure) and the
// integer latency otherwise. SQLite + pgx both handle
// the nil interface as NULL.
func nullableInt(lat int, healthy bool) interface{} {
	if !healthy {
		return nil
	}
	return lat
}
