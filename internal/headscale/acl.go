// Headscale ACL policy operations: get + set.
//
// The API path works in `policy.mode: database` deployments. For
// `file`-mode headscale (no DB-backed ACL) the API rejects the call
// and we fall back to writing acl_policy.hujson to the config volume
// and restarting the container.
package headscale

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ACLPolicy is the /api/v1/policy response. Different headscale
// versions populate either `policy` (string) or `data` (string); we
// honour both in GetACL.
type ACLPolicy struct {
	Policy string `json:"policy"`
	Data   string `json:"data"`
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
		if s := strings.TrimSpace(p.Policy); s != "" {
			c.cacheACL = s
			c.cacheACLAt = time.Now()
			return s, nil
		}
		if s := strings.TrimSpace(p.Data); s != "" {
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
// 2026-07-13: the file-mode fallback is now strictly gated on 404/405
// from the headscale API. A 5xx (or any non-2xx other than 404/405) is
// treated as a real failure and returned to the caller. The previous
// "any error → fallback" heuristic silently masked transient headscale
// failures (e.g. policy rejected mid-restart) by writing to
// acl_policy.hujson while the running headscale was still using the
// database policy — the operator saw "ok" in the audit log while the
// new rules never reached Tailscale clients. 404/405 are the
// documented headscale signals for "policy endpoint not available in
// this mode" and the only codes the fallback should react to.
func (c *Client) SetPolicy(policy string) error {
	var out PolicyBody
	err := c.do("PUT", "/api/v1/policy", PolicyBody{Policy: policy}, &out)
	if err == nil {
		c.clearACLCache()
		return nil
	}

	// Only fall back to file-mode on 404/405. Any other error is real.
	var apiErr *APIError
	if !errors.As(err, &apiErr) || (apiErr.StatusCode != 404 && apiErr.StatusCode != 405) {
		return err
	}

	// File-mode fallback: headscale rejects API in non-database mode.
	// Write ACL file to headscale config volume via alpine helper.
	// Use acl_policy.hujson (the path already referenced in config.yaml policy section).
	writeCmd := exec.Command("docker", "run", "-i", "--rm",
		"-v", "/home/skyadmin/headscale/config:/config",
		"alpine", "sh", "-c", "cat > /config/acl_policy.hujson")
	writeCmd.Stdin = strings.NewReader(policy)
	if cerr := writeCmd.Run(); cerr != nil {
		return fmt.Errorf("api: %w; write acl file: %v", err, cerr)
	}

	// Restart headscale to pick up new policy
	restartCmd := exec.Command("docker", "restart", c.ExecContainer)
	if o, e := restartCmd.CombinedOutput(); e != nil {
		return fmt.Errorf("api: %w; restart: %v (%s)", err, e, strings.TrimSpace(string(o)))
	}

	c.clearACLCache()
	return nil
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
