// Package devicemeta — per-device OS + device_type metadata for
// easier debugging. The operator can see at a glance "this is
// a Windows machine, check the Tailscale client version" vs
// "this is an Android phone, the policy should already
// work".
//
// refactor-v0.30 Phase B step 7 / device-meta feature
// (2026-07-29): the columns (os, device_type) live on
// node_owner_map (per-user, per-hostname). Two writes
// happen:
//
//   1. Auto-detect: every /my/devices load calls
//      Detect(hostname, tags, approvedRoutes) and
//      persists the result via db.SetDeviceMeta if the
//      row is currently empty (i.e. first visit since
//      this feature shipped). The auto-detect is
//      intentionally conservative — we never OVERWRITE
//      an operator-set value, and "unknown" / "" means
//      "fall back to the auto-detect".
//
//   2. Manual override: /admin/devices/{id}/meta POST
//      lets the operator set os / device_type explicitly
//      when the auto-detect is wrong (e.g. a hostname
//      like "laptop" with no OS hint). The override
//      sticks across restarts and across new
//      /my/devices loads (the auto-detect skips the
//      row if both fields are non-empty AND the
//      override-flag is set, or alternatively if the
//      stored value is non-default).
package devicemeta

import "strings"

// OS values stored in node_owner_map.os. Empty string
// means "not yet detected". We use short tokens
// (not "android" + "ios" + ...) so the UI badge
// stays compact.
const (
	OSUnknown  = "unknown"
	OSWindows  = "windows"
	OSMacOS    = "macos"
	OSLinux    = "linux"
	OSAndroid  = "android"
	OSiOS      = "ios"
	OSFreeBSD  = "freebsd"
)

// Type values stored in node_owner_map.device_type.
const (
	TypeUnknown     = "unknown"
	TypeClient      = "client"
	TypeExitNode    = "exit-node"
	TypeSubnetRtr   = "subnet-router"
	TypeServer      = "server"
	TypePhone       = "phone"
)

// OSIcon returns the FontAwesome icon class for the OS
// token. Returns a question mark for unknown.
func OSIcon(os string) string {
	switch os {
	case OSWindows:
		return "fa-brands fa-windows"
	case OSMacOS:
		return "fa-brands fa-apple"
	case OSLinux:
		return "fa-brands fa-linux"
	case OSAndroid:
		return "fa-brands fa-android"
	case OSiOS:
		return "fa-brands fa-apple"
	case OSFreeBSD:
		return "fa-solid fa-server"
	default:
		return "fa-solid fa-circle-question"
	}
}

// TypeIcon returns the FontAwesome icon class for the
// device type. Exit-nodes and subnet-routers get the
// distinct "router" / "globe" icons; clients get a
// generic box.
func TypeIcon(t string) string {
	switch t {
	case TypeExitNode:
		return "fa-solid fa-globe"
	case TypeSubnetRtr:
		return "fa-solid fa-route"
	case TypeServer:
		return "fa-solid fa-server"
	case TypePhone:
		return "fa-solid fa-mobile-screen"
	default:
		return "fa-solid fa-desktop"
	}
}

// DetectOS returns a best-guess OS for a Tailscale
// hostname. The matching is intentionally cheap
// (case-insensitive substring) — we never fail, we
// just return "unknown" if no heuristic matches. The
// admin can override via /admin/devices/{id}/meta.
//
// Heuristics (order matters: first match wins):
//
//   - DESKTOP-* / LAPTOP-* / MSI*         → windows
//     (Windows default hostname pattern)
//   - MacBook* / iMac* / Mac-mini*        → macos
//   - iPhone / iPad                       → ios
//   - Nothing Phone / Android*            → android
//     (covers both "Nothing Phone (2)" and
//     "A71 пользователя Константин" via
//     substring match)
//   - *android*                           → android
//   - raspberrypi / rpi / skygate-host-1      → linux
//   - *server* / *node*                   → linux
//     (VPS default pattern; weak — admin
//     can override)
//   - skygate-subnet-*                    → linux
//   - otherwise                           → unknown
func DetectOS(hostname string) string {
	h := strings.ToLower(hostname)
	switch {
	case h == "":
		return OSUnknown
	case strings.HasPrefix(h, "desktop-"),
		strings.HasPrefix(h, "laptop-"),
		strings.HasPrefix(h, "workstation-3"):
		return OSWindows
	case strings.HasPrefix(h, "macbook"),
		strings.HasPrefix(h, "imac"),
		strings.HasPrefix(h, "mac-mini"),
		strings.HasPrefix(h, "macmini"):
		return OSMacOS
	case strings.HasPrefix(h, "iphone"),
		strings.HasPrefix(h, "ipad"):
		return OSiOS
	case strings.Contains(h, "nothing phone"),
		strings.Contains(h, "android"),
		strings.HasPrefix(h, "android-"):
		return OSAndroid
	case strings.HasPrefix(h, "raspberrypi"),
		strings.HasPrefix(h, "rpi"),
		strings.HasPrefix(h, "skygate-host-1"),
		strings.HasPrefix(h, "skygate-subnet"):
		return OSLinux
	case strings.Contains(h, "server"),
		strings.Contains(h, "node"),
		strings.Contains(h, "linux"):
		return OSLinux
	}
	return OSUnknown
}

// DetectType returns the device type from the headscale
// node tags + routes. Logic:
//
//   - tag:exit-node OR approved_routes has 0.0.0.0/0
//     → exit-node
//   - tag:subnet-router OR subnet_routes non-empty
//     → subnet-router
//   - OS-detected android/ios → phone
//   - otherwise → client
//
// detectedOS is the value DetectOS() returned (so we
// can use the OS hint for the phone-vs-client
// distinction). Pass "" if not yet known — in that
// case we fall through to "client".
func DetectType(tags []string, approvedRoutes, subnetRoutes []string, detectedOS string) string {
	for _, t := range tags {
		if t == "tag:exit-node" {
			return TypeExitNode
		}
	}
	for _, r := range approvedRoutes {
		if r == "0.0.0.0/0" || r == "::/0" {
			return TypeExitNode
		}
	}
	for _, t := range tags {
		if t == "tag:subnet-router" {
			return TypeSubnetRtr
		}
	}
	if len(subnetRoutes) > 0 {
		return TypeSubnetRtr
	}
	switch detectedOS {
	case OSAndroid, OSiOS:
		return TypePhone
	}
	return TypeClient
}

// IsOSValid returns true for known OS tokens (including
// "unknown" — the empty-default).
func IsOSValid(s string) bool {
	switch s {
	case "", OSUnknown, OSWindows, OSMacOS, OSLinux, OSAndroid, OSiOS, OSFreeBSD:
		return true
	}
	return false
}

// IsTypeValid returns true for known type tokens.
func IsTypeValid(s string) bool {
	switch s {
	case "", TypeUnknown, TypeClient, TypeExitNode, TypeSubnetRtr, TypeServer, TypePhone:
		return true
	}
	return false
}
