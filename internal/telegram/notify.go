// Package telegram implements Skygate's notification + bot-command surface.
//
// Two responsibilities:
//
//  1. Outgoing notifications (Skygate -> Telegram). SendTelegram() formats
//     and POSTs a message to the configured chat_id via the Telegram
//     sendMessage API. A NoopNotifier is used when the bot is not
//     configured so callers don't have to nil-check.
//
//  2. Incoming commands (Telegram -> Skygate). Run() polls getUpdates
//     and dispatches text starting with "/" to HandleCommand. Replies
//     are sent back to the same chat.
//
// Configuration is read from the database (db.LoadTelegramToken) so the
// admin can rotate the token from the /admin/telegram page without
// restarting the process.
package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// Notifier is the interface used by code that wants to emit a message.
// Real impl talks to the Telegram API. Noop is a no-op (used when no
// token is configured).
type Notifier interface {
	SendTelegram(text string)
	// SendAlert posts text as a numbered alert (for /ack). Returns
	// the alert id, or 0 when the bot is not configured.
	SendAlert(text string) int64
}

// NoopNotifier discards all messages. Used when no token is configured.
type NoopNotifier struct{}

func (NoopNotifier) SendTelegram(string) {}

// RealNotifier holds the per-process Telegram configuration. The
// configured flag is consulted at SendTelegram time so a hot-swap
// (admin saves a token at runtime) takes effect without restart.
type RealNotifier struct {
	mu       sync.Mutex
	apiBase  string
	db       *sql.DB
	client   *http.Client
	pollInt  time.Duration
	off      bool
	// 2026-07-11: Phase 3 — per-user rule limits, used by /quota.
	// Stored on the notifier because main.go already constructs
	// NewRealNotifier with full config in hand; HandleCommand asks
	// the notifier for limits via BotEnv.
	userMaxRules map[string]int
	defaultMax   int
	// 2026-07-11: Phase 4 — build version (set by main.go from
	// app.Version). Surfaces in /version reply.
	version string
	// 2026-07-13: Этап 11 part 1 — *headscale.Client, set by main.go
	// from the same instance that handlers use. Needed by write-side
	// bot commands (/add_device issues a real preauth key against
	// headscale; /add_rule and /delete_rule, planned for part 2, will
	// need it for ACL sync via SetPolicy). nil is allowed — write
	// commands guard explicitly and return a clear "telegram not wired
	// for writes" hint so the existing read-only deploys keep working.
	HS *headscale.Client
}

func NewRealNotifier(d *sql.DB) *RealNotifier {
	api := os.Getenv("TELEGRAM_API")
	if api == "" {
		api = "https://api.telegram.org"
	}
	return &RealNotifier{
		apiBase: api,
		db:      d,
		client:  &http.Client{Timeout: 15 * time.Second},
		pollInt: 2 * time.Second,
	}
}

// SetLimits configures per-user rule caps consumed by /quota. Called
// once at startup from cmd/skygate/main.go after config.Load().
// Safe to leave at zero-values: /quota then shows "no limit" for
// every user.
func (n *RealNotifier) SetLimits(userMax map[string]int, defaultMax int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.userMaxRules = userMax
	n.defaultMax = defaultMax
}

// SetVersion stores the build label (e.g. "v0.3") used by /version.
// Called once at startup from cmd/skygate/main.go. Empty string is
// fine — /version then prints "v0.0-dev".
func (n *RealNotifier) SetVersion(v string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.version = v
}

// SetHS stores the *headscale.Client used by write-side bot commands
// (/add_device issues a real preauth key, /add_rule and /delete_rule
// planned for part 2 trigger ACL sync). Called once at startup from
// cmd/skygate/main.go — pass the same *headscale.Client that the
// web handlers use so a single source of truth drives both surfaces.
//
// nil is a valid value: it means the bot is running in read-only
// mode (the legacy single-admin-chat deploy). Write commands guard
// against nil and reply with a clear hint instead of crashing.
func (n *RealNotifier) SetHS(hs *headscale.Client) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.HS = hs
}

// env returns a BotEnv snapshot for HandleCommand. The DB pointer
// is the same one we already hold; the limits are read under the
// mu lock so a future SetLimits call mid-poll doesn't tear the map.
//
// 2026-07-12: Этап 11 — env() now takes a chat_id and looks up the
// binding in telegram_bindings. The legacy single-chat deploy
// (no rows in telegram_bindings) keeps working because the
// dispatcher in Run() also falls back to "treat as admin" when
// chat_id matches the configured telegram.chat_id.
func (n *RealNotifier) env(chatID int64) BotEnv {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Copy the map so HandleCommand can't observe a concurrent
	// SetLimits. The map is tiny (one entry per user) so the cost
	// is negligible.
	max := make(map[string]int, len(n.userMaxRules))
	for k, v := range n.userMaxRules {
		max[k] = v
	}
	env := BotEnv{DB: n.db, UserMaxRules: max, DefaultMax: n.defaultMax, Version: n.version, ChatID: chatID, HS: n.HS}
	if chatID == 0 {
		return env // legacy / no identity
	}
	// Look up the binding. ErrTelegramBindingNotFound is a normal
	// case (chat not bound yet) — we leave the env unidentified so
	// admin-only commands short-circuit to "chat not bound" rather
	// than being treated as admin. The dispatcher in Run() applies
	// the bootstrap-admin fallback before we get here.
	b, err := db.GetTelegramBinding(n.db, chatID)
	if err != nil {
		return env
	}
	env.PortalUserID = b.PortalUserID
	env.IsAdmin = b.IsAdmin
	// Username is not denormalized in the binding (we keep it lean);
	// look it up on demand. One indexed read per command is fine.
	if u, err := lookupPortalUsername(n.db, b.PortalUserID); err == nil {
		env.Username = u
	}
	return env
}

// lookupPortalUsername reads portal_users.username for a single
// portal_user_id. Used by env() to populate BotEnv.Username from
// the binding. Returning "" on error is fine — the bot's
// user-scope commands check Username == "" explicitly.
func lookupPortalUsername(d *sql.DB, userID int64) (string, error) {
	var u string
	err := d.QueryRow(`SELECT username FROM portal_users WHERE id = ?`, userID).Scan(&u)
	return u, err
}

// SendTelegram posts text to the configured chat_id. Silently no-ops if
// the token is not configured. Errors are logged but not returned, since
// this is fire-and-forget notification code; callers should not block
// on Telegram availability.
func (n *RealNotifier) SendTelegram(text string) {
	if n == nil {
		return
	}
	if !n.Configured() {
		return
	}
	token, chatID, ok, err := db.LoadTelegramToken(n.db)
	if err != nil || !ok {
		log.Printf("telegram: skip send: load err=%v ok=%v", err, ok)
		return
	}
	if !strings.HasPrefix(text, "```") {
		// Wrap plain text in a small fence so replies render monospaced.
		text = "```\n" + text + "\n```"
	}
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/sendMessage"
	payload := map[string]any{
		"chat_id":                chatID,
		"text":                   text,
		"disable_web_page_preview": true,
	}
	body, _ := json.Marshal(payload)
	resp, err := n.client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("telegram: POST %s failed: %v", endpoint, err)
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rb), `"ok":true`) {
		log.Printf("telegram: non-ok response: %s", string(rb))
	}
}

// Configured returns true if the bot token is present in the database.
// Called by callers that want to skip formatting work for a disabled
// bot.
func (n *RealNotifier) Configured() bool {
	if n == nil {
		return false
	}
	_, _, ok, err := db.LoadTelegramToken(n.db)
	if err != nil {
		return false
	}
	return ok
}

// Run polls getUpdates and dispatches commands. Blocks until ctx is
// done. Errors are logged and retried after a backoff.
//
// 2026-07-12: Этап 11 — Run now reads each update's chat_id and
// replies to the originating chat (not the configured admin chat
// only). The chat_id is also fed to env() so HandleCommand knows
// who's messaging. Replies to unbound chats are silently dropped
// (the bot silently ignores — there's no chat to send to).
func (n *RealNotifier) Run(ctx context.Context) {
	offset := int64(0)
	backoff := n.pollInt
	for {
		if ctx.Err() != nil {
			return
		}
		token, _, ok, err := db.LoadTelegramToken(n.db)
		if err != nil || !ok {
			// No token configured; sleep and re-check.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		updates, err := n.fetch(token, offset)
		if err != nil {
			log.Printf("telegram: getUpdates error: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
		}
		backoff = n.pollInt
		for _, u := range updates {
			offset = u.UpdateID + 1
			text := u.Message.Text
			if !strings.HasPrefix(text, "/") {
				continue
			}
			updateChatID := u.Message.Chat.ID
			// Apply the bootstrap-admin fallback: if the chat_id
			// matches the configured admin chat, force IsAdmin
			// even without a binding row. This keeps the legacy
			// single-admin deploy (chat_id configured but no
			// telegram_bindings row) working.
			effectiveChatID, isBootstrapAdmin := n.resolveBootstrapAdmin(updateChatID)
			env := n.env(effectiveChatID)
			if isBootstrapAdmin && !env.IsAdmin {
				env.IsAdmin = true
			}
			reply := HandleCommand(ctx, env, text)
			// Reply goes to the originating chat (not the configured
			// admin chat). If the chat is unbound, the reply is
			// generated but sent to the same chat — which works
			// for the bootstrap case (admin chat replies) and
			// degrades gracefully for unbound chats (the user
			// sees the "admin only" / "chat not bound" message).
			n.reply(token, updateChatID, reply)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(n.pollInt):
		}
	}
}

// resolveBootstrapAdmin checks whether the inbound chat_id matches
// the configured admin chat. When it does, the bot treats the
// message as coming from the bootstrap admin even without a row in
// telegram_bindings — preserving the original single-chat deploy.
//
// Returned effectiveChatID: 0 when the chat is unbound AND not the
// admin chat (so HandleCommand sees "no identity"). The bootstrap
// admin chat keeps its real id so env() does a normal lookup
// (which returns ErrTelegramBindingNotFound; env() then leaves the
// BotEnv unidentified; Run() re-flags IsAdmin=true on the way
// back).
func (n *RealNotifier) resolveBootstrapAdmin(chatID int64) (int64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, adminChatID, ok, err := db.LoadTelegramToken(n.db)
	if err != nil || !ok {
		return 0, false
	}
	// adminChatID is stored as a string in global_settings;
	// compare as int64.
	var adminID int64
	if _, err := fmt.Sscanf(adminChatID, "%d", &adminID); err != nil {
		return 0, false
	}
	if chatID == adminID {
		return chatID, true
	}
	// Look up the binding; if the chat is bound, return its real id.
	if b, err := db.GetTelegramBinding(n.db, chatID); err == nil {
		return b.ChatID, b.IsAdmin
	}
	// Unbound, non-admin chat: tell the dispatcher to treat as
	// unidentified (so admin-only commands return "chat not bound"
	// instead of being implicitly allowed).
	return 0, false
}

func (n *RealNotifier) fetch(token string, offset int64) ([]update, error) {
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/getUpdates"
	args := ""
	if offset > 0 {
		args = fmt.Sprintf("?offset=%d&timeout=0", offset)
	}
	resp, err := n.client.Get(endpoint + args)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var out struct {
		OK     bool     `json:"ok"`
		Result []update `json:"result"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates: not ok: %s", string(rb))
	}
	return out.Result, nil
}

func (n *RealNotifier) reply(token string, chatID int64, text string) {
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/sendMessage"
	payload := map[string]any{
		"chat_id":                chatID,
		"text":                   text,
		"disable_web_page_preview": true,
	}
	body, _ := json.Marshal(payload)
	resp, err := n.client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("telegram: reply POST failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}
