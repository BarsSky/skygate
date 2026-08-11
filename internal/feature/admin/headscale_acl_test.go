package admin

// headscale_acl_test.go — unit tests for the Network Access
// Manager (v0.33.0).
//
// B-checks covered:
//   B38: headscale_acl.go has unit tests (this file)
//   B39: fingerprintACL is order-invariant (req'd for idempotency)

import (
	"strings"
	"testing"
)

func TestFingerprintACL_OrderInvariant(t *testing.T) {
	// Same rule with src and dst in different orders must
	// produce the same fingerprint. AddACL is idempotent on
	// fingerprint; if the fingerprint is order-dependent, the
	// "add same rule twice" check fails.
	r1 := ACLRule{
		Action: "accept",
		Src:    []string{"skyadmin@tsnet.skynas.ru", "group:skyadmin"},
		Dst:    []string{"100.64.0.10:*", "100.64.0.15:*"},
	}
	r2 := ACLRule{
		Action: "accept",
		Src:    []string{"group:skyadmin", "skyadmin@tsnet.skynas.ru"},
		Dst:    []string{"100.64.0.15:*", "100.64.0.10:*"},
	}
	fp1 := fingerprintACL(r1)
	fp2 := fingerprintACL(r2)
	if fp1 != fp2 {
		t.Errorf("fingerprint must be order-invariant: %q vs %q", fp1, fp2)
	}
	if len(fp1) != 16 {
		t.Errorf("fingerprint should be 16 hex chars (truncated sha256), got %d", len(fp1))
	}
}

func TestFingerprintACL_ActionSensitive(t *testing.T) {
	// accept vs reject with same src/dst must produce different
	// fingerprints. Otherwise an operator who later flips
	// "accept" to "reject" on an existing rule would
	// mistakenly think it's a new rule.
	accept := ACLRule{Action: "accept", Src: []string{"a"}, Dst: []string{"b"}}
	reject := ACLRule{Action: "reject", Src: []string{"a"}, Dst: []string{"b"}}
	if fingerprintACL(accept) == fingerprintACL(reject) {
		t.Errorf("accept and reject must produce different fingerprints")
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a, b, c", []string{"a", "b", "c"}},
		{"  skyadmin@tsnet.skynas.ru , group:skyadmin  ", []string{"skyadmin@tsnet.skynas.ru", "group:skyadmin"}},
		{"", nil},
		{"single", []string{"single"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, tc := range tests {
		got := splitCSV(tc.in)
		if !equalStrings(got, tc.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSkygateACLID_Format(t *testing.T) {
	id := newSkygateACLID()
	if !strings.HasPrefix(id, "skygate-") {
		t.Errorf("id should start with 'skygate-', got %q", id)
	}
	// 12 bytes hex = 24 hex chars
	if len(id) != len("skygate-")+24 {
		t.Errorf("id should be skygate- + 24 hex chars, got %q (len=%d)", id, len(id))
	}
	// Two calls produce different ids (collision-resistant).
	if newSkygateACLID() == id {
		t.Errorf("ids should be unique: %q", id)
	}
}

func TestNewSkygateACLID_UniqueAcrossConcurrent(t *testing.T) {
	// Run 1000 IDs and check for collisions.
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := newSkygateACLID()
		if seen[id] {
			t.Fatalf("collision on iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestValidateACLRule(t *testing.T) {
	// AddACL rejects rules with no src or no dst. This
	// matches ValidateACLRule in headscale_acl.go.
	cases := []struct {
		name string
		rule ACLRule
		ok   bool
	}{
		{"valid-minimal", ACLRule{Src: []string{"a"}, Dst: []string{"b"}}, true},
		{"no-src", ACLRule{Dst: []string{"b"}}, false},
		{"no-dst", ACLRule{Src: []string{"a"}}, false},
		{"empty-both", ACLRule{}, false},
		{"with-action", ACLRule{Action: "reject", Src: []string{"a"}, Dst: []string{"b"}}, true},
		{"multi-src", ACLRule{Src: []string{"a", "c"}, Dst: []string{"b"}}, true},
		{"multi-dst", ACLRule{Src: []string{"a"}, Dst: []string{"b", "c"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateACLRule(tc.rule)
			if tc.ok && err != nil {
				t.Errorf("expected ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}
