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
	// 2026-07-16: v0.16.2 — mark HTML so the <b>ID DATE
	// ACTION BY</b> header row + the <i>──────</i>
	// separator in PreLinesRaw() render.
	// 2026-07-16: v0.16.5 — split into 2 bubbles if more
	// than 10 entries. The audit log can list 20 entries
	// (the LIMIT 20 in the query), which on a phone screen
	// scrolls past the fold; the operator reported that
	// /help and other long replies are hard to scan at
	// default font size. Splitting at 10 entries gives
	// 2 focused bubbles of 10 each.
	markHTMLReply()
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

	// 2026-07-16: v0.16.x — "more HTML" pass. The audit
	// list is tabular data; we render it as a <pre>
	// block with column-aligned headers. Telegram's
	// <pre> uses a fixed-pitch font on all clients, so
	// the column widths in the format strings
	// determine the visual alignment.
	//
	// Format:
	//   <pre>
	//   ID    DATE (UTC)        ACTION            BY
	//   ────────────────────────────────────────────
	//   #1234 2026-07-16 13:45  token_create      alice
	//   #1233 2026-07-16 13:42  user_create      skyadmin
	//   </pre>
	//
	// Detail (long strings) goes below the row, indented.
	// The header row is a separate <b>line so it doesn't
	// get lost in the monospace block.
	const (
		colID    = "#%-6d"
		colWhen  = "%-16s"
		colAct   = "%-18s"
		colBy    = "%s"
	)
	header := fmt.Sprintf(
		"<b>"+colID+"  "+colWhen+"  "+colAct+"  "+colBy+"</b>",
		0, "DATE (UTC)", "ACTION", "BY",
	)
	rule := strings.Repeat("─", 6+2+16+2+18+2+12)
	var lines []string
	lines = append(lines, header, "<i>"+rule+"</i>")
	for _, e := range entries {
		when := time.Unix(e.ts, 0).UTC().Format("2006-01-02 15:04")
		who := e.username
		if who == "" || who == "?" {
			who = "—"
		}
		lines = append(lines, fmt.Sprintf(
			colID+"  "+colWhen+"  "+colAct+"  "+colBy,
			e.id, when, e.action, who,
		))
		// Detail: long strings go below, truncated to
		// 60 chars (Telegram <pre> is 4096-wide; 60
		// keeps it under 80 on a phone with the
		// monospace font).
		if det := e.det; det != "" {
			if len(det) > 60 {
				det = det[:57] + "..."
			}
			lines = append(lines, "    "+det)
		}
		lines = append(lines, "") // blank row separator
	}
	// 2026-07-16: v0.16.5 — split into 2 bubbles if
	// more than auditSplitThreshold entries. The first
	// bubble gets the title + section header + first
	// half; the second gets the rest. Threshold is
	// 10 because: (a) 20 entries in one bubble is hard
	// to scan on a phone, (b) 10 entries fits in
	// ~30 lines of <pre> which is comfortable in one
	// screen even at default font size, (c) under 10
	// entries the reply is short enough that splitting
	// would be visual noise (two short bubbles for a
	// 5-entry log feels gratuitous).
	const auditSplitThreshold = 10
	body := i18n.T(lang, "bot.audit.header") + "\n\n" +
		Section(i18n.T(lang, "bot.audit.section_recent")) + "\n" +
		PreLinesRaw(lines...)
	if len(entries) <= auditSplitThreshold {
		return body
	}
	// Split the rendered lines at the threshold. We
	// split the <pre> by finding the entry boundary
	// (a blank line + a row line). Easier: re-render
	// the two halves.
	half := len(entries) / 2
	firstLines := lines[:entryBoundaryIndex(lines, half)]
	secondLines := lines[entryBoundaryIndex(lines, half):]
	firstBody := i18n.T(lang, "bot.audit.header") + "\n\n" +
		Section(i18n.T(lang, "bot.audit.section_recent")) + "\n" +
		PreLinesRaw(firstLines...) + "\n\n" +
		"<i>(" + i18n.Tf(lang, "bot.audit.split_more", len(entries)-half) + ")</i>"
	secondBody := PreLinesRaw(secondLines...)
	return firstBody + splitMessageMarker + secondBody
}

// entryBoundaryIndex returns the line index at which to
// split the audit <pre> block so the second half starts
// at a row boundary (not in the middle of a row's
// detail line). We split at the first blank line that
// follows the (splitAt)th entry's row, so the bubble
// boundary lands between entries.
//
// 2026-07-16: v0.16.5.
func entryBoundaryIndex(lines []string, splitAt int) int {
	// Each entry occupies 2 lines (row + detail) + 1
	// blank line separator. Walk the slice counting
	// rows and stop at the first blank line AFTER the
	// (splitAt)th row.
	rows := 0
	for i, l := range lines {
		// Header row + rule line are at the top; skip.
		if i < 2 {
			continue
		}
		// A blank line marks the end of an entry.
		if l == "" {
			rows++
			if rows >= splitAt {
				return i + 1
			}
		}
	}
	return len(lines)
}
