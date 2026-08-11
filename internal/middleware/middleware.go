package middleware

import (
	"net/http"
	"strings"
)

func RequireAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie("skygate_session")
			if err == nil && c.Value != "" && len(c.Value) >= 10 {
				next.ServeHTTP(w, r); return
			}
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if len(tok) >= 32 { next.ServeHTTP(w, r); return }
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		})
	}
}
