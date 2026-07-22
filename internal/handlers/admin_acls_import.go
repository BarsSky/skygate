package handlers

// admin_acls_import.go — /admin/acls export + import (with
// dry-run preview).
//
// 2026-07-16: v0.13.0 — ACL import/export. Operators had
// been asking for "save my ACL" + "load an ACL" since the
// v0.12.0 multi-plane work. Without export, the only way
// to back up the policy is the acl_snapshots table (which
// is in-DB and not portable). Without import, a fresh
// install has to re-add every user via the web UI before
// headscale accepts a SetPolicy with new identities.
//
// Flow:
//   GET  /admin/acls/export      → download the current
//                                  GenerateACLForPlane("")
//                                  output as
//                                  <timestamp>-skygate-acl.json
//   GET  /admin/acls/import      → form to upload a file
//   POST /admin/acls/import      → parse + dry-run diff
//                                  (side-by-side old vs new)
//   POST /admin/acls/import/apply → push the imported
//                                  policy to every plane +
//                                  write an acl_snapshots row
//
// The dry-run page shows the imported policy in a <pre> next
// to the current one. Apply is a separate POST so a typo
// can't accidentally wipe a working policy. v0.13.0: import
// is single-policy-across-all-planes (the same JSON to
// every plane); per-plane import is a future v0.16.0 item.

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
	"skygate/internal/headscale"
)

// GetAdminACLsExport returns the current GenerateACLForPlane("")
// output as a downloadable JSON file. Operator downloads this
// to back up the policy or to seed a fresh install.
func (a *App) GetAdminACLsExport(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	policy, err := acl.GenerateACL(a.DB)
	if err != nil {
		http.Error(w, "generate acl: "+err.Error(), 500)
		return
	}
	// Filename: 2026-07-16-1345-skygate-acl.json (UTC) so a
	// fresh download doesn't overwrite an older one in the
	// operator's download folder.
	ts := time.Now().UTC().Format("2006-01-02-1504")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-skygate-acl.json"`, ts))
	_, _ = io.WriteString(w, policy)
}

// GetAdminACLsImport shows the import form (file upload +
// paste-textarea). Always GETs the current policy so the
// dry-run page can render side-by-side.
func (a *App) GetAdminACLsImport(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	currentPolicy, _ := acl.GenerateACL(a.DB)
	a.renderWithLayout(w, r, "admin/acls_import.html", c, map[string]any{
		"CurrentPolicy": currentPolicy,
	})
}

// PostAdminACLsImport parses the uploaded file (or pasted
// textarea) and renders the dry-run page. The "Apply" button
// lives on the same page; pressing it POSTs to
// /admin/acls/import/apply with the policy in a hidden
// field (so the operator doesn't have to re-paste the file).
func (a *App) PostAdminACLsImport(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "form parse: "+err.Error(), 400)
		return
	}
	// Two ways to provide the policy: file upload OR paste
	// textarea. The file takes precedence — the textarea is
	// the "I have a 1-line policy" path.
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
		http.Error(w, "empty policy (no file and no textarea)", 400)
		return
	}
	// Surface basic shape problems before the dry-run
	// page renders — a non-JSON blob doesn't get a diff
	// page, it gets a 400.
	if err := validateImportedACL(policy); err != nil {
		http.Error(w, "policy: "+err.Error(), 400)
		return
	}
	currentPolicy, _ := acl.GenerateACL(a.DB)
	// SHA-256 of each — for the dry-run page to show "this
	// is the same as the current policy" without doing a
	// full JSON compare.
	hCur := sha256.Sum256([]byte(currentPolicy))
	hImp := sha256.Sum256([]byte(policy))
	a.renderWithLayout(w, r, "admin/acls_import.html", c, map[string]any{
		"CurrentPolicy": currentPolicy,
		"ImportedPolicy": policy,
		"SameAsCurrent": hCur == hImp,
		"CurrentHash":   hex.EncodeToString(hCur[:8]),
		"ImportedHash":  hex.EncodeToString(hImp[:8]),
	})
}

// PostAdminACLsImportApply pushes the imported policy to
// every plane. The policy arrives in a hidden form field
// (set by the dry-run page), NOT in a file — the file was
// already parsed in the dry-run POST and the result lives
// only in the operator's browser until they hit Apply.
func (a *App) PostAdminACLsImportApply(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse: "+err.Error(), 400)
		return
	}
	policy := r.FormValue("policy")
	if err := validateImportedACL(policy); err != nil {
		http.Error(w, "policy: "+err.Error(), 400)
		return
	}
	var alerter acl.Alerter
	if a.Notifier != nil {
		alerter = a.Notifier
	}
	results := acl.SetACLForAllPlanes(a.DB,
		func(planeURL string) *headscale.Client {
			if planeURL == "" {
				return a.HSGlobal()
			}
			rows, err := a.DB.Query("SELECT id FROM portal_users WHERE headscale_url = $1 LIMIT 1", planeURL)
			if err != nil {
				return a.HSGlobal()
			}
			defer rows.Close()
			if !rows.Next() {
				return a.HSGlobal()
			}
			var uid int64
			if err := rows.Scan(&uid); err != nil {
				return a.HSGlobal()
			}
			return a.HSForUser(uid)
		},
		alerter,
		c.Username,
		fmt.Sprintf("ACL import by %s (per-plane)", c.Username),
		policy,
	)
	for _, r := range results {
		if r.Err != nil {
			http.Error(w, "set policy: "+r.Err.Error(), 500)
			return
		}
	}
	if a.Notifier != nil {
		go a.Notifier.SendAlert(fmt.Sprintf("📥 ACL imported by %s → %d plane(s)", c.Username, len(results)))
	}
	http.Redirect(w, r, "/admin/acls?imported=1", http.StatusSeeOther)
}

// validateImportedACL does a cheap shape check on the
// imported JSON: must parse, must be an object, must have
// the four top-level keys headscale 0.29 expects (acls,
// tagOwners, groups, ssh). It does NOT verify that the
// identities exist in the local DB — headscale will
// reject unknown identities in tagOwners, and the dry-run
// page lets the operator eyeball the structure before
// hitting Apply.
func validateImportedACL(policy string) error {
	policy = strings.TrimSpace(policy)
	if policy == "" {
		return fmt.Errorf("empty")
	}
	// Quick parse check (no full validation; headscale
	// does the real one on SetPolicy).
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

// jsonUnmarshal is a tiny indirection so the tests can
// mock it if needed (and to keep validateImportedACL
// free of direct encoding/json imports — same pattern
// as the rest of this file). The signature matches
// encoding/json.Unmarshal.
var jsonUnmarshal = json.Unmarshal

