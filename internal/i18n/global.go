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
