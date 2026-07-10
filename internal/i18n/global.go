package i18n

// Global catalog and language. Used by the template function `t`
// registered in internal/handlers/templates.go. The catalog is
// initialised once at app startup and read by every template parse
// that happens later. Each render call still resolves through the
// per-request Lang field, so concurrent requests get the right
// translation.
var (
	GlobalCatalog *Catalog
	GlobalLang    = LangRU
)

// SetGlobal installs the catalog so the template `t` function can
// find it. Called once from handlers.New.
func SetGlobal(c *Catalog) {
	GlobalCatalog = c
}
