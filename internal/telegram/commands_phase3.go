package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// Phase 3 commands: /exit_nodes, /quota, /ack <id>.
//
// /exit_nodes is a focused slice of /nodes — only the nodes that are
// actually configured as exit-nodes (tag=tag:exit-node in
// node_owner_map). The operator cares about these specifically
// because they're the high-value infra: if emilia goes down, the
// whole tailnet loses a path out.
//
// /quota is "who is close to the per-user rule limit". It joins
// device_rules to portal_users to count rules per user, and looks
// the per-user cap up in the BotEnv so admin can spot a user who is
// about to hit the wall (e.g. skyadmin:2000 in SKYGATE_USER_MAX_RULES).
//
// /ack <id> marks a previously-sent alert as acknowledged. The id
// is the [#N] prefix on every alert message. We flip acked_at/acked_by
// in telegram_alerts and mirror the action into audit_log so the
// dashboard shows it too. Re-acking an already-acked id is a no-op
// (UPDATE only matches acked_at=0).

// exitNodesReply lists the nodes currently tagged as exit-nodes.
// Output is grouped by user, with last_seen (from devices table)
// when available — exit-nodes without a last_seen are likely offline.
func exitNodesReply(env BotEnv) string {
	lang := env.Lang
	d := env.DB
	// 2026-07-12: Этап 10 part 4 — node_owner_map rows for exit-nodes
	// come from db.ListExitNodeOwners. The devices-table LEFT JOIN for
	// last_seen/online stays in the bot (presentation concern), so we
	// build a small id→{last_seen,online} map and merge.
	owners, err := db.ListExitNodeOwners(d)
	if err != nil {
		return i18n.Tf(lang, "bot.exit_nodes.db_error", err)
	}
	type devState struct {
		lastSeen int64
		online   int
	}
	devMap := map[string]devState{}
	if rows, derr := d.Query(`SELECT node_id, COALESCE(last_seen, 0), COALESCE(online, 0) FROM devices`); derr == nil {
		for rows.Next() {
			var nid string
			var st devState
			if err := rows.Scan(&nid, &st.lastSeen, &st.online); err == nil {
				devMap[nid] = st
			}
		}
		rows.Close()
	}
	type row struct {
		user, nodeID string
		lastSeen     int64
		online       int
	}
	var byUser []row
	for _, n := range owners {
		user := n.Username
		if user == "" {
			user = "?"
		}
		st := devMap[n.NodeID]
		byUser = append(byUser, row{user: user, nodeID: n.NodeID, lastSeen: st.lastSeen, online: st.online})
	}
	if len(byUser) == 0 {
		return i18n.T(lang, "bot.exit_nodes.empty")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.exit_nodes.header", len(byUser)))
	for _, r := range byUser {
		status := "offline"
		if r.online == 1 {
			status = "online"
		}
		var seen string
		if r.lastSeen > 0 {
			seen = fmt.Sprintf(", last_seen %s", unixToShort(r.lastSeen))
		}
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.exit_nodes.row", r.nodeID, r.user, status, seen))
	}
	return trimForTelegram(sb.String())
}

// quotaReply shows rule counts per user alongside their per-user
// limit (from BotEnv.UserMaxRules / DefaultMax). Operators scan
// this to spot users close to their cap before they hit the wall.
func quotaReply(env BotEnv) string {
	lang := env.Lang
	d := env.DB
	rows, err := d.Query(`
		SELECT u.id, u.username, COUNT(r.id) AS cnt
		  FROM portal_users u
		  LEFT JOIN device_rules r ON r.user_id = u.id
		 GROUP BY u.id, u.username
		 ORDER BY cnt DESC, u.username`)
	if err != nil {
		return i18n.Tf(lang, "bot.quota.db_error", err)
	}
	defer rows.Close()

	type row struct {
		id       int64
		username string
		cnt, max int
	}
	var users []row
	var total, totalMax int
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.username, &r.cnt); err != nil {
			return i18n.Tf(lang, "bot.quota.scan_error", err)
		}
		r.max = env.MaxFor(r.username)
		users = append(users, r)
		total += r.cnt
		totalMax += r.max
	}
	if len(users) == 0 {
		return i18n.T(lang, "bot.quota.empty")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", i18n.T(lang, "bot.quota.header"))
	for _, r := range users {
		pct := 0
		if r.max > 0 {
			pct = (r.cnt * 100) / r.max
		} else {
			pct = -1 // -1 = no limit configured
		}
		bar := quotaBar(pct)
		maxStr := fmt.Sprintf("%d", r.max)
		if r.max == 0 {
			maxStr = "∞"
		}
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.quota.row", r.username, r.cnt, maxStr, bar, safePct(pct)))
	}
	if totalMax > 0 {
		fmt.Fprintf(&sb, "%s", i18n.Tf(lang, "bot.quota.total", total))
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.quota.total_with_cap", totalMax))
	} else {
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.quota.total", total))
	}
	return trimForTelegram(sb.String())
}

// ackReply handles "/ack <id>". It marks the matching row in
// telegram_alerts as acked (idempotent: re-acking a row whose
// acked_at is already set returns a friendly "already acked"
// message) and mirrors the action into audit_log so the dashboard
// reflects the operator's response.
func ackReply(env BotEnv, arg string) string {
	lang := env.Lang
	d := env.DB
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return i18n.T(lang, "bot.ack.usage")
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return i18n.Tf(lang, "bot.ack.invalid_id", arg)
	}
	// 1. Look up the row first so we can echo the body.
	var body string
	if err := d.QueryRow(`SELECT body FROM telegram_alerts WHERE id = $1`, id).Scan(&body); err != nil {
		return i18n.Tf(lang, "bot.ack.not_found", id)
	}
	// 2. Idempotent UPDATE — only flips rows that are still open.
	res, err := d.Exec(`UPDATE telegram_alerts
	                       SET acked_at = strftime('%s','now'),
	                           acked_by = 'telegram'
	                     WHERE id = $1 AND acked_at = 0`, id)
	if err != nil {
		return i18n.Tf(lang, "bot.ack.db_error", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Row exists but acked_at > 0 — already acked before.
		return i18n.Tf(lang, "bot.ack.already", formatAlertRow(id, body))
	}
	// 3. Mirror into audit_log so /admin/audit shows the operator's
	// response, not just the alert itself.
	detail := fmt.Sprintf("alert_id=%d", id)
	// 2026-07-11: Этап 9 part 2 — moved to db.AppendAuditLogNoUser
	if err := db.AppendAuditLogNoUser(d, "telegram", "telegram_ack", detail); err != nil {
		// audit_log failure isn't fatal — the ack itself succeeded.
		// We log but don't surface the error to keep the operator's
		// flow uninterrupted.
		ackAuditLogErr = err
	}
	return i18n.Tf(lang, "bot.ack.done", formatAlertRow(id, body))
}

// ackAuditLogErr is set when the audit_log write inside ackReply
// failed. The variable is exported via the package so a test
// (or future health check) can inspect it; ackReply itself doesn't
// return the error so the operator's Telegram reply stays clean.
var ackAuditLogErr error

// quotaBar renders a 10-char progress bar. pct<0 means "no limit".
func quotaBar(pct int) string {
	if pct < 0 {
		return "[no limit]"
	}
	if pct > 100 {
		pct = 100
	}
	filled := (pct + 5) / 10 // round to nearest 10%
	if filled > 10 {
		filled = 10
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", 10-filled) + "]"
}

func safePct(pct int) int {
	if pct < 0 {
		return 0
	}
	return pct
}

// unixToShort formats a unix-second timestamp as YYYY-MM-DD HH:MMZ.
// Output is compact enough to keep /exit_nodes one line per node.
func unixToShort(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04Z")
}
