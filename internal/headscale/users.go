// Headscale user operations: list / create / delete.
//
// All headscale user-management API endpoints live here. The headscale
// API returns {"users":[...]} as a wrapper, but some older versions
// return a flat array; ListUsers handles both shapes transparently.
package headscale

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type HSUser struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

type hsUserList struct {
	Users []HSUser `json:"users"`
}

// ListUsers returns all headscale users. Handles {"users":[...]} wrapper.
// Result is cached for cacheTTL to absorb the cost of headscale's
// gRPC-to-HTTP gateway on every page render.
func (c *Client) ListUsers() ([]HSUser, error) {
	c.cacheMu.RLock()
	if c.cacheUsers != nil && time.Since(c.cacheUsersAt) < c.cacheTTL {
		out := c.cacheUsers
		c.cacheMu.RUnlock()
		return out, nil
	}
	c.cacheMu.RUnlock()

	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cacheUsers != nil && time.Since(c.cacheUsersAt) < c.cacheTTL {
		return c.cacheUsers, nil
	}

	var list hsUserList
	err := c.do("GET", "/api/v1/user", nil, &list)
	if err == nil && list.Users != nil {
		c.cacheUsers = list.Users
		c.cacheUsersAt = time.Now()
		return list.Users, nil
	}
	// fallback for older headscale returning flat array
	var flat []HSUser
	if err2 := c.do("GET", "/api/v1/user", nil, &flat); err2 == nil {
		c.cacheUsers = flat
		c.cacheUsersAt = time.Now()
		return flat, nil
	}
	return nil, err
}

// CreateUser creates a new headscale user, or returns the existing one
// if the API call fails with a duplicate-name error. The headscale
// admin API does not consistently return the created user, so on
// failure we list users and look up by name as a best-effort fallback.
func (c *Client) CreateUser(name string) (*HSUser, error) {
	var u HSUser
	err := c.do("POST", "/api/v1/user", map[string]string{"name": name}, &u)
	if err == nil && u.ID != "" {
		return &u, nil
	}
	users, lerr := c.ListUsers()
	if lerr != nil {
		if err != nil {
			return nil, fmt.Errorf("create err: %v; list err: %v", err, lerr)
		}
		return nil, lerr
	}
	for i := range users {
		if users[i].Name == name {
			return &users[i], nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("user %q not found after create-err: %v", name, err)
	}
	return &u, nil
}

// DeleteUser removes a user from headscale by ID. Headscale refuses to
// delete a user with active nodes, so we enumerate nodes first (via the
// CLI — the admin API requires pagination), drop the ones owned by
// this user, and then call users delete. Returns the underlying CLI
// error if both passes fail.
func (c *Client) DeleteUser(userID int64) error {
	// First, delete all nodes owned by this user (headscale refuses to delete user with active nodes)
	if c.ExecContainer != "" {
		cmd := exec.Command("docker", "exec", c.ExecContainer, "headscale", "nodes", "list", "-o", "json")
		out, err := cmd.CombinedOutput()
		if err == nil {
			var nodes []struct {
				ID   string `json:"id"`
				User struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"user"`
			}
			if json.Unmarshal(out, &nodes) == nil {
				for _, n := range nodes {
					if n.User.ID == strconv.FormatInt(userID, 10) {
						nid, _ := strconv.ParseInt(n.ID, 10, 64)
						_ = c.DeleteNode(nid)
					}
				}
			}
		}
		// Now delete user via CLI
		cmd = exec.Command("docker", "exec", c.ExecContainer, "headscale", "users", "delete", "-u", "-f", strconv.FormatInt(userID, 10))
		out, err = cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		return fmt.Errorf("headscale users delete: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("cannot delete headscale user: ExecContainer not set")
}
