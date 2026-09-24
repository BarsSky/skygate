// Package derpmetricsproxy — the host-side loopback bridge that makes derper's
// metrics readable from the skygate container (B315).
//
// # THE PROBLEM IT SOLVES
//
// derper serves its rich metrics on `/debug/vars` (STUN counters, accepts,
// bytes, clients, connections) and serves its debug HTML on `/debug/`. Upstream
// `tsweb.AllowDebugAccess` admits **loopback and Tailscale sources only**, and
// answers everything else with `403 debug access denied`. The skygate container
// is neither, and no address it dials changes that: the verdict is made on the
// request's SOURCE IP.
//
// Measured live on the agent VM (2026-09-23):
//
//	host loopback  GET https://127.0.0.1:443/debug/vars   → 200, 6.3 KB JSON
//	from container GET https://<relay>:443/debug/vars     → 403 debug access denied
//
// The consequence was the worst kind: /admin/derp drew `0` in every traffic tile
// (values that look like measurements) and hung an unexplained warning next to
// them, so the operator could neither trust the page nor act on it.
//
// # WHAT THIS IS
//
// A deliberately tiny, path-allow-listed reverse proxy. It runs ON THE HOST,
// connects to the relay's loopback (`127.0.0.1:443`, where derper runs), and
// re-serves five debug paths to the docker bridge:
//
//	skygate container ──plain HTTP──▶ proxy (host) ──TLS──▶ derper 127.0.0.1:443
//
// Because the second hop starts from the host's own loopback it is ADMITTED, so
// the container finally sees real numbers. Nothing is opened on the relay: the
// allow-list is a closed set of read-only debug paths, and the client allow-list
// refuses anything that is not a private/loopback source, so binding on
// 0.0.0.0 does not publish the relay's debug surface.
//
// It ships inside the binary the operator already deploys, so there is no new
// dependency, no new image and no compose edit:
//
//	skygate derp-metrics-proxy --listen 172.18.0.1:8767 \
//	    --upstream 127.0.0.1:443 --server-name derp.example.com --insecure
//
// or, on a docker install, as a throwaway container from the same image:
//
//	docker run -d --name skygate-derp-metrics --restart unless-stopped \
//	    --network host ghcr.io/barssky/skygate:vX.Y.Z derp-metrics-proxy \
//	    --listen 172.18.0.1:8767 --upstream 127.0.0.1:443 \
//	    --server-name derp.example.com --insecure
package derpmetricsproxy

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AllowedPaths is the closed set of relay debug paths this proxy will forward.
//
// WHY AN ALLOW-LIST AND NOT A PREFIX MATCH: the upstream is derper's own TLS
// listener, which also carries the DERP protocol and any future handler. A
// generic forwarder would republish all of it on a plain-HTTP bridge port; this
// list is exactly what /admin/derp reads, and `/debug/` is listed separately
// from `/debug/vars` because both are real endpoints with different bodies (HTML
// vs JSON).
var AllowedPaths = []string{
	"/debug",
	"/debug/",
	"/debug/vars",
	"/debug/vars/",
	"/active-conn",
	"/all-recent",
}

// PathAllowed reports whether the proxy forwards `p` upstream.
func PathAllowed(p string) bool {
	for _, a := range AllowedPaths {
		if p == a {
			return true
		}
	}
	return false
}

// DefaultAllowedNets is the client allow-list used when the operator passes no
// --allow-net: loopback, RFC 1918, link-local, CGNAT/Tailscale and IPv6 ULA.
// A public source is refused even if the proxy is bound to 0.0.0.0.
var DefaultAllowedNets = []string{
	"127.0.0.0/8", "::1/128", // loopback
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", // RFC 1918 (docker bridges live in 172.16/12)
	"169.254.0.0/16", "fe80::/10", // link-local
	"100.64.0.0/10", "fd7a:115c:a1e0::/48", // CGNAT + Tailscale
	"fc00::/7", // IPv6 ULA
}

// Config is the proxy's runtime configuration.
type Config struct {
	// Listen is the TCP address the bridge is served on (required).
	Listen string
	// Upstream is the relay's loopback TLS endpoint (default 127.0.0.1:443).
	Upstream string
	// ServerName is the TLS name to verify/send as SNI. Empty means "use the
	// upstream's host", which is only useful when it is a name (or when the
	// certificate carries an IP SAN).
	ServerName string
	// Insecure skips upstream certificate verification. Legitimate here and only
	// here: the upstream is the same host's loopback, so there is no network to
	// man-in-the-middle — the operator uses it for a relay cert that is
	// name-based while the proxy dials 127.0.0.1.
	Insecure bool
	// AllowNets are the CIDRs allowed as CLIENT sources. Empty = DefaultAllowedNets.
	AllowNets []string
	// Timeout bounds one upstream round trip (default 10s).
	Timeout time.Duration
}

// ParseArgs parses the `skygate derp-metrics-proxy` command line.
//
// It is separated from Run so the flag surface is unit-testable without opening
// a socket (and so the caller can print the help text).
func ParseArgs(args []string) (Config, error) {
	var cfg Config
	var allow multiFlag
	fs := flag.NewFlagSet("derp-metrics-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the caller owns the output
	fs.StringVar(&cfg.Listen, "listen", "", "address to serve the metrics bridge on, e.g. 172.18.0.1:8767 (required)")
	fs.StringVar(&cfg.Upstream, "upstream", "127.0.0.1:443", "the relay's loopback TLS endpoint")
	fs.StringVar(&cfg.ServerName, "server-name", "", "TLS name of the relay certificate (SNI + verification); default: the upstream host")
	fs.BoolVar(&cfg.Insecure, "insecure", false, "do not verify the relay certificate (loopback hop only)")
	fs.Var(&allow, "allow-net", "CIDR allowed as a client source (repeatable); default: loopback + private ranges")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Second, "upstream round-trip timeout")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if strings.TrimSpace(cfg.Listen) == "" {
		return cfg, fmt.Errorf("--listen is required (e.g. --listen 172.18.0.1:8767)")
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return cfg, fmt.Errorf("--listen %q is not host:port: %w", cfg.Listen, err)
	}
	if strings.TrimSpace(cfg.Upstream) == "" {
		return cfg, fmt.Errorf("--upstream must not be empty")
	}
	if _, _, err := net.SplitHostPort(cfg.Upstream); err != nil {
		return cfg, fmt.Errorf("--upstream %q is not host:port: %w", cfg.Upstream, err)
	}
	cfg.AllowNets = allow
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return cfg, nil
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// Usage is the operator-facing help text.
var Usage = `skygate derp-metrics-proxy — serve derper's loopback-only debug endpoints to the skygate container.

  --listen <host:port>    address to serve on (required), e.g. 172.18.0.1:8767
  --upstream <host:port>  the relay's loopback TLS endpoint (default 127.0.0.1:443)
  --server-name <name>    the relay certificate's name (SNI + verification)
  --insecure              do not verify the relay certificate (loopback hop only)
  --allow-net <CIDR>      client source allow-list entry (repeatable)
  --timeout <duration>    upstream round-trip timeout (default 10s)

Forwarded paths (read-only): ` + strings.Join(AllowedPaths, ", ") + `
Then set the endpoint on /admin/derp (or SKYGATE_DERP_DEBUG_URL) to http://<listen>.
`

// Run serves the bridge until ctx is cancelled. It returns nil on a clean
// shutdown and a non-nil error if the listener never came up (the caller reports
// that as a failed start, so systemd/docker restarts it visibly).
func Run(ctx context.Context, cfg Config) error {
	client, err := newUpstreamClient(cfg)
	if err != nil {
		return err
	}
	allow, err := parseNets(cfg.AllowNets)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handle(cfg, client, allow, w, r)
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	log.Printf("derp-metrics-proxy: serving %s -> https://%s (SNI %q, insecure=%t, %d path(s))",
		cfg.Listen, cfg.Upstream, tlsName(cfg), cfg.Insecure, len(AllowedPaths))
	if !cfg.Insecure && net.ParseIP(hostOnly(cfg.Upstream)) != nil && cfg.ServerName == "" {
		log.Printf("derp-metrics-proxy: WARNING: upstream %s is an IP and no --server-name was given; "+
			"a name-based relay certificate cannot be verified — add --server-name <relay hostname> --insecure", cfg.Upstream)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		case <-done:
		}
	}()
	err = srv.Serve(ln)
	close(done)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func handle(cfg Config, client *http.Client, allow []*net.IPNet, w http.ResponseWriter, r *http.Request) {
	if !PathAllowed(r.URL.Path) {
		http.Error(w, "metrics proxy: path not forwarded: "+r.URL.Path, http.StatusNotFound)
		return
	}
	ip := clientIP(r)
	if !ipAllowed(ip, allow) {
		http.Error(w, fmt.Sprintf("metrics proxy: source %s is not in the client allow-list", ip), http.StatusForbidden)
		return
	}
	target := "https://" + cfg.Upstream + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", target, nil)
	if err != nil {
		http.Error(w, "metrics proxy: build request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Host = tlsName(cfg)
	resp, err := client.Do(req)
	if err != nil {
		// The body is surfaced verbatim by /admin/derp as the reason metrics are
		// unavailable, so it names the fix instead of only the symptom.
		http.Error(w, fmt.Sprintf("metrics proxy: upstream https://%s%s failed: %v%s",
			cfg.Upstream, r.URL.Path, err, upstreamHint(cfg, err)), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if rerr != nil {
		http.Error(w, "metrics proxy: read upstream body: "+rerr.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("X-Skygate-Metrics-Via", cfg.Upstream)
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	log.Printf("derp-metrics-proxy: %s %s <- %s %d (%d bytes)", ip, r.URL.Path, cfg.Upstream, resp.StatusCode, len(body))
}

// upstreamHint names the two certificate knobs when the failure looks like a TLS
// verification error — the single most likely first-run failure.
func upstreamHint(cfg Config, err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "x509") || strings.Contains(s, "certificate") || strings.Contains(s, "tls:"):
		return fmt.Sprintf(" — the relay certificate did not verify for name %q; pass --server-name <relay hostname> and, "+
			"for a cert you cannot verify over loopback, --insecure", tlsName(cfg))
	case strings.Contains(s, "connection refused"):
		return " — nothing is listening on the upstream; is the relay running, and is --upstream the right loopback port?"
	}
	return ""
}

// newUpstreamClient builds the HTTPS client used for every upstream hop.
func newUpstreamClient(cfg Config) (*http.Client, error) {
	name := tlsName(cfg)
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			ServerName:         name,
			InsecureSkipVerify: cfg.Insecure,
			MinVersion:         tls.VersionTLS12,
		},
		DialContext:           (&net.Dialer{Timeout: cfg.Timeout}).DialContext,
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{Timeout: cfg.Timeout, Transport: tr}, nil
}

// tlsName is the name put in SNI (and verified): --server-name when set,
// otherwise the upstream's host.
func tlsName(cfg Config) string {
	if s := strings.TrimSpace(cfg.ServerName); s != "" {
		return s
	}
	return hostOnly(cfg.Upstream)
}

// hostOnly strips an optional :port and IPv6 brackets from an address.
func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return strings.Trim(addr, "[]")
}

// parseNets compiles a CIDR list; empty falls back to DefaultAllowedNets. A bare
// address is accepted and treated as a /32 or /128.
func parseNets(raw []string) ([]*net.IPNet, error) {
	if len(raw) == 0 {
		raw = DefaultAllowedNets
	}
	out := make([]*net.IPNet, 0, len(raw))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			ip := net.ParseIP(c)
			if ip == nil {
				return nil, fmt.Errorf("--allow-net %q is neither a CIDR nor an address", c)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			c = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("--allow-net %q: %w", c, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// ipAllowed reports whether a client address is inside the allow-list.
func ipAllowed(ip net.IP, allow []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP extracts the connecting address from a RemoteAddr. Deliberately does
// NOT honour X-Forwarded-For: a header is attacker-controlled and this list is a
// security boundary.
func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

// MetricsEndpointFromListen builds the URL the operator should paste into
// /admin/derp for a proxy listening on `listen`. Exported so the UI card and the
// tests agree on one shape.
func MetricsEndpointFromListen(listen string) string {
	u := url.URL{Scheme: "http", Host: listen}
	return u.String()
}
