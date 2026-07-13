package telegram

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"skygate/internal/db"
)

// Phase 4 commands: /version, /restart, /help <command>.
//
// /version — build label (from app.Version), Go runtime, DB schema
// level. Lets the operator confirm what's running without SSH-ing
// into the box.
//
// /restart — graceful container restart with a one-time token. The
// first call mints a 6-char token valid for 30s; the second call
// (with the token) sends SIGTERM to the current process. The
// entrypoint.sh + docker-compose `restart: unless-stopped` policy
// bring the container back up; the entrypoint re-runs `go build`
// so any updated source under the bind mount is picked up.
//
// /help <command> — detailed help for a single command. The plain
// /help still shows the short list (backward compatible).

// dbSchemaVersion is the most recent migration level. Bump this
// when a new migrations_v*.go is added. The version string is the
// number of the highest migration in cmd/skygate's migrate() chain
// (see internal/db/db.go).
const dbSchemaVersion = "v0.29"

// restartTTL is how long a freshly-issued /restart token is valid.
// 30s is enough for a human to type the 6-char token in a follow-up
// message; tight enough that a leaked token can't be reused later.
const restartTTL = 30 * time.Second

// restartAlphabet is the character set for /restart tokens. 6 chars
// from 32 symbols = ~ 32^6 = ~1 billion possibilities; plenty for
// a 30s window.
const restartAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I,O,0,1 — easy to misread

// pendingRestarts holds unconsumed restart confirmations.
// Key: 6-char token. Value: time.Time (expiry).
// sync.Map is fine here — the map is small (one entry per active
// confirmation) and reads are common (we check on every /restart).
var pendingRestarts sync.Map

// killProcess holds the function /restart uses to terminate the
// process. Production loads it once at init and never reassigns;
// tests reassign it (via setKillProcess) to a no-op or a
// channel-signalling stub so the test binary doesn't actually die.
//
// We use atomic.Pointer[func()] (not a plain var) so that the
// restart goroutine (which reads killProcess ~200ms after the
// HandleCommand call returns) and the test's override/teardown
// don't race. -race catches the plain-var version.
//
// We use os.FindProcess (not syscall.Kill) so the file compiles
// on Windows dev machines — syscall.Kill is unix-only. The
// runtime target is the Linux container, where SIGTERM produces
// a graceful shutdown.
var killProcess atomic.Pointer[func()]

func init() {
	defaultKill := func() {
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			return
		}
		_ = p.Signal(syscall.SIGTERM)
	}
	killProcess.Store(&defaultKill)
}

// getKillProcess atomically loads the current killProcess func.
// Returns nil if the atomic has never been initialised (shouldn't
// happen — init() always stores — but defensive against manual
// zero-value usage in future tests).
func getKillProcess() func() {
	if p := killProcess.Load(); p != nil {
		return *p
	}
	return nil
}

// setKillProcess atomically replaces the killProcess func.
// Used by tests to install a no-op or signalling stub.
func setKillProcess(fn func()) {
	killProcess.Store(&fn)
}

// versionReply returns the build label, Go runtime, and DB schema
// level. The Go version comes from runtime.Version(); the schema
// level is a constant (see dbSchemaVersion comment for how to
// maintain it).
func versionReply(env BotEnv) string {
	v := env.Version
	if v == "" {
		v = "v0.0-dev"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Skygate %s\n", v)
	fmt.Fprintf(&sb, "Go: %s\n", runtime.Version())
	fmt.Fprintf(&sb, "DB schema: %s\n", dbSchemaVersion)
	return strings.TrimRight(sb.String(), "\n")
}

// restartReply handles both phases of /restart:
//
//	/restart           → mints a token, returns it
//	/restart <token>   → checks the token, triggers graceful restart
//
// The token is a 6-char alphanumeric from restartAlphabet, valid
// for 30s. After that, the entry is dropped (lazy eviction on
// next /restart call).
func restartReply(env BotEnv, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		// Phase 1: mint a token.
		token, err := mintRestartToken()
		if err != nil {
			return fmt.Sprintf("restart: token mint failed: %v", err)
		}
		exp := time.Now().Add(restartTTL)
		pendingRestarts.Store(token, exp)
		// Audit the request (not the action — that's a separate row
		// the second phase writes). Lets the operator see who asked
		// for a restart, even if they don't follow through.
		_ = env.DB // not used here; kept for symmetry with other reply funcs
		return fmt.Sprintf(
			"restart: confirm by sending within 30s\n"+
				"  /restart %s\n"+
				"(ignored if the token is wrong, expired, or the request is older than 30s)",
			token)
	}
	// Phase 2: confirm with a token.
	v, ok := pendingRestarts.Load(arg)
	if !ok {
		return fmt.Sprintf("restart: %q is not a valid confirmation token\n"+
			"(send /restart alone to mint a new one)", arg)
	}
	expiry, ok := v.(time.Time)
	if !ok {
		// shouldn't happen — we only ever store time.Time — but
		// treat a malformed entry as "expired" rather than panic.
		pendingRestarts.Delete(arg)
		return "restart: token store is corrupted; mint a new one with /restart"
	}
	if time.Now().After(expiry) {
		pendingRestarts.Delete(arg)
		return fmt.Sprintf("restart: token %q expired (>%s old); mint a new one with /restart",
			arg, restartTTL)
	}
	// Valid token, not expired. Consume it (delete so it can't be
	// reused) and trigger the restart.
	pendingRestarts.Delete(arg)

	// Audit the actual restart action. Failure here isn't fatal —
	// the process is about to die anyway — but log via env so an
	// operator scanning the audit log right before the restart
	// (and after, when the new container comes up) sees who
	// triggered it.
	// 2026-07-11: Этап 9 part 2 — moved to db.AppendAuditLogNoUser
	_ = db.AppendAuditLogNoUser(env.DB, "telegram", "telegram_restart", fmt.Sprintf("token=%s", arg))

	// SIGTERM the process. main.go's signal handler does
	// srv.Shutdown(5s) and then returns; the container's
	// `restart: unless-stopped` policy brings it back.
	go func() {
		// Brief delay so the reply message lands on Telegram
		// before the process dies.
		time.Sleep(200 * time.Millisecond)
		if fn := getKillProcess(); fn != nil {
			fn()
		}
	}()
	return "restart: confirmed — SIGTERM in 200ms, container will restart"
}

// mintRestartToken returns a 6-char token from restartAlphabet.
// Uses crypto/rand so a guesser can't predict the next token even
// if they see the previous one.
func mintRestartToken() (string, error) {
	out := make([]byte, 6)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(restartAlphabet))))
		if err != nil {
			return "", err
		}
		out[i] = restartAlphabet[n.Int64()]
	}
	return string(out), nil
}

// helpDetailReply returns long-form help for a single command.
// Unknown commands return a hint pointing the operator back at /help.
// The short /help (no arg) still calls the legacy helpReply in
// commands.go so existing operators don't lose the overview.
//
// env is currently unused but kept in the signature so the help
// text can be tailored (e.g. omit admin commands for non-admin
// callers) in a future change without a separate API.
func helpDetailReply(cmd string, env BotEnv) string {
	_ = env
	cmd = strings.ToLower(strings.TrimPrefix(cmd, "/"))
	switch cmd {
	case "status":
		return "/status — quick system summary (admin only).\n" +
			"Shows: total exit-rules, total portal users, last applied ACL snapshot version.\n" +
			"Use /my_status for your own summary instead.\n" +
			"Example: /status"
	case "nodes":
		return "/nodes — list all tailnet devices grouped by tag (admin only).\n" +
			"Reads node_owner_map (the portal's snapshot of headscale's tag layout).\n" +
			"Tags: tag:private (per-user), tag:public (shared), tag:exit-node (egress), tag:untagged.\n" +
			"Use /my_nodes to see only your own devices.\n" +
			"Example: /nodes"
	case "exit_nodes":
		return "/exit_nodes — list ONLY nodes tagged as exit-nodes, with last-seen (admin only).\n" +
			"Use this when /nodes is too noisy and you want to check egress health.\n" +
			"offline = devices.last_seen is null (headscale hasn't reported it recently).\n" +
			"Example: /exit_nodes"
	case "rules":
		return "/rules — show the 25 most recent exit-rules across all users (admin only).\n" +
			"Each row: id, user, exit-node, target_type/value, action (accept/deny).\n" +
			"Use /my_rules to see only your own.\n" +
			"Example: /rules"
	case "quota":
		return "/quota — per-user rule count vs per-user cap (admin only).\n" +
			"Includes a progress bar. '∞' / '[no limit]' means no cap is configured.\n" +
			"Use /my_quota to see only your own.\n" +
			"Example: /quota"
	case "audit":
		return "/audit — last 20 audit_log entries (admin only).\n" +
			"Shows admin actions: user_create, password_reset, telegram_save/disable, ACL_*.\n" +
			"Example: /audit"
	case "ack":
		return "/ack <id> — mark a previously-sent alert as acknowledged (admin only).\n" +
			"The id is the [#N] prefix on every alert message.\n" +
			"Idempotent: re-acking returns 'already acked' and writes no extra audit row.\n" +
			"Example: /ack 7"
	case "version":
		return "/version — show Skygate build label, Go runtime, DB schema level.\n" +
			"Use to confirm what's running after a deploy.\n" +
			"Example: /version"
	case "restart":
		return "/restart [confirm <token>] — graceful container restart (admin only).\n" +
			"First call mints a 6-char token (valid 30s).\n" +
			"Second call (with the token) sends SIGTERM to this process; the\n" +
			"container's `restart: unless-stopped` policy brings it back up, and\n" +
			"entrypoint.sh re-builds the binary from the bind-mounted source.\n" +
			"Example: /restart → /restart K7M2P9"
	case "bind":
		return "/bind <chat_id> <username> — bind a Telegram chat to a portal user (admin only).\n" +
			"The user supplies their chat_id (a positive integer for a DM, negative for a group)\n" +
			"and the admin pastes the command. Once bound, the chat can use /my_* commands\n" +
			"and write rules for the bound user.\n" +
			"Example: /bind 123456789 michail"
	case "unbind":
		return "/unbind <chat_id> — remove a chat binding (admin only).\n" +
			"Example: /unbind 123456789"
	case "my_status":
		return "/my_status — your own summary (rules count / cap, device count, last ACL).\n" +
			"Example: /my_status"
	case "my_nodes":
		return "/my_nodes — list only your own devices.\n" +
			"Filtered by telegram_bindings.portal_user_id. Admin uses this to see their own devices.\n" +
			"Example: /my_nodes"
	case "my_rules":
		return "/my_rules — list only your own exit-rules, newest first (max 25).\n" +
			"Use /delrule <id> to remove one.\n" +
			"Example: /my_rules"
	case "my_quota":
		return "/my_quota — your own rule count vs your per-user cap.\n" +
			"Same bar format as /quota, but only one row.\n" +
			"Example: /my_quota"
	case "myexitnodes":
		return "/myexitnodes — list every enabled exit-node you can route through.\n" +
			"Same data as admin /exit_nodes (hostname, online, last_seen) but\n" +
			"filtered to enabled=1 and tagged with [default] for the exit-node\n" +
			"your /setexitnode is currently pointing at.\n" +
			"Workflow: /myexitnodes -> /setexitnode <node_id> -> /add_rule <target>\n" +
			"Example: /myexitnodes"
	case "add_device":
		return "/add_device [username] — issue a 1h single-use preauth key.\n" +
			"Default: a key for yourself. With a username arg (admin only), for that user.\n" +
			"Note: bot-side key issuance is on the roadmap; the bot currently redirects to /my/preauth.\n" +
			"Examples:\n" +
			"  /add_device             (self)\n" +
			"  /add_device michail     (admin → michail)"
	case "add_rule":
		return "/add_rule <target> [deny]  — add a new exit-rule.\n" +
			"      /add_rule <username> <target> [deny]  (admin only)\n" +
			"target: domain (e.g. telegram.org), ip (1.2.3.4), or subnet (10.0.0.0/8).\n" +
			"Note: bot-side rule writes are on the roadmap; the bot currently redirects to /my/exit-rules.\n" +
			"Examples:\n" +
			"  /add_rule telegram.org\n" +
			"  /add_rule 1.2.3.4 deny\n" +
			"  /add_rule michail telegram.org"
	case "delrule":
		return "/delrule <id> [id2 ...] — remove one or more of your own rules.\n" +
			"/delrule <username> <id> ... — admin only: delete for another user.\n" +
			"Domain rules cascade to their /32 siblings (one delete covers the whole domain).\n" +
			"Multi-id: space-separated. Skipped ids are reported in the reply, not failed.\n" +
			"Triggers an ACL sync (same as /add_rule).\n" +
			"Examples:\n" +
			"  /delrule 7\n" +
			"  /delrule 7 8 9\n" +
			"  /delrule alice 5 6    (admin → alice)"
	case "clearrules":
		return "/clearrules [username] — wipe ALL exit-rules for you (admin: another user).\n" +
			"Two-phase: first call counts + samples the rules, second call (within 30s) confirms.\n" +
			"Domain rules cascade to /32 siblings (same as /delrule).\n" +
			"Triggers an ACL sync.\n" +
			"Use /delrule if you want to keep some rules; /clearrules is the nuclear option.\n" +
			"Examples:\n" +
			"  /clearrules\n" +
			"  /clearrules confirm          (within 30s)\n" +
			"  /clearrules alice            (admin → alice)\n" +
			"  /clearrules alice confirm    (admin confirms)"
	case "delete_rule":
		return "/delete_rule <id> — DEPRECATED alias of /delrule.\n" +
			"Use /delrule instead. The command still works for back-compat.\n" +
			"Example: /delete_rule 7   (use /delrule 7)"
	case "help":
		return "/help [command] — list all commands, or show detailed help for one.\n" +
			"Examples:\n" +
			"  /help         (short list of every command)\n" +
			"  /help ack     (full reference for /ack with examples)"
	default:
		return fmt.Sprintf("No detailed help for %q. Try /help for the full list.", cmd)
	}
}
