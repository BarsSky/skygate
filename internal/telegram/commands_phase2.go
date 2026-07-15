package telegram

import (
	"fmt"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// Phase 2 commands: /nodes, /rules, /audit.
//
// All three are read-only DB queries — they read the same tables the
// admin UI reads, so format parity with /admin/devices, /my/exit-rules
// and /admin/audit is intentional.
//
// Reply budget: Telegram caps messages at 4096 chars; we trim to 3800
// to leave headroom for the markdown fence that RealNotifier wraps
// around every reply.

// nodesReply lists tailnet nodes grouped by tag.
//
// We read from node_owner_map (the snapshot the portal maintains) and
// fall back to "(unknown)" for nodes that have no portal mapping —
// this happens for nodes that joined before node_owner_map existed or
// for nodes the backfill hasn't visited yet.
func nodesReply(env BotEnv) string {
	lang := env.Lang
	d := env.DB
	// 2026-07-12: Этап 10 part 4 — raw SQL replaced by
	// db.ListAllNodeOwners. The helper returns the same
	// (node_id, username, tag) shape we need; presentation grouping
	// stays in the bot.
	owners, err := db.ListAllNodeOwners(d)
	if err != nil {
		return i18n.Tf(lang, "bot.nodes.db_error", err)
	}
	// 2026-07-15: Этап 14 v13 — lazy backfill (hostname + tag).
	// Same pattern as myNodesReply: one headscale round-trip feeds
	// both backfills. See db.SyncTagsFromHeadscale for why the
	// tag update closes the v0.10.11 regression (PostAdminNodeTag
	//'s "tagged-devices" guard skipped the row update for
	// admin-tagged devices, so the bot's view drifted from the
	// headscale truth).
	if env.HS != nil {
		hsView := listAllNodesForBackfill(env.HS)
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
				if len(n.Tags) > 0 {
					tagMap[n.ID] = n.Tags[0]
				}
			}
			if db.AnyHostnameEmpty(owners) {
				if n, berr := db.BackfillEmptyHostnames(d, hnMap); berr == nil && n > 0 {
					if refreshed, rerr := db.ListAllNodeOwners(d); rerr == nil {
						owners = refreshed
					}
				}
			}
			if db.AnyTagStale(owners, tagMap) {
				if n, berr := db.SyncTagsFromHeadscale(d, tagMap); berr == nil && n > 0 {
					if refreshed, rerr := db.ListAllNodeOwners(d); rerr == nil {
						owners = refreshed
					}
				}
			}
		}
	}
	type key struct{ user, tag string }
	byGroup := map[key][]string{}
	order := []key{}
	totals := map[string]int{"tag:private": 0, "tag:public": 0, "tag:exit-node": 0, "tag:untagged": 0}
	for _, n := range owners {
		tag := n.Tag
		if tag == "" {
			tag = "tag:untagged"
		}
		user := n.Username
		if user == "" {
			user = "?"
		}
		k := key{user, tag}
		if _, ok := byGroup[k]; !ok {
			order = append(order, k)
		}
		// 2026-07-14: Этап 14 v10 — show "hostname (node_id)" so
		// the admin can identify the device by its friendly
		// name. Falls back to node_id when hostname is empty
		// (backfill hasn't visited this node yet).
		label := n.NodeID
		if n.Hostname != "" {
			label = n.Hostname + " (" + n.NodeID + ")"
		}
		byGroup[k] = append(byGroup[k], label)
		totals[tag]++
	}
	if len(order) == 0 {
		return i18n.T(lang, "bot.nodes.empty")
	}
	var sb strings.Builder
	total := totals["tag:private"] + totals["tag:public"] + totals["tag:exit-node"] + totals["tag:untagged"]
	fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.nodes.header_total", total))
	fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.nodes.tag_breakdown",
		totals["tag:private"], totals["tag:public"], totals["tag:exit-node"], totals["tag:untagged"]))
	for _, k := range order {
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.nodes.group_header", k.tag, k.user, len(byGroup[k])))
		for _, label := range byGroup[k] {
			fmt.Fprintf(&sb, "  • %s\n", label)
		}
		sb.WriteString("\n")
	}
	return trimForTelegram(sb.String())
}

// rulesReply shows recent exit-rules with user, exit-node, target and
// action. Mirrors the columns /admin/exit-rules shows, but compact
// (one line per rule). Top 25 by id DESC.
func rulesReply(env BotEnv) string {
	lang := env.Lang
	d := env.DB
	rows, err := d.Query(`
		SELECT r.id, COALESCE(u.username, '?') AS user, r.exit_node_id,
		       r.target_type, r.target_value, COALESCE(r.action, 'accept') AS action
		  FROM device_rules r
		  LEFT JOIN portal_users u ON u.id = r.user_id
		 ORDER BY r.id DESC
		 LIMIT 25`)
	if err != nil {
		return i18n.Tf(lang, "bot.rules.db_error", err)
	}
	defer rows.Close()

	type rule struct{ id int64; user, exitNode, tType, tValue, action string }
	var rules []rule
	for rows.Next() {
		var rr rule
		if err := rows.Scan(&rr.id, &rr.user, &rr.exitNode, &rr.tType, &rr.tValue, &rr.action); err != nil {
			return i18n.Tf(lang, "bot.rules.scan_error", err)
		}
		rules = append(rules, rr)
	}
	if len(rules) == 0 {
		return i18n.T(lang, "bot.rules.empty")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.rules.header", len(rules)))
	for _, rr := range rules {
		fmt.Fprintf(&sb, "%s\n\n",
			i18n.Tf(lang, "bot.rules.row", rr.id, rr.user, rr.exitNode, rr.tType, rr.tValue, rr.action))
	}
	return trimForTelegram(sb.String())
}

// auditReply shows the last 20 audit_log entries (admin actions: user
// creation/deletion, password reset, telegram save/disable, ACL
// rollback, etc). Created_at is stored as int64 unix seconds.
func auditReply(env BotEnv) string {
	lang := env.Lang
	rows, err := env.DB.Query(`
		SELECT id, COALESCE(username, '?') AS username, action, detail, created_at
		  FROM audit_log
		 ORDER BY id DESC
		 LIMIT 20`)
	if err != nil {
		return i18n.Tf(lang, "bot.audit.db_error", err)
	}
	defer rows.Close()

	type entry struct {
		id                   int64
		username, action, det string
		ts                   int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.username, &e.action, &e.det, &e.ts); err != nil {
			return i18n.Tf(lang, "bot.audit.scan_error", err)
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return i18n.T(lang, "bot.audit.empty")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", i18n.T(lang, "bot.audit.header"))
	for _, e := range entries {
		when := time.Unix(e.ts, 0).UTC().Format("2006-01-02 15:04")
		det := e.det
		if len(det) > 80 {
			det = det[:77] + "..."
		}
		fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.audit.row", e.id, when, e.action, e.username, det))
	}
	return trimForTelegram(sb.String())
}
