package update

import (
	"os"
	"strings"
)

// InstallKind describes how skygate was installed on the host.
// The /admin/update page uses this to generate the right manual
// update steps (Docker compose commands vs. systemctl vs. bare-binary
// mv). The v0.29.0 self-updater uses this to pick the right
// automated swap path (Docker pull vs. systemctl restart vs.
// binary rename).
//
// The detection is best-effort: a wrongly-detected install kind
// produces wrong manual steps, but doesn't break anything (the
// operator can still ssh + run the steps by hand). v0.29.0
// defaults to InstallDocker (most common); the operator can
// override via SKYGATE_INSTALL_KIND=docker|systemd|bare.
type InstallKind int

const (
	InstallUnknown InstallKind = iota
	InstallDocker
	InstallSystemd
	InstallBare
)

// String returns the human-readable name. Used in the /admin/update
// page and the audit log.
func (k InstallKind) String() string {
	switch k {
	case InstallDocker:
		return "docker"
	case InstallSystemd:
		return "systemd"
	case InstallBare:
		return "bare"
	default:
		return "unknown"
	}
}

// DetectInstallKind returns the install kind, in priority order:
//  1. SKYGATE_INSTALL_KIND env var (explicit override, for
//     air-gapped deploys where the filesystem-based detection
//     might be misleading)
//  2. Filesystem detection: /run/systemd/system → InstallSystemd,
//     /.dockerenv or /run/.containerenv → InstallDocker
//  3. Fallback: InstallDocker (most common skygate install path)
//
// On non-Linux platforms (Windows dev, macOS), returns
// InstallUnknown — the operator is running locally for
// development, the page shows "unknown" + the manual steps
// for each kind.
func DetectInstallKind() InstallKind {
	if v := os.Getenv("SKYGATE_INSTALL_KIND"); v != "" {
		switch strings.ToLower(v) {
		case "docker", "docker-compose", "compose":
			return InstallDocker
		case "systemd", "systemctl":
			return InstallSystemd
		case "bare", "binary":
			return InstallBare
		}
	}
	// Linux-specific detection
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return InstallSystemd
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return InstallDocker
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return InstallDocker
	}
	// macOS, Windows, BSD — no systemd, no docker
	// (the operator is running skygate natively for dev)
	return InstallUnknown
}
