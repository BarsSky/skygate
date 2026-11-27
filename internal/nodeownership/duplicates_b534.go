// duplicates_b534.go — B534 (2026-11-27)
//
// THE REPORTED SYMPTOM. During an OIDC login a device appeared in the tailnet as
// `skybars-1` although the device is `skybars`, and the operator asked the
// question this file answers: "не должно проходить дублирование — скорей всего
// причина в том что был сброшен кэш приложения, от чего могло поменяться ID
// устройства, и тут бы вариант как это отслеживать для skygate".
//
// WHY IT HAPPENS (and why "prevent it" is not the fix). A node's identity in
// headscale is its MACHINE KEY. Clearing the Tailscale app's state — or logging in
// again under another account — destroys that key, so the client registers as a
// brand-new node. headscale must keep given names unique, so the newcomer becomes
// `<name>-1`, `-2`, … Skygate cannot stop that: it never sees the old key again
// and has no hardware identifier to compare. What skygate CAN do — and did not,
// before this block — is NOTICE it and say what it costs:
//
//	live, 2026-11-27:  id=55  skybars    daniil     tag:dev-daniil-skybars     offline since 12:16
//	                   id=198 skybars-1  skyadmin   tag:dev-skyadmin-skybars-1 online, created 14:41
//
// The cost is not cosmetic. Every per-device ACL row, exit-node pin and exit rule
// is keyed by HOSTNAME (node_owner_map + the `tag:dev-<user>-<host>` tag), so the
// rules that were written for `skybars` now point at a node that will never come
// back while the live device — `skybars-1` — matches none of them. Nothing in the
// product said so; the operator found it by sight.
//
// WHAT THIS FILE DOES. `DetectDuplicates` compares the live node list with itself:
// a name ending in `-<n>` whose base name exists is a candidate, and the reported
// KIND says which evidence supports it, so the page never claims more than it
// measured:
//
//	same_host_stale      the two nodes report the SAME hostname and the older one
//	                     is offline — this is a re-registration of one physical
//	                     device (the live case above)
//	same_host_live       both are online: two live nodes claiming one device, which
//	                     means traffic can land on either
//	different_host       the base name matches but the hostnames differ — a name
//	                     collision, worth showing, weaker than the two above
//
// It is a REPORT, never an action: the operator decides which node is the device
// and whether to delete the stale one (the per-row delete already exists). A
// product that silently deleted nodes because their names looked similar would be
// one restart away from deleting a colleague's laptop.
package nodeownership

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/headscale"
)

// DuplicateKind is the evidence behind a duplicate report.
type DuplicateKind string

const (
	// DuplicateSameHostStale — same hostname, the older node is offline. This is
	// a client that re-registered (app data cleared, account changed, node key
	// lost), i.e. one physical device behind two nodes.
	DuplicateSameHostStale DuplicateKind = "same_host_stale"
	// DuplicateSameHostLive — same hostname, BOTH online: two live nodes exist
	// for one device name.
	DuplicateSameHostLive DuplicateKind = "same_host_live"
	// DuplicateDifferentHost — the base name matches, the hostnames differ.
	DuplicateDifferentHost DuplicateKind = "different_host"
)

// NodeDuplicate is one reported pair. The NEW node is the suffixed one (the one
// that appeared later and is usually the live device); the OLD node is the base
// name the product's rules were written against.
type NodeDuplicate struct {
	Kind DuplicateKind

	NewID       string
	NewGiven    string
	NewHost     string
	NewOwner    string
	NewTag      string
	NewOnline   bool
	NewCreated  string
	NewLastSeen string

	OldID       string
	OldGiven    string
	OldHost     string
	OldOwner    string
	OldTag      string
	OldOnline   bool
	OldCreated  string
	OldLastSeen string
}

// Reason is the operator-facing sentence used in the log, the audit row and the
// alert. It names both nodes by ID and given name so the operator can act without
// another query.
func (d NodeDuplicate) Reason() string {
	oldState := "offline"
	if d.OldOnline {
		oldState = "online"
	}
	newState := "offline"
	if d.NewOnline {
		newState = "online"
	}
	switch d.Kind {
	case DuplicateSameHostLive:
		return fmt.Sprintf("duplicate device: %q (id=%s, %s/%s, %s) and %q (id=%s, %s/%s, %s) report the SAME hostname %q and are BOTH online — traffic can land on either node, and the ACL rows keyed by one name do not cover the other",
			d.NewGiven, d.NewID, strings.TrimSpace(d.NewOwner), d.NewTag, newState,
			d.OldGiven, d.OldID, strings.TrimSpace(d.OldOwner), d.OldTag, oldState,
			firstNonEmpty(d.NewHost, d.OldHost))
	case DuplicateDifferentHost:
		return fmt.Sprintf("duplicate name: %q (id=%s, %s/%s) shares the base name of %q (id=%s, %s/%s) but reports a different hostname (%q vs %q) — the suffixed node usually matches none of the rules written for %q",
			d.NewGiven, d.NewID, strings.TrimSpace(d.NewOwner), d.NewTag,
			d.OldGiven, d.OldID, strings.TrimSpace(d.OldOwner), d.OldTag,
			d.NewHost, d.OldHost, d.OldGiven)
	default:
		return fmt.Sprintf("duplicate device: %q (id=%s, %s/%s, %s, created %s) looks like a RE-REGISTRATION of %q (id=%s, %s/%s, %s) — same hostname %q, and the old node has not been seen since %s. Rules, exit pins and tags keyed by %q will never match the live node",
			d.NewGiven, d.NewID, strings.TrimSpace(d.NewOwner), d.NewTag, newState, firstNonEmpty(d.NewCreated, "?"),
			d.OldGiven, d.OldID, strings.TrimSpace(d.OldOwner), d.OldTag, oldState,
			firstNonEmpty(d.NewHost, d.OldHost), firstNonEmpty(d.OldLastSeen, "it went offline"),
			d.OldGiven)
	}
}

// AlertError is what the tag alert sink records (audit + Telegram + metric). The
// text IS the alert, so it carries the whole sentence.
func (d NodeDuplicate) AlertError() error { return fmt.Errorf("%s", d.Reason()) }

// DetectDuplicates reports every node whose given name is `<other>-<n>` where
// `<other>` is also a node's given name. Pure: the caller passes the node list it
// already fetched, so the page, the tick and the tests agree on one rule.
//
// Nodes whose given name has no suffix, empty names, and a base that no node
// carries are ignored — a name like `work-2` with no `work` node is not a
// duplicate, it is somebody's naming.
func DetectDuplicates(nodes []headscale.NodeView) []NodeDuplicate {
	byGiven := make(map[string]headscale.NodeView, len(nodes))
	for _, n := range nodes {
		name := strings.TrimSpace(n.GivenName)
		if name == "" {
			continue
		}
		byGiven[name] = n
	}
	var out []NodeDuplicate
	for _, n := range nodes {
		given := strings.TrimSpace(n.GivenName)
		base, _, ok := duplicateBase(given)
		if !ok {
			continue
		}
		old, exists := byGiven[base]
		if !exists {
			continue
		}
		if strings.TrimSpace(old.ID) == strings.TrimSpace(n.ID) {
			continue
		}
		sameHost := sameHostname(old.Hostname, n.Hostname)
		kind := DuplicateDifferentHost
		switch {
		case sameHost && n.Online && old.Online:
			kind = DuplicateSameHostLive
		case sameHost:
			kind = DuplicateSameHostStale
		}
		out = append(out, NodeDuplicate{
			Kind: kind,

			NewID: n.ID, NewGiven: given, NewHost: strings.TrimSpace(n.Hostname),
			NewOwner: n.UserName, NewTag: devTagOf(n), NewOnline: n.Online,
			NewCreated: n.CreatedAt, NewLastSeen: n.LastSeen,

			OldID: old.ID, OldGiven: strings.TrimSpace(old.GivenName), OldHost: strings.TrimSpace(old.Hostname),
			OldOwner: old.UserName, OldTag: devTagOf(old), OldOnline: old.Online,
			OldCreated: old.CreatedAt, OldLastSeen: old.LastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NewGiven != out[j].NewGiven {
			return out[i].NewGiven < out[j].NewGiven
		}
		return out[i].NewID < out[j].NewID
	})
	return out
}

// duplicateBase splits `<name>-<n>` (n >= 1) into its base. A trailing `-1` on a
// name that itself ends in a number is still a suffix: headscale appends its
// counter to whatever the client reported.
func duplicateBase(given string) (base string, n int, ok bool) {
	i := strings.LastIndexByte(given, '-')
	if i <= 0 || i == len(given)-1 {
		return "", 0, false
	}
	v, err := strconv.Atoi(given[i+1:])
	if err != nil || v < 1 {
		return "", 0, false
	}
	base = given[:i]
	if strings.TrimSpace(base) == "" {
		return "", 0, false
	}
	return base, v, true
}

// sameHostname compares the hostname the CLIENT reported (NodeView.Hostname),
// not the given name headscale assigned. An empty hostname is not evidence of
// sameness.
func sameHostname(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

// devTagOf prefers the canonical dev-tag the node actually carries, so the report
// names the tag the operator will search for.
func devTagOf(n headscale.NodeView) string {
	for _, t := range n.Tags {
		if strings.HasPrefix(t, "tag:dev-") {
			return t
		}
	}
	if len(n.Tags) > 0 {
		return n.Tags[0]
	}
	return "(none)"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
