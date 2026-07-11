// Headscale tag operations + tag predicate helpers.
//
// headscale 0.29's admin API doesn't expose PUT /api/v1/node/{id}/tag —
// the admin API key lacks the scope. So all tag mutations go through
// `docker exec <container> headscale nodes tag`. The two predicates
// (IsPublicView / IsPrivateView) are used everywhere in handlers
// to decide ACL visibility.
package headscale

import (
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
