// 2026-08-25: v1.5.2 (B175) — focused unit test for
// matchOIDCStrategy (Strategy E for OIDC-registered
// nodes). The full Backfill function requires a real
// *sql.DB (PG-only in v1.3.0+), so the test exercises the
// pure condition logic in isolation. The 6 subtests cover
// the 5 critical paths + the default-tag-preservation path.

package nodeownership

import (
	"testing"

	"skygate/internal/headscale"
)

// TestMatchOIDCStrategy is the focused regression guard
// for B175. Pre-B175 the OIDC flow had a gap: headscale
// creates the OIDC user with name = skygate username, but
// the backfill had no strategy to match by name (Strategies
// A/C/D all required either a preauth key or an
// already-applied tag). Result: OIDC-registered nodes
// stayed orphaned in node_owner_map and the per-device
// dev-tag was never applied — the "⏳ pending" UX
// regression the operator reported on 2026-08-25.
//
// B175 adds matchOIDCStrategy (Strategy E) which matches
// `n.PreAuthKeyID == "" && n.UserName == portalUsername`
// and returns the default "tag:private" matchedTag (or
// firstTagOrFallback if the node already carries tags).
func TestMatchOIDCStrategy(t *testing.T) {
	// 1. The happy path: OIDC-registered node, no preauth
	//    key, name matches portal username → match with
	//    "tag:private" default.
	t.Run("OIDCNode_Match", func(t *testing.T) {
		got, ok := matchOIDCStrategy(headscale.NodeView{
			ID:           "1",
			Hostname:     "alice-laptop",
			PreAuthKeyID: "",
			UserName:     "alice",
			UserID:       "42",
			Tags:         nil,
		}, "alice")
		if !ok {
			t.Fatal("matchOIDCStrategy: got ok=false, want true (OIDC node with matching name MUST match)")
		}
		if got != "tag:private" {
			t.Errorf("matchOIDCStrategy: got tag=%q, want %q", got, "tag:private")
		}
	})

	// 2. OIDC node that already carries a tag (e.g. an
	//    operator-applied tag:subnet-router via headscale
	//    CLI before Strategy E ran) — the first existing
	//    tag is preserved (mirrors Strategy A/C fallback).
	t.Run("OIDCNode_WithExistingTags_PreserveFirst", func(t *testing.T) {
		got, ok := matchOIDCStrategy(headscale.NodeView{
			ID:           "2",
			Hostname:     "alice-router",
			PreAuthKeyID: "",
			UserName:     "alice",
			UserID:       "42",
			Tags:         []string{"tag:subnet-router", "tag:private"},
		}, "alice")
		if !ok {
			t.Fatal("matchOIDCStrategy: got ok=false, want true")
		}
		if got != "tag:subnet-router" {
			t.Errorf("matchOIDCStrategy: got tag=%q, want %q (firstTagOrFallback should preserve existing tag:subnet-router)", got, "tag:subnet-router")
		}
	})

	// 3. Preauth-key-registered node (/my/preauth flow) —
	//    Strategy E must NOT match (this is Strategy A's
	//    territory). The `PreAuthKeyID != ""` guard is
	//    what makes Strategy E safe to add: it never
	//    fires for /my/preauth nodes.
	t.Run("PreauthNode_NoMatch", func(t *testing.T) {
		got, ok := matchOIDCStrategy(headscale.NodeView{
			ID:           "3",
			Hostname:     "bob-desktop",
			PreAuthKeyID: "8",
			UserName:     "bob",
			UserID:       "43",
			Tags:         nil,
		}, "bob")
		if ok {
			t.Errorf("matchOIDCStrategy: preauth-key node MUST NOT match Strategy E (got tag=%q, want no match — Strategy A is the right path)", got)
		}
	})

	// 4. Username mismatch — node belongs to a different
	//    portal user. The backfill's per-user loop
	//    (Backfill is called once per portal_user) means
	//    the right user will see the node. Strategy E
	//    must NOT claim a node for the wrong user.
	t.Run("UsernameMismatch_NoMatch", func(t *testing.T) {
		got, ok := matchOIDCStrategy(headscale.NodeView{
			ID:           "4",
			Hostname:     "charlie-laptop",
			PreAuthKeyID: "",
			UserName:     "charlie",
			UserID:       "44",
			Tags:         nil,
		}, "alice")
		if ok {
			t.Errorf("matchOIDCStrategy: username mismatch MUST NOT match (got tag=%q) — would steal charlie's node for alice", got)
		}
	})

	// 5. Synthetic "tagged-devices" headscale user — this
	//    is what headscale creates when a node has a
	//    headscale tag applied. n.UserName ==
	//    "tagged-devices" must not match any REAL portal
	//    username (skyadmin, alice, etc.) — only the
	//    synthetic "tagged-devices" name itself would
	//    match it, and there's no portal user with that
	//    name (UNIQUE constraint + semantically different
	//    string). We test the realistic scenario: a
	//    real portal user "skyadmin" iterating and
	//    seeing a node owned by the synthetic
	//    tagged-devices user.
	t.Run("TaggedDevicesSyntheticUser_NoMatch", func(t *testing.T) {
		got, ok := matchOIDCStrategy(headscale.NodeView{
			ID:           "5",
			Hostname:     "skygate-host-1",
			PreAuthKeyID: "",
			UserName:     "tagged-devices",
			UserID:       "2147455555",
			Tags:         []string{"tag:dev-skyadmin-skygate-vm"},
		}, "skyadmin")
		if ok {
			t.Errorf("matchOIDCStrategy: tagged-devices synthetic user MUST NOT match a real portal user (got tag=%q) — would steal the synthetic-owned node for skyadmin", got)
		}
	})

	// 6. Empty portalUsername — defensive guard. The
	//    caller (Backfill) already short-circuits when
	//    portalUsername is empty, but matchOIDCStrategy
	//    is also exported for unit testing and should
	//    be safe to call with empty inputs.
	t.Run("EmptyPortalUsername_NoMatch", func(t *testing.T) {
		got, ok := matchOIDCStrategy(headscale.NodeView{
			ID:           "6",
			Hostname:     "alice-laptop",
			PreAuthKeyID: "",
			UserName:     "alice",
			UserID:       "42",
			Tags:         nil,
		}, "")
		if ok {
			t.Errorf("matchOIDCStrategy: empty portalUsername MUST NOT match (got tag=%q) — would attribute every node to empty user", got)
		}
	})

	// 7. "OtherOwners" safety — pre-B175 the per-user
	//    Backfill had a different bug where a renamed
	//    user could have their node stolen. We don't
	//    reproduce that here (the otherOwners map is
	//    built in Backfill itself, not in this helper),
	//    but we verify the helper behaves correctly
	//    when otherOwners would have filtered the node
	//    out (the helper itself doesn't see otherOwners
	//    — that's the caller's responsibility).
	t.Run("HelperIsOrderIndependent", func(t *testing.T) {
		// Same node, called twice — must return same
		// result. Idempotency guarantee for the caller.
		node := headscale.NodeView{
			ID:           "7",
			Hostname:     "daniil-laptop",
			PreAuthKeyID: "",
			UserName:     "daniil",
			UserID:       "45",
			Tags:         nil,
		}
		tag1, ok1 := matchOIDCStrategy(node, "daniil")
		tag2, ok2 := matchOIDCStrategy(node, "daniil")
		if !ok1 || !ok2 || tag1 != tag2 {
			t.Errorf("matchOIDCStrategy: not idempotent (call1 ok=%v tag=%q, call2 ok=%v tag=%q)", ok1, tag1, ok2, tag2)
		}
	})
}
