// B464 (2026-11-11) — the agent's own measurement of its host.
//
// The probe is the half of the fix that lives on the standby, so it is the half that
// can silently lie: a wrong port ("nothing answers" on a healthy host), a proxy (a
// 200 that is not skygate), or a driver message carrying a credential into another
// database. Each of those is pinned below with a real HTTP server, not a mock.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skygate/internal/cluster"
	skygatedb "skygate/internal/db"
)

// TestB464_ResolveHealthURL — precedence, and the ONE default that matters: 127.0.0.1
// and not the hostname (trap #2: a public name that resolves to a proxy, or to
// 127.0.0.1 inside a container, measures the wrong thing).
func TestB464_ResolveHealthURL(t *testing.T) {
	t.Setenv("SKYGATE_HEALTH_URL", "")
	t.Setenv("SKYGATE_PORT", "")
	got := resolveHealthURL("")
	if got != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("default = %q, want 127.0.0.1:8080 (skygate's own default port)", got)
	}

	t.Setenv("SKYGATE_PORT", "8082")
	if got := resolveHealthURL(""); got != "http://127.0.0.1:8082/healthz" {
		t.Fatalf("SKYGATE_PORT ignored: %q", got)
	}

	t.Setenv("SKYGATE_HEALTH_URL", "http://127.0.0.1:9000/healthz")
	if got := resolveHealthURL(""); got != "http://127.0.0.1:9000/healthz" {
		t.Fatalf("SKYGATE_HEALTH_URL ignored: %q", got)
	}

	// The flag wins over everything: an operator who names an address means it.
	if got := resolveHealthURL("http://127.0.0.1:7000/healthz"); got != "http://127.0.0.1:7000/healthz" {
		t.Fatalf("--health-url ignored: %q", got)
	}
}

// TestB464_ReadyzURLFor — one URL to configure, both probes derived from it.
func TestB464_ReadyzURLFor(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8080/healthz": "http://127.0.0.1:8080/readyz",
		"http://127.0.0.1:8080/":        "http://127.0.0.1:8080/readyz",
		"http://host:1":                 "http://host:1/readyz",
		"":                              "",
	}
	for in, want := range cases {
		if got := readyzURLFor(in); got != want {
			t.Errorf("readyzURLFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestB464_Measure_ServingAndReady — the good path: both endpoints answer, the build
// the probe saw is carried so the hub can compare it with its own.
func TestB464_Measure_ServingAndReady(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","build":"v1.5.210+dd2f623b"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"healthy":true,"db":"ok","headscale":"ok"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rd := newReadinessProber(srv.URL + "/healthz").measure(context.Background())
	if !rd.Serving || !rd.Ready {
		t.Fatalf("serving=%v ready=%v, want both true (%+v)", rd.Serving, rd.Ready, rd)
	}
	if rd.Build != "v1.5.210+dd2f623b" {
		t.Fatalf("build = %q", rd.Build)
	}
}

// TestB464_Measure_NotReady — /healthz answers but /readyz says the database is
// unreachable: the instance is up and still cannot serve. This is the case that a
// liveness-only probe would report as "healthy", and it is why the readiness payload
// carries both answers.
func TestB464_Measure_NotReady(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","build":"v1.5.108+59fbad7"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"healthy":false,"db":"error: connection refused"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rd := newReadinessProber(srv.URL + "/healthz").measure(context.Background())
	if !rd.Serving {
		t.Fatalf("serving = false, want true (healthz answered 200)")
	}
	if rd.Ready {
		t.Fatal("ready = true, want false — /readyz answered 503")
	}
	if !strings.Contains(rd.DB, "503") {
		t.Fatalf("db = %q, want it to name the readyz status", rd.DB)
	}
}

// TestB464_Measure_NotSkygate — a 200 that is not skygate's payload must NOT count as
// "serving". Otherwise any proxy, dev server or captive portal on the port makes a
// dead standby look healthy — the exact false positive this block removes.
func TestB464_Measure_NotSkygate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>hello</html>`))
	}))
	defer srv.Close()

	rd := newReadinessProber(srv.URL + "/healthz").measure(context.Background())
	if rd.Serving {
		t.Fatalf("serving = true for a non-skygate 200 (%+v) — a proxy would be counted as a standby", rd)
	}
	if rd.ProbeError == "" {
		t.Fatal("probe_error is empty, so the hub cannot say why")
	}
}

// TestB464_Measure_NothingListening — the live svyatoslava case: nothing on the port.
func TestB464_Measure_NothingListening(t *testing.T) {
	// Reserve a port, then close it so the connect is refused rather than hanging.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	rd := newReadinessProber("http://" + addr + "/healthz").measure(context.Background())
	if rd.Serving || rd.Ready {
		t.Fatalf("serving=%v ready=%v, want both false with nothing listening", rd.Serving, rd.Ready)
	}
	if rd.ProbeError == "" {
		t.Fatal("probe_error is empty — the hub would degrade the node with no explanation")
	}
}

// TestB464_RedactProbeDetail — a driver message can carry a DSN, and this string
// leaves the host (it goes into the hub's cluster_audit and onto a page).
func TestB464_RedactProbeDetail(t *testing.T) {
	in := `failed to connect: postgres://skygate:s3cr3t@127.0.0.1:5432/skygate password=hunter2`
	got := redactProbeDetail(in)
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "hunter2") {
		t.Fatalf("credential survived redaction: %q", got)
	}
	if !strings.Contains(got, "postgres://<redacted>@") {
		t.Fatalf("the DSN's shape was lost: %q", got)
	}
}

// b464Token builds a REAL sgn1 token for the given lifetime, signed with "secret".
// Using the product's own issuer (via a throwaway SQLite DB) rather than a hand-made
// string keeps the test honest: if the payload shape ever changes, this breaks.
func b464Token(t *testing.T, ttl time.Duration) string {
	t.Helper()
	d, err := skygatedb.OpenFastSQLiteForTest(t.TempDir() + "/b464token.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	hours := int(ttl.Hours())
	if hours <= 0 {
		hours = 1
	}
	_, token, _, err := cluster.IssueInvite(d, cluster.DefaultClusterID, cluster.NodeRoleStandby, "h", hours, "secret")
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	return token
}

// b464ExpiredToken hand-builds a token whose `exp` is in the past. IssueInvite always
// issues a FUTURE expiry (it adds ttlHours to now), and the daemon only READS the
// claim here — the signature is the hub's business — so the payload is enough.
func b464ExpiredToken(t *testing.T, age time.Duration) string {
	t.Helper()
	payload := fmt.Sprintf(`{"inv":"deadbeefdeadbeef","cid":"skygate-staging","rol":"skygate-standby","th":"h","exp":%d}`,
		time.Now().Add(-age).Unix())
	return "sgn1." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".c2ln"
}

// TestB464_TokenExpiryIsReadable — the daemon decodes its OWN token's expiry without
// the signing secret (it never verifies it; the hub does that).
func TestB464_TokenExpiryIsReadable(t *testing.T) {
	token := b464Token(t, 24*time.Hour)
	exp, ok := tokenExpiryUnix(token)
	if !ok || exp == 0 {
		t.Fatalf("tokenExpiryUnix(%q) = (%d, %v), want a value", token, exp, ok)
	}
	if time.Unix(exp, 0).Before(time.Now()) {
		t.Fatalf("expiry %s is not in the future", time.Unix(exp, 0))
	}
	// Garbage in, no claim out — never a panic and never an invented time.
	for _, bad := range []string{"", "sgn1", "sgn1.a.b", "sgn2.abc.def", "sgn1.!!!!.x"} {
		if _, ok := tokenExpiryUnix(bad); ok {
			t.Errorf("tokenExpiryUnix(%q) reported an expiry for a malformed token", bad)
		}
	}
}

// TestB464_ExpiredCredentialWarns — the live case (exp 2026-10-10, heartbeating
// 2026-11-12) must produce a journal line, and a valid credential must stay quiet so
// the warning keeps meaning something.
func TestB464_ExpiredCredentialWarns(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	warnIfCredentialExpired(b464Token(t, 24*time.Hour))
	if buf.Len() != 0 {
		t.Fatalf("a valid credential produced a warning: %q", buf.String())
	}

	warnIfCredentialExpired(b464ExpiredToken(t, 72*time.Hour))
	out := buf.String()
	for _, want := range []string{"expired", "rotate", "3 day"} {
		if !strings.Contains(out, want) {
			t.Fatalf("an expired credential produced %q, want it to contain %q", out, want)
		}
	}
}
