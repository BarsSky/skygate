package telegram

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"skygate/internal/db"
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
func nodesReply(d *sql.DB) string {
	// 2026-07-12: Этап 10 part 4 — raw SQL replaced by
	// db.ListAllNodeOwners. The helper returns the same
	// (node_id, username, tag) shape we need; presentation grouping
	// stays in the bot.
	owners, err := db.ListAllNodeOwners(d)
	if err != nil {
		return fmt.Sprintf("nodes: db error: %v", err)
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
		byGroup[k] = append(byGroup[k], n.NodeID)
		totals[tag]++
	}
	if len(order) == 0 {
		return "nodes: (no nodes in node_owner_map — run backfill from /admin/devices)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Tailnet nodes: %d total\n", totals["tag:private"]+totals["tag:public"]+totals["tag:exit-node"]+totals["tag:untagged"])
	fmt.Fprintf(&sb, "  private: %d  public: %d  exit-node: %d  untagged: %d\n\n",
		totals["tag:private"], totals["tag:public"], totals["tag:exit-node"], totals["tag:untagged"])
	for _, k := range order {
		fmt.Fprintf(&sb, "[%s] %s (%d)\n", k.tag, k.user, len(byGroup[k]))
		for _, nid := range byGroup[k] {
			fmt.Fprintf(&sb, "  • %s\n", nid)
		}
		sb.WriteString("\n")
	}
	return trimForTelegram(sb.String())
}

// rulesReply shows recent exit-rules with user, exit-node, target and
// action. Mirrors the columns /admin/exit-rules shows, but compact
// (one line per rule). Top 25 by id DESC.
func rulesReply(d *sql.DB) string {
	rows, err := d.Query(`
		SELECT r.id, COALESCE(u.username, '?') AS user, r.exit_node_id,
		       r.target_type, r.target_value, COALESCE(r.action, 'accept') AS action
		  FROM device_rules r
		  LEFT JOIN portal_users u ON u.id = r.user_id
		 ORDER BY r.id DESC
		 LIMIT 25`)
	if err != nil {
		return fmt.Sprintf("rules: db error: %v", err)
	}
	defer rows.Close()

	type rule struct{ id int64; user, exitNode, tType, tValue, action string }
	var rules []rule
	for rows.Next() {
		var rr rule
		if err := rows.Scan(&rr.id, &rr.user, &rr.exitNode, &rr.tType, &rr.tValue, &rr.action); err != nil {
			return fmt.Sprintf("rules: scan error: %v", err)
		}
		rules = append(rules, rr)
	}
	if len(rules) == 0 {
		return "rules: (no exit-rules in DB)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Recent exit-rules (latest 25 of %d shown):\n\n", len(rules))
	for _, rr := range rules {
		fmt.Fprintf(&sb, "#%d %s @%s\n  %s %s → %s\n\n",
			rr.id, rr.user, rr.exitNode, rr.tType, rr.tValue, rr.action)
	}
	return trimForTelegram(sb.String())
}

// auditReply shows the last 20 audit_log entries (admin actions: user
// creation/deletion, password reset, telegram save/disable, ACL
// rollback, etc). Created_at is stored as int64 unix seconds.
func auditReply(d *sql.DB) string {
	rows, err := d.Query(`
		SELECT id, COALESCE(username, '?') AS username, action, detail, created_at
		  FROM audit_log
		 ORDER BY id DESC
		 LIMIT 20`)
	if err != nil {
		return fmt.Sprintf("audit: db error: %v", err)
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
			return fmt.Sprintf("audit: scan error: %v", err)
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return "audit: (no entries in audit_log)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Last 20 audit_log entries:\n\n")
	for _, e := range entries {
		when := time.Unix(e.ts, 0).UTC().Format("2006-01-02 15:04")
		det := e.det
		if len(det) > 80 {
			det = det[:77] + "..."
		}
		fmt.Fprintf(&sb, "#%d %s %s by %s\n  %s\n\n", e.id, when, e.action, e.username, det)
	}
	return trimForTelegram(sb.String())
}

// trimForTelegram keeps the body under 3800 chars so the markdown fence
// RealNotifier wraps around it (```...```) plus headroom stays under
// the 4096-char Telegram limit. The implementation lives in
// personality.go now (single source of truth for the butler
// gatekeeper voice); kept as a thin re-export so the existing
// command files don't need to be touched.
