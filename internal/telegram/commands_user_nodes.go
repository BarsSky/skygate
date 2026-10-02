package telegram

import (
	"fmt"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"strings"
)

func myNodesReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML so the <b>NODE</b>
	// header row in PreLinesRaw() renders.
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.my_nodes.not_bound")
	}
	// 2026-07-12: Этап 10 part 4 — moved to
	// db.ListNodeOwnersByUsername.
	owners, err := db.ListNodeOwnersByUsername(env.DB, env.Username)
	if err != nil {
		return i18n.Tf(lang, "bot.my_nodes.db_error", err)
	}
	// 2026-07-15: Этап 14 v13 — lazy backfill pass. We do hostname
	// + tag in one headscale round-trip (the existing
	// hostnameMapFromHeadscale already calls ListAllNodes; we
	// also need the live tag from the same response).
	if env.userHS() != nil {
		hsView := listAllNodesForBackfill(env.userHS())
		if len(hsView) > 0 {
			hnMap := map[string]string{}
			tagMap := map[string]string{}
			for _, n := range hsView {
				hn := n.GivenName
				if hn == "" {
					hn = n.Hostname
				}
				if hn != "" {
					hnMap[n.ID] = hn
				}
				// B279 (v1.5.46): use the node's own tag, never
				// Tags[0] — a CLASS tag there (tag:exit-node /
				// tag:public / tag:private) would overwrite a real
				// per-node tag in node_owner_map on every read of
				// /my_nodes (the live `aro` revert).
				if tag := db.PickPerNodeTag(n.Tags); tag != "" {
					tagMap[n.ID] = tag
				}
			}
			// 2026-07-15: hostname backfill.
			if db.AnyHostnameEmpty(owners) {
				if n, berr := db.BackfillEmptyHostnames(env.DB, hnMap); berr == nil && n > 0 {
					if refreshed, rerr := db.ListNodeOwnersByUsername(env.DB, env.Username); rerr == nil {
						owners = refreshed
					}
				}
			}
			// 2026-07-15: tag backfill. Closes the v0.10.11
			// regression where admin-tagged devices showed
			// tag:untagged in the bot (PostAdminNodeTag's
			// "tagged-devices" guard skipped the row update).
			if db.AnyTagStale(owners, tagMap) {
				if n, berr := db.SyncTagsFromHeadscale(env.DB, tagMap); berr == nil && n > 0 {
					if refreshed, rerr := db.ListNodeOwnersByUsername(env.DB, env.Username); rerr == nil {
						owners = refreshed
					}
				}
			}
		}
	}
	type row struct{ node, tag, hostname string }
	var nodes []row
	for _, n := range owners {
		tag := n.Tag
		if tag == "" {
			tag = "tag:untagged"
		}
		nodes = append(nodes, row{node: n.NodeID, tag: tag, hostname: n.Hostname})
	}
	if len(nodes) == 0 {
		return i18n.Tf(lang, "bot.my_nodes.empty", env.Username)
	}
	// 2026-07-16: v0.16.x — "more HTML" pass. The device
	// list is tabular data; render it as a <pre> block
	// with a header row + aligned columns. Telegram's
	// <pre> uses a fixed-pitch font so the column
	// widths in the format strings determine the visual
	// alignment.
	//
	// Format:
	//   <pre>
	//   <b>NODE                       TAG</b>
	//   <i>────────────────────────────</i>
	//   alice-laptop               tag:private
	//   alice-phone (alice-2)       tag:private
	//   </pre>
	const (
		colDev = "%-30s"
		colTag = "%s"
	)
	header := fmt.Sprintf(
		"<b>"+colDev+"  "+colTag+"</b>",
		"NODE", "TAG",
	)
	rule := strings.Repeat("─", 30+2+10)
	var lines []string
	lines = append(lines, header, "<i>"+rule+"</i>")
	for _, n := range nodes {
		// 2026-07-14: Этап 14 v10 — show hostname when known,
		// fall back to node_id. Format: "hostname (node_id)"
		// so the user can find their device by either the
		// friendly name or the technical id.
		label := n.node
		if n.hostname != "" {
			label = n.hostname + " (" + n.node + ")"
		}
		// Truncate long labels so the columns align on
		// phones (a 30-char window is enough for any
		// practical device name + node_id).
		if len(label) > 30 {
			label = label[:27] + "..."
		}
		lines = append(lines, fmt.Sprintf(
			colDev+"  "+colTag,
			label, n.tag,
		))
	}
	return i18n.Tf(lang, "bot.my_nodes.header", env.Username, len(nodes)) + "\n\n" +
		PreLinesRaw(lines...)
}

// hostnameMapFromHeadscale calls hs.ListAllNodes and returns
// a node_id → friendly-name map. Shared by /my_nodes, /nodes, and
// the existing /setdefaultdevice / /defaultdevice paths; keeping
// the choice of "GivenName first, fall back to Hostname" in one
// place stops the three sites from drifting.
//
// 2026-07-15: Этап 14 v13 — extracted for the lazy backfill
// helper; previously inlined in three places.
// 2026-08-12 v1.3.9 (P4 catalog cleanup): hostnameMapFromHeadscale
// was used by the old /userlist command which showed
// headscale hostnames. The command was removed when
// the user list was reimplemented against the PG
// node_owner_map (which has fewer round-trips + better
// language support). Staticcheck U1000 (unused). Removed.

// listAllNodesForBackfill wraps the headscale round-trip used by
// the bot's lazy backfill (hostname + tag) so the call site can
// stay readable. nil hs → empty slice. Errors are swallowed
// because the bot still has to render the reply even when
// headscale is briefly unreachable; the next /my_nodes retries.
//
// 2026-07-15: Этап 14 v13 — extracted from myNodesReply so the
// same call also powers adminNodesReply's lazy tag sync.
