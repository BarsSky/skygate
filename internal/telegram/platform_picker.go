package telegram

// 2026-07-14: Этап 14 v10 — /add_device platform picker.
//
// After /add_device issues a preauth key, the user needs to know
// how to use it on their device. The web UI is "open the URL,
// click here, type here" — the bot doesn't have a clickable form,
// so the next best thing is an inline-keyboard prompt:
//
//   [🐧 Linux]  [⊞ Windows]  [🍎 macOS]
//   [📱 iOS]    [🤖 Android]
//
// When the user taps a platform, the callback handler in notify.go
// (handleCallback) routes the callback_data to
// renderPlatformInstructions, which looks up the i18n key
// `bot.add_device.platform.<platform>` (e.g. `linux`) and sends
// the per-platform instructions back. The instructions include
// the exact `tailscale up` command line so the user can copy-
// paste it into the device's terminal.
//
// The platform picker itself (the inline keyboard) is set via
// pendingReplyForCurrentMessage, the same side-channel the
// /start <token> confirmation prompt uses (see commands.go
// for the rationale).

import (
	"html"

	"skygate/internal/i18n"
)

// escapeHTML wraps html.EscapeString so reply helpers don't
// have to import "html" directly. Needed because the bot's
// /add_device reply now ships with parse_mode=HTML
// (see buildPlatformPicker), and the substituted username
// can legally contain <, >, & in a future release — HTML
// parse_mode would then 400 with "can't parse entities".
// The preauth key is always [A-Za-z0-9_-] so escaping is
// belt-and-braces, not load-bearing.
func escapeHTML(s string) string { return html.EscapeString(s) }

// platformKey is the internal code for each supported install
// platform. Stored in the callback_data ("add_device_platform:<key>")
// and looked up against the i18n catalog.
type platformKey string

const (
	platformLinux   platformKey = "linux"
	platformWindows platformKey = "windows"
	platformMacOS   platformKey = "macos"
	platformIOS     platformKey = "ios"
	platformAndroid platformKey = "android"
)

// buildPlatformPicker constructs the inline-keyboard reply for
// /add_device. Three rows:
//
//   📋 Скопировать            ← (full-width, copy_text = preauth key)
//   🐧 Linux  ⊞ Windows  🍎 macOS
//   📱 iOS    🤖 Android
//
// The Copy button is the new addition (Этап 14 v12,
// 2026-07-14): Telegram Bot API v7.0+ supports a `copy_text`
// field on inline-keyboard buttons, which copies the value
// to the user's clipboard on tap. We use it for the preauth
// key so the user doesn't have to long-press the code block
// in the body to copy it. The copy_text value is bound at
// button-construction time (passed in as preauthKey).
//
// The platform buttons keep the v9 callback_data shape so
// handleCallback can route to the per-platform install
// instructions.
func buildPlatformPicker(lang, preauthKey string) *PendingReply {
	mkBtn := func(label, data string) map[string]any {
		return map[string]any{"text": label, "callback_data": data}
	}
	// The Copy button uses copy_text instead of callback_data —
	// tapping it just copies the preauth key to the clipboard,
	// no callback handler needed.
	//
	// 2026-07-15: v0.14.1 — Telegram Bot API 7.0+ requires
	// `copy_text` to be an OBJECT {"text": "..."}, not a
	// bare string. We were sending it as a string and
	// Telegram rejected the whole sendMessage with HTTP 400
	// "Field \"copy_text\" must be of type Object" — which
	// in turn made /add_device silently fail (sendPlain
	// dropped the response body before the v0.14.1 logging
	// fix landed). The button rows therefore need to be
	// [][]map[string]any (not [][]map[string]string) so the
	// inner copy_text map can be a typed object.
	copyBtn := map[string]any{
		"text":      "📋 " + i18n.T(lang, "bot.add_device.copy_button"),
		"copy_text": map[string]any{"text": preauthKey},
	}
	rows := [][]map[string]any{
		{copyBtn},
		{
			mkBtn("🐧 "+i18n.T(lang, "bot.platform.linux"), "add_device_platform:linux"),
			mkBtn("⊞ "+i18n.T(lang, "bot.platform.windows"), "add_device_platform:windows"),
			mkBtn("🍎 "+i18n.T(lang, "bot.platform.macos"), "add_device_platform:macos"),
		},
		{
			mkBtn("📱 "+i18n.T(lang, "bot.platform.ios"), "add_device_platform:ios"),
			mkBtn("🤖 "+i18n.T(lang, "bot.platform.android"), "add_device_platform:android"),
		},
	}
	// ParseMode=HTML so the <code>key</code> in the bot.add_device.ok
	// string is rendered as a monospace block (Telegram draws
	// it that way) and is easy to long-press on mobile.
	return &PendingReply{InlineKeyboard: rows, ParseMode: "HTML"}
}

// renderPlatformInstructions returns the per-platform install
// message for the just-issued preauth key. The platform string
// is one of "linux", "windows", "macos", "ios", "android"
// (matching the platformKey constants and the callback_data
// shape). Unknown platforms get the "platform.unknown"
// fallback.
//
// The body includes the full `tailscale up` command so the
// user can copy-paste it directly into the device's terminal.
// The HEADSCALE_URL placeholder is intentional — the user
// fills it in (their deployment URL is not in the bot's
// context; the env's headscale URL is a server-to-server
// detail that we don't surface to the user).
func renderPlatformInstructions(lang, platform, key string) string {
	i18nKey := "bot.add_device.platform." + platform
	if i18n.T(lang, i18nKey) == i18nKey {
		return i18n.T(lang, "bot.add_device.platform.unknown")
	}
	header := i18n.Tf(lang, "bot.add_device.platform.header", i18n.T(lang, "bot.platform."+platform))
	return header + "\n\n" + i18n.Tf(lang, i18nKey, key)
}
