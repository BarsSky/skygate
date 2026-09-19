// File: internal/derphealth/map_b265_test.go
//
// B265 (2026-09-19) — regression tests for the IsOwn inversion.
//
// Pre-B265 `FetchOwnDERPs` computed `d.IsOwn = isBundled == 0`,
// which marked the operator's OWN locally-hosted derper
// (is_bundled=1, region 900) as a public relay and the region-901
// row (a Tailscale derpmap URL) as the operator's own. Live
// evidence from the reference VM before the fix:
//
//	derp_health: region 901 (controlplane.tailscale.com) is_own=1
//	             region 900 (derp.skynas.ru, 12 ms)     is_own=0
//
// so /admin/derp/dashboard's "Recommended DERP" banner and the
// main-page "Active DERP" hero both selected region 901.
//
// No test covered IsOwn before this file, which is exactly why the
// inversion survived.

package derphealth

import "testing"

func TestDerpRelayIsOwn_BundledIsOwn(t *testing.T) {
	if !derpRelayIsOwn(1) {
		t.Errorf("derpRelayIsOwn(1) = false; a bundled row is the operator's own local derper (region 900)")
	}
}

func TestDerpRelayIsOwn_ExternalIsNotOwn(t *testing.T) {
	if derpRelayIsOwn(0) {
		t.Errorf("derpRelayIsOwn(0) = true; an external row (is_bundled=0) is not the operator's infrastructure")
	}
}

// TestDerpRelayIsOwn_NotInverted is the explicit guard against
// reintroducing the pre-B265 expression: the mapping must never be
// the negation of is_bundled.
func TestDerpRelayIsOwn_NotInverted(t *testing.T) {
	for _, isBundled := range []int{0, 1} {
		want := isBundled == 1
		if got := derpRelayIsOwn(isBundled); got != want {
			t.Errorf("derpRelayIsOwn(%d) = %v, want %v (pre-B265 code had `isBundled == 0`)", isBundled, got, want)
		}
	}
}
