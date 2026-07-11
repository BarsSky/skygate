// Headscale preauth-key operations: create, expire, helpers.
//
// Preauth keys authenticate new node registrations against headscale.
// Both the create and expire paths go through the API first; if the
// API rejects the call (older/newer headscale, missing permission, etc.)
// we fall back to `docker exec <container> headscale preauthkeys ...`
// because the headscale admin API key lacks the scopes for the
// /api/v1/preauthkey/... endpoints in some deployments.
package headscale

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

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

// HSPreauthKey is the headscale-side representation of the preauth key
// embedded in HSNode (see nodes.go). The two structs overlap but headscale
// owns the field naming, so we keep them separate from PreauthKey above
// which is the client-side convenience type.
type HSPreauthKey struct {
	ID   string `json:"id"`
	User HSUser `json:"user"`
	Key  string `json:"key"`
	Used bool   `json:"used"`
}

var preauthKeyRe = regexp.MustCompile(`hskey-[A-Za-z0-9_-]+`)

// parseDuration accepts either a Go duration string ("30s", "5m", "1h")
// or an RFC3339 timestamp. Used by CreatePreauthKey to normalise the
// user-supplied expiration argument.
func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return time.Until(t), nil
	}
	return 0, fmt.Errorf("invalid expiration: %q", s)
}

// durationFlag renders a time.Duration as a string acceptable to the
// `headscale preauthkeys create --expiration=` flag. Whole hours
// ("1h"), whole minutes ("5m"), or fall back to the Go default
// ("1h30m45s"). The headscale CLI rejects fractional units.
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

// CreatePreauthKey creates a new preauth key for userID. Tries the
// headscale API first, falls back to `docker exec` if the API call
// fails. The CLI path also handles parsing the JSON output (when
// --output json is set) to extract the key ID for the temporal
// backfill match in handlers_node_ownership.go.
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

// createPreauthViaCLI shells out to `docker exec <container> headscale
// preauthkeys create`. Parses the hskey-... token out of stdout (the
// CLI is the only place that returns the plaintext key reliably) and
// best-effort parses the JSON --output block to extract the key ID.
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
