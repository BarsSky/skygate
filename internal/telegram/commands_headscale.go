// 2026-07-20: v0.20.0 — /headscale bot command.
//
// Mirrors the /admin/headscale page: prints the
// operator's pinned headscale version, the latest
// GitHub release, the update/breaking flag, and a
// short history of the last 3 seen releases.
//
// Admin-only (gated in commands.go's adminOnly map).
//
// Reads from the headscale-update-monitor
// (BotEnv.HeadscaleUpdateMonitor), not directly
// from the DB or GitHub. If the monitor is not
// wired (e.g. SKYGATE_HEADSCALE_POLL_INTERVAL=0),
// the reply explains the state instead of failing.

package telegram

import (
	"fmt"
	"strings"

	"skygate/internal/headscale_version"
	"skygate/internal/i18n"
)

// headscaleReply is the formatter for /headscale.
// Output shape (RU):
//
//	📡 Headscale update monitor
//	Pinned:  0.29.2
//	Latest:  0.30.0
//	Status:  ⚠️ breaking change
//	URL:     github.com/juanfont/headscale/releases/tag/v0.30.0
//
//	Last seen:
//	  • 0.30.0 (2026-08-01) — ⚠️ breaking — notified
//	  • 0.29.2 (2026-06-18) — patch
//	  • 0.29.1 (2026-06-15) — patch
//
// If the monitor is nil:
//	📡 Headscale update monitor disabled
//	Set SKYGATE_HEADSCALE_VERSION_PIN and restart skygate
//	to enable it.
func headscaleReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML so the <b>label:</b>
	// Field() rendering works on mobile Telegram.
	markHTMLReply()
	lang := env.Lang

	if env.HeadscaleUpdateMonitor == nil {
		return i18n.Tf(lang, "bot.headscale.disabled",
			"SKYGATE_HEADSCALE_POLL_INTERVAL",
		)
	}
	latest, upd, brk, checkedAt, history, pinned := env.HeadscaleUpdateMonitor.Snapshot()

	var statusKey string
	switch {
	case pinned == "":
		statusKey = "bot.headscale.status_no_pin"
	case upd && brk:
		statusKey = "bot.headscale.status_breaking"
	case upd:
		statusKey = "bot.headscale.status_update"
	default:
		statusKey = "bot.headscale.status_current"
	}

	out := &strings.Builder{}
	fmt.Fprintf(out, "%s\n", i18n.T(lang, "bot.headscale.title"))
	if pinned == "" {
		fmt.Fprintf(out, "<b>%s</b> %s\n",
			i18n.T(lang, "bot.headscale.pinned_label"),
			i18n.T(lang, "bot.headscale.pinned_empty"))
	} else {
		fmt.Fprintf(out, "<b>%s</b> <code>%s</code>\n",
			i18n.T(lang, "bot.headscale.pinned_label"), pinned)
	}
	if latest.TagName == "" {
		fmt.Fprintf(out, "<b>%s</b> %s\n",
			i18n.T(lang, "bot.headscale.latest_label"),
			i18n.T(lang, "bot.headscale.latest_empty"))
	} else {
		fmt.Fprintf(out, "<b>%s</b> <code>%s</code>\n",
			i18n.T(lang, "bot.headscale.latest_label"), latest.TagName)
	}
	fmt.Fprintf(out, "<b>%s</b> %s\n",
		i18n.T(lang, "bot.headscale.status_label"),
		i18n.T(lang, statusKey))
	if !checkedAt.IsZero() {
		fmt.Fprintf(out, "<b>%s</b> %s\n",
			i18n.T(lang, "bot.headscale.checked_label"),
			checkedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if latest.HTMLURL != "" {
		fmt.Fprintf(out, "<b>%s</b> %s\n",
			i18n.T(lang, "bot.headscale.url_label"),
			latest.HTMLURL)
	}

	// Last 3 history rows. Cap at 3 so the message
	// stays under Telegram's 4096-char limit on a
	// long history.
	if len(history) > 0 {
		fmt.Fprintf(out, "\n%s\n", i18n.T(lang, "bot.headscale.history_title"))
		max := 3
		if len(history) < max {
			max = len(history)
		}
		for i := 0; i < max; i++ {
			r := history[i]
			sev := i18n.T(lang, "bot.headscale.severity_patch")
			if r.IsBreaking {
				sev = i18n.T(lang, "bot.headscale.severity_breaking")
			}
			pub := "—"
			if !r.PublishedAt.IsZero() {
				pub = r.PublishedAt.Format("2006-01-02")
			}
			notified := ""
			if r.Notified {
				notified = " — " + i18n.T(lang, "bot.headscale.notified")
			}
			fmt.Fprintf(out, "  • <code>%s</code> (%s) — %s%s\n",
				r.Version, pub, sev, notified)
		}
	}
	return out.String()
}

// silence unused-import warnings when this file
// is built without headscale_version wired (the
// reverse-import path exists because the bot
// package is imported by handlers).
var _ = headscale_version.CompareSemver
