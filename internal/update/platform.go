package update

// platform.go — §12.15 item 3: show the operator WHICH platform the
// /admin/update page is talking about.
//
// The pre-fix page printed the install kind ("systemd (bare binary)")
// and a set of manual steps, but nothing about the host itself. On
// the reference VM that was actively misleading: the page said
// "systemd" (the container carries a /run/systemd/system bind-mount
// from its host) while the actual install was Docker, and the manual
// steps it printed (`systemctl restart skygate`) could never work
// inside the container. DetectInstallKind's container-first ordering
// fixed the detection; this file makes the inputs visible so the next
// disagreement between "what the page believes" and "what the host
// is" is one glance instead of an ssh session.
//
// Everything here is read-only and best-effort: a missing systemctl
// or docker is reported, never fatal.

import (
	"os"
	"os/exec"
	"runtime"
)

// PlatformInfo is the host/tooling snapshot rendered on
// /admin/update.
type PlatformInfo struct {
	// OS / Arch come from runtime (the platform of the running
	// BINARY, which is what the updater would replace).
	OS   string
	Arch string
	// InContainer is true when one of the container markers is
	// present (same markers DetectInstallKind uses).
	InContainer bool
	// HasSystemctl / HasDocker report whether the tool is on PATH.
	HasSystemctl bool
	HasDocker    bool
	// HelperPath is the privileged helper the native updater needs;
	// HelperInstalled reports whether it is present + executable.
	HelperPath      string
	HelperInstalled bool
	// StateDirWritable reports whether the update staging dir can be
	// created (the native path stages request files there).
	StateDirWritable bool
	// UpdateDir is the staging directory reported above.
	UpdateDir string
}

// DetectPlatform collects the snapshot. updateDir may be empty.
func DetectPlatform(updateDir string) PlatformInfo {
	info := PlatformInfo{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		UpdateDir:  updateDir,
		HelperPath: getEnv("SKYGATE_UPDATE_HELPER"),
	}
	if info.HelperPath == "" {
		info.HelperPath = DefaultNativeHelperPath
	}
	info.InContainer = inContainerFilesystem()
	info.HasSystemctl = lookPathOK("systemctl")
	info.HasDocker = lookPathOK("docker")
	if st, err := os.Stat(info.HelperPath); err == nil {
		info.HelperInstalled = hasExecBit(st)
	}
	if updateDir != "" {
		info.StateDirWritable = dirWritable(updateDir)
	}
	return info
}

// inContainerFilesystem mirrors the container half of
// detectInstallKindFilesystem so the page's "in container" flag and
// the detected install kind can never disagree.
func inContainerFilesystem() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	return false
}

// dirWritable reports whether the service can use the directory. A
// missing dir is created first (the updater does MkdirAll before
// staging the request), then probed with a temp file.
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".skygate-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// lookPathOK reports whether the tool is on PATH.
func lookPathOK(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
