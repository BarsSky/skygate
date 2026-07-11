package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// BotEnv is the read-only context HandleCommand needs beyond the
// database: per-user rule limits (from SKYGATE_USER_MAX_RULES), the
// default cap, and the DB itself.
//
// Why a struct: Phase 3 (/quota) needs to know per-user caps to
// answer "who is close to the limit". /ack needs the DB to update
// telegram_alerts. /exit_nodes and /nodes only need the DB. Threading
// a single struct is cleaner than a growing argument list, and
// tests can construct a BotEnv with empty limits to exercise the
// reply formatters without pulling in the full config stack.
type BotEnv struct {
	DB           *sql.DB
	UserMaxRules map[string]int
	DefaultMax   int
}

// MaxFor returns the per-user cap (from UserMaxRules) or the default.
// Return value of 0 means "no limit configured" — callers should
// render that as "∞" or "[no limit]" in the reply, not as a divisor.
func (e BotEnv) MaxFor(username string) int {
	if v, ok := e.UserMaxRules[username]; ok {
		return v
	}
	return e.DefaultMax
}

// HandleCommand returns the reply text for a command message.
// It is safe to call from the polling loop in Run().
//
// Phase 1 (MVP) implements /status. Phase 2 adds /nodes, /rules, /audit
// (see commands_phase2.go). Phase 3 adds /exit_nodes, /quota, /ack
// (see commands_phase3.go).
func HandleCommand(ctx context.Context, env BotEnv, raw string) string {
	_ = ctx
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 0 {
		return "Empty command."
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	switch cmd {
	case "/status":
		return statusReply(env.DB)
	case "/help":
		return helpReply()
	case "/nodes":
		return nodesReply(env.DB)
	case "/rules":
		return rulesReply(env.DB)
	case "/audit":
		return auditReply(env.DB)
	case "/exit_nodes":
		return exitNodesReply(env.DB)
	case "/quota":
		return quotaReply(env.DB, env)
	case "/ack":
		return ackReply(env.DB, strings.Join(args, " "))
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
	return "Commands:\n" +
		"/status — summary (rules/users/last acl)\n" +
		"/nodes — list tailnet devices by user+tag\n" +
		"/exit_nodes — list tailnet exit-nodes (tag:exit-node) with last-seen\n" +
		"/rules — recent exit-rules (id, user, target, action)\n" +
		"/quota — per-user rule count vs per-user cap\n" +
		"/audit — last 20 audit_log entries\n" +
		"/ack <id> — acknowledge an alert (id is the [#N] prefix)\n" +
		"/help — this list"
}
