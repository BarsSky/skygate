package update

// install.go — how skygate figures out HOW it was installed.
//
// Consumed by:
//   - /admin/update, which prints a different manual procedure per kind
//     (docker compose vs systemctl vs rc-service vs raw process), and
//   - the self-updater, which picks the automated path
//     (internal/update/docker.go for Docker, native.go for the rest).
//
// Detection is best-effort: a wrong answer sends the operator to the wrong
// procedure, which is why the ordering below is pinned by tests and why the
// env override exists for air-gapped hosts.

import (
	"os"
	"strings"
)

// InstallKind describes how skygate was installed on the host.
//
// The skill self-updater uses this to pick the right automated swap path
// (Docker pull vs. systemctl restart vs. rc-service restart vs. binary
// rename).
//
// Numeric values are append-only: InstallKind is rendered as a string into
// the update state file and audit rows, but nothing should depend on the
// ordinals either — keep new kinds at the end.
type InstallKind int

const (
	InstallUnknown InstallKind = iota
	// InstallDocker: skygate runs in a container (docker compose stack).
	InstallDocker
	// InstallSystemd: native binary under a systemd unit
	// (/etc/systemd/system/skygate.service), restarted with
	// `systemctl restart skygate`.
	InstallSystemd
	// InstallBare: native binary with no service manager — started by
	// nohup / supervisord / runit / hand-rolled scripts. The updater
	// restarts the process itself (setsid + the configured start
	// command).
	InstallBare
	// InstallOpenRC: native binary under an OpenRC service (Alpine and
	// other OpenRC distros), restarted with `rc-service skygate
	// restart`. Added 2026-09-18 (B262): the kind was missing entirely,
	// so an Alpine host detected as InstallUnknown and /admin/update
	// refused the update outright ("could not detect install kind")
	// even though deploy/install-alpine.sh exists and is a first-class
	// installer.
	InstallOpenRC
)

// String returns the human-readable name. Used in the /admin/update
// page and the audit log.
func (k InstallKind) String() string {
	switch k {
	case InstallDocker:
		return "docker"
	case InstallSystemd:
		return "systemd"
	case InstallOpenRC:
		return "openrc"
	case InstallBare:
		return "bare"
	default:
		return "unknown"
	}
}

// IsNative reports whether the kind is handled by the native (non-Docker)
// updater in native.go. Kept here so the handler and the page agree.
func (k InstallKind) IsNative() bool {
	return k == InstallSystemd || k == InstallOpenRC || k == InstallBare
}

// statExisting is injectable so the filesystem half of the detection can be
// unit-tested (the same pattern native.go uses for LookPath). Production
// always uses os.Stat.
var statExisting = func(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DetectInstallKind returns the install kind, in priority order:
//  1. SKYGATE_INSTALL_KIND env var (explicit override; the installers write
//     it into the unit/conf so air-gapped hosts don't rely on probing)
//  2. Filesystem detection (see detectInstallKindFilesystem)
//  3. Fallback: InstallUnknown (the page then shows every manual procedure
//     and the automated button refuses with an actionable message)
func DetectInstallKind() InstallKind {
	if v := os.Getenv("SKYGATE_INSTALL_KIND"); v != "" {
		switch strings.ToLower(v) {
		case "docker", "docker-compose", "compose":
			return InstallDocker
		case "systemd", "systemctl":
			return InstallSystemd
		case "openrc", "rc-service", "alpine":
			return InstallOpenRC
		case "bare", "binary":
			return InstallBare
		}
	}
	return detectInstallKindFilesystem()
}

// detectInstallKindFilesystem is the filesystem half of DetectInstallKind,
// split out so it can be referenced by tests.
//
// Order matters, and each step is there for a reason:
//
//  1. Container markers FIRST. skygate usually runs in a container ON a
//     systemd (or OpenRC) host, so anything that makes the host's init
//     markers visible inside the container — a bind-mount of /run, a
//     permissive image — would otherwise flip the answer to the host's init
//     system, and the page would print `systemctl restart skygate` for a
//     container that has no such unit. A container is always inside
//     something else. (Fixed 2026-09-18; before that the order was reversed.)
//  2. systemd before OpenRC. A host has exactly one live init; if
//     /run/systemd/system exists, systemd is PID 1 and OpenRC (if installed
//     at all for compatibility) is not what manages skygate.
//  3. /run/openrc is OpenRC's "I am the init" marker (Alpine et al).
//  4. Otherwise: unknown — a source checkout on a dev machine, or a host
//     with no service manager (the operator sets the override, or uses the
//     bare procedure).
func detectInstallKindFilesystem() InstallKind {
	if statExisting("/.dockerenv") {
		return InstallDocker
	}
	if statExisting("/run/.containerenv") {
		return InstallDocker
	}
	if statExisting("/run/systemd/system") {
		return InstallSystemd
	}
	if statExisting("/run/openrc") {
		return InstallOpenRC
	}
	// macOS, Windows, BSD — no systemd, no OpenRC, no docker
	// (the operator is running skygate natively for dev)
	return InstallUnknown
}
