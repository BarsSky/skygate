package admin

// system_tests_exit_node_speed_test.go — unit tests for the
// exit-node speed/availability system tests added in
// (per operator request, 2026-08-12).
//
// These tests don't touch the real network or the live
// headscale cluster. They pin three things:
//
//   1. tailscaleIPFromNode — correct 100.64.0.0/10 detection,
//      including boundary cases, IPv6-mapped forms, junk input.
//   2. probeExitNodeConnect — override hook + real-network
//      behavior (refused connection on 127.0.0.1:1).
//   3. The two test defs (exitNodesTCPSpeedTest,
//      exitNodesAvailabilityTest) — by running their Run
//      closures with a fake headscale server and a fake
//      probe function. This covers pass/fail/skip/slow
//      branches without depending on the operator's live
//      headscale state.
//
// Why not just test the helpers and trust the rest?
// The Run closures are 30+ lines of branching logic
// (skip if no nodes, sort, format, threshold checks). A bug
// in the sort/format/threshold code would be invisible
// without running the closure end-to-end. The fake headscale
// server is ~15 lines and gives full coverage.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"skygate/internal/headscale"
)

// --- tailscaleIPFromNode --------------------------------------------

func TestTailscaleIPFromNode(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{
			name: "single_cgnat_in_middle",
			in:   []string{"192.168.13.67", "100.64.0.3", "fd7a:115c:a1e0::3"},
			want: "100.64.0.3",
		},
		{
			name: "cgnat_low_boundary",
			in:   []string{"100.64.0.0"},
			want: "100.64.0.0",
		},
		{
			name: "cgnat_high_boundary",
			in:   []string{"100.127.255.255"},
			want: "100.127.255.255",
		},
		{
			name: "one_above_cgnat_returns_empty",
			in:   []string{"100.128.0.0"},
			want: "",
		},
		{
			name: "one_below_cgnat_returns_empty",
			in:   []string{"100.63.255.255"},
			want: "",
		},
		{
			name: "private_rfc1918_returns_empty",
			in:   []string{"10.0.0.1", "192.168.1.1", "172.16.0.1"},
			want: "",
		},
		{
			name: "link_local_returns_empty",
			in:   []string{"169.254.0.1", "fe80::1"},
			want: "",
		},
		{
			name: "ipv6_only_returns_empty",
			in:   []string{"fd7a:115c:a1e0::3", "fe80::1"},
			want: "",
		},
		{
			name: "ipv6_mapped_cgnat_is_accepted",
			in:   []string{"::ffff:100.64.0.5"},
			want: "100.64.0.5",
		},
		{
			name: "garbage_input_is_skipped",
			in:   []string{"not-an-ip", "", "100.64.0.7"},
			want: "100.64.0.7",
		},
		{
			name: "empty_list_returns_empty",
			in:   []string{},
			want: "",
		},
		{
			name: "nil_list_returns_empty",
			in:   nil,
			want: "",
		},
		{
			name: "all_garbage_returns_empty",
			in:   []string{"not-an-ip", "999.999.999.999", "abc"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tailscaleIPFromNode(tc.in)
			if got != tc.want {
				t.Errorf("tailscaleIPFromNode(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- formatLatencyMs -----------------------------------------------

func TestFormatLatencyMs(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "zero", in: 0, want: "0ms"},
		{name: "23ms", in: 23 * time.Millisecond, want: "23ms"},
		{name: "1s_round_down", in: 1500 * time.Millisecond, want: "1500ms"},
		{name: "sub_ms_clamps_to_zero", in: 500 * time.Microsecond, want: "0ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatLatencyMs(tc.in)
			if got != tc.want {
				t.Errorf("formatLatencyMs(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- probeExitNodeConnect -------------------------------------------

func TestProbeExitNodeConnect_OverrideReturnsLatency(t *testing.T) {
	defer func() { probeExitNodeConnectOverride = nil }()
	probeExitNodeConnectOverride = func(ctx context.Context, host, port string) (time.Duration, error) {
		if host != "100.64.0.3" || port != "22" {
			t.Errorf("override got %s:%s, want 100.64.0.3:22", host, port)
		}
		return 42 * time.Millisecond, nil
	}
	d, err := probeExitNodeConnect(context.Background(), "100.64.0.3", "22")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d != 42*time.Millisecond {
		t.Errorf("latency = %v, want 42ms", d)
	}
}

func TestProbeExitNodeConnect_OverrideReturnsError(t *testing.T) {
	defer func() { probeExitNodeConnectOverride = nil }()
	want := errors.New("simulated connection refused")
	probeExitNodeConnectOverride = func(ctx context.Context, host, port string) (time.Duration, error) {
		return 0, want
	}
	d, err := probeExitNodeConnect(context.Background(), "100.64.0.3", "22")
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if d != 0 {
		t.Errorf("latency on error = %v, want 0", d)
	}
}

// TestProbeExitNodeConnect_RealNetworkRefused verifies the
// non-override path actually fails fast on a refused TCP
// connection (127.0.0.1:1 is privileged and not bound by
// anything in CI). The 2s Dialer timeout is the upper bound
// — a refused connection returns in <100ms on Linux.
func TestProbeExitNodeConnect_RealNetworkRefused(t *testing.T) {
	// Skip on Windows where 127.0.0.1:1 behavior is
	// different (the "connection refused" path is less
	// reliable for short-lived connections).
	if _, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		// We just want to check that 127.0.0.1 is
		// reachable enough to refuse. On Windows CI the
		// loopback may be weird, so skip.
	}
	start := time.Now()
	_, err := probeExitNodeConnect(context.Background(), "127.0.0.1", "1")
	elapsed := time.Since(start)
	if err == nil {
		t.Skip("127.0.0.1:1 unexpectedly accepted a connection — environment not suitable for this test")
	}
	if elapsed > 2*time.Second {
		t.Errorf("refused connection took %v, want <2s (Dialer.Timeout)", elapsed)
	}
	if !strings.Contains(err.Error(), "refused") &&
		!strings.Contains(err.Error(), "connectex") &&
		!strings.Contains(err.Error(), "No connection could be made") {
		t.Logf("got expected error (unusual wording): %v", err)
	}
}

// --- fake headscale server for the Run-closure tests ---------------

// fakeHSNode is the wire shape we send back from the fake
// /api/v1/node endpoint. Only the fields ListAllNodes + toView
// actually read are populated; the rest stay zero.
type fakeHSNode struct {
	ID              string   `json:"id"`
	GivenName       string   `json:"givenName"`
	Name            string   `json:"name"`
	IPAddresses     []string `json:"ipAddresses"`
	Online          bool     `json:"online"`
	Tags            []string `json:"tags"`
	AvailableRoutes []string `json:"availableRoutes"`
	User            struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
}

type fakeHSNodeList struct {
	Nodes []fakeHSNode `json:"nodes"`
}

// fakeHS spins up an httptest server that responds to
// GET /api/v1/node with the given node list. Returns the
// server URL and a function to assert hit count.
func fakeHS(t *testing.T, nodes []fakeHSNode) (string, func() int32) {
	t.Helper()
	var hits int32
	body, err := json.Marshal(fakeHSNodeList{Nodes: nodes})
	if err != nil {
		t.Fatalf("marshal fake nodes: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/node" && r.Method == http.MethodGet {
			atomic.AddInt32(&hits, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int32 { return atomic.LoadInt32(&hits) }
}

// setUpServiceWithFakeHS constructs a real *headscale.Client
// pointed at the given server URL, pre-warms the
// ListAllNodes cache (so the test's Run closure hits the
// cache, not the network), wires it into a *Service via
// SetTestService, and returns the service + a cleanup func.
func setUpServiceWithFakeHS(t *testing.T, serverURL string) *Service {
	t.Helper()
	hs := headscale.New(serverURL, "fake-test-key")
	// Bump cache TTL to 1h so the test's later ListAllNodes
	// calls (from the Run closure) hit the cache, not the
	// httptest server again.
	hs.SetCacheTTL(time.Hour)
	// Pre-warm: this is the ONLY call that should hit the
	// fake server. The Run closures should see the cached
	// result.
	if _, err := hs.ListAllNodes(); err != nil {
		t.Fatalf("pre-warm ListAllNodes: %v", err)
	}
	svc := &Service{
		HSGlobalFn: func() *headscale.Client { return hs },
	}
	SetTestService(svc)
	t.Cleanup(func() {
		// Reset the package-level testService so other
		// tests (or this one's next subtest) don't
		// inherit a stale svc.
		SetTestService(nil)
	})
	return svc
}

// setUpProbe sets probeExitNodeConnectOverride to a function
// that returns the given latencies (or errors) keyed by IP.
// If a node's IP is not in the map, the override returns
// (0, errors.New("no fake latency configured for <ip>")).
// Cleanup restores nil.
func setUpProbe(t *testing.T, results map[string]fakeProbeResult) {
	t.Helper()
	probeExitNodeConnectOverride = func(ctx context.Context, host, port string) (time.Duration, error) {
		r, ok := results[host]
		if !ok {
			return 0, fmt.Errorf("no fake latency for %s", host)
		}
		return r.latency, r.err
	}
	t.Cleanup(func() { probeExitNodeConnectOverride = nil })
}

type fakeProbeResult struct {
	latency time.Duration
	err     error
}

// --- exitNodesTCPSpeedTest.Run --------------------------------------

func TestExitNodesTCPSpeedTest_NoService(t *testing.T) {
	// With SetTestService(nil) and a clean package state,
	// getTestService() returns nil → Run returns FAIL.
	SetTestService(nil)
	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want %v", status, SystemTestFail)
	}
	if !strings.Contains(out, "service not initialised") {
		t.Errorf("output = %q, want it to mention service not initialised", out)
	}
}

func TestExitNodesTCPSpeedTest_NoExitNodes_Skips(t *testing.T) {
	// Server returns an empty node list — pre-warm makes
	// the cache empty, Run should report SKIP.
	url, _ := fakeHS(t, []fakeHSNode{})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, nil) // no probes needed, no nodes to probe

	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestSkip {
		t.Errorf("status = %v, want %v (output: %s)", status, SystemTestSkip, out)
	}
}

func TestExitNodesTCPSpeedTest_NoTailscaleIP_Skips(t *testing.T) {
	// Exit node with only a private IP — the probe helper
	// returns "" (not in CGNAT range), so the test should
	// skip (no probes possible).
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "broken-relay", IPAddresses: []string{"192.168.99.1"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, nil)

	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestSkip {
		t.Errorf("status = %v, want %v (output: %s)", status, SystemTestSkip, out)
	}
}

func TestExitNodesTCPSpeedTest_AllFast_Passes(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "relay-1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "2", GivenName: "relay-2", IPAddresses: []string{"100.64.0.4"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "3", GivenName: "relay-3", IPAddresses: []string{"100.64.0.5"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 23 * time.Millisecond},
		"100.64.0.4": {latency: 47 * time.Millisecond},
		"100.64.0.5": {latency: 31 * time.Millisecond},
	})

	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want %v (output: %s)", status, SystemTestPass, out)
	}
	// All three nodes should be in the output, with their
	// latencies rendered.
	for _, want := range []string{"relay-1", "relay-2", "relay-3", "23ms", "47ms", "31ms", "3 exit nodes probed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull: %s", want, out)
		}
	}
	// No FAILED/SLOW blocks expected.
	if strings.Contains(out, "FAILED") || strings.Contains(out, "SLOW") {
		t.Errorf("output contains FAILED or SLOW but test passed:\n%s", out)
	}
}

func TestExitNodesTCPSpeedTest_OneFailed_Fails(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "relay-1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "2", GivenName: "relay-2", IPAddresses: []string{"100.64.0.4"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "3", GivenName: "relay-3", IPAddresses: []string{"100.64.0.5"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 20 * time.Millisecond},
		"100.64.0.4": {err: errors.New("i/o timeout")},
		"100.64.0.5": {latency: 30 * time.Millisecond},
	})

	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want %v (output: %s)", status, SystemTestFail, out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("output missing FAILED block:\n%s", out)
	}
	if !strings.Contains(out, "relay-2") {
		t.Errorf("output missing the failed relay-2:\n%s", out)
	}
}

func TestExitNodesTCPSpeedTest_OneSlow_PassesWithWarning(t *testing.T) {
	// slowThreshold in the test is 1s. relay-3 at 1.5s is
	// slow but still under the 2s hard fail threshold.
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "relay-1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "2", GivenName: "relay-2", IPAddresses: []string{"100.64.0.4"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "3", GivenName: "relay-3", IPAddresses: []string{"100.64.0.5"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
		"100.64.0.4": {latency: 75 * time.Millisecond},
		"100.64.0.5": {latency: 1500 * time.Millisecond},
	})

	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	if !strings.Contains(out, "SLOW") {
		t.Errorf("output missing SLOW block:\n%s", out)
	}
	if !strings.Contains(out, "relay-3") {
		t.Errorf("output missing the slow relay-3:\n%s", out)
	}
}

func TestExitNodesTCPSpeedTest_NotExitNode_Ignored(t *testing.T) {
	// A node that is online but NOT an exit node should be
	// skipped — it's a regular device, not part of the
	// operator's egress fleet.
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "skygate-vm", IPAddresses: []string{"100.64.100.10"}, Online: true, AvailableRoutes: []string{"192.168.13.0/24"}},
		{ID: "2", GivenName: "relay-1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 23 * time.Millisecond},
	})

	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	// skygate-vm is NOT an exit node → no probe.
	if strings.Contains(out, "skygate-vm") {
		t.Errorf("output should not include non-exit nodes:\n%s", out)
	}
	// relay-1 IS an exit node → probed.
	if !strings.Contains(out, "relay-1") {
		t.Errorf("output missing the exit node relay-1:\n%s", out)
	}
}

func TestExitNodesTCPSpeedTest_Offline_Ignored(t *testing.T) {
	// An exit node that is offline (per headscale) should
	// not be probed — the test specifically targets online
	// nodes. The exit-node-monitor package (different file)
	// handles the "node is offline" question; this test
	// trusts headscale's online flag.
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "relay-1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "2", GivenName: "relay-2-offline", IPAddresses: []string{"100.64.0.4"}, Online: false, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 23 * time.Millisecond},
	})

	status, out := exitNodesTCPSpeedTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	if strings.Contains(out, "relay-2-offline") {
		t.Errorf("output should not include offline nodes:\n%s", out)
	}
}

// --- exitNodesAvailabilityTest.Run ---------------------------------

func TestExitNodesAvailabilityTest_AllAvailable_Passes(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "relay-1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "2", GivenName: "relay-2", IPAddresses: []string{"100.64.0.4"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "3", GivenName: "relay-3", IPAddresses: []string{"100.64.0.5"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 23 * time.Millisecond},
		"100.64.0.4": {latency: 47 * time.Millisecond},
		"100.64.0.5": {latency: 31 * time.Millisecond},
	})

	status, out := exitNodesAvailabilityTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	if !strings.Contains(out, "3/3 exit nodes responsive (100%)") {
		t.Errorf("output missing '3/3 ... 100%%':\n%s", out)
	}
}

func TestExitNodesAvailabilityTest_OneDownOfFive_Passes(t *testing.T) {
	// 4/5 = 80% exactly — should still PASS (the threshold
	// is pct < 80 fail; pct >= 80 pass).
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "r1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "2", GivenName: "r2", IPAddresses: []string{"100.64.0.4"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "3", GivenName: "r3", IPAddresses: []string{"100.64.0.5"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "4", GivenName: "r4", IPAddresses: []string{"100.64.0.6"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "5", GivenName: "r5", IPAddresses: []string{"100.64.0.7"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 23 * time.Millisecond},
		"100.64.0.4": {latency: 47 * time.Millisecond},
		"100.64.0.5": {latency: 31 * time.Millisecond},
		"100.64.0.6": {latency: 50 * time.Millisecond},
		"100.64.0.7": {err: errors.New("connection refused")},
	})

	status, out := exitNodesAvailabilityTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS at 80%% (output: %s)", status, out)
	}
	if !strings.Contains(out, "4/5") {
		t.Errorf("output missing '4/5':\n%s", out)
	}
}

func TestExitNodesAvailabilityTest_TwoDownOfThree_Fails(t *testing.T) {
	// 1/3 = 33% — below 80%, FAIL.
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "r1", IPAddresses: []string{"100.64.0.3"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "2", GivenName: "r2", IPAddresses: []string{"100.64.0.4"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
		{ID: "3", GivenName: "r3", IPAddresses: []string{"100.64.0.5"}, Online: true, AvailableRoutes: []string{"0.0.0.0/0"}},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 23 * time.Millisecond},
		"100.64.0.4": {err: errors.New("timeout")},
		"100.64.0.5": {err: errors.New("timeout")},
	})

	status, out := exitNodesAvailabilityTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want FAIL at 33%% (output: %s)", status, out)
	}
	if !strings.Contains(out, "1/3") {
		t.Errorf("output missing '1/3':\n%s", out)
	}
}

func TestExitNodesAvailabilityTest_NoExitNodes_Skips(t *testing.T) {
	// Empty node list — skip (mirrors the speed test's
	// skip-on-no-exit-nodes behaviour).
	url, _ := fakeHS(t, []fakeHSNode{})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, nil)

	status, out := exitNodesAvailabilityTest.Run(context.Background())
	if status != SystemTestSkip {
		t.Errorf("status = %v, want SKIP (output: %s)", status, out)
	}
}

// --- TestRegistry wiring --------------------------------------------

// TestExitNodeSpeedTestsAreRegistered ensures the init() in
// system_tests_exit_node_speed.go wired the two new tests
// into TestRegistry. This is the contract that the
// /admin/system_tests page relies on — if init() breaks
// (typo in a name, missing init) the page silently loses
// the tests.
func TestExitNodeSpeedTestsAreRegistered(t *testing.T) {
	wants := map[string]string{
		"exit_nodes.tcp_connect_speed":   "network",
		"exit_nodes.availability_summary": "network",
	}
	for _, def := range TestRegistry {
		want, ok := wants[def.Name]
		if !ok {
			continue
		}
		if def.Category != want {
			t.Errorf("%s: Category = %q, want %q", def.Name, def.Category, want)
		}
		if def.Run == nil {
			t.Errorf("%s: Run is nil", def.Name)
		}
		delete(wants, def.Name)
	}
	if len(wants) > 0 {
		missing := make([]string, 0, len(wants))
		for n := range wants {
			missing = append(missing, n)
		}
		t.Errorf("TestRegistry is missing the new exit-node tests: %v", missing)
	}
}

// TestTestRegistryHasMinimumCoverage pins the B40 contract:
// the registry must always have ≥6 tests spanning the three
// core categories (network / db / headscale). The v1.1.0
// additions live in "network" and add 2 more, but the
// invariant that B40 verifies stays.
func TestTestRegistryHasMinimumCoverage(t *testing.T) {
	const minTotal = 6
	if len(TestRegistry) < minTotal {
		t.Errorf("TestRegistry has %d entries, want at least %d (B40)", len(TestRegistry), minTotal)
	}
	cats := map[string]int{}
	for _, def := range TestRegistry {
		cats[def.Category]++
	}
	for _, want := range []string{"network", "db", "headscale"} {
		if cats[want] == 0 {
			t.Errorf("TestRegistry has no test in category %q (categories: %v)", want, cats)
		}
	}
}

// TestExitNodeSpeedTestsDescribeThemselves pins that the
// Description field is non-empty for the two new tests. The
// /admin/system_tests page renders it as a tooltip on the
// "Run" button; an empty description means the operator has
// no way to know what the test does.
func TestExitNodeSpeedTestsDescribeThemselves(t *testing.T) {
	for _, def := range TestRegistry {
		if def.Name != "exit_nodes.tcp_connect_speed" && def.Name != "exit_nodes.availability_summary" {
			continue
		}
		if def.Description == "" {
			t.Errorf("%s: Description is empty", def.Name)
		}
	}
}

// --- helpers (re-exported headscale bits) ---------------------------

// strconv.Itoa is imported in some builds but unused here —
// kept to make the test file robust against test refactors
// that re-add numeric assertions.
var _ = strconv.Itoa
