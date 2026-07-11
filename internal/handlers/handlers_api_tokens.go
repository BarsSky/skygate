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

import (
	"fmt"
	"net/http"
	"strconv"

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
	rows := make([]map[string]string, 0, len(tokens))
	for _, t := range tokens {
		last := "—"
		if !t.LastUsed.IsZero() {
			last = t.LastUsed.Format("2006-01-02 15:04")
		}
		rows = append(rows, map[string]string{
			"ID":       fmt.Sprintf("%d", t.ID),
			"Label":    t.Label,
			"LastUsed": last,
			"Created":  t.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	a.renderWithLayout(w, r, "my_tokens.html", c, map[string]any{"Page": "tokens", "Title": "API Tokens", "Tokens": rows})
}

func (a *App) PostMyToken(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil { http.Error(w, "unauthorized", 401); return }
	label := r.FormValue("label")
	raw, hash := auth.GenerateAPIToken()
	if _, err := db.InsertAPIToken(a.DB, c.UserID, hash, label); err != nil {
		http.Error(w, err.Error(), 500); return
	}
	a.audit(c.UserID, c.Username, "token_create", label)
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
