// derp_b478_test.go — B478 (2026-11-15).
//
// THE LIVENESS PROBE MUST SPEAK THE RELAY'S PROTOCOL, OVER HTTP/1.1.
//
// Measured on the migration host while the panel said "DERPER SERVICE stopped"
// for a relay that was demonstrably serving clients:
//
//	curl --http1.1 -H 'Upgrade: DERP'      https://derp.skynas.ru/derp  → 101
//	curl --http1.1 -H 'Upgrade: websocket' https://derp.skynas.ru/derp  → 200
//	curl --http2  -H 'Upgrade: DERP'       https://derp.skynas.ru/derp  → 426
//
// The probe sent `Upgrade: websocket` and let Go negotiate h2, so through Caddy it
// never saw 101 and reported a healthy relay as dead. The same relay answers 101
// in ~10 ms to the real token.
package admin

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestB478_ProbeSendsTheRealUpgradeToken: the request must carry `Upgrade: DERP`.
// A websocket-shaped probe is answered differently by every proxy (nginx passes it,
// Caddy answers 200), which is exactly how the false "stopped" happened.
func TestB478_ProbeSendsTheRealUpgradeToken(t *testing.T) {
	var gotUpgrade, gotConnection, gotProto string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUpgrade = r.Header.Get("Upgrade")
		gotConnection = r.Header.Get("Connection")
		gotProto = r.Proto
		if r.Header.Get("Upgrade") == "DERP" {
			w.WriteHeader(http.StatusSwitchingProtocols)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ok, err := derperLivenessWebSocketProbe(srv.URL, "", 3*time.Second)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !ok {
		t.Fatalf("probe reported the relay dead although it answered 101 to `Upgrade: DERP` (B478)")
	}
	if gotUpgrade != "DERP" {
		t.Errorf("Upgrade = %q, want DERP (a websocket token is answered by the PROXY, not the relay)", gotUpgrade)
	}
	if gotConnection != "Upgrade" {
		t.Errorf("Connection = %q, want Upgrade", gotConnection)
	}
	if gotProto != "HTTP/1.1" {
		t.Errorf("request proto = %q, want HTTP/1.1 — an HTTP/2 connection cannot carry an upgrade (B478)", gotProto)
	}
}

// TestB478_TwoHundredIsNotHealthy: Caddy answers 200 to a websocket-shaped upgrade.
// 200 means "no upgrade happened", so it must never be read as a live relay.
func TestB478_TwoHundredIsNotHealthy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ok, err := derperLivenessWebSocketProbe(srv.URL, "", 3*time.Second)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if ok {
		t.Fatalf("a plain 200 was treated as a live relay — that is the Caddy false-'stopped' path in reverse (B478)")
	}
}

// TestB478_ProbeUsesThePinnedDialAddress: the URL hostname stays in Host + SNI,
// while the TCP connection goes to the address that is reachable from here (B302).
// B478 keeps that rule intact — the token change must not undo it.
//
// The URL names 127.0.0.2 (an IP literal, so the probe skips verification exactly
// like a container probing its own relay) while the server listens on 127.0.0.1:
// only a probe that honours the pinned dial address can reach it.
func TestB478_ProbeUsesThePinnedDialAddress(t *testing.T) {
	var gotHost string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	ok, err := derperLivenessWebSocketProbe("https://127.0.0.2:"+port, "127.0.0.1", 3*time.Second)
	if err != nil {
		t.Fatalf("probe did not reach the pinned dial address: %v", err)
	}
	if !ok {
		t.Fatalf("probe did not reach the pinned address (B478/B302)")
	}
	if !strings.HasPrefix(gotHost, "127.0.0.2") {
		t.Errorf("Host header = %q, want the URL's hostname (the SNI/Host must not become the dial address)", gotHost)
	}
}
