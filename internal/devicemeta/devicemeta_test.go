package devicemeta

import "testing"

// TestDetectOS pins the heuristic. When a hostname stops
// matching any of the patterns (operator renames a device),
// DetectOS returns "unknown" and the auto-detect is a no-op
// for that field — the admin can still override it
// manually via /admin/devices/{id}/meta.
func TestDetectOS(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		// Windows
		{"DESKTOP-CUO0TFB", OSWindows},
		{"LAPTOP-CONST", OSWindows},
		{"MSI-NEW", OSWindows},
		{"MSI", OSWindows},
		{"MY-WIN10", OSUnknown}, // no "MSI" or "DESKTOP" prefix — admin can override
		// macOS
		{"MacBook-Pro-Константина", OSMacOS},
		{"iMac-2024", OSMacOS},
		{"Mac-mini-Office", OSMacOS},
		// iOS
		{"iPhone-Константина", OSiOS},
		{"iPad-Living", OSiOS},
		// Android
		{"Nothing Phone (2)", OSAndroid},
		// A71 is a Samsung phone but the hostname doesn't
		// contain any of the auto-detect hints ("android",
		// "Nothing Phone", "android-"). It falls to
		// "unknown" and the operator has to set it
		// manually via /admin/devices/{id}/meta.
		// That's the intended escape hatch — we don't
		// pretend to know every Android model name.
		{"A71 пользователя Константин", OSUnknown},
		{"android-xyz", OSAndroid},
		// Linux
		{"raspberrypi-home", OSLinux},
		{"rpi-3", OSLinux},
		{"skygate-vm", OSLinux},
		{"skygate-subnet-skyadmin", OSLinux},
		{"my-linux-server", OSLinux},
		// Default: unknown
		{"", OSUnknown},
		{"random-hostname", OSUnknown},
	}
	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			got := DetectOS(c.host)
			if got != c.want {
				t.Errorf("DetectOS(%q) = %q, want %q", c.host, got, c.want)
			}
		})
	}
}

// TestDetectTypeExitNode pins the "tag:exit-node or 0.0.0.0/0
// route → exit-node" rule. This is the case the operator
// cares about for emilia/sharlotta/karolina.
func TestDetectTypeExitNode(t *testing.T) {
	tags := []string{"tag:exit-emilia", "tag:exit-node", "tag:public"}
	routes := []string{"0.0.0.0/0", "::/0"}
	if got := DetectType(tags, routes, nil, OSLinux); got != TypeExitNode {
		t.Errorf("DetectType(tag:exit-node, ...) = %q, want %q", got, TypeExitNode)
	}
	// Same detection works without the tag, via 0.0.0.0/0
	if got := DetectType(nil, routes, nil, OSLinux); got != TypeExitNode {
		t.Errorf("DetectType(0.0.0.0/0) = %q, want %q", got, TypeExitNode)
	}
}

// TestDetectTypeSubnetRouter: subnet-router by tag or by
// having subnet_routes.
func TestDetectTypeSubnetRouter(t *testing.T) {
	tags := []string{"tag:subnet-router", "tag:private"}
	subnetRoutes := []string{"10.0.1.0/24"}
	if got := DetectType(tags, nil, subnetRoutes, OSLinux); got != TypeSubnetRtr {
		t.Errorf("DetectType(tag:subnet-router) = %q, want %q", got, TypeSubnetRtr)
	}
	// No tag, but subnetRoutes present
	if got := DetectType(nil, nil, subnetRoutes, OSLinux); got != TypeSubnetRtr {
		t.Errorf("DetectType(subnet_routes) = %q, want %q", got, TypeSubnetRtr)
	}
}

// TestDetectTypePhone: Android / iOS → phone.
func TestDetectTypePhone(t *testing.T) {
	if got := DetectType(nil, nil, nil, OSAndroid); got != TypePhone {
		t.Errorf("DetectType(android) = %q, want %q", got, TypePhone)
	}
	if got := DetectType(nil, nil, nil, OSiOS); got != TypePhone {
		t.Errorf("DetectType(ios) = %q, want %q", got, TypePhone)
	}
	// Linux without exit/subnet tags → client
	if got := DetectType(nil, nil, nil, OSLinux); got != TypeClient {
		t.Errorf("DetectType(linux) = %q, want %q", got, TypeClient)
	}
	// Windows without tags → client
	if got := DetectType(nil, nil, nil, OSWindows); got != TypeClient {
		t.Errorf("DetectType(windows) = %q, want %q", got, TypeClient)
	}
}

// TestIconsKnown — every known token must have a non-empty
// icon. The template uses the icon directly, so a missing
// case would render an empty <i></i>.
func TestIconsKnown(t *testing.T) {
	for _, os := range []string{OSWindows, OSMacOS, OSLinux, OSAndroid, OSiOS, OSFreeBSD, OSUnknown} {
		if OSIcon(os) == "" {
			t.Errorf("OSIcon(%q) is empty", os)
		}
	}
	for _, ty := range []string{TypeClient, TypeExitNode, TypeSubnetRtr, TypeServer, TypePhone, TypeUnknown} {
		if TypeIcon(ty) == "" {
			t.Errorf("TypeIcon(%q) is empty", ty)
		}
	}
}

