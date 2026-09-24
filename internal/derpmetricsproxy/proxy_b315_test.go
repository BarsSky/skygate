package derpmetricsproxy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestB315_PathAllowListIsClosed is the security half of the block: the bridge
// fronts the relay's own TLS listener, which also carries the DERP protocol, so
// "forward everything" would republish the whole relay on a plain-HTTP port. The
// list is exactly what /admin/derp reads and nothing else.
func TestB315_PathAllowListIsClosed(t *testing.T) {
	want := map[string]bool{
		"/debug":       true,
		"/debug/":      true,
		"/debug/vars":  true,
		"/debug/vars/": true,
		"/active-conn": true,
		"/all-recent":  true,
	}
	for _, p := range AllowedPaths {
		if !want[p] {
			t.Fatalf("AllowedPaths contains an unexpected entry %q", p)
		}
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("AllowedPaths is missing %v", want)
	}
	for _, p := range []string{"/", "/derp", "/debug/pprof/heap", "/debug/vars/../derp", "/debugvars", "/active-conn/extra"} {
		if PathAllowed(p) {
			t.Fatalf("PathAllowed(%q) = true — the allow-list must be a closed set, not a prefix match", p)
		}
	}
}

// TestB315_ClientAllowListRefusesPublicSources pins that a public source address
// is refused even though the default allow-list is generous to private ranges:
// this is the difference between "the operator bound 0.0.0.0" and "the relay's
// debug surface is on the internet".
func TestB315_ClientAllowListRefusesPublicSources(t *testing.T) {
	nets, err := parseNets(nil)
	if err != nil {
		t.Fatalf("parseNets(default): %v", err)
	}
	for _, addr := range []string{"127.0.0.1", "::1", "172.18.0.3", "192.168.1.5", "10.1.2.3", "100.64.0.7"} {
		if !ipAllowed(net.ParseIP(addr), nets) {
			t.Fatalf("ipAllowed(%s) = false, want true", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8", "203.0.113.9", "2001:db8::1"} {
		if ipAllowed(net.ParseIP(addr), nets) {
			t.Fatalf("ipAllowed(%s) = true — a public source must be refused", addr)
		}
	}
	if ipAllowed(nil, nets) {
		t.Fatal("ipAllowed(nil) = true")
	}
	// A bare address is accepted as a /32 by parseNets.
	one, err := parseNets([]string{"192.0.2.9"})
	if err != nil {
		t.Fatalf("parseNets(single): %v", err)
	}
	if !ipAllowed(net.ParseIP("192.0.2.9"), one) || ipAllowed(net.ParseIP("192.0.2.10"), one) {
		t.Fatal("a bare address must behave as a single-host allow-list entry")
	}
	if _, err := parseNets([]string{"not-a-net"}); err == nil {
		t.Fatal("parseNets(not-a-net): expected an error")
	}
}

// TestB315_ParseArgsRequiresListen pins the flag surface: --listen is mandatory
// (a bridge with no listener is a typo, not a default), the upstream/port shapes
// are validated, and the timeout has a usable default.
func TestB315_ParseArgsRequiresListen(t *testing.T) {
	if _, err := ParseArgs(nil); err == nil {
		t.Fatal("ParseArgs(nil): expected --listen to be required")
	}
	if _, err := ParseArgs([]string{"--listen", "172.18.0.1"}); err == nil {
		t.Fatal("ParseArgs: a --listen without a port must be refused")
	}
	if _, err := ParseArgs([]string{"--listen", "172.18.0.1:8767", "--upstream", "127.0.0.1"}); err == nil {
		t.Fatal("ParseArgs: an --upstream without a port must be refused")
	}
	cfg, err := ParseArgs([]string{"--listen", "172.18.0.1:8767", "--server-name", "derp.example.com", "--allow-net", "192.0.2.0/24"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if cfg.Upstream != "127.0.0.1:443" {
		t.Fatalf("default upstream = %q, want 127.0.0.1:443", cfg.Upstream)
	}
	if cfg.Timeout != 10*time.Second {
		t.Fatalf("default timeout = %v", cfg.Timeout)
	}
	if tlsName(cfg) != "derp.example.com" {
		t.Fatalf("tlsName = %q", tlsName(cfg))
	}
	if len(cfg.AllowNets) != 1 || cfg.AllowNets[0] != "192.0.2.0/24" {
		t.Fatalf("allow-net = %v", cfg.AllowNets)
	}
	// Without --server-name the upstream host is the SNI, so the operator's
	// `--upstream 127.0.0.1:443` still produces a usable (if IP-based) name.
	plain, _ := ParseArgs([]string{"--listen", "127.0.0.1:1"})
	if tlsName(plain) != "127.0.0.1" {
		t.Fatalf("tlsName(no server-name) = %q", tlsName(plain))
	}
}

// TestB315_ProxyForwardsOnlyAllowedPathsAndHidesNothing is the end-to-end half:
// a real TLS upstream (self-signed, exactly like a name-based relay cert reached
// over loopback) behind a real proxy process, driven over real HTTP.
//
// It pins the three behaviours the page depends on:
//  1. an allowed path is fetched from the upstream and returned body-for-body;
//  2. a path outside the allow-list never reaches the upstream at all;
//  3. `/healthz` answers without touching the upstream, so a supervisor can
//     restart the bridge without depending on the relay being up.
func TestB315_ProxyForwardsOnlyAllowedPathsAndHidesNothing(t *testing.T) {
	cert, err := selfSignedFor("derp.example.com")
	if err != nil {
		t.Fatalf("self-signed cert: %v", err)
	}
	var reached []string
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		if r.URL.Path != "/debug/vars" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"derp":{"accepts":7},"stun":{"counter_requests":{"success":3,"not_stun":1}}}`)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	upstream.StartTLS()
	defer upstream.Close()

	listen := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Listen:     listen,
			Upstream:   strings.TrimPrefix(upstream.URL, "https://"),
			ServerName: "derp.example.com",
			Insecure:   true,
			Timeout:    5 * time.Second,
		})
	}()
	waitReady(t, listen)

	body, status := get(t, "http://"+listen+"/debug/vars")
	if status != 200 || !strings.Contains(body, `"accepts":7`) {
		t.Fatalf("allowed path: status=%d body=%q", status, body)
	}
	if _, status := get(t, "http://"+listen+"/healthz"); status != 200 {
		t.Fatalf("/healthz status=%d", status)
	}
	healthPaths := len(reached)

	if _, status := get(t, "http://"+listen+"/derp"); status != 404 {
		t.Fatalf("a path outside the allow-list must be refused locally, got status=%d", status)
	}
	if len(reached) != healthPaths {
		t.Fatalf("a refused path reached the upstream: %v", reached)
	}
	// ...and the refusal names the rule rather than answering a bare 404 page.
	if body, _ := get(t, "http://"+listen+"/derp"); !strings.Contains(body, "path not forwarded") {
		t.Fatalf("refusal body = %q", body)
	}

	// Shutdown is clean: Run returns nil, so a supervisor sees a normal stop.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// TestB315_UpstreamFailureNamesTheCertificateKnob pins the self-explaining
// failure path: the proxy's error body is surfaced verbatim by /admin/derp, so it
// has to name the flag that fixes it — otherwise the page shows the operator a
// TLS error with no next step.
func TestB315_UpstreamFailureNamesTheCertificateKnob(t *testing.T) {
	cert, err := selfSignedFor("other.example.com")
	if err != nil {
		t.Fatalf("self-signed cert: %v", err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"derp":{"accepts":1}}`)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	upstream.StartTLS()
	defer upstream.Close()

	listen := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Config{
			Listen:     listen,
			Upstream:   strings.TrimPrefix(upstream.URL, "https://"),
			ServerName: "derp.example.com", // does NOT match the served certificate
			Timeout:    5 * time.Second,
		})
	}()
	waitReady(t, listen)

	body, status := get(t, "http://"+listen+"/debug/vars")
	if status != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 (body=%q)", status, body)
	}
	if !strings.Contains(body, "--server-name") || !strings.Contains(body, "--insecure") {
		t.Fatalf("the failure body must name the two certificate knobs, got %q", body)
	}
}

// ---------- helpers ----------

func selfSignedFor(name string) (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("proxy never listened on %s", addr)
}

func get(t *testing.T, url string) (string, int) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode
}
