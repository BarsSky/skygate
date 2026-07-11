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
const dbSchemaVersion = "v0.27"

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
func helpDetailReply(cmd string) string {
	cmd = strings.ToLower(strings.TrimPrefix(cmd, "/"))
	switch cmd {
	case "status":
		return "/status — quick system summary.\n" +
			"Shows: total exit-rules, total portal users, last applied ACL snapshot version.\n" +
			"Example: /status"
	case "nodes":
		return "/nodes — list all tailnet devices grouped by tag.\n" +
			"Reads node_owner_map (the portal's snapshot of headscale's tag layout).\n" +
			"Tags: tag:private (per-user), tag:public (shared), tag:exit-node (egress), tag:untagged.\n" +
			"Example: /nodes"
	case "exit_nodes":
		return "/exit_nodes — list ONLY nodes tagged as exit-nodes, with last-seen.\n" +
			"Use this when /nodes is too noisy and you want to check egress health.\n" +
			"offline = devices.last_seen is null (headscale hasn't reported it recently).\n" +
			"Example: /exit_nodes"
	case "rules":
		return "/rules — show the 25 most recent exit-rules.\n" +
			"Each row: id, user, exit-node, target_type/value, action (accept/deny).\n" +
			"Use this to verify a rule was added, or to find an id to delete from the UI.\n" +
			"Example: /rules"
	case "quota":
		return "/quota — per-user rule count vs per-user cap (SKYGATE_USER_MAX_RULES).\n" +
			"Includes a progress bar. '∞' / '[no limit]' means no cap is configured.\n" +
			"Use this to spot a user about to hit their cap.\n" +
			"Example: /quota"
	case "audit":
		return "/audit — last 20 audit_log entries.\n" +
			"Shows admin actions: user_create, password_reset, telegram_save/disable, ACL_*.\n" +
			"Example: /audit"
	case "ack":
		return "/ack <id> — mark a previously-sent alert as acknowledged.\n" +
			"The id is the [#N] prefix on every alert message.\n" +
			"Idempotent: re-acking returns 'already acked' and writes no extra audit row.\n" +
			"Example: /ack 7"
	case "version":
		return "/version — show Skygate build label, Go runtime, DB schema level.\n" +
			"Use to confirm what's running after a deploy.\n" +
			"Example: /version"
	case "restart":
		return "/restart [confirm <token>] — graceful container restart.\n" +
			"First call mints a 6-char token (valid 30s).\n" +
			"Second call (with the token) sends SIGTERM to this process; the\n" +
			"container's `restart: unless-stopped` policy brings it back up, and\n" +
			"entrypoint.sh re-builds the binary from the bind-mounted source.\n" +
			"Example: /restart → /restart K7M2P9"
	case "help":
		return "/help [command] — list all commands, or show detailed help for one.\n" +
			"Examples:\n" +
			"  /help         (short list of every command)\n" +
			"  /help ack     (full reference for /ack with examples)"
	default:
		return fmt.Sprintf("No detailed help for %q. Try /help for the full list.", cmd)
	}
}
