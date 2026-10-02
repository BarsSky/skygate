package telegram

import (
	"fmt"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"strconv"
)

func myStatusReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML so the <b>label:</b>
	// and <code>value</code> in Field()/Section() render
	// instead of showing as literal text. Without this,
	// Telegram sends the body as plain text and the user
	// sees the raw source.
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.my_status.not_bound")
	}
	if env.Username == "" {
		return i18n.T(lang, "bot.my_status.no_username")
	}
	var ruleCount, deviceCount int64
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = $1`, env.PortalUserID).Scan(&ruleCount); err != nil {
		return i18n.Tf(lang, "bot.my_status.db_error", err)
	}
	// 2026-07-12: Этап 10 part 4 — count of owned devices derived
	// from db.ListNodeOwnersByUsername. We use the full row list
	// (rather than a separate COUNT query) so the helper stays a
	// single source of truth; the slice is tiny (a user's devices)
	// so the cost is negligible.
	owned, _ := db.ListNodeOwnersByUsername(env.DB, env.Username)
	deviceCount = int64(len(owned))
	var lastACL int64
	_ = env.DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM acl_snapshots WHERE applied_success = 1`).Scan(&lastACL)

	cap := env.MaxFor(env.Username)
	capStr := "∞"
	if cap > 0 {
		capStr = strconv.Itoa(cap)
	}
	_ = capStr
	// 2026-07-16: v0.16.x — Field() helper for aligned
	// key/value pairs (label bold, value in inline
	// <code>). The label is the short noun from
	// bot.my_status.label_* (no format spec); the value
	// is the count + cap, which sits in <code> so it
	// looks tabbed against the label.
	rulesLine := fmt.Sprintf("%d / %s", ruleCount, capStr)
	lastACLS := fmt.Sprintf("#%d", lastACL)
	deviceS := fmt.Sprintf("%d", deviceCount)
	return i18n.Tf(lang, "bot.my_status.header", env.Username) + "\n\n" +
		Section(i18n.T(lang, "bot.my_status.section_summary")) + "\n" +
		Field(i18n.T(lang, "bot.my_status.label_rules"), rulesLine) + "\n" +
		Field(i18n.T(lang, "bot.my_status.label_devices"), deviceS) + "\n" +
		Field(i18n.T(lang, "bot.my_status.label_last_acl"), lastACLS)
}

// myStatusBlocks is the Bot API 10.1 Rich Message variant of
// myStatusReply. Same data, structured as native <h2>
// sections + a key/value <table> (instead of the flat
// <b>label:</b> <code>value</code> lines that didn't align
// on mobile). Callers go through SendStatus which picks
// SendRich (10.1 client) or falls back to myStatusReply
// (old parse_mode=HTML on <10.1 clients).
//
// 2026-08-25 (B186): the new shape is a section heading +
// a 2-col key/value table + a closing footer. The table
// is real, so mobile Telegram aligns the columns without
// the <pre>+manual-padding hack the old code needed.
