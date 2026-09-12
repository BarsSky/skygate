// Headscale user operations: list / create / delete.
//
// All headscale user-management API endpoints live here. The headscale
// API returns {"users":[...]} as a wrapper, but some older versions
// return a flat array; ListUsers handles both shapes transparently.
package headscale

import (
	"encoding/json"
	"fmt"
	"net/url"
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
		// Now delete user via CLI.
		//
		// 2026-07-20: v0.21.1 fix — was using "-u
		// -f <id>" (typo for the identifier flag);
		// headscale's CLI parser reads "-u" as a
		// flag with no value and fails with
		// "unknown shorthand flag: 'u' in -u".
		// The correct flag is "-i" / "--identifier"
		// (see `headscale users delete --help` —
		// "Flags: -i, --identifier int"). The
		// "--force" global flag has no short
		// alias in 0.29.x, so we use the long
		// form. The previous bug left stale
		// "orphan" headscale users after every
		// skygate user delete; the audit log
		// captured every failed attempt with
		// "Error: unknown shorthand flag: 'u' in -u".
		cmd = c.deleteUserCmd(userID)
		out, err = cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		return fmt.Errorf("headscale users delete: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("cannot delete headscale user: ExecContainer not set")
}

// deleteUserCmd builds the `docker exec ...
// headscale users delete` command for a given
// user ID. Extracted as a method so the test
// can assert the exact args without spinning
// up a subprocess (Windows PATH + exec.Command
// is fragile enough that a real subprocess test
// is more brittle than helpful).
//
// 2026-07-20: v0.21.1 — extracted from
// DeleteUser to make the -i / --force fix
// regression-testable.
func (c *Client) deleteUserCmd(userID int64) *exec.Cmd {
	return exec.Command("docker", "exec", c.ExecContainer, "headscale", "users", "delete",
		"-i", strconv.FormatInt(userID, 10),
		"--force")
}

// RenameUser renames an existing headscale user. The new name goes in
// the URL path (NOT the request body) — this is the headscale v0.20+
// gRPC HTTP gateway shape; the pre-0.20 endpoint was
// POST /api/v1/user/{id}/rename with a {"name":"..."} body. The two
// are visually similar but headscale v0.29 silently ignores the body
// and only reads the path.
//
// Used by PostAdminUserRename (feature/admin/users.go) when the
// operator resolves a SKYGATE_ADMIN_USER ↔ headscale admin drift
// detected by check_b_admin_user_sync.sh (T1). After a successful
// rename the user cache is invalidated so the next ListUsers() call
// fetches the new name instead of serving a stale entry for the
// cacheTTL (5s by default).
//
// Returns the updated *HSUser, the *APIError on headscale rejection
// (e.g. duplicate-name conflict: 500 "expected exactly one user,
// found 2"; the operator must delete the duplicate first).
func (c *Client) RenameUser(userID int64, newName string) (*HSUser, error) {
	if newName == "" {
		return nil, fmt.Errorf("RenameUser: newName is empty")
	}
	// URL-path-encode the new name so "+" doesn't decode to " " on
	// the server side. path.Join leaves + alone; url.PathEscape
	// encodes it to %2B which headscale's gRPC gateway decodes
	// back to +.
	escaped := url.PathEscape(newName)
	path := fmt.Sprintf("/api/v1/user/%d/rename/%s", userID, escaped)
	var resp struct {
		User HSUser `json:"user"`
	}
	// c.do sends no body when the body arg is nil — headscale
	// v0.20+ takes the new name from the path, NOT the body.
	if err := c.do("POST", path, nil, &resp); err != nil {
		return nil, err
	}
	// Invalidate the user cache so the next ListUsers() fetches
	// the new name. We do this BEFORE returning so a caller that
	// immediately re-lists (e.g. PostAdminUserRename → redirect →
	// GetAdminUsers) sees the fresh name.
	c.InvalidateCache()
	if resp.User.ID == "" {
		// Defensive: headscale v0.29 always returns a populated
		// user envelope on success. If it's empty we treat it as
		// an APIError so the handler can surface a clear message.
		return nil, &APIError{
			Method:     "POST",
			Path:       path,
			StatusCode: 0,
			Body:       "empty response: missing user envelope",
		}
	}
	return &resp.User, nil
}
