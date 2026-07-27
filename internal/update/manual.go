package update

import (
	"fmt"
	"strings"
)

// ManualSteps is the copy-pasteable command sequence for an
// operator-driven update. v0.29.0 generates these but does NOT
// execute them automatically (the "Update now" button just
// scrolls to the manual steps section + copies to clipboard).
// v0.30.0 will add the automated swap.
type ManualSteps struct {
	// Kind is the install kind these steps are for.
	Kind InstallKind
	// Target is the version these steps upgrade TO (e.g. "v0.29.0").
	Target string
	// Current is the version these steps upgrade FROM (e.g. "v0.28.6").
	Current string
	// Steps is the ordered list of shell commands. The operator
	// runs them top-to-bottom in a terminal ssh'd to the host.
	// Comments (lines starting with #) are allowed for context.
	Steps []string
	// Rollback is the reverse sequence. Used if the new version
	// boots but breaks something. Keep it short and obvious.
	Rollback []string
	// VerifyAfter is the "is the upgrade complete?" check. The
	// /admin/update page surfaces this prominently.
	VerifyAfter string
}

// GenerateDockerSteps returns the manual update sequence for a
// Docker compose install. Matches the v0.28.6+ deployment shape:
// skygate is a container in a docker-compose stack, the source
// is /home/admin/skygate (bind-mounted into the container
// at /app), and the rebuild is `docker compose build skygate`
// followed by `docker compose up -d skygate`.
//
// The "Backup first" step is non-negotiable: even on a SQLite
// single-writer system, having a copy of the DB before the swap
// is the operator's insurance against "the new binary can't read
// the new schema" edge cases.
func GenerateDockerSteps(current, target string) ManualSteps {
	shortTarget := strings.TrimPrefix(target, "v")
	return ManualSteps{
		Kind:    InstallDocker,
		Target:  target,
		Current: current,
		Steps: []string{
			"# 1. SSH into the skygate host as the user that owns the repo",
			"ssh admin@192.0.2.1",
			"",
			"# 2. Backup the current DB (single file, fast to copy)",
			"docker exec skygate sqlite3 /data/skygate.db \\",
			"  \".backup /data/skygate.backup-$(date +%Y%m%d-%H%M%S).db\"",
			"",
			"# 3. Pull the new version",
			"cd /home/admin/skygate",
			"git fetch --tags",
			"git checkout " + target,
			"#   (verify the diff: git log --oneline " + current + ".." + target + ")",
			"",
			"# 4. Fix root-owned tailscale dirs (container tailscaled runs as root)",
			"sudo chown -R admin:admin data/ts/",
			"",
			"# 5. Build the new image (rebuilds the skygate binary + tailscale bits)",
			"docker compose build skygate",
			"",
			"# 6. Apply DB migrations BEFORE the new container starts",
			"#    (the new container will re-run them — they're idempotent —",
			"#     but doing it once outside the HTTP listener avoids the",
			"#     \"first request after restart hits a half-migrated DB\" race)",
			"docker run --rm --volumes-from skygate skygate-skygate:latest \\",
			"  /app/skygate --migrate-only 2>&1 | tail -10",
			"",
			"# 7. Recreate the container with the new image",
			"docker compose up -d --force-recreate --no-deps skygate",
			"",
			"# 8. Wait for /healthz to return 200 with the new build label",
			"until curl -fsS http://localhost:8080/healthz | grep -q '" + shortTarget + "'; do",
			"  sleep 5",
			"done",
			"",
			"# 9. Run the guarantee catalog to confirm the upgrade is clean",
			"#    (from the operator's workstation, NOT the VM — the script SSHes in)",
			"make verify-post",
			"# Expected: 26 PASS, 0 FAIL",
		},
		Rollback: []string{
			"# Rollback: revert to the previous tag and restart the container",
			"docker compose down skygate",
			"git checkout " + current,
			"docker compose build skygate",
			"docker compose up -d skygate",
			"until curl -fsS http://localhost:8080/healthz | grep -q '" + strings.TrimPrefix(current, "v") + "'; do sleep 5; done",
		},
		VerifyAfter: fmt.Sprintf("curl -fsS http://localhost:8080/healthz | grep -q '%s' && make verify-post (expect 26 PASS, 0 FAIL)", shortTarget),
	}
}

// GenerateBareSteps returns the manual update sequence for a
// bare-binary install (no Docker, no systemd). The operator
// stops the running process, swaps the binary, restarts.
//
// Used when skygate runs as a nohup/background process under
// tmux, supervisord, runit, or hand-rolled scripts. The
// v0.29.0 plan does NOT automate this path — the operator
// copy-pastes these steps into a terminal.
func GenerateBareSteps(current, target string) ManualSteps {
	shortTarget := strings.TrimPrefix(target, "v")
	shortCurrent := strings.TrimPrefix(current, "v")
	return ManualSteps{
		Kind:    InstallBare,
		Target:  target,
		Current: current,
		Steps: []string{
			"# 1. Stop the running skygate process",
			"#    (adjust the signal / supervisor to your setup)",
			"kill -TERM $(pidof skygate) || sudo systemctl stop skygate",
			"",
			"# 2. Backup the current binary and DB",
			"cp skygate skygate." + shortCurrent + ".bak",
			"cp -p skygate.db skygate.db." + shortCurrent + ".bak",
			"",
			"# 3. Download the new version (verify SHA256 before swap)",
			"curl -fsSL -o skygate." + shortTarget + " \\",
			"  https://github.com/skygate-operator/skygate/releases/download/" + target + "/skygate-linux-amd64",
			"curl -fsSL -o skygate." + shortTarget + ".sha256 \\",
			"  https://github.com/skygate-operator/skygate/releases/download/" + target + "/skygate-linux-amd64.sha256",
			"sha256sum -c skygate." + shortTarget + ".sha256",
			"",
			"# 4. Apply migrations (uses the new binary; idempotent on a current DB)",
			"chmod +x skygate." + shortTarget,
			"./skygate." + shortTarget + " --migrate-only",
			"",
			"# 5. Atomic swap (rename keeps the old binary as the rollback target)",
			"mv skygate skygate." + shortCurrent,
			"mv skygate." + shortTarget + " skygate",
			"chmod +x skygate",
			"",
			"# 6. Restart under your supervisor",
			"sudo systemctl start skygate",
			"#   or: nohup ./skygate &",
			"#   or: supervisorctl start skygate",
			"",
			"# 7. Verify",
			"until curl -fsS http://localhost:8080/healthz | grep -q '" + shortTarget + "'; do",
			"  sleep 2",
			"done",
		},
		Rollback: []string{
			"# Rollback: stop, restore the .bak binary, restart",
			"kill -TERM $(pidof skygate) || sudo systemctl stop skygate",
			"mv skygate skygate." + shortTarget + ".bad",
			"mv skygate." + shortCurrent + ".bak skygate",
			"sudo systemctl start skygate",
		},
		VerifyAfter: fmt.Sprintf("curl -fsS http://localhost:8080/healthz | grep -q '%s'", shortTarget),
	}
}

// GenerateSystemdSteps returns the manual update sequence for a
// systemd-managed bare binary. The systemd unit file points
// at /usr/local/bin/skygate (or similar). The operator stops
// the unit, swaps the binary, starts the unit.
func GenerateSystemdSteps(current, target string) ManualSteps {
	s := GenerateBareSteps(current, target)
	s.Kind = InstallSystemd
	// Replace the stop / restart lines with systemctl-specific
	// equivalents.
	s.Steps[1] = "# 1. Stop the systemd unit (waits for graceful shutdown, 30s timeout)"
	s.Steps[1] = "sudo systemctl stop skygate"
	s.Steps[len(s.Steps)-3] = "# 6. Start the systemd unit"
	s.Steps[len(s.Steps)-3] = "sudo systemctl start skygate"
	s.Steps[len(s.Steps)-2] = "# 7. Verify"
	return s
}

// GenerateManualSteps picks the right generator for the
// install kind. The /admin/update page calls this with the
// detected kind + the current + target version.
func GenerateManualSteps(kind InstallKind, current, target string) ManualSteps {
	switch kind {
	case InstallDocker:
		return GenerateDockerSteps(current, target)
	case InstallBare:
		return GenerateBareSteps(current, target)
	case InstallSystemd:
		return GenerateSystemdSteps(current, target)
	default:
		// Unknown: return Docker as a sane default. The
		// operator can scroll past it if their setup is
		// bare / systemd.
		return GenerateDockerSteps(current, target)
	}
}
