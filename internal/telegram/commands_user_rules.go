package telegram

import (
	"fmt"
	"skygate/internal/i18n"
	"strconv"
	"strings"
)

func myRulesReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML so the <b>ID/EXIT/...</b>
	// header row in PreLinesRaw() renders. Also required for
	// the <b>username</b> in the header line (bot.my_rules.header
	// has "<b>%s</b>").
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.my_rules.not_bound")
	}
	rows, err := env.DB.Query(`
		SELECT r.id, r.exit_node_id, r.target_type, r.target_value,
		       COALESCE(r.action, 'accept') AS action
		  FROM device_rules r
		 WHERE r.user_id = $1
		 ORDER BY r.id DESC
		 LIMIT 25`, env.PortalUserID)
	if err != nil {
		return i18n.Tf(lang, "bot.my_rules.db_error", err)
	}
	defer rows.Close()
	type rule struct {
		id                         int64
		exitNode, tType, tVal, act string
	}
	var rules []rule
	for rows.Next() {
		var rr rule
		if err := rows.Scan(&rr.id, &rr.exitNode, &rr.tType, &rr.tVal, &rr.act); err != nil {
			return i18n.Tf(lang, "bot.my_rules.scan_error", err)
		}
		rules = append(rules, rr)
	}
	if len(rules) == 0 {
		return i18n.Tf(lang, "bot.my_rules.empty", env.Username)
	}
	// Column widths chosen so the table fits a phone screen
	// (60-65 chars wide at Telegram's default font) while
	// staying wide enough for the longest realistic value in
	// each column: ID 4 (e.g. "#9999"), EXIT 12 (hostnames
	// like "relay-2"), TYPE 6 ("subnet"/"domain"/"ip"),
	// TARGET 24 (e.g. "91.108.4.0/22" or "github.com/32"),
	// ACTION 6 ("accept"/"deny"). Truncation falls back to
	// "..." when a value runs over.
	const (
		colID     = "%-4s"
		colExit   = "%-12s"
		colType   = "%-6s"
		colTarget = "%-24s"
		colAction = "%-6s"
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
		"<b>"+colID+"  "+colExit+"  "+colType+"  "+colTarget+"  "+colAction+"</b>",
		i18n.T(lang, "bot.my_rules.col_id"),
		i18n.T(lang, "bot.my_rules.col_exit"),
		i18n.T(lang, "bot.my_rules.col_type"),
		i18n.T(lang, "bot.my_rules.col_target"),
		i18n.T(lang, "bot.my_rules.col_action"),
	)
	ruleLine := strings.Repeat("─", 4+2+12+2+6+2+24+2+6)
	var lines []string
	lines = append(lines, header, "<i>"+ruleLine+"</i>")
	for _, rr := range rules {
		lines = append(lines, fmt.Sprintf(
			colID+"  "+colExit+"  "+colType+"  "+colTarget+"  "+colAction,
			"#"+strconv.FormatInt(rr.id, 10),
			trunc(rr.exitNode, 12),
			rr.tType,
			trunc(rr.tVal, 24),
			rr.act,
		))
	}
	// 2026-07-16: v0.16.5 — split into 2 bubbles if
	// more than 12 rules. /my_rules is the user's own
	// exit-rules; 12 is a reasonable threshold (most
	// users have 1-10 rules, but power users with
	// larger policies can hit 15+). Same pattern as
	// /audit: first bubble gets the title + section
	// header + the first half, second bubble gets
	// the rest. A "(N more — see next)" hint at the
	// end of the first bubble keeps the user oriented.
	const myRulesSplitThreshold = 12
	body := i18n.Tf(lang, "bot.my_rules.header", env.Username, len(rules)) + "\n" +
		Section(i18n.T(lang, "bot.my_rules.section_recent")) + "\n" +
		PreLinesRaw(lines...)
	if len(rules) <= myRulesSplitThreshold {
		return body
	}
	half := len(rules) / 2
	firstLines := lines[:2+half] // header (2 lines) + first half
	secondLines := lines[2+half:]
	firstBody := i18n.Tf(lang, "bot.my_rules.header", env.Username, len(rules)) + "\n" +
		Section(i18n.T(lang, "bot.my_rules.section_recent")) + "\n" +
		PreLinesRaw(firstLines...) + "\n\n" +
		"<i>(" + i18n.Tf(lang, "bot.my_rules.split_more", len(rules)-half) + ")</i>"
	secondBody := PreLinesRaw(secondLines...)
	return firstBody + splitMessageMarker + secondBody
}

// myQuotaReply shows the caller's own rule count vs their cap. The
// existing /quota renders the same bar across all users; this is the
// single-user version so a user can ask "how close am I?" without
// the admin's /quota having to answer.
//
// 2026-07-16: v0.16.x — "more HTML" pass. The reply now uses
// Field() for each metric (rules count, fill bar, cap) under a
// single Section() divider, the same way /my_status, /version,
// and /exit_nodes_health do. The previous "  %d / %s %s %d%%"
// prose row is gone — it was a single line that mashed three
// different data points together (count + cap + bar + pct),
// which the user had to mentally parse.
