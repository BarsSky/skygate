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
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
)

// Notifier is the interface used by code that wants to emit a message.
// Real impl talks to the Telegram API. Noop is a no-op (used when no
// token is configured).
type Notifier interface {
	SendTelegram(text string)
	// SendTelegramToChat is the explicit-target variant. Used by
	// /admin/telegram's "Send test" handler when the operator
	// hasn't yet set global_settings.telegram.chat_id but HAS
	// bound a chat via /start + [Bind] — in that case the test
	// message can still be sent to the bound chat, and we don't
	// want the handler to no-op just because the global chat_id
	// is empty.
	SendTelegramToChat(text string, chatID int64)
	// SendAlert posts text as a numbered alert (for /ack). Returns
	// the alert id, or 0 when the bot is not configured.
	SendAlert(text string) int64
	// BotUsernameCached returns the bot's @username, refreshing
	// from getMe at most once per botUsernameCacheTTL. Returns ""
	// if the token isn't configured yet. Used by the
	// loginHint / greetingForNewChat welcome to render a
	// tap-to-open link to the bot. Added in 2026-07-13
	// (Этап 13) for Bind-by-QR; the in-flow caller is
	// loginHint in commands_login.go. NoopNotifier returns "".
	BotUsernameCached() string
}

// NoopNotifier discards all messages. Used when no token is configured.
// SendAlert and SendTelegram* live in alerts.go (the SendAlert
// impl is owned by the alerts package; keeping it there avoids a
// package-boundary cycle with the Notifier interface). We only add
// the new methods here.
type NoopNotifier struct{}

func (NoopNotifier) SendTelegram(string)              {}
func (NoopNotifier) SendTelegramToChat(string, int64) {}
func (NoopNotifier) BotUsernameCached() string        { return "" }

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
	// headscale; /add_rule and /delrule trigger an ACL sync via
	// SetPolicy). nil is allowed — write commands guard explicitly
	// and return a clear "telegram not wired for writes" hint so the
	// existing read-only deploys keep working.
	HS *headscale.Client
	// 2026-07-13: Этап 11 part 2b — per-device + total rule caps,
	// set by main.go from config.Load(). Surfaced in BotEnv so
	// /add_rule can enforce them (mirrors the web form's
	// PostMyExitRule checks). Zero = "no cap" — same as
	// userMaxRules / defaultMax.
	maxRulesPerDevice int
	maxTotalRules     int

	// 2026-07-13: Этап 13 — bot's own @username (set by
	// getMe discovery, cached 1h). Used by the /my/telegram
	// page to render a QR code that opens Telegram with the
	// login token pre-filled. Empty = not yet discovered;
	// the page falls back to plain-text "send the key to the
	// bot" instructions in that case.
	BotUsername string

	// botUsernameFetchedAt is the unix-second timestamp of
	// the last successful getMe. Combined with
	// botUsernameCacheTTL, decides when to refresh.
	// 2026-07-13: Этап 13.
	botUsernameFetchedAt int64
}

// botUsernameCacheTTL is how long we cache the bot's username
// returned by getMe. The username is set at @BotFather time and
// changes only when the operator re-registers the bot — a 1-hour
// cache is plenty fresh. 2026-07-13: Этап 13.
const botUsernameCacheTTL = 3600

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
// (/add_device issues a real preauth key, /add_rule and /delrule
// trigger ACL sync). Called once at startup from
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

// SetRuleCaps stores the per-device and total rule caps used by
// /add_rule. 2026-07-13: Этап 11 part 2b. Called once at
// startup from cmd/skygate/main.go after config.Load(). Zero
// values mean "no cap" — /add_rule then skips the check (same
// convention as SetLimits).
func (n *RealNotifier) SetRuleCaps(maxPerDevice, maxTotal int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.maxRulesPerDevice = maxPerDevice
	n.maxTotalRules = maxTotal
}

// BotUsernameCached returns the bot's @username, refreshing
// from Telegram's getMe API at most once per
// botUsernameCacheTTL. Returns "" if the token isn't
// configured yet (the operator hasn't completed setup at
// /admin/telegram) OR if the last getMe call failed.
//
// This is the entry point the /my/telegram page uses to
// decide whether to render a QR code (when username is known)
// or fall back to plain-text "send the key to the bot"
// instructions (when it isn't).
//
// 2026-07-13: Этап 13 — Bind-by-QR.
func (n *RealNotifier) BotUsernameCached() string {
	n.mu.Lock()
	username := n.BotUsername
	fetchedAt := n.botUsernameFetchedAt
	n.mu.Unlock()
	// Cache hit.
	if username != "" && time.Now().Unix()-fetchedAt < botUsernameCacheTTL {
		return username
	}
	// Refresh. We don't return the error to the caller; the
	// web page handles the empty-string case the same way
	// regardless of WHY it's empty.
	token, _, ok, err := db.LoadTelegramToken(n.db)
	if err != nil || !ok {
		return ""
	}
	newUsername, err := n.fetchBotUsername(token)
	if err != nil || newUsername == "" {
		// Don't update the timestamp on failure — a fresh
		// page load retries immediately. This trades a bit
		// of latency on a misconfigured bot for prompt
		// recovery once the operator saves a token.
		return ""
	}
	n.mu.Lock()
	n.BotUsername = newUsername
	n.botUsernameFetchedAt = time.Now().Unix()
	n.mu.Unlock()
	return newUsername
}

// fetchBotUsername calls Telegram's getMe and returns the
// bot's @username (without the @ prefix). Used by
// BotUsernameCached for the cache-miss path.
func (n *RealNotifier) fetchBotUsername(token string) (string, error) {
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/getMe"
	resp, err := n.client.Get(endpoint)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("telegram getMe: not ok: %s", string(rb))
	}
	return out.Result.Username, nil
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
//
// 2026-07-13: Этап 12 — env() now also reads telegram.strict_mode
// from global_settings. We release n.mu before the DB read so a
// slow DB doesn't block the polling loop (and so the DB read
// doesn't hold a lock that another goroutine wants). The cost
// is a single extra SELECT per message; that is negligible
// against the network round-trip to api.telegram.org that
// already dominates per-message latency.
func (n *RealNotifier) env(chatID int64) BotEnv {
	n.mu.Lock()
	// Copy the map so HandleCommand can't observe a concurrent
	// SetLimits. The map is tiny (one entry per user) so the cost
	// is negligible.
	max := make(map[string]int, len(n.userMaxRules))
	for k, v := range n.userMaxRules {
		max[k] = v
	}
	env := BotEnv{
		DB:                 n.db,
		UserMaxRules:       max,
		DefaultMax:         n.defaultMax,
		Version:            n.version,
		ChatID:             chatID,
		HS:                 n.HS,
		MaxRulesPerDevice:  n.maxRulesPerDevice,
		MaxTotalRules:      n.maxTotalRules,
		Notifier:           n,
		// 2026-07-14: Этап 14 v5 — default Lang is "en";
		// envForMessage() overrides for unbound chats with
		// LangFromTelegramCode(langCode) and for bound chats
		// with the binding's stored lang.
		Lang: i18n.LangEN,
	}
	n.mu.Unlock()
	// Strict mode is read OUTSIDE the n.mu lock so a slow DB
	// doesn't starve other notifier calls. The flag is loaded
	// per-message so an operator toggle in /admin/telegram
	// takes effect on the next inbound update (typically <2s).
	env.StrictMode = db.LoadTelegramStrictMode(n.db)
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
	// 2026-07-14: Этап 14 v5 — pick the language from the
	// binding so the user's /lang choice sticks across
	// sessions and across devices (the binding is per-chat,
	// not per-user, so a phone and a laptop with two chats
	// each get their own preference; that's intentional —
	// the language is a UI choice, not an account choice).
	env.Lang = LangForBinding(b)
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

// envForMessage builds the BotEnv for an inbound message and
// applies the bootstrap-admin fallback. The dispatcher in
// Run() and handleCallback() call this with the actual
// chat_id of the message sender (NOT the "effective" chat_id
// from resolveBootstrapAdmin, which is 0 for unbound chats
// without a global chat_id match — passing 0 to env() would
// make every handler see env.ChatID=0 and refuse with
// "chat_id missing; contact admin"). The bootstrap-admin
// flag is still applied here independently of env.ChatID.
//
// 2026-07-14 (v0.10.3 fix): extracted from Run() /
// handleCallback() so the dispatch logic is testable in
// isolation. The previous v0.10.2 inline call passed
// effectiveChatID (= 0 for unbound non-bootstrap chats) and
// the bug surfaced when an operator ran /login <token>
// without having configured global_settings.telegram.chat_id:
// the bot received the message, dispatched to loginReply,
// which bailed on "if env.ChatID == 0 { return internal error }".
//
// 2026-07-14 (Этап 14 v5): added langCode param. env() reads
// the binding's lang column; for unbound chats we fall back
// to LangFromTelegramCode(langCode) so the very first /start
// (before any /login) greets the user in their Telegram
// client language. The langCode is also passed to HandleCommand
// via env.TelegramLangCode so loginReply can seed the
// binding's lang on first /login (auto-detect). Empty
// langCode (privacy setting hid the value) falls back to en.
//
// Returns the env and the isBootstrapAdmin flag (mostly for
// tests — production callers can ignore the bool).
func (n *RealNotifier) envForMessage(chatID int64, langCode string) (BotEnv, bool) {
	_, isBootstrapAdmin := n.resolveBootstrapAdmin(chatID)
	env := n.env(chatID)
	env.TelegramLangCode = langCode
	// Re-resolve env.Lang: env() always populates it from the
	// binding (or "en" when no binding), but for the unbound
	// case the binding path returns "en" — we want to use the
	// Telegram client language as a better default for the
	// first /start.
	if !env.IsIdentified() || env.PortalUserID == 0 {
		env.Lang = LangFromTelegramCode(langCode)
	}
	if isBootstrapAdmin && !env.IsAdmin {
		env.IsAdmin = true
	}
	return env, isBootstrapAdmin
}

// SendTelegram posts text to the configured chat_id. Silently no-ops
// if EITHER the token OR the chat_id is missing (we need both to
// sendMessage). Errors are logged but not returned, since this is
// fire-and-forget notification code; callers should not block on
// Telegram availability.
//
// 2026-07-13: switched from Configured() to LoadTelegramSendTarget
// so we correctly distinguish "polling" (token-only is enough) from
// "sending" (need both). Without this, Configured() returned true
// for a token-only config and SendTelegram would proceed with
// chatID="" → Telegram API returns 400.
func (n *RealNotifier) SendTelegram(text string) {
	if n == nil {
		return
	}
	token, chatIDStr, ok, err := db.LoadTelegramSendTarget(n.db)
	if err != nil || !ok {
		log.Printf("telegram: skip send: load err=%v ok=%v (need both token AND chat_id)", err, ok)
		return
	}
	// chat_id is stored as a string in global_settings (so it can
	// hold either a plain user id "12345" or a -100… supergroup id);
	// convert to int64 for the postToChat helper which mirrors
	// Telegram's API type.
	chatID, perr := strconv.ParseInt(chatIDStr, 10, 64)
	if perr != nil || chatID == 0 {
		log.Printf("telegram: skip send: chat_id %q is not a valid int64: %v", chatIDStr, perr)
		return
	}
	n.postToChat(token, chatID, text)
}

// SendTelegramToChat sends text to an explicit chat_id. It does NOT
// consult global_settings or the bindings table — the caller is
// responsible for choosing the target. Used by the /admin/telegram
// "Send test" handler as a fallback when global chat_id is empty
// but a bound chat exists.
//
// 2026-07-14 (Этап 14 v3 followup): the previous behaviour was
// "Send test" → no-op when global chat_id was empty, which left
// operators who had bound via /start+[Bind] but never pasted a
// chat_id in the form with no way to verify the bot was reachable
// from the web UI. SendTelegramToChat lets the handler iterate
// over telegram_bindings and reach the bound chats directly.
//
// If the bot token isn't configured (no token in global_settings)
// the call no-ops; the caller should check via a.Notifier.Configured()
// first. We log on failure (network / 4xx) but don't return an error
// — this matches the existing SendTelegram fire-and-forget style
// and keeps the test handler's audit/log shape simple.
func (n *RealNotifier) SendTelegramToChat(text string, chatID int64) {
	if n == nil {
		return
	}
	if chatID == 0 {
		// Defensive: 0 is the zero value for int64, and Telegram
		// returns 400 for chat_id=0. Bail rather than POST a
		// malformed request.
		log.Printf("telegram: SendTelegramToChat called with chatID=0; skipping")
		return
	}
	token, _, ok, err := db.LoadTelegramToken(n.db)
	if err != nil || !ok {
		log.Printf("telegram: SendTelegramToChat skip: load err=%v ok=%v (token not configured)", err, ok)
		return
	}
	n.postToChat(token, chatID, text)
}

// postToChat is the shared sendMessage implementation. Both
// SendTelegram and SendTelegramToChat funnel through here so the
// HTTP shape, error logging, and ``` wrapping stay in one place.
// Kept private — callers go through the public methods.
//
// chatID is taken as int64 (matching Telegram's chat_id type) and
// stringified inside via fmt.Sprintf. Using string here would force
// every caller to convert, and SendTelegramToChat already has the
// int64 from telegram_bindings.ChatID.
func (n *RealNotifier) postToChat(token string, chatID int64, text string) {
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
			// 2026-07-13: Этап 13 — inline-keyboard callbacks.
			// Callback queries are routed through the same
			// HandleCommand path as text commands; the helper
			// detects the "data:bind:..." prefix and switches
			// to callback semantics. We don't bail on
			// non-/command text here anymore: the callback
			// path also delivers via Message-bearing updates
			// (the inline button's "callback_data" sits in
			// CallbackQuery, not Message), so the text
			// prefix check belongs to the message branch
			// only.
			if u.CallbackQuery != nil {
				pendingReplyForCurrentMessage = nil
				n.handleCallback(token, u.CallbackQuery)
				continue
			}
			if u.Message == nil {
				continue
			}
			text := u.Message.Text
			if !strings.HasPrefix(text, "/") {
				continue
			}
			updateChatID := u.Message.Chat.ID
			updateLangCode := ""
			if u.Message.From != nil {
				updateLangCode = u.Message.From.LanguageCode
			}
			// 2026-07-14 (v0.10.3 fix): envForMessage uses
			// updateChatID (the actual chat_id of the message
			// sender) — NOT effectiveChatID. The previous code
			// passed the "effective" chat_id (0 for unbound
			// chats without a global chat_id match), causing
			// every handler to see env.ChatID=0 and refuse
			// with "login: internal error (chat_id missing)".
			// The bootstrap-admin fallback is still applied
			// inside envForMessage.
			//
			// 2026-07-14 (Этап 14 v5): also pass the
			// Telegram client language_code so the very
			// first /start (before /login) greets the
			// user in their client language, and so
			// loginReply can auto-detect the binding's
			// lang on first /login.
			env, _ := n.envForMessage(updateChatID, updateLangCode)
			pendingReplyForCurrentMessage = nil
			reply := HandleCommand(ctx, env, text)
			// Reply goes to the originating chat (not the configured
			// admin chat). If the chat is unbound, the reply is
			// generated but sent to the same chat — which works
			// for the bootstrap case (admin chat replies) and
			// degrades gracefully for unbound chats (the user
			// sees the "admin only" / "chat not bound" message).
			n.reply(token, updateChatID, reply, pendingReplyForCurrentMessage)
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

// reply posts `text` to `chatID` as a plain message. Errors are
// logged but not returned (this is fire-and-forget notification code;
// callers should not block on Telegram availability). For a real
// response the user's Telegram client renders the bot's reply.
//
// 2026-07-13: Этап 13 — accepts an optional *PendingReply to
// attach an inline-keyboard (or future rich-reply extras) to
// the message. nil = plain text. Callers that want a keyboard
// pass env.PendingReply; the rest pass nil.
func (n *RealNotifier) reply(token string, chatID int64, text string, pending *PendingReply) {
	n.sendPlain(token, chatID, text, pending)
}

// sendPlain is the shared POST /sendMessage implementation
// used by both the text-message path and the inline-keyboard
// callback path. Kept private so callers go through reply()
// (the only public entry point); handleCallback also calls
// it directly because it doesn't go through the public reply.
func (n *RealNotifier) sendPlain(token string, chatID int64, text string, pending *PendingReply) {
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/sendMessage"
	payload := map[string]any{
		"chat_id":                chatID,
		"text":                   text,
		"disable_web_page_preview": true,
	}
	if pending != nil && len(pending.InlineKeyboard) > 0 {
		payload["reply_markup"] = map[string]any{
			"inline_keyboard": pending.InlineKeyboard,
		}
	}
	body, _ := json.Marshal(payload)
	resp, err := n.client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("telegram: sendMessage failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

// handleCallback dispatches a callback_query (an inline
// button tap) to the right handler. We translate the
// callback data into a synthetic command and route through
// HandleCommand so the binding logic stays in one place. The
// callback acknowledgement (answerCallbackQuery) is sent
// first so the Telegram client dismisses the loading spinner;
// a follow-up sendMessage delivers the visible reply.
//
// 2026-07-13: Этап 13 — inline-keyboard confirmation for
// /start <token>. The user scans the QR or taps the deep
// link, lands in the chat with /start pre-filled, and the
// bot shows a [Bind] [Cancel] prompt instead of binding
// immediately. /login <token> still binds immediately
// (one-command shortcut for users who already know the flow).
func (n *RealNotifier) handleCallback(token string, cq *callbackQuery) {
	// 1. Acknowledge the callback so the loading spinner
	//    dismisses. Empty text = silent dismiss; we always
	//    send a follow-up sendMessage, so the ack just
	//    signals "I got it".
	n.ackCallback(token, cq.ID, "")
	if cq == nil || cq.Data == "" {
		return
	}
	data := cq.Data
	// 2. Resolve the chat_id from the callback envelope.
	//    Some Telegram clients put chat_id at the top level;
	//    others nest it under message.chat. We try both.
	chatID := cq.ChatID
	if chatID == 0 && cq.Message != nil {
		chatID = cq.Message.Chat.ID
	}
	if chatID == 0 {
		return
	}
	// 3. Translate the callback data into a synthetic
	//    command so HandleCommand's existing logic does
	//    the heavy lifting.
	var synthetic string
	switch {
	case data == "bind:cancel":
		synthetic = "/_bind_cancel"
	case strings.HasPrefix(data, "bind:confirm:"):
		tokenStr := strings.TrimPrefix(data, "bind:confirm:")
		synthetic = "/login " + tokenStr
	default:
		return
	}
	// 4. Build the same env the text-message path uses.
	// See the v0.10.3 fix in Run(): envForMessage uses the
	// actual chat_id (the chat that tapped the button), not
	// the "effective" chat_id from resolveBootstrapAdmin.
	//
	// 2026-07-14 (Этап 14 v5): callback_query carries
	// From.LanguageCode on the envelope (same field as
	// text messages), so we forward it to envForMessage
	// for the auto-detect pass on /login via [Bind].
	langCode := ""
	if cq.From != nil {
		langCode = cq.From.LanguageCode
	}
	env, _ := n.envForMessage(chatID, langCode)
	pendingReplyForCurrentMessage = nil
	reply := HandleCommand(context.Background(), env, synthetic)
	n.sendPlain(token, chatID, reply, pendingReplyForCurrentMessage)
}

// ackCallback posts answerCallbackQuery so the Telegram
// client dismisses the inline button's loading spinner.
// `text` (optional, ≤200 chars) is shown as a toast on the
// client. We pass empty text because we always send a
// follow-up sendMessage; the toast would be redundant.
func (n *RealNotifier) ackCallback(token, callbackID, text string) {
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/answerCallbackQuery"
	payload := map[string]any{
		"callback_query_id": callbackID,
	}
	if text != "" {
		payload["text"] = text
	}
	body, _ := json.Marshal(payload)
	resp, err := n.client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("telegram: answerCallbackQuery failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

type update struct {
	UpdateID int64 `json:"update_id"`
	// 2026-07-13: Этап 13 — inline-keyboard callback support.
	// CallbackQuery fires when the user taps a button under a
	// bot message; the bot answers via answerCallbackQuery
	// (separate API from sendMessage) and may edit the
	// original message via editMessageText. The polling loop
	// dispatches both shapes through the same HandleCommand
	// path so the command/binding logic stays in one place.
	CallbackQuery *callbackQuery `json:"callback_query"`
	Message       *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		// 2026-07-14: Этап 14 v5 — bot i18n. From.LanguageCode
		// is the user's Telegram client language (BCP-47). We
		// forward it to HandleCommand via BotEnv.TelegramLangCode
		// so the auto-detect path can seed the binding's lang
		// on first /login. Empty string for users who hid their
		// language preference in Telegram's privacy settings.
		From *struct {
			LanguageCode string `json:"language_code"`
		} `json:"from"`
	} `json:"message"`
}

// callbackQuery is the subset of Telegram's callback_query
// payload we read. We use a flat data field (the inline
// button's "callback_data" string) and the originating
// message's chat for routing — the inline message ID is
// optional (we edit it to acknowledge the action).
//
// 2026-07-14 (Этап 14 v5): From.LanguageCode is forwarded
// to envForMessage so the auto-detect works on bind via
// the [Bind] inline button as well as via /login <key>.
// (Previously we passed "" here, which forced a /lang
// after the inline-button bind; the From field is on
// every callback_query envelope so we can pick it up.)
type callbackQuery struct {
	ID      string `json:"id"`
	Data    string `json:"data"`
	ChatID  int64  `json:"chat_id"` // populated by some
	// 2026-07-14 (Этап 14 v5): From is the same shape as
	// update.Message.From — Telegram sends the originating
	// user's language_code on every callback_query so the
	// bot can localise its responses without a separate
	// getMe call.
	From *struct {
		LanguageCode string `json:"language_code"`
	} `json:"from"`
	Message *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		MessageID int64 `json:"message_id"`
	} `json:"message"`
}
