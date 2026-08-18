// 2026-08-18: v1.3.20.9 (B139) — B17 real unit test (PG-free).
//
// Pre-B139 the B17 contract pinned the production code path
// + a t.Skip stub (in exit_nodes_tag_test.go). The t.Skip
// was a Phase 2 PG-rewrite follow-up placeholder. Since the
// production function (nodeTagRefusedForUserDevice) is a
// pure function (no DB, no headscale, no globals), the test
// can run without PG — we just call it directly with
// constructed inputs.
//
// The function enforces the v0.30.1 workstation-8 invariant:
// a node that already has a per-user device tag (tag:dev-*)
// cannot be tagged as an exit-node (tag:exit-*). The guard
// runs in PostAdminNodeTag before the headscale API call,
// so a misclick in /admin/devices doesn't accidentally
// promote a user's personal device to an exit-node.
//
// 4 cases:
//   1. requested tag is NOT tag:exit-* → not refused
//   2. requested tag IS tag:exit-*, but current tags have
//      NO tag:dev-* → not refused (valid use case)
//   3. requested tag IS tag:exit-*, current tags contain
//      tag:dev-user1 → refused with message
//   4. multiple tag:dev-* current tags, one of them is the
//      matching one → refused (uses the first match)
//
// Plus 2 edge cases:
//   5. empty current tags → not refused
//   6. current tags have non-dev-, non-exit- tags only → not refused

package admin

import (
	"strings"
	"testing"
)

func TestNodeTagRefusedForUserDevice(t *testing.T) {
	tests := []struct {
		name           string
		nodeID         int64
		requestedTag   string
		currentTags    []string
		wantRefused    bool
		wantMessageSub string // substring check (message is verbose)
	}{
		{
			name:         "non-exit tag is not refused",
			nodeID:       42,
			requestedTag: "tag:dev-michail-laptop",
			currentTags:  []string{"tag:dev-michail-laptop"},
			wantRefused:  false,
		},
		{
			name:         "exit tag with no dev-* current tags is allowed",
			nodeID:       42,
			requestedTag: "tag:exit-emilia",
			currentTags:  []string{},
			wantRefused:  false,
		},
		{
			name:           "exit tag with dev-* current tag is refused",
			nodeID:         42,
			requestedTag:   "tag:exit-emilia",
			currentTags:    []string{"tag:dev-michail-laptop"},
			wantRefused:    true,
			wantMessageSub: "tag:dev-michail-laptop",
		},
		{
			name:           "multiple current tags, one is dev-*: refused with first match",
			nodeID:         7,
			requestedTag:   "tag:exit-emilia",
			currentTags:    []string{"tag:dev-alice-phone", "tag:dev-bob-laptop", "tag:dev-charlie-pc"},
			wantRefused:    true,
			wantMessageSub: "tag:dev-alice-phone",
		},
		{
			name:         "empty current tags, exit request: not refused",
			nodeID:       1,
			requestedTag: "tag:exit-relay-1",
			currentTags:  nil,
			wantRefused:  false,
		},
		{
			name:         "only non-dev- non-exit- tags: not refused",
			nodeID:       1,
			requestedTag: "tag:exit-relay-1",
			currentTags:  []string{"tag:public", "tag:infra", "tag:other"},
			wantRefused:  false,
		},
		{
			name:           "v0.30.1 workstation-8 repro: dev-* AND exit- request",
			nodeID:         8,
			requestedTag:   "tag:exit-relay-3",
			currentTags:    []string{"tag:dev-workstation-8"},
			wantRefused:    true,
			wantMessageSub: "workstation-8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refused, message, existingDevTag := nodeTagRefusedForUserDevice(
				tt.nodeID, tt.requestedTag, tt.currentTags)

			if refused != tt.wantRefused {
				t.Errorf("refused: got %v want %v (message=%q, devTag=%q)",
					refused, tt.wantRefused, message, existingDevTag)
			}
			if tt.wantRefused && tt.wantMessageSub != "" {
				if !strings.Contains(message, tt.wantMessageSub) {
					t.Errorf("message should contain %q, got %q", tt.wantMessageSub, message)
				}
			}
			if !tt.wantRefused && message != "" {
				t.Errorf("not-refused case should have empty message, got %q", message)
			}
		})
	}
}

// TestNodeTagRefusedForUserDevice_EdgeCases — extra tests for
// the regression guard. These are the cases the operator's
// 2026-08-09 emilia-on-live-VM report exercised.
func TestNodeTagRefusedForUserDevice_EdgeCases(t *testing.T) {
	// Case A: requested tag with mixed case (the real headscale
	// CLI sometimes sends "tag:Exit-*" — note capital E).
	// The current implementation uses HasPrefix which is
	// CASE-SENSITIVE. This test pins that behavior so any
	// future change is intentional.
	t.Run("capital-E tag:Exit-* is NOT refused (case-sensitive prefix)", func(t *testing.T) {
		refused, _, _ := nodeTagRefusedForUserDevice(1, "tag:Exit-emilia", []string{"tag:dev-michail"})
		if refused {
			t.Errorf("tag:Exit-* (capital E) should NOT be refused — HasPrefix is case-sensitive. " +
				"If this starts passing, the guard was loosened to case-insensitive — that's a behavior change")
		}
	})

	// Case B: requested tag with extra characters after exit
	// (e.g. "tag:exit-"). HasPrefix("tag:exit-") matches.
	t.Run("empty exit node name is still refused if dev-* present", func(t *testing.T) {
		refused, msg, _ := nodeTagRefusedForUserDevice(1, "tag:exit-", []string{"tag:dev-michail"})
		if !refused {
			t.Errorf("tag:exit- with dev-* should still be refused (HasPrefix matches)")
		}
		if !strings.Contains(msg, "tag:exit-") {
			t.Errorf("message should mention the requested tag, got %q", msg)
		}
	})
}
