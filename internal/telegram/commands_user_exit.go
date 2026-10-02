package telegram

import (
	"fmt"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"strconv"
	"strings"
)

func myExitNodesReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML. The function also
	// sets a pending InlineKeyboard for the per-row
	// "→ hostname" buttons; markHTMLReply() preserves
	// the existing keyboard and just sets ParseMode=HTML
	// on it, so we don't lose the tap-to-set UX.
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.myexitnodes.not_bound")
	}
	servers, err := db.ListExitServers(env.DB)
	if err != nil {
		return i18n.Tf(lang, "bot.myexitnodes.db_error", err)
	}
	// Filter to enabled. Disabled servers stay in the DB for the
	// admin to re-enable; users shouldn't see them in the menu.
	var enabled []db.ExitServer
	for _, s := range servers {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return i18n.T(lang, "bot.myexitnodes.empty")
	}
	// Build a node_id → {last_seen, online} map from the devices
	// table so the reply shows the same health info as admin
	// /exit_nodes. Best-effort: a headscale unreachable won't
	// hide the menu, the row just shows "offline".
	devMap := map[string]struct {
		lastSeen int64
		online   int
	}{}
	if rows, derr := env.DB.Query(`SELECT node_id, COALESCE(last_seen, 0), COALESCE(online, 0) FROM devices`); derr == nil {
		for rows.Next() {
			var nid string
			var st struct {
				lastSeen int64
				online   int
			}
			if err := rows.Scan(&nid, &st.lastSeen, &st.online); err == nil {
				devMap[nid] = st
			}
		}
		rows.Close()
	}
	// Look up the user's current default (if any) to mark the
	// matching row with ✓. Failures are non-fatal: the reply
	// still shows the menu, just without the highlight.
	defaultNodeID, _ := db.GetDefaultExitNode(env.DB, env.PortalUserID)
	marker := i18n.T(lang, "bot.myexitnodes.marker")

	// 2026-07-15: v0.14.0 — collect inline-keyboard rows in
	// parallel with the body. Each enabled node becomes a
	// button with callback_data "setexitnode:<node_id>";
	// the callback handler in notify.go applies the same
	// change /setexitnode would. The "Clear default" button
	// at the bottom resets the user's choice (callback_data
	// "setexitnode:clear"). Both inline + the text body go
	// back to the user.
	var btnRows [][]map[string]any

	// Column widths chosen to fit a phone screen
	// (HOSTNAME 16 / NODE 14 / STATUS 10 / DEFAULT 6).
	// Realistic exit-node hostnames in the wild are 6-10
	// chars, node_ids are 3-12, status is 6-7 chars, and
	// the default marker is just ✓ or empty. Truncation
	// adds "..." if a value ever overruns.
	const (
		colHost   = "%-16s"
		colNode   = "%-14s"
		colStatus = "%-10s"
		colMark   = "%-1s"
	)
	trunc := func(s string, w int) string {
		if len(s) <= w {
			return s
		}
		if w <= 3 {
			return s[:w]
		}
		return s[:w-3] + "..."
	}
	header := fmt.Sprintf(
		"<b>"+colHost+"  "+colNode+"  "+colStatus+"  "+colMark+"</b>",
		i18n.T(lang, "bot.myexitnodes.col_hostname"),
		i18n.T(lang, "bot.myexitnodes.col_node"),
		i18n.T(lang, "bot.myexitnodes.col_status"),
		i18n.T(lang, "bot.myexitnodes.col_default"),
	)
	ruleLine := strings.Repeat("─", 16+2+14+2+10+2+1)
	var lines []string
	lines = append(lines, header, "<i>"+ruleLine+"</i>")
	for _, s := range enabled {
		st := devMap[s.NodeID]
		status := "offline"
		if st.online == 1 {
			status = "online"
		}
		m := ""
		if s.NodeID == defaultNodeID {
			m = marker
		}
		lines = append(lines, fmt.Sprintf(
			colHost+"  "+colNode+"  "+colStatus+"  "+colMark,
			trunc(s.Hostname, 16),
			trunc(s.NodeID, 14),
			status,
			m,
		))
		// Build the button label with a checkmark for the
		// current default. Telegram's inline_keyboard limits
		// the label to 64 bytes — the hostname alone is well
		// under that, even for long hostnames.
		btnLabel := fmt.Sprintf("→ %s", s.Hostname)
		if s.NodeID == defaultNodeID {
			btnLabel = "✓ " + s.Hostname
		}
		btnRows = append(btnRows, []map[string]any{
			{"text": btnLabel, "callback_data": "setexitnode:" + s.NodeID},
		})
	}
	// "Clear default" button at the bottom. Only show it if
	// the user has a default set (otherwise the button is a
	// no-op that confuses the user).
	if defaultNodeID != "" {
		btnRows = append(btnRows, []map[string]any{
			{"text": i18n.T(lang, "bot.myexitnodes.clear_button"),
				"callback_data": "setexitnode:clear"},
		})
	}
	// Compose the reply. Section() splits the header from the
	// table; Field() surfaces the "N available" count next to
	// the header so the user can see at a glance how many
	// nodes are in the menu without scanning the table.
	reply := i18n.Tf(lang, "bot.myexitnodes.header", env.Username, len(enabled)) + "\n" +
		Section(i18n.T(lang, "bot.myexitnodes.section_menu")) + "\n" +
		Field(i18n.T(lang, "bot.myexitnodes.label_count"), strconv.Itoa(len(enabled))) + "\n" +
		PreLinesRaw(lines...) + "\n" +
		i18n.T(lang, "bot.myexitnodes.cta_tap")
	// 2026-07-16: v0.16.2 — preserve the ParseMode=HTML
	// that markHTMLReply() set at the top of this function.
	// The previous `&PendingReply{InlineKeyboard: btnRows}`
	// created a new struct without copying ParseMode, so
	// the <pre>/<b> in the body rendered as raw source.
	// Setting ParseMode=HTML explicitly on the new struct
	// is the cleanest fix (a "merge" helper would be more
	// code for a single call site).
	pendingReplyForCurrentMessage = &PendingReply{InlineKeyboard: btnRows, ParseMode: "HTML"}
	return trimForTelegram(reply)
}

// addDeviceReply issues a 1h single-use preauth key. For a regular
// user, the key is for themselves; for an admin, an optional
// `<username>` arg makes it for that user instead.
//
// Why this lives in the bot: posting a 1h preauth key from the web
// UI requires opening /my/preauth, copying the key, and shipping it
// to the device. The bot puts the key in the user's chat directly,
// so the workflow is: bot user types /add_device, copies the key,
// pastes into the device. ~10 seconds end-to-end.
//
// 2026-07-13: Этап 11 part 1 — real preauth issuance. Mirrors
// handlers_my_preauth.go:PostMyPreauth exactly:
//  1. env.HS.CreatePreauthKey (API + CLI fallback inside headscale pkg)
//  2. db.InsertPreauthKey (local row for the temporal backfill match)
//  3. db.AppendAuditLog (user can see "where did this key come from")
//
// The audit log records the action under the *target* user (so per-
// user audit views work) with detail "1h single-use (via bot)" so
// the bot-driven issuance is distinguishable from web-driven.
//
// Read-only deploys (HS == nil) get a clear hint instead of a panic.
// That keeps the legacy single-admin-chat deploy working even
// before SetHS is called from main.go.
