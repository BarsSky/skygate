// devices_b170_test.go — unit tests for the B170 (v1.5.2)
// expired-row sub-classification heuristic in
// internal/feature/my/devices.go.
//
// The heuristic is the small piece of logic that lets the
// /my/devices template disambiguate the three observable
// causes of an expired node (TTL ran out while offline /
// user ran `tailscale logout` / admin force-expired). The
// classification drives the small muted caption that the
// template renders under the red "expired" pill — see the
// comment on parseLastSeenAndClassify for the full design.
//
// These tests are pure (no DB, no headscale, no HTTP),
// so they run in <1ms each. The contracts pinned here:
//   1. Empty LastSeen → "no_activity"
//   2. Unparseable LastSeen → "no_activity"
//   3. LastSeen within 5 min of Expiry → "near_expiry"
//   4. LastSeen > 5 min before Expiry → "while_offline"
//   5. LastSeen > 5 min AFTER Expiry (rare but possible
//      if a clock skew / future-dated LastSeen lands in
//      headscale) → still "while_offline" (we use the
//      absolute delta, not signed)
//   6. The 5-min boundary is INCLUSIVE (|delta| == 5min
//      → "near_expiry", not "while_offline")
//   7. parseLastSeenAndClassify returns the parsed
//      time.Time when the input is valid (for reuse
//      from the template or future B-checks) and a zero
//      time when it is empty / unparseable
package my

import (
	"testing"
	"time"
)

// TestParseLastSeenAndClassify_NoActivity covers the two
// "we have no usable activity signal" cases:
//   - raw empty string (the headscale API omits LastSeen
//     for nodes that have never sent a single MapRequest —
//     happens for snapshot-only / pre-auth rows)
//   - malformed timestamp (defense-in-depth: headscale
//     should always return RFC3339Nano, but a regression
//     in the headscale API or a clock drift could break
//     the format)
func TestParseLastSeenAndClassify_NoActivity(t *testing.T) {
	expiry := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)

	// Empty string.
	ls, hint := parseLastSeenAndClassify(expiry, "", time.Now())
	if hint != "no_activity" {
		t.Errorf("empty LastSeen: hint = %q, want %q", hint, "no_activity")
	}
	if !ls.IsZero() {
		t.Errorf("empty LastSeen: parsed time = %v, want zero", ls)
	}

	// Malformed string.
	ls, hint = parseLastSeenAndClassify(expiry, "not-a-timestamp", time.Now())
	if hint != "no_activity" {
		t.Errorf("malformed LastSeen: hint = %q, want %q", hint, "no_activity")
	}
	if !ls.IsZero() {
		t.Errorf("malformed LastSeen: parsed time = %v, want zero", ls)
	}
}

// TestParseLastSeenAndClassify_NearExpiry pins the
// "device was online at the moment expiry was set" case —
// the operator's primary use case (a `tailscale logout`
// from an active device). Sub-cases:
//   - 0-second delta (logout triggered the same instant
//     as the last ping, e.g. the user was idle for
//     hours then ran logout just as a keepalive fired)
//   - 1-second delta (typical for a quick logout from
//     a client that just finished a state sync)
//   - 4 min 59 sec delta (slow client, just under the
//     5-min threshold — still classified as near_expiry)
//   - 5 min 0 sec delta (EXACTLY on the threshold — the
//     5-min window is inclusive, so this is still
//     near_expiry)
func TestParseLastSeenAndClassify_NearExpiry(t *testing.T) {
	expiry := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		lastSeen time.Time
	}{
		{"exact_same_moment", expiry},
		{"one_second_before", expiry.Add(-1 * time.Second)},
		{"one_minute_before", expiry.Add(-1 * time.Minute)},
		{"just_under_threshold", expiry.Add(-(5*time.Minute - 1*time.Second))},
		{"exactly_on_threshold", expiry.Add(-5 * time.Minute)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ls, hint := parseLastSeenAndClassify(expiry, c.lastSeen.Format(time.RFC3339Nano), time.Now())
			if hint != "near_expiry" {
				t.Errorf("delta from %s to %s: hint = %q, want %q",
					c.lastSeen, expiry, hint, "near_expiry")
			}
			if !ls.Equal(c.lastSeen) {
				t.Errorf("returned time = %v, want %v", ls, c.lastSeen)
			}
		})
	}
}

// TestParseLastSeenAndClassify_WhileOffline pins the
// "device was offline when the TTL ran out" case — the
// typical "I forgot to renew" flow + the admin
// force-expired-a-long-idle-device case. Sub-cases:
//   - 5 min 1 sec delta (just over the threshold — must
//     flip to while_offline)
//   - 1 hour delta
//   - 30 days delta (the canonical "TTL ran out while
//     device was offline" — LastSeen is exactly 30 days
//     before the original TTL deadline)
//   - LastSeen AFTER Expiry (rare; happens if a future
//     headscale clock skew or a forced ping after a
//     logout. The heuristic must still classify this as
//     while_offline — we use absolute delta, not signed.)
func TestParseLastSeenAndClassify_WhileOffline(t *testing.T) {
	expiry := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		lastSeen time.Time
	}{
		{"just_over_threshold", expiry.Add(-(5*time.Minute + 1*time.Second))},
		{"one_hour_before", expiry.Add(-1 * time.Hour)},
		{"thirty_days_before", expiry.Add(-30 * 24 * time.Hour)},
		{"one_hour_after", expiry.Add(1 * time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ls, hint := parseLastSeenAndClassify(expiry, c.lastSeen.Format(time.RFC3339Nano), time.Now())
			if hint != "while_offline" {
				t.Errorf("delta from %s to %s: hint = %q, want %q",
					c.lastSeen, expiry, hint, "while_offline")
			}
			if !ls.Equal(c.lastSeen) {
				t.Errorf("returned time = %v, want %v", ls, c.lastSeen)
			}
		})
	}
}

// TestParseLastSeenAndClassify_NanoPrecision is a
// regression guard for the RFC3339Nano parse path:
// headscale returns timestamps with sub-second precision
// (the "Nano" suffix in time.RFC3339Nano), and a parser
// that drops the sub-second digits would mis-classify a
// LastSeen that's 5min+0.5s before Expiry as "near_expiry"
// instead of "while_offline" (or vice versa). We pin the
// behaviour by feeding in a 5min+0.5s delta and asserting
// it's "while_offline".
func TestParseLastSeenAndClassify_NanoPrecision(t *testing.T) {
	expiry := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	// 5 minutes + 500 milliseconds before expiry — clearly
	// over the 5-min threshold, but only by 500ms. A
	// naive parser that truncated to whole seconds might
	// report this as a 5min delta (= "near_expiry" by
	// the inclusive boundary). The Nano-aware parse must
	// classify it as "while_offline".
	lastSeen := expiry.Add(-(5*time.Minute + 500*time.Millisecond))
	_, hint := parseLastSeenAndClassify(expiry, lastSeen.Format(time.RFC3339Nano), time.Now())
	if hint != "while_offline" {
		t.Errorf("5min+0.5s delta: hint = %q, want %q (Nano precision must NOT round to whole seconds)",
			hint, "while_offline")
	}
}
