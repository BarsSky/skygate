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
	// ACLTags is what headscale 0.29.x calls the key's tags (`aclTags` on the
	// wire, `acl_tags` in the request). Recorded so callers can VERIFY that a
	// requested tag actually landed — a silently untagged key is the failure
	// mode B304 found.
	ACLTags []string `json:"aclTags,omitempty"`
}

// preauthKeyWire is the shape headscale actually SERIALISES a preauth key in
// (B304). Two differences from the flat struct above matter:
//
//   - the object is wrapped: {"preAuthKey": {…}} — a flat decode therefore
//     matched nothing and every successful create looked like "no key in the
//     response", which is why the caller fell through to the CLI rung even when
//     the API had answered 200 (on the native host `aro` that rung then died
//     with the docker error, so the operator saw BOTH failures at once);
//   - `user` is an OBJECT ({"id":"85","name":"infra",…}) and `expiration` is a
//     protobuf Timestamp ({"seconds":…,"nanos":…}), not the flat string/id the
//     old struct assumed.
//
// Both are decoded tolerantly here so an older flat response keeps working.
type preauthKeyWire struct {
	ID         string          `json:"id"`
	Key        string          `json:"key"`
	Reusable   bool            `json:"reusable"`
	Ephemeral  bool            `json:"ephemeral"`
	Used       bool            `json:"used"`
	Expiration json.RawMessage `json:"expiration"`
	User       json.RawMessage `json:"user"`
	UserID     int64           `json:"user_id"`
	ACLTags    []string        `json:"aclTags"`
}

// parsePreauthKey decodes one key from either the 0.29.x envelope
// ({"preAuthKey": {…}}) or a flat object, normalising the varying `user` and
// `expiration` shapes.
func parsePreauthKey(raw []byte) (*PreauthKey, error) {
	var env struct {
		PreAuthKey *preauthKeyWire `json:"preAuthKey"`
	}
	w := &preauthKeyWire{}
	if err := json.Unmarshal(raw, &env); err == nil && env.PreAuthKey != nil {
		w = env.PreAuthKey
	} else if err := json.Unmarshal(raw, w); err != nil {
		return nil, err
	}
	out := &PreauthKey{
		ID:        w.ID,
		Key:       w.Key,
		UserID:    w.UserID,
		Reusable:  w.Reusable,
		Ephemeral: w.Ephemeral,
		Used:      w.Used,
		ACLTags:   w.ACLTags,
	}
	out.Expiration = wireExpiration(w.Expiration)
	out.UserID, out.UserName = wireUser(w.User, w.UserID)
	return out, nil
}

// wireUser accepts `user` as an object ({"id","name"}), a number or a string,
// and falls back to the legacy flat `user_id`.
func wireUser(raw json.RawMessage, legacyID int64) (int64, string) {
	if len(raw) == 0 {
		return legacyID, ""
	}
	var obj struct {
		ID   json.Number `json:"id"`
		Name string      `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Name != "" {
		id, _ := obj.ID.Int64()
		return id, obj.Name
	}
	var asNum json.Number
	if err := json.Unmarshal(raw, &asNum); err == nil && asNum.String() != "" {
		id, _ := asNum.Int64()
		return id, ""
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		if id, err := strconv.ParseInt(asStr, 10, 64); err == nil {
			return id, ""
		}
		return legacyID, asStr
	}
	return legacyID, ""
}

// wireExpiration accepts an RFC3339 string or a protobuf Timestamp object.
func wireExpiration(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var ts struct {
		Seconds int64 `json:"seconds"`
	}
	if err := json.Unmarshal(raw, &ts); err == nil && ts.Seconds > 0 {
		return time.Unix(ts.Seconds, 0).UTC().Format(time.RFC3339)
	}
	return ""
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
	return c.CreatePreauthKeyWithTags(userID, expiration, reusable, nil)
}

// CreatePreauthKeyWithTags is like CreatePreauthKey but also tags
// the key with the given set of headscale tags (e.g. "tag:subnet-router").
//
// 2026-07-17: v0.16.7 — per-user subnet sidecar. The preauth key
// generated for the user's tailscale sidecar MUST be tagged
// `tag:subnet-router` so headscale's ACL recognises the resulting
// node as eligible to advertise `10.0.<uid>.0/24`. The auto-approver
// in internal/sidecar watches for nodes with this exact tag.
//
// B304 (2026-09-23) — THE REQUEST FIELD IS `user`, NOT `user_id`.
//
// Live on the native host `aro`, «Сгенерировать ключ» answered
//
//	api: headscale POST /api/v1/preauthkey: 500 {"code":2,
//	     "message":"auth-key must be either tagged or owned by user"}
//	cli: docker exec: exec: "docker": executable file not found in $PATH
//
// The API half was a WRONG FIELD NAME, not a permission problem: headscale
// 0.29.x reads the owner from `user` and silently DISCARDS the unknown
// `user_id` (its gateway is protojson with unknown fields discarded), so
// headscale computed user=0, and its own validation answered with exactly the
// message above. Measured against the live API (headscale v0.29.3):
//
//	{"user":85}                     → 200, key created
//	{"user":85,"tags":["tag:…"]}    → 200, key created
//	{"user_id":85}                  → 500 "auth-key must be either tagged or owned by user"
//	{"tags":["tag:…"]} (no user)    → 500, the same message
//
// The fallback half was the SAME install-kind defect B267/B272 fixed elsewhere:
// the CLI rung hardcoded `docker exec`, so on a host without docker — every
// native install — it could not run at all. Both rungs now behave: the API
// sends `user`, and the CLI goes through runHeadscaleCLI (docker when docker
// exists, the local `headscale` binary otherwise).
func (c *Client) CreatePreauthKeyWithTags(userID int64, expiration string, reusable bool, tags []string) (*PreauthKey, error) {
	dur, err := parseDuration(expiration)
	if err != nil {
		return nil, err
	}
	exp := time.Now().UTC().Add(dur).Format(time.RFC3339)
	body := map[string]any{
		// `user` is the field headscale 0.29.x reads (see B304 above).
		// Sending `user_id` here cost the operator the whole key-generation
		// feature: it is ignored, so the owner looked unset.
		"user":       userID,
		"reusable":   reusable,
		"ephemeral":  false,
		"expiration": exp,
	}
	if len(tags) > 0 {
		// `acl_tags` — NOT `tags`. Measured on v0.29.3: a request with
		// {"user":85,"tags":["tag:exit-node"]} returns the key with
		// `aclTags: []` (the tag is silently dropped, so the node registered
		// with that key never becomes an exit node), while the same request
		// with `acl_tags` returns `aclTags: ["tag:exit-node"]`.
		body["acl_tags"] = tags
	}
	var rawKey json.RawMessage
	apiErr := c.do("POST", "/api/v1/preauthkey", body, &rawKey)
	if apiErr == nil {
		// B304: the 0.29.x response is wrapped in {"preAuthKey": {…}} and renders
		// `user`/`expiration` differently from the flat struct, so a successful
		// create used to look like an empty answer and the caller fell through to
		// the CLI rung. Parse both shapes.
		if parsed, perr := parsePreauthKey(rawKey); perr == nil && parsed.Key != "" {
			return parsed, nil
		}
		apiErr = fmt.Errorf("unparseable preauth key response: %s", truncateForKeyErr(string(rawKey)))
	}
	if c.ExecContainer == "" {
		return nil, fmt.Errorf("api failed (%v) and no ExecContainer configured", apiErr)
	}
	key, cliErr := c.createPreauthViaCLIWithTags(userID, dur, reusable, tags)
	if cliErr != nil {
		return nil, fmt.Errorf("api: %v; cli: %v", apiErr, cliErr)
	}
	return key, nil
}

// createPreauthViaCLIWithTags runs `headscale preauthkeys create` on whichever
// install kind this deployment is (B304 — through runHeadscaleCLI, so a native
// host without docker uses the local binary instead of dying on
// `exec: "docker": executable file not found in $PATH`). Parses the hskey-…
// token out of stdout (the CLI is the only place that returns the plaintext key
// reliably) and best-effort parses the JSON --output block for the key ID.
//
// headscale 0.23+ requires the owner or a tag; the CLI takes them as
// `-u <id>` and one `--tags <tag>` per tag (verified against v0.29.x, and the
// same spelling the repo's own operator docs use).
func (c *Client) createPreauthViaCLIWithTags(userID int64, dur time.Duration, reusable bool, tags []string) (*PreauthKey, error) {
	exp := durationFlag(dur)
	args := []string{"preauthkeys", "create",
		"-u", strconv.FormatInt(userID, 10), "--expiration", exp, "--output", "json"}
	if reusable {
		args = append(args, "--reusable")
	} else {
		args = append(args, "--reusable=false")
	}
	for _, t := range tags {
		args = append(args, "--tags", t)
	}
	out, err := c.runHeadscaleCLI(args...)
	if err != nil {
		return nil, fmt.Errorf("headscale CLI: %v (%s)", err, strings.TrimSpace(string(out)))
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
// headscale (so audit history is preserved) and its expiration moves to "now".
//
// B304 (2026-09-23) — the route and the CLI flags below were both wrong for
// headscale 0.29.x, so expiry silently never worked on the live hosts. Measured
// against headscale v0.29.3:
//
//	PUT  /api/v1/preauthkey/999999/expire  → 404 Not Found
//	POST /api/v1/preauthkey/999999/expire  → 404 Not Found
//	POST /api/v1/preauthkey/expire {"id":"1"} → 200, and the key's expiration
//	                                            really moved to now
//
// and `headscale preauthkeys expire --help` lists exactly one flag — `-i/--id`
// — so the old `-u <user>` argv could only ever fail with "unknown flag: -u"
// (that is why the rung is now `--id … --force`, with no user argument at all).
//
// The rungs are therefore: POST /api/v1/preauthkey/expire (0.29.x) → the older
// PUT /{id}/expire → the install-kind-aware CLI.
//
// On success, the caller is responsible for also updating the local
// preauth_keys row (marking the key as expired) so the dashboard's
// 3-way split reflects the new state. This function only talks to
// headscale.
func (c *Client) ExpirePreauthKey(userID int64, keyID string) error {
	if keyID == "" {
		return fmt.Errorf("empty key id")
	}
	// userID is still part of the signature (callers know the owner and the
	// older PUT route was documented with it), but neither the 0.29.x route nor
	// the CLI's `expire` verb takes it.
	_ = userID
	// 1. The route headscale 0.29.x actually serves.
	apiErr := c.do("POST", "/api/v1/preauthkey/expire", map[string]any{"id": keyID}, nil)
	if apiErr == nil {
		return nil
	}
	// 2. The older per-id spelling, kept as a rung for hosts on an older API.
	if err := c.do("PUT", "/api/v1/preauthkey/"+keyID+"/expire", nil, nil); err == nil {
		return nil
	}
	// 3. CLI fallback (B304: install-kind aware — docker exec when docker
	// exists, the local `headscale` binary on a native install).
	if c.ExecContainer == "" && !cliAvailable() {
		return fmt.Errorf("api: %v; no ExecContainer for CLI fallback", apiErr)
	}
	args := []string{"preauthkeys", "expire", "--id", keyID, "--force"}
	out, err := c.runHeadscaleCLI(args...)
	if err != nil {
		return fmt.Errorf("api: %v; cli: %v (%s)", apiErr, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cliAvailable reports whether a CLI rung can be attempted at all: a container
// name was configured, or a `headscale` binary is on PATH (a native install).
// Used only to keep the "no rung could even start" case an explicit error
// instead of a confusing exec failure.
func cliAvailable() bool {
	_, err := exec.LookPath("headscale")
	return err == nil
}

// truncateForKeyErr keeps an unparseable API answer readable in an error without
// dumping a whole body into a log line or a page.
func truncateForKeyErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
