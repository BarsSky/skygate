package admin

// acl_import.go — /admin/acls export + import (with dry-run preview).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/admin_acls_import.go.
//
// Handlers: GetAdminACLsExport, GetAdminACLsImport, PostAdminACLsImport,
// PostAdminACLsImportApply. Helper: validateImportedACL.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/headscale"
)

// GetAdminACLsExport returns the current acl.GenerateACL output as
// a downloadable JSON file. Admin-only.
func (s *Service) GetAdminACLsExport(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	policy, err := acl.GenerateACL(s.DB)
	if err != nil {
		http.Error(w, "generate acl: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ts := time.Now().UTC().Format("2006-01-02-1504")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-skygate-acl.json"`, ts))
	_, _ = io.WriteString(w, policy)
}

// GetAdminACLsImport shows the import form (file upload +
// paste-textarea). Always gets the current policy so the
// dry-run page can render side-by-side. Admin-only.
func (s *Service) GetAdminACLsImport(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	currentPolicy, _ := acl.GenerateACL(s.DB)
	s.Backend.RenderWithLayout(w, r, "admin/acls_import.html", c, map[string]any{
		"CurrentPolicy": currentPolicy,
	})
}

// PostAdminACLsImport parses the uploaded file (or pasted
// textarea) and renders the dry-run page. Admin-only.
func (s *Service) PostAdminACLsImport(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "form parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	var policy string
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		b, _ := io.ReadAll(file)
		policy = string(b)
	}
	if policy == "" {
		policy = r.FormValue("policy")
	}
	if strings.TrimSpace(policy) == "" {
		http.Error(w, "empty policy (no file and no textarea)", http.StatusBadRequest)
		return
	}
	if err := validateImportedACL(policy); err != nil {
		http.Error(w, "policy: "+err.Error(), http.StatusBadRequest)
		return
	}
	currentPolicy, _ := acl.GenerateACL(s.DB)
	hCur := sha256.Sum256([]byte(currentPolicy))
	hImp := sha256.Sum256([]byte(policy))
	s.Backend.RenderWithLayout(w, r, "admin/acls_import.html", c, map[string]any{
		"CurrentPolicy": currentPolicy,
		"ImportedPolicy": policy,
		"SameAsCurrent": hCur == hImp,
		"CurrentHash":   hex.EncodeToString(hCur[:8]),
		"ImportedHash":  hex.EncodeToString(hImp[:8]),
	})
}

// PostAdminACLsImportApply pushes the imported policy to every
// distinct headscale plane. The policy arrives in a hidden
// form field (set by the dry-run page), NOT in a file.
// Admin-only.
func (s *Service) PostAdminACLsImportApply(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	policy := r.FormValue("policy")
	if err := validateImportedACL(policy); err != nil {
		http.Error(w, "policy: "+err.Error(), http.StatusBadRequest)
		return
	}
	var alerter acl.Alerter
	if s.Notifier != nil {
		alerter = s.Notifier
	}
	results := acl.SetACLForAllPlanes(s.DB,
		func(planeURL string) *headscale.Client {
			if planeURL == "" {
				return s.HSGlobalFn()
			}
			// 2026-08-05 v0.33.1.12: db.PlaceholdersList(1) so
			// the "?" dispatches to "$1" on PG. Without this
			// the /admin/acls "Apply" import-Apply (which calls
			// this resolver for every distinct plane URL) fails
			// on PG with "syntax error at or near ','".
			rows, err := s.DB.Query("SELECT id FROM portal_users WHERE headscale_url = "+db.PlaceholdersList(1)+" LIMIT 1", planeURL)
			if err != nil {
				return s.HSGlobalFn()
			}
			defer rows.Close()
			if !rows.Next() {
				return s.HSGlobalFn()
			}
			var uid int64
			if err := rows.Scan(&uid); err != nil {
				return s.HSGlobalFn()
			}
			return s.HSForUserFn(uid)
		},
		alerter,
		c.Username,
		fmt.Sprintf("ACL import by %s (per-plane)", c.Username),
		policy,
	)
	for _, r := range results {
		if r.Err != nil {
			http.Error(w, "set policy: "+r.Err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if s.Notifier != nil {
		go s.Notifier.SendAlert(fmt.Sprintf("📥 ACL imported by %s → %d plane(s)", c.Username, len(results)))
	}
	http.Redirect(w, r, "/admin/acls?imported=1", http.StatusSeeOther)
}

// validateImportedACL does a cheap shape check on the imported
// JSON: must parse, must be an object, must have the four
// top-level keys headscale 0.29 expects (acls, tagOwners,
// groups, ssh). It does NOT verify that the identities exist
// in the local DB — headscale will reject unknown identities
// in tagOwners, and the dry-run page lets the operator eyeball
// the structure before hitting Apply.
func validateImportedACL(policy string) error {
	policy = strings.TrimSpace(policy)
	if policy == "" {
		return fmt.Errorf("empty")
	}
	var shape map[string]any
	if err := jsonUnmarshal([]byte(policy), &shape); err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	for _, key := range []string{"acls", "tagOwners", "groups", "ssh"} {
		if _, ok := shape[key]; !ok {
			return fmt.Errorf("missing top-level key %q (headscale 0.29 requires acls, tagOwners, groups, ssh)", key)
		}
	}
	return nil
}

// jsonUnmarshal is a tiny indirection so the tests can mock
// it if needed (and to keep validateImportedACL free of
// direct encoding/json imports — same pattern as the
// original file). The signature matches encoding/json.Unmarshal.
var jsonUnmarshal = json.Unmarshal
