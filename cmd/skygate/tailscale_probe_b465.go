// tailscale_probe_b465.go — the /readyz "is this host on the tailnet" probe.
//
// B465 (2026-11-12). The probe used to start with `isTailscaleRunningInContainer()`, which
// answers "is the IN-IMAGE tailscaled up" — FALSE on every native (systemd) install, where
// the client is the HOST's `tailscaled.service`. So a correct native installation reported
// `dependencies_healthy=false` forever with «tailscaled not running in container (non-RF
// mode)» (measured on the reference standby).
//
// The question the probe exists to answer is "is THIS host on the tailnet", and
// `tailscale status --json` answers it for both install kinds — it talks to the local
// tailscaled over its socket wherever that socket lives. So it is asked first; the
// container-specific state file is only the fallback, and a host with no client at all is
// "skipped" rather than "broken" (B405's rule for optional subsystems).
package main

func tailscaleReadinessProbe() (online bool, detail string) {
	state, ok := tailscaleBackendState()
	if ok {
		return state == "Running", "BackendState=" + state
	}
	if !isTailscaleRunningInContainer() {
		// Neither the client nor an in-image tailscaled: nothing to report, and an optional
		// component that was never installed must not fail readiness.
		return false, "tailscale is not installed on this host (skipped)"
	}
	// tailscale status --json failed although the in-image tailscaled is up (control socket
	// down, JSON parse error). Fall back to the state-file presence, which is the pre-D8
	// behaviour: tailscaled was up at some point and the state file still exists, so the
	// tailnet connection is at least partially functional.
	return true, "tailscaled running (state-file fallback — tailscale status --json failed)"
}
