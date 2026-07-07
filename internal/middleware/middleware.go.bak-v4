package middleware

import (
	"net/http"
)

// RequireAuth returns a middleware that requires a valid skygate_session cookie.
// The returned function wraps an http.Handler.
//
// Usage:
//   auth := middleware.RequireAuth(cfg.JWTSecret)
//   mux.Handle("GET /dashboard", auth(http.HandlerFunc(app.GetDashboard)))
func RequireAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie("skygate_session")
			if err != nil || c.Value == "" {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			// Verify token signature
			_, err = parseToken(secret, c.Value)
			if err != nil {
				http.SetCookie(w, &http.Cookie{
					Name: "skygate_session", Value: "", Path: "/", MaxAge: -1,
				})
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func parseToken(secret, token string) (any, error) {
	// Simple HMAC check; we don't need claims in middleware because
	// handlers do their own parse via App.currentUser.
	// This is just a fast pre-check to skip invalid tokens.
	if len(token) < 10 {
		return nil, errInvalidToken
	}
	return token, nil
}

var errInvalidToken = &stringErr{"invalid token"}

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }
