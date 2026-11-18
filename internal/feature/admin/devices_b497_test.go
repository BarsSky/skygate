package admin

import (
	"testing"

	"skygate/internal/db"
	"skygate/internal/exitpin"
)

// TestB497_PinsResolveTheDevicesOwnOwner is the regression for the defect this
// block was opened on: the page merged every user's per-device preferences into
// ONE map keyed by hostname, so a preference saved for `laptop` by user 1 was
// rendered as user 2's answer for a different device that happened to share the
// name. Live shape: a Michigan phone showing `emilia` (skyadmin's relay) while its
// own owner's preference was `karolina`.
func TestB497_PinsResolveTheDevicesOwnOwner(t *testing.T) {
	owners := map[string]int64{"laptop": 1, "phone": 2}
	devTags := map[string]string{
		"laptop": "tag:dev-alice-laptop",
		"phone":  "tag:dev-bob-phone",
	}
	devicePrefs := map[string]db.DeviceExitNodePref{
		// ONLY alice's laptop has a preference.
		b497PrefKey(1, "laptop"): {
			UserID: 1, DeviceHostname: "laptop",
			ExitNodeTag: "tag:dev-infra-emilia", ViaEnabled: true, SetByUserID: 1,
		},
	}
	userPrefs := map[int64]db.ExitNodePref{
		2: {UserID: 2, ExitNodeTag: "tag:dev-infra-karolina", ViaEnabled: true, SetByUserID: 2},
	}
	pins := b497Pins(owners, devTags, devicePrefs, userPrefs, map[string]string{
		"emilia": "online", "karolina": "online",
	})

	if got := pins["laptop"]; got.Source != exitpin.SourceDevice || got.Tag != "tag:dev-infra-emilia" || !got.Applies {
		t.Fatalf("alice's laptop = %+v, want her own device pin", got)
	}
	// bob's phone is TAGGED, so his per-user preference is stored but never
	// applied — the live olesya/A71 shape. It must NOT inherit alice's pin either.
	got := pins["phone"]
	if got.Tag != "tag:dev-infra-karolina" {
		t.Fatalf("bob's phone inherited another user's pin: %+v", got)
	}
	if got.Applies {
		t.Fatalf("a per-user pin on a tagged device reported APPLIED: %+v", got)
	}
	if got.Source != exitpin.SourceUser || got.Reason != exitpin.ReasonTaggedNoUser {
		t.Fatalf("bob's phone = %+v, want source=user reason=%s", got, exitpin.ReasonTaggedNoUser)
	}
}

// TestB497_PinsSkipDevicesWithoutAnOwner: guessing an owner is worse than saying
// nothing, because a wrong owner turns somebody else's preference into an answer
// the operator will act on.
func TestB497_PinsSkipDevicesWithoutAnOwner(t *testing.T) {
	pins := b497Pins(
		map[string]int64{"attributed": 1},
		map[string]string{"attributed": "tag:dev-alice-attributed", "orphan": "tag:dev-x-orphan"},
		map[string]db.DeviceExitNodePref{
			b497PrefKey(1, "orphan"): {UserID: 1, DeviceHostname: "orphan", ExitNodeTag: "tag:dev-infra-emilia", ViaEnabled: true, SetByUserID: 1},
		},
		nil, map[string]string{"emilia": "online"},
	)
	if got := pins["attributed"]; got.Source != exitpin.SourceNone || got.Tag != "" {
		t.Fatalf("attributed device = %+v, want a free choice", got)
	}
	if _, ok := pins["orphan"]; ok {
		t.Fatalf("an unattributed device got a pin: %+v", pins["orphan"])
	}
}

// TestB497_PinsWithholdAnUnhealthyRelay is the B356 half seen from the page: the
// row is real, the policy withholds the `via`, and the operator must be able to
// read that on screen instead of inferring it from a broken device.
func TestB497_PinsWithholdAnUnhealthyRelay(t *testing.T) {
	pins := b497Pins(
		map[string]int64{"basic": 1},
		map[string]string{"basic": "tag:dev-michail-basic"},
		map[string]db.DeviceExitNodePref{
			b497PrefKey(1, "basic"): {UserID: 1, DeviceHostname: "basic", ExitNodeTag: "tag:dev-infra-karolina", ViaEnabled: true, SetByUserID: 1},
		},
		nil, map[string]string{"karolina": "offline"},
	)
	got := pins["basic"]
	if got.Applies || got.Reason != exitpin.ReasonRelayDown {
		t.Fatalf("basic = %+v, want a withheld pin (%s)", got, exitpin.ReasonRelayDown)
	}
	// The stored preference is unchanged and still names the relay: the page says
	// «stored, not applied», it does not pretend the choice was never made.
	if b497PrefByHostname(pins)["basic"] != "karolina" {
		t.Fatalf("dead-rule comparison lost the stored preference: %v", b497PrefByHostname(pins))
	}
}

// TestB497_PrefKeyIsCaseInsensitive pins the key spelling: headscale reports a
// hostname in the node's own case while node_owner_map lowercases it, and a key
// that differs by case is a preference that stops being found.
func TestB497_PrefKeyIsCaseInsensitive(t *testing.T) {
	if b497PrefKey(7, " MacBook-Pro ") != b497PrefKey(7, "macbook-pro") {
		t.Fatalf("pref key is case/space sensitive: %q vs %q",
			b497PrefKey(7, " MacBook-Pro "), b497PrefKey(7, "macbook-pro"))
	}
	if b497PrefKey(7, "host") == b497PrefKey(8, "host") {
		t.Fatalf("pref key ignores the owner")
	}
}
