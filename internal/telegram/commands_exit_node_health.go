// 2026-07-15: v0.13.0 — /exit_nodes_health bot command.
//
// A focused slice of /exit_nodes: instead of the per-user /
// per-device list, this command renders the operator-facing
// health snapshot the background monitor (see
// internal/monitoring/exit_node_monitor.go) maintains.
//
// One row per monitored node:
//
//   hostname | state | last_seen_ago | last_check
//
// Plus a summary header (X/Y healthy, all-offline warning).
//
// The command is admin-only (registered in commands.go's
// adminOnly map) because the data is the operator's infra
// state, not something a regular user needs to see. /exit_nodes
// (user-facing) stays as-is.

package telegram

import (
	"fmt"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// exitNodesHealthReply lists the current exit-node health
// snapshot. Output is grouped by state (offline first, then
// degraded, then online) so a quick scan surfaces what needs
// attention. Last-check and last-seen use the same short
// formatting the existing /exit_nodes command uses
// (unixToShort + a "Xm ago" relative).
func exitNodesHealthReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML so the <b>STATE
	// NODE LAST SEEN LAST CHECK</b> header row + the
	// <i>──────</i> separators in PreLinesRaw() render.
	markHTMLReply()
	lang := env.Lang
	d := env.DB
	rows, err := db.ListExitNodeHealth(d)
	if err != nil {
		return i18n.Tf(lang, "bot.exit_nodes_health.db_error", err)
	}
	if len(rows) == 0 {
		return i18n.T(lang, "bot.exit_nodes_health.empty")
	}

	// Group by state. The "buckets" map preserves insertion
	// order via a parallel slice — Go map iteration is
	// randomised and we want a stable display (offline first).
	type bucket struct {
		state string
		rows  []db.ExitNodeHealth
	}
	buckets := []bucket{
		{state: "offline"}, {state: "degraded"}, {state: "online"},
	}
	byState := map[string][]db.ExitNodeHealth{
		"offline": nil, "degraded": nil, "online": nil,
	}
	for _, h := range rows {
		byState[h.State] = append(byState[h.State], h)
	}
	for i := range buckets {
		buckets[i].rows = byState[buckets[i].state]
	}

	healthy := byState["online"]
	healthyCount := len(healthy)
	totalCount := len(rows)

	now := time.Now().UTC()
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.exit_nodes_health.header", healthyCount, totalCount))
	if healthyCount == 0 && totalCount > 0 {
		fmt.Fprintf(&sb, "%s\n\n", i18n.T(lang, "bot.exit_nodes_health.all_offline_warning"))
	}
	for _, b := range buckets {
		if len(b.rows) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.exit_nodes_health.bucket_"+b.state, len(b.rows)))
		for _, h := range b.rows {
			seen := "—"
			if !h.LastSeenParsed.IsZero() {
				ago := now.Sub(h.LastSeenParsed)
				if ago < 0 {
					ago = 0
				}
				seen = formatAgo(ago)
			}
			checked := "—"
			if !h.LastCheckAt.IsZero() {
				checked = unixToShort(h.LastCheckAt.Unix())
			}
			fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.exit_nodes_health.row",
				h.Hostname, b.state, seen, checked))
		}
	}
	return trimForTelegram(sb.String())
}

// formatAgo is a small helper for "Xm ago" / "Xh ago" / "Xd ago"
// formatting used by /exit_nodes_health. Kept local because the
// existing unixToShort is absolute (no relative) and we want
// the relative form here ("3m ago" reads faster than
// "2026-07-15 12:34" when the operator is triaging).
func formatAgo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd ago", days)
}
