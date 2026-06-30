package handlers

import (
	"database/sql"
	"errors"
	"log"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"


	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/headscale"
)

type App struct {
	DB           *sql.DB
	HS           *headscale.Client
	HeadscaleKey string
	JWTSecret    string
	SessionHours int

	templates *Templates
}

func New(d *sql.DB, hs *headscale.Client, headscaleKey, secret string, sessionH int) *App {
	return &App{
		DB:           d,
		HS:           hs,
		HeadscaleKey: headscaleKey,
		JWTSecret:    secret,
		SessionHours: sessionH,
		templates:    LoadTemplates(),
	}
}

// render executes a template directly (no layout). Used for self-contained pages.
func (a *App) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render: "+err.Error(), 500)
	}
}

// renderWithLayout wraps a fragment template in the layout. data is merged into
// the wrapper, so handlers can add per-page fields (Nodes, Users, Entries, ...).
// IsAdmin and Page are auto-derived from c (the JWT claims) so admin nav stays visible.
func (a *App) renderWithLayout(w http.ResponseWriter, name string, c *auth.Claims, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data["Page"] = pageFromName(name)
	if c != nil {
		data["Username"] = c.Username
		data["IsAdmin"] = c.IsAdmin
	}
	// Theme: prefer explicit theme in data, else derive from logged-in user, else linear.
	theme := db.ThemeLinear
	if c != nil {
		theme = db.GetUserTheme(a.DB, c.UserID)
	}
	if t, ok := data["Theme"].(string); ok && db.IsValidTheme(t) {
		theme = t
	}
	data["Theme"] = theme
	data["ThemeLabel"] = db.ThemeLabel(theme)
	wrapper := map[string]any{
		"Page":         data["Page"],
		"BodyTemplate": name,
		"Title":        pageTitle(name),
		"Theme":        theme,
		"ThemeLabel":   db.ThemeLabel(theme),
	}
	for k, v := range data {
		wrapper[k] = v
	}
	if err := a.templates.ExecuteTemplate(w, "layout", wrapper); err != nil {
		http.Error(w, "render: "+err.Error(), 500)
	}
}

func pageFromName(name string) string {
	name = name[:len(name)-len(".html")]
	if name == "dashboard" {
		return "dashboard"
	}
	if name == "user/devices" || name == "user/preauth_result" {
		return "my/devices"
	}
	if name == "user/exit_nodes" {
		return "my/exit-nodes"
	}
	if strings.HasPrefix(name, "admin/") {
		return name
	}
	if name == "help" {
		return "help"
	}
	return name
}

func pageTitle(name string) string {
	switch name {
	case "dashboard.html":
		return "Главная"
	case "user/devices.html":
		return "Мои устройства"
	case "user/preauth_result.html":
		return "Preauth ключ"
	case "user/exit_nodes.html":
		return "Exit nodes"
	case "admin/users.html":
		return "Пользователи"
	case "admin/devices.html":
		return "Все устройства"
	case "admin/acls.html":
		return "ACL"
	case "admin/audit.html":
		return "Audit log"
	case "admin/derp.html":
		return "DERP relay"
	case "help.html":
		return "Справка"
	default:
		return "Skygate"
	}
}

func dataValue(data any, key string) any {
	if m, ok := data.(map[string]any); ok {
		return m[key]
	}
	return nil
}

// currentUser parses JWT cookie and returns claims. nil if not authenticated.
func (a *App) currentUser(r *http.Request) *auth.Claims {
	c, err := r.Cookie("skygate_session")
	if err != nil || c.Value == "" {
		return nil
	}
	claims, err := auth.ParseJWT(a.JWTSecret, c.Value)
	if err != nil {
		return nil
	}
	return claims
}

// audit writes a row to the audit log.
func (a *App) audit(userID int64, username, action, detail string) {
	_, _ = a.DB.Exec(`INSERT INTO audit_log(user_id, username, action, detail) VALUES(?,?,?,?)`,
		userID, username, action, detail)
}

// ---------- AUTH ----------

func (a *App) GetLogin(w http.ResponseWriter, r *http.Request) {
	// Resolve theme: ?theme=... on the URL wins; otherwise last user theme if any.
	theme := db.ThemeLinear
	if t := r.URL.Query().Get("theme"); db.IsValidTheme(t) {
		theme = t
	} else if c, _ := r.Cookie("skygate_session"); c != nil {
		if claims, err := auth.ParseJWT(a.JWTSecret, c.Value); err == nil {
			theme = db.GetUserTheme(a.DB, claims.UserID)
		}
	}
	a.render(w, "login.html", map[string]any{
		"Error":      "",
		"Theme":      theme,
		"ThemeLabel": db.ThemeLabel(theme),
	})
}

func (a *App) PostLogin(w http.ResponseWriter, r *http.Request) {
	u := strings.TrimSpace(r.FormValue("username"))
	p := r.FormValue("password")
	if u == "" || p == "" {
		a.render(w, "login.html", map[string]any{"Error": "username and password required", "Theme": db.ThemeLinear, "ThemeLabel": db.ThemeLabel(db.ThemeLinear)})
		return
	}
	var id int64
	var hash string
	var isAdmin int
	err := a.DB.QueryRow(`SELECT id, password_hash, is_admin FROM portal_users WHERE username=?`, u).
		Scan(&id, &hash, &isAdmin)
	if errors.Is(err, sql.ErrNoRows) || !auth.CheckPassword(hash, p) {
		a.audit(id, u, "login_fail", "")
		a.render(w, "login.html", map[string]any{"Error": "invalid credentials", "Theme": db.ThemeLinear, "ThemeLabel": db.ThemeLabel(db.ThemeLinear)})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tok, err := auth.IssueJWT(a.JWTSecret, id, u, isAdmin == 1, a.SessionHours)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(id, u, "login_ok", "")
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   a.SessionHours * 3600,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (a *App) PostLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "skygate_session", Value: "", Path: "/", MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ---------- SETTINGS (theme switcher) ----------

// PostSettingsTheme updates the user's theme preference and bounces back.
func (a *App) PostSettingsTheme(w http.ResponseWriter, r *http.Request) {
	theme := r.FormValue("theme")
	if !db.IsValidTheme(theme) {
		theme = r.URL.Query().Get("theme")
	}
	if !db.IsValidTheme(theme) {
		http.Error(w, "invalid theme", 400)
		return
	}
	c := a.currentUser(r)
	if c == nil {
		// not logged in - just bounce to login with theme in URL
		http.Redirect(w, r, "/login?theme="+theme, http.StatusFound)
		return
	}
	if err := db.SetUserTheme(a.DB, c.UserID, theme); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "theme_change", theme)
	// back to wherever the user came from
	ref := r.Referer()
	if ref == "" {
		ref = "/dashboard"
	}
	// strip old theme query so we don't loop
	if strings.Contains(ref, "theme=") {
		ref = stripThemeParam(ref)
	}
	http.Redirect(w, r, ref, http.StatusFound)
}

func stripThemeParam(url string) string {
	if i := strings.Index(url, "?"); i >= 0 {
		qs := url[i+1:]
		parts := strings.Split(qs, "&")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if !strings.HasPrefix(p, "theme=") {
				out = append(out, p)
			}
		}
		prefix := url[:i]
		if len(out) == 0 {
			return prefix
		}
		return prefix + "?" + strings.Join(out, "&")
	}
	return url
}

// ---------- DASHBOARD ----------

// TailnetMetrics is a small summary of the tailnet for the dashboard hero.
type TailnetMetrics struct {
	TotalNodes    int
	OnlineNodes   int
	ExitNodesCount int
	UsersCount    int
	ActiveDERP    string
}

func (a *App) computeTailnetMetrics() TailnetMetrics {
	m := TailnetMetrics{}
	nodes, _ := a.HS.ListAllNodes()
	m.TotalNodes = len(nodes)
	for _, n := range nodes {
		if n.Online {
			m.OnlineNodes++
		}
		if n.IsExitNode {
			m.ExitNodesCount++
		}
	}
	users, _ := a.HS.ListUsers()
	m.UsersCount = len(users)
	m.ActiveDERP = "waw" // could be parsed from netcheck but kept simple here
	return m
}

func (a *App) GetDashboard(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	a.renderWithLayout(w, "dashboard.html", c, map[string]any{
		"TailnetMetrics": a.computeTailnetMetrics(),
	})
}

// ---------- HELP ----------

func (a *App) GetHelp(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	a.renderWithLayout(w, "help.html", c, map[string]any{})
}

// ---------- USER SELF-SERVICE ----------

func (a *App) GetMyDevices(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	var hsUserID sql.NullInt64
	var username string
	_ = a.DB.QueryRow(`SELECT headscale_user_id, username FROM portal_users WHERE id=?`, c.UserID).
		Scan(&hsUserID, &username)

	// Get all nodes (cached). Reuse them for both my-nodes (filter by user)
	// and public nodes (filter by tag/exit) - one HTTP call to headscale
	// instead of two.
	t0 := time.Now()
	all, _ := a.HS.ListAllNodes()

	// headscale reassigns ownership to a synthetic "tagged-devices" user
	// whenever a tag is applied, so we cannot rely on the live user_id
	// alone. We keep a snapshot of the original owner in node_owner_map
	// and union both sources to compute "my devices".
	type myNodeRow struct {
		ID       string
		Hostname string
		IP       string
		Online   bool
		LastSeen string
		UserName string
		IsPublic bool
		Source   string
	}
	mySet := map[string]bool{}
	var myNodesList []myNodeRow
	for _, n := range all {
		if hsUserID.Valid && username != "" && n.UserName == username {
			mySet[n.ID] = true
			ip := ""
			if len(n.IPAddresses) > 0 {
				ip = n.IPAddresses[0]
			}
			myNodesList = append(myNodesList, myNodeRow{
				ID: n.ID, Hostname: n.Hostname, IP: ip,
				Online: n.Online, LastSeen: n.LastSeen,
				UserName: n.UserName, IsPublic: n.IsPublicView(),
				Source: "live",
			})
		}
	}
	if username != "" {
		rows, _ := a.DB.Query(`SELECT node_id FROM node_owner_map WHERE username=?`, username)
		if rows != nil {
			defer rows.Close()
			snapIDs := map[string]bool{}
			for rows.Next() {
				var nid string
				if err := rows.Scan(&nid); err == nil {
					snapIDs[nid] = true
				}
			}
			for _, n := range all {
				if !snapIDs[n.ID] || mySet[n.ID] {
					continue
				}
				ip := ""
				if len(n.IPAddresses) > 0 {
					ip = n.IPAddresses[0]
				}
				myNodesList = append(myNodesList, myNodeRow{
					ID: n.ID, Hostname: n.Hostname, IP: ip,
					Online: n.Online, LastSeen: n.LastSeen,
					UserName: n.UserName, IsPublic: n.IsPublicView(),
					Source: "snapshot",
				})
			}
		}
	}

	publicNodes := []headscale.NodeView{}
	for _, n := range all {
		if n.IsExitNode || n.IsPublicView() {
			publicNodes = append(publicNodes, n)
		}
	}

	log.Printf("DBG GetMyDevices fetch took %v nodes=%d my=%d public=%d", time.Since(t0), len(all), len(myNodesList), len(publicNodes))

	a.renderWithLayout(w, "user/devices.html", c, map[string]any{
		"MyNodes":     myNodesList,
		"PublicNodes": publicNodes,
		"HasMyNodes":  len(myNodesList) > 0,
	})
}

func (a *App) PostMyPreauth(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	var hsUserID sql.NullInt64
	var username string
	err := a.DB.QueryRow(`SELECT headscale_user_id, username FROM portal_users WHERE id=?`, c.UserID).
		Scan(&hsUserID, &username)
	if err != nil || !hsUserID.Valid {
		http.Error(w, "no headscale user linked", 400)
		return
	}
	key, err := a.HS.CreatePreauthKey(hsUserID.Int64, "1h", false)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, _ = a.DB.Exec(`INSERT INTO preauth_keys(user_id, key, expires_at) VALUES(?,?,?)`,
		c.UserID, key.Key, time.Now().Add(time.Hour).Unix())
	a.audit(c.UserID, c.Username, "preauth_issued", "1h single-use")
	a.renderWithLayout(w, "user/preauth_result.html", c, map[string]any{
		"Key":     key.Key,
		"Expires": "1 hour",
		"OS":      r.FormValue("os"),
	})
}

// GetExitNodes lists exit nodes advertised in the tailnet. Visible to all
// authenticated users so they can pick one to route through.
func (a *App) GetExitNodes(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	exits, _ := a.HS.ListExitNodes()
	a.renderWithLayout(w, "user/exit_nodes.html", c, map[string]any{
		"ExitNodes": exits,
	})
}

// ---------- ADMIN ----------

func (a *App) GetAdminUsers(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	rows, err := a.DB.Query(`SELECT id, username, is_admin, headscale_user_id, created_at, theme FROM portal_users ORDER BY id`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var users []db.User
	for rows.Next() {
		var u db.User
		var adminI int
		var createdI int64
		var hsID sql.NullInt64
		var theme sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &adminI, &hsID, &createdI, &theme); err == nil {
			u.IsAdmin = adminI == 1
			u.HeadscaleUserID = hsID.Int64
			u.CreatedAt = time.Unix(createdI, 0)
			if theme.Valid {
				u.Theme = theme.String
			}
			u.PasswordHash = ""
			users = append(users, u)
		}
	}

	// Fetch headscale users and detect orphans (in headscale but not in skygate)
	hsUsers, _ := a.HS.ListUsers()
	linked := make(map[string]bool)
	for _, u := range users {
		if u.HeadscaleUserID > 0 {
			linked[strconv.FormatInt(u.HeadscaleUserID, 10)] = true
		}
	}
	var orphans []map[string]any
	for _, h := range hsUsers {
		if !linked[h.ID] {
			orphans = append(orphans, map[string]any{
				"HeadscaleID": h.ID,
				"Username":    h.Name,
				"CreatedAt":   h.CreatedAt,
			})
		}
	}

	a.renderWithLayout(w, "admin/users.html", c, map[string]any{
		"Users":     users,
		"HSOrphans": orphans,
	})
}

func (a *App) PostAdminUser(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"
	if username == "" || password == "" {
		http.Error(w, "username and password required", 400)
		return
	}
	if len(password) < 6 {
		http.Error(w, "password too short (min 6)", 400)
		return
	}
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(username) {
		http.Error(w, "username: lowercase letters, digits, _ and - only", 400)
		return
	}
	var existingID int64
	err := a.DB.QueryRow(`SELECT id FROM portal_users WHERE username=?`, username).Scan(&existingID)
	if err == nil {
		http.Error(w, fmt.Sprintf("user %q already exists in skygate", username), 409)
		return
	}
	hsUser, err := a.HS.CreateUser(username)
	if err != nil {
		http.Error(w, "headscale create user: "+err.Error(), 500)
		return
	}
	hsID, _ := strconv.ParseInt(hsUser.ID, 10, 64)
	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, err = a.DB.Exec(`INSERT INTO portal_users(username, password_hash, is_admin, headscale_user_id) VALUES(?,?,?,?)`,
		username, hash, isAdmin, hsID)
	if err != nil {
		http.Error(w, "portal insert: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "user_create", fmt.Sprintf("%s hs_id=%d admin=%v", username, hsID, isAdmin))
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func extractIDFromPath(path string) string {
	// Supports:
	//   /admin/users/123/delete -> "123"
	//   /admin/nodes/123/untag  -> "123"
	//   /admin/nodes/123/tag    -> "123"
	parts := strings.Split(path, "/")
	if len(parts) >= 4 && parts[1] == "admin" {
		switch parts[2] {
		case "users", "nodes":
			return parts[3]
		}
	}
	return ""
}

func (a *App) PostAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == c.UserID {
		http.Error(w, "cannot delete yourself", 400)
		return
	}
	var username string
	var hsID sql.NullInt64
	err := a.DB.QueryRow(`SELECT username, headscale_user_id FROM portal_users WHERE id=?`, id).
		Scan(&username, &hsID)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	hsDeleteMsg := ""
	if hsID.Valid && hsID.Int64 > 0 {
		if err := a.HS.DeleteUser(hsID.Int64); err != nil {
			hsDeleteMsg = fmt.Sprintf(" [headscale: %v]", err)
		} else {
			hsDeleteMsg = " [headscale: deleted]"
		}
	}
	_, _ = a.DB.Exec(`DELETE FROM preauth_keys WHERE user_id=?`, id)
	_, _ = a.DB.Exec(`DELETE FROM audit_log WHERE user_id=?`, id)
	_, err = a.DB.Exec(`DELETE FROM portal_users WHERE id=?`, id)
	if err != nil {
		http.Error(w, "delete: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "user_delete", fmt.Sprintf("id=%d %s hs_id=%d%s", id, username, hsID.Int64, hsDeleteMsg))
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func (a *App) GetAdminDevices(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	users, _ := a.HS.ListUsers()
	allNodes, _ := a.HS.ListAllNodes()
	a.renderWithLayout(w, "admin/devices.html", c, map[string]any{
		"Nodes": allNodes,
		"Users": users,
	})
}

// PostAdminNodeTag adds a headscale tag to a node.
func (a *App) PostAdminNodeTag(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", 400)
		return
	}
	tag := r.FormValue("tag")
	if tag == "" {
		tag = headscale.TagPublicTag
	}

	var origUserID, origUserName string
	if nodes, err := a.HS.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == strconv.FormatInt(nodeID, 10) {
				origUserID = n.UserID
				origUserName = n.UserName
				break
			}
		}
	}

	if err := a.HS.TagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if origUserID != "" && origUserName != "" && origUserName != "tagged-devices" {
		_, _ = a.DB.Exec(`INSERT OR REPLACE INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id)
			VALUES (?, ?, ?, ?, ?)`,
			nodeID, origUserID, origUserName, tag, c.UserID)
	}

	a.HS.InvalidateCache()
	a.audit(c.UserID, c.Username, "node_tag", fmt.Sprintf("node=%d tag=%s owner=%s", nodeID, tag, origUserName))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}

func (a *App) PostAdminNodeUntag(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", 400)
		return
	}
	tag := r.FormValue("tag")
	if tag == "" {
		tag = headscale.TagPublicTag
	}
	if err := a.HS.UntagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, _ = a.DB.Exec(`DELETE FROM node_owner_map WHERE node_id=? AND tag=?`, nodeID, tag)

	a.HS.InvalidateCache()
	a.audit(c.UserID, c.Username, "node_untag", fmt.Sprintf("node=%d tag=%s", nodeID, tag))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}

func (a *App) GetAdminAudit(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	rows, err := a.DB.Query(`SELECT id, user_id, username, action, detail, created_at FROM audit_log ORDER BY id DESC LIMIT 200`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	type Entry struct {
		ID               int64
		UserID           int64
		Username, Action string
		Detail           string
		Time             string
	}
	var entries []Entry
	for rows.Next() {
		var e Entry
		var t int64
		_ = rows.Scan(&e.ID, &e.UserID, &e.Username, &e.Action, &e.Detail, &t)
		e.Time = time.Unix(t, 0).Format("2006-01-02 15:04:05")
		entries = append(entries, e)
	}
	a.renderWithLayout(w, "admin/audit.html", c, map[string]any{
		"Entries": entries,
	})
}

func (a *App) GetAdminACLs(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	policy, policyErr := a.HS.GetACL()
	errStr := ""
	if policyErr != nil {
		errStr = policyErr.Error()
	}
	a.renderWithLayout(w, "admin/acls.html", c, map[string]any{
		"Policy":       policy,
		"Error":        errStr,
		"HeadplaneURL": "https://tsnet.skynas.ru/admin/",
		"APIKey":       a.HeadscaleKey,
	})
}

// ---------- ADMIN DERP ----------

// DerpStatus describes the local custom DERP relay (derper) for /admin/derp.
type DerpStatus struct {
	Running         bool
	SocketListening bool
	STUNListening   bool
	DERPPort        string
	STUNPort        string
	Version         string
	Hostname        string
	RegionCode      string
	RegionID        string
	RegionName      string
	WhiteIP         string
	UpTime          string
	StartedAt       string
	PID             string
	Memory          string
	RecentLog       string
}

func (a *App) collectDerpStatus() DerpStatus {
	s := DerpStatus{
		DERPPort:   "8443",
		STUNPort:   "3478",
		Version:    "1.70.0",
		Hostname:   "derp.skynas.ru",
		RegionCode: "mow",
		RegionID:   "900",
		RegionName: "Moscow Custom",
		WhiteIP:    "95.165.170.190",
		UpTime:     "n/a",
		StartedAt:  "n/a",
		PID:        "n/a",
		Memory:     "n/a",
	}

	// systemctl show derper.service -p ActiveEnterTimestamp,MainPID,MemoryCurrent
	if out, err := exec.Command("systemctl", "show", "derper.service", "-p", "ActiveState", "-p", "MainPID", "-p", "MemoryCurrent", "-p", "ActiveEnterTimestamp").CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			switch {
			case strings.HasPrefix(line, "ActiveState="):
				s.Running = strings.TrimPrefix(line, "ActiveState=") == "active"
			case strings.HasPrefix(line, "MainPID="):
				if v := strings.TrimPrefix(line, "MainPID="); v != "" && v != "0" {
					s.PID = v
				}
			case strings.HasPrefix(line, "MemoryCurrent="):
				if v := strings.TrimPrefix(line, "MemoryCurrent="); v != "" && v != "[not set]" {
					if b, err := strconv.ParseInt(v, 10, 64); err == nil {
						s.Memory = fmt.Sprintf("%.1f MB", float64(b)/1024/1024)
					}
				}
			case strings.HasPrefix(line, "ActiveEnterTimestamp="):
				if v := strings.TrimPrefix(line, "ActiveEnterTimestamp="); v != "" && v != "n/a" {
					s.StartedAt = v
				}
			}
		}
	}

	// ss/netstat for port listeners
	if out, err := exec.Command("ss", "-tln").CombinedOutput(); err == nil {
		if strings.Contains(string(out), ":8443") {
			s.SocketListening = true
		}
	}
	if out, err := exec.Command("ss", "-uln").CombinedOutput(); err == nil {
		if strings.Contains(string(out), ":3478") {
			s.STUNListening = true
		}
	}

	// Recent log
	if out, err := exec.Command("journalctl", "-u", "derper.service", "-n", "20", "--no-pager").CombinedOutput(); err == nil {
		s.RecentLog = string(out)
		if len(s.RecentLog) > 2000 {
			s.RecentLog = s.RecentLog[len(s.RecentLog)-2000:]
		}
	}

	// Uptime (derper.service entered active)
	if s.StartedAt != "n/a" {
		if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", s.StartedAt); err == nil {
			d := time.Since(t).Round(time.Second)
			s.UpTime = d.String()
		} else {
			// try other format
			for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon 2006-01-02 15:04:05 UTC"} {
				if t, err := time.Parse(layout, s.StartedAt); err == nil {
					d := time.Since(t).Round(time.Second)
					s.UpTime = d.String()
					break
				}
			}
		}
	}

	return s
}

func (a *App) GetAdminDERP(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	a.renderWithLayout(w, "admin/derp.html", c, map[string]any{
		"DerpStatus": a.collectDerpStatus(),
	})
}

// GetAdminDERPRefresh forces a refresh - same page.
func (a *App) GetAdminDERPRefresh(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/derp", http.StatusFound)
}
