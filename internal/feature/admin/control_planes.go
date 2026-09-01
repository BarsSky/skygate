// Package admin — control_planes.go owns the per-user
// headscale control plane admin pages (/admin/control-planes
// landing, /admin/users/{id}/plane edit form, and the
// provision/decommission flow that creates+destroys a
// per-user headscale container via headscale.ProvisionUser
// / headscale.DecommissionUser).
//
// refactor-v0.30 Phase B step 3b.4 (2026-07-29): moved
// from internal/handlers/admin_control_planes.go. The
// move was forced by 3b.2 — admin_integrations_renderer.go
// owned the redirectWithFlash helper that this file uses.
// When the renderer moved to feature/admin, the
// redirectWithFlash helper had to follow. The
// admin_control_planes.go dependency on it meant
// admin_control_planes.go had to move too, in the same
// commit.

package admin

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
)

// ---------- /admin/control-planes ----------

// GetAdminControlPlanes renders the landing page.
func (s *Service) GetAdminControlPlanes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := db.AllUsersHeadscaleConfig(s.dbc())
	if err != nil {
		http.Error(w, "load users: "+err.Error(), http.StatusInternalServerError)
		return
	}
	globalURL := ""
	if s.HSGlobalFn() != nil {
		globalURL = s.HSGlobalFn().BaseURL
	}
	planes := db.SummariseControlPlanes(rows, globalURL)
	s.Backend.RenderWithLayout(w, r, "admin/control_planes.html", c, map[string]any{
		"Planes":     planes,
		"GlobalURL":  globalURL,
		"Rows":       rows,
		"FlashError": r.URL.Query().Get("err"),
		"FlashInfo":  r.URL.Query().Get("info"),
		"HasSecret":  s.SecretKeyHex != "",
	})
}

// PostAdminControlPlanesTest probes a single plane and
// redirects back with the result in the URL. Used by the
// "Test" button next to each plane row.
func (s *Service) PostAdminControlPlanesTest(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.controlPlanesRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	planeURL := strings.TrimSpace(r.FormValue("plane_url"))
	if planeURL == "" {
		s.controlPlanesRedirect(w, r, "", "plane_url is required")
		return
	}
	// For the global plane, the api key is in the global
	// client. For an overridden plane, we don't have a key
	// here (the per-user key is encrypted) — so we only
	// support testing the global plane from this page.
	if planeURL != s.HSGlobalFn().BaseURL {
		s.controlPlanesRedirect(w, r, "",
			"Per-user plane health has to be tested from the per-user form "+
				"(the per-user api key is encrypted and not exposed here).",
		)
		return
	}
	if _, err := s.HSGlobalFn().ListAllNodes(); err != nil {
		s.controlPlanesRedirect(w, r, "", "Test failed: "+err.Error())
		return
	}
	s.controlPlanesRedirect(w, r, i18n.T(s.I18n.LangFromRequest(r), "control_planes.test_ok"), "")
}

// ---------- /admin/users/{id}/plane (edit form) ----------

// GetAdminUserControlPlane renders the per-user edit form.
func (s *Service) GetAdminUserControlPlane(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	username, _ := db.GetUserNameByID(s.dbc(), id)
	if username == "" {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	cfg, err := db.GetUserHeadscaleConfig(s.dbc(), id, s.SecretKeyHex)
	currentURL := ""
	hasKey := false
	if err == nil {
		currentURL = cfg.URL
		hasKey = cfg.APIKey != ""
	} else if !errors.Is(err, db.ErrNoUserControlPlane) {
		// A corrupt ciphertext shows up as a flash on the
		// edit form rather than 500ing the page.
	}
	s.Backend.RenderWithLayout(w, r, "admin/user_control_plane.html", c, map[string]any{
		"UserID":         id,
		"TargetUsername": username,
		"CurrentURL":     currentURL,
		"HasKey":         hasKey,
		"HasSecret":      s.SecretKeyHex != "",
		"SecretMissing":  s.SecretKeyHex == "",
		"FlashError":     r.URL.Query().Get("err"),
		"FlashInfo":      r.URL.Query().Get("info"),
	})
}

// PostAdminUserControlPlane persists the (url, key)
// override for one user.
func (s *Service) PostAdminUserControlPlane(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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
		s.userControlPlaneRedirect(w, r, id, "", "Form parse error: "+err.Error())
		return
	}
	if s.SecretKeyHex == "" {
		s.userControlPlaneRedirect(w, r, id, "",
			"SKYGATE_SECRET_KEY is not set; per-user control plane keys cannot be encrypted. "+
				"Set SKYGATE_SECRET_KEY in .env and restart skygate.")
		return
	}
	url := strings.TrimSpace(r.FormValue("url"))
	apiKey := r.FormValue("api_key")
	if err := db.SetUserHeadscaleConfig(s.dbc(), id, url, apiKey, s.SecretKeyHex); err != nil {
		s.userControlPlaneRedirect(w, r, id, "", "Save failed: "+err.Error())
		return
	}
	if s.InvalidateHSCacheFn != nil {
		s.InvalidateHSCacheFn(url)
	}
	s.Backend.Audit(c.UserID, c.Username, "user_control_plane.set",
		fmt.Sprintf("user_id=%d url=%q", id, url))
	lang := s.I18n.LangFromRequest(r)
	s.userControlPlaneRedirect(w, r, id, i18n.T(lang, "control_planes.saved"), "")
}

// PostAdminUserControlPlaneClear removes the override
// (back to the global default).
func (s *Service) PostAdminUserControlPlaneClear(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	if existing, err := db.GetUserHeadscaleConfig(s.dbc(), id, s.SecretKeyHex); err == nil {
		if s.InvalidateHSCacheFn != nil {
			s.InvalidateHSCacheFn(existing.URL)
		}
	}
	if err := db.ClearUserHeadscaleConfig(s.dbc(), id); err != nil {
		s.userControlPlaneRedirect(w, r, id, "", "Clear failed: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "user_control_plane.clear", fmt.Sprintf("user_id=%d", id))
	lang := s.I18n.LangFromRequest(r)
	s.userControlPlaneRedirect(w, r, id, i18n.T(lang, "control_planes.cleared"), "")
}

// ---------- /admin/users/{id}/plane/provision + /decommission ----------

// PostAdminUserControlPlaneProvision runs the bootstrap
// script for the user, then writes the (url, encrypted
// api_key) to portal_users so HSForUser(uid) starts
// routing to the new per-user instance.
func (s *Service) PostAdminUserControlPlaneProvision(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	if s.SecretKeyHex == "" {
		s.userControlPlaneRedirect(w, r, id, "",
			"SKYGATE_SECRET_KEY is not set; cannot encrypt the per-user API key. "+
				"Set SKYGATE_SECRET_KEY in .env and restart skygate.")
		return
	}
	username, err := db.GetUserNameByID(s.dbc(), id)
	if err != nil {
		s.userControlPlaneRedirect(w, r, id, "", "user not found: "+err.Error())
		return
	}
	// Defensive: refuse to provision if a control plane is
	// already set.
	if existing, getErr := db.GetUserHeadscaleConfig(s.dbc(), id, s.SecretKeyHex); getErr == nil && existing.HasOverride() {
		s.userControlPlaneRedirect(w, r, id, "",
			"User already has a per-user headscale override ("+existing.URL+"). "+
				"Click 'Clear' below to remove it before re-provisioning.")
		return
	}
	log.Printf("user_provision_headscale: starting user=%d username=%s", id, username)
	s.Backend.Audit(c.UserID, c.Username, "user_provision_headscale.start",
		fmt.Sprintf("user_id=%d username=%s", id, username))
	result, err := headscale.ProvisionUser(username, id)
	if err != nil {
		log.Printf("user_provision_headscale: FAILED user=%d: %v", id, err)
		s.Backend.Audit(c.UserID, c.Username, "user_provision_headscale.fail",
			fmt.Sprintf("user_id=%d err=%q", id, err.Error()))
		s.userControlPlaneRedirect(w, r, id, "", err.Error())
		return
	}
	if err := db.SetUserHeadscaleConfig(s.dbc(), id, result.URL, result.APIKey, s.SecretKeyHex); err != nil {
		log.Printf("user_provision_headscale: SetUserHeadscaleConfig FAILED user=%d: %v", id, err)
		s.Backend.Audit(c.UserID, c.Username, "user_provision_headscale.fail",
			fmt.Sprintf("user_id=%d persist_err=%q", id, err.Error()))
		s.userControlPlaneRedirect(w, r, id, "",
			"container provisioned but DB write failed: "+err.Error()+
				" — run Decommission and try again")
		return
	}
	if s.InvalidateHSCacheFn != nil {
		s.InvalidateHSCacheFn(result.URL)
	}
	log.Printf("user_provision_headscale: OK user=%d url=%s container=%s hs_user_id=%d",
		id, result.URL, result.Container, result.HeadscaleUserID)
	s.Backend.Audit(c.UserID, c.Username, "user_provision_headscale.ok",
		fmt.Sprintf("user_id=%d url=%s container=%s hs_user_id=%d port=%d",
			id, result.URL, result.Container, result.HeadscaleUserID, result.HTTPPort))
	lang := s.I18n.LangFromRequest(r)
	s.userControlPlaneRedirect(w, r, id,
		i18n.Tf(lang, "control_planes.provisioned",
			result.Container, result.URL, result.HTTPPort),
		"")
}

// PostAdminUserControlPlaneDecommission tears down the
// per-user headscale container and clears the DB override.
func (s *Service) PostAdminUserControlPlaneDecommission(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	username, err := db.GetUserNameByID(s.dbc(), id)
	if err != nil {
		s.userControlPlaneRedirect(w, r, id, "", "user not found: "+err.Error())
		return
	}
	existing, _ := db.GetUserHeadscaleConfig(s.dbc(), id, s.SecretKeyHex)
	prevURL := ""
	if existing.HasOverride() {
		prevURL = existing.URL
	}
	log.Printf("user_decommission_headscale: starting user=%d username=%s prev_url=%s",
		id, username, prevURL)
	s.Backend.Audit(c.UserID, c.Username, "user_decommission_headscale.start",
		fmt.Sprintf("user_id=%d username=%s prev_url=%q", id, username, prevURL))
	if err := headscale.DecommissionUser(username); err != nil {
		log.Printf("user_decommission_headscale: script FAILED user=%d: %v", id, err)
		s.Backend.Audit(c.UserID, c.Username, "user_decommission_headscale.fail",
			fmt.Sprintf("user_id=%d err=%q", id, err.Error()))
		s.userControlPlaneRedirect(w, r, id, "", err.Error())
		return
	}
	if err := db.ClearUserHeadscaleConfig(s.dbc(), id); err != nil {
		log.Printf("user_decommission_headscale: ClearUserHeadscaleConfig FAILED user=%d: %v", id, err)
		s.Backend.Audit(c.UserID, c.Username, "user_decommission_headscale.fail",
			fmt.Sprintf("user_id=%d clear_err=%q", id, err.Error()))
		s.userControlPlaneRedirect(w, r, id, "",
			"container torn down but DB clear failed: "+err.Error())
		return
	}
	if s.InvalidateHSCacheFn != nil {
		s.InvalidateHSCacheFn(prevURL)
	}
	log.Printf("user_decommission_headscale: OK user=%d prev_url=%s", id, prevURL)
	s.Backend.Audit(c.UserID, c.Username, "user_decommission_headscale.ok",
		fmt.Sprintf("user_id=%d prev_url=%q", id, prevURL))
	lang := s.I18n.LangFromRequest(r)
	s.userControlPlaneRedirect(w, r, id, i18n.T(lang, "control_planes.decommissioned"), "")
}

// ---------- redirect helpers ----------

func (s *Service) controlPlanesRedirect(w http.ResponseWriter, r *http.Request, info, errMsg string) {
	RedirectWithFlash(w, r, "/admin/control-planes", info, errMsg)
}

func (s *Service) userControlPlaneRedirect(w http.ResponseWriter, r *http.Request, userID int64, info, errMsg string) {
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
