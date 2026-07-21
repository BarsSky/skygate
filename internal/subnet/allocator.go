// Package subnet — per-user personal subnets (v0.16.0+).
//
// The flat 100.64.0.0/10 design works for the operator's
// current ~10-user tailnet, but the moment skygate grows
// to multiple customers (multi-tenant SaaS), per-user
// subnets become the cleaner primitive. They give:
//   - IP-address predictability per user (user 42's
//     devices are always in 10.0.42.0/24)
//   - Cleaner user-side firewall rules
//     ("10.0.42.0/24 = my office")
//   - Independent routing decisions per user
//   - Foundation for per-user services (run a web
//     server on 10.0.42.5:8080, only that user reaches
//     it)
//
// v0.16.0 ships the schema + CIDR allocator + CRUD
// layer; the actual sidecar container management
// (start/stop tailscaled, tag node, approve routes)
// is the v0.16.1 follow-up.
//
// allocator.go — pure-function CIDR assignment.
//
// The allocator is the heart of v0.16.0. Given a user_id,
// it returns a deterministic CIDR (10.0.<user_id>.0/24 in
// v0.16.0). The function is pure (no DB access) so it
// can be unit-tested without a SQLite fixture; the
// manager (manager.go) wraps it with DB lookups to
// detect "already allocated" via the UNIQUE(user_id)
// constraint on user_subnets.
//
// Why deterministic (user_id → CIDR) instead of
// "first free slot"? Two reasons:
//  1. The result is stable across calls — /admin/users/{id}/subnet
//     shows the same CIDR every time the page is loaded
//     (no "find next free slot" race between concurrent
//     requests).
//  2. The mapping is easy to remember — user 42 is
//     always at 10.0.42.0/24. Operator can derive the
//     CIDR from the user_id without a DB lookup.
//
// The price of determinism is the 256-user cap (one
// /24 per user in the 10.0.0.0/16 range). For 4096
// users we'd switch to /28 per user, which needs a
// more complex allocation scheme (the schema's
// `subnet_bits` column is reserved for that future
// migration).
package subnet

import (
	"fmt"
	"net"
)

// DefaultSubnetBits is the per-user subnet width. 24
// bits = 256 addresses per subnet (254 usable), 256
// subnets in the 10.0.0.0/16 range. Bumped to 28 in a
// future release to support 4096 users (the schema's
// subnet_bits column is reserved for that).
const DefaultSubnetBits = 24

// MaxUserID is the highest user_id the allocator can
// serve. For /24 in 10.0.0.0/16 the range is 0..255
// (the third octet). The check is bits-independent —
// for /28 we'd cap at 4095 instead.
const MaxUserID int64 = 255

// ErrUserOutOfRange is returned when the user_id is
// outside the supported range (currently 0..255 for
// /24-per-user). Bumping to /28-per-user in the future
// raises this to 4095.
var ErrUserOutOfRange = fmt.Errorf("subnet: user_id out of range")

// AllocateCIDR returns the deterministic CIDR for a
// given user_id, using the /24-per-user scheme. The
// function is pure: same user_id always returns the
// same CIDR, no DB access. The DB constraint check
// happens in manager.go (where we can detect a
// "already allocated" error from the UNIQUE(user_id)
// index and translate it to "the user already has
// this CIDR, return the existing row").
//
// 10.0.<userID>.0/24 — the network address with /24
// mask, suitable for storing in user_subnets.cidr and
// passing to the headscale route advertisement. The
// 10.0.0.0/8 range is reserved for private use (RFC
// 1918) so it doesn't collide with the operator's
// existing 100.64.0.0/10 tailnet.
//
// Examples:
//   user_id=0   → 10.0.0.0/24
//   user_id=42  → 10.0.42.0/24
//   user_id=255 → 10.0.255.0/24
//   user_id=256 → error (out of range)
func AllocateCIDR(userID int64) (string, error) {
	if userID < 0 || userID > MaxUserID {
		return "", fmt.Errorf("%w: user_id=%d (max %d)", ErrUserOutOfRange, userID, MaxUserID)
	}
	// 10.0.<userID>.0/24 — three dotted octets, /24 mask.
	// The third octet is the userID (range-checked above).
	cidr := fmt.Sprintf("10.0.%d.0/%d", userID, DefaultSubnetBits)
	// Sanity-check the result. This is defensive — the
	// format string + range check should always produce
	// a valid CIDR, but ParseCIDR catches typos in the
	// format string (e.g. "10.0.300.0/24" would be caught
	// here even if a future change loosened the range).
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return "", fmt.Errorf("subnet: allocator produced invalid CIDR %q: %w", cidr, err)
	}
	return cidr, nil
}

// IsOutOfRangeError reports whether err is an
// ErrUserOutOfRange (a sentinel-error check so callers
// can map the allocator's error to a user-friendly
// admin message without string-matching the error
// text).
func IsOutOfRangeError(err error) bool {
	return err != nil && err.Error() != "" &&
		(len(err.Error()) >= len(ErrUserOutOfRange.Error())) &&
		(err.Error()[:len(ErrUserOutOfRange.Error())] == ErrUserOutOfRange.Error())
}
