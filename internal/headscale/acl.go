// Headscale ACL policy operations: get + set.
//
// The API path works in `policy.mode: database` deployments. For a
// `file`-mode headscale the API answers "update is disabled for modes other
// than database" (500) and we write the policy file + reload headscale
// instead — on a containerised install through the config volume, on a
// native/systemd install straight to disk (B272).
package headscale

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ACLPolicy is the /api/v1/policy response. headscale 0.29.x
// populates `policy` as a NESTED OBJECT (the parsed HuJSON),
// not a stringified blob. Older headscale versions used a
// string. We honour both in GetACL — the nested-object case
// is re-marshalled to a string before caching.
type ACLPolicy struct {
	Policy json.RawMessage `json:"policy"`
	Data   json.RawMessage `json:"data"`
}

// PolicyBody is the request/response for headscale policy API.
type PolicyBody struct {
	Policy string `json:"policy"`
}

// GetACL returns the headscale ACL policy. Falls back to docker exec CLI.
// Result is cached for cacheTTL - the policy rarely changes during a session.
func (c *Client) GetACL() (string, error) {
	c.cacheMu.RLock()
	if c.cacheACL != "" && time.Since(c.cacheACLAt) < c.cacheTTL {
		out := c.cacheACL
		c.cacheMu.RUnlock()
		return out, nil
	}
	c.cacheMu.RUnlock()

	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cacheACL != "" && time.Since(c.cacheACLAt) < c.cacheTTL {
		return c.cacheACL, nil
	}

	var p ACLPolicy
	err := c.do("GET", "/api/v1/policy", nil, &p)
	if err == nil {
		// Resolve which field wins: Policy (current shape,
		// object or stringified) takes precedence over Data
		// (legacy stringified hujson blob). Empty values
		// (including the JSON literal `""` of length 2)
		// are skipped — we fall through to the other field.
		if raw := bytes.TrimSpace(p.Policy); isNonEmptyPolicyField(raw) {
			if raw[0] == '{' || raw[0] == '[' {
				// headscale 0.29.x returns the
				// policy as a JSON OBJECT (the
				// parsed HuJSON), not a stringified
				// blob. Store as-is so we don't
				// double-encode on the PUT side.
				// EnsureTagOwner does its own
				// json.Marshal of the parsed map.
				c.cacheACL = string(raw)
				c.cacheACLAt = time.Now()
				return c.cacheACL, nil
			}
			s := strings.TrimSpace(string(raw))
			c.cacheACL = s
			c.cacheACLAt = time.Now()
			return s, nil
		}
		if raw := bytes.TrimSpace(p.Data); isNonEmptyPolicyField(raw) {
			// The legacy `data` field carries a
			// JSON-stringified policy blob (the value
			// is wrapped in `"…"`). Unquote it before
			// caching so the rest of the pipeline
			// (EnsureTagOwner, SetPolicy) sees the
			// raw policy document.
			s := string(raw)
			if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
				var unq string
				if err := json.Unmarshal([]byte(s), &unq); err == nil {
					s = unq
				}
			}
			c.cacheACL = s
			c.cacheACLAt = time.Now()
			return s, nil
		}
	}
	if c.ExecContainer == "" {
		return "", err
	}
	// Try several CLI variants since headscale versions differ
	variants := [][]string{
		{"policy", "get"},
		{"policy", "show"},
		{"policy"},
	}
	for _, args := range variants {
		fullArgs := append([]string{"exec", c.ExecContainer, "headscale"}, args...)
		cmd := exec.Command("docker", fullArgs...)
		out, cerr := cmd.CombinedOutput()
		if cerr == nil && len(strings.TrimSpace(string(out))) > 0 {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("api: %v; cli: all variants failed", err)
}

// SetPolicy sets the ACL policy.
// Tries REST API first (database mode), then file-mode fallback:
// write ACL to config volume + update config.yaml + restart headscale.
//
// 2026-07-13: the file-mode fallback is now strictly gated on http.StatusNotFound/http.StatusMethodNotAllowed
// from the headscale API. A 5xx (or any non-2xx other than http.StatusNotFound/http.StatusMethodNotAllowed) is
// treated as a real failure and returned to the caller. The previous
// "any error → fallback" heuristic silently masked transient headscale
// failures (e.g. policy rejected mid-restart) by writing to
// acl_policy.hujson while the running headscale was still using the
// database policy — the operator saw "ok" in the audit log while the
// new rules never reached Tailscale clients. http.StatusNotFound/http.StatusMethodNotAllowed are the
// documented headscale signals for "policy endpoint not available in
// this mode" and the only codes the fallback should react to.
func (c *Client) SetPolicy(policy string) error {
	var out PolicyBody
	err := c.do("PUT", "/api/v1/policy", PolicyBody{Policy: policy}, &out)
	if err == nil {
		c.clearACLCache()
		return nil
	}

	if !isFileModePolicyError(err) {
		return err
	}

	return c.setPolicyViaFile(policy, err)
}

// isFileModePolicyError reports whether a failed PUT /api/v1/policy means
// "this headscale keeps its policy in a FILE and refuses API writes" rather
// than a transient failure (B272).
//
// headscale versions disagree on how they say it:
//
//   - 404 / 405 — the endpoint does not exist in this mode (the only codes
//     the pre-B272 code accepted);
//   - 500 with `update is disabled for modes other than database`
//     (code 2 = gRPC ErrInternal) — headscale 0.29.2, verified live on a
//     native install whose config.yaml says `policy.mode: file`. The old
//     check treated this as a real failure, so the file fallback never ran
//     and every EnsureTagOwner died with the 500 while the operator saw
//     only "are invalid or not permitted" from AddTag.
func isFileModePolicyError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	}
	body := strings.ToLower(apiErr.Body)
	// Only a 5xx whose body names the policy mode counts — a 500 from a
	// broken policy (parse error) must stay a real failure.
	if apiErr.StatusCode < 500 {
		return false
	}
	return strings.Contains(body, "update is disabled") ||
		strings.Contains(body, "policy mode") ||
		strings.Contains(body, "modes other than database")
}

// setPolicyViaFile writes the policy to headscale's policy FILE and reloads
// headscale (B272). Two install kinds:
//
//   - CONTAINERISED: `docker run -i --rm -v <policyVolume> alpine sh -c 'cat >
//     /config/<file>'` then `docker restart <container>` (the historical path,
//     with the volume now configurable instead of hardcoded);
//   - NATIVE (systemd / bare binary): write the resolved PolicyPath directly,
//     then restart the unit (SIGHUP is not enough — headscale re-reads the
//     policy file only at startup in 0.29).
//
// When the path is unknown the error says exactly what to set, instead of
// silently doing nothing.
func (c *Client) setPolicyViaFile(policy string, apiErr error) error {
	path := c.PolicyPath
	if path == "" {
		if p, err := DiscoverPolicyPath(); err == nil && p != "" {
			path = p
			c.PolicyPath = p
		}
	}
	if path == "" {
		return fmt.Errorf("api: %w; headscale keeps its policy in a FILE (policy.mode: file) and the policy path is unknown — set SKYGATE_HEADSCALE_POLICY_PATH to the file named by `policy.path` in headscale's config.yaml (e.g. /etc/headscale/policy.hujson) and restart skygate", apiErr)
	}

	if !c.forceNativePolicyWrite {
		if _, err := exec.LookPath("docker"); err == nil {
			return c.setPolicyViaFileDocker(policy, path, apiErr)
		}
	}
	return c.setPolicyViaFileNative(policy, path, apiErr)
}

// setPolicyViaFileDocker writes through the headscale config volume.
func (c *Client) setPolicyViaFileDocker(policy, path string, apiErr error) error {
	// Inside the helper container the policy file is addressed by its
	// container-side path; the volume prefix maps host dir → container dir.
	containerPath := filepath.Base(path)
	mount := c.policyVolume
	if mount == "" {
		mount = "/home/admin/headscale/config:/config"
	}
	writeCmd := exec.Command("docker", "run", "-i", "--rm",
		"-v", mount,
		"alpine", "sh", "-c", "cat > /config/"+containerPath)
	writeCmd.Stdin = strings.NewReader(policy)
	if out, cerr := writeCmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("api: %w; write policy file %s via docker volume %s: %v (%s)", apiErr, path, mount, cerr, strings.TrimSpace(string(out)))
	}
	if out, e := exec.Command("docker", "restart", c.containerOr("headscale")).CombinedOutput(); e != nil {
		return fmt.Errorf("api: %w; wrote %s but restarting headscale failed: %v (%s)", apiErr, path, e, strings.TrimSpace(string(out)))
	}
	c.clearACLCache()
	return nil
}

// setPolicyViaFileNative writes the policy file on the local filesystem and
// restarts the headscale unit.
func (c *Client) setPolicyViaFileNative(policy, path string, apiErr error) error {
	prev, readErr := os.ReadFile(path)
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".skygate.tmp"
	if err := os.WriteFile(tmp, []byte(policy), mode); err != nil {
		return fmt.Errorf("api: %w; write policy file %s: %v (the skygate service user cannot write it — either run skygate with write access to headscale's config dir, or apply this policy manually: %s)", apiErr, path, err, oneLinePolicy(policy))
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("api: %w; replace policy file %s: %v", apiErr, path, err)
	}
	if err := c.restartHeadscaleUnit(); err != nil {
		// Roll back: a half-applied policy is worse than none, and the
		// operator needs the previous rules back.
		if readErr == nil {
			_ = os.WriteFile(path, prev, mode)
		}
		return fmt.Errorf("api: %w; wrote %s but restarting %s failed: %v", apiErr, path, c.headscaleUnit, err)
	}
	c.clearACLCache()
	return nil
}

// restartHeadscaleUnit restarts the local headscale service (native install).
func (c *Client) restartHeadscaleUnit() error {
	unit := c.headscaleUnit
	if unit == "" {
		unit = "headscale"
	}
	// Look up the binaries (not just systemctl) so the same code works on a
	// host with systemd, on OpenRC, and under a test shim on PATH.
	if _, err := exec.LookPath("systemctl"); err == nil {
		out, err := exec.Command("systemctl", "restart", unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl restart %s: %v (%s)", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// OpenRC / bare binary host: rc-service is the Alpine equivalent.
	if _, err := exec.LookPath("rc-service"); err == nil {
		out, rcErr := exec.Command("rc-service", unit, "restart").CombinedOutput()
		if rcErr != nil {
			return fmt.Errorf("rc-service %s restart: %v (%s)", unit, rcErr, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("neither systemctl nor rc-service is available to restart %q (start headscale manually so it re-reads the policy file)", unit)
}

// DiscoverPolicyPath reads the local headscale configuration and returns the
// `policy.path` value (B272). It searches the documented locations and
// understands the flat YAML shapes headscale ships, including the
// `policy:\n  mode: file\n  path: /etc/headscale/policy.hujson` block.
//
// Deliberately dependency-free (no YAML library): the value is a single
// scalar and headscale's own config template always writes it as
// `path: <value>` under `policy:`.
func DiscoverPolicyPath() (string, error) {
	candidates := []string{
		os.Getenv("SKYGATE_HEADSCALE_CONFIG"),
		"/etc/headscale/config.yaml",
		"/etc/headscale/config.yml",
		"/var/lib/headscale/config.yaml",
		"/opt/headscale/config.yaml",
	}
	var lastErr error
	for _, p := range candidates {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			lastErr = err
			continue
		}
		if path, mode := parseHeadscalePolicyConfig(string(b)); path != "" {
			// Only a file-mode policy has a path worth writing; in database
			// mode `path` may still be set as a seed and must NOT be treated
			// as authoritative.
			if mode == "" || mode == "file" {
				return path, nil
			}
			lastErr = fmt.Errorf("%s declares policy.mode=%s — the API path should have worked", p, mode)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no headscale config.yaml found in the standard locations")
	}
	return "", lastErr
}

// parseHeadscalePolicyConfig extracts (policy.path, policy.mode) from a
// headscale config.yaml. Only the `policy:` block is considered, so a `path:`
// belonging to another section (database, derp, …) can never be mistaken for
// the policy file.
func parseHeadscalePolicyConfig(cfg string) (path, mode string) {
	inPolicy := false
	policyIndent := -1
	for _, raw := range strings.Split(cfg, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if !inPolicy {
			if strings.HasPrefix(trimmed, "policy:") {
				inPolicy = true
				policyIndent = indent
			}
			continue
		}
		// A non-indented key ends the policy block.
		if indent <= policyIndent && !strings.HasPrefix(trimmed, "policy:") {
			inPolicy = false
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "path:"):
			path = yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "path:")))
		case strings.HasPrefix(trimmed, "mode:"):
			mode = yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "mode:")))
		}
	}
	return path, mode
}

// yamlScalar strips surrounding quotes and any trailing comment from a
// single-line YAML scalar.
func yamlScalar(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	v = strings.Trim(v, `"'`)
	return v
}

// oneLinePolicy renders a policy for an error message so the operator can
// apply it by hand when skygate cannot write the file.
func oneLinePolicy(policy string) string {
	flat := strings.Join(strings.Fields(policy), " ")
	const max = 400
	if len(flat) > max {
		flat = flat[:max] + "…"
	}
	return flat
}

// containerOr returns the configured container name or the given default.
func (c *Client) containerOr(def string) string {
	if c.ExecContainer != "" {
		return c.ExecContainer
	}
	return def
}

// clearACLCache drops the cached ACL string. Called by SetPolicy on
// success so the next GetACL re-reads the new policy. (The other
// caches — cacheAll/cacheUsers — are cleared by InvalidateCache in
// client.go when nodes or users change.)
func (c *Client) clearACLCache() {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cacheACL = ""
	c.cacheACLAt = time.Time{}
}

// isNonEmptyPolicyField returns true if the raw policy field
// carries a usable policy. The JSON literal `""` (two double-
// quotes, length 2) is an explicit empty marker from headscale
// and is treated the same as a missing field.
func isNonEmptyPolicyField(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if len(raw) == 2 && raw[0] == '"' && raw[1] == '"' {
		return false
	}
	// Bare whitespace also means "no policy".
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !(len(trimmed) == 2 && trimmed[0] == '"' && trimmed[1] == '"')
}
