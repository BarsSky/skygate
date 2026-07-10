package middleware

import (
	"net/http"

	"skygate/internal/ratelimit"
)

// RequireLoginLimit blocks POST /login after too many attempts.
//
// On block: returns 429 Too Many Requests with a plain-text message.
// On pass: delegates to next.ServeHTTP.
//
// The username comes from the form (parsed by the handler) but we
// conservatively rate-limit by IP even before login parsing — by
// reading r.FormValue("username"). This catches both automated
// credential-stuffing (per-IP) and brute-force on a known username
// (per-key).
//
// If AllowLogin returns false for either bucket we 429 immediately.
func RequireLoginLimit(rl *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ratelimit.ClientIP(r)
			username := r.FormValue("username")
			if !rl.AllowLogin(username, ip) {
				w.Header().Set("Retry-After", "30")
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte("too many login attempts, slow down\n"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAPILimit applies the per-IP API bucket to /my/exit-rules/api
// and similar JSON endpoints.
//
// On block: 429 with JSON error body matching the api response shape.
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
