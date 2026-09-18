package middleware

import (
	"net/http"

	"skygate/internal/ratelimit"
)

// RequireLoginLimit blocks POST /login after too many attempts.
//
// On block: redirects to /login?err=rate_limited so the login page shows
// the message inline.
// On pass: delegates to next.ServeHTTP.
//
// The username comes from the form (parsed by the handler) but we
// conservatively rate-limit by IP even before login parsing — by
// reading r.FormValue("username"). This catches both automated
// credential-stuffing (per-IP) and brute-force on a known username
// (per-key).
//
// If AllowLogin returns false for either bucket we redirect immediately.
//
// 2026-09-18 (R6): pre-fix this wrote a 429 with a text/plain body
// ("too many login attempts, slow down\n"), so a rate-limited user got a
// bare text page instead of the login form with an explanation. The only
// caller is the browser form (cmd/skygate/main.go), and the login page
// already renders .Error, so a redirect is the right shape here — it
// keeps the user on the form and localises the message via the
// ?err=rate_limited code (the middleware has no i18n catalog). A
// non-browser client sees 303 + Retry-After rather than 429, which is an
// acceptable trade for the only wiring that exists.
func RequireLoginLimit(rl *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ratelimit.ClientIP(r)
			username := r.FormValue("username")
			if !rl.AllowLogin(username, ip) {
				w.Header().Set("Retry-After", "30")
				http.Redirect(w, r, "/login?err=rate_limited", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAPILimit applies the per-IP API bucket to /my/exit-rules/api
// and similar JSON endpoints.
//
// On block: http.StatusTooManyRequests with JSON error body matching the api response shape.
func RequireAPILimit(rl *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ratelimit.ClientIP(r)
			if !rl.AllowAPI(ip) {
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":"rate limit exceeded","retry_after_seconds":60}` + "\n"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
