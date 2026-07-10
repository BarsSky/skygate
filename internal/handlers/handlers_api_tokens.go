package handlers

// handlers_api_tokens.go — extracted from handlers.go.
// Personal API tokens for AI integration (Bearer auth).
// Each user creates their own tokens; tokens are hashed (not stored
// in plaintext) and shown to the user ONCE at creation.
// - GetMyTokens       (/my/tokens)
// - PostMyToken      (create + show raw token)
// - PostMyTokenRevoke (delete by id)

import (
	"fmt"
	"net/http"
	"time"

	"skygate/internal/auth"
)


func (a *App) GetMyTokens(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil { http.Redirect(w, r, "/login", http.StatusFound); return }
	rows, err := a.DB.Query("SELECT id, label, last_used_at, created_at FROM personal_api_tokens WHERE user_id=? ORDER BY created_at DESC", c.UserID)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	type tRow struct { ID int64; Label string; LastUsed string; Created string }
	var tokens []tRow
	for rows.Next() {
		var t tRow; var lu, cr int64
		if rows.Scan(&t.ID, &t.Label, &lu, &cr) == nil {
			if lu > 0 { t.LastUsed = time.Unix(lu, 0).Format("2006-01-02 15:04") } else { t.LastUsed = "—" }
			t.Created = time.Unix(cr, 0).Format("2006-01-02 15:04")
			tokens = append(tokens, t)
		}
	}
	if tokens == nil { tokens = []tRow{} }
	a.renderWithLayout(w, r, "my_tokens.html", c, map[string]any{"Page": "tokens", "Title": "API Tokens", "Tokens": tokens})
}

func (a *App) PostMyToken(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil { http.Error(w, "unauthorized", 401); return }
	label := r.FormValue("label")
	raw, hash := auth.GenerateAPIToken()
	_, err := a.DB.Exec("INSERT INTO personal_api_tokens (user_id, token_hash, label) VALUES (?,?,?)", c.UserID, hash, label)
	if err != nil { http.Error(w, err.Error(), 500); return }
	a.audit(c.UserID, c.Username, "token_create", label)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<div class=\"card\"><h3>Токен создан</h3><p>Скопируйте сейчас — больше он показан не будет.</p><pre style=\"background:var(--bg);padding:12px;border-radius:4px;word-break:break-all\">%s</pre><p><a href=\"/my/tokens\">← Назад к списку</a></p></div>", raw)
}

func (a *App) PostMyTokenRevoke(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil { http.Error(w, "unauthorized", 401); return }
	idStr := r.PathValue("id")
	a.DB.Exec("DELETE FROM personal_api_tokens WHERE id=? AND user_id=?", idStr, c.UserID)
	a.audit(c.UserID, c.Username, "token_revoke", idStr)
	http.Redirect(w, r, "/my/tokens?revoked=1", http.StatusFound)
}
