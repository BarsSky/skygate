package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/localtime"
)

// Phase 3 commands: /exit_nodes, /quota, /ack <id>.
//
// /exit_nodes is a focused slice of /nodes — only the nodes that are
// actually configured as exit-nodes (tag=tag:exit-node in
// node_owner_map). The operator cares about these specifically
// because they're the high-value infra: if relay-1 goes down, the
// whole tailnet loses a path out.
//
// /quota is "who is close to the per-user rule limit". It joins
// device_rules to portal_users to count rules per user, and looks
// the per-user cap up in the BotEnv so admin can spot a user who is
// about to hit the wall (e.g. admin:2000 in SKYGATE_USER_MAX_RULES).
//
// /ack <id> marks a previously-sent alert as acknowledged. The id
// is the [#N] prefix on every alert message. We flip acked_at/acked_by
// in telegram_alerts and mirror the action into audit_log so the
// dashboard shows it too. Re-acking an already-acked id is a no-op
// (UPDATE only matches acked_at=0).

// exitNodesReply lists the nodes currently tagged as exit-nodes.
// Output is grouped by user, with the health monitor's state and
// last_seen for each node.
//
// B390 (TD-33 H1): the status comes from `exit_node_health` (the
// snapshot the monitor refreshes; the same source /admin/exit-nodes
// renders). It used to come from `SELECT … FROM devices`, a table with
// no writer anywhere in the repository, so the query always returned
// zero rows and EVERY node was labelled "offline" — including a healthy
// relay that clients were routing through at that moment. A node the
// snapshot does not describe is now reported as "no data", which is the
// only true thing to say about it.
func exitNodesReply(env BotEnv) string {
	lang := env.Lang
	d := env.DB
	owners, err := db.ListExitNodeOwners(d)
	if err != nil {
		return i18n.Tf(lang, "bot.exit_nodes.db_error", err)
	}
	// A failure here is a fact about the READ; it is reported as such and
	// never translated into a per-node status (B390 — the old code could
	// not tell "no rows" from "not measured", and answered "offline").
	health, herr := exitNodeHealthIndex(d)
	healthNote := ""
	if herr != nil {
		healthNote = "\n" + i18n.Tf(lang, "bot.exit_nodes.health_unavailable", herr)
		health = map[string]db.ExitNodeHealth{}
	}
	type row struct {
		user, nodeID string
		status       string
		lastSeen     int64
	}
	var byUser []row
	for _, n := range owners {
		user := n.Username
		if user == "" {
			user = "?"
		}
		status, h := exitNodeStatusFor(lang, health, n.NodeID)
		var seen int64
		if !h.LastSeenParsed.IsZero() {
			seen = h.LastSeenParsed.Unix()
		}
		byUser = append(byUser, row{user: user, nodeID: n.NodeID, status: status, lastSeen: seen})
	}
	if len(byUser) == 0 {
		return i18n.T(lang, "bot.exit_nodes.empty")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.exit_nodes.header", len(byUser)))
	for _, r := range byUser {
		var seen string
		if r.lastSeen > 0 {
			seen = i18n.Tf(lang, "bot.exit_nodes.last_seen", unixToShort(r.lastSeen))
		}
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.exit_nodes.row", r.nodeID, r.user, r.status, seen))
	}
	if healthNote != "" {
		sb.WriteString(healthNote)
	}
	return trimForTelegram(env.Lang, sb.String())
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
		bar := quotaBar(lang, pct)
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
	return trimForTelegram(env.Lang, sb.String())
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
	// 2026-09-16 (B253 fix): PG-native $1 placeholder +
	// `EXTRACT(EPOCH FROM now())::bigint` (was SQLite
	// `strftime('%s','now')` which doesn't parse on PG).
	var body string
	if err := d.QueryRow(`SELECT body FROM telegram_alerts WHERE id = $1`, id).Scan(&body); err != nil {
		return i18n.Tf(lang, "bot.ack.not_found", id)
	}
	// 2. Idempotent UPDATE — only flips rows that are still open.
	// 2026-09-18: the timestamp fragment goes through db.NowUnixSQL()
	// (PG: EXTRACT(EPOCH ...), SQLite: strftime('%s','now')). The B253
	// fix hardcoded the PG form to get PG working; routing it through
	// the dialect helper keeps PG working AND stops the statement from
	// being a syntax error on a SQLite deployment.
	res, err := d.Exec(`UPDATE telegram_alerts
	                       SET acked_at = `+db.NowUnixSQL()+`,
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
	// 2026-07-11: Этап 9 part 2 — moved to db.AppendAuditLogNoUser.
	// audit_log failure isn't fatal — the ack itself succeeded.
	// We log but don't surface the error to keep the operator's
	// flow uninterrupted.
	_ = db.AppendAuditLogNoUser(d, "telegram", "telegram_ack", detail)
	return i18n.Tf(lang, "bot.ack.done", formatAlertRow(id, body))
}

// quotaBar renders a 10-char progress bar. pct<0 means "no limit".
// B389: the "no limit" label was hardcoded English in both languages.
func quotaBar(lang string, pct int) string {
	if pct < 0 {
		return i18n.T(lang, "bot.quota.no_limit")
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
	return localtime.Format(time.Unix(ts, 0))
}
