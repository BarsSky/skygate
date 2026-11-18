// Package exitpin answers ONE question — «which exit node will this device
// really use, and why?» — with exactly the rules the ACL generator applies.
//
// B497 (2026-11-19). The operator compared two phones that work with two that do
// not and asked what actually differs. The answer lives in the generated policy,
// not in the panel, and the panel could not show it:
//
//   - `device_exit_node_prefs` becomes a pin only through the grant
//     `{ src: [tag:dev-<u>-<d>], dst: [autogroup:internet], via: [<tag>] }`, and
//     the row carries `via` ONLY when all of the following hold:
//     `via_enabled = 1` (v0.28.5), written by a HUMAN (`set_by_user_id > 0`,
//     B398 — skygate's own guess must never become a filter), the node actually
//     carries a tag (B284 — an invented tag cannot be declared in tagOwners), and
//     the relay is usable (B356/B361). Any one missing and the generator emits
//     the UNPINNED grant: the device may use any relay it likes.
//   - `user_exit_node_prefs` pins only the grant whose `src` is `user@domain`, and
//     headscale does NOT resolve a TAGGED node through a user selector. Every
//     skygate device carries `tag:dev-<user>-<device>`, so the per-user preference
//     reaches none of them (measured in B265).
//
// So an empty cell on /admin/devices meant "free choice", a filled per-user
// preference meant "applies to nobody", and a device row whose `via_enabled` was
// 0 or that skygate derived itself meant "the relay name you see is not a pin".
// All three read as «настройка есть, но не работает». This package makes the
// answer a value the page can print instead of a conclusion it has to draw.
//
// It deliberately holds NO SQL: the callers own the reads (see exitpin_prefs.go
// for the two-row adapters), so the rules can be tested as a table.
package exitpin

import (
	"strings"

	"skygate/internal/monitoring"
)

// Source names WHERE the effective decision comes from.
type Source string

const (
	// SourceNone — nothing pins this device. It chooses its own exit node in the
	// Tailscale client, and the ACL lets it (the loose `autogroup:internet` grant).
	SourceNone Source = "none"
	// SourceDevice — an explicit per-device preference (`device_exit_node_prefs`).
	SourceDevice Source = "device"
	// SourceUser — the per-user preference (`user_exit_node_prefs`). Applies only
	// to UNTAGGED devices, i.e. in this deployment to none of them.
	SourceUser Source = "user"
)

// Reason tokens. They are stable strings (not translated text) so a template can
// map them to an i18n key and a contract can assert on them.
const (
	// ReasonNone — the pin applies as written.
	ReasonNone = ""
	// ReasonDerived — skygate wrote the row itself (B398). It feeds the prefix
	// assignment but never narrows what the client may choose.
	ReasonDerived = "derived"
	// ReasonViaOff — `via_enabled = 0`: the row is stored but the ACL emits the
	// loose grant (the Android-friendly default).
	ReasonViaOff = "via_off"
	// ReasonRelayDown — the named relay is measured as not usable (B356).
	ReasonRelayDown = "relay_down"
	// ReasonNoDeviceTag — the node carries no tag, so no per-device grant can be
	// emitted for it at all (B284).
	ReasonNoDeviceTag = "no_device_tag"
	// ReasonTaggedNoUser — a per-user preference exists, but this node is TAGGED
	// and headscale does not match a tagged node through `user@` (B265).
	ReasonTaggedNoUser = "tagged_no_user_src"
	// ReasonFreeChoice — no preference at all: the device picks.
	ReasonFreeChoice = "free_choice"
	// ReasonNoRelay — a row exists but names no relay (a half-written row).
	ReasonNoRelay = "no_relay"
)

// Pref is the narrow shape both preference tables share.
type Pref struct {
	// Tag is the exit-node tag the row names (`tag:dev-infra-karolina`).
	Tag string
	// ViaEnabled is the per-row strict switch (`via_enabled`).
	ViaEnabled bool
	// SetByUserID is 0 when the row was written by skygate's reconciler and > 0
	// when a human saved it. Only a human's row may become a permission filter.
	SetByUserID int64
	// OptedOut is B399: the operator removed the auto-derivation by hand.
	OptedOut bool
}

// Inputs is one device, fully described. Every field is a fact the caller reads;
// no field is inferred here.
type Inputs struct {
	// Device is the per-device row for (owner, hostname), nil when absent.
	Device *Pref
	// DeviceTag is `node_owner_map.tag` for this device ("" = untagged).
	DeviceTag string
	// DeviceRelayState is the monitor's last state for Device.Tag's relay
	// ("" = never measured — the pin stands, see B356's absence-of-evidence rule).
	DeviceRelayState string
	// User is the per-user row for the device's OWNER, nil when absent.
	User *Pref
	// UserRelayState is the monitor's last state for User.Tag's relay.
	UserRelayState string
	// Tagged reports whether the node carries a tag in headscale (which every
	// skygate device does since v0.28.0).
	Tagged bool
}

// Pin is the answer, ready to render.
type Pin struct {
	Source Source
	// Tag is the exit-node tag the decision names ("" for SourceNone).
	Tag string
	// Applies is true only when the generated policy really carries `via` for
	// this device's default route. It is FALSE for every preference the ACL
	// ignores — which is exactly the distinction the page was missing.
	Applies bool
	// Reason explains a Tag that does not apply, or a free choice.
	Reason string
}

// Effective derives the pin from the same predicates the generator uses, in the
// same order: the per-device row wins over the per-user row, and a per-user row
// cannot reach a tagged node.
func Effective(in Inputs) Pin {
	if p := in.Device; p != nil && strings.TrimSpace(p.Tag) != "" {
		tag := strings.TrimSpace(p.Tag)
		switch {
		case !p.ViaEnabled:
			return Pin{Source: SourceDevice, Tag: tag, Reason: ReasonViaOff}
		case p.SetByUserID <= 0:
			return Pin{Source: SourceDevice, Tag: tag, Reason: ReasonDerived}
		case strings.TrimSpace(in.DeviceTag) == "":
			return Pin{Source: SourceDevice, Tag: tag, Reason: ReasonNoDeviceTag}
		case relayUnusable(in.DeviceRelayState):
			return Pin{Source: SourceDevice, Tag: tag, Reason: ReasonRelayDown}
		}
		return Pin{Source: SourceDevice, Tag: tag, Applies: true}
	}
	if p := in.User; p != nil && strings.TrimSpace(p.Tag) != "" {
		tag := strings.TrimSpace(p.Tag)
		switch {
		case !p.ViaEnabled:
			return Pin{Source: SourceUser, Tag: tag, Reason: ReasonViaOff}
		case in.Tagged:
			// The row is real and the operator meant it, but headscale resolves
			// the grant's `src` — and a tagged node never matches `user@`.
			return Pin{Source: SourceUser, Tag: tag, Reason: ReasonTaggedNoUser}
		// NOTE: there is deliberately no `DeviceTag == ""` branch here. The
		// per-user grant is `src: [user@domain]`, which an UNTAGGED node matches
		// by definition — that is the one case where this table still works.
		case relayUnusable(in.UserRelayState):
			return Pin{Source: SourceUser, Tag: tag, Reason: ReasonRelayDown}
		}
		return Pin{Source: SourceUser, Tag: tag, Applies: true}
	}
	if (in.Device != nil && in.Device.OptedOut) || (in.User != nil && in.User.OptedOut) {
		// B399 decided not to derive anything for this device; that is a
		// decision, not an absence, and the page says so.
		return Pin{Source: SourceNone, Reason: ReasonFreeChoice, Applies: false}
	}
	if in.Device != nil || in.User != nil {
		return Pin{Source: SourceNone, Reason: ReasonNoRelay}
	}
	return Pin{Source: SourceNone, Reason: ReasonFreeChoice}
}

// relayUnusable mirrors B356 exactly: a relay the monitor has MEASURED and found
// unusable withholds the pin; "never measured" ("") keeps it, because absence of
// evidence must not change a policy. The predicate itself is the monitor's, not a
// second copy of it (`monitoring.ExitNodeUsable` — the same call the ACL makes).
func relayUnusable(state string) bool {
	s := strings.TrimSpace(state)
	if s == "" {
		return false
	}
	return !monitoring.ExitNodeUsable(s)
}

// Applied reports whether the generated policy carries this pin, spelled as a
// method so a template does not have to read two fields.
func (p Pin) Applied() bool { return p.Applies && p.Tag != "" }

// ReasonKey is the i18n key suffix the page renders for a non-applying pin.
func (p Pin) ReasonKey() string {
	if p.Applies {
		return ""
	}
	return "exitpin.reason." + p.Reason
}

// SourceKey is the i18n key naming the source.
func (p Pin) SourceKey() string { return "exitpin.source." + string(p.Source) }

// Free reports «nothing pins this device».
func (p Pin) Free() bool { return p.Source == SourceNone }
