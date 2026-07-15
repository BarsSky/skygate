// 2026-07-15: Этап 14 v18 (v0.12.0) — admin handlers for the
// per-user headscale control plane feature.
//
// Three new pages:
//
//   /admin/control-planes       — landing. Lists every
//                                distinct headscale plane
//                                (global default + per-user
//                                overrides) with the user
//                                count + a per-plane health
//                                probe ("Test" button).
//                                This is the operator's
//                                cockpit view of the
//                                multi-control-plane setup.
//
//   /admin/users/{id}/plane     — edit form for one user's
//                                (url, key) override. Save
//                                persists via
//                                db.SetUserHeadscaleConfig;
//                                the cached per-url client
//                                is invalidated so the next
//                                HSForUser call returns a
//                                fresh client.
//
//   POST /admin/users/{id}/plane/clear — clears the
//                                override (back to the
//                                global default).
//
// Per-plane ACL is deferred to v0.13.0 (per
// docs/skygate-as-shell.md) — GenerateACL still writes to
// the global headscale. v0.12.0 unlocks "per-user device
// list" and "per-user preauth key issuance"; the ACL
// migration is the next step.

package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// ---------- /admin/control-planes ----------

// GetAdminControlPlanes renders the landing page.
func (a *App) GetAdminControlPlanes(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := db.AllUsersHeadscaleConfig(a.DB)
	if err != nil {
		http.Error(w, "load users: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// We need the global default URL to render the "global"
	// row's URL field. The /admin/control-planes page shows
	// the operator's primary plane as the first row even
	// when no users are on it.
	globalURL := ""
	if a.HSGlobal() != nil {
		globalURL = a.HSGlobal().BaseURL
	}
	planes := db.SummariseControlPlanes(rows, globalURL)
	a.renderWithLayout(w, r, "admin-control-planes", c, map[string]any{
		"Planes":      planes,
		"GlobalURL":   globalURL,
		"Rows":        rows,
		"FlashError":  r.URL.Query().Get("err"),
		"FlashInfo":   r.URL.Query().Get("info"),
		"HasSecret":   a.SecretKeyHex != "",
	})
}

// PostAdminControlPlanesTest probes a single plane and
// redirects back with the result in the URL. Used by the
// "Test" button next to each plane row. The plane URL
// comes from a form field (the row's URL).
func (a *App) PostAdminControlPlanesTest(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		controlPlanesRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	planeURL := strings.TrimSpace(r.FormValue("plane_url"))
	if planeURL == "" {
		controlPlanesRedirect(w, r, "", "plane_url is required")
		return
	}
	// For the global plane, the api key is a.HeadscaleKey.
	// For an overridden plane, we don't have a key here
	// (the per-user key is encrypted) — so we only support
	// testing the global plane from this page. Per-user
	// plane health has to be tested from the per-user form.
	if planeURL != a.HSGlobal().BaseURL {
		controlPlanesRedirect(w, r, "",
			"Per-user plane health has to be tested from the per-user form "+
				"(the per-user api key is encrypted and not exposed here).",
		)
		return
	}
	// The global client already has the api key. Do a
	// /api/v1/node list call as a connectivity probe.
	if _, err := a.HSGlobal().ListAllNodes(); err != nil {
		controlPlanesRedirect(w, r, "", "Test failed: "+err.Error())
		return
	}
	controlPlanesRedirect(w, r, i18n.T(a.I18n.LangFromRequest(r), "control_planes.test_ok"), "")
}

// ---------- /admin/users/{id}/plane (edit form) ----------

// GetAdminUserControlPlane renders the per-user edit form.
// id is the portal_users.id of the user whose override
// we're editing. The form shows the current (url, key)
// state; saving POSTs to PostAdminUserControlPlane.
func (a *App) GetAdminUserControlPlane(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	username, _ := db.GetUserNameByID(a.DB, id)
	if username == "" {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	// Read the current override (decrypted if SKYGATE_SECRET_KEY
	// is set). The form pre-fills the URL; the key field is
	// always empty (we don't echo the secret back).
	cfg, err := db.GetUserHeadscaleConfig(a.DB, id, a.SecretKeyHex)
	currentURL := ""
	hasKey := false
	if err == nil {
		currentURL = cfg.URL
		hasKey = cfg.APIKey != ""
	} else if !errors.Is(err, db.ErrNoUserControlPlane) {
		// A corrupt ciphertext shows up as a flash on the
		// edit form ("stored key was encrypted with a
		// different key; re-enter") rather than 500ing
		// the whole page.
	}
	a.renderWithLayout(w, r, "admin-user-control-plane", c, map[string]any{
		"UserID":         id,
		"TargetUsername": username,
		"CurrentURL":     currentURL,
		"HasKey":         hasKey,
		"HasSecret":      a.SecretKeyHex != "",
		"SecretMissing":  a.SecretKeyHex == "",
		"FlashError":     r.URL.Query().Get("err"),
		"FlashInfo":      r.URL.Query().Get("info"),
		"CorruptKey":     err != nil && errors.Is(err, db.ErrSecretCiphertextCorrupt),
	})
}

// PostAdminUserControlPlane persists the (url, key)
// override for one user.
func (a *App) PostAdminUserControlPlane(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		userControlPlaneRedirect(w, r, id, "", "Form parse error: "+err.Error())
		return
	}
	if a.SecretKeyHex == "" {
		userControlPlaneRedirect(w, r, id, "",
			"SKYGATE_SECRET_KEY is not set; per-user control plane keys cannot be encrypted. "+
				"Set SKYGATE_SECRET_KEY in .env and restart skygate.")
		return
	}
	url := strings.TrimSpace(r.FormValue("url"))
	apiKey := r.FormValue("api_key")
	if err := db.SetUserHeadscaleConfig(a.DB, id, url, apiKey, a.SecretKeyHex); err != nil {
		userControlPlaneRedirect(w, r, id, "", "Save failed: "+err.Error())
		return
	}
	// Invalidate the cached per-url client so the next
	// HSForUser call returns a fresh client with the new
	// credentials.
	a.InvalidateHSCache(url)
	a.audit(c.UserID, c.Username, "user_control_plane.set",
		fmt.Sprintf("user_id=%d url=%q", id, url))
	lang := a.I18n.LangFromRequest(r)
	userControlPlaneRedirect(w, r, id, i18n.T(lang, "control_planes.saved"), "")
}

// PostAdminUserControlPlaneClear removes the override
// (back to the global default). Same routing semantics
// as the Save form: invalidate the cached client for
// the (now-cleared) URL.
func (a *App) PostAdminUserControlPlaneClear(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	// Read the current url (if any) so we can invalidate
	// the right cache entry. If the row already has no
	// override, this is a no-op.
	if existing, err := db.GetUserHeadscaleConfig(a.DB, id, a.SecretKeyHex); err == nil {
		a.InvalidateHSCache(existing.URL)
	}
	if err := db.ClearUserHeadscaleConfig(a.DB, id); err != nil {
		userControlPlaneRedirect(w, r, id, "", "Clear failed: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "user_control_plane.clear", fmt.Sprintf("user_id=%d", id))
	lang := a.I18n.LangFromRequest(r)
	userControlPlaneRedirect(w, r, id, i18n.T(lang, "control_planes.cleared"), "")
}

// ---------- redirect helpers ----------

func controlPlanesRedirect(w http.ResponseWriter, r *http.Request, info, errMsg string) {
	redirectWithFlash(w, r, "/admin/control-planes", info, errMsg)
}

func userControlPlaneRedirect(w http.ResponseWriter, r *http.Request, userID int64, info, errMsg string) {
	q := []string{}
	if info != "" {
		q = append(q, "info="+info)
	}
	if errMsg != "" {
		q = append(q, "err="+errMsg)
	}
	target := fmt.Sprintf("/admin/users/%d/plane", userID)
	if len(q) > 0 {
		target += "?" + strings.Join(q, "&")
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
