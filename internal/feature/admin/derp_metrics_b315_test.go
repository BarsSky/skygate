package admin

// derp_metrics_b315_test.go — B315: derper's rich metrics are readable from the
// container only through an operator-configured endpoint, and the page must never
// draw a zero it did not measure.
//
// The two defects this pins, both live on the agent VM:
//
//  1. `parseDerperVars` accepted ANY JSON object as derper's metrics. The old
//     tail (`if v.DERP.Accepts >= 0 { st.Running = true }`) is true for `{}`, so
//     a 200 response that carried nothing still produced "Running: active" next
//     to twelve zeros — and a zero on a busy relay reads as a measurement.
//  2. Every failure mode (403 from the relay, 502 from a broken bridge, a
//     connection refusal, an unrelated 200) collapsed into the same warning
//     banner. The bridge's own error body is the fix instructions, so it has to
//     survive into the page with its status code distinguishable.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skygate/internal/derpcfg"
)

// TestB315_ParseDerperVarsRequiresTheDerpBlock pins the presence check: only a
// body that actually carries derper's `derp` object counts as measured metrics.
func TestB315_ParseDerperVarsRequiresTheDerpBlock(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty object", `{}`, false},
		{"not json", `<html>debug access denied</html>`, false},
		{"unrelated json", `{"ok":true,"service":"derp-debug-proxy"}`, false},
		{"derp block", `{"derp":{"accepts":12,"gauge_current_connections":2,"bytes_received":100,"bytes_sent":200,"packets_received":3,"packets_sent":4,"gauge_clients_total":5},"stun":{"counter_requests":{"success":9,"not_stun":1}},"go_version":"go1.22"}`, true},
		{"derp block only", `{"derp":{}}`, true},
	}
	for _, c := range cases {
		var st DerpStatus
		got := parseDerperVars(&st, []byte(c.body))
		if got != c.want {
			t.Fatalf("%s: parseDerperVars = %v, want %v", c.name, got, c.want)
		}
		if got && !st.Running {
			t.Fatalf("%s: a measured body must set Running", c.name)
		}
		if !got && st.Running {
			t.Fatalf("%s: an unmeasured body must NOT set Running", c.name)
		}
	}

	// The counters the STUN tile is explained with are read out of the same body.
	var st DerpStatus
	if !parseDerperVars(&st, []byte(`{"derp":{"accepts":12},"stun":{"counter_requests":{"success":9,"not_stun":4}}}`)) {
		t.Fatal("expected the derp body to parse")
	}
	if st.STUNRequests != 9 || st.STUNNotSTUN != 4 {
		t.Fatalf("STUN counters = success %d / not_stun %d, want 9 / 4", st.STUNRequests, st.STUNNotSTUN)
	}
}

// TestB315_CheckMetricsEndpointNamesEveryOutcome drives the shared check against
// real HTTP servers, one per failure shape the operator can actually hit.
func TestB315_CheckMetricsEndpointNamesEveryOutcome(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug/vars" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"derp":{"accepts":41},"stun":{"counter_requests":{"success":7,"not_stun":2}}}`))
	}))
	defer relay.Close()

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "debug access denied", http.StatusForbidden)
	}))
	defer denied.Close()

	brokenBridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "metrics proxy: upstream https://127.0.0.1:443/debug/vars failed: dial tcp 127.0.0.1:443: connect: connection refused — nothing is listening on the upstream", http.StatusBadGateway)
	}))
	defer brokenBridge.Close()

	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"derp-debug-proxy"}`))
	}))
	defer junk.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // nothing listens any more

	ok := checkMetricsEndpoint(relay.URL, "", 3*time.Second)
	if !ok.OK || ok.Code != "ok" || ok.Accepts != 41 || ok.STUNOK != 7 || ok.STUNNotSTUN != 2 {
		t.Fatalf("healthy endpoint: %+v", ok)
	}
	if ok.Bytes == 0 {
		t.Fatal("a healthy read must report the body size")
	}

	if got := checkMetricsEndpoint(denied.URL, "", 3*time.Second); got.Code != "denied" || !got.Denied || got.OK {
		t.Fatalf("403 from the relay: %+v", got)
	}
	if got := checkMetricsEndpoint(brokenBridge.URL, "", 3*time.Second); got.Code != "badstatus" || got.Status != http.StatusBadGateway {
		t.Fatalf("502 from the bridge: %+v", got)
	} else if !strings.Contains(got.Detail, "connection refused") {
		t.Fatalf("the bridge's own explanation must survive into the page: %q", got.Detail)
	}
	if got := checkMetricsEndpoint(junk.URL, "", 3*time.Second); got.Code != "badbody" || got.OK {
		t.Fatalf("200 without metrics: %+v", got)
	}
	if got := checkMetricsEndpoint(deadURL, "", 2*time.Second); got.Code != "unreachable" || got.OK {
		t.Fatalf("no listener: %+v", got)
	}
}

// TestB315_ResolveMetricsEndpointKeepsTheThreeLayers pins the layering the page
// renders: DB override > SKYGATE_DERP_DEBUG_URL > none, and the relay URL is
// always reported as the fallback so the page can say what WOULD be used.
func TestB315_ResolveMetricsEndpointKeepsTheThreeLayers(t *testing.T) {
	t.Setenv(derpcfg.DebugEnvKey, "http://192.0.2.30:8767")
	mep := resolveMetricsEndpoint(nil, "https://relay.example.com:443")
	if mep.Base != "http://192.0.2.30:8767" || mep.Source != derpcfg.SourceEnv {
		t.Fatalf("env layer: %+v", mep)
	}
	if mep.Env != "http://192.0.2.30:8767" {
		t.Fatalf("the env value must be reported for the form hint: %+v", mep)
	}
	if mep.Relay != "https://relay.example.com:443" {
		t.Fatalf("the relay fallback must be reported: %+v", mep)
	}

	t.Setenv(derpcfg.DebugEnvKey, "")
	mep = resolveMetricsEndpoint(nil, "https://relay.example.com:443")
	if mep.Base != "" || mep.Source != derpcfg.SourceDefault {
		t.Fatalf("default layer must be an empty endpoint (never a guessed one): %+v", mep)
	}
}

// TestB315_ErrorDetailCollapsesWhitespaceAndTruncates keeps the page readable:
// derper/bridge bodies are HTML/JSON with newlines, and the detail line is one
// line in the card.
func TestB315_ErrorDetailCollapsesWhitespaceAndTruncates(t *testing.T) {
	got := metricsErrorDetail(http.StatusBadGateway, []byte("line one\n\n   line two\t"), "http://192.0.2.31:8767")
	if !strings.Contains(got, "HTTP 502") || !strings.Contains(got, "line one line two") {
		t.Fatalf("detail = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("detail must be one line: %q", got)
	}
	long := strings.Repeat("x", 500)
	if got := metricsErrorDetail(500, []byte(long), ""); len(got) > 300 {
		t.Fatalf("detail was not truncated: %d chars", len(got))
	}
	if got := metricsErrorDetail(500, nil, ""); !strings.Contains(got, "(empty body)") {
		t.Fatalf("empty body detail = %q", got)
	}
}
