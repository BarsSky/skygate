// Headscale tag operations + tag predicate helpers.
//
// headscale 0.29's admin API doesn't expose PUT /api/v1/node/{id}/tag —
// the admin API key lacks the scope. So all tag mutations go through
// `docker exec <container> headscale nodes tag`. The two predicates
// (IsPublicView / IsPrivateView) are used everywhere in handlers
// to decide ACL visibility.
package headscale

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TagPublicTag marks a node as accessible to all users (via ACL).
const TagPublicTag = "tag:public"

// TagPrivateTag marks a node as accessible only to its owner (and admins).
// Replaces tag:public when the admin clicks "Сделать приватной" so the
// headscale tag-owner rules let a tagged node carry this label.
const TagPrivateTag = "tag:private"

// TagNode sets tags on a headscale node via the CLI (the admin API key lacks
// the permission needed for /api/v1/node/{id}/tag).
//
// IMPORTANT: headscale 0.29's `nodes tag` REPLACES the entire tag
// set on a node (no add/remove — see UntagNode for the read-modify-write
// dance we use to remove a single tag). Callers that want to ADD a
// tag without clobbering others must use AddTag (which reads
// the current tag set first and writes the union), or pass
// the full desired tag set to TagNode.
//
// 2026-08-10: switched to c.dockerRunner when non-nil (the same
// injection point ExtendNodeExpiry uses) so unit tests can
// stub the docker exec without touching the system daemon.
// The production path (nil dockerRunner) still uses
// exec.Command("docker", ...).
func (c *Client) TagNode(nodeID int64, tags ...string) error {
	if c.ExecContainer == "" {
		return fmt.Errorf("no ExecContainer configured")
	}
	args := []string{"exec", c.ExecContainer, "headscale", "nodes", "tag",
		"-i", strconv.FormatInt(nodeID, 10), "-t", strings.Join(tags, ","), "--force"}
	var out []byte
	var err error
	if c.dockerRunner != nil {
		out, err = c.dockerRunner(args...)
	} else {
		out, err = exec.Command("docker", args...).CombinedOutput()
	}
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
	if c == nil {
		return fmt.Errorf("ensure-tag-owner: nil client")
	}
	if tag == "" {
		return fmt.Errorf("ensure-tag-owner: empty tag")
	}
	if len(owners) == 0 {
		return fmt.Errorf("ensure-tag-owner: empty owners list for tag %q", tag)
	}
	policy, err := c.GetACL()
	if err != nil {
		return fmt.Errorf("ensure-tag-owner: get ACL: %w", err)
	}
	// Parse the policy HuJSON. The current headscale policy
	// schema is {acls:[...], tagOwners:{...}, ...}; we parse
	// into a generic map so a future headscale schema bump
	// doesn't break us (we only touch tagOwners).
	var p map[string]interface{}
	if err := json.Unmarshal([]byte(policy), &p); err != nil {
		return fmt.Errorf("ensure-tag-owner: parse ACL (got %d bytes): %w", len(policy), err)
	}
	tagOwnersRaw, ok := p["tagOwners"]
	if !ok || tagOwnersRaw == nil {
		tagOwnersRaw = map[string]interface{}{}
		p["tagOwners"] = tagOwnersRaw
	}
	tagOwners, ok := tagOwnersRaw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("ensure-tag-owner: tagOwners is %T, not object", tagOwnersRaw)
	}
	// Idempotent: already-present tag is no-op.
	if existing, ok := tagOwners[tag]; ok {
		if existing != nil {
			// The tag exists with some owners; preserve as-is.
			return nil
		}
	}
	tagOwners[tag] = owners
	// Marshal back. Use 2-space indent for readability (headscale
	// accepts both compact and indented HuJSON).
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("ensure-tag-owner: marshal: %w", err)
	}
	if err := c.SetPolicy(string(out)); err != nil {
		return fmt.Errorf("ensure-tag-owner: set policy (added %q with %d owners): %w", tag, len(owners), err)
	}
	// SetPolicy clears the ACL cache; we don't need a second call.
	return nil
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
