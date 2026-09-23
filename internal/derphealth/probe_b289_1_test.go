// probe_b289_1_test.go — the health probe must dial the relay's ADDRESS while
// presenting the relay's NAME (B289.1).
//
// Live `skygate-host`, 2026-09-23: the row for region 900 carried
// `url=https://derp.skynas.ru:443` and the health probe reported
//
//	tls dial: dial tcp 127.0.0.1:443: connect: connection refused
//
// for a relay that was up, serving a valid Let's Encrypt certificate — because
// the skygate container inherits the host's /etc/hosts, where the relay's own
// public name maps to 127.0.0.1 (AGENTS deployment trap #2). The port came from
// nowhere either: the probe forced :443 and ignored the row's URL.
package derphealth

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestB289_1_DialTargetKeepsTheNameAndTakesTheAddress(t *testing.T) {
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "")

	// The row's URL owns the port; the hostname becomes the SNI.
	addr, sni, port := dialTargetFor(DERPInfo{Host: "relay.example.com", URL: "https://relay.example.com:8443"})
	if addr != "relay.example.com" || sni != "relay.example.com" || port != "8443" {
		t.Errorf("dialTargetFor = (%q,%q,%q), want (relay.example.com, relay.example.com, 8443)", addr, sni, port)
	}

	// No port in the URL → the DERP default.
	addr, sni, port = dialTargetFor(DERPInfo{Host: "relay.example.com", URL: "https://relay.example.com"})
	if addr != "relay.example.com" || sni != "relay.example.com" || port != "443" {
		t.Errorf("dialTargetFor = (%q,%q,%q), want (relay.example.com, relay.example.com, 443)", addr, sni, port)
	}

	// A hostname with an explicit port in Host (older rows) is honoured too.
	_, _, port = dialTargetFor(DERPInfo{Host: "relay.example.com:9443", URL: ""})
	if port != "9443" {
		t.Errorf("port = %q, want 9443 from Host", port)
	}

	// The leaked-name case: the operator's hint becomes the ADDRESS, and the
	// public hostname stays the SNI (derper's manual certmode resolves the
	// certificate by SNI; Go sends none for an IP literal).
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "192.0.2.69")
	addr, sni, port = dialTargetFor(DERPInfo{Host: "localhost", URL: "https://localhost:443"})
	if addr != "192.0.2.69" {
		t.Errorf("addr = %q, want the operator's probe host when the relay name resolves to loopback", addr)
	}
	if sni != "localhost" {
		t.Errorf("sni = %q, want the relay's public hostname (never the dialled address)", sni)
	}
	if port != "443" {
		t.Errorf("port = %q, want 443", port)
	}

	// A name that resolves normally is NOT rewritten by the hint.
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "192.0.2.69")
	addr, sni, _ = dialTargetFor(DERPInfo{Host: "192.0.2.10", URL: "https://192.0.2.10:443"})
	if addr != "192.0.2.10" || sni != "192.0.2.10" {
		t.Errorf("dialTargetFor = (%q,%q), want the row's own address when no leak is detected", addr, sni)
	}
}

func TestB289_1_LoopbackDetection(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"localhost", true},
		{"192.0.2.10", false},
	}
	for _, c := range cases {
		if got := nameResolvesToLoopback(c.host); got != c.want {
			t.Errorf("nameResolvesToLoopback(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestB289_1_ProbeReachesAnSNIStrictRelayViaTheHint is the end-to-end form of the
// health-probe half: an SNI-strict listener (manual-certmode derper) is reachable
// only through the operator's hint, and the probe must report it healthy.
func TestB289_1_ProbeReachesAnSNIStrictRelayViaTheHint(t *testing.T) {
	srv := newSNIStrictHealthServer(t, "localhost")
	addr, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	t.Setenv("SKYGATE_DERP_PROBE_HOST", addr)

	// The fixture's certificate is httptest's own (issued for example.com /
	// 127.0.0.1), so trust verification cannot succeed for `localhost`. What this
	// test pins is the CONNECTION plumbing — address from the hint, SNI from the
	// row — so it relaxes verification for the fixture only. Production keeps the
	// system trust store (that is the whole point of this probe).
	prev := ProbeOneTLSConfig
	ProbeOneTLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test fixture
	t.Cleanup(func() { ProbeOneTLSConfig = prev })

	lat, err := ProbeOne(t.Context(), DERPInfo{
		RegionID: 900,
		Host:     "localhost",
		URL:      "https://localhost:" + port,
		IsOwn:    true,
	}, nil)
	if err != nil {
		t.Fatalf("ProbeOne failed for a healthy SNI-strict relay reached via SKYGATE_DERP_PROBE_HOST: %v", err)
	}
	if lat < 0 {
		t.Errorf("latency = %d, want >= 0", lat)
	}
}

// newSNIStrictHealthServer serves a certificate ONLY for `expectedName` in SNI,
// like `derper --certmode=manual`. The TLS listener is built by hand because
// httptest.StartTLS() injects its own certificate into an empty
// `Certificates` slice, and Go then skips GetCertificate for clients that sent
// no SNI — the exact blind spot that hid this bug.
func newSNIStrictHealthServer(t *testing.T, expectedName string) *httptest.Server {
	t.Helper()
	seed := httptest.NewTLSServer(nil)
	cert := seed.TLS.Certificates[0]
	seed.Close()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	srv.Listener = tls.NewListener(srv.Listener, &tls.Config{
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hi.ServerName != expectedName {
				return nil, fmt.Errorf("cert mismatch with hostname: %q", hi.ServerName)
			}
			return &cert, nil
		},
	})
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}
