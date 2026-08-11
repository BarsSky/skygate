package i18n

import "sync/atomic"

// Global catalog and language. Used by the template functions `t` /
// `tf` registered in internal/handlers/templates.go. The catalog is
// installed once at startup. GlobalLang is an atomic.Value so that
// renderWithLayout can publish the per-request language before
// ExecuteTemplate and the funcmap can read it concurrently without a
// data race.
var (
	GlobalCatalog *Catalog
	GlobalLang    atomic.Value // string — "ru" or "en"
)

func init() {
	GlobalLang.Store(LangRU)
}

// SetGlobal installs the catalog so the template `t` function can
// find it. Called once from handlers.New.
func SetGlobal(c *Catalog) {
	GlobalCatalog = c
}

// SetLang publishes the active language for template funcmap reads.
// Called from renderWithLayout / render on every request. atomic.Value
// guarantees that any in-flight ExecuteTemplate either sees the old
// value or the new one — never a torn read.
func SetLang(lang string) {
	if lang != LangRU && lang != LangEN {
		lang = LangRU
	}
	GlobalLang.Store(lang)
}

// T is a convenience wrapper around (*Catalog).T using the global
// catalog installed at startup. Callers that don't want to thread
// the catalog through their call chain (e.g. the Telegram bot's
// per-reply functions, which are dispatched via HandleCommand with
// only BotEnv) can call this directly.
//
// 2026-07-14: Этап 14 v5 — added for the bot i18n work. The web
// funcmap still uses GlobalCatalog directly (registered at startup
// in templates.go) so it sees the freshest possible data; this
// convenience function is for non-template callers.
//
// If GlobalCatalog is nil (e.g. in a test that didn't install it),
// T returns the key itself — same fallback behavior as
// (*Catalog).T for a missing key. That keeps tests fail-safe:
// a missing catalog produces a renderable string (the key) rather
// than a nil-pointer panic.
func T(lang, key string) string {
	if GlobalCatalog == nil {
		return key
	}
	return GlobalCatalog.T(lang, key)
}

// Tf is the printf-style variant of T. Same nil-catalog fallback
// as T.
func Tf(lang, key string, args ...any) string {
	if GlobalCatalog == nil {
		return key
	}
	return GlobalCatalog.Tf(lang, key, args...)
}
