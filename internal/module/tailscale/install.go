// install.go — Tailscale install modes (B-mod-tailscale, 2026-09-10).
//
// The Tailscale module supports three install modes, configured
// via the SKYGATE_TS_INSTALL_MODE env var (or the install form
// on /admin/modules/tailscale):
//
//   - "os_level":   install tailscaled as a systemd service
//                   (apt install tailscale + systemctl enable
//                   --now tailscaled). This is the classic
//                   "operator owns the host" install.
//
//   - "in_container": run tailscaled as a sidecar container
//                     (docker run tailscale/tailscale).
//                     skygate then talks to it via a shared
//                     unix socket. Used in containerized
//                     skygate deployments.
//
//   - "attach":     tailscaled is ALREADY running on the host
//                   (operator installed it manually or via
//                   their own tooling). skygate does not
//                   install or start tailscaled — it only
//                   runs `tailscale up` to attach to the
//                   headscale control server. This is the
//                   B209.1 fallback for agents that already
//                   have tailscale.
//
//   - "none":       do not install anything. Just record the
//                   state. Used for "I just want the admin
//                   page to know tailscale exists" without
//                   actually bringing it up.
//
// The install() method is the entry point. It dispatches to
// installOsLevel / installInContainer / installAttach based on
// the configured mode.

package tailscale

import (
	"context"
	"fmt"
	"strings"
)

// installMode values. Stored in State.InstallMode.
const (
	// InstallModeOSLevel — tailscaled runs on the host (apt +
	// systemd). The classic install for VM-based skygate.
	InstallModeOSLevel = "os_level"

	// InstallModeContainer — tailscaled runs as a sidecar
	// container; skygate talks to it via a mounted socket.
	InstallModeContainer = "in_container"

	// InstallModeAttach — tailscaled is already running on the
	// host (operator-installed). skygate only runs `tailscale
	// up` to register with headscale.
	InstallModeAttach = "attach"

	// InstallModeNone — no install, just state.
	InstallModeNone = "none"
)

// validInstallMode reports whether m is a recognised install
// mode value. Used by Install() + by the /admin/modules
// install form to reject typos early.
func validInstallMode(m string) bool {
	switch m {
	case InstallModeOSLevel, InstallModeContainer, InstallModeAttach, InstallModeNone:
		return true
	}
	return false
}

// installOpts carries everything install() needs to know. Built
// once in Install() from the ModuleConfig.Env, then passed to
// installOsLevel / installInContainer / installAttach.
type installOpts struct {
	// Mode is one of the InstallMode* constants.
	Mode string

	// LoginServer is the headscale control URL
	// (e.g. https://head.skynas.ru or http://headscale:50444).
	// Required for os_level and attach; ignored for in_container
	// (the container has its own --login-server flag).
	LoginServer string

	// AuthKey is the headscale preauth key to register with.
	// Optional for os_level (operator can `tailscale up` later
	// from the CLI); required for attach (otherwise skygate
	// can't attach).
	AuthKey string

	// Hostname is the tailscale node name
	// (e.g. "skygate-host-1-1"). Defaults to the OS hostname
	// if empty. Used for os_level + attach; passed via
	// --hostname to the container for in_container.
	Hostname string

	// ContainerName is the docker container name for
	// in_container mode. Defaults to "skygate-tailscale".
	ContainerName string
}

// buildInstallOpts reads SKYGATE_TS_* env vars from cfg.Env and
// returns a populated installOpts. Missing required fields are
// returned as zero values; the caller decides which ones are
// required for the chosen mode.
func buildInstallOpts(env map[string]string, hostname string) installOpts {
	opts := installOpts{
		Mode:          env["SKYGATE_TS_INSTALL_MODE"],
		LoginServer:   env["SKYGATE_TS_LOGIN_SERVER"],
		AuthKey:       env["SKYGATE_TS_AUTHKEY"],
		Hostname:      env["SKYGATE_TS_HOSTNAME"],
		ContainerName: env["SKYGATE_TS_CONTAINER_NAME"],
	}
	if opts.Mode == "" {
		// Default to os_level for VM-based installs, attach
		// for the agent (which already has tailscale from
		// B209.1). The caller can override via SKYGATE_TS_INSTALL_MODE.
		opts.Mode = InstallModeOSLevel
	}
	if opts.Hostname == "" {
		opts.Hostname = hostname
	}
	if opts.ContainerName == "" {
		opts.ContainerName = "skygate-tailscale"
	}
	return opts
}

// install dispatches to the right install function based on
// Mode. Returns nil on success, a wrapped error on failure.
// All commands run through the CmdRunner (so tests can mock).
//
// Idempotency: each sub-function is safe to call twice. The
// "os_level" path checks `which tailscale` first and skips the
// apt install if already present. The "in_container" path
// checks `docker ps` for the container name first.
func (m *Module) install(ctx context.Context, opts installOpts) error {
	switch opts.Mode {
	case InstallModeOSLevel:
		return m.installOsLevel(ctx, opts)
	case InstallModeContainer:
		return m.installInContainer(ctx, opts)
	case InstallModeAttach:
		return m.installAttach(ctx, opts)
	case InstallModeNone:
		// Nothing to do. The state will record Mode=none so
		// Start() knows to skip the runtime check.
		return nil
	default:
		return fmt.Errorf("tailscale install: unknown mode %q (valid: os_level, in_container, attach, none)", opts.Mode)
	}
}

// installOsLevel installs tailscaled via apt + systemd. Steps:
//
//  1. Verify systemctl is on PATH (sanity check — if the
//     container is missing systemd, we should fail here, not
//     3 steps later).
//  2. apt-get update (ignore exit code — the cache may be
//     fresh already).
//  3. apt-get install -y tailscale.
//  4. systemctl enable --now tailscaled.
//  5. tailscale up --login-server=$LOGIN_SERVER --authkey=$AUTH_KEY
//     --hostname=$HOSTNAME --accept-routes=false
//     --netfilter-mode=nodir (B179 safety).
//
// Step 5 is skipped if AuthKey is empty (the operator may
// prefer to run it manually from the CLI to avoid putting
// the auth key in /etc/skygate/skygate.env).
func (m *Module) installOsLevel(ctx context.Context, opts installOpts) error {
	if m.runner.LookPath("systemctl") == "" {
		return fmt.Errorf("install os_level: systemctl not on PATH (this is a non-systemd host — use in_container mode)")
	}
	if m.runner.LookPath("tailscale") != "" {
		// Already installed — skip the apt steps, jump to
		// step 5. Idempotent re-install.
		m.logf("tailscale already installed, skipping apt")
	} else {
		if m.runner.LookPath("apt-get") == "" {
			return fmt.Errorf("install os_level: apt-get not on PATH and tailscale not installed")
		}
		// apt-get update — best-effort. Failure here is
		// non-fatal if the cache is recent (e.g. < 1h old).
		_, _, _, _ = m.runner.Run(ctx, "apt-get", "update", "-qq")
		stdout, stderr, code, err := m.runner.Run(ctx, "apt-get", "install", "-y", "tailscale")
		if err != nil {
			return fmt.Errorf("install os_level: apt-get install tailscale: %w (stdout=%q stderr=%q)", err, stdout, stderr)
		}
		if code != 0 {
			return fmt.Errorf("install os_level: apt-get install tailscale: %s", errFromExit(stdout, stderr, code))
		}
	}
	// Step 4: enable + start tailscaled. Use --now so we
	// don't need a separate start.
	if stdout, stderr, code, err := m.runner.Run(ctx, "systemctl", "enable", "--now", "tailscaled"); err != nil || code != 0 {
		return fmt.Errorf("install os_level: systemctl enable --now tailscaled: %s (stdout=%q stderr=%q)", errFromExit(stdout, stderr, code), stdout, stderr)
	}
	// Step 5: tailscale up. Skip if no auth key.
	if opts.AuthKey == "" {
		m.logf("tailscale: SKYGATE_TS_AUTHKEY empty, skipping 'tailscale up' (operator must run it manually)")
		return nil
	}
	if opts.LoginServer == "" {
		return fmt.Errorf("install os_level: SKYGATE_TS_LOGIN_SERVER is required when SKYGATE_TS_AUTHKEY is set")
	}
	args := []string{
		"up",
		"--login-server=" + opts.LoginServer,
		"--authkey=" + opts.AuthKey,
		"--accept-routes=false",
		"--netfilter-mode=nodir", // B179 safety: never 'off' on a new node
		"--accept-dns=false",
	}
	if opts.Hostname != "" {
		args = append(args, "--hostname="+opts.Hostname)
	}
	if stdout, stderr, code, err := m.runner.Run(ctx, "tailscale", args...); err != nil || code != 0 {
		return fmt.Errorf("install os_level: tailscale up: %s (stdout=%q stderr=%q)", errFromExit(stdout, stderr, code), stdout, stderr)
	}
	return nil
}

// installInContainer runs tailscaled as a sidecar container.
// Steps:
//
//  1. Verify docker is on PATH.
//  2. Check if the container is already running (idempotent).
//  3. docker run -d --name=$NAME --network=host
//     --cap-add=NET_ADMIN --cap-add=NET_RAW
//     -v /var/lib/tailscale-skygate:/var/lib/tailscale
//     -v /dev/net/tun:/dev/net/tun
//     tailscale/tailscale
//     tailscaled --state=/var/lib/tailscale/tailscaled.state
//     --socket=/var/run/tailscale/tailscaled.sock
//
//  4. tailscale up (via the container's exec).
func (m *Module) installInContainer(ctx context.Context, opts installOpts) error {
	if m.runner.LookPath("docker") == "" {
		return fmt.Errorf("install in_container: docker not on PATH")
	}
	// Idempotency: check if the container is already running.
	stdout, _, _, _ := m.runner.Run(ctx, "docker", "ps", "-q", "-f", "name="+opts.ContainerName)
	if strings.TrimSpace(stdout) != "" {
		m.logf("container %s already running, skipping docker run", opts.ContainerName)
	} else {
		// Remove a stopped container with the same name, if any.
		_, _, _, _ = m.runner.Run(ctx, "docker", "rm", "-f", opts.ContainerName)
		args := []string{
			"run", "-d",
			"--name=" + opts.ContainerName,
			"--network=host",
			"--cap-add=NET_ADMIN",
			"--cap-add=NET_RAW",
			"-v", "/var/lib/" + opts.ContainerName + ":/var/lib/tailscale",
			"-v", "/dev/net/tun:/dev/net/tun",
			"tailscale/tailscale",
			"tailscaled",
			"--state=/var/lib/tailscale/tailscaled.state",
			"--socket=/var/run/tailscale/tailscaled.sock",
		}
		if out, stderr, code, err := m.runner.Run(ctx, "docker", args...); err != nil || code != 0 {
			return fmt.Errorf("install in_container: docker run: %s (stdout=%q stderr=%q)", errFromExit(out, stderr, code), out, stderr)
		}
	}
	if opts.AuthKey == "" {
		m.logf("tailscale: SKYGATE_TS_AUTHKEY empty, skipping 'tailscale up' in container")
		return nil
	}
	if opts.LoginServer == "" {
		return fmt.Errorf("install in_container: SKYGATE_TS_LOGIN_SERVER is required when SKYGATE_TS_AUTHKEY is set")
	}
	upArgs := []string{
		"exec", opts.ContainerName,
		"tailscale", "up",
		"--login-server=" + opts.LoginServer,
		"--authkey=" + opts.AuthKey,
		"--accept-routes=false",
		"--netfilter-mode=nodir",
		"--accept-dns=false",
	}
	if opts.Hostname != "" {
		upArgs = append(upArgs, "--hostname="+opts.Hostname)
	}
	if out, stderr, code, err := m.runner.Run(ctx, "docker", upArgs...); err != nil || code != 0 {
		return fmt.Errorf("install in_container: docker exec tailscale up: %s (stdout=%q stderr=%q)", errFromExit(out, stderr, code), out, stderr)
	}
	return nil
}

// installAttach assumes tailscaled is already running on the
// host (operator-installed). skygate only runs `tailscale up`
// to register with headscale. No apt, no docker.
//
// The assumption is verified by `tailscale status --json` — if
// BackendState != "Running", the attach fails with a clear
// error pointing the operator at the install docs.
func (m *Module) installAttach(ctx context.Context, opts installOpts) error {
	if m.runner.LookPath("tailscale") == "" {
		return fmt.Errorf("install attach: tailscale binary not on PATH — install it first or use a different mode")
	}
	stdout, _, _, _ := m.runner.Run(ctx, "tailscale", "status", "--json")
	if !strings.Contains(stdout, `"BackendState": "Running"`) && !strings.Contains(stdout, `"BackendState":"Running"`) {
		return fmt.Errorf("install attach: tailscaled is not running (status=%q). Start it before installing the skygate module, or switch to os_level/in_container mode", strings.TrimSpace(stdout))
	}
	if opts.AuthKey == "" {
		m.logf("tailscale: SKYGATE_TS_AUTHKEY empty in attach mode, skipping 'tailscale up'")
		return nil
	}
	if opts.LoginServer == "" {
		return fmt.Errorf("install attach: SKYGATE_TS_LOGIN_SERVER is required when SKYGATE_TS_AUTHKEY is set")
	}
	args := []string{
		"up",
		"--login-server=" + opts.LoginServer,
		"--authkey=" + opts.AuthKey,
		"--accept-routes=false",
		"--netfilter-mode=nodir",
		"--accept-dns=false",
	}
	if opts.Hostname != "" {
		args = append(args, "--hostname="+opts.Hostname)
	}
	if out, stderr, code, err := m.runner.Run(ctx, "tailscale", args...); err != nil || code != 0 {
		return fmt.Errorf("install attach: tailscale up: %s (stdout=%q stderr=%q)", errFromExit(out, stderr, code), out, stderr)
	}
	return nil
}
