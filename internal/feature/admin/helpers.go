// Package admin — helpers.go holds cross-handler helpers
// (redirect-flash pattern, CSV splitting, etc.) shared by
// the integrations / control-planes / other admin handlers.

package admin

import (
	"net/http"
	"net/url"
	"strings"
)

// RedirectWithFlash centralises the "redirect + flash query
// param" pattern that all admin POST handlers use. okMsg
// and errMsg are mutually independent: an empty one doesn't
// show as a query param at all. Used by integrations.go,
// control_planes.go (which moved into a separate future
// refactor), and any other admin POST handler.
func RedirectWithFlash(w http.ResponseWriter, r *http.Request, path, okMsg, errMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	target := path
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// splitAndTrimCSV is the form-side counterpart to db.splitCSV.
// The form input may have either commas or newlines (the
// textarea helper shows one per line). Normalise to
// comma-separated before splitting, trim whitespace, drop
// empty entries.
func splitAndTrimCSV(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\n", ",")
	s = strings.ReplaceAll(s, "\r", "")
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
