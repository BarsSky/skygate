// 2026-07-14: Этап 14 v5 — /lang command implementation.
//
// The command takes one optional argument:
//
//	/lang           — show the chat's current language
//	/lang ru        — switch to Russian (persists in telegram_bindings)
//	/lang en        — switch to English (persists in telegram_bindings)
//	/lang anything  — reject with a usage hint
//
// The persistence path is db.SetTelegramBindingLang (added in
// the same v0.33 migration as the lang column itself). The next
// HandleCommand after a /lang <x> call reads the binding's new
// lang via envForMessage → env() → db.GetTelegramBinding.
//
// /lang is open to any chat (no admin gate, no strict-mode lock).
// An unidentified chat that issues /lang <x> writes the choice
// to the binding row that /login will create later — but a
// binding row doesn't exist yet, so the write is a no-op until
// the user /login's. We surface a note explaining that the
// choice will apply once they bind, so the user isn't confused
// when /start still renders in the auto-detected language.

package telegram

import (
	"strings"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// langReply is the user-scope reply for /lang. It does NOT require
// a bound chat (the choice is per-chat and the dispatcher always
// knows the chat_id from the inbound update), but the persisted
// choice only sticks when the chat is bound.
//
// 2026-07-15: v0.14.0 — the no-args case now attaches a
// 2-button inline keyboard (RU / EN) so the user can
// switch language with one tap instead of typing
// "/lang ru" by hand. The callback handler in notify.go
// applies the change and re-renders the same /lang reply
// in the new language.
func langReply(env BotEnv, args []string) string {
	lang := env.Lang

	// No args: report the current language + attach the
	// language picker. The user can tap a button to switch.
	if len(args) == 0 {
		// env.Lang is "ru" / "en" — render the human-readable
		// name by reversing the LangFromTelegramCode map (we
		// don't keep one, so a tiny inline switch is fine).
		name := env.Lang
		if env.Lang == i18n.LangRU {
			name = "русский"
		} else if env.Lang == i18n.LangEN {
			name = "English"
		} else {
			name = i18n.T(lang, "bot.lang.unknown")
		}
		body := i18n.Tf(lang, "bot.lang.current", name)
		// 2026-07-15: v0.14.0 — attach the picker.
		// PendingReply is consumed by the polling loop after
		// this returns and attached to the sendMessage
		// payload (see notify.go's reply path). The
		// callback_data uses the same shape as the
		// /add_device platform picker so handleCallback's
		// prefix-routing can dispatch both.
		pendingReplyForCurrentMessage = buildLangPicker(lang, env.Lang)
		return body
	}

	arg := strings.ToLower(strings.TrimSpace(args[0]))
	switch arg {
	case "ru", "russian", "русский":
		arg = i18n.LangRU
	case "en", "english", "английский":
		arg = i18n.LangEN
	}
	if !IsValidLang(arg) {
		return i18n.Tf(lang, "bot.lang.invalid", args[0]) + "\n" +
			i18n.T(lang, "bot.lang.usage")
	}

	// Persist only if the chat is bound. Unbound chats have
	// no row to update; the next /login will seed the lang
	// from this env (we set env.Lang from
	// LangFromTelegramCode which already includes the
	// dispatcher's hint).
	if env.IsIdentified() && env.PortalUserID > 0 {
		if err := db.SetTelegramBindingLang(env.DB, env.ChatID, arg); err != nil {
			return i18n.Tf(lang, "bot.login.db_error", err)
		}
	}
	return i18n.Tf(lang, "bot.lang.set_ok", arg)
}

// buildLangPicker returns the inline keyboard shown
// alongside the /lang reply. Two buttons (RU + EN) with
// callback_data "lang:ru" / "lang:en" — the callback
// handler in notify.go reads this and re-runs langReply
// (the no-args branch) on the new chat lang. The "current"
// language is rendered with a checkmark so the user can
// see at a glance which one is active.
//
// 2026-07-15: v0.14.0.
func buildLangPicker(lang, current string) *PendingReply {
	return buildLangPickerForLang(current, current)
}

// buildLangPickerForLang is the same as buildLangPicker but
// takes the "render-in" lang and the "current" lang as
// separate parameters. The notify.go callback path needs
// this (the picker is rendered in the new language; the
// checkmark is on the new active language). buildLangPicker
// is a thin wrapper for the langReply no-args path.
func buildLangPickerForLang(renderLang, current string) *PendingReply {
	mkBtn := func(label, data string) map[string]string {
		return map[string]string{"text": label, "callback_data": data}
	}
	check := "✓"
	noCheck := "  "
	ruLabel := noCheck + " " + i18n.T(renderLang, "lang.ru")
	enLabel := noCheck + " " + i18n.T(renderLang, "lang.en")
	if current == i18n.LangRU {
		ruLabel = check + " " + i18n.T(renderLang, "lang.ru")
	}
	if current == i18n.LangEN {
		enLabel = check + " " + i18n.T(renderLang, "lang.en")
	}
	rows := [][]map[string]string{
		{
			mkBtn(ruLabel, "lang:ru"),
			mkBtn(enLabel, "lang:en"),
		},
	}
	return &PendingReply{InlineKeyboard: rows}
}
