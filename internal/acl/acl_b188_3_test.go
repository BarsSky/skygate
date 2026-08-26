// v1.5.2 (B188.3) — unit tests for resolvePerCIDRVia
// (the per-CIDR via= matching helper shared by both
// GenerateACLForPlane and GenerateACLWithViaForPlane).
//
// These tests pin the helper's contract without a DB
// dependency. The DB-level integration is exercised by:
//   * scripts/check_b188_2.sh contracts S-X (live policy
//     checks on the VM for the useVia=true path).
//   * scripts/check_b188_3.sh contracts (new — live policy
//     checks for the useVia=false path).
//
// Why a separate test file: the helper is the only piece
// of the B188.2 + B188.3 logic that is small enough to
// test without a DB roundtrip. Everything else is grant
// emission, which requires `openTestDB(t)` to seed
// device_rules + device_exit_node_prefs + node_owner_map.

package acl

import "testing"

// TestResolvePerCIDRVia covers the per-CIDR via= matching
// helper. The 8 case categories mirror the comments in
// the helper's docstring.
func TestResolvePerCIDRVia(t *testing.T) {
	cases := []struct {
		name        string
		devTag      string
		exitNodeID  string
		viaByDevice map[string]string
		want        string
	}{
		{
			name:       "happy path: per-device pref matches rule's exit",
			devTag:     "tag:dev-michail-basic",
			exitNodeID: "emilia",
			viaByDevice: map[string]string{
				"tag:dev-michail-basic": "tag:dev-infra-emilia",
			},
			want: "tag:dev-infra-emilia",
		},
		{
			name:       "happy path: karolina pref matches karolina rule",
			devTag:     "tag:dev-skyadmin-skyworker",
			exitNodeID: "karolina",
			viaByDevice: map[string]string{
				"tag:dev-skyadmin-skyworker": "tag:dev-infra-karolina",
			},
			want: "tag:dev-infra-karolina",
		},
		{
			name:       "no match: device pref is emilia but rule targets karolina",
			devTag:     "tag:dev-michail-basic",
			exitNodeID: "karolina",
			viaByDevice: map[string]string{
				"tag:dev-michail-basic": "tag:dev-infra-emilia",
			},
			want: "",
		},
		{
			name:        "no match: device has no per-device pref",
			devTag:      "tag:dev-michail-basic",
			exitNodeID:  "emilia",
			viaByDevice: map[string]string{}, // no per-device prefs
			want:        "",
		},
		{
			name:        "no match: rule has no exit_node_id (legacy rule, pre-v0.28.x)",
			devTag:      "tag:dev-michail-basic",
			exitNodeID:  "", // empty for legacy rules
			viaByDevice: map[string]string{"tag:dev-michail-basic": "tag:dev-infra-emilia"},
			want:        "",
		},
		{
			name:       "no match: devTag is empty (src=* wildcard rule, no per-device source)",
			devTag:     "", // no per-device src
			exitNodeID: "emilia",
			viaByDevice: map[string]string{
				"tag:dev-michail-basic": "tag:dev-infra-emilia",
			},
			want: "",
		},
		{
			name:       "no match: device pref is empty (defensive — should never happen)",
			devTag:     "tag:dev-michail-basic",
			exitNodeID: "emilia",
			viaByDevice: map[string]string{
				"tag:dev-michail-basic": "", // empty pref — shouldn't happen in production
			},
			want: "",
		},
		{
			name:       "no match: pref is the catch-all sentinel tag:exit-node (not a real hostname)",
			devTag:     "tag:dev-michail-basic",
			exitNodeID: "emilia", // rule targets emilia, NOT the catch-all
			viaByDevice: map[string]string{
				"tag:dev-michail-basic": "tag:exit-node", // headscale catch-all sentinel
			},
			want: "", // exitNodeTagToHostname("tag:exit-node") = "node", not "emilia"
		},
		{
			name:       "match: legacy tag:exit-emilia pref (post-B188 migration rewrote to tag:dev-infra-emilia, but old data may still exist briefly)",
			devTag:     "tag:dev-michail-basic",
			exitNodeID: "emilia",
			viaByDevice: map[string]string{
				"tag:dev-michail-basic": "tag:exit-emilia", // legacy pre-B188 form
			},
			want: "tag:exit-emilia", // exitNodeTagToHostname strips the "exit-" prefix too
		},
		{
			name:        "no viaByDevice nil-safety: nil map + matching hostname",
			devTag:      "tag:dev-michail-basic",
			exitNodeID:  "emilia",
			viaByDevice: nil, // nil map
			want:        "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolvePerCIDRVia(c.devTag, c.exitNodeID, c.viaByDevice)
			if got != c.want {
				t.Errorf("resolvePerCIDRVia(%q, %q, %v) = %q, want %q",
					c.devTag, c.exitNodeID, c.viaByDevice, got, c.want)
			}
		})
	}
}

// TestResolvePerCIDRVia_MultipleDevices exercises the
// map-isolation property: a via= for one device must not
// bleed into another device's grants. This is the
// "no cross-device leakage" guarantee.
func TestResolvePerCIDRVia_MultipleDevices(t *testing.T) {
	viaByDevice := map[string]string{
		"tag:dev-michail-basic":  "tag:dev-infra-emilia",
		"tag:dev-skyadmin-a71":   "tag:dev-infra-emilia",
		"tag:dev-skyadmin-cyborg": "tag:dev-infra-karolina",
	}
	// basic + a71 both want emilia; cyborg wants karolina.
	// A rule targeting karolina for basic should NOT pick
	// up emilia just because the map has emilia for basic.
	got := resolvePerCIDRVia("tag:dev-michail-basic", "karolina", viaByDevice)
	if got != "" {
		t.Errorf("basic→karolina should be no match (basic's pref is emilia), got %q", got)
	}
	// cyborg→karolina should match
	got = resolvePerCIDRVia("tag:dev-skyadmin-cyborg", "karolina", viaByDevice)
	if got != "tag:dev-infra-karolina" {
		t.Errorf("cyborg→karolina should match, got %q", got)
	}
	// cyborg→emilia should NOT match (cyborg's pref is karolina)
	got = resolvePerCIDRVia("tag:dev-skyadmin-cyborg", "emilia", viaByDevice)
	if got != "" {
		t.Errorf("cyborg→emilia should be no match, got %q", got)
	}
}
