package telegram

import (
	"crypto/rand"
	"database/sql"
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
	"skygate/internal/i18n"
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

// schemaLevelLabel answers "what schema level is this database at?" from the
// database itself.
//
// B390 (TD-33 H3): this replaced a hand-maintained constant
//
//	const dbSchemaVersion = "v0.32"
//
// which had disagreed with the live database for 49 migrations — the bot reported
// v0.32 against a v0.81 schema, and /help recommended /version as the post-deploy
// check. A number a human must remember to bump is not a fact about the deployment
// (the same class as "0 devices" and "every relay offline": a value that CAN be
// right, is not measured, and is therefore wrong in silence).
//
// The label is the migration's own version number, formatted the way the migration
// registry names it (version 81 → "v0.81"), so it is comparable with
// `applied_migrations` and with the registry in internal/db.
func schemaLevelLabel(lang string, d *sql.DB) string {
	v, err := db.LatestAppliedMigrationVersion(d)
	if err != nil {
		return i18n.Tf(lang, "bot.version.schema_unavailable", err)
	}
	if v == 0 {
		// A database where no migration ran yet: say that, do not invent a level.
		return i18n.T(lang, "bot.version.schema_none")
	}
	return fmt.Sprintf("v0.%d", v)
}

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
// 2026-08-12 v1.3.9 (P4 catalog cleanup): setKillProcess
// was a leftover test hook from a kill-signal test
// that was removed when we moved the test suite to
// pure runtime (no kill signal in the test path).
// Staticcheck U1000 (unused). Removed.

// versionReply returns the build label, Go runtime, and DB schema
// level. The Go version comes from runtime.Version(); the schema
// level is a constant (see dbSchemaVersion comment for how to
// maintain it).
//
// 2026-07-16: v0.16.x — "more HTML" pass. Uses Field() for
// aligned key/value display (label bold, value in <code>)
// so the three lines render as a tabbed block on mobile
// Telegram.
func versionReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML so the <b>Билд:</b>
	// / <b>Go:</b> / <b>Схема БД:</b> Field() labels and
	// the <code>value</code> render. Without this, the
	// user sees raw "<b>...</b> <code>...</code>" text.
	markHTMLReply()
	v := env.Version
	if v == "" {
		v = "v0.0-dev"
	}
	return i18n.T(env.Lang, "bot.version.title") + "\n\n" +
		Field(i18n.T(env.Lang, "bot.version.label_build"), v) + "\n" +
		Field(i18n.T(env.Lang, "bot.version.label_go"), runtime.Version()) + "\n" +
		Field(i18n.T(env.Lang, "bot.version.label_schema"), schemaLevelLabel(env.Lang, env.DB))
}

// versionBlocks is the B186 rich-message variant of
// /version. Same data as versionReply, structured as a
// Heading + KeyValueTable (3 rows: build / go / schema)
// + Footer. The envelope (gate + signoff) is added by
// ComposeRich in the dispatcher.
//
// 2026-08-25 (B186): /version is the second command
// migrated to the rich path — chosen because the data
// fits naturally in a 3-row key/value table and the
// command is exercised frequently by both the operator
// (smoke test) and the v1 personality tests.
func versionBlocks(env BotEnv) []RichBlock {
	v := env.Version
	if v == "" {
		v = "v0.0-dev"
	}
	return []RichBlock{
		Heading(i18n.T(env.Lang, "bot.version.title"), 2),
		KeyValueTableLang(env.Lang, []KVRow{
			{Label: i18n.T(env.Lang, "bot.version.label_build"), Value: v},
			{Label: i18n.T(env.Lang, "bot.version.label_go"), Value: runtime.Version()},
			{Label: i18n.T(env.Lang, "bot.version.label_schema"), Value: schemaLevelLabel(env.Lang, env.DB)},
		}),
	}
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
	lang := env.Lang
	arg = strings.TrimSpace(arg)
	if arg == "" {
		// Phase 1: mint a token.
		token, err := mintRestartToken()
		if err != nil {
			return i18n.Tf(lang, "bot.restart.mint_failed", err)
		}
		exp := time.Now().Add(restartTTL)
		pendingRestarts.Store(token, exp)
		// Audit the request (not the action — that's a separate row
		// the second phase writes). Lets the operator see who asked
		// for a restart, even if they don't follow through.
		_ = env.DB // not used here; kept for symmetry with other reply funcs
		return i18n.Tf(lang, "bot.restart.confirm_prompt", token)
	}
	// Phase 2: confirm with a token.
	v, ok := pendingRestarts.Load(arg)
	if !ok {
		return i18n.Tf(lang, "bot.restart.invalid_token", arg)
	}
	expiry, ok := v.(time.Time)
	if !ok {
		// shouldn't happen — we only ever store time.Time — but
		// treat a malformed entry as "expired" rather than panic.
		pendingRestarts.Delete(arg)
		return i18n.T(lang, "bot.restart.corrupt")
	}
	if time.Now().After(expiry) {
		pendingRestarts.Delete(arg)
		return i18n.Tf(lang, "bot.restart.expired", arg, restartTTL)
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
	return i18n.T(lang, "bot.restart.confirmed")
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
//
// B390 (TD-33, the last item): the 23 reference texts already lived in BOTH
// catalogues as `bot.help_detail.<command>`, and this function ignored them —
// it was a 150-line switch of hardcoded ENGLISH strings, so a Russian-speaking
// operator who asked /help exit_nodes got English back while the translation sat
// unused in the runtime catalogue. The lookup below IS the reference: the
// catalogue is the single source, and a command without an entry says so
// (in the caller's language) instead of falling through to an English default.
//
// The short /help (no argument) still calls helpReply in commands.go.
func helpDetailReply(cmd string, env BotEnv) string {
	cmd = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cmd), "/"))
	if cmd == "" {
		return i18n.T(env.Lang, "bot.help_detail.missing")
	}
	key := "bot.help_detail." + cmd
	if text := i18n.T(env.Lang, key); text != key {
		return text
	}
	// i18n.T returns the KEY when the catalogue has no entry (its documented
	// fallback), so reaching here means "no detailed reference for this command".
	return i18n.Tf(env.Lang, "bot.help_detail.missing", cmd)
}
