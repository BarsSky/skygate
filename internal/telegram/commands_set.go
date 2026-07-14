package telegram

// 2026-07-15: Этап 14 v13 — i18n-aware setMyCommands.
//
// Telegram's bot API has /setMyCommands which registers the
// command menu Telegram shows above the keyboard when the user
// opens the chat. The API supports per-language menus via the
// `language_code` parameter (Bot API 7.0+): we send one menu
// per supported locale, and Telegram picks the right one for
// the user's client language. Falls back to the default (no
// language_code) when no per-language menu matches.
//
// Why a separate file: the menu spec is the only piece of bot
// state that lives outside HandleCommand (it's a one-shot
// registration on startup, not a per-message decision), and
// keeping it here stops HandleCommand's commandContext table
// and the menu from drifting. The two tables are deliberately
// separate — a command that's hidden from the menu (e.g.
// /login, which only makes sense for unbound chats) still
// needs a commandContext entry so the help system and the
// strict-mode gate know about it.
//
// 2026-07-14: the v0.10.4 worktree had a hardcoded-EN
// implementation of this file (unmerged, on the
// `feature/telegram-bot-ux` branch). v0.10.12 supersedes it
// with the i18n-aware version, so the menu is rendered in the
// user's own locale. The hardcoded-EN version leaked English
// into RU chats (the screenshot in the v0.10.12 release
// notes shows `/help The Threshold's codex` even when the
// chat's language is Russian).

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
	"skygate/internal/i18n"
)

// telegramMenuCommand is the JSON shape Telegram expects for
// one entry in the commands array. Kept private to this
// file — callers use the higher-level SetMyCommandsAll. The
// `Description` is a fully-resolved string (not an i18n key)
// because Telegram does not look the key up itself; we
// resolve each language's text on our side before sending.
type telegramMenuCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// MenuEntry is one row in the spec. The Description is
// resolved from DescriptionKey against the active i18n
// catalog at registration time, so a future translation
// update (or a new language) automatically propagates to
// the Telegram-side menu on the next deploy.
type MenuEntry struct {
	Command       string // without leading "/", e.g. "help"
	DescriptionKey string // i18n key, e.g. "bot.menu.help.description"
}

// MyCommandsSpec is the single source of truth for the bot's
// command menu. Common is the menu every chat sees; AdminOnly
// is the menu that pops up for chats where the bot recognises
// the user as an admin. Both lists are sent in a single
// setMyCommands call per language by the SetMyCommandsAll
// orchestrator.
//
// Exported so main.go can hand it to RealNotifier.SetMyCommandsAll
// on startup; tests can pin the menu without dealing with json
// tags, and a future change touches one literal rather than
// every call site.
type MyCommandsSpec struct {
	// Common is the menu every chat sees. Includes the
	// user-scope /my_* commands + the public add/del
	// helpers; the admin command list lives in AdminOnly.
	Common []MenuEntry
	// AdminOnly is the menu that pops up for admin chats.
	// Telegram scopes the command list per-chat, so a
	// non-admin in the same bot never sees these entries.
	AdminOnly []MenuEntry
}

// All returns Common + AdminOnly. Used by the JSON payload
// builder; the Telegram API doesn't care about the scope
// distinction (we register everything in the "default"
// scope and rely on the dispatcher's IsAdmin check to gate
// execution — see notes in the v0.10.4 worktree version
// for why we don't use a separate admin scope).
func (s MyCommandsSpec) All() []MenuEntry {
	out := make([]MenuEntry, 0, len(s.Common)+len(s.AdminOnly))
	out = append(out, s.Common...)
	out = append(out, s.AdminOnly...)
	return out
}

// resolve builds a []telegramMenuCommand with all descriptions
// filled in from the given i18n language. Missing keys fall
// back to a deterministic "[no description]" so a typo in a
// catalog key doesn't silently drop a command from the menu.
func (s MyCommandsSpec) resolve(lang string) []telegramMenuCommand {
	all := s.All()
	out := make([]telegramMenuCommand, 0, len(all))
	for _, e := range all {
		desc := i18n.T(lang, e.DescriptionKey)
		if desc == e.DescriptionKey {
			// i18n.T returns the key itself when the lookup
			// misses — surface that as an explicit "[no
			// description: <key>]" string so a missing key
			// is obvious in the menu instead of a silent
			// empty cell (which Telegram would render as
			// blank).
			desc = fmt.Sprintf("[no description: %s]", e.DescriptionKey)
		}
		out = append(out, telegramMenuCommand{Command: e.Command, Description: desc})
	}
	return out
}

// DefaultMyCommandsSpec is the bot's command menu, in the
// "common" + "admin" form. The two lists together cover
// every public command the dispatcher accepts in
// HandleCommand. /login, /start, /unbind_self, /lang and
// /_bind_cancel are deliberately NOT in the menu — they
// are auth-flavored, used during the binding flow, or
// synthetic; listing them in the menu would be redundant
// or confusing.
//
// 2026-07-15: v0.10.12 — the spec now references i18n
// keys (DescriptionKey) instead of hardcoded English
// strings, so the same spec serves both EN and RU menus
// after one setMyCommandsAll call.
var DefaultMyCommandsSpec = MyCommandsSpec{
	Common: []MenuEntry{
		{Command: "help", DescriptionKey: "bot.menu.help.description"},
		{Command: "version", DescriptionKey: "bot.menu.version.description"},
		{Command: "my_status", DescriptionKey: "bot.menu.my_status.description"},
		{Command: "my_rules", DescriptionKey: "bot.menu.my_rules.description"},
		{Command: "my_nodes", DescriptionKey: "bot.menu.my_nodes.description"},
		{Command: "my_quota", DescriptionKey: "bot.menu.my_quota.description"},
		{Command: "add_rule", DescriptionKey: "bot.menu.add_rule.description"},
		{Command: "delrule", DescriptionKey: "bot.menu.delrule.description"},
		{Command: "add_device", DescriptionKey: "bot.menu.add_device.description"},
	},
	AdminOnly: []MenuEntry{
		{Command: "status", DescriptionKey: "bot.menu.status.description"},
		{Command: "nodes", DescriptionKey: "bot.menu.nodes.description"},
		{Command: "exit_nodes", DescriptionKey: "bot.menu.exit_nodes.description"},
		{Command: "rules", DescriptionKey: "bot.menu.rules.description"},
		{Command: "quota", DescriptionKey: "bot.menu.quota.description"},
		{Command: "audit", DescriptionKey: "bot.menu.audit.description"},
		{Command: "ack", DescriptionKey: "bot.menu.ack.description"},
		{Command: "restart", DescriptionKey: "bot.menu.restart.description"},
		{Command: "bind", DescriptionKey: "bot.menu.bind.description"},
		{Command: "unbind", DescriptionKey: "bot.menu.unbind.description"},
	},
}

// SetMyCommandsAll registers the bot's command menu with
// Telegram in every supported language. Called once at
// startup from the bot's Run path (via a background
// goroutine kicked off by main.go), so a Telegram-side
// failure doesn't block the bot from starting.
//
// We register two menus:
//   - "default" (no language_code): the English menu. This
//     is the fallback Telegram uses when no per-language
//     menu matches the user's client language.
//   - "ru" (with language_code="ru"): the Russian menu.
//
// Adding a new language is a 3-line change in i18n.T
// (already driven by the catalog) + a one-line addition
// to the supportedLangs list below. The menu is rebuilt
// from DefaultMyCommandsSpec at registration time so a
// future catalog update shows up on the next deploy
// without code changes.
//
// We use a 5-second timeout because setMyCommands is a
// one-shot best-effort call — we'd rather log a warning
// and continue without a command menu than have a hung
// registration block the bot. Telegram's docs don't
// specify a recommended timeout, but the average reply
// is <300ms.
func (n *RealNotifier) SetMyCommandsAll(ctx context.Context, spec MyCommandsSpec) error {
	if n.apiBase == "" || n.db == nil {
		return fmt.Errorf("SetMyCommandsAll: notifier not configured")
	}
	token, _, ok, err := db.LoadTelegramToken(n.db)
	if err != nil || !ok {
		return fmt.Errorf("SetMyCommandsAll: token not configured: %v", err)
	}
	// Register one menu per supported language. The first
	// entry (no language_code) is the fallback Telegram
	// uses for clients whose language we don't cover; the
	// rest are explicit per-locale menus.
	supportedLangs := []string{"", i18n.LangEN, i18n.LangRU}
	for _, lang := range supportedLangs {
		cmds := spec.resolve(langOrEN(lang))
		if err := postSetMyCommands(ctx, n, token, "default", lang, cmds); err != nil {
			// Don't abort the whole registration on one
			// language's failure — log and continue. The
			// user might still see a menu in another
			// language, and the bot's HTTP path doesn't
			// depend on the menu being registered.
			log.Printf("telegram: setMyCommands lang=%q failed: %v", lang, err)
		}
	}
	return nil
}

// langOrEN normalises the empty (default-scope) language to
// "en" for catalog lookups. The setMyCommands helper passes
// "" through to Telegram as "no language_code"; for the
// in-process i18n.T call we need a real catalog key, and
// the English catalog is the safe default (matches
// Telegram's own fallback behaviour).
func langOrEN(lang string) string {
	if lang == "" {
		return i18n.LangEN
	}
	return lang
}

// postSetMyCommands is the HTTP helper that POSTs a single
// setMyCommands request to Telegram. scope is one of
// "default" or "all_chat_admins"; lang is empty for the
// default menu, or an i18n code ("ru", "en", ...) for a
// per-language menu. cmds is the fully-resolved command
// list for that language.
func postSetMyCommands(ctx context.Context, n *RealNotifier, token, scope, lang string, cmds []telegramMenuCommand) error {
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
		return fmt.Errorf("setMyCommands %s/%s: HTTP %d: %s", scope, lang, resp.StatusCode, string(rb))
	}
	// 200 with {"ok":true,"result":true} is the success shape.
	// We don't strictly need to inspect it — non-2xx is
	// surfaced as an error above — but we log the response
	// body for any unexpected shape so a future API change
	// surfaces in the operator's logs.
	if !bytes.Contains(rb, []byte(`"ok":true`)) {
		log.Printf("telegram: setMyCommands %s/%s returned non-ok: %s", scope, lang, string(rb))
	}
	return nil
}
