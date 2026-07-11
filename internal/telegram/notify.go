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

// env returns a BotEnv snapshot for HandleCommand. The DB pointer
// is the same one we already hold; the limits are read under the
// mu lock so a future SetLimits call mid-poll doesn't tear the map.
func (n *RealNotifier) env() BotEnv {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Copy the map so HandleCommand can't observe a concurrent
	// SetLimits. The map is tiny (one entry per user) so the cost
	// is negligible.
	max := make(map[string]int, len(n.userMaxRules))
	for k, v := range n.userMaxRules {
		max[k] = v
	}
	return BotEnv{DB: n.db, UserMaxRules: max, DefaultMax: n.defaultMax, Version: n.version}
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
func (n *RealNotifier) Run(ctx context.Context) {
	offset := int64(0)
	backoff := n.pollInt
	for {
		if ctx.Err() != nil {
			return
		}
		token, chatID, ok, err := db.LoadTelegramToken(n.db)
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
			reply := HandleCommand(ctx, n.env(), text)
			n.reply(token, chatID, reply)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(n.pollInt):
		}
	}
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

func (n *RealNotifier) reply(token, chatID, text string) {
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
