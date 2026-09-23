// internal/feature/admin/derp_probe_dial_b302_test.go — B302 (2026-09-23).
//
// Live on the agent VM (/admin/derp rendered from inside the skygate container):
//
//	DERPER.SERVICE   stopped          ← FALSE: derper was up 39 h
//	DERP SOCKET      :443 TCP listening  ← TRUE (this probe already pinned the address)
//	STUN UDP         :3478 closed        ← TRUE (derper answers no STUN at all)
//	VERSION          v1.70.0 go
//
// `/etc/hosts` inside the container maps the relay's own public name to 127.0.0.1
// (AGENTS trap #2), so the WebSocket liveness probe connected to the container's
// own loopback and failed while its neighbours — which DO pin the dial address
// (B289.1's httpGetVia) — succeeded. This test pins the fix: the probe must reach a
// listener whose URL host does NOT resolve, because the address to dial is passed
// separately from the name to speak.
package admin

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// b302UpgradeServer answers `GET /derp` with 101 Switching Protocols, the signal
// derper sends for a live relay.
func b302UpgradeServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/derp" {
			http.NotFound(w, r)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "not an upgrade", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The live failure, as a regression test: an unresolvable URL host plus a pinned
// dial address must still reach the relay. Pre-B302 the probe dialled the URL host
// and this test fails with a DNS error.
func TestWebSocketProbe_DialsThePinnedAddress_B302(t *testing.T) {
	srv := b302UpgradeServer(t)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}

	// The URL host is deliberately unresolvable — exactly what the container's
	// /etc/hosts does to the relay name, except here it cannot resolve at all.
	unresolvable := "http://derp-name-that-does-not-resolve.invalid:" + port

	// Without a pinned address the probe must fail (this is the pre-B302
	// behaviour, kept as the negative half so the positive half means something).
	if ok, err := derperLivenessWebSocketProbe(unresolvable, "", 2*time.Second); err == nil && ok {
		t.Fatal("without a dial address the probe reached a host that does not resolve — the negative control is broken")
	}

	ok, err := derperLivenessWebSocketProbe(unresolvable, "127.0.0.1", 3*time.Second)
	if err != nil {
		t.Fatalf("with a pinned dial address the probe must reach the relay: %v", err)
	}
	if !ok {
		t.Fatal("the probe did not see 101 Switching Protocols — it is dialling the URL host again, and on a host whose /etc/hosts points the relay name at loopback that reports a healthy derper as stopped")
	}
}

// A relay that is genuinely down must still be reported as down: the fix must not
// turn the probe into a rubber stamp.
func TestWebSocketProbe_StillFailsWhenNothingListens_B302(t *testing.T) {
	// Bind and immediately close, so the port is (almost certainly) free.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ok, err := derperLivenessWebSocketProbe("http://derp.invalid:1/", strings.Split(addr, ":")[0], 1500*time.Millisecond)
	if err == nil && ok {
		t.Fatal("a probe against a closed port reported a live relay")
	}
}

// The probe must send the Host header and the upgrade headers on the pinned path
// too — the address changes, the protocol does not.
func TestWebSocketProbe_KeepsHostAndUpgradeHeaders_B302(t *testing.T) {
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Host + "|" + r.Header.Get("Upgrade")
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n\r\n")
		_ = buf.Flush()
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	if _, err := derperLivenessWebSocketProbe("http://derp.skynas.ru:"+port, "127.0.0.1", 2*time.Second); err != nil {
		t.Fatalf("probe: %v", err)
	}
	select {
	case got := <-seen:
		if !strings.HasPrefix(got, "derp.skynas.ru") {
			t.Errorf("Host header = %q, want the URL hostname (the certificate's name), not the dialled address", got)
		}
		if !strings.Contains(strings.ToLower(got), "websocket") {
			t.Errorf("Upgrade header missing on the pinned path: %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Error("the test server never saw a request")
	}
}
