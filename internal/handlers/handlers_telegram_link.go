package handlers

// 2026-07-14: helper for the Telegram deep link used by both
// the QR code (served from /my/telegram/qr) and the visible
// link in the bind-options card on /my/telegram.
//
// Why a helper and not a duplicated fmt.Sprintf: the link format
// is the contract between skygate and the user's phone. If the
// shape ever needs to change (e.g. switch to tg://resolve for
// better Android deep-link reliability, or add utm_source for
// analytics), there's exactly one place to change it AND one
// place to test it.

import "fmt"

// TelegramDeepLink builds the URL the user's phone should open
// when scanning the QR or clicking the bind-options link on
// /my/telegram. The current format is `https://t.me/<bot>?start=<token>`,
// which is the standard Telegram deep-link shape — Android's
// default camera app treats it as a verified app link for the
// Telegram client (when installed), and the URL also opens
// cleanly in any browser as a fallback.
//
// We deliberately use https://t.me/ (not the tg:// scheme)
// because some QR scanners refuse to scan tg:// URLs (the
// scanner doesn't know which app should handle them). https://
// is universally scannable; the trade-off is that the user
// might end up in the browser instead of the Telegram app, in
// which case the browser's "open in Telegram" handoff kicks in.
//
// Empty username or token produces "" (not a partial URL).
// Callers should check for "" before using the result in any
// URL-emitting context.
func TelegramDeepLink(username, token string) string {
	if username == "" || token == "" {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s?start=%s", username, token)
}
