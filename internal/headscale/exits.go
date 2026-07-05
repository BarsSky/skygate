package headscale

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// PolicyBody is the request/response for headscale policy API.
type PolicyBody struct {
	Policy string `json:"policy"`
}

// SetPolicy sets the ACL policy.
// Tries REST API first (database mode), then file-mode fallback:
// write ACL to config volume + update config.yaml + restart headscale.
func (c *Client) SetPolicy(policy string) error {
	var out PolicyBody
	err := c.do("PUT", "/api/v1/policy", PolicyBody{Policy: policy}, &out)
	if err == nil {
		c.clearACLCache()
		return nil
	}

	// File-mode fallback: headscale rejects API in non-database mode.
	// Write ACL file to headscale config volume via alpine helper.
	// Use acl_policy.hujson (the path already referenced in config.yaml policy section).
	writeCmd := exec.Command("docker", "run", "-i", "--rm",
		"-v", "/home/skyadmin/headscale/config:/config",
		"alpine", "sh", "-c", "cat > /config/acl_policy.hujson")
	writeCmd.Stdin = strings.NewReader(policy)
	if cerr := writeCmd.Run(); cerr != nil {
		return fmt.Errorf("api: %v; write acl file: %v", err, cerr)
	}

	// Restart headscale to pick up new policy
	restartCmd := exec.Command("docker", "restart", c.ExecContainer)
	if o, e := restartCmd.CombinedOutput(); e != nil {
		return fmt.Errorf("api: %v; restart: %v (%s)", err, e, strings.TrimSpace(string(o)))
	}

	c.clearACLCache()
	return nil
}


// NodeList returns all headscale nodes.
func (c *Client) NodeList() ([]map[string]any, error) {
	var list struct {
		Nodes []struct {
			ID        string   `json:"id"`
			GivenName string   `json:"givenName"`
			IPAddress string   `json:"ipAddress4"`
			UserID    string   `json:"user"`
			Tags      []string `json:"forcedTags"`
		} `json:"nodes"`
	}
	err := c.do("GET", "/api/v1/node?show_all=true", nil, &list)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, n := range list.Nodes {
		out = append(out, map[string]any{
			"id": n.ID, "hostname": n.GivenName,
			"ip": n.IPAddress, "user": n.UserID,
		})
	}
	return out, nil
}

func (c *Client) clearACLCache() {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cacheACL = ""
	c.cacheACLAt = time.Time{}
}

// NodeInfo returns hostname + IP for a node matching hostname prefix.
func (c *Client) NodeInfo(hostname string) (string, string, error) {
	var list struct {
		Nodes []struct {
			ID         string   `json:"id"`
			GivenName  string   `json:"givenName"`
			IPAddress4 string   `json:"ipAddress4"`
			Tags       []string `json:"forcedTags"`
		} `json:"nodes"`
	}
	if err := c.do("GET", "/api/v1/node?show_all=true", nil, &list); err != nil {
		return "", "", err
	}
	for _, n := range list.Nodes {
		if strings.EqualFold(n.GivenName, hostname) || strings.HasPrefix(n.GivenName, hostname) {
			return n.GivenName, n.IPAddress4, nil
		}
	}
	return "", "", fmt.Errorf("node %q not found", hostname)
}
