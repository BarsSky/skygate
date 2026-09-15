// Headscale ACL policy operations: get + set.
//
// The API path works in `policy.mode: database` deployments. For
// `file`-mode headscale (no DB-backed ACL) the API rejects the call
// and we fall back to writing acl_policy.hujson to the config volume
// and restarting the container.
package headscale

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
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

	// Only fall back to file-mode on http.StatusNotFound/http.StatusMethodNotAllowed. Any other error is real.
	var apiErr *APIError
	if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusNotFound && apiErr.StatusCode != http.StatusMethodNotAllowed) {
		return err
	}

	// File-mode fallback: headscale rejects API in non-database mode.
	// Write ACL file to headscale config volume via alpine helper.
	// Use acl_policy.hujson (the path already referenced in config.yaml policy section).
	writeCmd := exec.Command("docker", "run", "-i", "--rm",
		"-v", "/home/admin/headscale/config:/config",
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
