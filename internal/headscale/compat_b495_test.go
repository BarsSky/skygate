// compat_b495_test.go — the capability table is a claim about versions, so it is pinned by versions.
//
// Every case below is a REAL line: the one running on the production host (0.29.3), upstream's newest
// release at the time of writing (0.29.7), the future line RR-5 waits for (0.30), and the shapes a
// probe can hand us that must NOT unlock anything (pre-release of the same minor, a pin, garbage).
package headscale

import (
	"strings"
	"testing"
)

// TestB495_CapabilitiesTable pins the table row by row.
func TestB495_CapabilitiesTable(t *testing.T) {
	cases := []struct {
		version      string
		wantKnown    bool
		wantExtra    bool
		wantPolicyDB bool
	}{
		{"0.29.3", true, false, true},     // the production host, measured 2026-11-18
		{"v0.29.3", true, false, true},    // the CLI prints a "v"
		{"0.29.7", true, false, true},     // upstream's newest release, measured 2026-11-18
		{"0.30.0", true, true, true},      // the line RR-5 waits for
		{"v0.30.0", true, true, true},     // same, as printed
		{"0.30.0-rc.1", true, true, true}, // a pre-release of that line already unlocks it
		{"0.26.0", true, false, true},     // the oldest line with the REST policy write
		{"0.25.4", true, false, false},    // older than the policy-database mode
		{"0.31.0", true, true, true},      // a later line cannot LOSE a capability
		{"1.0.0", true, true, true},       // a major bump is still "at least"
		{"", false, false, false},         // no answer
		{"nonsense", false, false, false},
		{"0", false, false, false},
		{"abc.def", false, false, false},
	}
	for _, tc := range cases {
		caps := CapabilitiesFor(tc.version)
		if caps.Known != tc.wantKnown {
			t.Errorf("CapabilitiesFor(%q).Known = %v, want %v", tc.version, caps.Known, tc.wantKnown)
		}
		if caps.ExtraRecords != tc.wantExtra {
			t.Errorf("CapabilitiesFor(%q).ExtraRecords = %v, want %v (per-user DNS needs %s+)",
				tc.version, caps.ExtraRecords, tc.wantExtra, MinVersionExtraRecords)
		}
		if caps.PolicyDatabase != tc.wantPolicyDB {
			t.Errorf("CapabilitiesFor(%q).PolicyDatabase = %v, want %v (needs %s+)",
				tc.version, caps.PolicyDatabase, tc.wantPolicyDB, MinVersionPolicyDatabase)
		}
	}
}

// TestB495_UnknownVersionClaimsNothing is the rule that keeps a broken probe from enabling a feature
// on an old daemon: "I could not read the version" must never mean "the newest one".
func TestB495_UnknownVersionClaimsNothing(t *testing.T) {
	for _, v := range []string{"", "  ", "unknown", "v", "0.x.1"} {
		caps := CapabilitiesFor(v)
		if caps.Known {
			t.Fatalf("CapabilitiesFor(%q) claims a known version", v)
		}
		if caps.ExtraRecords || caps.PolicyDatabase {
			t.Fatalf("CapabilitiesFor(%q) claims a capability without a version: %+v", v, caps)
		}
		if !strings.Contains(caps.Summary(), "unknown") {
			t.Fatalf("an unknown version must SAY so, got %q", caps.Summary())
		}
	}
}

// TestB495_SummaryNamesTheMinimum: the line the operator reads has to name what is missing and the
// version that fixes it, otherwise it is a "not supported" with no next step.
func TestB495_SummaryNamesTheMinimum(t *testing.T) {
	caps := CapabilitiesFor("0.29.3")
	s := caps.Summary()
	for _, want := range []string{"0.29.3", "no", MinVersionExtraRecords, MinVersionPolicyDatabase} {
		if !strings.Contains(s, want) {
			t.Fatalf("Summary() = %q, missing %q", s, want)
		}
	}
	if summary := CapabilitiesFor("0.30.0").Summary(); !strings.Contains(summary, "records yes") {
		t.Fatalf("0.30 must report per-user records as available, got %q", summary)
	}
}
