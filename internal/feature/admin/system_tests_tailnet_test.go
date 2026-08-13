package admin

// system_tests_tailnet_test.go — unit tests for the
// tailnet reachability/speed/split system tests added
// in v1.3.10 (B110) per operator request 2026-08-13.
//
// These tests use the same fakeHS + setUpServiceWithFakeHS
// + setUpProbe harness as system_tests_exit_node_speed_test.go.
// They pin the B110 contract: the 3 new TestRegistry
// entries behave correctly across the pass/fail/skip
// branches, and vpsHostnameSet returns the expected
// operator-specific VPS list.
//
// Why not extract the harness to a shared testutil file?
// ======================================================
// The harness (fakeHS, setUpServiceWithFakeHS, setUpProbe)
// is already shared via the same package — it lives in
// system_tests_exit_node_speed_test.go and is reused
// here by Go test discovery. Extracting it would help
// only if we expected 3+ files to use it; right now the
// two test files are stable and the duplication is
// minimal (zero, actually — see "use the helpers from
// the other file" in the imports).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// setUpTailnetSelfOverride pins tailnetSelfHostname to a
// test-controlled value (otherwise it would call
// `tailscale status --json` which doesn't exist in the
// test process, returning "" and leaving the test's own
// skygate-host-1 node in the probe set).
func setUpTailnetSelfOverride(t *testing.T, hostname string) {
	t.Helper()
	prev := tailnetSelfHostnameOverride
	tailnetSelfHostnameOverride = func() string { return hostname }
	t.Cleanup(func() { tailnetSelfHostnameOverride = prev })
}

// --- vpsHostnameSet -----------------------------------------------

func TestVpsHostnameSet_IncludesKnownVPS(t *testing.T) {
	vps := vpsHostnameSet()
	for _, want := range []string{"emilia", "karolina", "sharlotta", "skygate-host-1", "svyatoslava-1"} {
		if !vps[want] {
			t.Errorf("vpsHostnameSet() missing %q", want)
		}
	}
}

func TestVpsHostnameSet_ExcludesHomeDevices(t *testing.T) {
	vps := vpsHostnameSet()
	for _, home := range []string{"skybars", "skyworker", "a71", "olesya", "nothing-phone-2", "basic", "base", "skybars-1", "cyborg", "svyatoslava-legacy"} {
		if vps[home] {
			t.Errorf("vpsHostnameSet() should NOT include home device %q", home)
		}
	}
}

func TestVpsHostnameSet_ReturnsFreshMap(t *testing.T) {
	// Defensive: callers may mutate the returned map.
	// The function returns a fresh map each call so
	// mutations don't leak across tests.
	m1 := vpsHostnameSet()
	m1["evil"] = true
	m2 := vpsHostnameSet()
	if m2["evil"] {
		t.Error("vpsHostnameSet() returned a shared map; mutation leaked")
	}
}

// --- allNodesReachabilityTest.Run ---------------------------------

func TestAllNodesReachabilityTest_NoService(t *testing.T) {
	SetTestService(nil)
	status, out := allNodesReachabilityTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want FAIL", status)
	}
	if !strings.Contains(out, "service not initialised") {
		t.Errorf("output = %q, want 'service not initialised'", out)
	}
}

func TestAllNodesReachabilityTest_NoNodes_Skips(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, nil)

	status, out := allNodesReachabilityTest.Run(context.Background())
	if status != SystemTestSkip {
		t.Errorf("status = %v, want SKIP (output: %s)", status, out)
	}
}

func TestAllNodesReachabilityTest_AllReachable_Passes(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
		"100.64.0.2": {latency: 75 * time.Millisecond},
		"100.64.0.4": {latency: 60 * time.Millisecond},
	})

	status, out := allNodesReachabilityTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	for _, want := range []string{"3/3 probable Tailscale nodes reachable (100%)", "emilia", "karolina", "sharlotta"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull: %s", want, out)
		}
	}
}

func TestAllNodesReachabilityTest_SplitScenario_FailsBelow60(t *testing.T) {
	// v1.3.16: only VPS-class nodes in the test set
	// (home-LAN-without-SSH are now in tailnetSkipHostnames,
	// self is also skipped). 6 probable VPS nodes total,
	// 3 reachable = 3/6 = 50% < 60% → FAIL (split).
	// The 2 home-LAN nodes below are NOT in the reachable
	// set — they're filtered by the skip set so they
	// don't drag the % down further (they were never
	// meant to be probed).
	setUpTailnetSelfOverride(t, "skygate-host-1")
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
		{ID: "4", GivenName: "svyatoslava-1", IPAddresses: []string{"100.64.0.15"}, Online: true},
		{ID: "5", GivenName: "relay-1", IPAddresses: []string{"100.64.0.7"}, Online: true},
		{ID: "6", GivenName: "relay-2", IPAddresses: []string{"100.64.0.8"}, Online: true},
		// home-LAN — these should be SKIPPED, not probed.
		// We include them in the setup to verify the skip
		// filter works.
		{ID: "7", GivenName: "skygate-host-1", IPAddresses: []string{"100.64.0.18"}, Online: true},
		{ID: "8", GivenName: "skybars", IPAddresses: []string{"100.64.0.5"}, Online: true},
		{ID: "9", GivenName: "skyworker", IPAddresses: []string{"100.64.0.1"}, Online: true},
		{ID: "10", GivenName: "a71", IPAddresses: []string{"100.64.0.19"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3":  {latency: 50 * time.Millisecond},
		"100.64.0.2":  {latency: 75 * time.Millisecond},
		"100.64.0.4":  {latency: 60 * time.Millisecond},
		// 3 VPS nodes unreachable (split signature)
		"100.64.0.15": {err: errors.New("i/o timeout")},
		"100.64.0.7":  {err: errors.New("i/o timeout")},
		"100.64.0.8":  {err: errors.New("i/o timeout")},
		// home-LAN-without-SSH — also unreachable but should
		// be SKIPPED (not fail the test)
		"100.64.0.18": {err: errors.New("connection refused")},
		"100.64.0.5":  {err: errors.New("i/o timeout")},
		"100.64.0.1":  {err: errors.New("i/o timeout")},
		"100.64.0.19": {err: errors.New("i/o timeout")},
	})

	status, out := allNodesReachabilityTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want FAIL on split scenario (output: %s)", status, out)
	}
	if !strings.Contains(out, "UNREACHABLE") {
		t.Errorf("output missing UNREACHABLE block:\n%s", out)
	}
	// v1.3.16: 3/6 = 50%, 4 home-LAN+self skipped
	if !strings.Contains(out, "3/6 probable Tailscale nodes reachable (50%)") {
		t.Errorf("output missing 3/6 summary line:\n%s", out)
	}
	// The skip block should list the home-LAN hostnames
	if !strings.Contains(out, "skygate-host-1") || !strings.Contains(out, "skybars") {
		t.Errorf("output should list skipped home-LAN/self hostnames:\n%s", out)
	}
}

func TestAllNodesReachabilityTest_OneUnreachable_Passes(t *testing.T) {
	// v1.3.16: only VPS-class nodes. 5 probable, 4 reachable
	// = 80% > 60% → PASS, but unreachable node listed.
	setUpTailnetSelfOverride(t, "skygate-host-1")
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
		{ID: "4", GivenName: "svyatoslava-1", IPAddresses: []string{"100.64.0.15"}, Online: true},
		{ID: "5", GivenName: "relay-1", IPAddresses: []string{"100.64.0.7"}, Online: true},
		// home-LAN — skipped
		{ID: "6", GivenName: "skygate-host-1", IPAddresses: []string{"100.64.0.18"}, Online: true},
		{ID: "7", GivenName: "skybars", IPAddresses: []string{"100.64.0.5"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3":  {latency: 50 * time.Millisecond},
		"100.64.0.2":  {latency: 75 * time.Millisecond},
		"100.64.0.4":  {latency: 60 * time.Millisecond},
		"100.64.0.15": {latency: 80 * time.Millisecond},
		// 1 VPS unreachable = "phone asleep" or relay hiccup
		"100.64.0.7":  {err: errors.New("i/o timeout")},
		// home-LAN unreachable — skipped
		"100.64.0.18": {err: errors.New("connection refused")},
		"100.64.0.5":  {err: errors.New("i/o timeout")},
	})

	status, out := allNodesReachabilityTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS at 80%% (output: %s)", status, out)
	}
	if !strings.Contains(out, "4/5 probable Tailscale nodes reachable (80%)") {
		t.Errorf("output missing 4/5 summary:\n%s", out)
	}
}

func TestAllNodesReachabilityTest_Offline_Ignored(t *testing.T) {
	// An offline node is not probed — we only care about
	// online nodes (matching the B98 pattern).
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "cyborg-offline", IPAddresses: []string{"100.64.0.13"}, Online: false},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 30 * time.Millisecond},
	})

	status, out := allNodesReachabilityTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	if strings.Contains(out, "cyborg-offline") {
		t.Errorf("output should not mention offline node:\n%s", out)
	}
}

func TestAllNodesReachabilityTest_NoTailscaleIP_Skipped(t *testing.T) {
	// An online node with only a private IP — no Tailscale
	// IP to probe. Skipped, not failed.
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "broken-device", IPAddresses: []string{"192.168.99.1"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, nil)

	status, out := allNodesReachabilityTest.Run(context.Background())
	if status != SystemTestSkip {
		t.Errorf("status = %v, want SKIP (output: %s)", status, out)
	}
}

// --- vpsToVPSLatencyTest.Run --------------------------------------

func TestVpsToVPSLatencyTest_AllFast_Passes(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
		{ID: "4", GivenName: "skybars", IPAddresses: []string{"100.64.0.5"}, Online: true}, // home — should be filtered
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
		"100.64.0.2": {latency: 80 * time.Millisecond},
		"100.64.0.4": {latency: 100 * time.Millisecond},
	})

	status, out := vpsToVPSLatencyTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	// 3 VPS probed (skybars filtered out by vpsHostnameSet).
	if !strings.Contains(out, "3 VPS nodes probed:") {
		t.Errorf("output should probe 3 VPS nodes (skybars filtered):\n%s", out)
	}
	if strings.Contains(out, "skybars") {
		t.Errorf("output should not include home device skybars:\n%s", out)
	}
}

func TestVpsToVPSLatencyTest_OneSlow_PassesWithWarning(t *testing.T) {
	// slowThreshold = 1s. relay-3 at 1.5s is slow but not failed.
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
		"100.64.0.2": {latency: 80 * time.Millisecond},
		"100.64.0.4": {latency: 1500 * time.Millisecond},
	})

	status, out := vpsToVPSLatencyTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	if !strings.Contains(out, "SLOW") {
		t.Errorf("output missing SLOW block:\n%s", out)
	}
}

func TestVpsToVPSLatencyTest_OneFailed_Fails(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
		"100.64.0.2": {err: errors.New("i/o timeout")},
	})

	status, out := vpsToVPSLatencyTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want FAIL (output: %s)", status, out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("output missing FAILED block:\n%s", out)
	}
}

func TestVpsToVPSLatencyTest_LessThanTwoVPS_Skips(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "skybars", IPAddresses: []string{"100.64.0.5"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
	})

	status, out := vpsToVPSLatencyTest.Run(context.Background())
	if status != SystemTestSkip {
		t.Errorf("status = %v, want SKIP (output: %s)", status, out)
	}
	if !strings.Contains(out, "only 1 online VPS") {
		t.Errorf("output should mention how many VPS nodes found:\n%s", out)
	}
}

// --- splitSuspectedTest.Run ---------------------------------------

func TestSplitSuspectedTest_NoService(t *testing.T) {
	SetTestService(nil)
	status, _ := splitSuspectedTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want FAIL", status)
	}
}

func TestSplitSuspectedTest_NoNodes_Skips(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, nil)

	status, _ := splitSuspectedTest.Run(context.Background())
	if status != SystemTestSkip {
		t.Errorf("status = %v, want SKIP", status)
	}
}

func TestSplitSuspectedTest_AllReachable_Passes(t *testing.T) {
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
		"100.64.0.2": {latency: 80 * time.Millisecond},
		"100.64.0.4": {latency: 100 * time.Millisecond},
	})

	status, out := splitSuspectedTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS (output: %s)", status, out)
	}
	if !strings.Contains(out, "3 online / 3 reachable (100%)") {
		t.Errorf("output missing 3/3 summary:\n%s", out)
	}
}

func TestSplitSuspectedTest_OneUnreachable_Passes(t *testing.T) {
	// 1 unreachable is "phone asleep", not a split. Test
	// must PASS with the unreachable entry listed.
	// v1.3.16: use a VPS-class node (relay-1) as the
	// unreachable one — home-LAN-without-SSH hostnames
	// (skybars, skyworker, a71, olesya, nothing-phone-2)
	// are now SKIPPED by tailnetSkipHostnames() and never
	// appear in the probe set, so they can't exercise the
	// "1 unreachable is OK" branch.
	setUpTailnetSelfOverride(t, "skygate-host-1")
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
		{ID: "4", GivenName: "relay-1", IPAddresses: []string{"100.64.0.7"}, Online: true},
		// self + home-LAN — should be SKIPPED
		{ID: "5", GivenName: "skygate-host-1", IPAddresses: []string{"100.64.0.18"}, Online: true},
		{ID: "6", GivenName: "skybars", IPAddresses: []string{"100.64.0.5"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3": {latency: 50 * time.Millisecond},
		"100.64.0.2": {latency: 80 * time.Millisecond},
		"100.64.0.4": {latency: 100 * time.Millisecond},
		// 1 VPS unreachable (relay-1) — "phone asleep" case
		"100.64.0.7":  {err: errors.New("i/o timeout")},
		// self + home-LAN unreachable — should be SKIPPED
		"100.64.0.18": {err: errors.New("connection refused")},
		"100.64.0.5":  {err: errors.New("i/o timeout")},
	})

	status, out := splitSuspectedTest.Run(context.Background())
	if status != SystemTestPass {
		t.Errorf("status = %v, want PASS at 75%% (output: %s)", status, out)
	}
	if !strings.Contains(out, "UNREACHABLE") {
		t.Errorf("output should still list the unreachable node:\n%s", out)
	}
}

func TestSplitSuspectedTest_ManyUnreachable_Fails(t *testing.T) {
	// v1.3.16: only VPS-class nodes in the test set (home-
	// LAN-without-SSH are skipped by tailnetSkipHostnames).
	// 6 probable VPS nodes total, 3 reachable = 3/6 = 50%
	// < 90% threshold + 3 unreachable ≥ 2 → FAIL.
	setUpTailnetSelfOverride(t, "skygate-host-1")
	url, _ := fakeHS(t, []fakeHSNode{
		{ID: "1", GivenName: "emilia", IPAddresses: []string{"100.64.0.3"}, Online: true},
		{ID: "2", GivenName: "karolina", IPAddresses: []string{"100.64.0.2"}, Online: true},
		{ID: "3", GivenName: "sharlotta", IPAddresses: []string{"100.64.0.4"}, Online: true},
		{ID: "4", GivenName: "svyatoslava-1", IPAddresses: []string{"100.64.0.15"}, Online: true},
		{ID: "5", GivenName: "relay-1", IPAddresses: []string{"100.64.0.7"}, Online: true},
		{ID: "6", GivenName: "relay-2", IPAddresses: []string{"100.64.0.8"}, Online: true},
		// home-LAN — should be SKIPPED
		{ID: "7", GivenName: "skygate-host-1", IPAddresses: []string{"100.64.0.18"}, Online: true},
		{ID: "8", GivenName: "skybars", IPAddresses: []string{"100.64.0.5"}, Online: true},
		{ID: "9", GivenName: "skyworker", IPAddresses: []string{"100.64.0.1"}, Online: true},
		{ID: "10", GivenName: "a71", IPAddresses: []string{"100.64.0.19"}, Online: true},
	})
	setUpServiceWithFakeHS(t, url)
	setUpProbe(t, map[string]fakeProbeResult{
		"100.64.0.3":  {latency: 50 * time.Millisecond},
		"100.64.0.2":  {latency: 80 * time.Millisecond},
		"100.64.0.4":  {latency: 100 * time.Millisecond},
		// 3 VPS nodes unreachable (split signature)
		"100.64.0.15": {err: errors.New("i/o timeout")},
		"100.64.0.7":  {err: errors.New("i/o timeout")},
		"100.64.0.8":  {err: errors.New("i/o timeout")},
		// home-LAN unreachable — should be SKIPPED, not fail
		"100.64.0.18": {err: errors.New("connection refused")},
		"100.64.0.5":  {err: errors.New("i/o timeout")},
		"100.64.0.1":  {err: errors.New("i/o timeout")},
		"100.64.0.19": {err: errors.New("i/o timeout")},
	})

	status, out := splitSuspectedTest.Run(context.Background())
	if status != SystemTestFail {
		t.Errorf("status = %v, want FAIL on split signature (output: %s)", status, out)
	}
	if !strings.Contains(out, "LIKELY TAILNET SPLIT") {
		t.Errorf("output missing LIKELY TAILNET SPLIT warning:\n%s", out)
	}
	if !strings.Contains(out, "docs/tailnet-diagnostics.md") {
		t.Errorf("output should point operator at docs:\n%s", out)
	}
}
