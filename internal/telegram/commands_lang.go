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
func langReply(env BotEnv, args []string) string {
	lang := env.Lang

	// No args: report the current language. We use the
	// user-friendly name (русский / English) instead of the
	// internal code so the response is self-explanatory.
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
		return i18n.Tf(lang, "bot.lang.current", name)
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
