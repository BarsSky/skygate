package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// HandleCommand returns the reply text for a command message.
// It is safe to call from the polling loop in Run().
//
// Phase 1 (MVP) implements /status only. Phase 2 will add /nodes,
// /exit_nodes, /rules, /quota, /audit, /ack, /help.
func HandleCommand(ctx context.Context, d *sql.DB, raw string) string {
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 0 {
		return "Empty command."
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	_ = args  // used by commands that take arguments (phase 2)
	switch cmd {
	case "/status":
		return statusReply(d)
	case "/help":
		return helpReply()
	default:
		return fmt.Sprintf("Unknown command: %s. Try /help.", cmd)
	}
}

func statusReply(d *sql.DB) string {
	var totalRules, totalUsers, lastACL int64
	if err := d.QueryRow(`SELECT COUNT(*) FROM device_rules`).Scan(&totalRules); err != nil {
		return fmt.Sprintf("status: db error: %v", err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM portal_users`).Scan(&totalUsers); err != nil {
		return fmt.Sprintf("status: db error: %v", err)
	}
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM acl_snapshots WHERE applied_success=1`).Scan(&lastACL); err != nil {
		return fmt.Sprintf("status: db error: %v", err)
	}
	return fmt.Sprintf("Skygate status\nrules: %d\nusers: %d\nlast acl: #%d", totalRules, totalUsers, lastACL)
}

func helpReply() string {
	return "Commands (phase 1):\n/status \u2014 summary\n/help \u2014 this list\n\n(More commands coming in phase 2: /nodes, /exit_nodes, /rules, /quota, /audit)"
}
