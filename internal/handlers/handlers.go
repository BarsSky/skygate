package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/headscale"
)






type App struct {
	Version string
	DB           *sql.DB
	HS           *headscale.Client
	HeadscaleKey string
	JWTSecret    string
	// ControlURL is the public-facing URL of the headscale control plane,
	// shown to users in preauth instructions so they can configure
	// Tailscale with a custom coordination server. Typically
	// https://head.skynas.ru; falls back to a hardcoded default if the
	// SKYGATE_CONTROL_URL env var is empty at startup.
	ControlURL   string
	SessionHours int
	DerpBaseURL  string // base URL of the local custom DERP server
	SSHKeyPath   string // SSH key for exit node route sync
	Cfg         *config.Config // 2026-07-07: issue #12 — limits & stagger sync

	templates *Templates
}

func New(d *sql.DB, hs *headscale.Client, headscaleKey, secret, controlURL, sshKeyPath string, sessionH int, cfg *config.Config) *App {
	return &App{
		DB:           d,
		HS:           hs,
		HeadscaleKey: headscaleKey,
		JWTSecret:    secret,
		ControlURL:   controlURL,
		SessionHours: sessionH,
		DerpBaseURL:  "http://192.168.13.69:8766",
		templates:    LoadTemplates(),
		Cfg:          cfg,
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
	data["Version"] = a.Version
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
	if err == nil && c.Value != "" {
		claims, err := auth.ParseJWT(a.JWTSecret, c.Value)
		if err == nil {
			return claims
		}
	}
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "Bearer ") {
		tok := strings.TrimPrefix(authHdr, "Bearer ")
		if tok != "" {
			rows, err := a.DB.Query("SELECT pt.user_id, pu.username, pu.is_admin, pt.token_hash FROM personal_api_tokens pt JOIN portal_users pu ON pu.id = pt.user_id")
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var uid int64; var uname string; var adm bool; var hash string
					if rows.Scan(&uid, &uname, &adm, &hash) == nil {
						if auth.CheckAPIToken(hash, tok) {
							rows.Close()
							a.DB.Exec("UPDATE personal_api_tokens SET last_used_at=strftime('%s','now') WHERE token_hash=?", hash)
							return &auth.Claims{UserID: uid, Username: uname, IsAdmin: adm}
						}
					}
				}
			}
		}
	}
	return nil
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
// For admin: shows the whole tailnet. For users: shows their own devices
// and only the public/exit nodes they're allowed to see.
type TailnetMetrics struct {
	TotalNodes     int
	OnlineNodes    int
	ExitNodesCount int
	UsersCount     int
	ActiveDERP     string
	// User-scoped metrics (populated when called with a username)
	MyTotalNodes     int
	MyOnlineNodes    int
	MyExitNodesCount int
	// MyPreauthKeys is a 3-way split (used/active/expired). Empty
	// when not a per-user call.
	MyPreauthKeys PreauthKeyStats
}

func (a *App) computeTailnetMetrics(myUsername string, myUserID int64) TailnetMetrics {
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
	// Per-user metrics: for non-admin users, count nodes via node_owner_map
	// (same source /my/devices uses) rather than n.UserName, because
	// headscale reassigns tagged nodes to a synthetic "tagged-devices"
	// user and the live user_id link is lost. The backfill that runs in
	// /my/devices also fires from here, so the dashboard sees the same
	// set the moment the user lands on the page.
	if myUserID != 0 {
		a.backfillNodeOwnership(a.DB, nodes, myUserID, myUsername)
	}
	if myUsername != "" {
		// Use a set of node IDs the user owns, sourced from node_owner_map.
		owned := map[string]bool{}
		rows, _ := a.DB.Query(`SELECT node_id FROM node_owner_map WHERE username=?`, myUsername)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var nid string
				if err := rows.Scan(&nid); err == nil {
					owned[nid] = true
				}
			}
		}
		// Plus any node still showing the live user name (untagged nodes).
		for _, n := range nodes {
			if n.UserName == myUsername {
				owned[n.ID] = true
			}
		}
		for _, n := range nodes {
			if !owned[n.ID] {
				continue
			}
			m.MyTotalNodes++
			if n.Online {
				m.MyOnlineNodes++
			}
			if n.IsExitNode {
				m.MyExitNodesCount++
			}
		}
	}
	users, _ := a.HS.ListUsers()
	m.UsersCount = len(users)
	// Preauth split is per-user; admins see zero (their own key history
	// is admin tooling, not a per-user metric).
	if myUserID != 0 {
		m.MyPreauthKeys = a.countMyPreAuthKeys(myUserID, nodes)
	}
	m.ActiveDERP = "waw" // could be parsed from netcheck but kept simple here
	return m
}

func (a *App) GetDashboard(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Look up the headscale username for this portal user (may be empty for
	// brand-new users who haven't registered a device yet).
	var hsUserName string
	_ = a.DB.QueryRow(`SELECT username FROM portal_users WHERE id=?`, c.UserID).Scan(&hsUserName)
	// Admins see whole-tailnet metrics; users see only their own.
	scope := ""
	if !c.IsAdmin && hsUserName != "" {
		scope = hsUserName
	}
	a.renderWithLayout(w, "dashboard.html", c, map[string]any{
		"TailnetMetrics": a.computeTailnetMetrics(scope, c.UserID),
	})
}

// ---------- MY KEYS ----------

// GetMyKeys lists every preauth key the current user has been issued,
// with its lifecycle state. Lets a user see what's outstanding and
// revoke keys that are no longer needed (e.g. they generated a key
// for a one-off install, did the install, and don't want the unused
// key to sit around).
func (a *App) GetMyKeys(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	type keyRow struct {
		ID                int64
		Key               string
		Used              bool
		ExpiresAt         int64
		CreatedAt         int64
		HeadscalePreauthID string
	}
	rows, err := a.DB.Query(`SELECT id, key, used, expires_at, created_at, headscale_preauth_id
		FROM preauth_keys WHERE user_id=? ORDER BY created_at DESC`, c.UserID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var keys []keyRow
	now := time.Now().Unix()
	for rows.Next() {
		var k keyRow
		var hsID sql.NullString
		var usedInt, expNull int64
		if err := rows.Scan(&k.ID, &k.Key, &usedInt, &expNull, &k.CreatedAt, &hsID); err != nil {
			continue
		}
		k.Used = usedInt == 1
		if expNull > 0 {
			k.ExpiresAt = expNull
		}
		if hsID.Valid {
			k.HeadscalePreauthID = hsID.String
		}
		keys = append(keys, k)
	}
	// Live "used" check: if any headscale node currently has this
	// key as its preAuthKey, mark used even if our local flag is
	// behind. Same logic as countMyPreauthKeys.
	if hsUsed, hsErr := a.HS.ListAllNodes(); hsErr == nil {
		liveByKeyID := map[string]bool{}
		for _, n := range hsUsed {
			if n.PreAuthKeyID != "" {
				liveByKeyID[n.PreAuthKeyID] = true
			}
		}
		for i := range keys {
			if keys[i].HeadscalePreauthID != "" && liveByKeyID[keys[i].HeadscalePreauthID] {
				keys[i].Used = true
			}
		}
	}
	a.renderWithLayout(w, "user/keys.html", c, map[string]any{
		"Keys":     keys,
		"HasKeys":  len(keys) > 0,
		"Now":      now,
	})
}

// PostMyKeyExpire revokes a preauth key by ID. The key must belong
// to the current user (we filter on user_id in the SELECT/UPDATE
// chain). Used keys cannot be expired - the action is a no-op for
// them and we redirect back to the list with no error. Already-
// expired keys are also no-ops, idempotently.
//
// Workflow:
//   1. Look up the key by id, scoped to current user.
//   2. If used or already expired: redirect to /my/keys.
//   3. Call headscale.ExpirePreauthKey(userID, keyID).
//   4. On success, mark the local preauth_keys row as expired by
//      setting expires_at to the current time. We do NOT delete
//      the row - it's audit history.
//
// On error from headscale we return 500 with the message; the user
// can retry. We do NOT mutate the local row in that case.
func (a *App) PostMyKeyExpire(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Path parameter: /my/keys/{id}/expire
	idStr := r.PathValue("id")
	if idStr == "" {
		http.Error(w, "missing key id", 400)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad key id", 400)
		return
	}
	// Look up the key, scoped to current user.
	var usedInt int
	var expNull sql.NullInt64
	var hsID sql.NullString
	err = a.DB.QueryRow(`SELECT used, expires_at, headscale_preauth_id FROM preauth_keys
		WHERE id=? AND user_id=?`, id, c.UserID).Scan(&usedInt, &expNull, &hsID)
	if err == sql.ErrNoRows {
		http.Error(w, "key not found", 404)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// No-ops for used or already-expired keys.
	now := time.Now().Unix()
	if usedInt == 1 {
		a.audit(c.UserID, c.Username, "preauth_expire_noop", fmt.Sprintf("key_id=%d already used", id))
		http.Redirect(w, r, "/my/keys", http.StatusFound)
		return
	}
	if expNull.Valid && expNull.Int64 <= now {
		a.audit(c.UserID, c.Username, "preauth_expire_noop", fmt.Sprintf("key_id=%d already expired", id))
		http.Redirect(w, r, "/my/keys", http.StatusFound)
		return
	}
	// Resolve the headscale user ID for this portal user. We need
	// it for the headscale API/CLI call.
	var hsUserID sql.NullInt64
	if err := a.DB.QueryRow(`SELECT headscale_user_id FROM portal_users WHERE id=?`, c.UserID).Scan(&hsUserID); err != nil || !hsUserID.Valid {
		http.Error(w, "no headscale user linked", 400)
		return
	}
	// Expire in headscale. The local headscale_preauth_id is the
	// primary identifier; without it we fall back to... nothing,
	// the key is no longer addressable in headscale. (This is the
	// case for the 5/7 michail keys from before the API field
	// started populating. The user-facing behavior is the same:
	// we mark the local row expired and move on. They can't
	// register a device with the key anyway because the underlying
	// key string is in our DB only, not headscale.)
	if hsID.Valid && hsID.String != "" {
		if err := a.HS.ExpirePreauthKey(hsUserID.Int64, hsID.String); err != nil {
			http.Error(w, "headscale expire failed: "+err.Error(), 500)
			return
		}
	}
	// Mark local row as expired. We set expires_at to the current
	// time so the dashboard's 3-way split picks it up immediately
	// on next render (no separate 'expired' column; we reuse the
	// expires_at timestamp convention used for TTL-based expiry).
	if _, err := a.DB.Exec(`UPDATE preauth_keys SET expires_at=? WHERE id=? AND user_id=?`,
		now, id, c.UserID); err != nil {
		http.Error(w, "local update failed: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "preauth_expired", fmt.Sprintf("key_id=%d", id))
	http.Redirect(w, r, "/my/keys", http.StatusFound)
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

	// Lazy-backfill node_owner_map from headscale's preAuthKey history.
	// When a user creates a preauth key in /my/devices, we save its
	// headscale ID. When that key is later used to register a node,
	// headscale's API exposes node.PreAuthKey.ID. Match them and
	// snapshot the (node -> user) link in node_owner_map. This is the
	// ONLY way to recover ownership for nodes that headscale has
	// reassigned to the synthetic "tagged-devices" user because of
	// tag:private. We do this here, on the user's first /my/devices
	// load, so the same fix happens for every node the user owns -
	// without scanning the headscale DB up front.
	if c.UserID != 0 {
		a.backfillNodeOwnership(a.DB, all, c.UserID, username)
	}

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
	// TEMP DEBUG (v0.3.16)
	// Save headscale_preauth_id so we can later map a node's preAuthKey
	// back to this portal user when the device registers with this key.
	_, _ = a.DB.Exec(`INSERT INTO preauth_keys(user_id, key, expires_at, headscale_preauth_id) VALUES(?,?,?,?)`,
		c.UserID, key.Key, time.Now().Add(time.Hour).Unix(), key.ID)
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

// Admin user management functions moved to handlers_admin_users.go.
// (GetAdminUsers, PostAdminUser, extractIDFromPath, PostAdminDeleteUser)

// Admin device/tag handlers moved to handlers_admin_nodes.go.
// (GetAdminDevices, PostAdminNodeTag, PostAdminNodeUntag)

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
	GoVersion       string
	Machine         string
	Connections     int
	Accepts         int
	BytesIn         int64
	BytesOut        int64
	PacketsIn       int
	PacketsOut      int
	Clients         int
	STUNRequests    int
	RecentLog       string

	// Active connections to derper (src IP, reverse DNS).
	ActiveTCP []DerpPeer
	ActiveUDP []DerpPeer
	// ConnSummary aggregates ActiveTCP+ActiveUDP by kind for the hero badges.
	ConnSummary *ConnSummary
	// Snapshot history tail (parsed recent records).
	Snapshot []DerpSnapshot
}

// DerpPeer is one observed peer connecting to derper.
type DerpPeer struct {
	IP   string `json:"ip"`
	Host string `json:"host"`
	Port string `json:"port"`
	// Kind classifies the source: ws_relay (Tailscale client),
	// ws_admin (NPM WebSocket pool), lan, internet, unknown.
	Kind string `json:"kind,omitempty"`
}

// ConnSummary aggregates connections by kind for the dashboard hero badges.
type ConnSummary struct {
	Relay int
	Admin int
	LAN   int
	Self  int
	Other int
}

// DerpSnapshot is one entry from the rolling snapshot log on the agent.
type DerpSnapshot struct {
	TS      string                 `json:"ts"`
	Conns   []DerpPeer             `json:"conns"`
	Metrics map[string]interface{} `json:"metrics"`
	Summary *ConnSummary           `json:"summary,omitempty"`
}

// currentConns extracts gauge_current_connections (or current_conns)
// from a snapshot metrics map. JSON numbers decode to float64 by default
// so we always go through here rather than touching the map directly.

// DERP types and collectors moved to handlers_derp.go.
// (DerpSnapshot.CurrentConns, collectDerpStatus)
// counter honest without a separate garbage-collection job.
func (a *App) countMyPreAuthKeys(myUserID int64, nodes []headscale.NodeView) PreauthKeyStats {
	var s PreauthKeyStats
	if myUserID == 0 {
		return s
	}
	// Collect headscale preAuthKey IDs currently attached to any node.
	// These are authoritative "used" keys.
	hsUsedKeyIDs := map[string]bool{}
	for _, n := range nodes {
		if n.PreAuthKeyID != "" {
			hsUsedKeyIDs[n.PreAuthKeyID] = true
		}
	}
	now := time.Now().Unix()
	rows, err := a.DB.Query(`SELECT id, headscale_preauth_id, used, expires_at FROM preauth_keys WHERE user_id=?`, myUserID)
	if err != nil {
		return s
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var hsID sql.NullString
		var usedInt int
		var exp sql.NullInt64
		if err := rows.Scan(&id, &hsID, &usedInt, &exp); err != nil {
			continue
		}
		s.Total++
		// Determine the authoritative used state. Prefer the live
		// headscale signal (node.preAuthKey.id) over the local flag,
		// so a missing local flip doesn't keep a key listed as active
		// once the device exists. We DO NOT clear the local flag here
		// - that's a side-effect the user should opt into via a
		// separate sync job; for the counter, just trust headscale.
		isUsed := usedInt == 1
		if hsID.Valid && hsUsedKeyIDs[hsID.String] {
			isUsed = true
		}
		switch {
		case isUsed:
			s.Used++
		case exp.Valid && exp.Int64 <= now:
			s.Expired++
		default:
			s.Active++
		}
	}
	return s
}

// backfillNodeOwnership walks all nodes, and for any node whose headscale
// preAuthKey matches one of this portal user's preauth_keys, inserts a row
// in node_owner_map (idempotent via INSERT OR IGNORE).
//
// Why this exists:
//   - When a user issues a preauth key via /my/devices, we save the
//     headscale ID in preauth_keys.headscale_preauth_id.
//   - When that key is later consumed by a Tailscale client, the resulting
//     node reports its origin via the headscale API (node.preAuthKey.id).
//   - If the node then gets a tag applied (e.g. tag:private by ACL),
//     headscale reassigns ownership to a synthetic "tagged-devices" user
//     and the live user_id link is lost.
//   - This backfill reconstructs the link from the persisted key, so the
//     node shows up under the original owner in /my/devices and on the
//     user dashboard. Safe to call on every /my/devices load - the IGNORE
//     makes it a no-op once the snapshot exists.
//
// Garbage collection: this function also reconciles the snapshot against
// current reality. If a node that node_owner_map claims the user owns no
// longer exists in headscale (deleted, expired, reaped), the orphan row
// is removed. Without this, a user who deletes their device would keep
// seeing it on the dashboard forever - the original symptom of the
// michail "0/0" report. The flip side is that a transient headscale API
// hiccup could drop a row; the next successful /my/devices load will
// re-backfill it from preAuthKey, so the blast radius is one page load.
//
// Two strategies, applied in order, first match wins:
//
//   A. Strict join on n.PreAuthKeyID == preauth_keys.headscale_preauth_id.
//      Works for keys whose headscale_preauth_id was captured at issue
//      time. This is the original path from v0.3.9 - fast and accurate,
//      but vulnerable to API response shape changes (a preauth key issued
//      when the response field name shifted will not have a stored
//      headscale_preauth_id, and the node will not match here).
//
//   C. Temporal fallback. If (A) failed AND the node has a non-empty
//      CreatedAt AND the user has at least one preauth key created
//      within 1 hour BEFORE the node's CreatedAt, we attribute the node
//      to that key's owner. The 1-hour window is a safety margin: a
//      user can't physically generate a preauth key, ship it to a remote
//      device, and have that device register with headscale faster
//      than that. If a key was created within the window, it's
//      effectively the only plausible cause. This recovers ownership
//      for keys whose headscale_preauth_id was never captured (the
//      michail case: 5/7 keys have NULL headscale_preauth_id because
//      the API stopped populating that field on the day they were
//      generated).
//
// Safety: BOTH strategies skip nodes whose current headscale user
// belongs to a *different* portal user. A node that headscale has
// reassigned to "tagged-devices" still has user=tagged-devices there
// (we never override that), and nodes still in someone's namespace
// (user != "tagged-devices") keep their live link. We only insert
// snapshot rows for nodes that headscale has effectively orphaned
// OR for nodes that the user plausibly owns via temporal correlation.
func (a *App) backfillNodeOwnership(db *sql.DB, nodes []headscale.NodeView, portalUserID int64, portalUsername string) {
	if portalUserID == 0 || portalUsername == "" {
		return
	}
	// Build a set of currently-live node IDs.
	live := map[string]bool{}
	for _, n := range nodes {
		live[n.ID] = true
	}
	// GC pass: drop snapshot rows for nodes that no longer exist in
	// headscale. Restricted to rows that this portal user owns, so a
	// row owned by a different portal user (and pointing at the same
	// node id, possible if a node was re-tagged under someone else)
	// is left alone.
	snapRows, err := db.Query(`SELECT node_id FROM node_owner_map WHERE username=?`, portalUsername)
	if err == nil {
		var orphans []string
		for snapRows.Next() {
			var nid string
			if err := snapRows.Scan(&nid); err == nil && !live[nid] {
				orphans = append(orphans, nid)
			}
		}
		snapRows.Close()
		for _, nid := range orphans {
			_, _ = db.Exec(`DELETE FROM node_owner_map WHERE node_id=? AND username=?`, nid, portalUsername)
		}
	}
	// Preload this user's preauth keys once.
	type pakRow struct {
		ID                int64
		HeadscalePreauthID sql.NullString
		CreatedAt         int64
	}
	rows, err := db.Query(`SELECT id, headscale_preauth_id, created_at FROM preauth_keys WHERE user_id=? ORDER BY created_at DESC`, portalUserID)
	if err != nil {
		return
	}
	defer rows.Close()
	var paks []pakRow
	for rows.Next() {
		var r pakRow
		if err := rows.Scan(&r.ID, &r.HeadscalePreauthID, &r.CreatedAt); err == nil {
			paks = append(paks, r)
		}
	}
	// Look up the headscale user IDs that other portal users own,
	// so we can detect "this node is currently in someone else's
	// namespace" and refuse to steal it. A node whose n.UserID maps
	// to a different portal user is theirs, not ours.
	otherOwners := map[string]bool{}
	if portalUserID != 0 {
		oRows, _ := db.Query(`SELECT headscale_user_id FROM portal_users WHERE id != ? AND headscale_user_id IS NOT NULL AND headscale_user_id != ''`, portalUserID)
		if oRows != nil {
			defer oRows.Close()
			for oRows.Next() {
				var hid string
				if err := oRows.Scan(&hid); err == nil {
					otherOwners[hid] = true
				}
			}
		}
	}
	// Track nodes we've already snapshotted in this pass so a node
	// doesn't get two snapshot rows (e.g. matching (A) AND (C)).
	inserted := map[string]bool{}
	for _, n := range nodes {
		if inserted[n.ID] {
			continue
		}
		// Refuse to steal a node that headscale currently has in
		// another portal user's namespace. tagged-devices is a
		// synthetic user created by headscale for tag-bearing
		// nodes, NOT a portal user, so it doesn't appear in
		// otherOwners and is fair game for snapshot rows.
		if n.UserID != "" && otherOwners[n.UserID] {
			continue
		}
		var matchedTag string
		// Strategy A: strict join on headscale_preauth_id.
		if n.PreAuthKeyID != "" {
			for _, p := range paks {
				if p.HeadscalePreauthID.Valid && p.HeadscalePreauthID.String == n.PreAuthKeyID {
					matchedTag = firstTagOrFallback(n)
					break
				}
			}
		}
		// Strategy C: temporal fallback. Node has CreatedAt, and
		// one of this user's preauth keys was created within the
		// 1-hour window before the node.
		if matchedTag == "" && n.CreatedAt != "" {
			if nodeAt, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
				bestKey := int64(0)
				bestDelta := time.Duration(0)
				for _, p := range paks {
					keyAt := time.Unix(p.CreatedAt, 0)
					delta := nodeAt.Sub(keyAt)
					// Preauth key must be created BEFORE the node
					// (delta >= 0), and within 1 hour. The user
					// can issue a key, send it to a device, and
					// have the device register - but not faster
					// than ~minute for a remote network, and we
					// want a wide enough window to absorb clock
					// skew, retries, slow SSH tunnels, etc.
					if delta < 0 || delta > time.Hour {
						continue
					}
					if bestKey == 0 || delta < bestDelta {
						bestKey = p.ID
						bestDelta = delta
					}
				}
				if bestKey != 0 {
				// 2026-07-10: bug fix — when the match came through a skygate-issued preauth
				// key, the node must have been registered BY our user. Default to
				// tag:private (so the user only sees their own devices in Tailscale).
				// Previously firstTagOrFallback(n) returned tag:untagged for
				// headscale-tagless nodes — UI showed tag:private locally but
				// headscale had no tag. Admins can still set tag:public manually
				// via /admin/devices/taged (PostAdminNodeTag).
				matchedTag = "tag:private"
				}
			}
		}
		if matchedTag == "" {
			continue
		}
		if matchedTag == "tag:private" {
			// 2026-07-10: bug fix — sync DB and headscale. If we already
			// have a stale "tag:untagged" row from an older build (or empty
			// tag), upgrade to tag:private. Skip rows that already carry
			// tag:public (admin-assigned exit-node tag).
			_, _ = db.Exec(`UPDATE node_owner_map SET tag=?, tagged_by_user_id=?, tagged_at=strftime('%s','now')
				WHERE node_id=? AND (tag = '' OR tag = 'tag:untagged')`,
				matchedTag, portalUserID, n.ID)
		} else {
			_, _ = db.Exec(`INSERT OR IGNORE INTO node_owner_map
				(node_id, headscale_user_id, username, tag, tagged_by_user_id)
				VALUES (?, ?, ?, ?, ?)`,
				n.ID, portalUserID, portalUsername, matchedTag, portalUserID)
		}
		// Push tag:private to headscale if matched. Safe for empty/untagged rows.
		if matchedTag == "tag:private" {
			if nodeIDInt, err := strconv.ParseInt(n.ID, 10, 64); err == nil && a != nil && a.HS != nil {
				if err := a.HS.TagNode(nodeIDInt, "tag:private"); err != nil {
					log.Printf("warn: auto-tag node %s: %v", n.ID, err)
				}
				a.HS.InvalidateCache()
			}
		}
				inserted[n.ID] = true
		// 2026-07-10: bug fix — sync node tag to headscale. New nodes
		// registered via skygate now get tag:private automatically so the
		// in-DB node_owner_map and headscale's tag reflect the same state
		// (was a real bug — UI showed private but Android Tailscale still
		// saw all clients). Only re-tag when matchedTag is tag:private to
		// avoid clobbering an admin's manual public/exit tags. Fire-and-
		// forget: tag failures are logged but do not block the backfill.
		if matchedTag == "tag:private" {
			if nodeIDInt, err := strconv.ParseInt(n.ID, 10, 64); err == nil && a != nil && a.HS != nil {
				if err := a.HS.TagNode(nodeIDInt, "tag:private"); err != nil {
					log.Printf("warn: auto-tag node %s: %v", n.ID, err)
				}
				a.HS.InvalidateCache()
			}
		}
		// Mark the preauth key as used if headscale has a node attached to it.
		if n.PreAuthKeyID != "" {
			if _, err := db.Exec(`UPDATE preauth_keys SET used=1 WHERE headscale_preauth_id=? AND used=0`, n.PreAuthKeyID); err != nil {
				log.Printf("warn: mark key %s used: %v", n.PreAuthKeyID, err)
			}
		}
	}
}

// DERP helpers (firstTagOrFallback, classifyDerp*, summarizeDerpPeers) moved to handlers_derp.go.
// DERP admin handlers moved to handlers_derp.go.
// (GetAdminDERP, GetAdminDERPRefresh)

// ── API Tokens ──

// API token handlers moved to handlers_api_tokens.go.
// (GetMyTokens, PostMyToken, PostMyTokenRevoke)


// 2026-07-07: getMaxRulesForUser returns per-user rule limit or default.
func (a *App) getMaxRulesForUser(username string) int {
	if a.Cfg == nil { return 0 }
	if v, ok := a.Cfg.UserMaxRules[username]; ok {
		return v
	}
	return a.Cfg.MaxRulesPerDevice
}
