// Package headscale is a thin client over headscale v0.29's admin
// REST API, with a `docker exec` fallback for endpoints the admin
// API key doesn't expose (tag, preauth, etc.).
//
// The package is split across several files for readability:
//
//	headscale.go  — Client struct, New, HTTP helper (do), cache lifecycle
//	users.go      — HSUser + ListUsers / CreateUser / DeleteUser
//	preauth.go    — PreauthKey + create/expire (with CLI fallback)
//	nodes.go      — HSNode / NodeView + list/exit-node/delete + NodeList/NodeInfo
//	tags.go       — TagPublicTag / TagPrivateTag + TagNode / UntagNode + IsPublic*
//	acl.go        — ACLPolicy / GetACL / SetPolicy (with file-mode fallback)
//	routes.go     — ApproveAllRoutes* (headscale side) + SetAdvertisedRoutes (SSH)
//	route_args.go — pure helpers for the tailscale set command line
package headscale

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// Client is the entry point for talking to headscale. The fields
// below the cache block are not exported; everything in the cache
// is private to this package.
type Client struct {
	BaseURL       string
	apiKey        string
	http          *http.Client
	ExecContainer string

	// dockerRunner is the function used to shell out `docker`
	// commands (ExtendNodeExpiry, fallback paths in
	// CreatePreauthKeyWithTags, etc.). nil = use the
	// default (exec.Command("docker", ...)). Tests can
	// inject a stub that records the call without
	// touching the system docker.
	dockerRunner func(args ...string) ([]byte, error)

	// Caches to avoid hammering headscale on every page render. Each cache
	// entry holds a value + the time it was populated. Reads return the
	// cached value if it's still fresh, otherwise they fetch and refresh.
	cacheMu      sync.RWMutex
	cacheAll     []NodeView
	cacheAllAt   time.Time
	cacheUsers   []HSUser
	cacheUsersAt time.Time
	cacheACL     string
	cacheACLAt   time.Time
	cacheTTL     time.Duration
}

// New returns a Client configured for the given headscale URL and API
// key. ExecContainer defaults to env HEADSCALE_CONTAINER, then to
// "headscale" (the docker-compose service name).
func New(baseURL, k string) *Client {
	v := k
	c := &Client{}
	c.BaseURL = baseURL
	c.apiKey = v
	c.http = &http.Client{Timeout: 10 * time.Second}
	c.ExecContainer = getenvDefault("HEADSCALE_CONTAINER", "headscale")
	c.cacheTTL = 5 * time.Second
	return c
}

// ApiKeyForCache returns the api key this client was built
// with. Used by handlers.App.clientFor to detect when an
// admin has rotated a per-user api_key via /admin/users
// and the cached client is now stale. The api key itself
// is treated as a write-capable secret on headscale, so
// callers should never log it.
func (c *Client) ApiKeyForCache() string {
	return c.apiKey
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// APIError is the typed error returned by do() for non-2xx responses.
// Callers can use errors.As(err, &apiErr) to inspect StatusCode and
// decide between "endpoint not supported in this headscale mode" (e.g.
// file-mode 404/405) and "request failed for other reasons" (5xx,
// network). The Error() string keeps the legacy "headscale METHOD PATH:
// CODE BODY" format so existing log scrapers / human greps stay
// readable.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("headscale %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// do executes a JSON request against c.BaseURL + path and decodes
// the response into out (if non-nil). 4xx/5xx responses are returned
// as *APIError so callers can branch on the status code (e.g. file-mode
// fallback detection in SetPolicy).
func (c *Client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		return &APIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       string(buf),
		}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// InvalidateCache clears all three caches (nodes, users, ACL).
// Call after mutations (delete node, tag node, etc.) to force a
// fresh fetch. SetPolicy uses clearACLCache (in acl.go) for the
// narrower case of ACL-only changes.
func (c *Client) InvalidateCache() {
	c.cacheMu.Lock()
	c.cacheAll = nil
	c.cacheUsers = nil
	c.cacheACL = ""
	c.cacheAllAt = time.Time{}
	c.cacheUsersAt = time.Time{}
	c.cacheACLAt = time.Time{}
	c.cacheMu.Unlock()
}

// SetDockerRunner overrides the function used to shell out
// `docker` commands. nil = use the default (exec.Command).
// Used by tests to inject a stub that records the call
// without touching the system docker.
func (c *Client) SetDockerRunner(fn func(args ...string) ([]byte, error)) {
	c.dockerRunner = fn
}
