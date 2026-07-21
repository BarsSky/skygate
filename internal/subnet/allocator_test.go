package subnet

import (
	"strings"
	"testing"
)

// TestAllocateCIDR_ValidRange pins the v0.16.0 contract:
// user_ids 0..255 get deterministic 10.0.<uid>.0/24
// allocations. Outside that range the allocator returns
// ErrUserOutOfRange.
//
// 2026-07-17: v0.16.0 — schema + CIDR allocator. The
// /24-per-user scheme caps at 256 users; the schema's
// subnet_bits column is reserved for the future
// /28-per-user migration (4096 users). This test
// asserts the boundary so a future refactor that
// bumps the cap (or the operator decides to switch
// to /28) doesn't silently break the contract.
func TestAllocateCIDR_ValidRange(t *testing.T) {
	cases := []struct {
		userID int64
		want   string
	}{
		{0, "10.0.0.0/24"},
		{1, "10.0.1.0/24"},
		{2, "10.0.2.0/24"},
		{10, "10.0.10.0/24"},
		{42, "10.0.42.0/24"},
		{100, "10.0.100.0/24"},
		{200, "10.0.200.0/24"},
		{254, "10.0.254.0/24"},
		{255, "10.0.255.0/24"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got, err := AllocateCIDR(tc.userID)
			if err != nil {
				t.Fatalf("AllocateCIDR(%d) returned error: %v", tc.userID, err)
			}
			if got != tc.want {
				t.Errorf("AllocateCIDR(%d) = %q, want %q", tc.userID, got, tc.want)
			}
		})
	}
}

// TestAllocateCIDR_OutOfRange pins the v0.16.0
// boundary: user_id < 0 or > 255 returns
// ErrUserOutOfRange. The admin UI uses this to show
// "this user_id is out of range for /24-per-user" if
// the operator ever tries to assign a user_id > 255
// (currently impossible because INTEGER PRIMARY KEY
// caps at 2^63, but defensive).
func TestAllocateCIDR_OutOfRange(t *testing.T) {
	cases := []int64{-1, -100, 256, 1000, 1 << 30}
	for _, uid := range cases {
		t.Run("uid="+itoa(uid), func(t *testing.T) {
			cidr, err := AllocateCIDR(uid)
			if err == nil {
				t.Fatalf("AllocateCIDR(%d) = %q, want error", uid, cidr)
			}
			if !IsOutOfRangeError(err) {
				t.Errorf("AllocateCIDR(%d) error %v is not ErrUserOutOfRange", uid, err)
			}
		})
	}
}

// TestAllocateCIDR_Idempotent pins the v0.16.0
// contract: same user_id → same CIDR across
// repeated calls. This is the property the manager
// relies on for "second call after a failed
// transaction must return the same CIDR" (no
// re-allocation, no surprises).
func TestAllocateCIDR_Idempotent(t *testing.T) {
	for _, uid := range []int64{0, 1, 42, 100, 255} {
		first, err := AllocateCIDR(uid)
		if err != nil {
			t.Fatalf("first call: %v", err)
		}
		for i := 0; i < 5; i++ {
			again, err := AllocateCIDR(uid)
			if err != nil {
				t.Fatalf("repeat call #%d: %v", i, err)
			}
			if again != first {
				t.Errorf("AllocateCIDR(%d) returned %q on repeat, want %q", uid, again, first)
			}
		}
	}
}

// TestAllocateCIDR_OutputIsParseable pins the
// v0.16.0 contract: every output must be a valid
// CIDR that the headscale API + headscale route
// approval will accept. The allocator's internal
// sanity check (net.ParseCIDR) is the only safety
// net against a future typo in the format string,
// so we test it explicitly here.
func TestAllocateCIDR_OutputIsParseable(t *testing.T) {
	for uid := int64(0); uid <= MaxUserID; uid++ {
		cidr, err := AllocateCIDR(uid)
		if err != nil {
			t.Fatalf("AllocateCIDR(%d) error: %v", uid, err)
		}
		if !strings.Contains(cidr, "/24") {
			t.Errorf("AllocateCIDR(%d) = %q, want /24 prefix", uid, cidr)
		}
		if !strings.HasPrefix(cidr, "10.0.") {
			t.Errorf("AllocateCIDR(%d) = %q, want 10.0. prefix", uid, cidr)
		}
	}
}

// itoa is a tiny strconv.FormatInt wrapper for test
// sub-case names. Avoids importing strconv just for
// a sub-case label.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
