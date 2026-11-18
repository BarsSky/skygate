package exitpin

import (
	"testing"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// b497Human is the portal user id of an operator-saved preference (any > 0).
const b497Human int64 = 6

func b497Ptr(p Pref) *Pref { return &p }

// TestB497_EffectivePinTable is the whole rule set as a table. Each row is one
// combination the ACL generator distinguishes, named by the live symptom the
// operator reported rather than by the branch it takes.
func TestB497_EffectivePinTable(t *testing.T) {
	karolina := Pref{Tag: "tag:dev-infra-karolina", ViaEnabled: true, SetByUserID: b497Human}
	emilia := Pref{Tag: "tag:dev-infra-emilia", ViaEnabled: true, SetByUserID: b497Human}

	cases := []struct {
		name string
		in   Inputs
		want Pin
	}{
		{
			// The baseline the operator calls «работает правильно»: an explicit
			// device pin written by a human, strict mode on, relay online.
			name: "device pin applies",
			in: Inputs{
				Device: b497Ptr(karolina), DeviceTag: "tag:dev-michail-basic",
				DeviceRelayState: "online",
			},
			want: Pin{Source: SourceDevice, Tag: karolina.Tag, Applies: true},
		},
		{
			// B356: the row survives, the pin does not — otherwise the device
			// loses egress entirely.
			name: "device pin withheld when the relay is down",
			in: Inputs{
				Device: b497Ptr(karolina), DeviceTag: "tag:dev-michail-basic",
				DeviceRelayState: "offline",
			},
			want: Pin{Source: SourceDevice, Tag: karolina.Tag, Reason: ReasonRelayDown},
		},
		{
			// «never measured» is not «broken»: absence of evidence keeps the pin.
			name: "device pin kept when the relay was never measured",
			in: Inputs{
				Device: b497Ptr(karolina), DeviceTag: "tag:dev-michail-basic",
				DeviceRelayState: "",
			},
			want: Pin{Source: SourceDevice, Tag: karolina.Tag, Applies: true},
		},
		{
			// An untagged relay WORKS (B273) — the tag is advisory, not a health
			// signal, so the pin stands.
			name: "device pin applies through an untagged relay",
			in: Inputs{
				Device: b497Ptr(karolina), DeviceTag: "tag:dev-michail-basic",
				DeviceRelayState: "untagged",
			},
			want: Pin{Source: SourceDevice, Tag: karolina.Tag, Applies: true},
		},
		{
			// B398: skygate's own derivation must never become a permission
			// filter — this is the row that made the operator's emilia choice
			// fail with «нет доступа ни к чему».
			name: "derived device row does not pin",
			in: Inputs{
				Device:           b497Ptr(Pref{Tag: emilia.Tag, ViaEnabled: true, SetByUserID: 0}),
				DeviceTag:        "tag:dev-skyadmin-cyborg",
				DeviceRelayState: "online",
			},
			want: Pin{Source: SourceDevice, Tag: emilia.Tag, Reason: ReasonDerived},
		},
		{
			// v0.28.5: strict mode off means «this device may use any relay»,
			// which is the Android-friendly default.
			name: "via disabled does not pin",
			in: Inputs{
				Device:    b497Ptr(Pref{Tag: karolina.Tag, ViaEnabled: false, SetByUserID: b497Human}),
				DeviceTag: "tag:dev-michail-basic",
			},
			want: Pin{Source: SourceDevice, Tag: karolina.Tag, Reason: ReasonViaOff},
		},
		{
			// B284: no tag in node_owner_map → no per-device grant can be
			// emitted, because the generator cannot declare the tag.
			name: "device pin without a device tag",
			in: Inputs{
				Device: b497Ptr(karolina), DeviceTag: "",
			},
			want: Pin{Source: SourceDevice, Tag: karolina.Tag, Reason: ReasonNoDeviceTag},
		},
		{
			// THE LIVE CASE (olesya, A71): michail's per-user preference is real
			// and saved, and the node is TAGGED, so headscale never matches it.
			name: "user pin does not reach a tagged device",
			in: Inputs{
				User: b497Ptr(karolina), UserRelayState: "online",
				DeviceTag: "tag:dev-michail-olesya", Tagged: true,
			},
			want: Pin{Source: SourceUser, Tag: karolina.Tag, Reason: ReasonTaggedNoUser},
		},
		{
			// The same preference DOES apply to an untagged node (a device
			// registered before the tag backfill, or a relay-side user node).
			name: "user pin applies to an untagged device",
			in: Inputs{
				User: b497Ptr(karolina), UserRelayState: "online",
				DeviceTag: "", Tagged: false,
			},
			want: Pin{Source: SourceUser, Tag: karolina.Tag, Applies: true},
		},
		{
			name: "user pin with strict mode off does not pin",
			in: Inputs{
				User:      b497Ptr(Pref{Tag: karolina.Tag, ViaEnabled: false, SetByUserID: b497Human}),
				DeviceTag: "tag:dev-michail-basic", Tagged: true,
			},
			want: Pin{Source: SourceUser, Tag: karolina.Tag, Reason: ReasonViaOff},
		},
		{
			name: "user pin withheld when the relay is down",
			in: Inputs{
				User: b497Ptr(karolina), UserRelayState: "degraded",
				DeviceTag: "tag:dev-michail-basic", Tagged: true,
			},
			want: Pin{Source: SourceUser, Tag: karolina.Tag, Reason: ReasonTaggedNoUser},
		},
		{
			// No preference anywhere: the device chooses, and the ACL's loose
			// grant is what makes that true.
			name: "no preference is a free choice, not a dash",
			in: Inputs{
				DeviceTag: "tag:dev-michail-olesya", Tagged: true,
			},
			want: Pin{Source: SourceNone, Reason: ReasonFreeChoice},
		},
		{
			// B399: the operator turned auto-derivation off by hand; that is a
			// decision and the page must say so instead of showing an empty cell.
			name: "opted out is still a free choice",
			in: Inputs{
				Device:    b497Ptr(Pref{OptedOut: true}),
				DeviceTag: "tag:dev-skyadmin-cyborg",
			},
			want: Pin{Source: SourceNone, Reason: ReasonFreeChoice},
		},
		{
			// A half-written row (no tag) must not be mistaken for «the device
			// picks» — it is a stored row that names nothing.
			name: "row without a relay",
			in: Inputs{
				Device:    b497Ptr(Pref{ViaEnabled: true, SetByUserID: b497Human}),
				DeviceTag: "tag:dev-michail-basic",
			},
			want: Pin{Source: SourceNone, Reason: ReasonNoRelay},
		},
		{
			// The device row wins over the user row, always — including when it
			// is the row that does NOT apply. Two people's preferences must not
			// be blended into a third answer.
			name: "device row wins over the user row",
			in: Inputs{
				Device:           b497Ptr(Pref{Tag: emilia.Tag, ViaEnabled: true, SetByUserID: b497Human}),
				DeviceTag:        "tag:dev-michail-olesya",
				DeviceRelayState: "online",
				User:             b497Ptr(karolina),
				UserRelayState:   "online",
				Tagged:           true,
			},
			want: Pin{Source: SourceDevice, Tag: emilia.Tag, Applies: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Effective(tc.in)
			if got != tc.want {
				t.Fatalf("Effective(%+v)\n got: %+v\nwant: %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestB497_PinAccessors pins the small helpers the template calls, because a
// template that reads a wrong field renders a wrong sentence and no test of
// Effective() would notice.
func TestB497_PinAccessors(t *testing.T) {
	applied := Effective(Inputs{
		Device:           b497Ptr(Pref{Tag: "tag:dev-infra-karolina", ViaEnabled: true, SetByUserID: b497Human}),
		DeviceTag:        "tag:dev-michail-basic",
		DeviceRelayState: "online",
	})
	if !applied.Applied() || applied.Free() || applied.ReasonKey() != "" {
		t.Fatalf("applied pin misreported: %+v (Applied=%v Free=%v ReasonKey=%q)",
			applied, applied.Applied(), applied.Free(), applied.ReasonKey())
	}
	if applied.SourceKey() != "exitpin.source.device" {
		t.Fatalf("SourceKey = %q, want exitpin.source.device", applied.SourceKey())
	}

	withheld := Effective(Inputs{
		User: b497Ptr(Pref{Tag: "tag:dev-infra-karolina", ViaEnabled: true, SetByUserID: b497Human}),
		// Tagged on purpose: this is the live shape, and the Reason token must be
		// the one the i18n catalogues actually define.
		Tagged: true, DeviceTag: "tag:dev-michail-olesya",
	})
	if withheld.Applied() {
		t.Fatalf("a per-user pin on a tagged node reported APPLIED: %+v", withheld)
	}
	if withheld.ReasonKey() != "exitpin.reason.tagged_no_user_src" {
		t.Fatalf("ReasonKey = %q, want exitpin.reason.tagged_no_user_src", withheld.ReasonKey())
	}
	if withheld.SourceKey() != "exitpin.source.user" {
		t.Fatalf("SourceKey = %q, want exitpin.source.user", withheld.SourceKey())
	}

	free := Effective(Inputs{DeviceTag: "tag:dev-michail-olesya", Tagged: true})
	if !free.Free() || free.Applied() {
		t.Fatalf("free choice misreported: %+v", free)
	}
	if free.ReasonKey() != "exitpin.reason.free_choice" {
		t.Fatalf("ReasonKey = %q, want exitpin.reason.free_choice", free.ReasonKey())
	}
}

// TestB497_EveryReasonHasAnI18nKey is the link the B325 ratchet cannot make:
// the Reason tokens above are assembled into keys at render time
// ("exitpin.reason." + Reason), so a typo here would be invisible to both the
// parity test and the ratchet. Assert the exact key set the template may ask for.
func TestB497_EveryReasonHasAnI18nKey(t *testing.T) {
	want := map[string]bool{
		ReasonDerived:      true,
		ReasonViaOff:       true,
		ReasonRelayDown:    true,
		ReasonNoDeviceTag:  true,
		ReasonTaggedNoUser: true,
		ReasonFreeChoice:   true,
		ReasonNoRelay:      true,
	}
	// i18n.Catalog.T returns the key itself when it is missing, which is exactly
	// the probe: a defined key never equals its own name in either language. The
	// GLOBAL catalog is installed by handlers.New and is nil in a unit test, so
	// build the bundled one directly.
	catalog := i18n.New()
	defined := func(key string) bool {
		for _, lang := range []string{"ru", "en"} {
			if catalog.T(lang, key) == key {
				return false
			}
		}
		return true
	}
	for reason := range want {
		if !defined("exitpin.reason." + reason) {
			t.Fatalf("catalogues are missing exitpin.reason.%s (RU and EN must both define it)", reason)
		}
	}
	for _, src := range []Source{SourceNone, SourceDevice, SourceUser} {
		if !defined("exitpin.source." + string(src)) {
			t.Fatalf("catalogues are missing exitpin.source.%s", src)
		}
	}
}

// TestB497_AdaptersReadTheRowAsStored keeps the two adapters honest: a field
// renamed in db must fail here instead of silently reading a zero value (the
// `via_enabled` column is an INTEGER, so a dropped field decodes as false and
// would turn every human's pin into «strict mode off»).
func TestB497_AdaptersReadTheRowAsStored(t *testing.T) {
	dev := FromDevice(db.DeviceExitNodePref{
		ExitNodeTag: "tag:dev-infra-karolina", ViaEnabled: true, SetByUserID: 6, OptedOut: true,
	})
	if dev.Tag != "tag:dev-infra-karolina" || !dev.ViaEnabled || dev.SetByUserID != 6 || !dev.OptedOut {
		t.Fatalf("FromDevice lost a field: %+v", dev)
	}
	usr := FromUser(db.ExitNodePref{
		ExitNodeTag: " tag:dev-infra-emilia ", ViaEnabled: true, SetByUserID: 7,
	})
	if usr.Tag != "tag:dev-infra-emilia" || !usr.ViaEnabled || usr.SetByUserID != 7 {
		t.Fatalf("FromUser lost a field: %+v", usr)
	}
	states := RelayStates([]db.ExitNodeHealth{
		{Hostname: "Karolina", State: "online"},
		{Hostname: "", State: "offline"},
	})
	if got := states["karolina"]; got != "online" {
		t.Fatalf("RelayStates[\"karolina\"] = %q, want online (hostnames are lowercased)", got)
	}
	if len(states) != 1 {
		t.Fatalf("RelayStates kept an empty hostname: %v", states)
	}
}
