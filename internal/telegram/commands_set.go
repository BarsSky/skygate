package telegram

// 2026-07-14: Этап 14 v4 — setMyCommands on bot startup.
//
// Telegram's bot API has a /setMyCommands endpoint that
// registers the command menu (the auto-suggest list Telegram
// shows above the keyboard when the user opens the chat).
// Without it, the user has to type every command from
// memory; with it, they tap the menu icon and pick.
//
// We set two scopes:
//   - default (the menu every chat sees) — top-level
//     commands: /help, /version, /my_status, /add_rule,
//     /delrule, /add_device
//   - "all_chat_admins" — admin-only commands: /status,
//     /nodes, /exit_nodes, /rules, /quota, /audit, /ack,
//     /restart, /bind, /unbind
//
// /login, /start, /unbind_self are deliberately NOT in the
// menu — they're either shown on first /start (the binding
// flow), or are auth-flavored commands that the user
// already has the context for. Listing /login in the menu
// would be redundant ("you wouldn't tap login unless you
// already knew what it was"). Same for /unbind_self.
//
// 2026-07-14: docstring above is the v0.10.4 design
// rationale. The shape of the JSON is the standard Telegram
// Bot API "setMyCommands" payload: { commands: [
// { command: "help", description: "..." }, ... ] }.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"skygate/internal/db"
)

// telegramCommand is the JSON shape Telegram expects for
// one entry in the commands array. Kept private to this
// file — callers use the higher-level SetMyCommands.
type telegramCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// myCommandsSpec holds the default + admin scope command
// lists. Both lists are sent in a single setMyCommands call
// by passing them in the scope-keyed wrapper that the
// Telegram API documents:
//
//   POST /bot<token>/setMyCommands
//   {
//     "commands": [...],
//     "scope": {"type": "default"},
//     "language_code": "en"
//   }
//
// and again for "all_chat_admins". The HTTP helper in this
// file builds both payloads and POSTs them in order.
type myCommandsSpec struct {
	// Default is the menu every chat sees.
	Default []telegramCommand
	// AdminOnly is the menu that pops up for chats where the
	// bot recognises the user as an admin. Telegram scopes
	// the command list per-chat, so a non-admin in the
	// same bot never sees the admin commands.
	AdminOnly []telegramCommand
}

// DefaultMyCommandsSpec is the single source of truth for
// the bot's command menu. Exported so main.go can hand it
// to RealNotifier.SetMyCommands on startup; tests can pin
// the menu without dealing with json tags, and a future
// change touches one slice literal rather than three
// call sites.
var DefaultMyCommandsSpec = myCommandsSpec{
	Default: []telegramCommand{
		{Command: "help", Description: "The Threshold's codex (list every command)"},
		{Command: "version", Description: "Build label, Go runtime, DB schema level"},
		{Command: "my_status", Description: "Your own summary: rules, devices, last ACL"},
		{Command: "my_rules", Description: "Your own exit-rules (newest first)"},
		{Command: "my_nodes", Description: "Your own tailnet devices"},
		{Command: "my_quota", Description: "Your rule count vs cap (progress bar)"},
		{Command: "add_rule", Description: "Add an exit-rule (domain, IP, or subnet)"},
		{Command: "delrule", Description: "Delete one or more of your rules by id"},
		{Command: "add_device", Description: "Issue a 1h single-use preauth key for yourself"},
	},
	AdminOnly: []telegramCommand{
		{Command: "status", Description: "System summary: rules, users, last ACL"},
		{Command: "nodes", Description: "List all tailnet devices by user+tag"},
		{Command: "exit_nodes", Description: "List exit-nodes with last-seen"},
		{Command: "rules", Description: "Recent exit-rules across all users"},
		{Command: "quota", Description: "Per-user rule count vs cap (admin view)"},
		{Command: "audit", Description: "Last 20 audit_log entries"},
		{Command: "ack", Description: "Acknowledge an alert (id from [#N] prefix)"},
		{Command: "restart", Description: "Graceful container restart (requires confirm)"},
		{Command: "bind", Description: "Bind a chat to a portal user"},
		{Command: "unbind", Description: "Remove a chat binding"},
	},
}

// SetMyCommands registers the bot's command menu with
// Telegram. Called once at startup from the bot's Run path
// (via a background goroutine kicked off by main.go), so a
// Telegram-side failure doesn't block the bot from starting
// polling.
//
// We use a 5-second timeout because setMyCommands is a
// one-shot best-effort call — we'd rather log a warning and
// continue without a command menu than have a hung
// registration block the bot. Telegram's docs don't specify
// a recommended timeout, but the average reply is <300ms.
//
// v0.10.4 design note: we use the "default" scope only.
// The "all_chat_admins" scope would let us hide the admin
// commands from non-admin users, but as of Telegram Bot API
// 7.x the scope type "all_chat_admins" returns
// "can't parse BotCommandScope: Unsupported type specified"
// for bots that aren't admin in any chat. Since we don't
// have a way to discover that up-front, the safer choice
// is to register all commands in the default scope and rely
// on the dispatcher's IsAdmin check to gate the actual
// execution. (The /help text marks admin commands with
// ✦ so a user can still see at a glance that a command is
// wardens-only.) If/when Telegram fixes the scope API, we
// can split the menu.
func (n *RealNotifier) SetMyCommands(ctx context.Context, spec myCommandsSpec) error {
	if n.apiBase == "" || n.db == nil {
		return fmt.Errorf("SetMyCommands: notifier not configured")
	}
	token, _, ok, err := db.LoadTelegramToken(n.db)
	if err != nil || !ok {
		return fmt.Errorf("SetMyCommands: token not configured: %v", err)
	}

	// Single scope: default. We merge the admin list into
	// the default list so the menu is one register call
	// and one menu visible to everyone. See the function
	// comment above for why we don't use a separate
	// admin scope.
	merged := make([]telegramCommand, 0, len(spec.Default)+len(spec.AdminOnly))
	merged = append(merged, spec.Default...)
	merged = append(merged, spec.AdminOnly...)
	if err := postSetMyCommands(ctx, n, token, "default", "", merged); err != nil {
		return fmt.Errorf("set default: %w", err)
	}
	return nil
}

// postSetMyCommands is the HTTP helper that POSTs a single
// setMyCommands request to Telegram. scope is one of
// "default" or "all_chat_admins"; lang is empty for now
// (Telegram's per-language commands list is a future
// enhancement — for v0.10.4 we set the English menu and
// let Telegram fall back to it for other locales).
func postSetMyCommands(ctx context.Context, n *RealNotifier, token, scope, lang string, cmds []telegramCommand) error {
	if len(cmds) == 0 {
		return nil // nothing to register for this scope
	}
	payload := map[string]any{
		"commands": cmds,
		"scope":    map[string]string{"type": scope},
	}
	if lang != "" {
		payload["language_code"] = lang
	}
	body, _ := json.Marshal(payload)
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/setMyCommands"

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("setMyCommands %s: HTTP %d: %s", scope, resp.StatusCode, string(rb))
	}
	// 200 with {"ok":true,"result":true} is the success shape.
	// We don't strictly need to inspect it — non-2xx is
	// surfaced as an error above — but we log the response
	// body for any unexpected shape so a future API change
	// surfaces in the operator's logs.
	if !bytes.Contains(rb, []byte(`"ok":true`)) {
		log.Printf("telegram: setMyCommands %s returned non-ok: %s", scope, string(rb))
	}
	return nil
}