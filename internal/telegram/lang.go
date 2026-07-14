// 2026-07-14: Этап 14 v5 — bot i18n helpers.
//
// One source of truth for the "what language does this chat
// speak?" question. Three entry points the dispatcher needs:
//
//   1. LangForBinding(b) — read the language from a binding
//      row that the dispatcher just fetched. Zero DB cost;
//      the field is on the struct.
//
//   2. LangForChat(db, chatID) — read the language for an
//      unidentified chat that doesn't have a binding row
//      yet. Used by the dispatcher to pick the welcome
//      language for an unbound /start (the message itself
//      has a language_code; the binding row doesn't exist
//      yet because the user hasn't /login'd).
//
//   3. LangFromTelegramCode(code) — map Telegram's
//      message.from.language_code (a BCP-47 tag like
//      "ru", "en", "ru-RU", "en-US") to our two-language
//      vocabulary. Anything we don't ship falls back to
//      'en'; that's a UX choice the catalog supports (the
//      template funcmap also falls back to RU if the
//      requested key is missing, but here we only pick a
//      language, not a key).
//
// We deliberately don't support more than ru + en for now.
// The i18n catalog has both, and adding a third would mean
// translating every key in both directions. If the user
// population needs more later, the contract here is "one of
// the constants i18n.LangRU / i18n.LangEN", so the expansion
// is a one-line match.
//
// This file is bot-only; the web UI uses internal/i18n
// directly (cookie → Accept-Language → 'ru'). Bot and web
// have independent language settings on purpose: the user's
// Telegram UI language and the user's web-portal UI language
// are not always the same person (a sysadmin in Telegram,
// a customer on the web; or just different device profiles).

package telegram

import (
	"database/sql"
	"strings"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// LangForBinding returns the language stored in the binding
// row. Empty or nil → "en" (v0.33+ always sets the column,
// so this is the "field missing" defensive path). Any other
// value is passed through — the catalog is the only thing
// that knows what to render, and it already falls back to
// the key (or RU) when the lang is unknown. We deliberately
// don't normalize "fo" to "en" here so a future "fo"
// catalog would Just Work.
func LangForBinding(b *db.TelegramBinding) string {
	if b == nil || b.Lang == "" {
		return i18n.LangEN
	}
	return b.Lang
}

// LangForChat reads the language for a chat from the DB.
// Used when the chat has no binding row yet (e.g. an
// unidentified /start, where we still want to greet the
// user in their own language even though we have no row
// to read it from). The function prefers the persisted
// value (which is empty for unbound chats) and falls
// back to "en". To get the auto-detected language for an
// unbound chat, the caller should pass
// LangFromTelegramCode via the message's language_code
// and then call this function as the override path.
func LangForChat(d *sql.DB, chatID int64) string {
	b, err := db.GetTelegramBinding(d, chatID)
	if err != nil {
		return i18n.LangEN
	}
	return LangForBinding(b)
}

// LangFromTelegramCode maps a BCP-47 language tag to one
// of our two supported languages. The match is on the
// primary subtag (the part before the first '-' or '_')
// so "ru-RU", "ru_RU" and "ru" all collapse to ru. We
// don't try to be clever with regional variants
// ("en-GB" → en) — every variant of English goes to the
// same catalog. An unknown primary subtag (e.g. "de",
// "fr", "zh") falls back to 'en' so the bot still
// renders, just not in the user's language.
func LangFromTelegramCode(code string) string {
	if code == "" {
		return i18n.LangEN
	}
	// Trim and lowercase; we don't ToTitle a BCP-47 tag.
	tag := strings.ToLower(strings.TrimSpace(code))
	// Strip the region subtag: "ru-RU" → "ru".
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case "ru", "uk", "be", "kk": // CIS-cluster: Russian + близкие
		// We don't ship Ukrainian/Belarusian/Kazakh catalogs
		// (yet). Until we do, route them to Russian — close
		// enough for the "warden of the threshold" voice,
		// which is mostly about politeness, not grammar that
		// diverges wildly between the three. When we add
		// those languages, this match grows.
		return i18n.LangRU
	case "en":
		return i18n.LangEN
	default:
		return i18n.LangEN
	}
}

// IsValidLang returns true when the string is one of the
// languages the bot ships catalogs for. Used by the
// SetTelegramBindingLang path to reject /lang fo before it
// reaches the DB (so the column always holds a renderable
// value, not a "fo" that would silently fall back to "en"
// on every read).
func IsValidLang(lang string) bool {
	switch lang {
	case i18n.LangRU, i18n.LangEN:
		return true
	}
	return false
}
