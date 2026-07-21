package handlers

// handlers_api_tokens.go — extracted from handlers.go.
// Personal API tokens for AI integration (Bearer auth).
// Each user creates their own tokens; tokens are hashed (not stored
// in plaintext) and shown to the user ONCE at creation.
// - GetMyTokens       (/my/tokens)
// - PostMyToken      (create + show raw token)
// - PostMyTokenRevoke (delete by id)
//
// 2026-07-11: Этап 10 part 2 — raw SQL moved to internal/db helpers:
//   ListAPITokensByUser (this file), InsertAPIToken, DeleteAPITokenByUser.
//
// 2026-07-16: v0.15.5 — personal API token rotation. PostMyToken
// now accepts a TTL dropdown (1h / 1d / 7d / 30d / never) and
// an auto-rotate checkbox. ExpiresAt is the unix timestamp at
// which the Bearer-auth path stops accepting the token; 0 =
// never (the pre-v0.15.5 behaviour, which is preserved for
// existing rows). AutoRotate is the operator's "rotate before
// expiry" flag; the actual rotation job is a v0.16.0 follow-up
// but the column is in v0.15.5 so the UI can store + read it.

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
)


func (a *App) GetMyTokens(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil { http.Redirect(w, r, "/login", http.StatusFound); return }
	tokens, err := db.ListAPITokensByUser(a.DB, c.UserID)
	if err != nil { http.Error(w, err.Error(), 500); return }
	// db helper already returns a non-nil empty slice, but be explicit
	// for the template (renders nothing on an empty list).
	if tokens == nil { tokens = []db.APIToken{} }
	// Pre-format times here (UI strings) so the template stays
	// dumb. LastUsed.IsZero() → "—" (never-used token).
	// ExpiresAt.IsZero() → "—" (never expires).
	rows := make([]map[string]string, 0, len(tokens))
	for _, t := range tokens {
		last := "—"
		if !t.LastUsed.IsZero() {
			last = t.LastUsed.Format("2006-01-02 15:04")
		}
		exp := "—"
		if !t.ExpiresAt.IsZero() {
			exp = t.ExpiresAt.Format("2006-01-02 15:04")
		}
		rows = append(rows, map[string]string{
			"ID":         fmt.Sprintf("%d", t.ID),
			"Label":      t.Label,
			"LastUsed":   last,
			"Created":    t.CreatedAt.Format("2006-01-02 15:04"),
			"Expires":    exp,
			"AutoRotate": fmt.Sprintf("%v", t.AutoRotate),
		})
	}
	a.renderWithLayout(w, r, "my_tokens.html", c, map[string]any{"Page": "tokens", "Title": "API Tokens", "Tokens": rows})
}

func (a *App) PostMyToken(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil { http.Error(w, "unauthorized", 401); return }
	label := r.FormValue("label")
	// 2026-07-16: v0.15.5 — TTL dropdown. "1h" / "1d" /
	// "7d" / "30d" / "never". Any other value falls through
	// to "never" (the v0.15.0 default) so an old / buggy
	// form can't lock the user out.
	expiresAt := int64(0)
	switch r.FormValue("ttl") {
	case "1h":
		expiresAt = time.Now().Add(time.Hour).Unix()
	case "1d":
		expiresAt = time.Now().Add(24 * time.Hour).Unix()
	case "7d":
		expiresAt = time.Now().Add(7 * 24 * time.Hour).Unix()
	case "30d":
		expiresAt = time.Now().Add(30 * 24 * time.Hour).Unix()
	case "never", "":
		expiresAt = 0
	}
	autoRotate := r.FormValue("auto_rotate") == "1"
	raw, hash := auth.GenerateAPIToken()
	if _, err := db.InsertAPIToken(a.DB, c.UserID, hash, label, expiresAt, autoRotate); err != nil {
		http.Error(w, err.Error(), 500); return
	}
	// Audit detail includes the TTL + auto-rotate flag so
	// the operator can see the policy applied at creation
	// time (the rotation job is a future follow-up).
	detail := fmt.Sprintf("label=%q ttl=%s auto_rotate=%v", label, r.FormValue("ttl"), autoRotate)
	a.audit(c.UserID, c.Username, "token_create", detail)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<div class=\"card\"><h3>Токен создан</h3><p>Скопируйте сейчас — больше он показан не будет.</p><pre style=\"background:var(--bg);padding:12px;border-radius:4px;word-break:break-all\">%s</pre><p><a href=\"/my/tokens\">← Назад к списку</a></p></div>", raw)
}

func (a *App) PostMyTokenRevoke(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil { http.Error(w, "unauthorized", 401); return }
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	// Bad id: the helper just won't match anything, no-op delete.
	// This matches the pre-refactor behavior (raw Exec with string
	// id was never validated either).
	_, _ = db.DeleteAPITokenByUser(a.DB, id, c.UserID)
	a.audit(c.UserID, c.Username, "token_revoke", idStr)
	http.Redirect(w, r, "/my/tokens?revoked=1", http.StatusFound)
}
