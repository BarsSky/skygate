// Headscale tag operations + tag predicate helpers.
//
// B272: tag mutations try the REST API first (`POST /api/v1/node/{id}/tags`)
// because the CLI path (`docker exec … headscale nodes tag`) cannot work on a
// native/systemd install — live case: every dev-tag silently unapplied with
// `exec: "docker": executable file not found in $PATH`. The CLI remains as a
// fallback through runHeadscaleCLI (docker when present, local binary
// otherwise). The two predicates (IsPublicView / IsPrivateView) are used
// everywhere in handlers to decide ACL visibility.
package headscale

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson" // B251/B245: headscale 0.29 returns ACL as HuJSON
)

// TagPublicTag marks a node as accessible to all users (via ACL).
const TagPublicTag = "tag:public"

// TagPrivateTag marks a node as accessible only to its owner (and admins).
// Replaces tag:public when the admin clicks "Сделать приватной" so the
// headscale tag-owner rules let a tagged node carry this label.
const TagPrivateTag = "tag:private"

// TagNode sets the full tag set on a headscale node.
//
// B272: the REST API is tried FIRST (`POST /api/v1/node/{id}/tags`), because
// the CLI path shells out to `docker exec` and therefore cannot work at all
// on a NATIVE install (verified live: `tag: exec: "docker": executable file
// not found in $PATH` on a systemd host, which left every dev-tag
// unapplied). The CLI remains as a fallback for headscale builds whose admin
// API rejects the endpoint, and now goes through runHeadscaleCLI — docker
// when docker exists, the local binary otherwise, with SKYGATE_HEADSCALE_CLI
// overriding the in-container path (same pattern as B267 for approve-routes).
//
// IMPORTANT: headscale's `nodes tag` REPLACES the entire tag set on a node
// (no add/remove — see UntagNode for the read-modify-write dance we use to
// remove a single tag). Callers that want to ADD a tag without clobbering
// others must use AddTag (which reads the current tag set first and writes
// the union), or pass the full desired tag set to TagNode.
//
// 2026-08-10: switched to c.dockerRunner when non-nil (the same
// injection point ExtendNodeExpiry uses) so unit tests can
// stub the docker exec without touching the system daemon.
func (c *Client) TagNode(nodeID int64, tags ...string) error {
	if len(tags) == 0 {
		return fmt.Errorf("tag: empty tag list for node %d", nodeID)
	}
	// 1. REST first. The admin API key can set node tags in headscale 0.29.
	if err := c.setNodeTagsAPI(nodeID, tags); err == nil {
		return nil
	} else {
		apiErr := err
		// 2. CLI fallback via runHeadscaleCLI (install-kind aware).
		if cliOut, cliErr := c.runHeadscaleCLI("nodes", "tag",
			"-i", strconv.FormatInt(nodeID, 10), "-t", strings.Join(tags, ","), "--force"); cliErr != nil {
			return fmt.Errorf("tag: api: %v; cli: %v (%s)", apiErr, cliErr, strings.TrimSpace(string(cliOut)))
		}
		return nil
	}
}

// setNodeTagsAPI replaces a node's tag set through the headscale REST API
// (B272). Tries the documented endpoint and, if that build exposes a
// different one, the legacy spelling — reporting both attempts on failure.
func (c *Client) setNodeTagsAPI(nodeID int64, tags []string) error {
	payload := map[string]any{"tags": tags}
	attempts := []struct {
		method string
		path   string
	}{
		{"POST", fmt.Sprintf("/api/v1/node/%d/tags", nodeID)},
		{"PUT", fmt.Sprintf("/api/v1/node/%d/tags", nodeID)},
	}
	var lastErr error
	for _, a := range attempts {
		if err := c.do(a.method, a.path, payload, nil); err != nil {
			lastErr = err
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				continue // try the next spelling
			}
			return err
		}
		return nil
	}
	return lastErr
}

// UntagNode removes a tag from a headscale node.
//
// headscale 0.29 has no "nodes untag" subcommand. The tag write REPLACES the
// tag set on a node, so to remove a single tag we rewrite the full tag list,
// leaving every other tag in place. If the result would be empty (e.g. the
// node carried only this single tag) we fall back to TagPrivateTag so
// headscale keeps at least one tag.
//
// B272: routes through TagNode, so the REST API is tried before any CLI.
func (c *Client) UntagNode(nodeID int64, tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("untag: empty tag for node %d", nodeID)
	}
	// B321 (2026-09-25): the read below used to swallow its own error and then fall
	// through with an EMPTY current list, so a failed read rewrote the node's tags to
	// `[tag:private]` — silently wiping every other tag it carried. That is the same
	// class of defect the 2026-08-10 AddTag fix closed in the other direction ("on read
	// error, do NOT call the inner TagNode"), and it stopped being theoretical once
	// B321 started calling UntagNode automatically after a name reclaim. Now: a failed
	// read, a node that is not in the list, and an absent tag each end the call without
	// writing anything.
	nodes, err := c.ListAllNodes()
	if err != nil {
		return fmt.Errorf("untag: read the tags of node %d: %w (nothing written)", nodeID, err)
	}
	var current []string
	found := false
	for _, n := range nodes {
		if n.ID == strconv.FormatInt(nodeID, 10) {
			current = append(current, n.Tags...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("untag: node %d is not in headscale (nothing written)", nodeID)
	}
	filtered := make([]string, 0, len(current))
	present := false
	for _, t := range current {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			present = true
			continue
		}
		filtered = append(filtered, t)
	}
	if !present {
		// Nothing to remove: do NOT rewrite the tag set (an equal rewrite is a write
		// headscale would audit, and a lossy one would be a regression).
		return nil
	}
	if len(filtered) == 0 {
		filtered = []string{TagPrivateTag}
	}
	if err := c.TagNode(nodeID, filtered...); err != nil {
		return fmt.Errorf("untag: %w", err)
	}
	return nil
}

// AddTag adds a single tag to a headscale node WITHOUT removing
// any other tags the node already has. Use this instead of
// TagNode(...) when you want to add tag:private to a node
// that already carries tag:subnet-router (the v0.24.x subnet-router
// flow) or tag:exit-node.
//
// 2026-07-22: v0.26.0 — was missing. The backfill code in
// handlers_node_ownership.go was calling TagNode(..., "tag:private")
// to upgrade a tagless node to tag:private, but headscale's
// `nodes tag --force` REPLACES the entire tag set, so the
// tag:subnet-router that the preauth key had applied got
// silently wiped. End-to-end subnet-router pilot caught it:
// skygate-subnet-admin (id=25) registered with tag:subnet-router,
// sidecar approved 10.0.1.0/24, then the backfill clobbered
// the tag to just [tag:private].
//
// If the node already has `want`, AddTag is a no-op (no
// headscale call). Errors from the inner TagNode call are
// propagated as-is.
//
// 2026-08-10 v0.33.1.35: ListAllNodes errors are now
// propagated instead of silently swallowed. Pre-fix the
// helper proceeded with an empty `current` slice when
// the read failed — that meant the inner TagNode call
// wrote only `[want]`, silently wiping every pre-existing
// tag the node carried. The risk surface was small
// (ListAllNodes has a 5s cache, and headscale's API is
// reliable), but the post-fix B85+ contract for the
// "Tag as exit-node" handler requires "preserve
// existing per-user dev-tags even on the unhappy path".
// The new contract: on read error, AddTag returns
// (read-err) and does NOT call the inner TagNode.
func (c *Client) AddTag(nodeID int64, want string) error {
	if c.ExecContainer == "" {
		return fmt.Errorf("no ExecContainer configured")
	}
	nodes, err := c.ListAllNodes()
	if err != nil {
		return fmt.Errorf("add-tag: read current tags: %w", err)
	}
	current := []string{}
	for _, n := range nodes {
		if n.ID == strconv.FormatInt(nodeID, 10) {
			current = append(current, n.Tags...)
			break
		}
	}
	for _, t := range current {
		if t == want {
			return nil // already tagged
		}
	}
	current = append(current, want)
	return c.TagNode(nodeID, current...)
}

// EnsureTagOwner is the B245 (v1.5.2+) fix for the "tag not in
// tagOwners" chicken-and-egg that the B77 autoupdater used to
// hit on brand-new dev-tags for orphan devices (e.g. cyborg on
// 2026-09-15 — headscale rejected `nodes tag -i 56 -t
// 'tag:dev-skyadmin-cyborg'` with `InvalidArgument: are invalid or
// not permitted` because tagOwners never listed the tag, so the
// autoupdater could never apply it, so tagOwners never grew the
// entry — permanent stuck state).
//
// Semantics:
//   - Idempotent: if `tag` is already in tagOwners with any owners,
//     this is a no-op (the existing entry is preserved — we don't
//     accidentally widen or rewrite it).
//   - If `tag` is missing, the entry is added with the supplied
//     owners and the policy is re-applied via SetPolicy (which
//     handles the database-mode API + file-mode fallback itself).
//   - The cache (cacheACL) is invalidated on success so the next
//     GetACL re-reads.
//
// Callers should pass the full headscale user identifier
// (`<username>@<baseDomain>`), e.g. `skyadmin@tsnet.skynas.ru`.
// Pass `tagged-devices@<baseDomain>` as a second owner when the
// tag should also be auto-applicable by the synthetic sentinel
// (which is the case for `tag:dev-<user>-<device>` dev-tags —
// orphaned devices in the sentinel need the same grants as their
// eventual owning user).
//
// Returns nil if the tag is already in tagOwners, or after
// successfully adding it. Returns an error if the policy update
// failed (e.g. headscale API unreachable); the caller (Backfill)
// decides whether to proceed with AddTag anyway (current behavior:
// fall through to AddTag which will likely also fail, surface via
// the B227 alert sink).
func (c *Client) EnsureTagOwner(tag string, owners []string) error {
	return c.EnsureTagOwners(map[string][]string{tag: owners})
}

// EnsureTagOwners is the B272.7 (v1.5.35) batch form of EnsureTagOwner: it makes
// EVERY tag in `wants` present in TAGOWNERS with ONE read-modify-write of the
// policy.
//
// Why one write matters. EnsureTagOwner is a read-modify-write of the WHOLE
// policy, and the reconciler used to call it once per device. On a `policy.mode:
// file` host the write is handed to the privileged applier — a `.path` unit that
// writes the file and RESTARTS headscale — so the applier runs asynchronously.
// Live on `aro` (2026-09-20, right after the helper was finally installed) three
// devices needed three dev-tags and the first tick permitted exactly ONE:
// `applied=1 failed=2` with `400 requested tags [tag:dev-daniil-laptop] are
// invalid or not permitted`, and `grep -c 'tag:dev-'` in the policy showed 1 for
// three devices. Call N read the policy file BEFORE the applier had written call
// N-1's result, so every write after the first started from the same stale
// snapshot and silently dropped its predecessor's tag. One combined write makes
// the race impossible: there is nothing left to interleave.
//
// Semantics:
//   - Idempotent per tag: a tag that already has any owners is preserved as-is
//     (the operator's entry is never widened or rewritten).
//   - Returns nil (and issues NO write) when every tag is already present.
//   - Validates the whole request BEFORE writing: an empty tag or an empty owner
//     list is an error, and because the write is atomic, one malformed entry
//     cannot take the others down with it.
//   - An empty/nil map is a no-op (nil error) so callers can call it
//     unconditionally.
func (c *Client) EnsureTagOwners(wants map[string][]string) error {
	const op = "ensure-tag-owners"
	if c == nil {
		return fmt.Errorf("%s: nil client", op)
	}
	if len(wants) == 0 {
		return nil
	}
	// Validate in a deterministic order so the reported error is stable.
	tags := make([]string, 0, len(wants))
	for tag := range wants {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		if tag == "" {
			return fmt.Errorf("%s: empty tag", op)
		}
		if len(wants[tag]) == 0 {
			return fmt.Errorf("%s: empty owners list for tag %q", op, tag)
		}
	}

	p, err := c.loadPolicyMap(op)
	if err != nil {
		return err
	}
	tagOwners, err := policyTagOwners(p, op)
	if err != nil {
		return err
	}

	missing := make([]string, 0, len(tags))
	for _, tag := range tags {
		// Idempotent: an already-present tag with owners is preserved as-is.
		if existing, ok := tagOwners[tag]; ok && existing != nil {
			continue
		}
		tagOwners[tag] = wants[tag]
		missing = append(missing, tag)
	}
	if len(missing) == 0 {
		return nil
	}
	// Marshal back. Use 2-space indent for readability (headscale
	// accepts both compact and indented HuJSON).
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: marshal: %w", op, err)
	}
	if err := c.SetPolicy(string(out)); err != nil {
		return fmt.Errorf("%s: set policy (adding %d tag(s): %s): %w", op, len(missing), strings.Join(missing, ", "), err)
	}
	// SetPolicy clears the ACL cache; we don't need a second call.
	return nil
}

// loadPolicyMap fetches the current ACL and decodes it into a generic JSON
// object. `op` only names the caller in the error text.
//
// headscale 0.29 returns the policy in one of two shapes:
//
//	(a) JSON object directly: `{"acls":[...], "tagOwners":{...}, ...}`
//	    — what `headscale policy get` prints and what newer
//	    headscale versions emit on the /api/v1/policy endpoint.
//
//	(b) Stringified JSON: `{"policy": "{...stringified JSON...}"}`
//	    — what the legacy headscale < 0.23 wire format used,
//	    AND what `c.GetACL` falls back to when the API
//	    returns a quoted `Policy` field. The pre-B251 code
//	    passed this stringified blob straight to
//	    `json.Unmarshal(p, &p)` and died with:
//	      `json: cannot unmarshal string into Go value of type map[string]interface {}`
//
// B251 unifies both: we unquote the stringified form (a), then
// hujson.Standardize() tolerates comments + trailing commas in the unwrapped
// policy bytes (b), and only then do we json.Unmarshal into the generic map.
func (c *Client) loadPolicyMap(op string) (map[string]interface{}, error) {
	policy, err := c.GetACL()
	if err != nil {
		return nil, fmt.Errorf("%s: get ACL: %w", op, err)
	}
	policyBytes, unquoteErr := unquotePolicyIfStringified([]byte(policy))
	if unquoteErr != nil {
		return nil, fmt.Errorf("%s: unquote stringified policy (got %d bytes): %w", op, len(policy), unquoteErr)
	}
	policyBytes, hujErr := hujson.Standardize(policyBytes)
	if hujErr != nil {
		return nil, fmt.Errorf("%s: standardize HuJSON (got %d bytes): %w", op, len(policy), hujErr)
	}
	var p map[string]interface{}
	if err := json.Unmarshal(policyBytes, &p); err != nil {
		return nil, fmt.Errorf("%s: parse ACL (got %d bytes): %w", op, len(policy), err)
	}
	return p, nil
}

// policyTagOwners returns the policy's `tagOwners` object, creating an empty one
// in place when the policy has none (a valid policy state — live on `aro` the
// policy carried only tagOwners + autoApprovers and no grants at all).
func policyTagOwners(p map[string]interface{}, op string) (map[string]interface{}, error) {
	tagOwnersRaw, ok := p["tagOwners"]
	if !ok || tagOwnersRaw == nil {
		tagOwnersRaw = map[string]interface{}{}
		p["tagOwners"] = tagOwnersRaw
	}
	tagOwners, ok := tagOwnersRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s: tagOwners is %T, not object", op, tagOwnersRaw)
	}
	return tagOwners, nil
}

// unquotePolicyIfStringified inspects the policy bytes returned
// by GetACL. If they're a JSON string literal (the legacy
// headscale wire format that wrapped the policy in `"…"`), it
// unquotes it. If they're already a JSON object, it returns the
// bytes unchanged. Returns an error if the payload is neither
// a valid JSON string nor a JSON object.
//
// B251: pre-B251 EnsureTagOwner passed stringified bytes straight
// to json.Unmarshal into a map, which crashed with:
//
//	`cannot unmarshal string into Go value of type map[string]interface {}`
//
// on the live skygate VM (skygate-host-1-1 incident, 2026-09-15).
func unquotePolicyIfStringified(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, fmt.Errorf("empty policy bytes")
	}
	// Fast path — already a JSON object/array.
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return raw, nil
	}
	// Slow path — must be a JSON string literal. Decode it.
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return raw, fmt.Errorf("policy is neither object nor string literal: %w", err)
	}
	return []byte(s), nil
}

// IsPublic returns whether an HSNode carries the tag:public tag.
// Case-insensitive to be robust against headscale version drift
// (Tailscale/Android sometimes normalise the tag differently).
func (n HSNode) IsPublic() bool {
	for _, t := range n.Tags {
		if strings.EqualFold(t, TagPublicTag) {
			return true
		}
	}
	return false
}

// IsPublicView is the NodeView-side mirror of IsPublic. Same semantics.
func (n NodeView) IsPublicView() bool {
	for _, t := range n.Tags {
		if strings.EqualFold(t, TagPublicTag) {
			return true
		}
	}
	return false
}

// IsPrivateView reports whether the node carries tag:private. Used
// in the dashboard to decide which nodes the owning user can see.
func (n NodeView) IsPrivateView() bool {
	for _, t := range n.Tags {
		if strings.EqualFold(t, TagPrivateTag) {
			return true
		}
	}
	return false
}
