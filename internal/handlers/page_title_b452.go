// page_title_b452.go — B452 (2026-10-30). The rest of the page-title localization.
//
// B450 closed the case where a handler put a CATALOG KEY into the page title. What remained (and was
// listed there as debt) are the page titles written as ENGLISH LITERALS:
//
//	"Exit Nodes" · "Integration status" · "Service control" · "Tailscale" · "Telegram" ·
//	"Account" · "API Tokens" · "Exit Rules" · "Node Load" · "Notifications" · "Мониторинг"
//
// They are not keys, so the B450 guard does not flag them, and a Russian operator reads them in
// English in the browser tab — «не вся локализация покрыта».
//
// WHY ONE RESOLVER INSTEAD OF FIFTEEN HANDLER EDITS. Each of these handlers lives in a different
// package with a different way to reach the language (auth.Service, exit_rules.Service, the handler
// struct, a bare claims value). Rewriting fifteen call sites would have meant fifteen chances to get
// the language wrong, and the NEXT page would still write a literal. The layout is the single place
// that renders the title, so the translation lives there: `{{pt .Title}}` resolves
//
//  1. a catalogue key — the B450 defect, now also caught at render time;
//  2. a KNOWN literal from the table below — the pre-B452 debt, translated for RU readers;
//  3. anything else unchanged — a title that is data (a module name, a user name) is not ours to
//     translate, and guessing would be worse than leaving it.
//
// The table is deliberately a closed list of literals the tree actually passes, and a test asserts
// each of them maps to a key that exists in BOTH catalogues.
package handlers

import (
	"strings"

	"skygate/internal/i18n"
)

// pageTitleLiteralKeys maps page-data Title LITERALS to catalogue keys. Closed list: a literal that
// is not here renders unchanged, which is the honest default for titles that are data.
var pageTitleLiteralKeys = map[string]string{
	"Exit Nodes":          "title.admin_exit_nodes",
	"Integration status":  "title.admin_services",
	"Service control":     "title.admin_service_control",
	"Tailscale":           "title.admin_tailscale",
	"Telegram":            "title.admin_telegram",
	"Account":             "title.account",
	"API Tokens":          "title.my_tokens",
	"API token":           "title.my_tokens",
	"Exit Rules API Help": "title.exit_rules_help",
	"Cleanup exit rules":  "title.admin_exit_rules",
	"Exit Rules":          "title.admin_exit_rules",
	"My exit rules":       "title.my_exit_rules",
	"Node Load":           "title.node_load",
	"Notifications":       "title.notifications",
	"Мониторинг":          "title.admin_monitor",
}

// resolvePageTitle is the `pt` template helper. It reads the request language exactly like `t`
// does (i18n.GlobalLang is set by the renderer before ExecuteTemplate), so no handler has to
// thread a language through its page data.
func resolvePageTitle(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return value
	}
	lang, _ := i18n.GlobalLang.Load().(string)
	if key, ok := pageTitleLiteralKeys[raw]; ok {
		return i18n.GlobalCatalog.T(lang, key)
	}
	if looksLikeCatalogKey(raw) {
		// A handler passed the key itself (B450). Rendering the key is the bug; rendering its
		// translation here fixes the page even if a future handler forgets again.
		return i18n.GlobalCatalog.T(lang, raw)
	}
	return value
}

// looksLikeCatalogKey reports whether a title value is shaped like an i18n key: lowercase dotted
// segments and no spaces. «Exit Rules» and «Мониторинг» are not; «title.admin_update» is.
func looksLikeCatalogKey(s string) bool {
	if strings.ContainsAny(s, " \t") || !strings.Contains(s, ".") {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' {
				return false
			}
		}
	}
	return true
}
