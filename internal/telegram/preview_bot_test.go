// One-off preview test (not committed) — dumps the current
// /my_status, /my_rules, /my_nodes, /my_quota, /myexitnodes,
// /audit, /exit_nodes_health, /version, /restart replies
// to /tmp for eyeball review. Always passes.
//
// 2026-07-16: v0.16.x — "more HTML" pass added new tabular
// previews (my_rules, my_nodes, my_quota, myexitnodes) so
// the operator can eyeball the new aligned output without
// rebuilding + restarting the bot.
package telegram

import (
	"os"
	"strings"
	"testing"
)

func TestPreviewBotReplies(t *testing.T) {
	d := setupTestDB(t)
	// Seed audit rows + devices + exit-nodes so the
	// previews have something to show.
	_, _ = d.Exec(`INSERT INTO audit_log(username, action, detail, created_at) VALUES
		('alice', 'token_create', 'label=ci-runner ttl=1d auto_rotate=false', 1722000000),
		('alice', 'rule_added', 'via bot: ip 1.2.3.4 → emilia (action=accept, ids=[12])', 1722000100),
		('skyadmin', 'user_create', 'created bob', 1722000200),
		('alice', 'restart_confirmed', 'SIGTERM in 200ms, container restarted', 1722000300)`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag, hostname) VALUES
		('alice-laptop', 'alice', 'tag:private', 'alice-laptop'),
		('alice-phone', 'alice', 'tag:private', 'alice-phone')`)
	// Seed a couple of exit-rules for alice so /my_rules
	// has a multi-row table to render.
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES
		(2, 'emilia', 'subnet', '91.108.4.0/22', 'accept'),
		(2, 'emilia', 'subnet', 'github.com/32', 'accept'),
		(2, 'sharlotta', 'subnet', 'telegram.org/32', 'accept')`)
	// Seed two enabled exit-servers for alice so /myexitnodes
	// shows a table.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES
		('emilia-1', 'emilia', 1),
		('sharlotta-2', 'sharlotta', 1)`)
	_, _ = d.Exec(`UPDATE portal_users SET default_exit_node_id = 'sharlotta-2' WHERE id = 2`)

	env := userEnv(d)
	env.IsAdmin = true // /audit is admin-only
	cmds := []string{
		"/my_status", "/my_rules", "/my_nodes", "/my_quota", "/myexitnodes",
		"/audit", "/exit_nodes_health", "/version", "/restart",
	}
	for _, c := range cmds {
		got := HandleCommand(nil, env, c)
		out := "/tmp/preview_" + strings.TrimPrefix(c, "/") + ".txt"
		_ = os.WriteFile(out, []byte(got), 0644)
		t.Logf("Wrote %s (%d bytes)", out, len(got))
	}
}
