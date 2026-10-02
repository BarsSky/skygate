// telegram_helpers.go — pure helpers: validators, formatting, flash redirects.
//
// Split out of telegram.go in refactor Phase D (2026-10-01): nothing here touches
// the database, which is what makes this the file to read first when a form
// rejects a token or a flash message renders wrong.

package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// findEnabledExitServer scans exit_servers for an
// enabled row whose node_id matches. Returns (nil, nil)
// when no row matches; (nil, err) on a real DB error;
// (row, nil) on success. Kept private to the admin
// package because the egress selector is the only caller.
func findEnabledExitServer(d *sql.DB, nodeID string) (*db.ExitServer, error) {
	rows, err := db.ListExitServers(d)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].NodeID == nodeID && rows[i].Enabled {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// TelegramCIDRs is the canonical Telegram IP list mirrored
// from deploy/tailscale-relay/update-routes.sh. The same
// constant lives in docs/telegram-relay.md; the helper
// SetAdvertisedRoutes dedupes + prepends 0.0.0.0/0+::/0 so
// the relay keeps its exit-node capability.
//
// IPv4 covers api.telegram.org + the DC ranges; IPv6 is
// aspirational (headscale routes it but Tailscale clients
// may not advertise the v6 routes without an explicit
// --advertise-routes flag on the client).
var TelegramCIDRs = []string{
	"91.108.4.0/22", "91.108.8.0/22", "91.108.12.0/22",
	"91.108.16.0/22", "91.108.20.0/22", "91.108.56.0/22",
	"149.154.160.0/20", "185.76.151.0/24",
	"2001:67c:4e8::/48", "2001:b28:f23c::/48",
	"2001:b28:f23f::/48", "2001:7a0:1::/48",
}

// (the sqlDB interface alias was removed in v0.33.1.8 —
// findEnabledExitServer takes *sql.DB directly now).

func boolToOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// looksLikeTelegramBotToken: structural sanity check.
func looksLikeTelegramBotToken(s string) bool {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// looksLikeTelegramChatID: digits, optional leading minus.
func looksLikeTelegramChatID(s string) bool {
	if s == "" {
		return true
	}
	if s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func formatTelegramMessage(host, subject, body string) string {
	return fmt.Sprintf("[%s] %s\n%s\n—\n%s",
		host, subject, time.Now().UTC().Format("2006-01-02T15:04:05Z"), body)
}

func (s *Service) redirectWithFlash(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	target := "/admin/telegram"
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func writeFlashRedirect(w http.ResponseWriter, r *http.Request, okMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	http.Redirect(w, r, "/admin/telegram?"+q.Encode(), http.StatusSeeOther)
}

func writeErrRedirect(w http.ResponseWriter, r *http.Request, errMsg string) {
	q := url.Values{}
	q.Set("err", errMsg)
	http.Redirect(w, r, "/admin/telegram?"+q.Encode(), http.StatusSeeOther)
}

func redactChatID(chatID, token string, c *auth.Claims) string {
	if chatID == "" {
		return "<token-only>"
	}
	return chatID
}
