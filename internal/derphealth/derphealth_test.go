package derphealth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchPublicDERPs_EmptyMap(t *testing.T) {
	body := `{"Regions":{}}`
	var raw derpMapResponse
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Regions) != 0 {
		t.Fatalf("want 0 regions, got %d", len(raw.Regions))
	}
}

func TestProbeOne_ConnRefused(t *testing.T) {
	d := DERPInfo{RegionID: 999, RegionCode: "tst", Host: "127.0.0.1:1"}
	_, err := ProbeOne(context.Background(), d, &http.Client{Timeout: 500 * time.Millisecond})
	if err == nil {
		t.Fatalf("want error connecting to 127.0.0.1:1, got nil")
	}
}

func TestProbeOne_HandshakeOK(t *testing.T) {
	cert, err := tls.X509KeyPair([]byte(testCertPEM), []byte(testKeyPEM))
	if err != nil {
		t.Fatalf("test cert: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(testCertPEM)) {
		t.Fatalf("failed to add test cert to pool")
	}
	// Save the package-level TLS override and restore after the test.
	prev := ProbeOneTLSConfig
	ProbeOneTLSConfig = &tls.Config{RootCAs: pool}
	defer func() { ProbeOneTLSConfig = prev }()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	host := srv.Listener.Addr().String()
	d := DERPInfo{RegionID: 1, RegionCode: "tst", Host: host}
	lat, err := ProbeOne(context.Background(), d, srv.Client())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if lat <= 0 {
		t.Fatalf("want positive latency, got %d", lat)
	}
	if lat > 1000 {
		t.Logf("warning: local TLS handshake took %dms", lat)
	}
}

func TestProbeAll_PersistCount(t *testing.T) {
	derps := []DERPInfo{
		{RegionID: 1, RegionCode: "ok1", Host: "127.0.0.1:1"},
		{RegionID: 2, RegionCode: "ok2", Host: "127.0.0.1:2"},
		{RegionID: 3, RegionCode: "ok3", Host: "127.0.0.1:3"},
	}
	persistCalls := atomic.Int64{}
	persist := func(ctx context.Context, d DERPInfo, lat int, healthy bool, err error) error {
		persistCalls.Add(1)
		return nil
	}
	results := ProbeAll(context.Background(), derps, &http.Client{Timeout: 200 * time.Millisecond}, persist)
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	if got := persistCalls.Load(); got != 3 {
		t.Fatalf("want persist called 3 times, got %d", got)
	}
	for _, r := range results {
		if r.Healthy {
			t.Errorf("probe to 127.0.0.1 should not be healthy (got %dms)", r.LatencyMs)
		}
	}
}

func TestNullableInt(t *testing.T) {
	if nullableInt(50, true) != 50 {
		t.Errorf("healthy probe should return raw latency")
	}
	if nullableInt(0, false) != nil {
		t.Errorf("failed probe should return nil (SQL NULL)")
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Errorf("boolToInt wrong")
	}
}
