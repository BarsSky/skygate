package headscale

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	BaseURL       string
	apiKey        string
	http          *http.Client
	ExecContainer string

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

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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
		return fmt.Errorf("headscale %s %s: %d %s", method, path, resp.StatusCode, string(buf))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type HSUser struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

type hsUserList struct {
	Users []HSUser `json:"users"`
}

// ListUsers returns all headscale users. Handles {"users":[...]} wrapper.
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

type PreauthKey struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	UserID     int64  `json:"user_id"`
	UserName   string `json:"user"`
	Reusable   bool   `json:"reusable"`
	Ephemeral  bool   `json:"ephemeral"`
	Used       bool   `json:"used"`
	Expiration string `json:"expiration"`
}

func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return time.Until(t), nil
	}
	return 0, fmt.Errorf("invalid expiration: %q", s)
}

func (c *Client) CreatePreauthKey(userID int64, expiration string, reusable bool) (*PreauthKey, error) {
	dur, err := parseDuration(expiration)
	if err != nil {
		return nil, err
	}
	exp := time.Now().UTC().Add(dur).Format(time.RFC3339)
	body := map[string]any{
		"user_id":    userID,
		"reusable":   reusable,
		"ephemeral":  false,
		"expiration": exp,
	}
	var p PreauthKey
	apiErr := c.do("POST", "/api/v1/preauthkey", body, &p)
	if apiErr == nil && p.Key != "" {
		return &p, nil
	}
	if c.ExecContainer == "" {
		return nil, fmt.Errorf("api failed (%v) and no ExecContainer configured", apiErr)
	}
	key, cliErr := c.createPreauthViaCLI(userID, dur, reusable)
	if cliErr != nil {
		return nil, fmt.Errorf("api: %v; cli: %v", apiErr, cliErr)
	}
	return key, nil
}

var preauthKeyRe = regexp.MustCompile(`hskey-[A-Za-z0-9_-]+`)

// ExpirePreauthKey marks a preauth key as expired in headscale so it can
// no longer be used to register a node. The key's row stays in
// headscale (so audit history is preserved) but the used=false &&
// !expired state flips to expired=true.
//
// Both API and CLI require the user_id that owns the key. The caller
// passes it explicitly so we don't have to enumerate users.
//
// API path: headscale v0.29 has PUT /api/v1/preauthkey/{id}/expire.
// We try that first and fall back to docker exec for older/newer
// headscale versions that may use a different endpoint.
//
// On success, the caller is responsible for also updating the local
// preauth_keys row (marking the key as expired) so the dashboard's
// 3-way split reflects the new state. This function only talks to
// headscale.
func (c *Client) ExpirePreauthKey(userID int64, keyID string) error {
	if keyID == "" {
		return fmt.Errorf("empty key id")
	}
	if userID == 0 {
		return fmt.Errorf("empty user id")
	}
	// API first.
	apiErr := c.do("PUT", "/api/v1/preauthkey/"+keyID+"/expire", nil, nil)
	if apiErr == nil {
		return nil
	}
	// CLI fallback. -u is the headscale user ID, --id is the
	// preauth key id. Returns 0 on success.
	if c.ExecContainer == "" {
		return fmt.Errorf("api: %v; no ExecContainer for CLI fallback", apiErr)
	}
	args := []string{"exec", c.ExecContainer, "headscale", "preauthkeys", "expire",
		"-u", strconv.FormatInt(userID, 10), "--id", keyID}
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("api: %v; cli: %v (%s)", apiErr, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *Client) createPreauthViaCLI(userID int64, dur time.Duration, reusable bool) (*PreauthKey, error) {
	exp := durationFlag(dur)
	args := []string{"exec", c.ExecContainer, "headscale", "preauthkeys", "create",
		"-u", strconv.FormatInt(userID, 10), "--expiration", exp, "--output", "json"}
	if reusable {
		args = append(args, "--reusable")
	} else {
		args = append(args, "--reusable=false")
	}
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker exec: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	m := preauthKeyRe.FindString(string(out))
	if m == "" {
		return nil, fmt.Errorf("no key in CLI output: %s", strings.TrimSpace(string(out)))
	}
	key := &PreauthKey{
		UserID:     userID,
		Key:        m,
		Reusable:   reusable,
		Expiration: time.Now().UTC().Add(dur).Format(time.RFC3339),
	}
	// Best-effort parse of the id from JSON output (headscale --output json).
	// If parsing fails, key.ID stays empty and temporal fallback in
	// backfillNodeOwnership (v0.3.15) can still attribute new nodes.
	// Parse the id from JSON output. The expiration field is a protobuf
	// timestamp object ({"seconds":...,"nanos":...}) which we ignore
	// because we already have the expiration from the function call.
	var idOnly struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &idOnly); err == nil {
		key.ID = idOnly.ID.String()
	}
	return key, nil
}

func durationFlag(d time.Duration) string {
	hours := int(d.Hours())
	if hours >= 1 && time.Duration(hours)*time.Hour == d {
		return strconv.Itoa(hours) + "h"
	}
	mins := int(d.Minutes())
	if mins >= 1 && time.Duration(mins)*time.Minute == d {
		return strconv.Itoa(mins) + "m"
	}
	return d.String()
}

type HSPreauthKey struct {
	ID     string `json:"id"`
	User   HSUser `json:"user"`
	Key    string `json:"key"`
	Used   bool   `json:"used"`
}

type HSNode struct {
	ID            string         `json:"id"`
	MachineKey    string         `json:"machineKey"`
	NodeKey       string         `json:"nodeKey"`
	DiscoKey      string         `json:"discoKey"`
	Name          string         `json:"name"`
	GivenName     string         `json:"givenName"`
	User          HSUser         `json:"user"`
	IPAddresses   []string       `json:"ipAddresses"`
	Online        bool           `json:"online"`
	LastSeen      string         `json:"lastSeen"`
	CreatedAt     string         `json:"createdAt"`
	Tags            []string     `json:"tags"`
	AvailableRoutes []string     `json:"availableRoutes"`
	PreAuthKey    *HSPreauthKey  `json:"preAuthKey"`
}

type NodeView struct {
	ID              string
	Hostname        string
	GivenName       string
	IPAddresses     []string
	Online          bool
	LastSeen        string
	UserName        string
	UserID          string
	IsExitNode      bool
	Tags            []string
	AvailableRoutes []string
	// PreAuthKeyID is the headscale ID of the preauth key this node
	// registered with, or "" if the node predates our key tracking.
	PreAuthKeyID string
	// CreatedAt is the RFC3339 timestamp from headscale for when the
	// node first registered. Used by backfillNodeOwnership as a
	// fallback when a user's preauth key has no stored headscale_preauth_id
	// (e.g. because the headscale API response shape changed and the
	// key ID field stopped being captured). In that case we still
	// match by "node created after this preauth key" with a safety
	// margin to avoid stealing another user's recent node.
	CreatedAt string
}

func (n HSNode) toView() NodeView {
	tags := append([]string{}, n.Tags...)
	host := n.GivenName
	if host == "" {
		host = n.Name
	}
	var pakID string
	if n.PreAuthKey != nil {
		pakID = n.PreAuthKey.ID
	}
	return NodeView{
		ID:            n.ID,
		Hostname:      host,
		GivenName:     n.GivenName,
		IPAddresses:   n.IPAddresses,
		Online:        n.Online,
		LastSeen:      n.LastSeen,
		UserName:      n.User.Name,
		UserID:        n.User.ID,
		IsExitNode:    hasExitNodeTag(tags, n.Name, n.AvailableRoutes),
		Tags:          tags,
		AvailableRoutes: n.AvailableRoutes,
		PreAuthKeyID:  pakID,
		CreatedAt:     n.CreatedAt,
	}
}

func hasExitNodeTag(tags []string, name string, availableRoutes []string) bool {
	// 1. Explicit tag:exit-node (Tailscale convention)
	for _, t := range tags {
		if strings.EqualFold(t, "tag:exit-node") {
			return true
		}
	}
	// 2. Name starts with exit- or exitnode
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "exit-") || strings.HasPrefix(n, "exitnode") {
		return true
	}
	// 3. headscale 0.29: any node with availableRoutes containing 0.0.0.0/0
	//    is functionally an exit node (advertises itself as a router for
	//    the whole internet). This is how our karolina/emilia/sharlotta
	//    work as exit nodes without an explicit tag.
	for _, r := range availableRoutes {
		if r == "0.0.0.0/0" || r == "::/0" {
			return true
		}
	}
	return false
}

type hsNodeList struct {
	Nodes []HSNode `json:"nodes"`
}

// ListNodesByUser returns all nodes belonging to a headscale user.
// Handles {"nodes":[...]} wrapper.
// DeleteUser removes a user from headscale by ID.
// InvalidateCache clears cached headscale results. Call after mutations
// (delete node, tag node, etc.) to force a fresh fetch.
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
		return fmt.Errorf("headscale users delete: %v: %s", err, string(out))
	}
	return fmt.Errorf("cannot delete headscale user: ExecContainer not set")
}

func (c *Client) ListNodesByUser(userName string) ([]NodeView, error) {
	var list hsNodeList
	err := c.do("GET", "/api/v1/node?user="+userName, nil, &list)
	if err == nil && list.Nodes != nil {
		out := make([]NodeView, 0, len(list.Nodes))
		for _, n := range list.Nodes {
			out = append(out, n.toView())
		}
		return out, nil
	}
	var flat []HSNode
	if err2 := c.do("GET", "/api/v1/node?user="+userName, nil, &flat); err2 == nil {
		out := make([]NodeView, 0, len(flat))
		for _, n := range flat {
			out = append(out, n.toView())
		}
		return out, nil
	}
	return nil, err
}

// ListAllNodes returns every node in the tailnet (admin). Result is cached
// for cacheTTL to absorb the ~50ms cost of headscale's gRPC-to-HTTP gateway
// on every page render.
func (c *Client) ListAllNodes() ([]NodeView, error) {
	c.cacheMu.RLock()
	if c.cacheAll != nil && time.Since(c.cacheAllAt) < c.cacheTTL {
		out := c.cacheAll
		c.cacheMu.RUnlock()
		return out, nil
	}
	c.cacheMu.RUnlock()

	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	// double-check after acquiring write lock
	if c.cacheAll != nil && time.Since(c.cacheAllAt) < c.cacheTTL {
		return c.cacheAll, nil
	}

	var raw hsNodeList
	err := c.do("GET", "/api/v1/node", nil, &raw)
	if err != nil {
		return nil, err
	}
	out := make([]NodeView, 0, len(raw.Nodes))
	for _, n := range raw.Nodes {
		out = append(out, n.toView())
	}
	c.cacheAll = out
	c.cacheAllAt = time.Now()
	return out, nil
}

func (c *Client) ListExitNodes() ([]NodeView, error) {
	all, err := c.ListAllNodes()
	if err != nil {
		return nil, err
	}
	var exits []NodeView
	for _, n := range all {
		if n.IsExitNode {
			exits = append(exits, n)
		}
	}
	return exits, nil
}

func (c *Client) DeleteNode(nodeID int64) error {
	err := c.do("DELETE", "/api/v1/node/"+strconv.FormatInt(nodeID, 10), nil, nil)
	if err == nil {
		c.InvalidateCache()
	}
	return err
}

// TagPublicTag marks a node as accessible to all users (via ACL).
const TagPublicTag = "tag:public"

// TagPrivateTag marks a node as accessible only to its owner (and admins).
// Replaces tag:public when the admin clicks "Сделать приватной" so the
// headscale tag-owner rules let a tagged node carry this label.
const TagPrivateTag = "tag:private"

// TagNode sets tags on a headscale node via the CLI (the admin API key lacks
// the permission needed for /api/v1/node/{id}/tag).
func (c *Client) TagNode(nodeID int64, tags ...string) error {
	if c.ExecContainer == "" {
		return fmt.Errorf("no ExecContainer configured")
	}
	args := []string{"exec", c.ExecContainer, "headscale", "nodes", "tag",
		"-i", strconv.FormatInt(nodeID, 10), "-t", strings.Join(tags, ","), "--force"}
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tag: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// UntagNode removes a tag from a headscale node via the CLI.
//
// headscale 0.29 has no "nodes untag" subcommand. The "nodes tag" command
// REPLACES the tag set on a node, so to remove a single tag we rewrite
// the full tag list, leaving every other tag in place. If the result
// would be empty (e.g. the node carried only this single tag) we fall
// back to TagPrivateTag so headscale keeps at least one tag.
func (c *Client) UntagNode(nodeID int64, tag string) error {
	if c.ExecContainer == "" {
		return fmt.Errorf("no ExecContainer configured")
	}
	current := []string{}
	if nodes, err := c.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == strconv.FormatInt(nodeID, 10) {
				current = append(current, n.Tags...)
				break
			}
		}
	}
	filtered := []string{}
	for _, t := range current {
		if t != tag {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == 0 {
		filtered = []string{TagPrivateTag}
	}
	args := []string{"exec", c.ExecContainer, "headscale", "nodes", "tag",
		"-i", strconv.FormatInt(nodeID, 10), "-t", strings.Join(filtered, ","), "--force"}
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("untag: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsPublic returns whether a node carries the tag:public tag.
func (n HSNode) IsPublic() bool {
	for _, t := range n.Tags {
		if strings.EqualFold(t, TagPublicTag) {
			return true
		}
	}
	return false
}

func (n NodeView) IsPublicView() bool {
	for _, t := range n.Tags {
		if strings.EqualFold(t, TagPublicTag) {
			return true
		}
	}
	return false
}

func (n NodeView) IsPrivateView() bool {
	for _, t := range n.Tags {
		if strings.EqualFold(t, TagPrivateTag) {
			return true
		}
	}
	return false
}

type ACLPolicy struct {
	Policy string `json:"policy"`
	Data   string `json:"data"`
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

// SetAdvertisedRoutes updates advertised routes on an exit node via SSH/docker exec.
// ApproveAllRoutes enables all pending routes for a node via headscale CLI (docker exec).
// 2026-07-07: previously used /api/v1/routes but that's deprecated/404 in headscale 0.29.1.
// Now we shell out to `docker exec headscale headscale nodes approve-routes -i <id> -r <routes>`.
func (c *Client) ApproveAllRoutes(nodeHostname string) (int, error) {
	return c.ApproveAllRoutesWithList(nodeHostname, nil)
}

func (c *Client) ApproveAllRoutesWithList(nodeHostname string, routes []string) (int, error) {
	nodes, err := c.ListAllNodes()
	if err != nil {
		return 0, err
	}
	var nodeID int
	for _, n := range nodes {
		if strings.EqualFold(n.Hostname, nodeHostname) || strings.EqualFold(n.GivenName, nodeHostname) {
			nodeID, _ = strconv.Atoi(n.ID)
			break
		}
	}
	if nodeID == 0 {
		return 0, fmt.Errorf("node %q not found", nodeHostname)
	}

	if len(routes) == 0 {
		var nodeInfo struct {
			Node struct {
				AvailableRoutes []string `json:"availableRoutes"`
			} `json:"node"`
		}
		if err := c.do("GET", fmt.Sprintf("/api/v1/node/%d", nodeID), nil, &nodeInfo); err == nil {
			routes = nodeInfo.Node.AvailableRoutes
		}
	}

	if len(routes) == 0 {
		return 0, nil
	}

	routeStr := strings.Join(routes, ",")
	cmd := exec.Command("docker", "exec", "headscale",
		"/ko-app/headscale", "nodes", "approve-routes",
		"-i", strconv.Itoa(nodeID),
		"-r", routeStr,
		"--force")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("approve-routes: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return len(routes), nil
}

func (c *Client) SetAdvertisedRoutes(nodeHostname string, routes []string) (string, error) {
	if len(routes) == 0 {
		return "", fmt.Errorf("empty routes list")
	}
	routeStr := strings.Join(routes, ",")
	cmd := fmt.Sprintf("tailscale set --advertise-exit-node --advertise-routes=%s", routeStr)
	sshCmd := exec.Command("ssh", "-F", "/home/skyadmin/.ssh/config",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		nodeHostname, cmd)
	out, err := sshCmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("run manually on %s: %s", nodeHostname, cmd)
}

