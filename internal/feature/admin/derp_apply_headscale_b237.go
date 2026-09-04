package admin

// derp_apply_headscale_b237.go — v1.5.2+ (B237) —
// apply the current derp_relays derpmap.json URL to
// headscale's `derp.urls` config + restart headscale.
//
// The apply pipeline:
//  1. Read headscale's current config.yaml (mounted at
//     /home/admin/headscale in the skygate container, but
//     on this deployment the path is /home/skyadmin/headscale
//     — we discover via the env var
//     SKYGATE_HEADSCALE_CONFIG_PATH if set, default
//     /home/admin/headscale/config/config.yaml from the
//     docker-compose bind mount).
//  2. Replace the `derp.urls:` block with the merged list
//     (public Tailscale derpmap + skygate's own derpmap URL).
//  3. Restart the headscale container via docker (skygate
//     has /var/run/docker.sock mounted per docker-compose.yml).
//  4. Audit: row `derp_apply_headscale` with before/after
//     config snippet + docker restart output.
//
// Why not just `docker exec headscale configtest`?
// headscale doesn't have a subcommand to update its own
// config — you must write the file, then restart. The
// `headscale serve` process reads config.yaml on every
// start; a graceful restart (SIGHUP) does NOT reload
// config (only ACL policy in some versions), so we have
// to do a full container restart.
//
// Why not just write a deploy script? The operator
// principle: "if admin must SSH to a node or hand-edit
// .env to do X, then X is a gap, not a workaround". A
// one-click Apply button on /admin/derp is the path.
//
// 2026-09-04: v1.5.2+ (B237).

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PostAdminDerpRelaysApplyHeadscale is the one-click
// "Apply derpmap URL to headscale" button on /admin/derp.
// Reads the current derpmap.json URL, rewrites the
// headscale config.yaml's derp.urls block to include it,
// then restarts the headscale container. Audit row
// `derp_apply_headscale` records the new config snippet
// + the docker restart output.
func (s *Service) PostAdminDerpRelaysApplyHeadscale(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=parse_form", http.StatusFound)
		return
	}
	// The skygateURL is the in-cluster URL headscale
	// should fetch the derpmap from. We default to the
	// docker compose service name `skygate:8080` (works
	// because headscale is in the same docker-compose
	// network). The operator can override via env var
	// if running on a different host.
	skygateURL := os.Getenv("SKYGATE_DERPMAP_URL")
	if skygateURL == "" {
		skygateURL = "http://skygate:8080/admin/derp/relays/derpmap.json"
	}
	rewritten, dockerOut, err := applyHeadscaleDerpURLsConfig(skygateURL)
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "derp_apply_headscale",
			fmt.Sprintf("err=%q skygate_url=%s", err.Error(), skygateURL))
		http.Redirect(w, r, "/admin/derp/relays?err="+urlMsg(err),
			http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "derp_apply_headscale",
		fmt.Sprintf("skygate_url=%s config_snippet=%q docker_out=%q",
			skygateURL, firstLines(rewritten, 8), firstLines(dockerOut, 3)))
	// 5s grace — headscale's REST API needs a moment
	// to come back up before the redirect lands.
	time.Sleep(2 * time.Second)
	http.Redirect(w, r, "/admin/derp/relays?ok=applied", http.StatusFound)
}

// firstLines returns the first n non-empty lines of s.
// Used for the audit row to keep the row short.
func firstLines(s string, n int) string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) >= n {
			break
		}
	}
	return strings.Join(out, " | ")
}

// applyHeadscaleDerpURLsConfig reads headscale's
// config.yaml, rewrites the derp.urls block to include
// the skygate derpmap URL, writes the file, and restarts
// headscale. Returns the new file content + docker
// restart output. Pure orchestration — the YAML
// rewriting is the only logic; the rest is plumbing.
//
// The config path is taken from SKYGATE_HEADSCALE_CONFIG_PATH
// (env var, set in docker-compose.yml). If the env var is
// empty OR the file at the given path doesn't exist, we
// try the well-known defaults: /home/admin/headscale/config/
// config.yaml, /home/skyadmin/headscale/config/config.yaml,
// /opt/headscale/config/config.yaml. The first one that
// reads successfully wins.
func applyHeadscaleDerpURLsConfig(skygateURL string) (rewritten string, dockerOut string, err error) {
	configPath := resolveHeadscaleConfigPath()
	containerName := os.Getenv("SKYGATE_HEADSCALE_CONTAINER")
	if containerName == "" {
		containerName = "headscale"
	}
	// 1. Read.
	current, err := readHeadscaleConfig(configPath)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", configPath, err)
	}
	// 2. Rewrite.
	newCfg, err := rewriteDerpURLs(current, skygateURL)
	if err != nil {
		return "", "", fmt.Errorf("rewrite derp.urls: %w", err)
	}
	// 3. Write.
	if err := writeHeadscaleConfig(configPath, newCfg); err != nil {
		return "", "", fmt.Errorf("write %s: %w", configPath, err)
	}
	// 4. Restart.
	out, err := restartDockerContainer(containerName)
	if err != nil {
		// Config was written; surface the docker error
		// so the operator can fix it manually.
		return newCfg, out, fmt.Errorf("restart %s: %w (config written; restart needed)", containerName, err)
	}
	return newCfg, out, nil
}

// readHeadscaleConfig reads the config file.
func readHeadscaleConfig(path string) (string, error) {
	out, err := exec.Command("cat", path).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// resolveHeadscaleConfigPath finds the headscale config
// file. Tries in order: SKYGATE_HEADSCALE_CONFIG_PATH env
// var (if non-empty), /home/admin/headscale/config/config.yaml,
// /home/skyadmin/headscale/config/config.yaml,
// /opt/headscale/config/config.yaml. Returns the first one
// that `cat` succeeds on.
func resolveHeadscaleConfigPath() string {
	candidates := []string{}
	if v := strings.TrimSpace(os.Getenv("SKYGATE_HEADSCALE_CONFIG_PATH")); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates,
		"/home/admin/headscale/config/config.yaml",
		"/home/skyadmin/headscale/config/config.yaml",
		"/opt/headscale/config/config.yaml",
	)
	for _, p := range candidates {
		if _, err := exec.Command("test", "-r", p).Output(); err == nil {
			// `test -r` returns empty output + exit 0 if the
			// file is readable. Anything else is a miss.
			if out, err2 := exec.Command("test", "-r", p).CombinedOutput(); err2 == nil && len(out) == 0 {
				return p
			}
		}
	}
	// Fallback to the env var (which is the default in
	// docker-compose.yml); the read will fail with a clear
	// "read X: no such file" error if the path is wrong.
	if v := os.Getenv("SKYGATE_HEADSCALE_CONFIG_PATH"); v != "" {
		return v
	}
	return "/home/admin/headscale/config/config.yaml"
}

// writeHeadscaleConfig writes the config back. Uses a
// tmp file + atomic mv so a partial write can't leave
// headscale with a broken config.
func writeHeadscaleConfig(path, content string) error {
	tmpPath := path + ".b237.tmp"
	w := exec.Command("sh", "-c", fmt.Sprintf("cat > %s <<'B237_EOF'\n%s\nB237_EOF", tmpPath, content))
	if err := w.Run(); err != nil {
		return err
	}
	return exec.Command("mv", tmpPath, path).Run()
}

// rewriteDerpURLs replaces the `derp:` block in the
// headscale config. The new block contains both the
// public Tailscale derpmap URL AND the skygate derpmap
// URL. If the skygate URL is already in the list, the
// config is returned unchanged (idempotent re-apply).
//
// Pure function — easy to unit test.
//
// The replacement preserves YAML formatting (2-space
// indent, dash list style). If the existing `derp:` block
// is missing, the new block is appended at the end.
func rewriteDerpURLs(yaml, skygateURL string) (string, error) {
	if skygateURL == "" {
		return "", fmt.Errorf("skygateURL is empty")
	}
	publicURL := "https://controlplane.tailscale.com/derpmap/default"
	if strings.Contains(yaml, skygateURL) {
		return yaml, nil // idempotent
	}
	newBlock := "derp:\n  urls:\n  - " + publicURL + "\n  - " + skygateURL + "\n"

	lines := strings.Split(yaml, "\n")
	out := make([]string, 0, len(lines))
	derpFound := false
	i := 0
	for i < len(lines) {
		line := lines[i]
		// Detect the start of a `derp:` block at the top
		// level (no leading whitespace, exact `derp:`).
		if strings.HasPrefix(line, "derp:") {
			derpFound = true
			out = append(out, newBlock)
			i++
			// Skip everything until the next top-level
			// key or EOF.
			for i < len(lines) {
				cur := lines[i]
				if cur == "" {
					i++
					continue
				}
				if !strings.HasPrefix(cur, " ") && !strings.HasPrefix(cur, "\t") && !strings.HasPrefix(cur, "#") {
					break
				}
				i++
			}
			continue
		}
		out = append(out, line)
		i++
	}
	if !derpFound {
		out = append(out, newBlock)
	}
	return strings.Join(out, "\n"), nil
}

// restartDockerContainer restarts a docker container
// via the docker socket (skygate has /var/run/docker.sock
// mounted per docker-compose.yml). 10s timeout so a
// stuck docker restart doesn't block the POST handler.
func restartDockerContainer(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "restart", name)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// log is imported in this file by the package's other
// .go files; we don't need a new import here. But the
// linter wants explicit use, so reference it once.
// (No-op; log.Printf is already used by other files in
// this package and Go's import system is per-file.)
